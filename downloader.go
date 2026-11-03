// Ports qobuz_dl/downloader.py from qobuz-dl 2.5.9: Download, the naming
// helpers, the extras and the direct transfer. The segmented transfer comes
// with step 8; lyrics, booklet-only downloads, filter_label and
// playlist_as_albums are not ported.

package qobuzdl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// qlDowngrade is the restriction Qobuz sets on an answer below the requested
// format.
const qlDowngrade = "FormatRestrictedByFormatAvailability"

// nameMaxBytes is the longest file or folder name Linux and macOS take.
const nameMaxBytes = 255

// statePrefixBytes is the length of "[IN PROGRESS] ", the longest state prefix.
const statePrefixBytes = len("[IN PROGRESS] ")

// truncStrip is what smart truncation strips around the cut.
const truncStrip = ` ."-_'`

// fallbackTiers are the formats a track tries, best first.
var fallbackTiers = []int{27, 7, 6, 5}

var tierNames = map[int]string{27: "24-bit/>96kHz", 7: "24-bit/96kHz", 6: "16-bit/44.1kHz (CD)", 5: "MP3 320kbps"}

// artSizes are the sizes _get_extra puts into a cover URL.
var artSizes = []string{"50", "100", "150", "300", "600", "max", "org"}

// formatReleaseType is format_release_type: "Unknown" when empty, "EP" for
// any case of "ep", else str.title.
func formatReleaseType(rt any) (string, error) {
	if !pyTruthy(rt) {
		return "Unknown", nil
	}
	s, err := strForMethod(rt, "lower")
	if err != nil {
		return "", err
	}
	s = pyLower(s)
	if s == "ep" {
		return "EP", nil
	}
	return pyTitle(s), nil
}

// isNameTooLong is _is_name_too_long: whether the file system refuses name
// in parent as too long. It looks the name up in the nearest existing
// folder, so nothing is created.
func isNameTooLong(parent, name string) bool {
	for parent != "" && !isDir(parent) {
		up := pyDirname(parent)
		if up == parent {
			break
		}
		parent = up
	}
	_, err := os.Stat(pyPathJoin(parent, name))
	return errors.Is(err, syscall.ENAMETOOLONG)
}

// shortenUTF8 is _shorten_utf8: name cut in the middle with "..." until it
// fits in maxBytes bytes of UTF-8.
func shortenUTF8(name string, maxBytes int) string {
	if len(name) <= maxBytes {
		return name
	}
	rs := []rune(name)
	n := len(rs)
	for keep := n - 1; keep >= 0; keep-- {
		head := pyRstrip(string(rs[:(keep+1)/2]), truncStrip)
		tail := pyLstrip(string(rs[n-keep/2:]), truncStrip)
		if short := head + "..." + tail; len(short) <= maxBytes {
			return short
		}
	}
	return "..."
}

// fitName is _fit_name: name+suffix when the file system takes it, else name
// shortened 16 bytes at a time until it is taken or 64 bytes are left.
// reserve keeps room for a prefix added later.
func fitName(parent, name, suffix string, reserve int) string {
	if !isNameTooLong(parent, name+suffix) {
		return name + suffix
	}
	maxBytes := nameMaxBytes - reserve
	for {
		short := shortenUTF8(name, maxBytes-len(suffix))
		if maxBytes <= 64 || !isNameTooLong(parent, short+suffix) {
			return short + suffix
		}
		maxBytes -= 16
	}
}

// cutMiddle is smart truncation: the first head characters and the last
// tail, stripped toward the cut, around "...".
func cutMiddle(s string, head, tail int) string {
	return pyRstrip(pySlice(s, 0, head), truncStrip) + "..." + pyLstrip(pySlice(s, -tail, pyLen(s)), truncStrip)
}

// processFolderFormatWithSubdirs is process_folder_format_with_subdirs: each
// part of the template between slashes formatted, cleaned and cut to 120
// characters, empty parts dropped, and with path, each part fitted to the
// file system and the whole joined under path. A KeyError keeps the part's
// own text.
func processFolderFormatWithSubdirs(log *slog.Logger, format string, attrs pyAttrs, path *string, legacyCharmap, disableTruncation bool) (string, error) {
	var parts []string
	for _, part := range strings.Split(format, "/") {
		if part == "" {
			continue
		}
		formatted, err := pyFormat(part, attrs)
		if err != nil {
			if !isPy(err, pyKeyError) {
				return "", err
			}
			log.Warn(fmt.Sprintf("Format error (%s), using original text.", pyExceptionStr(err)))
			formatted = part
		}
		cleaned := sanitizeFilepath(CleanFilename(formatted, legacyCharmap), "_")
		if !disableTruncation && cleaned != "" && pyLen(cleaned) > 120 {
			cleaned = cutMiddle(cleaned, 60, 50)
		}
		if cleaned != "" {
			parts = append(parts, cleaned)
		}
	}
	if path != nil {
		parent := *path
		for i, part := range parts {
			parts[i] = fitName(parent, part, "", statePrefixBytes)
			parent = pyPathJoin(parent, parts[i])
		}
	}
	final := pyJoin(parts...)
	if path != nil {
		return pyPathJoin(*path, final), nil
	}
	return final, nil
}

// download is Download: one album or track download. Each call builds its
// own, with copies of the options, so the templates and disc settings it
// changes stay its own.
type download struct {
	client   *Client
	opts     Options
	log      *slog.Logger
	progress Progress

	itemID string
	path   string
	// isPlaylist and playlistTrackNumber are Download's is_playlist and
	// playlist_track_number; 0 is None.
	isPlaylist          bool
	playlistTrackNumber int

	folderFormat, trackFormat                                   string
	origFolderFormat, origTrackFormat, origMultiDiscTrackFormat string
}

// newDownload is Download.__init__. Empty templates fall back to
// DefaultFolder and DefaultTrack, as Python's `or` does.
func newDownload(c *Client, opts Options, itemID, path string, p Progress) *download {
	opts.Settings.VariousArtistsAliases = slices.Clone(opts.Settings.VariousArtistsAliases)
	opts.RetryDelays = slices.Clone(opts.RetryDelays)
	d := &download{
		client:   c,
		opts:     opts,
		progress: orNoProgress(p),
		itemID:   itemID,
		path:     path,
	}
	base := opts.Logger
	if base == nil {
		base = slog.New(slog.DiscardHandler)
	}
	d.log = slog.New(progressHandler{Handler: base.Handler(), p: d.progress})
	d.origFolderFormat = cmpOr(opts.FolderFormat, DefaultFolder)
	d.origTrackFormat = cmpOr(opts.TrackFormat, DefaultTrack)
	d.origMultiDiscTrackFormat = opts.Settings.MultipleDiscTrackFormat
	d.folderFormat, d.trackFormat = d.origFolderFormat, d.origTrackFormat
	return d
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (d *download) info(msg string) { d.log.Info(msg) }
func (d *download) warn(msg string) { d.log.Warn(msg) }

// stopped is the error a download returns when its context ends.
func stopped(ctx context.Context) error {
	return fmt.Errorf("qobuzdl: download stopped: %w", ctx.Err())
}

// sleepCtx sleeps d, or less when ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return stopped(ctx)
	}
}

// downloadIDByType is download_id_by_type: the templates and the disc
// template back to the ones the call started with, then the album or the
// track.
func (d *download) downloadIDByType(ctx context.Context, track bool) (*Release, error) {
	d.folderFormat = d.origFolderFormat
	d.trackFormat = d.origTrackFormat
	d.opts.Settings.MultipleDiscTrackFormat = d.origMultiDiscTrackFormat
	if !track {
		return d.downloadRelease(ctx)
	}
	return d.downloadTrack(ctx)
}

// failRelease marks rel failed with err and returns both.
func failRelease(rel *Release, err error) (*Release, error) {
	rel.Status = StatusFailed
	rel.Err = err
	return rel, err
}

// setGate fills a release's quality fields from the gate.
func (rel *Release) setGate(format string, qualityMet bool, bitDepth, samplingRate any) {
	rel.Format = format
	rel.QualityMet = qualityMet
	rel.BitDepth = int(asFloat(bitDepth))
	rel.SamplingRate = asFloat(samplingRate)
}

// asFloat reads a number leniently, for the typed results.
func asFloat(v any) float64 {
	i, f, isFloat, ok := pyNumber(v)
	switch {
	case !ok:
		return 0
	case isFloat:
		return f
	}
	return float64(i)
}

// pyStrPtr is str(v), or nil for None.
func pyStrPtr(v any) *string {
	if v == nil {
		return nil
	}
	s := pyStr(v)
	return &s
}

// downloadRelease is download_release.
func (d *download) downloadRelease(ctx context.Context) (*Release, error) {
	rel := &Release{Kind: "album", ID: d.itemID}
	s := &d.opts.Settings
	albumMeta, err := d.client.albumMeta(ctx, d.itemID)
	if err != nil {
		return failRelease(rel, err)
	}
	rel.Album, _ = albumFromDict(albumMeta)

	if !pyTruthy(albumMeta.get("streamable", nil)) {
		return failRelease(rel, fmt.Errorf("%w: This release is not streamable", ErrNonStreamable))
	}
	if d.opts.IgnoreSinglesEPs {
		skip := !pyEqual(albumMeta.get("release_type", nil), "album")
		if !skip {
			artist, err := asDict(albumMeta.get("artist", nil))
			if err != nil {
				return failRelease(rel, err)
			}
			skip = pyEqual(artist.get("name", nil), "Various Artists")
		}
		if skip {
			d.info("Ignoring Single/EP/VA: " + pyStr(albumMeta.get("title", "n/a")))
			rel.Status, rel.Skip = StatusSkipped, SkipAlbumsOnly
			return rel, nil
		}
	}

	albumTitle, err := downloaderGetTitle(albumMeta)
	if err != nil {
		return failRelease(rel, err)
	}
	url := albumMeta.get("url", "")
	releaseDate := albumMeta.get("release_date_original", "")

	fileFormat, qualityMet, bitDepth, samplingRate, err := d.getFormat(ctx, albumMeta, false, nil)
	if err != nil {
		return failRelease(rel, err)
	}
	rel.setGate(fileFormat, qualityMet, bitDepth, samplingRate)
	if !d.opts.QualityFallback && !qualityMet {
		d.info(fmt.Sprintf("Skipping %s as it doesn't meet quality requirement", pyStr(albumTitle)))
		rel.Status, rel.Skip = StatusSkipped, SkipQuality
		return rel, nil
	}
	d.info(fmt.Sprintf("Downloading: %s\nQuality: %s (%s/%s)", pyStr(albumTitle), fileFormat, pyStr(bitDepth), pyStr(samplingRate)))

	albumAttr, err := getAlbumAttr(d.log, albumMeta, albumTitle, fileFormat, bitDepth, samplingRate)
	if err != nil {
		return failRelease(rel, err)
	}
	tracksMeta, err := tracksItems(albumMeta)
	if err != nil {
		return failRelease(rel, err)
	}
	if err := d.determineFormats(albumMeta, albumAttr, tracksMeta, nil, false, fileFormat); err != nil {
		return failRelease(rel, err)
	}
	targetDirn, err := processFolderFormatWithSubdirs(d.log, d.folderFormat, albumAttr, &d.path, s.LegacyCharmap, s.DisableSmartTruncation)
	if err != nil {
		return failRelease(rel, err)
	}
	basePath, folderName := pySplit(targetDirn)
	incompleteDirn := pyPathJoin(basePath, "[INCOMPLETE] "+folderName)
	inprogressDirn := pyPathJoin(basePath, "[IN PROGRESS] "+folderName)

	// A folder name that only fits without the prefix is used as it is, the
	// same way as when renaming the folder fails.
	isStandardAlbum := !d.isPlaylist
	if isStandardAlbum && isNameTooLong(basePath, posixBasename(inprogressDirn)) {
		isStandardAlbum = false
	}
	workingDirn := targetDirn
	if isStandardAlbum {
		workingDirn = inprogressDirn
		var err error
		switch {
		case pathExists(incompleteDirn):
			err = os.Rename(incompleteDirn, workingDirn)
		case pathExists(targetDirn):
			err = os.Rename(targetDirn, workingDirn)
		}
		if err != nil {
			d.warn(fmt.Sprintf("[!] Could not rename existing folder to [IN PROGRESS]. Operating in standard mode. (%v)", err))
			workingDirn = targetDirn
		}
	}
	if err := makeDirs(workingDirn); err != nil {
		return failRelease(rel, err)
	}
	rel.Folder = workingDirn
	dirn := workingDirn

	isMultiple, err := pyLess(int64(1), albumMeta.get("media_count", int64(1)))
	if err != nil {
		return failRelease(rel, err)
	}
	activeWorkers := max(s.MaxWorkers, 1)
	switch {
	case s.Delay > 0:
		d.info("[*] Safety Delay active: Multithreading disabled (Sequential mode).")
		activeWorkers = 1
	case activeWorkers > 1:
		d.info(fmt.Sprintf("[*] Multithreading Enabled (%d workers): UI optimized for clean parallel logging.", activeWorkers))
	}

	d.progress.ReleaseStart(ReleaseInfo{
		Kind: "album", ID: d.itemID, Title: pyStr(albumTitle), Folder: workingDirn,
		Album: rel.Album, Tracks: plannedTracks(tracksMeta),
	})

	results, failedTracks, err := d.releaseBody(ctx, albumMeta, dirn, albumTitle, fileFormat, bitDepth, samplingRate, tracksMeta, isMultiple, activeWorkers)
	rel.Tracks = results
	if err != nil {
		// Python's uncaught exception leaves the folder as it is.
		return failRelease(rel, err)
	}
	abortedByUser := ctx.Err() != nil
	if !abortedByUser {
		cleanEmbedArt(dirn)
	}

	finalDirn := workingDirn
	if isStandardAlbum && workingDirn == inprogressDirn {
		finalDirn = incompleteDirn
		if failedTracks == 0 && !abortedByUser {
			finalDirn = targetDirn
		}
		if err := os.Rename(workingDirn, finalDirn); err != nil {
			d.warn(fmt.Sprintf("[!] Could not rename final folder state (OS Lock might still be active). (%v)", err))
			finalDirn = workingDirn
		}
		switch {
		case abortedByUser:
			d.warn("[!] Download aborted. Folder successfully marked as [INCOMPLETE].")
		case failedTracks > 0:
			d.warn(fmt.Sprintf("[!] Album downloaded partially (%d tracks skipped). Folder marked as [INCOMPLETE].", failedTracks))
		}
	}
	rel.Folder = finalDirn
	for i := range rel.Tracks {
		rel.Tracks[i].Path = rebase(rel.Tracks[i].Path, workingDirn, finalDirn)
	}

	nFailed := 0
	for _, t := range rel.Tracks {
		if t.Outcome == OutcomeFailed {
			nFailed++
		}
	}
	switch {
	case nFailed == 0 && !abortedByUser:
		rel.Status = StatusDone
	case nFailed == len(rel.Tracks):
		rel.Status = StatusFailed
	default:
		rel.Status = StatusPartial
	}
	if abortedByUser {
		rel.Err = stopped(ctx)
		return rel, rel.Err
	}
	// An incomplete album stays out of the database, so the next run retries
	// its missing tracks.
	if failedTracks > 0 {
		rel.Err = fmt.Errorf("qobuzdl: %d of %d tracks failed", failedTracks, len(rel.Tracks))
		d.warn(fmt.Sprintf("[!] Not recorded in the database: %d tracks failed. Run again to retry them.", failedTracks))
		return rel, nil
	}
	d.record(ctx, DownloadRecord{
		ID: d.itemID, MediaType: "album", Quality: d.opts.Quality, FileFormat: fileFormat, QualityMet: qualityMet,
		BitDepth: pyStrPtr(bitDepth), SamplingRate: pyStrPtr(samplingRate), SavedPath: finalDirn, Status: "downloaded",
		URL: pyStr(url), ReleaseDate: pyStr(releaseDate),
		Artist: pyStr(attrOr(albumAttr, "album_artist")), Album: pyStr(attrOr(albumAttr, "album_title")),
	})
	d.info("Completed")
	return rel, nil
}

// attrOr is attrs.get(key, "Unknown").
func attrOr(attrs pyAttrs, key string) any {
	if v, ok := attrs[key]; ok {
		return v
	}
	return "Unknown"
}

// record is handle_download_id with add_id. A failure is logged, as Python
// logs it, and leaves the release done.
func (d *download) record(ctx context.Context, rec DownloadRecord) {
	if d.opts.DownloadsDB == nil {
		return
	}
	if err := d.opts.DownloadsDB.Add(ctx, rec); err != nil {
		d.log.Error("Unexpected DB error: " + err.Error())
	}
}

// rebase moves p from the folder old to the folder now.
func rebase(p, old, now string) string {
	if p == "" || old == now {
		return p
	}
	if rest, ok := strings.CutPrefix(p, old+"/"); ok {
		return pyPathJoin(now, rest)
	}
	return p
}

// tracksItems is album_meta["tracks"]["items"] iterated.
func tracksItems(album *pyDict) ([]any, error) {
	tracks, err := album.item("tracks")
	if err != nil {
		return nil, err
	}
	items, err := pyGetItem(tracks, "items")
	if err != nil {
		return nil, err
	}
	return pyIter(items)
}

// plannedTracks lists an album's tracks for ReleaseStart.
func plannedTracks(items []any) []PlannedTrack {
	out := make([]PlannedTrack, len(items))
	for i, it := range items {
		t, _ := it.(*pyDict)
		if t == nil {
			t = newDict()
		}
		out[i] = PlannedTrack{
			Position: i + 1, ID: viewStr(t, "id"), Title: viewStr(t, "title"), ISRC: viewStr(t, "isrc"),
			DiscNumber: viewInt(t, "media_number"), TrackNumber: viewInt(t, "track_number"), Duration: viewInt(t, "duration"),
		}
	}
	return out
}

// newTrackResult starts a track's result from its dict.
func newTrackResult(t *pyDict, pos int) TrackResult {
	if t == nil {
		t = newDict()
	}
	return TrackResult{
		Position: pos, ID: viewStr(t, "id"), Title: viewStr(t, "title"), ISRC: viewStr(t, "isrc"),
		DiscNumber: viewInt(t, "media_number"), TrackNumber: viewInt(t, "track_number"),
	}
}

// releaseBody is the try block of download_release: the extras, then the
// tracks through the worker pool. It returns every planned track's result
// and how many failed. An error is an exception Python doesn't catch.
func (d *download) releaseBody(ctx context.Context, albumMeta *pyDict, dirn string, albumTitle any, fileFormat string,
	bitDepth, samplingRate any, items []any, isMultiple bool, workers int) ([]TrackResult, int, error) {
	s := &d.opts.Settings
	results := make([]TrackResult, len(items))
	for i, it := range items {
		t, _ := it.(*pyDict)
		results[i] = newTrackResult(t, i+1)
	}

	if err := d.generateTracklist(ctx, albumMeta, dirn, albumTitle, fileFormat, bitDepth, samplingRate); err != nil {
		return results, 0, err
	}
	large := func() (any, error) {
		image, err := albumMeta.item("image")
		if err != nil {
			return nil, err
		}
		return pyGetItem(image, "large")
	}
	if s.NoCover {
		d.info("Skipping cover")
	} else {
		u, err := large()
		if err != nil {
			return results, 0, err
		}
		if err := d.getExtra(ctx, u, dirn, "cover.jpg", s.SavedArtSize); err != nil {
			return results, 0, err
		}
	}
	if s.EmbedArt {
		u, err := large()
		if err != nil {
			return results, 0, err
		}
		if err := d.getExtra(ctx, u, dirn, embCoverName, s.EmbeddedArtSize); err != nil {
			return results, 0, err
		}
	}
	if albumMeta.has("goodies") {
		d.downloadGoodies(ctx, albumMeta, dirn)
	}

	// ThreadPoolExecutor runs jobs in the order they were submitted; a
	// queue read by the workers keeps that order.
	queue := make(chan int, len(items))
	errs := make([]error, len(items))
	submitted := make([]bool, len(items))
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range queue {
				t := items[i].(*pyDict)
				var multiple any
				if isMultiple {
					multiple = t.get("media_number", nil)
				}
				results[i], errs[i] = d.downloadAndTag(ctx, dirn, i, t, albumMeta, false, multiple, i+1)
				d.progress.TrackEnd(results[i])
			}
		})
	}

	failedTracks := 0
	trackFailed := func(i int, err error) {
		results[i].Outcome, results[i].Err = OutcomeFailed, err
		d.progress.TrackEnd(results[i])
		failedTracks++
	}
	loopErr := func() error {
		for i, it := range items {
			if ctx.Err() != nil {
				break
			}
			t, err := asDict(it)
			if err != nil {
				return err
			}
			id, err := t.item("id")
			if err != nil {
				return err
			}
			parse, err := d.client.getTrackURL(ctx, pyStr(id), d.opts.Quality, false)
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				d.warn(fmt.Sprintf("[!] API Error for track %s (ID: %s): %s", pyStr(t.get("track_number", "unknown")), pyStr(id), pyExceptionStr(err)))
				d.warn("[*] Skipping track and continuing with the album...")
				trackFailed(i, err)
				continue
			}
			if !pyTruthy(parse) {
				d.info("Demo. Skipping")
				trackFailed(i, errDemo)
				continue
			}
			submitted[i] = true
			queue <- i
		}
		return nil
	}()
	close(queue)
	wg.Wait()
	if loopErr != nil {
		return results, failedTracks, loopErr
	}

	for i := range items {
		switch {
		case !submitted[i] && results[i].Outcome == 0:
			// The context ended before the main loop got to the track.
			results[i].Outcome, results[i].Err = OutcomeFailed, stopped(ctx)
			d.progress.TrackEnd(results[i])
		case !submitted[i]:
		case ctx.Err() != nil:
			// Python stops collecting results when aborted.
		case errs[i] != nil:
			d.warn(fmt.Sprintf("[!] Track download failed: %s", pyExceptionStr(errs[i])))
			failedTracks++
		case results[i].Outcome == OutcomeFailed:
			failedTracks++
		}
	}
	return results, failedTracks, nil
}

// errDemo is a track whose getFileUrl answer was empty.
var errDemo = errors.New("qobuzdl: demo track: getFileUrl answered nothing")

// errAllTiers is a track no tier could deliver.
var errAllTiers = errors.New("qobuzdl: track discarded after all downgrades")

// downloadTrack is download_track.
func (d *download) downloadTrack(ctx context.Context) (*Release, error) {
	rel := &Release{Kind: "track", ID: d.itemID}
	s := &d.opts.Settings
	parse, err := d.client.getTrackURL(ctx, d.itemID, d.opts.Quality, false)
	if err != nil {
		return failRelease(rel, err)
	}
	if !pyTruthy(parse) {
		d.info("Demo. Skipping")
		rel.Status, rel.Skip = StatusSkipped, SkipDemo
		d.info("Completed")
		return rel, nil
	}
	trackMeta, err := d.client.trackMeta(ctx, d.itemID)
	if err != nil {
		return failRelease(rel, err)
	}
	pos := 1
	if d.isPlaylist && d.playlistTrackNumber != 0 {
		trackMeta.set("track_number", int64(d.playlistTrackNumber))
		pos = d.playlistTrackNumber
	}
	if tv, err := trackFromDict(trackMeta); err == nil {
		rel.Track, rel.Album = tv, tv.Album
	}

	trackTitle, err := downloaderGetTitle(trackMeta)
	if err != nil {
		return failRelease(rel, err)
	}
	artist, err := safeGet(trackMeta, nil, "performer", "name")
	if err != nil {
		return failRelease(rel, err)
	}
	d.info(fmt.Sprintf("Downloading: %s - %s", pyStr(artist), pyStr(trackTitle)))

	album, err := trackMeta.dict("album")
	if err != nil {
		return failRelease(rel, err)
	}
	url := album.get("url", "")
	releaseDate := trackMeta.get("release_date_original", "")
	fileFormat, qualityMet, bitDepth, samplingRate, err := d.getFormat(ctx, trackMeta, true, parse)
	if err != nil {
		return failRelease(rel, err)
	}
	rel.setGate(fileFormat, qualityMet, bitDepth, samplingRate)
	if !d.opts.QualityFallback && !qualityMet {
		d.info(fmt.Sprintf("Skipping %s as it doesn't meet quality requirement", pyStr(trackTitle)))
		rel.Status, rel.Skip = StatusSkipped, SkipQuality
		return rel, nil
	}
	trackAttr, err := getTrackAttr(d.log, trackMeta, trackTitle, bitDepth, samplingRate, fileFormat)
	if err != nil {
		return failRelease(rel, err)
	}
	if err := d.determineFormats(trackMeta.get("album", newDict()), nil, []any{trackMeta}, trackAttr, true, fileFormat); err != nil {
		return failRelease(rel, err)
	}
	dirn, err := processFolderFormatWithSubdirs(d.log, d.folderFormat, trackAttr, &d.path, s.LegacyCharmap, s.DisableSmartTruncation)
	if err != nil {
		return failRelease(rel, err)
	}
	if err := makeDirs(dirn); err != nil {
		return failRelease(rel, err)
	}
	rel.Folder = dirn
	if !d.isPlaylist {
		planned := plannedTracks([]any{trackMeta})
		planned[0].Position = pos
		d.progress.ReleaseStart(ReleaseInfo{Kind: "track", ID: d.itemID, Title: pyStr(trackTitle), Folder: dirn, Album: rel.Album, Tracks: planned})
	}

	large := func() (any, error) {
		a, err := trackMeta.item("album")
		if err != nil {
			return nil, err
		}
		image, err := pyGetItem(a, "image")
		if err != nil {
			return nil, err
		}
		return pyGetItem(image, "large")
	}
	switch {
	case d.isPlaylist:
		d.info("Skipping standard cover save to keep playlist folder clean")
	case s.NoCover:
		d.info("Skipping cover")
	default:
		u, err := large()
		if err != nil {
			return failRelease(rel, err)
		}
		if err := d.getExtra(ctx, u, dirn, "cover.jpg", s.SavedArtSize); err != nil {
			return failRelease(rel, err)
		}
	}
	if s.EmbedArt {
		_ = os.Remove(pyPathJoin(dirn, embCoverName))
		u, err := large()
		if err != nil {
			return failRelease(rel, err)
		}
		if err := d.getExtra(ctx, u, dirn, embCoverName, s.EmbeddedArtSize); err != nil {
			return failRelease(rel, err)
		}
	} else {
		d.info("Skipping embedded art")
	}

	tr, err := d.downloadAndTag(ctx, dirn, 1, trackMeta, trackMeta, true, false, pos)
	rel.Tracks = []TrackResult{tr}
	d.progress.TrackEnd(tr)
	if err != nil {
		return failRelease(rel, err)
	}
	cleanEmbedArt(dirn)

	if ctx.Err() != nil {
		rel.Status, rel.Err = StatusFailed, stopped(ctx)
		return rel, rel.Err
	}
	// A failed track stays out of the database, so the next run retries it.
	if tr.Outcome == OutcomeFailed {
		rel.Status, rel.Err = StatusFailed, tr.Err
		d.warn(fmt.Sprintf("[!] Not recorded in the database: '%s' failed. Run again to retry it.", pyStr(trackTitle)))
		return rel, nil
	}
	rel.Status = StatusDone
	d.record(ctx, DownloadRecord{
		ID: d.itemID, MediaType: "track", Quality: d.opts.Quality, FileFormat: fileFormat, QualityMet: qualityMet,
		BitDepth: pyStrPtr(bitDepth), SamplingRate: pyStrPtr(samplingRate), SavedPath: dirn, Status: "downloaded",
		URL: pyStr(url), ReleaseDate: pyStr(releaseDate),
		Artist: pyStr(attrOr(trackAttr, "artist")), Album: pyStr(attrOr(trackAttr, "album")),
	})
	d.info("Completed")
	return rel, nil
}

// discFolder is "<multiple_disc_prefix> <NN>" for a disc number: int() of
// it, or 1 for a bool or what int() refuses.
func (d *download) discFolder(multiple any) (string, error) {
	dNum := int64(1)
	if _, isBool := multiple.(bool); !isBool {
		n, err := pyInt(multiple)
		switch {
		case err == nil:
			dNum = n
		case !isPy(err, pyValueError, pyTypeError):
			return "", err
		}
	}
	nn, err := pyFormatValue(dNum, "02")
	if err != nil {
		return "", err
	}
	return d.opts.Settings.MultipleDiscPrefix + " " + nn, nil
}

// fileName formats a template for a track file: str.format, clean_filename
// and sanitize_filename with "_".
func (d *download) fileName(tmpl string, attrs pyAttrs) (string, error) {
	formatted, err := pyFormat(tmpl, attrs)
	if err != nil {
		return "", err
	}
	return sanitizeFilename(CleanFilename(formatted, d.opts.Settings.LegacyCharmap), "_"), nil
}

// trackPath is the naming part of _download_and_tag: the final file of a
// track under rootDir, disc folder included, cut to 180 characters and
// fitted to the file system.
func (d *download) trackPath(rootDir string, track, albumOrTrack *pyDict, isTrack bool, multiple any, ext string) (string, error) {
	s := &d.opts.Settings
	trackArtist, err := safeGet(track, nil, "performer", "name")
	if err != nil {
		return "", err
	}
	var album any = albumOrTrack
	if isTrack {
		album = albumOrTrack.get("album", newDict())
	}
	albumD, err := asDict(album)
	if err != nil {
		d.log.Error("Error getting album artist: " + pyExceptionStr(err))
		return "", err
	}
	attrs, err := getFilenameAttr(d.log, trackArtist, track, albumD)
	if err != nil {
		return "", err
	}

	var formatted string
	switch {
	case d.isPlaylist:
		formatted, err = d.fileName("{artist} - {track_title}", attrs)
	case pyTruthy(multiple) && s.MultipleDiscOneDir:
		formatted, err = d.fileName(s.MultipleDiscTrackFormat, attrs)
	default:
		formatted, err = d.fileName(d.trackFormat, attrs)
		if err != nil {
			return "", err
		}
		if pyTruthy(multiple) {
			more, err := pyLess(int64(1), albumOrTrack.get("media_count", int64(1)))
			if err != nil {
				return "", err
			}
			if more {
				folder, err := d.discFolder(multiple)
				if err != nil {
					return "", err
				}
				formatted = pyPathJoin(folder, formatted)
			}
		}
	}
	if err != nil {
		return "", err
	}
	if !s.DisableSmartTruncation && pyLen(formatted) > OKMaxCharacterLength {
		formatted = cutMiddle(formatted, 110, 60)
	}
	// The file name is shortened further only if the file system refuses it.
	finalDir, finalName := pySplit(pyPathJoin(rootDir, formatted))
	return pyPathJoin(finalDir, fitName(finalDir, finalName, ext, 0)), nil
}

// downloadAndTag is _download_and_tag: the track's file name, the tiers from
// the requested format down, the transfer and the tags. The error is an
// exception Python doesn't catch there; every other failure is in the
// result. A failed track leaves no temp file.
func (d *download) downloadAndTag(ctx context.Context, rootDir string, tmpCount int, track, albumOrTrack *pyDict,
	isTrack bool, multiple any, pos int) (tr TrackResult, err error) {
	s := &d.opts.Settings
	quality := d.opts.Quality
	tr = newTrackResult(track, pos)
	fail := func(err error) (TrackResult, error) {
		tr.Outcome, tr.Err = OutcomeFailed, err
		return tr, nil
	}
	escape := func(err error) (TrackResult, error) {
		tr.Outcome, tr.Err = OutcomeFailed, err
		return tr, err
	}
	ext := ".flac"
	if quality == 5 {
		ext = ".mp3"
	}
	finalFile, err := d.trackPath(rootDir, track, albumOrTrack, isTrack, multiple, ext)
	if err != nil {
		return escape(err)
	}
	if pathExists(finalFile) {
		d.info(fmt.Sprintf("[*] Skipping: %s (Already exists)", posixBasename(finalFile)))
		tr.Outcome, tr.Path = OutcomeSkipped, finalFile
		existingFormat(&tr)
		return tr, nil
	}
	if ctx.Err() != nil {
		return fail(stopped(ctx))
	}
	if err := sleepCtx(ctx, d.opts.TrackPause); err != nil {
		return fail(err)
	}

	if pyTruthy(multiple) {
		more, err := pyLess(int64(1), albumOrTrack.get("media_count", int64(1)))
		if err != nil {
			return escape(err)
		}
		if more && !s.MultipleDiscOneDir {
			folder, err := d.discFolder(multiple)
			if err != nil {
				return escape(err)
			}
			rootDir = pyPathJoin(rootDir, folder)
		}
	}
	if !pathExists(rootDir) {
		if err := makeDirs(rootDir); err != nil {
			return escape(err)
		}
	}

	filename := pyPathJoin(rootDir, fmt.Sprintf("~tmp_%02d.tmp", tmpCount))
	defer func() {
		if tr.Outcome != OutcomeDone {
			_ = os.Remove(filename)
			_ = os.Remove(filename + ".mp4")
		}
	}()
	trackNo := pyZfill(pyStr(track.get("track_number", int64(0))), 2)
	desc := trackNo + ". " + pyStr(track.get("title", nil))

	startIdx := max(slices.Index(fallbackTiers, quality), 0)
	// With fallback off a track is saved in the requested quality or not at all.
	qualities := []int{quality}
	if d.opts.QualityFallback {
		qualities = fallbackTiers[startIdx:]
	}
	var fresh *pyDict
	success := false
	finalFmt := quality
	for _, attempt := range qualities {
		if ctx.Err() != nil {
			return fail(stopped(ctx))
		}
		if attempt != quality {
			d.warn(fmt.Sprintf("[!] Automatic downgrade: Attempting to save in %s...", tierNames[attempt]))
		}
		answer, ok := d.tryTier(ctx, track, attempt, filename, desc, pos)
		if answer != nil {
			fresh = answer
		}
		if ok {
			success, finalFmt = true, attempt
			break
		}
		if ctx.Err() != nil {
			return fail(stopped(ctx))
		}
	}
	if !success {
		d.warn(fmt.Sprintf("[!] TRACK %s DEFINITIVELY DISCARDED AFTER ALL DOWNGRADES.", trackNo))
		d.warn("[!] Skipping to the next track...")
		return fail(errAllTiers)
	}

	// The format the server delivered, such as MP3 after falling back from
	// lossless, decides the extension and the tagger.
	if v := fresh.get("format_id", nil); pyTruthy(v) {
		n, err := pyInt(v)
		switch {
		case err == nil:
			finalFmt = int(n)
		case !isPy(err, pyTypeError, pyValueError):
			return escape(err)
		}
	}
	tagger, codec := TagFLAC, "FLAC"
	ext = ".flac"
	if finalFmt == 5 {
		tagger, codec, ext = TagMP3, "MP3", ".mp3"
	}
	root, _ := pySplitext(finalFile)
	finalFile = root + ext
	if err := tagger(d.log, filename, rootDir, finalFile, track, albumOrTrack, isTrack, d.opts.EmbedArt, s); err != nil {
		d.log.Error("[!] Error tagging: " + pyExceptionStr(err))
		return fail(fmt.Errorf("qobuzdl: tagging %s: %w", posixBasename(finalFile), err))
	}

	if s.Delay > 0 && ctx.Err() == nil {
		d.info(fmt.Sprintf("[*] Sleeping for %s seconds to prevent rate limiting...", seconds(s.Delay)))
		_ = sleepCtx(ctx, s.Delay)
	}
	tr.Outcome, tr.Path = OutcomeDone, finalFile
	tr.FormatID, tr.Codec = finalFmt, codec
	tr.BitDepth = int(asFloat(fresh.get("bit_depth", nil)))
	tr.SamplingRate = asFloat(fresh.get("sampling_rate", nil))
	return tr, nil
}

// tryTier is one pass of the tier loop: a fresh URL, then the direct
// transfer, then the segmented one. Python catches every exception here,
// so a failure only moves on to the next tier. answer is the last
// getFileUrl answer, which decides the delivered format.
func (d *download) tryTier(ctx context.Context, track *pyDict, fmtID int, filename, desc string, pos int) (answer *pyDict, ok bool) {
	id, err := track.item("id")
	if err != nil {
		return nil, false
	}
	d.log.Debug("tier", "track", pyStr(id), "format", fmtID)
	answer, err = d.client.getTrackURL(ctx, pyStr(id), fmtID, false)
	if err != nil {
		d.log.Debug("tier failed", "track", pyStr(id), "format", fmtID, "error", err)
		return nil, false
	}
	// A sample at this tier moves on to the next one.
	if pyTruthy(answer.get("sample", false)) {
		return answer, false
	}
	title := pyStr(track.get("title", nil))
	if answer.has("url") {
		d.progress.TrackStart(TrackInfo{Position: pos, ID: pyStr(id), Title: title, FormatID: fmtID})
		d.info("[+] In progress: " + desc)
		meter := newByteMeter(d.progress, pos)
		err := d.transfer(ctx, pyStr(answer.get("url", nil)), filename, meter.update)
		meter.finish()
		if err == nil {
			d.info("  L Completed: " + desc)
			return answer, true
		}
		if ctx.Err() != nil {
			return answer, false
		}
		d.warn("[!] Akamai block detected. Activating fallback segmented download...")
		answer, err = d.client.getTrackURL(ctx, pyStr(id), fmtID, true)
		if err != nil {
			d.log.Debug("tier failed", "track", pyStr(id), "format", fmtID, "error", err)
			return nil, false
		}
	}
	if answer.has("url_template") {
		d.progress.TrackStart(TrackInfo{Position: pos, ID: pyStr(id), Title: title, FormatID: fmtID, Segmented: true})
		meter := newByteMeter(d.progress, pos)
		err := d.transferSegments(ctx, answer, filename, meter.update)
		meter.finish()
		if err != nil {
			d.log.Debug("tier failed", "track", pyStr(id), "format", fmtID, "error", err)
			return answer, false
		}
		return answer, true
	}
	return answer, false
}

// transferSegments is tqdm_download_segments, which step 8 ports.
func (d *download) transferSegments(_ context.Context, _ *pyDict, _ string, _ func(done, total int64)) error {
	return errSegmentedNotPorted
}

// existingFormat fills a skipped track's format from its file: STREAMINFO
// for a FLAC, format 5 for an MP3.
func existingFormat(tr *TrackResult) {
	if strings.HasSuffix(tr.Path, ".mp3") {
		tr.FormatID, tr.Codec = 5, "MP3"
		return
	}
	bitDepth, rate, _, err := flacStreamInfo(tr.Path)
	if err != nil {
		return
	}
	tr.Codec, tr.BitDepth, tr.SamplingRate = "FLAC", bitDepth, float64(rate)/1000
	switch {
	case bitDepth <= 16:
		tr.FormatID = 6
	case rate > 96000:
		tr.FormatID = 27
	default:
		tr.FormatID = 7
	}
}

// seconds prints a duration in seconds as Python prints the setting.
func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
}

// flattenArtists is _flatten_artists on get_album_artist's list.
func flattenArtists(names []any) (string, error) {
	parts, err := pyJoinItems(names)
	if err != nil {
		return "", err
	}
	return strings.Join(parts, ", "), nil
}

// reLabelSep is
//
//	\s*[\;\/]\s*|\s+\-\s+
//
// with Python's Unicode \s.
var reLabelSep = regexp.MustCompile(reS + `*[;/]` + reS + `*|` + reS + `+-` + reS + `+`)

// labelAttr is the label attribute: label.name with whitespace runs
// collapsed, separators replaced by sep, stripped.
func labelAttr(meta *pyDict, sep string) (string, error) {
	label, err := meta.dict("label")
	if err != nil {
		return "", err
	}
	name, err := strForMethod(label.get("name", ""), "split")
	if err != nil {
		return "", err
	}
	joined := strings.Join(strings.FieldsFunc(name, pyIsSpace), " ")
	return pyStrip(reLabelSep.ReplaceAllString(joined, sep), ""), nil
}

// yearOf is d.get(key, "").split("-")[0].
func yearOf(d *pyDict, key string) (string, error) {
	s, err := strForMethod(d.get(key, ""), "split")
	if err != nil {
		return "", err
	}
	year, _, _ := strings.Cut(s, "-")
	return year, nil
}

// subGet is d.get(key, {}).get(sub, def).
func subGet(d *pyDict, key, sub string, def any) (any, error) {
	inner, err := d.dict(key)
	if err != nil {
		return nil, err
	}
	return inner.get(sub, def), nil
}

// explicitFlag is "[E]" when v is truthy.
func explicitFlag(v any) string {
	if pyTruthy(v) {
		return "[E]"
	}
	return ""
}

// qualityTag is the quality_tag attribute.
func qualityTag(fileFormat string, bitDepth any) string {
	switch {
	case pyUpper(fileFormat) == "MP3":
		return "MP3"
	case pyTruthy(bitDepth):
		return fileFormat + " " + pyStr(bitDepth)
	}
	return fileFormat
}

// versionTag is " - <version>" when the version is truthy.
func versionTag(v any) string {
	if pyTruthy(v) {
		return " - " + pyStr(v)
	}
	return ""
}

// attrBuilder sets pyAttrs in Python's evaluation order and keeps the first
// error, as a dict literal raises at its first failing value.
type attrBuilder struct {
	attrs pyAttrs
	err   error
}

// add sets key from a fallible value: b.add("year")(yearOf(...)).
func (b *attrBuilder) add(key string) func(v any, err error) {
	return func(v any, err error) {
		if b.err != nil {
			return
		}
		if err != nil {
			b.err = err
			return
		}
		b.attrs[key] = v
	}
}

func (b *attrBuilder) val(key string, v any) { b.add(key)(v, nil) }

// getAlbumAttr is _get_album_attr: the attributes of an album's folder name.
func getAlbumAttr(log *slog.Logger, meta *pyDict, albumTitle any, fileFormat string, bitDepth, samplingRate any) (pyAttrs, error) {
	names, err := getAlbumArtist(log, meta)
	if err != nil {
		return nil, err
	}
	albumArtist, err := flattenArtists(names)
	if err != nil {
		return nil, err
	}
	b := &attrBuilder{attrs: pyAttrs{}}
	b.add("artist")(subGet(meta, "artist", "name", ""))
	b.val("album", albumTitle)
	b.val("album_id", meta.get("id", ""))
	b.val("album_url", meta.get("url", ""))
	b.val("album_title", albumTitle)
	b.val("album_title_base", meta.get("title", ""))
	b.val("album_artist", albumArtist)
	b.val("albumartist", albumArtist)
	b.add("album_genre")(subGet(meta, "genre", "name", ""))
	b.add("album_composer")(subGet(meta, "composer", "name", ""))
	b.add("label")(anyOf(labelAttr(meta, " ∕ ")))
	b.val("copyright", meta.get("copyright", ""))
	b.val("upc", meta.get("upc", ""))
	b.val("barcode", meta.get("upc", ""))
	b.val("release_date", meta.get("release_date_original", ""))
	b.add("year")(anyOf(yearOf(meta, "release_date_original")))
	b.add("media_type")(anyOf(capitalizeOf(meta, "product_type")))
	b.val("format", fileFormat)
	b.val("bit_depth", bitDepth)
	b.val("sampling_rate", samplingRate)
	b.val("quality_tag", qualityTag(fileFormat, bitDepth))
	b.val("album_version", meta.get("version", ""))
	b.val("version_tag", versionTag(meta.get("version", nil)))
	b.val("disc_count", meta.get("media_count", int64(1)))
	b.val("track_count", meta.get("track_count", int64(1)))
	b.val("ExplicitFlag", explicitFlag(meta.get("parental_warning", nil)))
	b.val("explicit", explicitFlag(meta.get("parental_warning", nil)))
	b.add("release_type")(anyOf(formatReleaseType(meta.get("release_type", nil))))
	return b.attrs, b.err
}

func anyOf(s string, err error) (any, error) { return s, err }

// capitalizeOf is d.get(key, "").capitalize().
func capitalizeOf(d *pyDict, key string) (string, error) {
	s, err := strForMethod(d.get(key, ""), "capitalize")
	if err != nil {
		return "", err
	}
	return pyCapitalize(s), nil
}

// getTrackAttr is _get_track_attr: the attributes of a single track's
// folder name, mostly from the track, with its album for the album fields.
func getTrackAttr(log *slog.Logger, meta *pyDict, trackTitle any, bitDepth, samplingRate any, fileFormat string) (pyAttrs, error) {
	album, err := asDict(meta.get("album", newDict()))
	if err != nil {
		log.Error("Error getting album artist: " + pyExceptionStr(err))
		return nil, err
	}
	names, err := getAlbumArtist(log, album)
	if err != nil {
		return nil, err
	}
	var albumArtist any
	if len(names) > 0 {
		albumArtist, err = flattenArtists(names)
	} else {
		albumArtist, err = safeGet(meta, nil, "performer", "name")
	}
	if err != nil {
		return nil, err
	}
	b := &attrBuilder{attrs: pyAttrs{}}
	b.add("album")(downloaderGetTitle(album))
	b.val("artist", albumArtist)
	b.val("tracktitle", trackTitle)
	b.val("track_title", trackTitle)
	b.val("track_title_base", meta.get("title", ""))
	b.val("album_id", meta.get("id", ""))
	b.val("album_url", meta.get("url", ""))
	b.add("album_title")(downloaderGetTitle(album))
	b.val("album_title_base", album.get("title", ""))
	b.val("album_artist", albumArtist)
	b.val("albumartist", albumArtist)
	b.add("album_genre")(subGet(meta, "genre", "name", ""))
	b.add("album_composer")(subGet(meta, "composer", "name", ""))
	b.add("label")(anyOf(labelAttr(meta, " ／ ")))
	b.val("copyright", meta.get("copyright", ""))
	b.val("upc", meta.get("upc", ""))
	b.val("barcode", meta.get("upc", ""))
	b.val("release_date", meta.get("release_date_original", ""))
	b.add("year")(anyOf(yearOf(meta, "release_date_original")))
	b.add("media_type")(anyOf(capitalizeOf(meta, "product_type")))
	b.val("format", fileFormat)
	b.val("bit_depth", bitDepth)
	b.val("sampling_rate", samplingRate)
	b.val("quality_tag", qualityTag(fileFormat, bitDepth))
	b.val("album_version", meta.get("version", ""))
	b.val("version_tag", versionTag(meta.get("version", nil)))
	b.val("disc_count", meta.get("media_count", ""))
	b.val("track_count", meta.get("track_count", ""))
	b.val("ExplicitFlag", explicitFlag(album.get("parental_warning", nil)))
	b.val("explicit", explicitFlag(album.get("parental_warning", nil)))
	b.add("release_type")(anyOf(formatReleaseType(album.get("release_type", nil))))
	return b.attrs, b.err
}

// getFilenameAttr is _get_filename_attr: the attributes of a track's file
// name.
func getFilenameAttr(log *slog.Logger, trackArtist any, track, album *pyDict) (pyAttrs, error) {
	names, err := getAlbumArtist(log, album)
	if err != nil {
		return nil, err
	}
	albumArtist := trackArtist
	if len(names) > 0 {
		if albumArtist, err = flattenArtists(names); err != nil {
			return nil, err
		}
	}
	b := &attrBuilder{attrs: pyAttrs{}}
	b.val("artist", trackArtist)
	b.val("albumartist", albumArtist)
	b.add("tracktitle")(downloaderGetTitle(track))
	b.add("album_title")(downloaderGetTitle(album))
	b.val("album_title_base", album.get("title", nil))
	b.val("album_artist", albumArtist)
	b.val("track_id", track.get("id", nil))
	b.val("track_artist", trackArtist)
	b.add("track_composer")(safeGet(track, nil, "composer", "name"))
	b.add("track_number")(anyOf(pyFormatValue(track.get("track_number", int64(0)), "02")))
	b.val("isrc", track.get("isrc", nil))
	b.val("bit_depth", track.get("maximum_bit_depth", nil))
	b.val("sampling_rate", track.get("maximum_sampling_rate", nil))
	b.add("track_title")(downloaderGetTitle(track))
	b.val("track_title_base", track.get("title", nil))
	b.val("version", track.get("version", nil))
	b.add("year")(anyOf(yearOf(track, "release_date_original")))
	b.add("disc_number")(anyOf(pyFormatValue(track.get("media_number", nil), "02")))
	b.val("release_date", track.get("release_date_original", nil))
	b.val("ExplicitFlag", explicitFlag(track.get("parental_warning", nil)))
	b.val("explicit", explicitFlag(track.get("parental_warning", nil)))
	return b.attrs, b.err
}

// getFormat is _get_format: the release gate. It asks for the first track at
// the requested format, unless a single track brings its answer, and gives
// the format name, whether the quality was met, and the bit depth and rate
// the answer names. Any failure there gives ("Unknown", met, None, None),
// so a failed gate never skips a release; only an album without tracks
// fails, with ErrNonStreamable.
func (d *download) getFormat(ctx context.Context, item *pyDict, isTrackID bool, trackURL *pyDict) (format string, qualityMet bool, bitDepth, samplingRate any, err error) {
	var trackDict any = item
	if !isTrackID {
		noTracks := fmt.Errorf("%w: This release has no tracks available (possibly region-locked or removed)", ErrNonStreamable)
		if !item.has("tracks") {
			return "", false, nil, nil, noTracks
		}
		tracks, err := asDict(item.get("tracks", nil))
		if err != nil {
			return "", false, nil, nil, err
		}
		if !pyTruthy(tracks.get("items", nil)) {
			return "", false, nil, nil, noTracks
		}
		if trackDict, err = pyIndex(tracks.get("items", nil), 0); err != nil {
			return "", false, nil, nil, err
		}
	}
	qualityMet = true
	unknown := func() (string, bool, any, any, error) { return "Unknown", qualityMet, nil, nil, nil }

	answer := trackURL
	if answer == nil || !pyTruthy(answer) {
		id, err := pyGetItem(trackDict, "id")
		if err != nil {
			return unknown()
		}
		answer, err = d.client.getTrackURL(ctx, pyStr(id), d.opts.Quality, false)
		if err != nil {
			if ctx.Err() != nil {
				return "", false, nil, nil, stopped(ctx)
			}
			return unknown()
		}
	}
	if !pyTruthy(answer) {
		return unknown()
	}
	if restrictions, ok := answer.get("restrictions", nil).([]any); ok {
		for _, r := range restrictions {
			rd, err := asDict(r)
			if err != nil {
				return unknown()
			}
			if pyEqual(rd.get("code", nil), qlDowngrade) {
				qualityMet = false
				break
			}
		}
	}
	format = "FLAC"
	if d.opts.Quality == 5 {
		format = "MP3"
	}
	if bitDepth, err = answer.item("bit_depth"); err != nil {
		return unknown()
	}
	if samplingRate, err = answer.item("sampling_rate"); err != nil {
		return unknown()
	}
	return format, qualityMet, bitDepth, samplingRate, nil
}

// pyIndex is v[i] for an int index.
func pyIndex(v any, i int) (any, error) {
	switch t := v.(type) {
	case []any:
		if i < len(t) {
			return t[i], nil
		}
		return nil, pyErr(pyIndexError, "list index out of range")
	case string:
		rs := []rune(t)
		if i < len(rs) {
			return string(rs[i]), nil
		}
		return nil, pyErr(pyIndexError, "string index out of range")
	case *pyDict:
		return nil, pyErr(pyKeyError, "%d", i)
	}
	return nil, pyErr(pyTypeError, "'%s' object is not subscriptable", pyTypeName(v))
}

// determineFormats is _determine_formats: the first combination of folder,
// track and multi-disc templates that formats the folder and every track
// without a KeyError or ValueError. Other exceptions propagate.
func (d *download) determineFormats(albumMeta any, albumAttr pyAttrs, tracks []any, trackAttr pyAttrs, isTrack bool, fileFormat string) error {
	s := &d.opts.Settings
	combinations := [][3]string{
		{d.origFolderFormat, d.origTrackFormat, d.origMultiDiscTrackFormat},
		{s.FallbackFolderFormat, d.origTrackFormat, d.origMultiDiscTrackFormat},
		{s.FallbackFolderFormat, DefaultTrack, DefaultMultipleDiscTrack},
		{DefaultFolder, DefaultTrack, DefaultMultipleDiscTrack},
	}
	album, err := asDict(albumMeta)
	if err != nil {
		return err
	}
	isMultiple, err := pyLess(int64(1), album.get("media_count", int64(1)))
	if err != nil {
		return err
	}
	attrs := albumAttr
	if isTrack {
		attrs = trackAttr
	}
	for _, c := range combinations {
		folderFmt, trackFmt := cleanFormatStr(c[0], c[1])
		err := d.tryFormats(folderFmt, trackFmt, c[2], attrs, album, tracks, isMultiple)
		if isPy(err, pyKeyError, pyValueError) {
			continue
		}
		if err != nil {
			return err
		}
		d.folderFormat, d.trackFormat = folderFmt, trackFmt
		s.MultipleDiscTrackFormat = c[2]
		return nil
	}
	d.folderFormat, d.trackFormat = DefaultFolder, DefaultTrack
	return nil
}

// tryFormats formats the folder and every track's file name with one
// combination of templates.
func (d *download) tryFormats(folderFmt, trackFmt, multiDiscFmt string, attrs pyAttrs, album *pyDict, tracks []any, isMultiple bool) error {
	s := &d.opts.Settings
	if _, err := processFolderFormatWithSubdirs(d.log, folderFmt, attrs, nil, s.LegacyCharmap, s.DisableSmartTruncation); err != nil {
		return err
	}
	for _, it := range tracks {
		track, err := asDict(it)
		if err != nil {
			return err
		}
		artist, err := safeGet(track, nil, "performer", "name")
		if err != nil {
			return err
		}
		attrs, err := getFilenameAttr(d.log, artist, track, album)
		if err != nil {
			return err
		}
		tmpl := trackFmt
		switch {
		case isMultiple && s.MultipleDiscOneDir:
			tmpl = multiDiscFmt
		case isMultiple:
			n, err := track.item("media_number")
			if err != nil {
				return err
			}
			if _, err := pyFormatValue(n, "02"); err != nil {
				return err
			}
		}
		if _, err := d.fileName(tmpl, attrs); err != nil {
			return err
		}
	}
	return nil
}

// cleanFormatStr is _clean_format_str: a trailing .mp3 or .flac and the
// surrounding spaces off both templates.
func cleanFormatStr(folder, track string) (string, string) {
	clean := func(fs string) string {
		if strings.HasSuffix(fs, ".mp3") {
			fs = fs[:len(fs)-4]
		} else if strings.HasSuffix(fs, ".flac") {
			fs = fs[:len(fs)-5]
		}
		return pyStrip(fs, "")
	}
	return clean(folder), clean(track)
}

// safeGet is _safe_get: d[k1][k2]... through dict.get with def, stopping at
// def or at a value that can't be indexed. A string or list on the way is
// an AttributeError, as Python calls .get on it.
func safeGet(d *pyDict, def any, keys ...string) (any, error) {
	var curr any = d
	res := def
	for _, k := range keys {
		cd, err := asDict(curr)
		if err != nil {
			return nil, err
		}
		res = cd.get(k, def)
		switch res.(type) {
		case string, []any, *pyDict:
			if !pyEqual(res, def) {
				curr = res
				continue
			}
		}
		return res, nil
	}
	return res, nil
}

// downloaderGetTitle is downloader.py's _get_title: "title (version)" unless
// the version is already in the title, ignoring case.
func downloaderGetTitle(item *pyDict) (any, error) {
	return getTitleWithVersion(item.get("title", nil), item.get("version", nil))
}

// getDescription is _get_description, which nothing upstream calls.
func getDescription(item *pyDict, trackTitle string, multiple any) string {
	title := fmt.Sprintf("%s [%s/%s]", trackTitle, pyStr(item.get("bit_depth", "")), pyStr(item.get("sampling_rate", "")))
	if pyTruthy(multiple) {
		title = fmt.Sprintf("[CD %s] %s", pyStr(multiple), title)
	}
	return title
}

// getExtra is _get_extra: a cover or a booklet into dir, unless it is there.
// artSize, when known, replaces the _600. size in the URL. A failed transfer
// is logged and the download goes on; the error is an exception Python
// doesn't catch, such as a URL that isn't a string.
func (d *download) getExtra(ctx context.Context, item any, dir, name, artSize string) error {
	if ctx.Err() != nil {
		return nil
	}
	extraFile := pyPathJoin(dir, name)
	if isFile(extraFile) {
		d.info(name + " was already downloaded")
		return nil
	}
	url := item
	if slices.Contains(artSizes, artSize) {
		s, err := strForMethod(item, "replace")
		if err != nil {
			return err
		}
		url = strings.ReplaceAll(s, "_600.", "_"+artSize+".")
	}
	d.info("[+] In progress: " + name)
	if err := d.transfer(ctx, pyStr(url), extraFile, nil); err != nil {
		if ctx.Err() == nil {
			d.warn(fmt.Sprintf("[!] Skipping cover art '%s': URL unreachable (%s)", name, err))
		}
		return nil
	}
	d.info("  L Completed: " + name)
	return nil
}

// downloadGoodies is _download_goodies: each goody with a URL saved as
// "<title> (<id>).pdf". Errors are logged.
func (d *download) downloadGoodies(ctx context.Context, album *pyDict, dir string) {
	if ctx.Err() != nil {
		return
	}
	err := func() error {
		goodies, err := album.list("goodies")
		if err != nil {
			return err
		}
		for _, g := range goodies {
			if ctx.Err() != nil {
				break
			}
			goody, err := asDict(g)
			if err != nil {
				return err
			}
			if !pyTruthy(goody.get("url", nil)) {
				continue
			}
			name := fmt.Sprintf("%s (%s).pdf", pyStr(album.get("title", nil)), pyStr(goody.get("id", nil)))
			name = sanitizeFilename(CleanFilename(name, false), "")
			if err := d.getExtra(ctx, goody.get("url", nil), dir, name, ""); err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		d.log.Error("Error downloading goodies: " + pyExceptionStr(err))
	}
}

// cleanEmbedArt is _clean_embed_art: embed_cover.jpg removed once the tracks
// are tagged. Python sleeps half a second first for Windows' file locks.
func cleanEmbedArt(dir string) {
	embed := pyPathJoin(dir, embCoverName)
	if pathExists(embed) {
		_ = os.Remove(embed)
	}
}

// Tracklist patterns, with Python's Unicode \s:
//
//	\r?\n|\s+-\s+
//	<br\s*/?>
//	<[^<]+>
var (
	rePerformerLines = regexp.MustCompile(`\r?\n|` + reS + `+-` + reS + `+`)
	reBr             = regexp.MustCompile(`<br` + reS + `*/?>`)
	reTag            = regexp.MustCompile(`<[^<]+>`)
)

// sanitizeTitle is sanitize_filename(title) with pathvalidate's handling of
// None and non-strings.
func sanitizeTitle(title any) (string, error) {
	switch t := title.(type) {
	case nil:
		return "", nil
	case string:
		return sanitizeFilename(t, ""), nil
	}
	return "", pyErr(pyTypeError, "text must be a string: actual=<class '%s'>", pyTypeName(title))
}

// generateTracklist is _generate_tracklist: "<album title> - Tracklist.txt"
// with the album's credits, tracks and review. An existing file is kept.
// Errors while writing are logged, and what was written so far stays; the
// error is an exception Python raises before that.
func (d *download) generateTracklist(ctx context.Context, meta *pyDict, dir string, albumTitle any, fileFormat string, bitDepth, samplingRate any) error {
	if d.opts.NoCredits || ctx.Err() != nil {
		return nil
	}
	safeTitle, err := sanitizeTitle(albumTitle)
	if err != nil {
		return err
	}
	tracklistPath := pyPathJoin(dir, fitName(dir, safeTitle, " - Tracklist.txt", 0))
	if isFile(tracklistPath) {
		return nil
	}
	d.info("[+] Generating Digital Booklet...")
	artistName, err := safeGet(meta, "Unknown Artist", "artist", "name")
	if err != nil {
		return err
	}
	composer, err := safeGet(meta, "N/A", "composer", "name")
	if err != nil {
		return err
	}
	label, err := safeGet(meta, "Independent", "label", "name")
	if err != nil {
		return err
	}
	rawGenre, err := safeGet(meta, "Unknown Genre", "genre", "name")
	if err != nil {
		return err
	}
	genre := rawGenre
	if !pyEqual(rawGenre, "Unknown Genre") {
		switch g := rawGenre.(type) {
		case string:
			if mapped, ok := localGenreMap[g]; ok {
				genre = mapped
			}
		case []any, *pyDict:
			return pyErr(pyTypeError, "unhashable type: '%s'", pyTypeName(rawGenre))
		}
	}
	releaseDate := meta.get("release_date_original", "Unknown Date")

	var b strings.Builder
	werr := writeTracklist(&b, meta, albumTitle, artistName, composer, label, genre, releaseDate, fileFormat, bitDepth, samplingRate)
	if err := writeFile(tracklistPath, []byte(b.String())); err != nil {
		werr = err
	}
	if werr != nil {
		d.log.Error("[!] Error creating booklet: " + pyExceptionStr(werr))
		return nil
	}
	d.info("  L Completed: Digital Booklet.txt (Credits & Review)")
	return nil
}

// writeTracklist writes the tracklist's text into b. An error is what
// Python raises partway through; b keeps what came before.
func writeTracklist(b *strings.Builder, meta *pyDict, albumTitle, artistName, composer, label, genre, releaseDate any,
	fileFormat string, bitDepth, samplingRate any) error {
	rule := strings.Repeat("=", 70)
	explicitTag := ""
	if pyTruthy(meta.get("parental_warning", nil)) {
		explicitTag = " [E]"
	}
	b.WriteString(rule + "\n")
	b.WriteString("ALBUM      : " + pyStr(albumTitle) + explicitTag + "\n")
	if !pyEqual(composer, "N/A") {
		b.WriteString("COMPOSER   : " + pyStr(composer) + "\n")
	}
	b.WriteString("MAIN ART.  : " + pyStr(artistName) + "\n")
	b.WriteString("LABEL      : " + pyStr(label) + "\n")
	b.WriteString("GENRE      : " + pyStr(genre) + "\n")
	b.WriteString("RELEASE    : " + pyStr(releaseDate) + "\n")
	fmt.Fprintf(b, "QUALITY    : %s (%s-Bit / %s kHz)\n", fileFormat, pyStr(bitDepth), pyStr(samplingRate))
	b.WriteString(rule + "\n\n")

	tracksD, err := meta.dict("tracks")
	if err != nil {
		return err
	}
	tracks, err := tracksD.list("items")
	if err != nil {
		return err
	}
	discs := make([]any, 0, len(tracks))
	for _, t := range tracks {
		td, err := asDict(t)
		if err != nil {
			return err
		}
		discs = append(discs, td.get("media_number", int64(1)))
	}
	var totalDiscs any = int64(1)
	if len(discs) > 0 {
		if totalDiscs, err = pyMinMax(discs, true); err != nil {
			return err
		}
	}
	var currentDisc any
	for _, t := range tracks {
		track := t.(*pyDict)
		discNum := track.get("media_number", int64(1))
		more, err := pyLess(int64(1), totalDiscs)
		if err != nil {
			return err
		}
		if more && !pyEqual(discNum, currentDisc) {
			if currentDisc != nil {
				b.WriteString("\n")
			}
			b.WriteString("--- DISC " + pyStr(discNum) + " ---\n\n")
			currentDisc = discNum
		}
		tNum := pyZfill(pyStr(track.get("track_number", int64(0))), 2)
		tTitle := pyStr(track.get("title", "Unknown Title"))
		if pyTruthy(track.get("parental_warning", nil)) {
			tTitle += " [E]"
		}
		duration, err := pyInt(track.get("duration", int64(0)))
		if err != nil {
			return err
		}
		mins, secs := floorDivmod(duration, 60)
		m, _ := pyFormatValue(mins, "02")
		sec, _ := pyFormatValue(secs, "02")
		b.WriteString(pyLjust(tNum+". "+tTitle, 60) + " [" + m + ":" + sec + "]\n")

		if performers := track.get("performers", ""); pyTruthy(performers) {
			for _, line := range rePerformerLines.Split(pyStr(performers), -1) {
				if s := pyStrip(line, ""); s != "" {
					b.WriteString("    * " + s + "\n")
				}
			}
		} else {
			tArtist, err := safeGet(track, artistName, "performer", "name")
			if err != nil {
				return err
			}
			b.WriteString("    " + pyStr(tArtist) + "\n")
		}
		b.WriteString("\n")
	}

	if description := meta.get("description", nil); pyTruthy(description) {
		b.WriteString("\n" + rule + "\nALBUM REVIEW / NOTES\n" + rule + "\n\n")
		clean := reTag.ReplaceAllString(reBr.ReplaceAllString(pyStr(description), "\n"), "")
		for _, p := range strings.Split(clean, "\n") {
			if s := pyStrip(p, ""); s != "" {
				b.WriteString(textwrapFill(s, 70) + "\n\n")
			}
		}
	}
	return nil
}

// floorDivmod is divmod(a, b) for ints, rounding toward negative infinity.
func floorDivmod(a, b int64) (int64, int64) {
	q, r := a/b, a%b
	if r != 0 && (r < 0) != (b < 0) {
		q--
		r += b
	}
	return q, r
}

// pyZfill is s.zfill(width): zeros after any sign up to width code points.
func pyZfill(s string, width int) string {
	n := pyLen(s)
	if n >= width {
		return s
	}
	pad := strings.Repeat("0", width-n)
	if s != "" && (s[0] == '+' || s[0] == '-') {
		return s[:1] + pad + s[1:]
	}
	return pad + s
}

// The transfer engine.

// transferChunk is the 64 KiB requests reads at a time.
const transferChunk = 65536

const transferAttempts = 5

var (
	errFileNotFound = errors.New("qobuzdl: HTTP 404: File not found on server")
	errIncomplete   = errors.New("qobuzdl: Incomplete download")
)

// Request headers of tqdm_download.
const (
	transferUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
	transferAccept    = "audio/webm,audio/ogg,audio/wav,audio/*;q=0.9,*/*;q=0.5"
)

// transfer is tqdm_download: up to five attempts, each resuming with Range
// from the bytes already saved. A 416 counts as done. A 404, or any error
// whose text holds "404", deletes the file and fails at once; other errors
// sleep RetryDelays and retry. An attempt that ends short of the announced
// size, without an error, retries at once. More bytes than announced
// deletes the file but keeps the count, so the next attempt resumes into a
// new file from that offset, as upstream does.
func (d *download) transfer(ctx context.Context, url, fname string, onBytes func(done, total int64)) error {
	if ctx.Err() != nil {
		return stopped(ctx)
	}
	if onBytes == nil {
		onBytes = func(int64, int64) {}
	}
	var downloaded, total int64
	for attempt := range transferAttempts {
		if ctx.Err() != nil {
			return stopped(ctx)
		}
		complete, err := d.transferAttempt(ctx, url, fname, attempt, &downloaded, &total, onBytes)
		if err == nil {
			if complete {
				return nil
			}
			continue
		}
		if ctx.Err() != nil {
			return stopped(ctx)
		}
		if strings.Contains(err.Error(), "404") {
			removeFile(fname)
			return errFileNotFound
		}
		if attempt < transferAttempts-1 {
			wait := d.retryDelay(attempt)
			d.warn(fmt.Sprintf("[!] Server block. Retrying in %ss (%d/%d) | Error details: %v", seconds(wait), attempt+1, transferAttempts, err))
			if err := sleepCtx(ctx, wait); err != nil {
				return err
			}
			continue
		}
		removeFile(fname)
		return fmt.Errorf("qobuzdl: Definitive timeout after %d attempts. Last error: %w", transferAttempts, err)
	}
	if ctx.Err() != nil {
		return stopped(ctx)
	}
	removeFile(fname)
	return errIncomplete
}

// retryDelay is the sleep after a failed attempt: 2, 4, 8 and 16 seconds
// upstream.
func (d *download) retryDelay(attempt int) time.Duration {
	delays := d.opts.RetryDelays
	switch {
	case attempt < len(delays):
		return delays[attempt]
	case len(delays) > 0:
		return delays[len(delays)-1]
	}
	return 0
}

// removeFile is os.path.exists then os.remove.
func removeFile(name string) {
	if pathExists(name) {
		_ = os.Remove(name)
	}
}

// transferAttempt is one pass of tqdm_download's loop. complete is true when
// the file is whole or the server answered 416. An error is what Python
// raises inside the attempt; a short body without one returns neither.
func (d *download) transferAttempt(ctx context.Context, url, fname string, attempt int, downloaded, total *int64,
	onBytes func(done, total int64)) (complete bool, err error) {
	attemptCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timers := newTransferTimers(cancel, d.opts.Timeouts)
	defer timers.stop()

	rangeHeader := "bytes=0-"
	if *downloaded > 0 {
		rangeHeader = fmt.Sprintf("bytes=%d-", *downloaded)
	}
	d.log.Debug("transfer", "url", redactURL(url), "range", rangeHeader, "attempt", attempt+1)
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(attemptCtx, timers.trace()), http.MethodGet, url, nil)
	if err != nil {
		return false, &requestError{op: "transfer", err: errors.New("invalid URL")}
	}
	req.Header.Set("User-Agent", transferUserAgent)
	req.Header.Set("Accept", transferAccept)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Range", rangeHeader)
	resp, err := d.opts.HTTPClient.Do(req)
	if err != nil {
		return false, transferError(ctx, attemptCtx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	timers.startIdle()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return true, nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return false, fmt.Errorf("qobuzdl: transfer: Status Server: %d", resp.StatusCode)
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if *downloaded > 0 {
		flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	if *downloaded > 0 && resp.StatusCode == http.StatusOK {
		// The server ignored Range and sent the whole file again: start
		// over instead of appending it to the part already saved.
		*downloaded, *total = 0, 0
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	if *total == 0 {
		n := int64(0)
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			v, err := parsePyInt(cl)
			if err != nil {
				return false, err
			}
			n = v
		}
		*total = *downloaded + n
	}

	f, err := openFile(fname, flags)
	if err != nil {
		return false, err
	}
	body := &idleReader{r: resp.Body, timers: timers}
	buf := make([]byte, transferChunk)
	var readErr error
	for {
		n, err := readChunk(body, buf)
		if err != nil && !errors.Is(err, io.EOF) {
			readErr = transferError(ctx, attemptCtx, err)
			break
		}
		if ctx.Err() != nil {
			readErr = stopped(ctx)
			break
		}
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				readErr = werr
				break
			}
			*downloaded += int64(n)
			onBytes(*downloaded, *total)
		}
		if err != nil {
			break
		}
	}
	if cerr := f.Close(); readErr == nil {
		readErr = cerr
	}
	if readErr != nil {
		return false, readErr
	}
	if *downloaded > *total && *total > 0 {
		removeFile(fname)
		return false, fmt.Errorf("qobuzdl: transfer: Downloaded %d bytes, but the server announced %d", *downloaded, *total)
	}
	return *downloaded >= *total, nil
}

// readChunk fills buf as urllib3 reads a chunk: until it is full or the body
// ends. A body that breaks off loses the unfinished chunk, as in Python.
func readChunk(r io.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if errors.Is(err, io.EOF) {
			return n, io.EOF
		}
		if err != nil {
			return 0, err
		}
	}
	return n, nil
}

// openFile opens a file for writing, created 0644 whatever the umask.
func openFile(name string, flags int) (*os.File, error) {
	f, err := os.OpenFile(name, flags, 0o644)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// writeFile writes a whole file, 0644 whatever the umask.
func writeFile(name string, data []byte) error {
	f, err := openFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// transferError sorts a failed request or read: the caller's context ending,
// a timeout of the attempt's own, or a request error without the URL.
func transferError(ctx, attemptCtx context.Context, err error) error {
	if ctx.Err() != nil {
		return stopped(ctx)
	}
	var te *timeoutError
	if errors.As(context.Cause(attemptCtx), &te) {
		return te
	}
	return callError(ctx, attemptCtx, Timeouts{}, "transfer", err)
}

// transferTimers stand in for requests' timeout=(10, 60): a connect timer
// from asking for a connection to having one, and an idle timer from then
// to the first response byte and between body reads. Either cancels the
// attempt with a timeoutError as the cause.
type transferTimers struct {
	cancel           context.CancelCauseFunc
	connect, idleFor time.Duration

	mu          sync.Mutex
	connTimer   *time.Timer
	idleTimer   *time.Timer
	stoppedOnce bool
}

func newTransferTimers(cancel context.CancelCauseFunc, t Timeouts) *transferTimers {
	def := DefaultTimeouts()
	return &transferTimers{
		cancel:  cancel,
		connect: cmpDuration(t.Connect, def.Connect),
		idleFor: cmpDuration(t.Idle, def.Idle),
	}
}

func cmpDuration(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

func (tt *transferTimers) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn:              func(string) { tt.startConnect() },
		GotConn:              func(httptrace.GotConnInfo) { tt.startIdle() },
		GotFirstResponseByte: tt.startIdle,
	}
}

// startConnect starts the connect timer, or starts it again for a redirect.
func (tt *transferTimers) startConnect() {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if tt.stoppedOnce {
		return
	}
	if tt.idleTimer != nil {
		tt.idleTimer.Stop()
	}
	if tt.connTimer == nil {
		tt.connTimer = time.AfterFunc(tt.connect, func() {
			tt.cancel(&timeoutError{op: "transfer: connect", after: tt.connect})
		})
		return
	}
	tt.connTimer.Reset(tt.connect)
}

// startIdle stops the connect timer and starts the idle timer, or starts it
// again after a byte came.
func (tt *transferTimers) startIdle() {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	if tt.stoppedOnce {
		return
	}
	if tt.connTimer != nil {
		tt.connTimer.Stop()
	}
	if tt.idleTimer == nil {
		tt.idleTimer = time.AfterFunc(tt.idleFor, func() {
			tt.cancel(&timeoutError{op: "transfer: read", after: tt.idleFor})
		})
		return
	}
	tt.idleTimer.Reset(tt.idleFor)
}

func (tt *transferTimers) stop() {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	tt.stoppedOnce = true
	if tt.connTimer != nil {
		tt.connTimer.Stop()
	}
	if tt.idleTimer != nil {
		tt.idleTimer.Stop()
	}
}

// idleReader restarts the idle timer on every read of the body.
type idleReader struct {
	r      io.Reader
	timers *transferTimers
}

func (r *idleReader) Read(p []byte) (int, error) {
	r.timers.startIdle()
	return r.r.Read(p)
}

// Python's os.path functions on POSIX paths.

// pySplit is os.path.split.
func pySplit(p string) (head, tail string) {
	i := strings.LastIndexByte(p, '/') + 1
	head, tail = p[:i], p[i:]
	if head != "" && strings.Trim(head, "/") != "" {
		head = strings.TrimRight(head, "/")
	}
	return head, tail
}

// pyDirname is os.path.dirname.
func pyDirname(p string) string {
	head, _ := pySplit(p)
	return head
}

// pyJoin is os.path.join with any number of parts; none gives "".
func pyJoin(parts ...string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out = pyPathJoin(out, p)
	}
	return out
}

// pySplitext is os.path.splitext: the extension is from the last dot of the
// last component, unless only dots come before it.
func pySplitext(p string) (root, ext string) {
	sep := strings.LastIndexByte(p, '/')
	dot := strings.LastIndexByte(p, '.')
	if dot > sep {
		for i := sep + 1; i < dot; i++ {
			if p[i] != '.' {
				return p[:dot], p[dot:]
			}
		}
	}
	return p, ""
}

// pathExists is os.path.exists, which follows symlinks.
func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// isDir is os.path.isdir.
func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// makeDirs is os.makedirs(p, exist_ok=True), with each new folder 0755
// whatever the umask.
func makeDirs(p string) error {
	if st, err := os.Stat(p); err == nil {
		if st.IsDir() {
			return nil
		}
		return &os.PathError{Op: "mkdir", Path: p, Err: syscall.ENOTDIR}
	}
	if parent := pyDirname(strings.TrimRight(p, "/")); parent != "" && parent != p {
		if err := makeDirs(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		if isDir(p) {
			return nil
		}
		return err
	}
	return os.Chmod(p, 0o755)
}

// Ports qobuz_dl/core.py from qobuz-dl 2.5.9: QobuzDL's __init__,
// initialize_client, get_tokens, download_from_id, handle_url and
// search_by_type. Lucky mode, the interactive menu, text files of URLs,
// Last.fm playlists, the blacklist and the label filter are not ported.

package qobuzdl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Options holds QobuzDL's arguments and the Go-only knobs. Its zero value is
// not upstream's default; start from DefaultOptions.
type Options struct {
	Directory         string // "QobuzDownloads"
	Quality           int    // 6
	EmbedArt          bool   // QobuzDL.embed_art: embed embed_cover.jpg when present
	IgnoreSinglesEPs  bool
	NoM3UForPlaylists bool
	QualityFallback   bool        // true
	DownloadsDB       DownloadsDB // nil: no database
	FolderFormat      string      // "{artist} - {album} ({year}) [{bit_depth}B-{sampling_rate}kHz]"
	TrackFormat       string      // "{track_number} - {track_title}"
	SmartDiscography  bool
	ForceEnglish      bool // true
	NoCredits         bool
	Settings          Settings

	HTTPClient  *http.Client // required; without a Timeout, which would cut long transfers
	Logger      *slog.Logger // nil discards
	FFmpegPath  string       // "ffmpeg"
	Timeouts    Timeouts
	TrackPause  time.Duration   // 1s, the sleep before each track
	RetryDelays []time.Duration // 2s, 4s, 8s, 16s
	APIBase     string          // "" for https://www.qobuz.com/api.json/0.2/
	PlayBase    string          // "" for https://play.qobuz.com
}

// DefaultOptions returns QobuzDL's defaults with DefaultSettings, and the
// Go-only knobs set as upstream behaves. HTTPClient stays nil.
func DefaultOptions() Options {
	return Options{
		Directory:       "QobuzDownloads",
		Quality:         6,
		QualityFallback: true,
		FolderFormat:    "{artist} - {album} ({year}) [{bit_depth}B-{sampling_rate}kHz]",
		TrackFormat:     "{track_number} - {track_title}",
		ForceEnglish:    true,
		Settings:        DefaultSettings(),
		FFmpegPath:      "ffmpeg",
		Timeouts:        DefaultTimeouts(),
		TrackPause:      time.Second,
		RetryDelays:     []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second},
	}
}

// DownloadsDB is the downloads database. The downloadsdb package implements
// it in SQLite with Python's table; callers may keep their own.
type DownloadsDB interface {
	// Has ports handle_download_id without add_id. Python matches ID and
	// quality only; the media type is there for implementations that key on
	// it.
	Has(ctx context.Context, mediaType, id string, quality int) (bool, error)
	// Add ports handle_download_id with add_id. A duplicate is not an error.
	Add(ctx context.Context, rec DownloadRecord) error
}

// DownloadRecord is one row of the downloads table.
type DownloadRecord struct {
	ID, MediaType          string
	Quality                int
	FileFormat             string
	QualityMet             bool
	BitDepth, SamplingRate *string // str() of Python's value; nil for None
	SavedPath, Status      string  // Status is "downloaded"
	URL, ReleaseDate       string
	Artist, Album          string
}

// webURL is core.py's WEB_URL.
const webURL = "https://play.qobuz.com/"

// qualities is core.py's QUALITIES, for the line initialize_client logs.
var qualities = map[int]string{
	5:  "5 - MP3",
	6:  "6 - 16 bit, 44.1kHz",
	7:  "7 - 24 bit, <96kHz",
	27: "27 - 24 bit, >96kHz",
}

// DL is QobuzDL. It is safe for concurrent use once InitializeClient has
// run: each call copies the options it changes, where Python changes the
// shared ones.
type DL struct {
	opts Options

	mu      sync.Mutex
	client  *Client
	appID   string
	secrets []string
}

// DownloadRequest is download_from_id's arguments.
type DownloadRequest struct {
	ID            string
	Album         bool   // album=True in Python; false downloads a track
	AltPath       string // alt_path; empty means Options.Directory
	IsPlaylist    bool
	PlaylistIndex int // 1-based; 0 means None
	IgnoreDB      bool
	Progress      Progress // nil allowed
}

// SearchResult is one search_by_type result: the line Python shows and the
// item's web player URL.
type SearchResult struct{ Text, URL string }

// New ports QobuzDL.__init__. It makes Directory absolute and creates it, as
// create_and_return_dir does. It rejects what Python would fail on later:
// no HTTP client, fewer than one worker, and a quality outside 5, 6, 7 and
// 27, which initialize_client looks up in QUALITIES.
func New(opts Options) (*DL, error) {
	if opts.HTTPClient == nil {
		return nil, errors.New("qobuzdl: Options.HTTPClient is nil")
	}
	if opts.Settings.MaxWorkers < 1 {
		return nil, fmt.Errorf("qobuzdl: Settings.MaxWorkers is %d, below 1", opts.Settings.MaxWorkers)
	}
	if _, ok := qualities[opts.Quality]; !ok {
		return nil, fmt.Errorf("%w: %d: choose between 5, 6, 7 or 27", ErrInvalidQuality, opts.Quality)
	}
	dir, err := createAndReturnDir(opts.Directory)
	if err != nil {
		return nil, err
	}
	opts.Directory = dir
	opts.Settings.VariousArtistsAliases = slices.Clone(opts.Settings.VariousArtistsAliases)
	opts.RetryDelays = slices.Clone(opts.RetryDelays)
	return &DL{opts: opts}, nil
}

// logger is the options' logger with Python's shown lines also going to p.
func (d *DL) logger(p Progress) *slog.Logger {
	base := d.opts.Logger
	if base == nil {
		base = slog.New(slog.DiscardHandler)
	}
	return slog.New(progressHandler{Handler: base.Handler(), p: orNoProgress(p)})
}

// GetTokens ports get_tokens: it fetches the web player's bundle and keeps
// its app ID and its non-empty secrets.
func (d *DL) GetTokens(ctx context.Context) error {
	b, err := fetchBundle(ctx, d.opts.HTTPClient, d.opts.PlayBase, d.opts.Timeouts)
	if err != nil {
		return err
	}
	appID, err := b.AppID()
	if err != nil {
		return err
	}
	all, err := b.Secrets()
	if err != nil {
		return err
	}
	var secrets []string
	for _, s := range all {
		if s.Value != "" {
			secrets = append(secrets, s.Value)
		}
	}
	d.mu.Lock()
	d.appID, d.secrets = appID, secrets
	d.mu.Unlock()
	return nil
}

// AppID is the app ID GetTokens found.
func (d *DL) AppID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.appID
}

// Secrets are the secrets GetTokens found.
func (d *DL) Secrets() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.secrets)
}

// InitializeClient ports initialize_client: a Client with
// Settings.UserAuthToken as the token, or pwd when it is longer than 60
// characters and no token is set.
func (d *DL) InitializeClient(ctx context.Context, email, pwd, appID string, secrets []string) error {
	c, err := NewClient(ctx, ClientOptions{
		Email: email, Password: pwd, AppID: appID, Secrets: secrets,
		UserAuthToken: d.opts.Settings.UserAuthToken, ForceEnglish: d.opts.ForceEnglish,
		HTTPClient: d.opts.HTTPClient, Logger: d.opts.Logger, APITimeout: d.opts.Timeouts.API,
		APIBase: d.opts.APIBase, PlayBase: d.opts.PlayBase,
	})
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.client = c
	d.mu.Unlock()
	d.logger(nil).Info("Set max quality: " + qualities[d.opts.Quality] + "\n")
	return nil
}

// Client is the client InitializeClient made, or nil.
func (d *DL) Client() *Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.client
}

// errNoClient is Python's AttributeError on self.client before
// initialize_client.
var errNoClient = errors.New("qobuzdl: InitializeClient has not run")

// unhandled wraps an exception Python doesn't catch, which would end the
// Python CLI, in ErrUnhandled. The context's end stays as it is.
func unhandled(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, ErrUnhandled) {
		return err
	}
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrUnhandled, err)
}

// DownloadFromID ports download_from_id: an album or a track, unless the
// downloads database has it at the requested quality. NonStreamable and
// request errors are logged and reported in the Result, as Python logs and
// skips them; other errors return wrapped in ErrUnhandled.
func (d *DL) DownloadFromID(ctx context.Context, req DownloadRequest) (*Result, error) {
	return d.downloadFromID(ctx, d.opts, req)
}

func (d *DL) downloadFromID(ctx context.Context, opts Options, req DownloadRequest) (*Result, error) {
	kind := "track"
	if req.Album {
		kind = "album"
	}
	res := &Result{Kind: kind, ID: req.ID}
	c := d.Client()
	if c == nil {
		return res, errNoClient
	}
	log := d.logger(req.Progress)
	if !req.IgnoreDB && opts.DownloadsDB != nil {
		has, err := opts.DownloadsDB.Has(ctx, kind, req.ID, opts.Quality)
		if err != nil {
			return res, fmt.Errorf("qobuzdl: downloads database: %w", err)
		}
		if has {
			log.Info(fmt.Sprintf("This release ID (%s) was already downloaded according to the local database.\n"+
				"Use the '--no-db' flag to bypass this.", req.ID))
			res.Releases = []Release{{Kind: kind, ID: req.ID, Status: StatusSkipped, Skip: SkipInDatabase}}
			return res, nil
		}
	}
	path := req.AltPath
	if path == "" {
		path = opts.Directory
	}
	dl := newDownload(c, opts, req.ID, path, req.Progress)
	dl.isPlaylist = req.IsPlaylist
	dl.playlistTrackNumber = req.PlaylistIndex
	rel, err := dl.downloadIDByType(ctx, !req.Album)
	if rel != nil {
		res.Folder = rel.Folder
		res.Releases = []Release{*rel}
	}
	switch {
	case err == nil:
		return res, nil
	case ctx.Err() != nil:
		return res, err
	case errors.Is(err, ErrNonStreamable) || isRequestError(err):
		log.Error(fmt.Sprintf("Error getting release: %v. Skipping...", err))
		return res, nil
	}
	return res, unhandled(ctx, err)
}

// HandleURL ports handle_url: the URL's kind and ID go to HandleID. A URL
// that isn't Qobuz's is logged and returns ErrInvalidURL.
func (d *DL) HandleURL(ctx context.Context, url string, p Progress) (*Result, error) {
	kind, id, err := GetURLInfo(url)
	if err != nil {
		d.logger(p).Info(fmt.Sprintf("Invalid url: \"%s\". Use urls from https://play.qobuz.com!", url))
		return &Result{}, err
	}
	return d.handle(ctx, url, kind, id, p)
}

// HandleID is handle_url after get_url_info. Kind is album, track,
// playlist, artist or label. A playlist lands in a flat folder named after
// it, with an m3u8 unless NoM3UForPlaylists; artists and labels download
// each album into a folder named after them.
func (d *DL) HandleID(ctx context.Context, kind, id string, p Progress) (*Result, error) {
	return d.handle(ctx, webURL+kind+"/"+id, kind, id, p)
}

// collectionKinds are handle_url's possibles for collections: the endpoint,
// multi_meta's count key and the key the items are under.
var collectionKinds = map[string]struct{ endpoint, countKey, itemsKey string }{
	"playlist": {"playlist/get", "tracks_count", "tracks"},
	"artist":   {"artist/get", "albums_count", "albums"},
	"label":    {"label/get", "albums_count", "albums"},
}

func (d *DL) handle(ctx context.Context, url, kind, id string, p Progress) (*Result, error) {
	log := d.logger(p)
	if kind == "album" || kind == "track" {
		return d.DownloadFromID(ctx, DownloadRequest{ID: id, Album: kind == "album", Progress: p})
	}
	coll, ok := collectionKinds[kind]
	if !ok {
		log.Info(fmt.Sprintf("Invalid url: \"%s\". Use urls from https://play.qobuz.com!", url))
		return &Result{}, fmt.Errorf("%w: kind %q", ErrInvalidURL, kind)
	}
	res := &Result{Kind: kind, ID: id}
	c := d.Client()
	if c == nil {
		return res, errNoClient
	}
	content, err := c.multiMeta(ctx, coll.endpoint, coll.countKey, id)
	if err != nil {
		return res, unhandled(ctx, err)
	}
	if len(content) == 0 {
		log.Warn("[!] Skipped URL: Content empty or unavailable (Geo-blocked/Removed). URL: " + url)
		return res, fmt.Errorf("%w: %s %s", ErrEmptyContent, kind, id)
	}
	name, err := content[0].item("name")
	if err != nil {
		return res, unhandled(ctx, err)
	}
	log.Info(fmt.Sprintf("Downloading all the music from %s (%s)!", pyStr(name), kind))
	nameStr, ok := name.(string)
	if !ok {
		return res, unhandled(ctx, pyErr(pyTypeError, "name must be a str, not %s", pyTypeName(name)))
	}
	newPath, err := createAndReturnDir(pyPathJoin(d.opts.Directory, sanitizeFilename(nameStr, "")))
	if err != nil {
		return res, unhandled(ctx, err)
	}
	res.Folder = newPath

	var items []any
	if d.opts.SmartDiscography && kind == "artist" {
		filtered, err := SmartDiscographyFilter(content, true, true)
		if err != nil {
			return res, unhandled(ctx, err)
		}
		for _, it := range filtered {
			items = append(items, it)
		}
	} else {
		for _, chunk := range content {
			sub, err := chunkGet(chunk, coll.itemsKey)
			if err != nil {
				return res, unhandled(ctx, err)
			}
			batch, err := chunkGet(sub, "items")
			if err != nil {
				return res, unhandled(ctx, err)
			}
			more, err := pyIter(batch)
			if err != nil {
				return res, unhandled(ctx, err)
			}
			items = append(items, more...)
		}
	}
	log.Debug(fmt.Sprintf("Number of chunks: %d", len(content)))
	if first, err := chunkGet(content[0], coll.itemsKey); err == nil {
		if batch, err := chunkGet(first, "items"); err == nil {
			if n, err := pyLenValue(batch); err == nil {
				log.Debug(fmt.Sprintf("Items in first chunk: %d", n))
			}
		}
	}
	log.Info(fmt.Sprintf("%d downloads in queue", len(items)))

	// The playlist's flat folder: Python sets these on the shared object and
	// restores them after the loop; the copy keeps other calls apart.
	opts := d.opts
	isPlaylist := kind == "playlist"
	if isPlaylist {
		opts.FolderFormat = "."
		opts.Settings.MultipleDiscOneDir = true
		pl, _ := playlistFromPages(content)
		orNoProgress(p).ReleaseStart(ReleaseInfo{
			Kind: "playlist", ID: id, Title: nameStr, Folder: newPath, Playlist: pl, Tracks: plannedTracks(items),
		})
	}

	var queued []*pyDict
	for idx, item := range items {
		if ctx.Err() != nil {
			return res, stopped(ctx)
		}
		itemID, err := pyGetItem(item, "id")
		if err != nil {
			return res, unhandled(ctx, err)
		}
		queued = append(queued, item.(*pyDict))
		r, err := d.downloadFromID(ctx, opts, DownloadRequest{
			ID: pyStr(itemID), Album: coll.itemsKey == "albums", AltPath: newPath,
			IsPlaylist: isPlaylist, PlaylistIndex: idx + 1, Progress: p,
		})
		res.Releases = append(res.Releases, r.Releases...)
		if isPlaylist {
			endUnstartedTrack(p, r, item.(*pyDict), idx+1)
		}
		if err != nil {
			return res, err
		}
	}

	if isPlaylist && !d.opts.NoM3UForPlaylists {
		m3u, err := makeM3U(log, newPath, queued)
		if err != nil {
			return res, unhandled(ctx, err)
		}
		res.M3U = m3u
	}
	return res, nil
}

// chunkGet is chunk.get(key, {}) followed by a .get on the value, which
// fails as Python's AttributeError when the value has no .get.
func chunkGet(v any, key string) (any, error) {
	d, ok := v.(*pyDict)
	if !ok {
		return nil, pyErr(pyAttributeError, "'%s' object has no attribute 'get'", pyTypeName(v))
	}
	def := any(newDict())
	if key == "items" {
		def = []any{}
	}
	return d.get(key, def), nil
}

// endUnstartedTrack sends TrackEnd for a playlist track that never reached
// a transfer, such as one in the downloads database, so that every track
// the playlist's ReleaseStart planned ends.
func endUnstartedTrack(p Progress, r *Result, item *pyDict, pos int) {
	if p == nil || len(r.Releases) != 1 || len(r.Releases[0].Tracks) > 0 {
		return
	}
	rel := r.Releases[0]
	tr := newTrackResult(item, pos)
	tr.Outcome, tr.Err = OutcomeFailed, rel.Err
	if rel.Status == StatusSkipped {
		tr.Outcome = OutcomeSkipped
	}
	p.TrackEnd(tr)
}

// SearchByType ports search_by_type without lucky mode. itemType is album,
// track, artist or playlist. A query under 3 characters is
// ErrQueryTooShort; where Python answers {} on a failed search, this
// returns the error.
func (d *DL) SearchByType(ctx context.Context, query, itemType string, limit int) ([]SearchResult, error) {
	if pyLen(query) < 3 {
		d.logger(nil).Info("Your search query is too short or invalid")
		return nil, ErrQueryTooShort
	}
	key := itemType + "s"
	switch itemType {
	case "album", "track", "artist", "playlist":
	default:
		d.logger(nil).Info("Invalid type: " + itemType)
		return nil, fmt.Errorf("qobuzdl: invalid search type %q", itemType)
	}
	c := d.Client()
	if c == nil {
		return nil, errNoClient
	}
	ans, err := c.search(ctx, key, query, limit, 0)
	if err != nil {
		return nil, err
	}
	sub, err := ans.item(key)
	if err != nil {
		return nil, err
	}
	items, err := pyGetItem(sub, "items")
	if err != nil {
		return nil, err
	}
	list, err := pyIter(items)
	if err != nil {
		return nil, unhandled(ctx, err)
	}
	out, err := searchResults(itemType, list)
	return out, unhandled(ctx, err)
}

// searchResults is search_by_type's loop over the items: one line per
// item, in columns for albums and tracks, and the web player's URL.
func searchResults(itemType string, items []any) ([]SearchResult, error) {
	out := []SearchResult{}
	for _, it := range items {
		i, ok := it.(*pyDict)
		if !ok {
			return nil, pyErr(pyAttributeError, "'%s' object has no attribute 'get'", pyTypeName(it))
		}
		var text string
		var err error
		if itemType == "album" || itemType == "track" {
			text, err = searchLine(itemType, i)
		} else {
			text = searchNameLine(i)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, SearchResult{Text: text, URL: webURL + itemType + "/" + pyStr(i.get("id", ""))})
	}
	return out, nil
}

// searchLine is the line of an album or a track.
func searchLine(itemType string, i *pyDict) (string, error) {
	name := func(key string) (any, error) {
		sub := i.get(key, newDict())
		d, ok := sub.(*pyDict)
		if !ok {
			return nil, pyErr(pyAttributeError, "'%s' object has no attribute 'get'", pyTypeName(sub))
		}
		return d.get("name", nil), nil
	}
	artist, err := name("artist")
	if err != nil {
		return "", err
	}
	if !pyTruthy(artist) {
		if artist, err = name("performer"); err != nil {
			return "", err
		}
		if !pyTruthy(artist) {
			artist = "Unknown"
		}
	}
	title := pyOr(i.get("title", nil), i.get("name", nil), "Unknown")
	titleText := pyStr(title)
	if v := i.get("version", nil); pyTruthy(v) {
		titleText = fmt.Sprintf("%s (%s)", titleText, pyStr(v))
	}
	if pyTruthy(i.get("parental_warning", nil)) {
		titleText += " [E]"
	}
	year := pySlice(pyStr(pyOr(i.get("release_date_original", nil), i.get("release_date", nil), "    ")), 0, 4)

	rawType := pyOr(i.get("release_type", nil), i.get("product_type", nil))
	if !pyTruthy(rawType) {
		count, duration := i.get("tracks_count", int64(0)), i.get("duration", int64(0))
		rawType = itemType
		if itemType == "album" && (pyTruthy(count) || pyTruthy(duration)) {
			long, err := pyGreaterEqual(duration, int64(1740))
			if err != nil {
				return "", err
			}
			if !long {
				if long, err = pyGreaterEqual(count, int64(7)); err != nil {
					return "", err
				}
			}
			switch {
			case long:
				rawType = "Album"
			case pyEqual(count, int64(1)):
				rawType = "Single"
			default:
				rawType = "EP"
			}
		}
	}
	rt, ok := rawType.(string)
	if !ok {
		return "", pyErr(pyAttributeError, "'%s' object has no attribute 'lower'", pyTypeName(rawType))
	}
	relType := pyTitle(rt)
	if pyLower(rt) == "ep" {
		relType = "EP"
	}

	quality := "[ CD ] 16b/44.1kHz"
	if pyTruthy(i.get("hires_streamable", nil)) {
		quality = fmt.Sprintf("[HI-RES] %sb/%skHz", pyStr(i.get("maximum_bit_depth", int64(24))),
			pyStr(i.get("maximum_sampling_rate", 96.0)))
	}
	return fmt.Sprintf("%s   %s   %s   %s   %s", alignText(pyStr(artist), 20), alignText(titleText, 35),
		alignText(relType, 8), year, quality), nil
}

// searchNameLine is the line of an artist or a playlist.
func searchNameLine(i *pyDict) string {
	count, desc := i.get("tracks_count", int64(0)), "tracks"
	if i.has("albums_count") {
		count, desc = i.get("albums_count", nil), "albums"
	}
	return fmt.Sprintf("%s   %s %s", alignText(pyStr(i.get("name", "Unknown")), 50), pyStr(count), desc)
}

// pyOr is Python's `a or b or ...`: the first true value, else the last.
func pyOr(vals ...any) any {
	for _, v := range vals[:len(vals)-1] {
		if pyTruthy(v) {
			return v
		}
	}
	return vals[len(vals)-1]
}

// pyGreaterEqual is a >= b for a number b: false for NaN, a TypeError for
// what isn't a number.
func pyGreaterEqual(a, b any) (bool, error) {
	if _, _, _, ok := pyNumber(a); !ok {
		return false, pyErr(pyTypeError, "'>=' not supported between instances of '%s' and '%s'", pyTypeName(a), pyTypeName(b))
	}
	c, ok := cmpNumbers(a, b)
	return ok && c >= 0, nil
}

// alignText is _align_text: cut to width with "..." at the end, or padded
// with spaces to width.
func alignText(text string, width int) string {
	if pyLen(text) > width {
		return pySlice(text, 0, width-3) + "..."
	}
	return pyLjust(text, width)
}

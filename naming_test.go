package qobuzdl

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// namingRoot is the download folder of naming.json, which doesn't exist, so
// the name checks walk up to /.
const namingRoot = "/ROOT"

// applyDownloadSettings sets the settings golden cases override, by their
// Python names.
func applyDownloadSettings(tb testing.TB, s *Settings, over *pyDict) {
	tb.Helper()
	for _, k := range over.keys {
		v := over.vals[k]
		switch k {
		case "multiple_disc_one_dir":
			s.MultipleDiscOneDir = v.(bool)
		case "multiple_disc_track_format":
			s.MultipleDiscTrackFormat = v.(string)
		case "multiple_disc_prefix":
			s.MultipleDiscPrefix = v.(string)
		case "fallback_folder_format":
			s.FallbackFolderFormat = v.(string)
		case "legacy_charmap":
			s.LegacyCharmap = v.(bool)
		case "disable_smart_truncation":
			s.DisableSmartTruncation = v.(bool)
		case "embed_art":
			s.EmbedArt = v.(bool)
		case "embedded_art_size":
			s.EmbeddedArtSize = v.(string)
		case "saved_art_size":
			s.SavedArtSize = v.(string)
		case "no_cover":
			s.NoCover = v.(bool)
		case "max_workers":
			s.MaxWorkers = int(v.(int64))
		default:
			tb.Fatalf("setting %s is not mapped", k)
		}
	}
}

// strOrEmpty is a template from a golden: None is Download's default.
func strOrEmpty(v any) string {
	s, _ := v.(string)
	return s
}

func patchDict(d, patch *pyDict) *pyDict {
	d = d.clone()
	for _, k := range patch.keys {
		d.set(k, cloneValue(patch.vals[k]))
	}
	return d
}

// namingAlbum is gen.py's naming_album: the recorded album with the case's
// patches on the album and on every track.
func namingAlbum(tb testing.TB, in *pyDict) *pyDict {
	tb.Helper()
	album := patchDict(fixtureDict(tb, "album-get-"+in.get("album", nil).(string)+".json"), in.get("album_patch", nil).(*pyDict))
	tracks := album.get("tracks", nil).(*pyDict)
	var items []any
	for _, t := range tracks.get("items", nil).([]any) {
		items = append(items, patchDict(t.(*pyDict), in.get("track_patch", nil).(*pyDict)))
	}
	tracks.set("items", items)
	return album
}

type namingResult struct {
	formats [3]string
	folder  string
	files   []string
	err     error
}

func runNaming(t *testing.T, in *pyDict) namingResult {
	t.Helper()
	o := DefaultOptions()
	o.Quality = int(in.get("quality", nil).(int64))
	o.FolderFormat = strOrEmpty(in.get("folder_format", nil))
	o.TrackFormat = strOrEmpty(in.get("track_format", nil))
	applyDownloadSettings(t, &o.Settings, in.get("settings", nil).(*pyDict))
	d := newDownload(nil, o, "id", namingRoot, nil)
	s := &d.opts.Settings
	gate := in.get("gate", nil).([]any)
	format, bitDepth, rate := gate[0].(string), gate[2], gate[3]
	ext := ".flac"
	if o.Quality == 5 {
		ext = ".mp3"
	}
	root := namingRoot
	var r namingResult
	fail := func(err error) namingResult { r.err = err; return r }

	var folder string
	if in.get("kind", nil) == "album" {
		album := namingAlbum(t, in)
		title, err := downloaderGetTitle(album)
		if err != nil {
			return fail(err)
		}
		attrs, err := getAlbumAttr(d.log, album, title, format, bitDepth, rate)
		if err != nil {
			return fail(err)
		}
		items, err := tracksItems(album)
		if err != nil {
			return fail(err)
		}
		if err := d.determineFormats(album, attrs, items, nil, false, format); err != nil {
			return fail(err)
		}
		if folder, err = processFolderFormatWithSubdirs(d.log, d.folderFormat, attrs, &root, s.LegacyCharmap, s.DisableSmartTruncation); err != nil {
			return fail(err)
		}
		multiple, err := pyLess(int64(1), album.get("media_count", int64(1)))
		if err != nil {
			return fail(err)
		}
		for _, it := range items {
			track := it.(*pyDict)
			var m any
			if multiple {
				m = track.get("media_number", nil)
			}
			p, err := d.trackPath(folder, track, album, false, m, ext)
			if err != nil {
				return fail(err)
			}
			r.files = append(r.files, p)
		}
	} else {
		track := patchDict(fixtureDict(t, "track-get-"+in.get("track", nil).(string)+".json"), in.get("track_patch", nil).(*pyDict))
		title, err := downloaderGetTitle(track)
		if err != nil {
			return fail(err)
		}
		attrs, err := getTrackAttr(d.log, track, title, bitDepth, rate, format)
		if err != nil {
			return fail(err)
		}
		if err := d.determineFormats(track.get("album", newDict()), nil, []any{track}, attrs, true, format); err != nil {
			return fail(err)
		}
		if folder, err = processFolderFormatWithSubdirs(d.log, d.folderFormat, attrs, &root, s.LegacyCharmap, s.DisableSmartTruncation); err != nil {
			return fail(err)
		}
		p, err := d.trackPath(folder, track, track, true, false, ext)
		if err != nil {
			return fail(err)
		}
		r.files = append(r.files, p)
	}
	r.formats = [3]string{d.folderFormat, d.trackFormat, s.MultipleDiscTrackFormat}
	r.folder = folder
	return r
}

func relToRoot(t *testing.T, p string) string {
	t.Helper()
	if p == namingRoot {
		return ""
	}
	rel, ok := strings.CutPrefix(p, namingRoot+"/")
	if !ok {
		t.Fatalf("%q is outside %s", p, namingRoot)
	}
	return rel
}

func TestNamingGolden(t *testing.T) {
	for _, c := range loadGolden(t, "naming") {
		t.Run(c.Name, func(t *testing.T) {
			r := runNaming(t, c.Input.(*pyDict))
			if c.Error != "" {
				checkGoldenError(t, c.Name, r.err, c.Error)
				return
			}
			if r.err != nil {
				t.Fatal(r.err)
			}
			want := c.Output.(*pyDict)
			for i, f := range want.get("formats", nil).([]any) {
				if r.formats[i] != f {
					t.Errorf("format %d: got %q, want %q", i, r.formats[i], f)
				}
			}
			if got, w := relToRoot(t, r.folder), want.get("folder", nil); got != w {
				t.Errorf("folder:\n got %q\nwant %q", got, w)
			}
			wantFiles := want.get("files", nil).([]any)
			if len(r.files) != len(wantFiles) {
				t.Fatalf("%d files, want %d", len(r.files), len(wantFiles))
			}
			for i, f := range r.files {
				if got := relToRoot(t, f); got != wantFiles[i] {
					t.Errorf("file %d:\n got %q\nwant %q", i, got, wantFiles[i])
				}
			}
		})
	}
}

// formatsFor runs determineFormats on the CD album with the given templates.
func formatsFor(t *testing.T, folder, track string, s func(*Settings)) (*download, error) {
	t.Helper()
	o := DefaultOptions()
	o.FolderFormat, o.TrackFormat = folder, track
	if s != nil {
		s(&o.Settings)
	}
	d := newDownload(nil, o, "id", namingRoot, nil)
	album := fixtureDict(t, "album-get-0634904080853.json")
	attrs, err := getAlbumAttr(d.log, album, "Com Lag", "FLAC", int64(16), 44.1)
	if err != nil {
		t.Fatal(err)
	}
	items, err := tracksItems(album)
	if err != nil {
		t.Fatal(err)
	}
	return d, d.determineFormats(album, attrs, items, nil, false, "FLAC")
}

func TestDetermineFormatsFallback(t *testing.T) {
	t.Run("ValueError in the folder", func(t *testing.T) {
		d, err := formatsFor(t, "{year:d}", "{track_number} - {track_title}", nil)
		if err != nil || d.folderFormat != DefaultFolder || d.trackFormat != "{track_number} - {track_title}" {
			t.Errorf("got %q %q %v", d.folderFormat, d.trackFormat, err)
		}
	})
	t.Run("KeyError in the track", func(t *testing.T) {
		d, err := formatsFor(t, "{artist}", "{nope}", nil)
		// The fallback folder replaces the configured one together with the
		// default track template.
		if err != nil || d.folderFormat != DefaultFolder || d.trackFormat != DefaultTrack {
			t.Errorf("got %q %q %v", d.folderFormat, d.trackFormat, err)
		}
	})
	t.Run("nothing works", func(t *testing.T) {
		d, err := formatsFor(t, "{year:d}", "{nope}", func(s *Settings) {
			s.FallbackFolderFormat = "{album:d}"
			s.MultipleDiscTrackFormat = "{x}"
		})
		// The last combination is the defaults, which format.
		if err != nil || d.folderFormat != DefaultFolder || d.trackFormat != DefaultTrack ||
			d.opts.Settings.MultipleDiscTrackFormat != DefaultMultipleDiscTrack {
			t.Errorf("got %q %q %q %v", d.folderFormat, d.trackFormat, d.opts.Settings.MultipleDiscTrackFormat, err)
		}
	})
	t.Run("a TypeError escapes", func(t *testing.T) {
		o := DefaultOptions()
		d := newDownload(nil, o, "id", namingRoot, nil)
		album := fixtureDict(t, "album-get-evz7usmruz35a.json")
		items, _ := tracksItems(album)
		items[3].(*pyDict).set("media_number", nil)
		attrs, err := getAlbumAttr(d.log, album, "Ummagumma", "FLAC", int64(24), int64(192))
		if err != nil {
			t.Fatal(err)
		}
		err = d.determineFormats(album, attrs, items, nil, false, "FLAC")
		if !isPy(err, pyTypeError) {
			t.Errorf("got %v, want a TypeError", err)
		}
	})
	t.Run("the call's copy changes, not the options", func(t *testing.T) {
		o := DefaultOptions()
		o.FolderFormat = "{year:d}"
		d := newDownload(nil, o, "id", namingRoot, nil)
		if d.opts.Settings.MultipleDiscTrackFormat != o.Settings.MultipleDiscTrackFormat {
			t.Fatal("settings not copied")
		}
		d.opts.Settings.MultipleDiscTrackFormat = "changed"
		if o.Settings.MultipleDiscTrackFormat == "changed" {
			t.Error("the options changed")
		}
	})
}

func TestFolderPartKeyErrorKeepsText(t *testing.T) {
	attrs := pyAttrs{"artist": "Artist", "album": "Album"}
	var logged strings.Builder
	log := textLogger(&logged)
	got, err := processFolderFormatWithSubdirs(log, "{artist}/{nope} {album}/{album}", attrs, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Artist/{nope} {album}/Album"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !strings.Contains(logged.String(), "Format error ('nope'), using original text.") {
		t.Errorf("log %q", logged.String())
	}
	if _, err := processFolderFormatWithSubdirs(log, "{artist:d}", attrs, nil, false, false); !isPy(err, pyValueError) {
		t.Errorf("a ValueError: got %v", err)
	}
}

func TestSmartTruncation(t *testing.T) {
	long := strings.Repeat("Word ", 40) // 200 characters
	attrs := pyAttrs{"album": long}
	got, err := processFolderFormatWithSubdirs(discardLogger(), "{album}", attrs, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	want := pyRstrip(pySlice(pyStrip(long, ""), 0, 60), truncStrip) + "..." + pyLstrip(pySlice(pyStrip(long, ""), -50, 200), truncStrip)
	if got != want || pyLen(got) > 120 {
		t.Errorf("folder part:\n got %q\nwant %q", got, want)
	}
	if got, _ := processFolderFormatWithSubdirs(discardLogger(), "{album}", attrs, nil, false, true); got != pyStrip(long, "") {
		t.Errorf("without truncation: %q", got)
	}

	o := DefaultOptions()
	o.TrackFormat = "{track_title}"
	track := newDict()
	track.set("title", strings.Repeat("Title ", 40))
	track.set("media_number", int64(1))
	album := newDict()
	album.set("title", "A")
	d := newDownload(nil, o, "id", "/ROOT", nil)
	p, err := d.trackPath("/ROOT", track, album, false, nil, ".flac")
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimSuffix(strings.TrimPrefix(p, "/ROOT/"), ".flac")
	if pyLen(name) > 180 || !strings.Contains(name, "...") {
		t.Errorf("track name %d characters: %q", pyLen(name), name)
	}
	o.Settings.DisableSmartTruncation = true
	d = newDownload(nil, o, "id", "/ROOT", nil)
	p, _ = d.trackPath("/ROOT", track, album, false, nil, ".flac")
	if strings.Contains(p, "...") {
		t.Errorf("without truncation: %q", p)
	}
}

func TestFitNameShortensUTF8(t *testing.T) {
	dir := t.TempDir()
	name := strings.Repeat("Ж", 200) // 400 bytes
	if !isNameTooLong(dir, name) {
		t.Skip("the file system takes names over 255 bytes")
	}
	got := fitName(dir, name, ".flac", 0)
	if len(got) > nameMaxBytes || !strings.HasSuffix(got, ".flac") || !utf8.ValidString(got) || !strings.Contains(got, "...") {
		t.Fatalf("got %d bytes: %q", len(got), got)
	}
	if err := os.WriteFile(filepath.Join(dir, got), nil, 0o644); err != nil {
		t.Fatalf("the file system refuses %q: %v", got, err)
	}
	if fitName(dir, "short", ".flac", 0) != "short.flac" {
		t.Error("a short name changed")
	}
	// A missing parent is looked up through its nearest existing folder.
	if !isNameTooLong(filepath.Join(dir, "a", "b"), name) {
		t.Error("missing parent")
	}
	withPrefix := fitName(dir, name, "", statePrefixBytes)
	if len(withPrefix)+statePrefixBytes > nameMaxBytes {
		t.Errorf("no room for the prefix: %d bytes", len(withPrefix))
	}
}

func TestShortenUTF8Golden(t *testing.T) {
	for _, c := range loadGolden(t, "name_length") {
		in := c.Input.(*pyDict)
		got := shortenUTF8(in.get("name", nil).(string), int(in.get("max_bytes", nil).(int64)))
		if got != c.Output {
			t.Errorf("%s:\n got %q\nwant %q", c.Name, got, c.Output)
		}
	}
}

func TestFormatReleaseTypeGolden(t *testing.T) {
	for _, c := range loadGolden(t, "release_type") {
		got, err := formatReleaseType(c.Input)
		if c.Error != "" {
			checkGoldenError(t, c.Name, err, c.Error)
			continue
		}
		if err != nil || got != c.Output {
			t.Errorf("%s: got %q, %v, want %q", c.Name, got, err, c.Output)
		}
	}
}

func TestTracklistGolden(t *testing.T) {
	for _, c := range loadGolden(t, "tracklist") {
		t.Run(c.Name, func(t *testing.T) {
			in := c.Input.(*pyDict)
			album, ok := in.get("album", nil).(*pyDict)
			if !ok {
				album = fixtureDict(t, "album-get-"+in.get("album", nil).(string)+".json")
			}
			o := DefaultOptions()
			o.NoCredits = in.get("no_credits", nil).(bool)
			d := newDownload(nil, o, "id", namingRoot, nil)
			gate := in.get("gate", nil).([]any)
			dir := t.TempDir()
			err := d.generateTracklist(context.Background(), album, dir, in.get("title", nil), gate[0].(string), gate[1], gate[2])
			if c.Error != "" {
				checkGoldenError(t, c.Name, err, c.Error)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			want := c.Output.([]any)
			if len(entries) != len(want) {
				t.Fatalf("%d files, want %d", len(entries), len(want))
			}
			for i, e := range entries {
				w := want[i].(*pyDict)
				data, err := os.ReadFile(filepath.Join(dir, e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if e.Name() != w.get("name", nil) {
					t.Errorf("name %q, want %q", e.Name(), w.get("name", nil))
				}
				if string(data) != w.get("text", nil) {
					t.Errorf("text:\n%s\nwant:\n%s", data, w.get("text", nil))
				}
			}
		})
	}
}

func TestTracklistKeepsExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Album - Tracklist.txt")
	if err := os.WriteFile(path, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := newDownload(nil, DefaultOptions(), "id", dir, nil)
	album := newDict()
	album.set("title", "Album")
	if err := d.generateTracklist(context.Background(), album, dir, "Album", "FLAC", int64(16), 44.1); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "mine" {
		t.Errorf("rewritten: %q", data)
	}
	// A cancelled download writes no tracklist.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	other := t.TempDir()
	if err := d.generateTracklist(ctx, album, other, "Album", "FLAC", int64(16), 44.1); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Errorf("wrote %v", entries)
	}
}

// textLogger writes every record's message to b.
func textLogger(b *strings.Builder) *slog.Logger {
	return slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

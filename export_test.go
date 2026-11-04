package qobuzdl

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Exports for the scenario tests in package qobuzdl_test, which can import
// qobuzdltest; this package can't, since qobuzdltest imports it.

// DownloadIDByType runs one album or track download, as DownloadFromID
// will.
func DownloadIDByType(ctx context.Context, c *Client, o Options, id string, track bool, p Progress) (*Release, error) {
	return newDownload(c, o, id, o.Directory, p).downloadIDByType(ctx, track)
}

// LayoutCase is a case of a layout golden.
type LayoutCase struct {
	Name, Kind, ID string
	// Configure applies the case's options on top of the fake's.
	Configure func(o *Options)
	// Check compares the tree under root with Python's.
	Check func(t *testing.T, root string)
	// CheckDB compares the downloads database's rows with the ones Python
	// wrote, saved_path relative to root; nil for goldens without them.
	CheckDB func(t *testing.T, root string, rows []map[string]any)
}

// LayoutCases reads a layout golden.
func LayoutCases(t *testing.T, golden string) []LayoutCase {
	t.Helper()
	var out []LayoutCase
	for _, c := range loadGolden(t, golden) {
		if c.Error != "" {
			t.Fatalf("%s: Python raised %s", c.Name, c.Error)
		}
		in := c.Input.(*pyDict)
		opts := in.get("options", nil).(*pyDict)
		var want []any
		var checkDB func(t *testing.T, root string, rows []map[string]any)
		switch o := c.Output.(type) {
		case []any:
			want = o
		case *pyDict:
			want = o.get("tree", nil).([]any)
			wantRows := o.get("db", nil).([]any)
			checkDB = func(t *testing.T, root string, rows []map[string]any) { checkDBRows(t, root, rows, wantRows) }
		}
		out = append(out, LayoutCase{
			Name: c.Name,
			Kind: in.get("kind", nil).(string),
			ID:   in.get("id", nil).(string),
			Configure: func(o *Options) {
				o.Quality = int(opts.get("quality", nil).(int64))
				o.QualityFallback = opts.get("fallback", nil).(bool)
				o.EmbedArt = opts.get("embed_art", nil).(bool)
				o.IgnoreSinglesEPs = opts.get("albums_only", nil).(bool)
				o.NoCredits = opts.get("no_credits", nil).(bool)
				o.NoM3UForPlaylists = opts.get("no_m3u", false).(bool)
				o.SmartDiscography = opts.get("smart", false).(bool)
				o.FolderFormat = strOrEmpty(opts.get("folder_format", nil))
				o.TrackFormat = strOrEmpty(opts.get("track_format", nil))
				applyDownloadSettings(t, &o.Settings, opts.get("settings", nil).(*pyDict))
			},
			Check:   func(t *testing.T, root string) { checkLayout(t, root, want) },
			CheckDB: checkDB,
		})
	}
	return out
}

// FLACComments returns a FLAC file's comments as KEY=value.
func FLACComments(t *testing.T, path string) []string {
	t.Helper()
	b, err := readFLACBlocks(path)
	if err != nil {
		t.Fatal(err)
	}
	_, list, _ := b.comments()
	return list
}

// FLACPictures counts a FLAC file's PICTURE blocks.
func FLACPictures(t *testing.T, path string) int {
	t.Helper()
	pics, _ := flacResult(t, path).get("pictures", nil).([]any)
	return len(pics)
}

// ErrDemo and ErrAllTiers are the failures of a demo track and of a track
// no tier could deliver.
var (
	ErrDemo     = errDemo
	ErrAllTiers = errAllTiers
)

// FLACAudio returns a FLAC file's audio frames, the bytes after its
// metadata blocks.
func FLACAudio(t *testing.T, path string) []byte {
	t.Helper()
	b, err := readFLACBlocks(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data[b.audio:]
}

// FLACStreamInfo is a FLAC file's bit depth, rate and sample count.
func FLACStreamInfo(t *testing.T, path string) (bitDepth, rate int, samples int64) {
	t.Helper()
	bitDepth, rate, samples, err := flacStreamInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	return bitDepth, rate, samples
}

// checkDBRows compares rows read from a downloads database, in id and
// quality order, with gen.py's.
func checkDBRows(t *testing.T, root string, got []map[string]any, want []any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		w := w.(*pyDict)
		g := got[i]
		if p, ok := g["saved_path"].(string); ok {
			if rest, ok := strings.CutPrefix(p, root+"/"); ok {
				g["saved_path"] = rest
			}
		}
		if len(g) != len(w.keys) {
			t.Errorf("row %d: columns %v, want %v", i, g, w.keys)
		}
		for _, k := range w.keys {
			if gv, wv := g[k], w.get(k, nil); !pyEqual(gv, wv) || pyTypeName(gv) != pyTypeName(wv) {
				t.Errorf("row %d (%v): %s = %s, want %s", i, g["id"], k, pyRepr(gv), pyRepr(wv))
			}
		}
	}
}

// SearchGolden is search_text.json: each case's item type, its items and
// Python's lines, or the error's class name.
func SearchGolden(t *testing.T) map[string]struct {
	Type  string
	Lines []SearchResult
	Error string
} {
	t.Helper()
	out := map[string]struct {
		Type  string
		Lines []SearchResult
		Error string
	}{}
	for _, c := range loadGolden(t, "search_text") {
		e := out[c.Name]
		e.Type = c.Input.(*pyDict).get("type", nil).(string)
		e.Error = c.Error
		if list, ok := c.Output.([]any); ok {
			for _, r := range list {
				r := r.(*pyDict)
				e.Lines = append(e.Lines, SearchResult{Text: r.get("text", nil).(string), URL: r.get("url", nil).(string)})
			}
		}
		out[c.Name] = e
	}
	return out
}

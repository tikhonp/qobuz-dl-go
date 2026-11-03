package qobuzdl

import (
	"context"
	"testing"
)

// Exports for the scenario tests in package qobuzdl_test, which can import
// qobuzdltest; this package can't, since qobuzdltest imports it.

// DownloadIDByType runs one album or track download, as DownloadFromID
// will.
func DownloadIDByType(ctx context.Context, c *Client, o Options, id string, track bool, p Progress) (*Release, error) {
	return newDownload(c, o, id, o.Directory, p).downloadIDByType(ctx, track)
}

// LayoutCase is a case of layout_album.json or layout_track.json.
type LayoutCase struct {
	Name, Kind, ID string
	// Configure applies the case's options on top of the fake's.
	Configure func(o *Options)
	// Check compares the tree under root with Python's.
	Check func(t *testing.T, root string)
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
		want := c.Output.([]any)
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
				o.FolderFormat = strOrEmpty(opts.get("folder_format", nil))
				o.TrackFormat = strOrEmpty(opts.get("track_format", nil))
				applyDownloadSettings(t, &o.Settings, opts.get("settings", nil).(*pyDict))
			},
			Check: func(t *testing.T, root string) { checkLayout(t, root, want) },
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

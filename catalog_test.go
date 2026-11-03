package qobuzdl

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// fixtureDict reads a recorded API answer as the client sees it, normalized.
func fixtureDict(tb testing.TB, name string) *pyDict {
	tb.Helper()
	v, err := decodeJSON(strings.NewReader(readFixture(tb, "api", name)))
	if err != nil {
		tb.Fatal(err)
	}
	return normalizeJSONStrings(v).(*pyDict)
}

func TestCatalogFromFixtures(t *testing.T) {
	a, err := albumFromDict(fixtureDict(t, "album-get-evz7usmruz35a.json"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "evz7usmruz35a" || a.Title != "Ummagumma" || a.Artist.Name != "Pink Floyd" || a.MediaCount != 2 ||
		a.TracksCount != 16 || len(a.Tracks) != 16 || a.TracksTotal != 16 || a.MaximumBitDepth != 24 ||
		a.MaximumSamplingRate != 192 || a.Label.Name == "" || a.Image.Large == "" || a.Genre == "" {
		t.Errorf("album %+v", a)
	}
	last := a.Tracks[15]
	if last.MediaNumber != 2 || last.ID == "" || last.Title == "" || last.ISRC == "" || last.Album != nil {
		t.Errorf("last track %+v", last)
	}

	g, err := albumFromDict(fixtureDict(t, "album-get-el3qez8zpc8bc.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Goodies) != 1 || g.Goodies[0].ID != "168983" || !strings.HasPrefix(g.Goodies[0].URL, "https://") ||
		g.Version != "" || g.Image.Back != "" || g.ProductType != "single" || g.ReleaseType != "album" {
		t.Errorf("album with goodies %+v", g)
	}
	if len(g.Artists) != 2 || g.Artists[1].Name != "Budapest Festival Orchestra" ||
		!slices.Equal(g.Artists[0].Roles, []string{"main-artist", "conductor"}) {
		t.Errorf("artists %+v", g.Artists)
	}

	tr, err := trackFromDict(fixtureDict(t, "track-get-20273753.json"))
	if err != nil {
		t.Fatal(err)
	}
	if tr.ID != "20273753" || tr.Version != "Mono Version" || tr.Performer.Name != "Miles Davis" || tr.Work != "" ||
		tr.MaximumSamplingRate != 96 || tr.Album == nil || tr.Album.ID == "" || tr.Album.Title == "" {
		t.Errorf("track %+v", tr)
	}

	page := fixtureDict(t, "playlist-get-2082798-0.json")
	p, err := playlistFromPages([]*pyDict{page, page})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "2082798" || p.Name != "Top 10 Operas" || p.Owner != "gramophonemagazine" || p.TracksCount != 10 ||
		len(p.Tracks) != 20 || len(p.Images) != 4 || p.Tracks[0].Album == nil {
		t.Errorf("playlist %+v with %d tracks", p, len(p.Tracks))
	}

	apage := fixtureDict(t, "artist-get-50782-0.json")
	ar, err := artistFromPages([]*pyDict{apage, apage})
	if err != nil {
		t.Fatal(err)
	}
	if ar.ID != "50782" || ar.Name != "Tord Gustavsen Trio" || ar.AlbumsCount != 8 || len(ar.Albums) != 16 ||
		ar.Image.Large == "" || ar.Albums[0].Title == "" {
		t.Errorf("artist %+v with %d albums", ar, len(ar.Albums))
	}

	l, err := labelFromPages([]*pyDict{fixtureDict(t, "label-get-572190-0.json")})
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "572190" || l.Name != "Naked Jain Records" || l.AlbumsCount != 15 || len(l.Albums) != 15 {
		t.Errorf("label %+v", l)
	}

	for _, tc := range []struct{ file, key string }{
		{"catalog-search-albums.json", "albums"}, {"catalog-search-tracks.json", "tracks"},
		{"catalog-search-artists.json", "artists"}, {"catalog-search-playlists.json", "playlists"},
	} {
		d := fixtureDict(t, tc.file)
		var n, total int
		switch tc.key {
		case "albums":
			sp, err := searchPageFromDict(d, tc.key, albumFromDict)
			if err != nil {
				t.Fatal(err)
			}
			n, total = len(sp.Items), sp.Total
		case "tracks":
			sp, err := searchPageFromDict(d, tc.key, trackFromDict)
			if err != nil {
				t.Fatal(err)
			}
			n, total = len(sp.Items), sp.Total
			if sp.Items[0].Album == nil {
				t.Error("search track without its album")
			}
		case "artists":
			sp, err := searchPageFromDict(d, tc.key, artistFromDict)
			if err != nil {
				t.Fatal(err)
			}
			n, total = len(sp.Items), sp.Total
		case "playlists":
			sp, err := searchPageFromDict(d, tc.key, playlistFromDict)
			if err != nil {
				t.Fatal(err)
			}
			n, total = len(sp.Items), sp.Total
		}
		if n != 10 || total < n {
			t.Errorf("%s: %d items of %d", tc.file, n, total)
		}
	}
}

func TestCatalogEmptyCollections(t *testing.T) {
	if _, err := playlistFromPages(nil); err == nil {
		t.Error("playlist without pages")
	}
	if _, err := artistFromPages(nil); err == nil {
		t.Error("artist without pages")
	}
	if _, err := labelFromPages(nil); err == nil {
		t.Error("label without pages")
	}
}

func TestCatalogRawKeepsOrder(t *testing.T) {
	d := fixtureDict(t, "album-get-k43q1q3kx88fa.json")
	a, err := albumFromDict(d)
	if err != nil {
		t.Fatal(err)
	}
	want, err := d.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Raw, want) || !json.Valid(a.Raw) {
		t.Error("Raw differs from the dict")
	}
}

func TestTrackURLRawIsClean(t *testing.T) {
	d := fixtureDict(t, "track-getFileUrl-20273753-27.json")
	d.set("url_template", "https://cdn.test/$SEGMENT$?sig=1")
	d.set("key", "qbz-1.a.b")
	d.set("raw_key", "secret")
	u, err := trackURLFromDict(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"url"`, `"url_template"`, `"key"`, `"raw_key"`, `"blob"`} {
		if bytes.Contains(u.Raw, []byte(k)) {
			t.Errorf("Raw has %s: %s", k, u.Raw)
		}
	}
	if u.URL == "" || u.FormatID != 7 || u.BitDepth != 24 || u.SamplingRate != 96 || u.Segmented ||
		!slices.Equal(u.Restrictions, []string{"FormatRestrictedByFormatAvailability"}) || u.MimeType != "audio/flac" {
		t.Errorf("track URL %+v", u)
	}
	if !bytes.Contains(u.Raw, []byte(`"restrictions"`)) {
		t.Errorf("Raw lost restrictions: %s", u.Raw)
	}
	seg := newDict()
	seg.set("url_template", "https://cdn.test/$SEGMENT$")
	seg.set("n_segments", int64(12))
	s, err := trackURLFromDict(seg)
	if err != nil || !s.Segmented || s.NSegments != 12 {
		t.Errorf("segmented %+v, %v", s, err)
	}
}

func TestGetAlbumArtistTyped(t *testing.T) {
	n := 0
	for _, c := range loadGolden(t, "album_artist") {
		if c.Error != "" {
			continue
		}
		d := c.Input.(*pyDict)
		names, err := getAlbumArtist(discardLogger(), d)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		want := make([]string, len(names))
		for i, v := range names {
			want[i] = pyStr(v)
		}
		a, err := albumFromDict(d)
		if err != nil {
			t.Fatal(err)
		}
		n++
		if got := GetAlbumArtist(a); !slices.Equal(got, want) {
			t.Errorf("%s: got %q, want %q", c.Name, got, want)
		}
	}
	if n == 0 {
		t.Fatal("no cases")
	}
	a := &Album{Artist: ArtistRef{Name: "Solo"}, Artists: []ArtistRef{{Name: "Guest", Roles: []string{"featured-artist"}}}}
	if got := GetAlbumArtist(a); !slices.Equal(got, []string{"Solo"}) {
		t.Errorf("album without Raw: %q", got)
	}
}

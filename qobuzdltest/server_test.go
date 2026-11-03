package qobuzdltest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
)

func newClient(t *testing.T, s *Server) *qobuzdl.Client {
	t.Helper()
	c, err := qobuzdl.NewClient(t.Context(), s.ClientOptions())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// apiRequests is the log's API calls to one endpoint.
func apiRequests(s *Server, endpoint string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Path == apiPrefix+endpoint {
			out = append(out, r)
		}
	}
	return out
}

func get(t *testing.T, s *Server, url, rng string) (*http.Response, []byte, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := s.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return resp, body, err
}

func audio(t *testing.T, name string) []byte {
	t.Helper()
	data, err := fs.ReadFile(embedded, "testdata/audio/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFakeAuthAndSecretChoice(t *testing.T) {
	s := New(t)
	c := newClient(t, s)
	if c.AppID() != AppID || c.Label() != "Studio" || c.UserID() != UserID {
		t.Errorf("app ID %q, label %q, user ID %q", c.AppID(), c.Label(), c.UserID())
	}
	reqs := s.Requests()
	var got []string
	for _, r := range reqs {
		got = append(got, fmt.Sprintf("%s %d", strings.TrimPrefix(r.Path, apiPrefix), r.Status))
	}
	want := []string{"user/get 200", "track/getFileUrl 400", "track/getFileUrl 200"}
	if !slices.Equal(got, want) {
		t.Fatalf("requests %q, want %q", got, want)
	}
	for i, secret := range []string{BadSecret, Secret} {
		q := reqs[i+1].Query
		if q.Get("request_sig") != getFileURLSig(q.Get("track_id"), q.Get("format_id"), q.Get("request_ts"), secret) {
			t.Errorf("getFileUrl %d not signed with secret %d", i, i)
		}
	}
	if reqs[0].Header.Get("X-User-Auth-Token") != Token {
		t.Errorf("user/get carried token %q", reqs[0].Header.Get("X-User-Auth-Token"))
	}
	// Signed calls now pass, which they do only with Secret.
	if _, err := c.GetTrackURL(t.Context(), TrackSingle, 27, false); err != nil {
		t.Fatal(err)
	}

	// Without an app ID the client reads the bundle and takes all three of
	// its secrets, the empty one included.
	s.Reset()
	o := s.ClientOptions()
	o.AppID, o.Secrets = "", nil
	c, err := qobuzdl.NewClient(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	if c.AppID() != AppID {
		t.Errorf("app ID %q from the bundle", c.AppID())
	}
	got = nil
	for _, r := range s.Requests() {
		got = append(got, fmt.Sprintf("%s %d", r.Path, r.Status))
	}
	want = []string{"/login 200", "/resources/8.1.0-b019/bundle.js 200", apiPrefix + "user/get 200",
		apiPrefix + "track/getFileUrl 400", apiPrefix + "track/getFileUrl 200"}
	if !slices.Equal(got, want) {
		t.Errorf("requests %q, want %q", got, want)
	}
}

func TestFakeRejectsWrongAppIDAndToken(t *testing.T) {
	s := New(t)
	o := s.ClientOptions()
	o.UserAuthToken = "x" + Token
	if _, err := qobuzdl.NewClient(t.Context(), o); !errors.Is(err, qobuzdl.ErrAuthentication) {
		t.Errorf("a wrong token: got %v, want ErrAuthentication", err)
	}
	o = s.ClientOptions()
	o.AppID = "987654321"
	c, err := qobuzdl.NewClient(t.Context(), o)
	if err != nil {
		t.Fatal(err) // user/get's 400 is the profile bypass, as in Python
	}
	var ae *qobuzdl.APIError
	if _, err := c.GetAlbumMeta(t.Context(), AlbumCD); !errors.As(err, &ae) || ae.Status != http.StatusBadRequest {
		t.Errorf("a wrong app ID: got %v, want a 400", err)
	}
}

func TestFakeBundle(t *testing.T) {
	s := New(t)
	b, err := qobuzdl.FetchBundle(t.Context(), s.ClientOptions().HTTPClient, s.URL)
	if err != nil {
		t.Fatal(err)
	}
	id, err := b.AppID()
	if err != nil || id != AppID {
		t.Errorf("app ID %q, %v", id, err)
	}
	secrets, err := b.Secrets()
	if err != nil {
		t.Fatal(err)
	}
	want := []qobuzdl.Secret{{Timezone: "berlin", Value: BadSecret}, {Timezone: "london", Value: Secret}, {Timezone: "abidjan", Value: ""}}
	if !slices.Equal(secrets, want) {
		t.Errorf("secrets %q, want %q", secrets, want)
	}
}

// index is testdata/api/index.json.
type index struct {
	Items struct {
		Albums                         map[string]string
		Track, Playlist, Artist, Label string
	}
	Requests []struct {
		Endpoint string
		Params   map[string]any
		File     string
	}
}

func readIndex(t *testing.T) index {
	t.Helper()
	data, err := fs.ReadFile(embedded, "testdata/api/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var ix index
	if err := json.Unmarshal(data, &ix); err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestFakeConstantsMatchIndex(t *testing.T) {
	ix := readIndex(t)
	got := map[string]string{
		"multi_disc": AlbumMultiDisc, "hires_96": AlbumHiRes96, "cd": AlbumCD, "compilation": AlbumCompilation,
		"with_version": AlbumWithVersion, "classical": AlbumClassical, "goodies": AlbumGoodies,
	}
	for k, v := range ix.Items.Albums {
		if got[k] != v {
			t.Errorf("album %s: constant %q, index %q", k, got[k], v)
		}
	}
	if len(got) != len(ix.Items.Albums) {
		t.Errorf("%d album constants, %d albums in the index", len(got), len(ix.Items.Albums))
	}
	if ix.Items.Track != TrackSingle || ix.Items.Playlist != Playlist || ix.Items.Artist != Artist || ix.Items.Label != Label {
		t.Errorf("index items %+v", ix.Items)
	}
}

func listLen(t *testing.T, o *object, key string) int {
	t.Helper()
	sub := o.object(key)
	if sub == nil {
		t.Fatalf("no %s in the fixture", key)
	}
	items, _ := sub.vals["items"].([]any)
	return len(items)
}

func TestFakeServesFixtures(t *testing.T) {
	s := New(t)
	c := newClient(t, s)
	ctx := t.Context()
	ix := readIndex(t)
	for _, r := range ix.Requests {
		fx, ok, err := fixture(r.File)
		if !ok || err != nil {
			t.Fatalf("%s: %v", r.File, err)
		}
		id := fmt.Sprint(r.Params["id"])
		switch r.Endpoint {
		case "album/get":
			a, err := c.GetAlbumMeta(ctx, id)
			if err != nil {
				t.Fatalf("album %s: %v", id, err)
			}
			if a.ID != id || len(a.Tracks) != listLen(t, fx, "tracks") || len(a.Tracks) == 0 {
				t.Errorf("album %s: ID %q, %d tracks", id, a.ID, len(a.Tracks))
			}
			if !strings.HasPrefix(a.Image.Large, s.URL+staticPrefix) {
				t.Errorf("album %s: cover %q not on the fake", id, a.Image.Large)
			}
			for _, g := range a.Goodies {
				if !strings.HasPrefix(g.URL, s.URL+staticPrefix) {
					t.Errorf("album %s: goody %q not on the fake", id, g.URL)
				}
			}
		case "track/get":
			tr, err := c.GetTrackMeta(ctx, id)
			if err != nil || tr.ID != id || tr.Album == nil {
				t.Errorf("track %s: %+v, %v", id, tr, err)
			}
		case "playlist/get":
			p, err := c.GetPlaylistMeta(ctx, id)
			if err != nil || p.ID != id || len(p.Tracks) != listLen(t, fx, "tracks") {
				t.Errorf("playlist %s: %v", id, err)
			}
		case "artist/get":
			a, err := c.GetArtistMeta(ctx, id)
			if err != nil || a.ID != id || len(a.Albums) != listLen(t, fx, "albums") {
				t.Errorf("artist %s: %v", id, err)
			}
		case "label/get":
			l, err := c.GetLabelMeta(ctx, id)
			if err != nil || l.ID != id || len(l.Albums) != listLen(t, fx, "albums") {
				t.Errorf("label %s: %v", id, err)
			}
		case "catalog/search":
			typ, query := fmt.Sprint(r.Params["type"]), fmt.Sprint(r.Params["query"])
			limit := int(r.Params["limit"].(float64))
			var n, total int
			switch typ {
			case "albums":
				p, e := c.SearchAlbums(ctx, query, limit, 0)
				if err = e; e == nil {
					n, total = len(p.Items), p.Total
				}
			case "tracks":
				p, e := c.SearchTracks(ctx, query, limit, 0)
				if err = e; e == nil {
					n, total = len(p.Items), p.Total
				}
			case "artists":
				p, e := c.SearchArtists(ctx, query, limit, 0)
				if err = e; e == nil {
					n, total = len(p.Items), p.Total
				}
			case "playlists":
				p, e := c.SearchPlaylists(ctx, query, limit, 0)
				if err = e; e == nil {
					n, total = len(p.Items), p.Total
				}
			}
			wantTotal, _ := strconv.Atoi(fx.object(typ).str("total"))
			if err != nil || n != listLen(t, fx, typ) || total != wantTotal {
				t.Errorf("search %s: %d items, total %d, %v", typ, n, total, err)
			}
		case "track/getFileUrl":
			fmtID := int(r.Params["format_id"].(float64))
			u, err := c.GetTrackURL(ctx, id, fmtID, false)
			if err != nil {
				t.Errorf("getFileUrl %s at %d: %v", id, fmtID, err)
				continue
			}
			delivered, _ := strconv.Atoi(fx.str("format_id"))
			rate, _ := strconv.ParseFloat(fx.str("sampling_rate"), 64)
			depth, _ := strconv.Atoi(fx.str("bit_depth"))
			var codes []string
			if rs, ok := fx.vals["restrictions"].([]any); ok {
				for _, x := range rs {
					codes = append(codes, x.(*object).str("code"))
				}
			}
			if u.FormatID != delivered || u.SamplingRate != rate || u.BitDepth != depth ||
				u.MimeType != fx.str("mime_type") || !slices.Equal(u.Restrictions, codes) || u.Sample {
				t.Errorf("getFileUrl %s at %d: %+v", id, fmtID, u)
			}
			if u.URL != s.cdnURL(id, delivered) {
				t.Errorf("getFileUrl %s at %d: url %q", id, fmtID, u.URL)
			}
		default:
			t.Errorf("no check for %s", r.Endpoint)
		}
	}

	// Each format's file, a cover and a booklet come from the fake.
	for fmtID, f := range formats {
		u, err := c.GetTrackURL(ctx, TrackSingle, fmtID, false)
		if err != nil {
			t.Fatal(err)
		}
		resp, body, err := get(t, s, u.URL, "")
		if err != nil || resp.StatusCode != 200 || string(body) != string(audio(t, formats[u.FormatID].file)) {
			t.Errorf("format %d (%s): %d, %v", fmtID, f.file, resp.StatusCode, err)
		}
	}
	a, err := c.GetAlbumMeta(ctx, AlbumGoodies)
	if err != nil {
		t.Fatal(err)
	}
	for u, file := range map[string]string{
		strings.Replace(a.Image.Large, "_600.", "_org.", 1): "cover.jpg",
		a.Goodies[0].URL: "booklet.pdf",
	} {
		resp, body, err := get(t, s, u, "")
		if err != nil || resp.StatusCode != 200 || string(body) != string(audio(t, file)) {
			t.Errorf("%s: %d, %v", u, resp.StatusCode, err)
		}
	}

	// Unknown IDs get Qobuz's 404.
	var ae *qobuzdl.APIError
	if _, err := c.GetAlbumMeta(ctx, "nope"); !errors.As(err, &ae) || ae.Status != 404 {
		t.Errorf("unknown album: %v", err)
	}
}

func TestFakeTrackGetForListedTracks(t *testing.T) {
	s := New(t)
	c := newClient(t, s)
	cat, err := buildCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, albumID := range []string{AlbumMultiDisc, AlbumCD, AlbumClassical} {
		a, err := c.GetAlbumMeta(t.Context(), albumID)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range a.Tracks {
			tr, err := c.GetTrackMeta(t.Context(), want.ID)
			if err != nil || tr.Title != want.Title || tr.Album == nil || tr.Album.ID != albumID || len(tr.Album.Tracks) != 0 {
				t.Errorf("album %s track %s: %+v, %v", albumID, want.ID, tr, err)
			}
		}
	}
	p, err := c.GetPlaylistMeta(t.Context(), Playlist)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range p.Tracks {
		tr, err := c.GetTrackMeta(t.Context(), want.ID)
		if err != nil || tr.Title != want.Title || tr.Album == nil || tr.Album.ID != want.Album.ID {
			t.Errorf("playlist track %s: %+v, %v", want.ID, tr, err)
		}
		if src := cat.tracks[want.ID]; src.album == nil {
			o, _ := cat.trackGet(want.ID)
			for _, k := range []string{"position", "created_at", "playlist_track_id"} {
				if _, ok := o.get(k); ok {
					t.Errorf("playlist track %s keeps %s", want.ID, k)
				}
			}
		}
	}
}

func TestFakePaging(t *testing.T) {
	s := New(t)
	c := newClient(t, s)
	ctx := t.Context()

	p, err := c.GetPlaylistMeta(ctx, PlaylistLong)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tracks) != 120 || p.TracksCount != 120 {
		t.Errorf("long playlist: %d tracks, count %d", len(p.Tracks), p.TracksCount)
	}
	a, err := c.GetArtistMeta(ctx, ArtistLong)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Albums) != 120 || a.AlbumsCount != 120 {
		t.Errorf("long artist: %d albums, count %d", len(a.Albums), a.AlbumsCount)
	}
	for _, ep := range []string{"playlist/get", "artist/get"} {
		var pages []string
		for _, r := range apiRequests(s, ep) {
			pages = append(pages, r.Query.Get("offset")+"+"+r.Query.Get("limit"))
		}
		if want := []string{"0+50", "50+50", "100+50"}; !slices.Equal(pages, want) {
			t.Errorf("%s pages %q, want %q", ep, pages, want)
		}
	}

	// A page past the end is empty and keeps the total.
	raw := pageOf(t, s, "playlist/get", "playlist_id", Playlist, 50)
	if items := raw.object("tracks").vals["items"].([]any); len(items) != 0 || raw.object("tracks").str("total") != "10" {
		t.Errorf("page past the end: %d items, total %s", len(items), raw.object("tracks").str("total"))
	}

	if _, err := c.GetPlaylistMeta(ctx, PlaylistEmpty); !errors.Is(err, qobuzdl.ErrEmptyContent) {
		t.Errorf("empty playlist: %v", err)
	}
	if _, err := c.GetArtistMeta(ctx, ArtistEmpty); !errors.Is(err, qobuzdl.ErrEmptyContent) {
		t.Errorf("empty artist: %v", err)
	}
	if _, err := c.GetLabelMeta(ctx, LabelEmpty); !errors.Is(err, qobuzdl.ErrEmptyContent) {
		t.Errorf("empty label: %v", err)
	}
}

// pageOf fetches one page of a collection with a plain request.
func pageOf(t *testing.T, s *Server, endpoint, key, id string, offset int) *object {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		fmt.Sprintf("%s%s%s?%s=%s&offset=%d&limit=50", s.URL, apiPrefix, endpoint, key, id, offset), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-App-Id", AppID)
	req.Header.Set("X-User-Auth-Token", Token)
	resp, err := s.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	v, err := decode(body)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*object)
}

func TestFakeRejectsBadSignature(t *testing.T) {
	s := New(t)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.URL+apiPrefix+
		"track/getFileUrl?track_id=1&format_id=27&intent=stream&request_ts=946684800&request_sig="+
		getFileURLSig("1", "27", "946684800", BadSecret), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-App-Id", AppID)
	req.Header.Set("X-User-Auth-Token", Token)
	resp, err := s.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "request_sig") {
		t.Errorf("%d %s", resp.StatusCode, body)
	}

	// A client whose only secret is bad keeps it, and its signed calls fail.
	o := s.ClientOptions()
	o.Secrets = []string{BadSecret}
	c, err := qobuzdl.NewClient(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	var ae *qobuzdl.APIError
	_, err = c.GetTrackURL(t.Context(), TrackSingle, 5, false)
	if !errors.Is(err, qobuzdl.ErrInvalidAppSecret) || !errors.As(err, &ae) || ae.Status != http.StatusBadRequest {
		t.Errorf("got %v, want ErrInvalidAppSecret around a 400", err)
	}
}

func TestFakeCDNRange(t *testing.T) {
	s := New(t)
	file := audio(t, "16-44.1.flac")
	u := s.cdnURL("42", 6)

	resp, body, err := get(t, s, u, "bytes=100-")
	if err != nil || resp.StatusCode != http.StatusPartialContent || string(body) != string(file[100:]) {
		t.Errorf("Range: %d, %d bytes, %v", resp.StatusCode, len(body), err)
	}
	if cr, want := resp.Header.Get("Content-Range"), fmt.Sprintf("bytes 100-%d/%d", len(file)-1, len(file)); cr != want {
		t.Errorf("Content-Range %q, want %q", cr, want)
	}
	resp, _, _ = get(t, s, u, fmt.Sprintf("bytes=%d-", len(file)))
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("Range past the end: %d, want 416", resp.StatusCode)
	}

	s.IgnoreRange("42")
	resp, body, err = get(t, s, u, "bytes=100-")
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != string(file) {
		t.Errorf("IgnoreRange: %d, %d bytes, %v", resp.StatusCode, len(body), err)
	}
	// Other tracks keep their ranges.
	if resp, _, _ := get(t, s, s.cdnURL("43", 6), "bytes=100-"); resp.StatusCode != http.StatusPartialContent {
		t.Errorf("another track: %d", resp.StatusCode)
	}

	var ranges []string
	for _, r := range s.Requests() {
		ranges = append(ranges, r.Range)
	}
	if want := []string{"bytes=100-", fmt.Sprintf("bytes=%d-", len(file)), "bytes=100-", "bytes=100-"}; !slices.Equal(ranges, want) {
		t.Errorf("logged ranges %q, want %q", ranges, want)
	}
	if q := s.Requests()[0].Query; q.Get("hmac") == "" {
		t.Errorf("the log lost the CDN query: %v", q)
	}
}

func TestFakeCDNFailures(t *testing.T) {
	s := New(t)
	file := audio(t, "24-96.flac")
	u := s.cdnURL("7", 7)

	s.Fail404("7")
	for range 2 {
		if resp, _, _ := get(t, s, u, ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("Fail404: %d", resp.StatusCode)
		}
	}
	s.Reset()

	s.FailTimes("7", 2, http.StatusServiceUnavailable)
	var codes []int
	for range 3 {
		resp, _, _ := get(t, s, u, "")
		codes = append(codes, resp.StatusCode)
	}
	if want := []int{503, 503, 200}; !slices.Equal(codes, want) {
		t.Errorf("FailTimes: %v, want %v", codes, want)
	}

	s.CutAfter("7", 1000)
	resp, body, err := get(t, s, u, "")
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(body) != 1000 || resp.ContentLength != int64(len(file)) {
		t.Errorf("CutAfter: %d of %d bytes, %v", len(body), resp.ContentLength, err)
	}
	if string(body) != string(file[:1000]) {
		t.Error("CutAfter: the bytes before the cut differ")
	}
	resp, body, err = get(t, s, u, "bytes=1000-")
	if err != nil || resp.StatusCode != http.StatusPartialContent || string(body) != string(file[1000:]) {
		t.Errorf("after the cut: %d, %d bytes, %v", resp.StatusCode, len(body), err)
	}
	if st := s.Requests()[len(s.Requests())-2].Status; st != http.StatusOK {
		t.Errorf("the cut answer logged status %d", st)
	}
}

func TestFakeQualitySwitches(t *testing.T) {
	s := New(t)
	c := newClient(t, s)
	ctx := t.Context()
	trackURL := func(fmtID int) *qobuzdl.TrackURL {
		t.Helper()
		u, err := c.GetTrackURL(ctx, TrackSingle, fmtID, false)
		if err != nil {
			t.Fatalf("format %d: %v", fmtID, err)
		}
		return u
	}

	s.Sample(TrackSingle, 7)
	if !trackURL(7).Sample || trackURL(6).Sample {
		t.Error("Sample: not at 7 alone")
	}

	s.Restrict(TrackSingle, 6)
	u := trackURL(6)
	if u.FormatID != 5 || u.BitDepth != 16 || u.SamplingRate != 44.1 || u.MimeType != "audio/mpeg" ||
		!slices.Equal(u.Restrictions, []string{downgradeCode}) || u.URL != s.cdnURL(TrackSingle, 5) {
		t.Errorf("Restrict 6: %+v", u)
	}
	s.Restrict(TrackSingle, 5)
	if u := trackURL(5); u.FormatID != 5 || !slices.Equal(u.Restrictions, []string{downgradeCode}) {
		t.Errorf("Restrict 5: %+v", u)
	}

	s.Reset()
	s.Deliver(TrackSingle, 5)
	for _, fmtID := range []int{27, 7, 6} {
		u := trackURL(fmtID)
		if u.FormatID != 5 || u.MimeType != "audio/mpeg" || len(u.Restrictions) != 0 {
			t.Errorf("Deliver 5 at %d: %+v", fmtID, u)
		}
		resp, body, err := get(t, s, u.URL, "")
		if err != nil || resp.StatusCode != 200 || string(body) != string(audio(t, "320.mp3")) {
			t.Errorf("Deliver 5 at %d: the CDN served %d bytes, %v", fmtID, len(body), err)
		}
	}

	s.Reset()
	s.EmptyAnswer(TrackSingle)
	if u := trackURL(5); u.URL != "" || u.FormatID != 0 || string(u.Raw) != "{}" {
		t.Errorf("EmptyAnswer: %+v", u)
	}
	// Without a url the client goes on to the segmented path.
	if _, err := c.GetTrackURL(ctx, TrackSingle, 27, false); err == nil {
		t.Error("EmptyAnswer at 27: no error")
	}

	s.Reset()
	if u := trackURL(27); u.FormatID != 7 || u.Sample || !slices.Equal(u.Restrictions, []string{downgradeCode}) {
		t.Errorf("after Reset: %+v", u)
	}
	// A track nothing recorded gets an answer from the format table.
	u, err := c.GetTrackURL(ctx, "1", 27, false)
	if err != nil || u.FormatID != 27 || u.BitDepth != 24 || u.SamplingRate != 192 || len(u.Restrictions) != 0 {
		t.Errorf("built answer: %+v, %v", u, err)
	}
}

func TestFakeExpireToken(t *testing.T) {
	s := New(t)
	c := newClient(t, s)
	ctx := t.Context()
	u, err := c.GetTrackURL(ctx, TrackSingle, 6, false)
	if err != nil {
		t.Fatal(err)
	}

	s.ExpireToken()
	var ae *qobuzdl.APIError
	if _, err := c.GetAlbumMeta(ctx, AlbumCD); !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
		t.Errorf("album/get: %v, want a 401", err)
	}
	if _, err := c.SearchAlbums(ctx, "miles", 10, 0); !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
		t.Errorf("catalog/search: %v, want a 401", err)
	}
	if _, err := c.GetTrackURL(ctx, TrackSingle, 5, false); !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
		t.Errorf("getFileUrl: %v, want a 401", err)
	}
	if _, err := qobuzdl.NewClient(ctx, s.ClientOptions()); !errors.Is(err, qobuzdl.ErrAuthentication) {
		t.Errorf("NewClient: %v, want ErrAuthentication", err)
	}
	// The bundle and the CDN don't use the token.
	if _, err := qobuzdl.FetchBundle(ctx, s.ClientOptions().HTTPClient, s.URL); err != nil {
		t.Errorf("bundle: %v", err)
	}
	if resp, _, err := get(t, s, u.URL, ""); err != nil || resp.StatusCode != 200 {
		t.Errorf("CDN: %d, %v", resp.StatusCode, err)
	}

	s.Reset()
	if _, err := c.GetAlbumMeta(ctx, AlbumCD); err != nil {
		t.Errorf("after Reset: %v", err)
	}
}

func TestFakeConfigure(t *testing.T) {
	s := New(t)
	o := qobuzdl.DefaultOptions()
	s.Configure(&o)
	if o.APIBase != s.URL+apiPrefix || o.PlayBase != s.URL || o.HTTPClient == nil || o.Settings.UserAuthToken != Token {
		t.Errorf("options %+v", o)
	}
	if o.TrackPause != 0 || len(o.RetryDelays) != 4 || slices.ContainsFunc(o.RetryDelays, func(d time.Duration) bool { return d != 0 }) {
		t.Errorf("sleeps: pause %v, retries %v", o.TrackPause, o.RetryDelays)
	}
	if o.HTTPClient.Timeout != 0 {
		t.Errorf("the client has a Timeout of %v", o.HTTPClient.Timeout)
	}
}

func TestFakeSwitchesUnderConcurrency(t *testing.T) {
	s := New(t)
	c := newClient(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 50 {
			s.Sample(TrackSingle, 7)
			s.CutAfter(TrackSingle, int64(i))
			_ = s.Requests()
			s.Reset()
		}
	}()
	for range 50 {
		if _, err := c.GetTrackURL(ctx, TrackSingle, 7, false); err != nil {
			t.Fatal(err)
		}
	}
	<-done
}

func TestFakeFailStatic(t *testing.T) {
	s := New(t)
	u := s.URL + staticPrefix + "images/covers/a/b/cover_org.jpg"
	if resp, _, _ := get(t, s, u, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("cover: %d", resp.StatusCode)
	}
	s.FailStatic()
	for _, p := range []string{u, s.URL + staticPrefix + "goodies/1/booklet.pdf"} {
		if resp, _, _ := get(t, s, p, ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	if resp, _, _ := get(t, s, s.cdnURL("7", 7), ""); resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		t.Errorf("the CDN: %d", resp.StatusCode)
	}
	s.Reset()
	if resp, _, _ := get(t, s, u, ""); resp.StatusCode != http.StatusOK {
		t.Errorf("after Reset: %d", resp.StatusCode)
	}
}

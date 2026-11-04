package qobuzdl_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
	"github.com/tikhonp/qobuz-dl-go/downloadsdb"
	"github.com/tikhonp/qobuz-dl-go/qobuzdltest"
)

// newDL is a DL on a fake Qobuz at quality 27, the bot's, with the app ID
// and secrets read from the fake's bundle. change runs before New.
func newDL(t *testing.T, change func(o *qobuzdl.Options)) (*qobuzdl.DL, *qobuzdltest.Server, qobuzdl.Options) {
	t.Helper()
	srv := qobuzdltest.New(t)
	o := qobuzdl.DefaultOptions()
	srv.Configure(&o)
	o.Directory = t.TempDir()
	o.Quality = 27
	if change != nil {
		change(&o)
	}
	d, err := qobuzdl.New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.GetTokens(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := d.InitializeClient(t.Context(), "", "", d.AppID(), d.Secrets()); err != nil {
		t.Fatal(err)
	}
	return d, srv, o
}

// openDB opens a downloads database in a temp folder.
func openDB(t *testing.T) (*downloadsdb.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qobuz_dl.db")
	db, err := downloadsdb.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

// dbRows reads the downloads table in id and quality order.
func dbRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT * FROM downloads ORDER BY id, quality")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, _ := rows.Columns()
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func collectionLayoutGolden(t *testing.T, golden, kind string) {
	for _, c := range qobuzdl.LayoutCases(t, golden) {
		t.Run(c.Name, func(t *testing.T) {
			var dbPath string
			d, _, o := newDL(t, func(o *qobuzdl.Options) {
				c.Configure(o)
				o.DownloadsDB, dbPath = openDB(t)
			})
			if _, err := d.HandleURL(t.Context(), "https://play.qobuz.com/"+kind+"/"+c.ID, nil); err != nil {
				t.Fatal(err)
			}
			c.Check(t, o.Directory)
			c.CheckDB(t, o.Directory, dbRows(t, dbPath))
			noTempFiles(t, o.Directory)
		})
	}
}

func TestPlaylistLayoutGolden(t *testing.T) {
	collectionLayoutGolden(t, "layout_playlist", "playlist")
}

func TestArtistLayoutGolden(t *testing.T) {
	collectionLayoutGolden(t, "layout_artist", "artist")
}

func TestGetTokensAndInitializeClient(t *testing.T) {
	var logs bytes.Buffer
	d, _, _ := newDL(t, func(o *qobuzdl.Options) { o.Logger = slog.New(slog.NewTextHandler(&logs, nil)) })
	if d.AppID() != qobuzdltest.AppID {
		t.Errorf("app ID %s", d.AppID())
	}
	if want := []string{qobuzdltest.BadSecret, qobuzdltest.Secret}; !slices.Equal(d.Secrets(), want) {
		t.Errorf("secrets %v, want %v", d.Secrets(), want)
	}
	if d.Client() == nil || d.Client().UserID() != qobuzdltest.UserID {
		t.Error("no client")
	}
	if !strings.Contains(logs.String(), "Set max quality: 27 - 24 bit, >96kHz") {
		t.Errorf("logs:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), qobuzdltest.Token) || strings.Contains(logs.String(), qobuzdltest.Secret) {
		t.Error("logs hold the token or the secret")
	}
}

func TestPlaylistM3U8(t *testing.T) {
	d, srv, o := newDL(t, nil)
	ev := &events{}
	res, err := d.HandleID(t.Context(), "playlist", qobuzdltest.Playlist, ev)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := d.Client().GetPlaylistMeta(t.Context(), qobuzdltest.Playlist)
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(o.Directory, pl.Name)
	if res.Kind != "playlist" || res.ID != qobuzdltest.Playlist || res.Folder != folder {
		t.Fatalf("result %+v", res)
	}
	if res.M3U != filepath.Join(folder, pl.Name+".m3u8") {
		t.Fatalf("m3u %s", res.M3U)
	}
	// Flat: the tracks and the playlist file, no folders, no cover.
	var names []string
	for _, p := range tree(t, folder) {
		if strings.HasSuffix(p, "/") || filepath.Ext(p) == ".jpg" || filepath.Ext(p) == ".txt" {
			t.Errorf("unexpected %s", p)
		}
		names = append(names, p)
	}
	if len(names) != 11 {
		t.Fatalf("files %q", names)
	}
	// The m3u8 lists the tracks in the playlist's order.
	data, err := os.ReadFile(res.M3U)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) != 21 || lines[0] != "#EXTM3U" {
		t.Fatalf("m3u8:\n%s", data)
	}
	if len(res.Releases) != len(pl.Tracks) {
		t.Fatalf("%d releases", len(res.Releases))
	}
	for i, tr := range pl.Tracks {
		rel := res.Releases[i]
		if rel.Kind != "track" || rel.ID != tr.ID || rel.Status != qobuzdl.StatusDone || len(rel.Tracks) != 1 {
			t.Fatalf("release %d: %+v", i, rel)
		}
		got := rel.Tracks[0]
		if got.Position != i+1 || got.TrackNumber != i+1 {
			t.Errorf("track %d: position %d, number %d", i, got.Position, got.TrackNumber)
		}
		if lines[2+2*i] != filepath.Base(got.Path) {
			t.Errorf("m3u8 line %d is %q, want %q", 2+2*i, lines[2+2*i], filepath.Base(got.Path))
		}
		if c := qobuzdl.FLACComments(t, got.Path); !slices.Contains(c, "TRACKNUMBER="+strconv.Itoa(i+1)) {
			t.Errorf("%s: %v", got.Path, c)
		}
	}
	// One ReleaseStart for the playlist with every track planned, and one
	// TrackEnd per track.
	if len(ev.releases) != 1 {
		t.Fatalf("%d ReleaseStart calls", len(ev.releases))
	}
	r := ev.releases[0]
	if r.Kind != "playlist" || r.ID != qobuzdltest.Playlist || r.Folder != folder || r.Playlist == nil || len(r.Tracks) != 10 {
		t.Fatalf("ReleaseStart %+v", r)
	}
	var ends []int
	for i, p := range r.Tracks {
		if p.Position != i+1 || p.ID != pl.Tracks[i].ID {
			t.Errorf("planned %d: %+v", i, p)
		}
	}
	for _, e := range ev.ends {
		ends = append(ends, e.Position)
	}
	slices.Sort(ends)
	if !slices.Equal(ends, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}) {
		t.Errorf("TrackEnd positions %v", ends)
	}
	// The playlist's pages come once: no second fetch for the m3u8.
	n := 0
	for _, req := range srv.Requests() {
		if strings.HasSuffix(req.Path, "playlist/get") {
			n++
		}
	}
	if n != 2 { // the download's and the test's GetPlaylistMeta
		t.Errorf("%d playlist/get calls", n)
	}
}

func TestPlaylistWithoutM3U(t *testing.T) {
	d, _, _ := newDL(t, func(o *qobuzdl.Options) { o.NoM3UForPlaylists = true })
	res, err := d.HandleID(t.Context(), "playlist", qobuzdltest.Playlist, nil)
	if err != nil || res.M3U != "" {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestPlaylistPaging(t *testing.T) {
	d, _, _ := newDL(t, func(o *qobuzdl.Options) { o.Quality = 5 })
	res, err := d.HandleID(t.Context(), "playlist", qobuzdltest.PlaylistLong, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Releases) != 120 {
		t.Fatalf("%d releases", len(res.Releases))
	}
	// The same ten tracks twelve times: the files exist after the first
	// round, and every m3u8 entry matches by track ID.
	data, _ := os.ReadFile(res.M3U)
	if n := strings.Count(string(data), "#EXTINF:"); n != 120 {
		t.Errorf("%d m3u8 entries", n)
	}
}

func TestLabelDownload(t *testing.T) {
	d, _, o := newDL(t, nil)
	res, err := d.HandleID(t.Context(), "label", qobuzdltest.Label, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != "label" || res.Folder != filepath.Join(o.Directory, "Naked Jain Records") {
		t.Fatalf("result %+v", res)
	}
	if len(res.Releases) != 15 {
		t.Fatalf("%d releases", len(res.Releases))
	}
	// Only the compilation is recorded; the fake answers 404 for the other
	// albums, which Python logs and skips as a request error.
	for _, rel := range res.Releases {
		if rel.ID == qobuzdltest.AlbumCompilation {
			if rel.Status != qobuzdl.StatusDone || !strings.HasPrefix(rel.Folder, res.Folder+"/") || len(rel.Tracks) != 20 {
				t.Errorf("compilation: %+v", rel)
			}
			continue
		}
		var ae *qobuzdl.APIError
		if rel.Status != qobuzdl.StatusFailed || !errors.As(rel.Err, &ae) || ae.Status != http.StatusNotFound {
			t.Errorf("%s: %v, %v", rel.ID, rel.Status, rel.Err)
		}
	}
}

func TestEmptyContent(t *testing.T) {
	d, _, o := newDL(t, nil)
	for _, c := range []struct{ kind, id string }{
		{"playlist", qobuzdltest.PlaylistEmpty},
		{"artist", qobuzdltest.ArtistEmpty},
		{"label", qobuzdltest.LabelEmpty},
	} {
		res, err := d.HandleID(t.Context(), c.kind, c.id, nil)
		if !errors.Is(err, qobuzdl.ErrEmptyContent) || res == nil || len(res.Releases) != 0 {
			t.Errorf("%s: %+v, %v", c.kind, res, err)
		}
	}
	if got := tree(t, o.Directory); len(got) != 0 {
		t.Errorf("files: %v", got)
	}
}

func TestHandleURLInvalid(t *testing.T) {
	ev := &events{}
	d, srv, o := newDL(t, nil)
	before := len(srv.Requests())
	for _, u := range []string{
		"", "https://example.com/", "https://www.qobuz.com/us-en/interpreter/miles-davis/6760",
		"https://play.qobuz.com/playlists/123", "not a url at all",
	} {
		res, err := d.HandleURL(t.Context(), u, ev)
		if !errors.Is(err, qobuzdl.ErrInvalidURL) || res == nil {
			t.Errorf("%q: %+v, %v", u, res, err)
		}
	}
	if _, err := d.HandleID(t.Context(), "interpreter", "6760", nil); !errors.Is(err, qobuzdl.ErrInvalidURL) {
		t.Errorf("HandleID: %v", err)
	}
	if n := len(srv.Requests()) - before; n != 0 {
		t.Errorf("%d requests", n)
	}
	if got := tree(t, o.Directory); len(got) != 0 {
		t.Errorf("files: %v", got)
	}
	if !ev.logged(`Invalid url: "https://example.com/". Use urls from https://play.qobuz.com!`) {
		t.Errorf("logs: %q", ev.logs)
	}
}

func TestHandleURLAlbumAndTrack(t *testing.T) {
	d, _, _ := newDL(t, nil)
	res, err := d.HandleURL(t.Context(), "https://www.qobuz.com/us-en/album/begin-again/"+qobuzdltest.AlbumHiRes96, nil)
	if err != nil || res.Kind != "album" || len(res.Releases) != 1 || res.Releases[0].Status != qobuzdl.StatusDone {
		t.Fatalf("album: %+v, %v", res, err)
	}
	if res.Folder != res.Releases[0].Folder {
		t.Errorf("folder %s, release %s", res.Folder, res.Releases[0].Folder)
	}
	res, err = d.HandleURL(t.Context(), "https://open.qobuz.com/track/"+qobuzdltest.TrackSingle, nil)
	if err != nil || res.Kind != "track" || res.Releases[0].Status != qobuzdl.StatusDone {
		t.Fatalf("track: %+v, %v", res, err)
	}
}

// memDB is a DownloadsDB in memory that notes its calls.
type memDB struct {
	mu     sync.Mutex
	has    map[string]bool // "<id> <quality>"
	asked  []string
	added  []qobuzdl.DownloadRecord
	hasErr error
}

func (m *memDB) Has(_ context.Context, mediaType, id string, quality int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, mediaType+" "+id+" "+strconv.Itoa(quality))
	return m.has[id+" "+strconv.Itoa(quality)], m.hasErr
}

func (m *memDB) Add(_ context.Context, rec qobuzdl.DownloadRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.added = append(m.added, rec)
	return nil
}

func TestDownloadsDBSkips(t *testing.T) {
	db := &memDB{has: map[string]bool{qobuzdltest.AlbumHiRes96 + " 27": true, "611905 27": true}}
	ev := &events{}
	d, srv, o := newDL(t, func(o *qobuzdl.Options) { o.DownloadsDB = db })
	res, err := d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: qobuzdltest.AlbumHiRes96, Album: true, Progress: ev})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Releases) != 1 || res.Releases[0].Status != qobuzdl.StatusSkipped || res.Releases[0].Skip != qobuzdl.SkipInDatabase {
		t.Fatalf("%+v", res)
	}
	for _, r := range srv.Requests() {
		if strings.HasSuffix(r.Path, "album/get") {
			t.Error("album/get for an album in the database")
		}
	}
	if got := tree(t, o.Directory); len(got) != 0 {
		t.Errorf("files: %v", got)
	}
	if !slices.Equal(db.asked, []string{"album " + qobuzdltest.AlbumHiRes96 + " 27"}) {
		t.Errorf("asked %v", db.asked)
	}
	if !ev.logged("This release ID (" + qobuzdltest.AlbumHiRes96 + ") was already downloaded according to the local database.") {
		t.Errorf("logs %q", ev.logs)
	}

	// In a playlist the track's end is still reported, as skipped.
	ev = &events{}
	res, err = d.HandleID(t.Context(), "playlist", qobuzdltest.Playlist, ev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Releases[1].ID != "611905" || res.Releases[1].Skip != qobuzdl.SkipInDatabase {
		t.Fatalf("release 2: %+v", res.Releases[1])
	}
	var skipped []qobuzdl.TrackResult
	for _, e := range ev.ends {
		if e.Outcome == qobuzdl.OutcomeSkipped {
			skipped = append(skipped, e)
		}
	}
	if len(ev.ends) != 10 || len(skipped) != 1 || skipped[0].Position != 2 || skipped[0].ID != "611905" {
		t.Errorf("TrackEnd: %d calls, skipped %+v", len(ev.ends), skipped)
	}
	if !slices.Contains(db.asked, "track 611905 27") {
		t.Errorf("asked %v", db.asked)
	}
}

func TestIgnoreDB(t *testing.T) {
	db := &memDB{has: map[string]bool{qobuzdltest.AlbumHiRes96 + " 27": true}}
	d, _, _ := newDL(t, func(o *qobuzdl.Options) { o.DownloadsDB = db })
	res, err := d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: qobuzdltest.AlbumHiRes96, Album: true, IgnoreDB: true})
	if err != nil || res.Releases[0].Status != qobuzdl.StatusDone {
		t.Fatalf("%+v, %v", res, err)
	}
	if len(db.asked) != 0 {
		t.Errorf("asked %v", db.asked)
	}
	if len(db.added) != 1 {
		t.Errorf("added %v", db.added)
	}
}

func TestDownloadsDBRecords(t *testing.T) {
	db, path := openDB(t)
	d, srv, o := newDL(t, func(o *qobuzdl.Options) { o.DownloadsDB = db })
	ids := (&env{c: d.Client()}).trackIDs(t, qobuzdltest.AlbumHiRes96)
	srv.Fail404(ids[2])
	res, err := d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: qobuzdltest.AlbumHiRes96, Album: true})
	if err != nil || res.Releases[0].Status != qobuzdl.StatusPartial {
		t.Fatalf("%+v, %v", res, err)
	}
	if rows := dbRows(t, path); len(rows) != 0 {
		t.Fatalf("a partial album was recorded: %v", rows)
	}

	srv.Reset()
	res, err = d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: qobuzdltest.AlbumHiRes96, Album: true})
	if err != nil || res.Releases[0].Status != qobuzdl.StatusDone {
		t.Fatalf("%+v, %v", res, err)
	}
	rows := dbRows(t, path)
	if len(rows) != 1 {
		t.Fatalf("rows %v", rows)
	}
	// What the artist golden's Python run wrote for the same album.
	want := map[string]any{
		"id": qobuzdltest.AlbumHiRes96, "media_type": "album", "quality": int64(27), "file_format": "FLAC",
		"quality_met": int64(0), "bit_depth": "24", "sampling_rate": "96",
		"saved_path": filepath.Join(o.Directory, "Norah Jones - Begin Again (2019) [24B-96kHz]"), "status": "downloaded",
		"url": "https://www.qobuz.com/fr-fr/album/begin-again-norah-jones/k43q1q3kx88fa", "release_date": "2019-04-12",
		"artist": "Norah Jones", "album": "Begin Again",
	}
	for k, v := range want {
		if rows[0][k] != v {
			t.Errorf("%s = %#v, want %#v", k, rows[0][k], v)
		}
	}
	// Now the database has it.
	res, err = d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: qobuzdltest.AlbumHiRes96, Album: true})
	if err != nil || res.Releases[0].Skip != qobuzdl.SkipInDatabase {
		t.Fatalf("%+v, %v", res, err)
	}
	// A track is recorded as a track.
	if _, err := d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: qobuzdltest.TrackSingle}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.Has(t.Context(), "track", qobuzdltest.TrackSingle, 27); err != nil || !ok {
		t.Errorf("track: %v, %v", ok, err)
	}
}

func TestDownloadsDBFailingHasStops(t *testing.T) {
	boom := errors.New("disk on fire")
	d, srv, _ := newDL(t, func(o *qobuzdl.Options) { o.DownloadsDB = &memDB{hasErr: boom} })
	before := len(srv.Requests())
	_, err := d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: qobuzdltest.AlbumHiRes96, Album: true})
	if !errors.Is(err, boom) || errors.Is(err, qobuzdl.ErrUnhandled) {
		t.Fatalf("%v", err)
	}
	if len(srv.Requests()) != before {
		t.Error("requests after a failing Has")
	}
}

// rewriteTransport changes the bodies of the API answers rewrite picks.
type rewriteTransport struct {
	base    http.RoundTripper
	rewrite func(r *http.Request, body []byte) []byte
}

func (rt *rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := rt.base.RoundTrip(r)
	if err != nil || !strings.Contains(r.URL.Path, "/api.json/") {
		return resp, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	body = rt.rewrite(r, body)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Del("Content-Length")
	return resp, nil
}

func rewriting(o *qobuzdl.Options, rewrite func(r *http.Request, body []byte) []byte) {
	base := o.HTTPClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	o.HTTPClient = &http.Client{Transport: &rewriteTransport{base: base, rewrite: rewrite}}
}

// albumAnswer changes album/get's answer for id.
func albumAnswer(id, old, now string) func(r *http.Request, body []byte) []byte {
	return func(r *http.Request, body []byte) []byte {
		if strings.HasSuffix(r.URL.Path, "album/get") && r.URL.Query().Get("album_id") == id {
			return bytes.Replace(body, []byte(old), []byte(now), 1)
		}
		return body
	}
}

func TestDownloadFromIDErrors(t *testing.T) {
	t.Run("not streamable is reported", func(t *testing.T) {
		d, _, o := newDL(t, func(o *qobuzdl.Options) {
			rewriting(o, albumAnswer(qobuzdltest.AlbumCD, `"streamable":true`, `"streamable":false`))
		})
		ev := &events{}
		res, err := d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: qobuzdltest.AlbumCD, Album: true, Progress: ev})
		if err != nil {
			t.Fatal(err)
		}
		rel := res.Releases[0]
		if rel.Status != qobuzdl.StatusFailed || !errors.Is(rel.Err, qobuzdl.ErrNonStreamable) {
			t.Fatalf("%+v", rel)
		}
		if !ev.logged("Error getting release: ") || len(tree(t, o.Directory)) != 0 {
			t.Errorf("logs %q", ev.logs)
		}
	})
	t.Run("a request error is reported", func(t *testing.T) {
		d, _, _ := newDL(t, nil)
		res, err := d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: "nosuchalbum", Album: true})
		var ae *qobuzdl.APIError
		if err != nil || !errors.As(res.Releases[0].Err, &ae) || ae.Status != http.StatusNotFound {
			t.Fatalf("%+v, %v", res, err)
		}
		res, err = d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: "999999999"})
		if err != nil || res.Releases[0].Status != qobuzdl.StatusFailed {
			t.Fatalf("track: %+v, %v", res, err)
		}
	})
	t.Run("another error stops", func(t *testing.T) {
		d, _, _ := newDL(t, func(o *qobuzdl.Options) {
			rewriting(o, albumAnswer(qobuzdltest.AlbumCD, `"media_count":`, `"media_count":"x","was":`))
		})
		res, err := d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: qobuzdltest.AlbumCD, Album: true})
		if !errors.Is(err, qobuzdl.ErrUnhandled) || !strings.Contains(err.Error(), "TypeError") {
			t.Fatalf("%v", err)
		}
		if res == nil || len(res.Releases) != 1 || res.Releases[0].Status != qobuzdl.StatusFailed {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("another error stops an artist", func(t *testing.T) {
		d, srv, _ := newDL(t, func(o *qobuzdl.Options) {
			rewriting(o, albumAnswer(qobuzdltest.AlbumWithVersion, `"media_count":`, `"media_count":"x","was":`))
		})
		res, err := d.HandleID(t.Context(), "artist", qobuzdltest.ArtistAlbums, nil)
		if !errors.Is(err, qobuzdl.ErrUnhandled) {
			t.Fatalf("%v", err)
		}
		if len(res.Releases) != 2 || res.Releases[0].Status != qobuzdl.StatusDone {
			t.Fatalf("%+v", res.Releases)
		}
		for _, r := range srv.Requests() {
			if r.Query.Get("album_id") == qobuzdltest.AlbumCD {
				t.Error("the album after the failure was asked for")
			}
		}
	})
	t.Run("the context's end", func(t *testing.T) {
		d, _, _ := newDL(t, nil)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := d.DownloadFromID(ctx, qobuzdl.DownloadRequest{ID: qobuzdltest.AlbumCD, Album: true})
		if !errors.Is(err, context.Canceled) || errors.Is(err, qobuzdl.ErrUnhandled) {
			t.Fatalf("%v", err)
		}
		_, err = d.HandleID(ctx, "playlist", qobuzdltest.Playlist, nil)
		if !errors.Is(err, context.Canceled) || errors.Is(err, qobuzdl.ErrUnhandled) {
			t.Fatalf("playlist: %v", err)
		}
	})
	t.Run("a collection's failed page is unhandled", func(t *testing.T) {
		d, srv, _ := newDL(t, nil)
		srv.ExpireToken()
		_, err := d.HandleID(t.Context(), "playlist", qobuzdltest.Playlist, nil)
		var ae *qobuzdl.APIError
		if !errors.Is(err, qobuzdl.ErrUnhandled) || !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
			t.Fatalf("%v", err)
		}
	})
	t.Run("before InitializeClient", func(t *testing.T) {
		o := qobuzdl.DefaultOptions()
		o.HTTPClient = http.DefaultClient
		o.Directory = t.TempDir()
		d, err := qobuzdl.New(o)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: "1"}); err == nil {
			t.Error("no error")
		}
	})
}

func TestCancelMidPlaylist(t *testing.T) {
	d, _, _ := newDL(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	ev := &events{onEnd: func(tr qobuzdl.TrackResult) {
		if tr.Position == 3 {
			cancel()
		}
	}}
	res, err := d.HandleID(ctx, "playlist", qobuzdltest.Playlist, ev)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if len(res.Releases) != 3 || res.M3U != "" {
		t.Fatalf("%d releases, m3u %q", len(res.Releases), res.M3U)
	}
}

func TestConcurrentDownloads(t *testing.T) {
	d, _, o := newDL(t, nil)
	var wg sync.WaitGroup
	results := make([]*qobuzdl.Result, 3)
	errs := make([]error, 3)
	wg.Go(func() {
		results[0], errs[0] = d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: qobuzdltest.AlbumHiRes96, Album: true})
	})
	wg.Go(func() {
		results[1], errs[1] = d.HandleID(t.Context(), "playlist", qobuzdltest.Playlist, &events{})
	})
	wg.Go(func() {
		results[2], errs[2] = d.DownloadFromID(t.Context(), qobuzdl.DownloadRequest{ID: qobuzdltest.AlbumMultiDisc, Album: true})
	})
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	// The playlist's flat folder template didn't reach the albums.
	for _, i := range []int{0, 2} {
		rel := results[i].Releases[0]
		if rel.Status != qobuzdl.StatusDone || filepath.Dir(rel.Folder) != o.Directory || strings.Contains(rel.Folder, "Top 10") {
			t.Errorf("album %d: %s, %v", i, rel.Folder, rel.Status)
		}
	}
	multi := results[2].Releases[0]
	if !slices.ContainsFunc(multi.Tracks, func(tr qobuzdl.TrackResult) bool { return strings.Contains(tr.Path, "/CD 02/") }) {
		t.Error("the multi-disc album lost its disc folders")
	}
	if len(results[1].Releases) != 10 || results[1].M3U == "" {
		t.Errorf("playlist: %d releases", len(results[1].Releases))
	}
}

func TestSearchByType(t *testing.T) {
	d, srv, _ := newDL(t, nil)
	golden := qobuzdl.SearchGolden(t)
	for _, typ := range []string{"album", "track", "artist", "playlist"} {
		want := golden["recorded "+typ+"s"]
		got, err := d.SearchByType(t.Context(), "miles davis", typ, 10)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want.Lines) {
			t.Errorf("%s:\n got %q\nwant %q", typ, got, want.Lines)
		}
	}
	reqs := srv.Requests()
	last := reqs[len(reqs)-1]
	if last.Query.Get("query") != "miles davis" || last.Query.Get("type") != "playlists" || last.Query.Get("limit") != "10" {
		t.Errorf("request %v", last.Query)
	}
	srv.ExpireToken()
	if _, err := d.SearchByType(t.Context(), "miles davis", "album", 10); err == nil {
		t.Error("a failed search returned no error")
	}
}

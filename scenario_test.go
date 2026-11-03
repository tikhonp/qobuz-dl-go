package qobuzdl_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
	"github.com/tikhonp/qobuz-dl-go/qobuzdltest"
)

// env is a fake Qobuz, a client on it and options pointing at it, with the
// download folder a fresh temp dir and the bot's quality, 27.
type env struct {
	srv  *qobuzdltest.Server
	c    *qobuzdl.Client
	opts qobuzdl.Options
}

func newEnv(t *testing.T) *env {
	t.Helper()
	srv := qobuzdltest.New(t)
	o := qobuzdl.DefaultOptions()
	srv.Configure(&o)
	o.Directory = t.TempDir()
	o.Quality = 27
	c, err := qobuzdl.NewClient(t.Context(), srv.ClientOptions())
	if err != nil {
		t.Fatal(err)
	}
	return &env{srv: srv, c: c, opts: o}
}

func (e *env) download(t *testing.T, id string, track bool, p qobuzdl.Progress) (*qobuzdl.Release, error) {
	t.Helper()
	return qobuzdl.DownloadIDByType(t.Context(), e.c, e.opts, id, track, p)
}

// mustDownload downloads and fails the test on an error.
func (e *env) mustDownload(t *testing.T, id string, track bool, p qobuzdl.Progress) *qobuzdl.Release {
	t.Helper()
	rel, err := e.download(t, id, track, p)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func (e *env) trackIDs(t *testing.T, album string) []string {
	t.Helper()
	a, err := e.c.GetAlbumMeta(t.Context(), album)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tr := range a.Tracks {
		ids = append(ids, tr.ID)
	}
	return ids
}

// events records every Progress call.
type events struct {
	mu       sync.Mutex
	releases []qobuzdl.ReleaseInfo
	starts   []qobuzdl.TrackInfo
	bytes    map[int][][2]int64
	ends     []qobuzdl.TrackResult
	logs     []string
	onEnd    func(qobuzdl.TrackResult)
	onBytes  func(pos int)
}

func (e *events) ReleaseStart(r qobuzdl.ReleaseInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.releases = append(e.releases, r)
}

func (e *events) TrackStart(t qobuzdl.TrackInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.starts = append(e.starts, t)
}

func (e *events) TrackBytes(pos int, done, total int64) {
	e.mu.Lock()
	if e.bytes == nil {
		e.bytes = map[int][][2]int64{}
	}
	e.bytes[pos] = append(e.bytes[pos], [2]int64{done, total})
	f := e.onBytes
	e.mu.Unlock()
	if f != nil {
		f(pos)
	}
}

func (e *events) TrackEnd(t qobuzdl.TrackResult) {
	e.mu.Lock()
	e.ends = append(e.ends, t)
	f := e.onEnd
	e.mu.Unlock()
	if f != nil {
		f(t)
	}
}

func (e *events) Log(_ slog.Level, msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.logs = append(e.logs, msg)
}

func (e *events) logged(s string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.ContainsFunc(e.logs, func(l string) bool { return strings.Contains(l, s) })
}

// tree lists every path under root, relative, with "/" after folders.
func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			rel += "/"
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func noTempFiles(t *testing.T, root string) {
	t.Helper()
	for _, p := range tree(t, root) {
		if base := filepath.Base(p); strings.HasPrefix(base, "~tmp_") || strings.HasPrefix(base, ".tagging-") {
			t.Errorf("temp file left: %s", p)
		}
	}
}

func TestAlbumLayoutGolden(t *testing.T) {
	for _, c := range qobuzdl.LayoutCases(t, "layout_album") {
		t.Run(c.Name, func(t *testing.T) {
			e := newEnv(t)
			c.Configure(&e.opts)
			e.mustDownload(t, c.ID, false, nil)
			c.Check(t, e.opts.Directory)
		})
	}
}

func TestTrackLayoutGolden(t *testing.T) {
	for _, c := range qobuzdl.LayoutCases(t, "layout_track") {
		t.Run(c.Name, func(t *testing.T) {
			e := newEnv(t)
			c.Configure(&e.opts)
			ev := &events{}
			var sawTemp bool
			ev.onBytes = func(int) {
				ev.mu.Lock()
				folder := ev.releases[0].Folder
				ev.mu.Unlock()
				if _, err := os.Stat(filepath.Join(folder, "~tmp_01.tmp")); err == nil {
					sawTemp = true
				}
			}
			rel := e.mustDownload(t, c.ID, true, ev)
			c.Check(t, e.opts.Directory)
			if rel.Status == qobuzdl.StatusDone && !sawTemp {
				t.Error("~tmp_01.tmp was never there")
			}
			noTempFiles(t, e.opts.Directory)
		})
	}
}

func TestMultiDiscAlbum(t *testing.T) {
	e := newEnv(t)
	rel := e.mustDownload(t, qobuzdltest.AlbumMultiDisc, false, nil)
	if rel.Status != qobuzdl.StatusDone || len(rel.Tracks) != 16 {
		t.Fatalf("status %v, %d tracks", rel.Status, len(rel.Tracks))
	}
	for _, tr := range rel.Tracks {
		disc := filepath.Base(filepath.Dir(tr.Path))
		want := "CD 0" + string(rune('0'+tr.DiscNumber))
		if disc != want {
			t.Errorf("track %d in %q, want %q", tr.Position, disc, want)
		}
		comments := qobuzdl.FLACComments(t, tr.Path)
		for _, c := range []string{"DISCNUMBER=" + string(rune('0'+tr.DiscNumber)), "DISCTOTAL=2"} {
			if !slices.Contains(comments, c) {
				t.Errorf("track %d lacks %s: %q", tr.Position, c, comments)
			}
		}
	}
	// The cover and the tracklist stay in the album folder.
	for _, f := range []string{"cover.jpg", "Ummagumma - Tracklist.txt"} {
		if _, err := os.Stat(filepath.Join(rel.Folder, f)); err != nil {
			t.Error(err)
		}
	}
}

func TestFolderStates(t *testing.T) {
	e := newEnv(t)
	ids := e.trackIDs(t, qobuzdltest.AlbumHiRes96)
	const name = "Norah Jones - Begin Again (2019) [24B-96kHz]"
	dir := e.opts.Directory

	e.srv.Fail404(ids[2])
	ev := &events{}
	rel := e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, ev)
	if got := filepath.Base(ev.releases[0].Folder); got != "[IN PROGRESS] "+name {
		t.Errorf("work folder %q", got)
	}
	if rel.Folder != filepath.Join(dir, "[INCOMPLETE] "+name) || rel.Status != qobuzdl.StatusPartial {
		t.Errorf("after a failed track: %q, status %v", rel.Folder, rel.Status)
	}
	if fails := rel.Tracks[2]; fails.Outcome != qobuzdl.OutcomeFailed || !errors.Is(fails.Err, qobuzdl.ErrAllTiers) {
		t.Errorf("track 3: %+v", fails)
	}

	// An [INCOMPLETE] folder is resumed: done tracks are kept, the failed one
	// downloads.
	e.srv.Reset()
	rel = e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, nil)
	if rel.Folder != filepath.Join(dir, name) || rel.Status != qobuzdl.StatusDone {
		t.Errorf("resumed: %q, status %v", rel.Folder, rel.Status)
	}
	for i, tr := range rel.Tracks {
		want := qobuzdl.OutcomeSkipped
		if i == 2 {
			want = qobuzdl.OutcomeDone
		}
		if tr.Outcome != want {
			t.Errorf("track %d: outcome %v, want %v", i+1, tr.Outcome, want)
		}
	}

	// The final folder is resumed too, through [IN PROGRESS].
	ev = &events{}
	rel = e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, ev)
	if filepath.Base(ev.releases[0].Folder) != "[IN PROGRESS] "+name || rel.Folder != filepath.Join(dir, name) {
		t.Errorf("final folder again: work %q, final %q", ev.releases[0].Folder, rel.Folder)
	}

	// A leftover [IN PROGRESS] is used as it is.
	if err := os.Rename(rel.Folder, filepath.Join(dir, "[IN PROGRESS] "+name)); err != nil {
		t.Fatal(err)
	}
	rel = e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, nil)
	if got := tree(t, dir); len(got) != 10 || got[0] != name+"/" || rel.Status != qobuzdl.StatusDone {
		t.Errorf("tree after a leftover [IN PROGRESS]: %q", got)
	}
}

func TestFolderStatesNameTooLong(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("日", 82) // 246 bytes; with "2019 " the folder takes 251
	if _, err := os.Stat(filepath.Join(e.opts.Directory, "[IN PROGRESS] 2019 "+long)); !errors.Is(err, syscall.ENAMETOOLONG) {
		t.Skip("the file system takes names over 255 bytes")
	}
	e.opts.FolderFormat = "{year} " + long
	ev := &events{}
	rel := e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, ev)
	want := filepath.Join(e.opts.Directory, "2019 "+long)
	if ev.releases[0].Folder != want || rel.Folder != want || rel.Status != qobuzdl.StatusDone {
		t.Errorf("work %q, final %q, status %v", ev.releases[0].Folder, rel.Folder, rel.Status)
	}
}

func TestNoFallbackSkips(t *testing.T) {
	e := newEnv(t)
	e.opts.QualityFallback = false
	rel, err := e.download(t, qobuzdltest.AlbumHiRes96, false, nil)
	if err != nil || rel.Status != qobuzdl.StatusSkipped || rel.Skip != qobuzdl.SkipQuality || rel.QualityMet {
		t.Fatalf("got %+v, %v", rel, err)
	}
	if got := tree(t, e.opts.Directory); len(got) != 0 {
		t.Errorf("wrote %q", got)
	}
	// Without fallback a track takes the requested format or nothing.
	e.opts.Quality = 7
	e.srv.Restrict(e.trackIDs(t, qobuzdltest.AlbumHiRes96)[1], 7)
	rel = e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, nil)
	if rel.Tracks[1].Outcome != qobuzdl.OutcomeDone || rel.Tracks[1].FormatID != 6 {
		t.Errorf("restricted track: %+v", rel.Tracks[1])
	}
}

func TestGateFailureIsUnknown(t *testing.T) {
	e := newEnv(t)
	e.opts.Quality = 5
	e.srv.EmptyAnswer(e.trackIDs(t, qobuzdltest.AlbumHiRes96)[0])
	rel := e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, nil)
	if rel.Format != "Unknown" || !rel.QualityMet || rel.BitDepth != 0 {
		t.Errorf("gate %q %v %d", rel.Format, rel.QualityMet, rel.BitDepth)
	}
	if want := "[INCOMPLETE] Norah Jones - Begin Again (2019) [NoneB-NonekHz]"; filepath.Base(rel.Folder) != want {
		t.Errorf("folder %q", filepath.Base(rel.Folder))
	}
}

func TestFallbackDeliveredFormat(t *testing.T) {
	e := newEnv(t)
	ids := e.trackIDs(t, qobuzdltest.AlbumHiRes96)
	e.srv.Deliver(ids[1], 5)
	rel := e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, nil)
	for i, tr := range rel.Tracks {
		ext, codec, format := ".flac", "FLAC", 7
		if i == 1 {
			ext, codec, format = ".mp3", "MP3", 5
		}
		if filepath.Ext(tr.Path) != ext || tr.Codec != codec || tr.FormatID != format {
			t.Errorf("track %d: %s %s %d", i+1, tr.Path, tr.Codec, tr.FormatID)
		}
	}
	data, err := os.ReadFile(rel.Tracks[1].Path)
	if err != nil || string(data[:3]) != "ID3" {
		t.Errorf("not tagged as MP3: %v", err)
	}
}

func TestSampleMovesToNextTier(t *testing.T) {
	e := newEnv(t)
	id := e.trackIDs(t, qobuzdltest.AlbumMultiDisc)[1]
	e.srv.Sample(id, 27)
	ev := &events{}
	rel := e.mustDownload(t, qobuzdltest.AlbumMultiDisc, false, ev)
	if tr := rel.Tracks[1]; tr.Outcome != qobuzdl.OutcomeDone || tr.FormatID != 7 || tr.SamplingRate != 96 {
		t.Errorf("track 2: %+v", tr)
	}
	var formats []string
	for _, r := range e.srv.Requests() {
		if strings.HasSuffix(r.Path, "track/getFileUrl") && r.Query.Get("track_id") == id {
			formats = append(formats, r.Query.Get("format_id"))
		}
	}
	// The main loop's call, then the tiers: 27 is a sample, 7 delivers.
	if want := []string{"27", "27", "7"}; !slices.Equal(formats, want) {
		t.Errorf("getFileUrl formats %v, want %v", formats, want)
	}
	for _, s := range ev.starts {
		if s.Position == 2 && s.FormatID != 7 {
			t.Errorf("a transfer at %d", s.FormatID)
		}
	}
}

func TestDemoTracksSkipped(t *testing.T) {
	e := newEnv(t)
	e.opts.Quality = 5
	ids := e.trackIDs(t, qobuzdltest.AlbumHiRes96)
	e.srv.EmptyAnswer(ids[3])
	ev := &events{}
	rel := e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, ev)
	if tr := rel.Tracks[3]; tr.Outcome != qobuzdl.OutcomeFailed || !errors.Is(tr.Err, qobuzdl.ErrDemo) {
		t.Errorf("demo track: %+v", tr)
	}
	if rel.Status != qobuzdl.StatusPartial || !strings.HasPrefix(filepath.Base(rel.Folder), "[INCOMPLETE] ") || !ev.logged("Demo. Skipping") {
		t.Errorf("status %v, folder %q", rel.Status, rel.Folder)
	}
	if len(ev.ends) != 7 {
		t.Errorf("%d TrackEnd calls", len(ev.ends))
	}

	e = newEnv(t)
	e.opts.Quality = 5
	e.srv.EmptyAnswer(qobuzdltest.TrackSingle)
	rel, err := e.download(t, qobuzdltest.TrackSingle, true, nil)
	if err != nil || rel.Status != qobuzdl.StatusSkipped || rel.Skip != qobuzdl.SkipDemo {
		t.Errorf("single demo: %+v, %v", rel, err)
	}
	if got := tree(t, e.opts.Directory); len(got) != 0 {
		t.Errorf("wrote %q", got)
	}
}

func TestExistingFileSkipped(t *testing.T) {
	for _, q := range []int{27, 5} {
		e := newEnv(t)
		e.opts.Quality = q
		first := e.mustDownload(t, qobuzdltest.AlbumMultiDisc, false, nil)
		e.srv.Reset()
		ev := &events{}
		rel := e.mustDownload(t, qobuzdltest.AlbumMultiDisc, false, ev)
		for i, tr := range rel.Tracks {
			want := first.Tracks[i]
			if tr.Outcome != qobuzdl.OutcomeSkipped || tr.Path != want.Path || tr.FormatID != want.FormatID || tr.Codec != want.Codec {
				t.Errorf("q%d track %d: %+v, first %+v", q, i+1, tr, want)
			}
			if q == 27 && (tr.BitDepth != 24 || tr.SamplingRate != 192) {
				t.Errorf("track %d: %d/%v", i+1, tr.BitDepth, tr.SamplingRate)
			}
		}
		for _, r := range e.srv.Requests() {
			if strings.HasPrefix(r.Path, "/cdn/") {
				t.Errorf("q%d: fetched %s", q, r.Path)
			}
		}
		if !ev.logged("(Already exists)") {
			t.Error("no skip line")
		}
	}
}

func TestTempFileRemoved(t *testing.T) {
	e := newEnv(t)
	ids := e.trackIDs(t, qobuzdltest.AlbumHiRes96)
	e.srv.Fail404(ids[1])
	e.srv.FailTimes(ids[2], 100, http.StatusServiceUnavailable)
	rel := e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, nil)
	if rel.Tracks[1].Outcome != qobuzdl.OutcomeFailed || rel.Tracks[2].Outcome != qobuzdl.OutcomeFailed {
		t.Errorf("tracks: %+v", rel.Tracks[1:3])
	}
	noTempFiles(t, e.opts.Directory)

	// A tagging failure: the MP3 the server delivers can't take the name of
	// the folder in its way.
	e.srv.Reset()
	e.srv.Deliver(ids[1], 5)
	inProgress := filepath.Join(filepath.Dir(rel.Folder), strings.Replace(filepath.Base(rel.Folder), "[INCOMPLETE] ", "", 1))
	blocker := filepath.Join(rel.Folder, strings.TrimSuffix(filepath.Base(rel.Tracks[0].Path), ".flac"))
	blocker = strings.Replace(blocker, "01 - My Heart Is Full", "02 - Begin Again", 1) + ".mp3"
	if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel = e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, nil)
	if tr := rel.Tracks[1]; tr.Outcome != qobuzdl.OutcomeFailed || tr.Err == nil || !strings.Contains(tr.Err.Error(), "tagging") {
		t.Errorf("track 2: %+v", tr)
	}
	if rel.Tracks[2].Outcome != qobuzdl.OutcomeDone || filepath.Dir(rel.Folder) != filepath.Dir(inProgress) {
		t.Errorf("track 3: %+v", rel.Tracks[2])
	}
	noTempFiles(t, e.opts.Directory)
}

func TestCancelMidAlbum(t *testing.T) {
	e := newEnv(t)
	e.opts.Settings.MaxWorkers = 1
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ev := &events{}
	ev.onEnd = func(qobuzdl.TrackResult) { cancel() }
	rel, err := qobuzdl.DownloadIDByType(ctx, e.c, e.opts, qobuzdltest.AlbumHiRes96, false, ev)
	if !errors.Is(err, context.Canceled) || rel == nil {
		t.Fatalf("got %v", err)
	}
	if !strings.HasPrefix(filepath.Base(rel.Folder), "[INCOMPLETE] ") || !errors.Is(rel.Err, context.Canceled) {
		t.Errorf("folder %q, err %v", rel.Folder, rel.Err)
	}
	if len(ev.starts) != 1 || len(ev.ends) != 7 {
		t.Errorf("%d transfers started, %d tracks ended", len(ev.starts), len(ev.ends))
	}
	first := rel.Tracks[0]
	if first.Outcome != qobuzdl.OutcomeDone || filepath.Dir(first.Path) != rel.Folder {
		t.Errorf("track 1: %+v", first)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Error(err)
	}
	for _, tr := range rel.Tracks[1:] {
		if tr.Outcome != qobuzdl.OutcomeFailed || !errors.Is(tr.Err, context.Canceled) {
			t.Errorf("track %d: %+v", tr.Position, tr)
		}
	}
	noTempFiles(t, e.opts.Directory)
}

// gatedTransport holds each CDN request until three are in flight or
// 30 ms pass, and notes the most in flight at once.
type gatedTransport struct {
	base http.RoundTripper

	mu       sync.Mutex
	inFlight int
	most     int
	arrived  chan struct{}
	once     sync.Once
}

type countedBody struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (b *countedBody) Close() error {
	b.once.Do(b.done)
	return b.ReadCloser.Close()
}

func (g *gatedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(r.URL.Path, "/cdn/") {
		return g.base.RoundTrip(r)
	}
	g.mu.Lock()
	g.inFlight++
	g.most = max(g.most, g.inFlight)
	n := g.inFlight
	g.mu.Unlock()
	done := func() {
		g.mu.Lock()
		g.inFlight--
		g.mu.Unlock()
	}
	if n >= 3 {
		g.once.Do(func() { close(g.arrived) })
	} else {
		select {
		case <-g.arrived:
		case <-time.After(30 * time.Millisecond):
		}
	}
	resp, err := g.base.RoundTrip(r)
	if err != nil {
		done()
		return nil, err
	}
	resp.Body = &countedBody{ReadCloser: resp.Body, done: done}
	return resp, nil
}

func TestWorkerPoolOrder(t *testing.T) {
	for _, delay := range []time.Duration{0, time.Millisecond} {
		e := newEnv(t)
		e.opts.Settings.Delay = delay
		g := &gatedTransport{base: e.opts.HTTPClient.Transport, arrived: make(chan struct{})}
		e.opts.HTTPClient = &http.Client{Transport: g}
		ev := &events{}
		rel := e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, ev)
		if rel.Status != qobuzdl.StatusDone {
			t.Fatalf("status %v", rel.Status)
		}
		want := 3
		if delay > 0 {
			want = 1
			var order []int
			for _, s := range ev.starts {
				order = append(order, s.Position)
			}
			if !slices.Equal(order, []int{1, 2, 3, 4, 5, 6, 7}) {
				t.Errorf("one worker started %v", order)
			}
			if !ev.logged("Safety Delay active") || !ev.logged("Sleeping for 0.001 seconds") {
				t.Error("no delay lines")
			}
		}
		if g.most != want {
			t.Errorf("delay %v: %d transfers at once, want %d", delay, g.most, want)
		}
	}
}

func TestExtras(t *testing.T) {
	e := newEnv(t)
	e.opts.EmbedArt = true
	e.opts.Settings.EmbedArt = true
	e.opts.Settings.EmbeddedArtSize = "300"
	rel := e.mustDownload(t, qobuzdltest.AlbumGoodies, false, nil)
	var static []string
	for _, r := range e.srv.Requests() {
		if strings.HasPrefix(r.Path, "/static/") {
			static = append(static, r.Path)
		}
	}
	if len(static) != 3 || !strings.HasSuffix(static[0], "_org.jpg") || !strings.HasSuffix(static[1], "_300.jpg") || !strings.HasSuffix(static[2], ".pdf") {
		t.Errorf("extras fetched %q", static)
	}
	names := tree(t, rel.Folder)
	if !slices.Contains(names, "cover.jpg") || !slices.ContainsFunc(names, func(n string) bool {
		return strings.HasPrefix(n, "Beethoven： Symphony No. 3") && strings.HasSuffix(n, " (168983).pdf")
	}) {
		t.Errorf("files %q", names)
	}

	// Existing extras are kept; only embed_cover.jpg, removed at the end, comes again.
	e.srv.Reset()
	e.mustDownload(t, qobuzdltest.AlbumGoodies, false, nil)
	static = nil
	for _, r := range e.srv.Requests() {
		if strings.HasPrefix(r.Path, "/static/") {
			static = append(static, r.Path)
		}
	}
	if len(static) != 1 || !strings.HasSuffix(static[0], "_300.jpg") {
		t.Errorf("second run fetched %q", static)
	}

	// A cover that fails doesn't fail the album.
	e = newEnv(t)
	e.srv.FailStatic()
	ev := &events{}
	rel = e.mustDownload(t, qobuzdltest.AlbumGoodies, false, ev)
	if rel.Status != qobuzdl.StatusDone || !ev.logged("Skipping cover art 'cover.jpg'") {
		t.Errorf("status %v", rel.Status)
	}
	for _, p := range tree(t, rel.Folder) {
		if p == "cover.jpg" || strings.HasSuffix(p, ".pdf") {
			t.Errorf("%s exists", p)
		}
	}
}

func TestEmbedCoverRemoved(t *testing.T) {
	for _, track := range []bool{false, true} {
		e := newEnv(t)
		e.opts.EmbedArt = true
		e.opts.Settings.EmbedArt = true
		id := qobuzdltest.AlbumHiRes96
		if track {
			id = qobuzdltest.TrackSingle
		}
		rel := e.mustDownload(t, id, track, nil)
		if _, err := os.Stat(filepath.Join(rel.Folder, "embed_cover.jpg")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("embed_cover.jpg: %v", err)
		}
		for _, tr := range rel.Tracks {
			if qobuzdl.FLACPictures(t, tr.Path) != 1 {
				t.Errorf("%s: no embedded cover", tr.Path)
			}
		}
	}
}

func TestAlbumsOnly(t *testing.T) {
	e := newEnv(t)
	e.opts.IgnoreSinglesEPs = true
	for _, id := range []string{qobuzdltest.AlbumCompilation, qobuzdltest.AlbumCD} {
		rel, err := e.download(t, id, false, nil)
		if err != nil || rel.Status != qobuzdl.StatusSkipped || rel.Skip != qobuzdl.SkipAlbumsOnly {
			t.Errorf("%s: %+v, %v", id, rel, err)
		}
	}
	if got := tree(t, e.opts.Directory); len(got) != 0 {
		t.Errorf("wrote %q", got)
	}
	if rel := e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, nil); rel.Status != qobuzdl.StatusDone {
		t.Errorf("an album: %v", rel.Status)
	}
}

func TestProgressEvents(t *testing.T) {
	e := newEnv(t)
	ids := e.trackIDs(t, qobuzdltest.AlbumHiRes96)
	// Five failures use up the first tier; the next one delivers.
	e.srv.FailTimes(ids[1], 5, http.StatusServiceUnavailable)
	ev := &events{}
	e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, ev)

	if len(ev.releases) != 1 {
		t.Fatalf("%d ReleaseStart calls", len(ev.releases))
	}
	r := ev.releases[0]
	if r.Kind != "album" || r.ID != qobuzdltest.AlbumHiRes96 || r.Title != "Begin Again" || r.Album == nil || len(r.Tracks) != 7 {
		t.Errorf("release %+v", r)
	}
	for i, p := range r.Tracks {
		if p.Position != i+1 || p.ID != ids[i] || p.TrackNumber != i+1 || p.DiscNumber != 1 || p.Title == "" || p.ISRC == "" || p.Duration == 0 {
			t.Errorf("planned %+v", p)
		}
	}
	starts := map[int][]int{}
	for _, s := range ev.starts {
		starts[s.Position] = append(starts[s.Position], s.FormatID)
	}
	for pos := 1; pos <= 7; pos++ {
		want := []int{27}
		if pos == 2 {
			want = []int{27, 7}
		}
		if !slices.Equal(starts[pos], want) {
			t.Errorf("track %d started at %v, want %v", pos, starts[pos], want)
		}
		calls := ev.bytes[pos]
		if len(calls) == 0 {
			t.Errorf("track %d: no TrackBytes", pos)
			continue
		}
		if last := calls[len(calls)-1]; last[0] == 0 || last[0] != last[1] {
			t.Errorf("track %d: last bytes %v", pos, last)
		}
	}
	var ended []int
	for _, tr := range ev.ends {
		ended = append(ended, tr.Position)
	}
	slices.Sort(ended)
	if !slices.Equal(ended, []int{1, 2, 3, 4, 5, 6, 7}) {
		t.Errorf("TrackEnd for %v", ended)
	}
}

func TestResultFields(t *testing.T) {
	e := newEnv(t)
	rel := e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, nil)
	want := filepath.Join(e.opts.Directory, "Norah Jones - Begin Again (2019) [24B-96kHz]")
	if rel.Kind != "album" || rel.ID != qobuzdltest.AlbumHiRes96 || rel.Folder != want || rel.Status != qobuzdl.StatusDone || rel.Err != nil {
		t.Errorf("release %+v", rel)
	}
	// At 27 Qobuz answers 24/96 with the downgrade restriction.
	if rel.Format != "FLAC" || rel.QualityMet || rel.BitDepth != 24 || rel.SamplingRate != 96 || rel.Album == nil || rel.Album.Title != "Begin Again" {
		t.Errorf("gate %q %v %d %v", rel.Format, rel.QualityMet, rel.BitDepth, rel.SamplingRate)
	}
	for i, tr := range rel.Tracks {
		if tr.Position != i+1 || tr.Outcome != qobuzdl.OutcomeDone || filepath.Dir(tr.Path) != want || tr.FormatID != 7 ||
			tr.Codec != "FLAC" || tr.BitDepth != 24 || tr.SamplingRate != 96 || tr.ID == "" || tr.ISRC == "" || tr.TrackNumber != i+1 {
			t.Errorf("track %+v", tr)
		}
		if _, err := os.Stat(tr.Path); err != nil {
			t.Error(err)
		}
	}

	rel = e.mustDownload(t, qobuzdltest.TrackSingle, true, nil)
	if rel.Kind != "track" || rel.Track == nil || rel.Album == nil || len(rel.Tracks) != 1 || rel.Status != qobuzdl.StatusDone {
		t.Fatalf("track %+v", rel)
	}
	if tr := rel.Tracks[0]; tr.Position != 1 || filepath.Dir(tr.Path) != rel.Folder || tr.FormatID != 7 || tr.SamplingRate != 96 {
		t.Errorf("track result %+v", tr)
	}
}

func TestFileModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	e := newEnv(t)
	e.mustDownload(t, qobuzdltest.AlbumGoodies, false, nil)
	e.opts.Quality = 5
	e.mustDownload(t, qobuzdltest.TrackSingle, true, nil)
	err := filepath.WalkDir(e.opts.Directory, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == e.opts.Directory {
			return err
		}
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		want := os.FileMode(0o644)
		if d.IsDir() {
			want = 0o755
		}
		if st.Mode().Perm() != want {
			t.Errorf("%s: %v, want %v", p, st.Mode().Perm(), want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDownloadLogsAreClean(t *testing.T) {
	srv := qobuzdltest.New(t)
	var buf strings.Builder
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), &slog.HandlerOptions{Level: slog.LevelDebug}))
	co := srv.ClientOptions()
	co.Logger = logger
	c, err := qobuzdl.NewClient(t.Context(), co)
	if err != nil {
		t.Fatal(err)
	}
	o := qobuzdl.DefaultOptions()
	srv.Configure(&o)
	o.Directory = t.TempDir()
	o.Quality = 27
	o.Logger = logger
	a, err := c.GetAlbumMeta(t.Context(), qobuzdltest.AlbumHiRes96)
	if err != nil {
		t.Fatal(err)
	}
	srv.Fail404(a.Tracks[1].ID)
	srv.FailTimes(a.Tracks[2].ID, 6, http.StatusServiceUnavailable)
	ev := &events{}
	rel, err := qobuzdl.DownloadIDByType(t.Context(), c, o, qobuzdltest.AlbumHiRes96, false, ev)
	if err != nil {
		t.Fatal(err)
	}
	var errs []string
	for _, tr := range rel.Tracks {
		if tr.Err != nil {
			errs = append(errs, tr.Err.Error())
		}
	}
	mu.Lock()
	text := buf.String() + strings.Join(ev.logs, "\n") + strings.Join(errs, "\n")
	mu.Unlock()
	if !strings.Contains(text, "/cdn/file/") || !strings.Contains(text, "Retrying") {
		t.Fatalf("the logs miss the transfers: %s", text)
	}
	for _, secret := range []string{qobuzdltest.Token, qobuzdltest.Secret, qobuzdltest.BadSecret, "hmac", "etsp", "request_sig="} {
		if strings.Contains(text, secret) {
			t.Errorf("the logs hold %q: %s", secret, text)
		}
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

package qobuzdl_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
	"github.com/tikhonp/qobuz-dl-go/qobuzdltest"
)

func needFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not on PATH")
	}
}

// failingFFmpeg writes a stand-in for ffmpeg that prints to stderr and
// exits 1, or, with realAfter, fails once and then runs the real one.
func failingFFmpeg(t *testing.T, realAfter bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'remux broke: %0600d' 0 >&2\nexit 1\n"
	if realAfter {
		ffmpeg, err := exec.LookPath("ffmpeg")
		if err != nil {
			t.Skip("ffmpeg is not on PATH")
		}
		mark := filepath.Join(dir, "failed")
		script = "#!/bin/sh\nif [ ! -e '" + mark + "' ]; then : > '" + mark + "'; echo 'remux broke' >&2; exit 1; fi\nexec '" + ffmpeg + "' \"$@\"\n"
	}
	path := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// trackRequests lists the API calls and CDN fetches that concern one
// track, as "endpoint format" or "METHOD /cdn/kind/format/segment".
func trackRequests(srv *qobuzdltest.Server, id string) []string {
	var out []string
	for _, r := range srv.Requests() {
		switch {
		case strings.HasSuffix(r.Path, "/session/start"):
			out = append(out, "session/start")
		case strings.HasSuffix(r.Path, "/track/getFileUrl") && r.Query.Get("track_id") == id:
			out = append(out, "getFileUrl "+r.Query.Get("format_id"))
		case strings.HasSuffix(r.Path, "/file/url") && r.Query.Get("track_id") == id:
			out = append(out, "file/url "+r.Query.Get("format_id"))
		case strings.HasPrefix(r.Path, "/cdn/file/"+id+"/"):
			out = append(out, r.Method+" "+strings.Replace(r.Path, "/"+id+"/", "/", 1))
		case strings.HasPrefix(r.Path, "/cdn/seg/"+id+"/"):
			out = append(out, r.Method+" "+strings.Replace(r.Path, "/"+id+"/", "/", 1))
		}
	}
	return out
}

func count(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return n
}

func segGets(list []string) []string {
	var out []string
	for _, x := range list {
		if strings.HasPrefix(x, "GET /cdn/seg/") {
			out = append(out, x)
		}
	}
	return out
}

func TestSegmentedPath(t *testing.T) {
	needFFmpeg(t)
	e := newEnv(t)
	ids := e.trackIDs(t, qobuzdltest.AlbumMultiDisc)
	id := ids[2]
	e.srv.Segmented(id)
	ev := &events{}
	rel := e.mustDownload(t, qobuzdltest.AlbumMultiDisc, false, ev)
	tr := rel.Tracks[2]
	if tr.Outcome != qobuzdl.OutcomeDone || tr.FormatID != 27 || tr.BitDepth != 24 || tr.SamplingRate != 192 || tr.Codec != "FLAC" {
		t.Fatalf("track 3: %+v", tr)
	}
	if rel.Status != qobuzdl.StatusDone {
		t.Errorf("status %v", rel.Status)
	}

	// The remuxed audio is the fixture the segments were cut from, frame
	// for frame, and the tagger saw it.
	want := filepath.Join("testdata", "fixtures", "audio", "24-192.flac")
	if !bytes.Equal(qobuzdl.FLACAudio(t, tr.Path), qobuzdl.FLACAudio(t, want)) {
		t.Error("audio differs from 24-192.flac")
	}
	bd, rate, samples := qobuzdl.FLACStreamInfo(t, tr.Path)
	wbd, wrate, wsamples := qobuzdl.FLACStreamInfo(t, want)
	if bd != wbd || rate != wrate || samples != wsamples {
		t.Errorf("stream info %d/%d/%d, want %d/%d/%d", bd, rate, samples, wbd, wrate, wsamples)
	}
	if c := qobuzdl.FLACComments(t, tr.Path); !slices.Contains(c, "TRACKNUMBER=3") || !slices.Contains(c, "ARTIST=Pink Floyd") {
		t.Errorf("comments %q", c)
	}
	if info, err := os.Stat(tr.Path); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("mode %v, %v", info.Mode(), err)
	}
	noTempFiles(t, e.opts.Directory)

	reqs := trackRequests(e.srv, id)
	if n := count(reqs, "session/start"); n != 1 {
		t.Errorf("%d session/start calls", n)
	}
	if !slices.Contains(reqs, "file/url 27") || slices.ContainsFunc(reqs, func(s string) bool { return strings.HasPrefix(s, "GET /cdn/file/") }) {
		t.Errorf("requests %q", reqs)
	}
	for _, seg := range []string{"0", "1", "2", "3", "4"} {
		if count(reqs, "HEAD /cdn/seg/27/"+seg) != 1 || count(reqs, "GET /cdn/seg/27/"+seg) != 1 {
			t.Errorf("segment %s: requests %q", seg, reqs)
		}
	}
	if gets := segGets(reqs); len(gets) != 5 || gets[0] != "GET /cdn/seg/27/0" || gets[1] != "GET /cdn/seg/27/1" {
		t.Errorf("segments 0 and 1 come first, in order: %q", gets)
	}
	for _, r := range e.srv.Requests() {
		if strings.HasPrefix(r.Path, "/cdn/seg/") && r.Header.Get("X-User-Auth-Token") != "" {
			t.Error("a segment request carries the token")
		}
	}

	var started []qobuzdl.TrackInfo
	for _, s := range ev.starts {
		if s.Position == 3 {
			started = append(started, s)
		}
	}
	if len(started) != 1 || !started[0].Segmented || started[0].FormatID != 27 {
		t.Errorf("TrackStart %+v", started)
	}
	last := ev.bytes[3][len(ev.bytes[3])-1]
	if last[0] == 0 || last[0] != last[1] {
		t.Errorf("last TrackBytes %v: the HEAD sizes should add up to the bytes", last)
	}
	if !ev.logged("  > Assembling the final FLAC file...") {
		t.Error("no assembling line")
	}
}

func TestSegmentedFallbacks(t *testing.T) {
	needFFmpeg(t)
	t.Run("getFileUrl without url", func(t *testing.T) {
		e := newEnv(t)
		e.srv.Segmented(qobuzdltest.TrackSingle)
		rel := e.mustDownload(t, qobuzdltest.TrackSingle, true, nil)
		// Qobuz delivers the 24/96 track at 7, segmented as direct.
		if tr := rel.Tracks[0]; tr.Outcome != qobuzdl.OutcomeDone || tr.FormatID != 7 || tr.SamplingRate != 96 {
			t.Fatalf("track: %+v", tr)
		}
		reqs := trackRequests(e.srv, qobuzdltest.TrackSingle)
		want := []string{"getFileUrl 27", "session/start", "file/url 27", "getFileUrl 27", "file/url 27"}
		if got := slices.DeleteFunc(slices.Clone(reqs), func(s string) bool { return strings.Contains(s, "/cdn/") }); !slices.Equal(got, want) {
			t.Errorf("API calls %q, want %q", got, want)
		}
	})
	t.Run("failed direct transfer", func(t *testing.T) {
		e := newEnv(t)
		id := e.trackIDs(t, qobuzdltest.AlbumHiRes96)[0]
		// The direct transfer's five attempts fail; the segments come after.
		e.srv.FailTimes(id, 5, http.StatusServiceUnavailable)
		ev := &events{}
		rel := e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, ev)
		if tr := rel.Tracks[0]; tr.Outcome != qobuzdl.OutcomeDone || tr.FormatID != 7 || tr.SamplingRate != 96 {
			t.Fatalf("track 1: %+v", tr)
		}
		reqs := trackRequests(e.srv, id)
		var api []string
		for _, r := range reqs {
			if !strings.HasPrefix(r, "HEAD ") && !strings.HasPrefix(r, "GET /cdn/seg/") {
				api = append(api, r)
			}
		}
		// The release gate's call, the main loop's, the tier's, five
		// attempts at the delivered 7, then the same tier forced onto
		// segments without asking getFileUrl again.
		want := []string{"getFileUrl 27", "getFileUrl 27", "getFileUrl 27"}
		for range 5 {
			want = append(want, "GET /cdn/file/7")
		}
		want = append(want, "session/start", "file/url 27")
		if !slices.Equal(api, want) {
			t.Errorf("requests %q, want %q", api, want)
		}
		if len(segGets(reqs)) != 5 {
			t.Errorf("segments %q", segGets(reqs))
		}
		if !ev.logged("Akamai block detected") {
			t.Error("no fallback line")
		}
		var segmented []bool
		for _, s := range ev.starts {
			if s.Position == 1 {
				segmented = append(segmented, s.Segmented)
			}
		}
		if !slices.Equal(segmented, []bool{false, true}) {
			t.Errorf("TrackStart segmented %v", segmented)
		}
	})
}

func TestMP3NeverSegmented(t *testing.T) {
	e := newEnv(t)
	e.opts.Quality = 5
	e.srv.Segmented(qobuzdltest.TrackSingle)
	rel, err := e.download(t, qobuzdltest.TrackSingle, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr := rel.Tracks[0]; tr.Outcome != qobuzdl.OutcomeFailed || !errors.Is(tr.Err, qobuzdl.ErrAllTiers) {
		t.Errorf("track: %+v", tr)
	}
	for _, r := range trackRequests(e.srv, qobuzdltest.TrackSingle) {
		if r != "getFileUrl 5" {
			t.Errorf("request %s", r)
		}
	}
	u, err := e.c.GetTrackURL(t.Context(), qobuzdltest.TrackSingle, 5, true)
	if err != nil || u.Segmented || u.URL != "" || u.FormatID != 5 {
		t.Errorf("forced at 5: %+v, %v", u, err)
	}
	for _, r := range e.srv.Requests() {
		if strings.HasSuffix(r.Path, "/session/start") || strings.HasSuffix(r.Path, "/file/url") {
			t.Errorf("request %s", r.Path)
		}
	}
}

func TestSessionStartsOnce(t *testing.T) {
	srv := qobuzdltest.New(t)
	c, err := qobuzdl.NewClient(t.Context(), srv.ClientOptions())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		wg.Go(func() {
			u, err := c.GetTrackURL(t.Context(), qobuzdltest.TrackSingle, []int{6, 7, 27}[i%3], true)
			if err == nil && (!u.Segmented || u.NSegments < 3) {
				err = errors.New("not a segmented answer")
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	starts, urls := 0, 0
	for _, r := range srv.Requests() {
		switch {
		case strings.HasSuffix(r.Path, "/session/start"):
			starts++
			if r.Header.Get("X-Session-Id") != "" {
				t.Error("session/start carries a session")
			}
		case strings.HasSuffix(r.Path, "/file/url"):
			urls++
			if r.Header.Get("X-Session-Id") == "" || r.Status != http.StatusOK {
				t.Errorf("file/url without a session: %d", r.Status)
			}
		}
	}
	if starts != 1 || urls != 16 {
		t.Errorf("%d session/start and %d file/url calls", starts, urls)
	}
}

func TestSegmentedQualityFields(t *testing.T) {
	e := newEnv(t)
	e.opts.Quality = 7
	// No transfer is needed for the names; a missing ffmpeg fails the
	// tracks and leaves the folder in place.
	e.opts.FFmpegPath = filepath.Join(t.TempDir(), "no-ffmpeg")
	for _, id := range e.trackIDs(t, qobuzdltest.AlbumHiRes96) {
		e.srv.Segmented(id)
	}
	u, err := e.c.GetTrackURL(t.Context(), e.trackIDs(t, qobuzdltest.AlbumHiRes96)[0], 7, false)
	if err != nil || u.BitDepth != 24 || u.SamplingRate != 96 || !u.Segmented || u.FormatID != 7 {
		t.Fatalf("track URL %+v, %v", u, err)
	}
	if strings.Contains(string(u.Raw), "key") || strings.Contains(string(u.Raw), "url_template") {
		t.Errorf("Raw keeps a secret: %s", u.Raw)
	}
	rel := e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, nil)
	if rel.BitDepth != 24 || rel.SamplingRate != 96 {
		t.Errorf("gate %d/%v", rel.BitDepth, rel.SamplingRate)
	}
	if want := "Norah Jones - Begin Again (2019) [24B-96.0kHz]"; !strings.HasSuffix(filepath.Base(rel.Folder), want) {
		t.Errorf("folder %q, want it to end in %q", filepath.Base(rel.Folder), want)
	}
	// 44100 Hz is 44.1 after the division.
	e.opts.Quality = 6
	e.opts.Directory = t.TempDir()
	rel = e.mustDownload(t, qobuzdltest.AlbumHiRes96, false, nil)
	if want := "[16B-44.1kHz]"; !strings.HasSuffix(filepath.Base(rel.Folder), want) {
		t.Errorf("folder %q", filepath.Base(rel.Folder))
	}
}

func TestSegmentTempRemoved(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		needFFmpeg(t)
		e := newEnv(t)
		e.srv.Segmented(qobuzdltest.TrackSingle)
		rel := e.mustDownload(t, qobuzdltest.TrackSingle, true, nil)
		if rel.Tracks[0].Outcome != qobuzdl.OutcomeDone {
			t.Fatalf("track: %+v", rel.Tracks[0])
		}
		noTempFiles(t, e.opts.Directory)
	})
	t.Run("remux failure", func(t *testing.T) {
		e := newEnv(t)
		e.opts.FFmpegPath = failingFFmpeg(t, false)
		e.srv.Segmented(qobuzdltest.TrackSingle)
		rel := e.mustDownload(t, qobuzdltest.TrackSingle, true, nil)
		if rel.Tracks[0].Outcome != qobuzdl.OutcomeFailed {
			t.Fatalf("track: %+v", rel.Tracks[0])
		}
		noTempFiles(t, e.opts.Directory)
	})
	t.Run("segment failure", func(t *testing.T) {
		e := newEnv(t)
		e.srv.Segmented(qobuzdltest.TrackSingle)
		e.srv.Fail404(qobuzdltest.TrackSingle)
		rel := e.mustDownload(t, qobuzdltest.TrackSingle, true, nil)
		if rel.Tracks[0].Outcome != qobuzdl.OutcomeFailed {
			t.Fatalf("track: %+v", rel.Tracks[0])
		}
		noTempFiles(t, e.opts.Directory)
	})
	t.Run("cut segment", func(t *testing.T) {
		e := newEnv(t)
		e.opts.Quality = 7
		e.opts.QualityFallback = false
		e.srv.Segmented(qobuzdltest.TrackSingle)
		e.srv.CutAfter(qobuzdltest.TrackSingle, 100)
		rel := e.mustDownload(t, qobuzdltest.TrackSingle, true, nil)
		if rel.Tracks[0].Outcome != qobuzdl.OutcomeFailed {
			t.Fatalf("track: %+v", rel.Tracks[0])
		}
		noTempFiles(t, e.opts.Directory)
	})
	t.Run("cancel", func(t *testing.T) {
		e := newEnv(t)
		e.srv.Segmented(qobuzdltest.TrackSingle)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var sawMP4 bool
		base := e.opts.HTTPClient.Transport
		// The .mp4 holds segments 0 and 1 when segment 2 is asked for.
		e.opts.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/2") && strings.HasPrefix(r.URL.Path, "/cdn/seg/") {
				matches, _ := filepath.Glob(filepath.Join(e.opts.Directory, "*", "*.mp4"))
				sawMP4 = len(matches) > 0
				cancel()
			}
			return base.RoundTrip(r)
		})}
		_, err := qobuzdl.DownloadIDByType(ctx, e.c, e.opts, qobuzdltest.TrackSingle, true, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
		if !sawMP4 {
			t.Error("the .mp4 never existed")
		}
		noTempFiles(t, e.opts.Directory)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// logBuffer collects log lines from several goroutines.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestRemuxFailureNextTier(t *testing.T) {
	t.Run("direct next tier", func(t *testing.T) {
		e := newEnv(t)
		e.opts.FFmpegPath = failingFFmpeg(t, false)
		var logs logBuffer
		e.opts.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		// Tier 27 fails its direct transfer and then its remux; tier 7's
		// direct transfer works.
		e.srv.FailTimes(qobuzdltest.TrackSingle, 5, http.StatusServiceUnavailable)
		ev := &events{}
		rel := e.mustDownload(t, qobuzdltest.TrackSingle, true, ev)
		if tr := rel.Tracks[0]; tr.Outcome != qobuzdl.OutcomeDone || tr.FormatID != 7 {
			t.Fatalf("track: %+v", tr)
		}
		var starts []qobuzdl.TrackInfo
		for _, s := range ev.starts {
			starts = append(starts, qobuzdl.TrackInfo{FormatID: s.FormatID, Segmented: s.Segmented})
		}
		want := []qobuzdl.TrackInfo{{FormatID: 27}, {FormatID: 27, Segmented: true}, {FormatID: 7}}
		if !slices.Equal(starts, want) {
			t.Errorf("TrackStart %+v, want %+v", starts, want)
		}
		text := logs.String()
		if !strings.Contains(text, "FFmpeg remux failed for ~tmp_01.tmp") || !strings.Contains(text, "remux broke: 0000") {
			t.Errorf("no remux error with its stderr in the log:\n%s", text)
		}
		if strings.Contains(text, strings.Repeat("0", 500)) {
			t.Error("stderr not cut to 500 bytes")
		}
		noTempFiles(t, e.opts.Directory)
	})
	t.Run("segmented next tier", func(t *testing.T) {
		e := newEnv(t)
		e.opts.FFmpegPath = failingFFmpeg(t, true)
		e.srv.Segmented(qobuzdltest.TrackSingle)
		rel := e.mustDownload(t, qobuzdltest.TrackSingle, true, nil)
		if tr := rel.Tracks[0]; tr.Outcome != qobuzdl.OutcomeDone || tr.FormatID != 7 || tr.SamplingRate != 96 {
			t.Fatalf("track: %+v", tr)
		}
		want := filepath.Join("testdata", "fixtures", "audio", "24-96.flac")
		if !bytes.Equal(qobuzdl.FLACAudio(t, rel.Tracks[0].Path), qobuzdl.FLACAudio(t, want)) {
			t.Error("audio differs from 24-96.flac")
		}
	})
}

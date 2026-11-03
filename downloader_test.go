package qobuzdl

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

// layoutEntry is one file or folder of a download tree, as gen.py's
// layout_tree records it.
type layoutEntry struct {
	path string
	full string
	dir  bool
}

func walkLayout(t *testing.T, root string) []layoutEntry {
	t.Helper()
	var out []layoutEntry
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, layoutEntry{path: filepath.ToSlash(rel), full: p, dir: e.IsDir()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(out, func(a, b layoutEntry) int { return strings.Compare(a.path, b.path) })
	return out
}

// checkLayout compares the tree under root with a layout golden's output.
func checkLayout(t *testing.T, root string, want []any) {
	t.Helper()
	got := walkLayout(t, root)
	var gotPaths, wantPaths []string
	for _, e := range got {
		gotPaths = append(gotPaths, e.path)
	}
	for _, w := range want {
		wantPaths = append(wantPaths, w.(*pyDict).get("path", nil).(string))
	}
	if !slices.Equal(gotPaths, wantPaths) {
		t.Fatalf("tree:\n got %q\nwant %q", gotPaths, wantPaths)
	}
	for i, e := range got {
		w := want[i].(*pyDict)
		switch typ := w.get("type", nil).(string); typ {
		case "dir":
			if !e.dir {
				t.Errorf("%s: not a folder", e.path)
			}
		case "flac":
			compareFLACResult(t, flacResult(t, e.full), w.get("result", nil).(*pyDict))
		case "mp3":
			compareMP3Result(t, mp3Result(t, e.full), w.get("result", nil).(*pyDict))
		case "text":
			data, err := os.ReadFile(e.full)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != w.get("text", nil) {
				t.Errorf("%s:\n%s\nwant:\n%s", e.path, data, w.get("text", nil))
			}
		case "file":
			if got := fileSHA(t, e.full); got != w.get("sha256", nil) {
				t.Errorf("%s: sha256 %s, want %s", e.path, got, w.get("sha256", nil))
			}
		default:
			t.Fatalf("%s: type %s", e.path, typ)
		}
	}
}

// compareMP3Result compares mp3Result with gen.py's mp3_result; frame order
// differs from mutagen's, so frames compare as sets.
func compareMP3Result(t *testing.T, got, want *pyDict) {
	t.Helper()
	for _, k := range []string{"version", "frames", "audio", "raw"} {
		g, w := got.get(k, nil), want.get(k, nil)
		if gl, ok := g.([]any); ok {
			wl, _ := w.([]any)
			wl = slices.Clone(wl)
			sortByRepr(gl)
			sortByRepr(wl)
			w = wl
		}
		if g, w := pyRepr(g), pyRepr(w); g != w {
			t.Errorf("%s:\n got %s\nwant %s", k, g, w)
		}
	}
}

func TestSafeGet(t *testing.T) {
	d := newDict()
	performer := newDict()
	performer.set("name", "P")
	d.set("performer", performer)
	d.set("none", nil)
	d.set("text", "abc")
	d.set("num", int64(5))
	for _, c := range []struct {
		keys    []string
		def     any
		want    any
		wantErr bool
	}{
		{[]string{"performer", "name"}, nil, "P", false},
		{[]string{"missing", "name"}, "D", "D", false},
		{[]string{"none", "name"}, "D", nil, false},
		{[]string{"num", "name"}, nil, int64(5), false},
		// Python calls .get on the string.
		{[]string{"text", "name"}, nil, nil, true},
		// The default stops the walk even when it can be indexed.
		{[]string{"text", "name"}, "abc", "abc", false},
	} {
		got, err := safeGet(d, c.def, c.keys...)
		if (err != nil) != c.wantErr || (err == nil && !pyEqual(got, c.want)) {
			t.Errorf("%v: got %v, %v", c.keys, got, err)
		}
	}
}

func TestCleanFormatStr(t *testing.T) {
	for _, c := range [][4]string{
		// The suffix is checked before the strip, as in Python.
		{" {a}.flac ", "{b}.mp3", "{a}.flac", "{b}"},
		{"{a}.mp3.flac", "{b}.FLAC", "{a}.mp3", "{b}.FLAC"},
		{"\u3000x\u3000", "y", "x", "y"},
	} {
		f, tr := cleanFormatStr(c[0], c[1])
		if f != c[2] || tr != c[3] {
			t.Errorf("%q %q: got %q %q", c[0], c[1], f, tr)
		}
	}
}

func TestGetDescription(t *testing.T) {
	item := newDict()
	item.set("bit_depth", int64(24))
	item.set("sampling_rate", 96.0)
	if got := getDescription(item, "T", nil); got != "T [24/96.0]" {
		t.Errorf("got %q", got)
	}
	if got := getDescription(newDict(), "T", int64(2)); got != "[CD 2] T [/]" {
		t.Errorf("got %q", got)
	}
}

func TestPythonPaths(t *testing.T) {
	for _, c := range [][3]string{
		{"/a/b", "/a", "b"}, {"/a/b/", "/a/b", ""}, {"a", "", "a"}, {"/a", "/", "a"}, {"//a", "//", "a"}, {"a//b", "a", "b"},
	} {
		if h, tl := pySplit(c[0]); h != c[1] || tl != c[2] {
			t.Errorf("split %q: %q %q", c[0], h, tl)
		}
	}
	for _, c := range [][3]string{
		{"a/b.flac", "a/b", ".flac"}, {"a/.flac", "a/.flac", ""}, {"a.b/c", "a.b/c", ""}, {"a/..x", "a/..x", ""}, {"a/b.c.d", "a/b.c", ".d"},
	} {
		if r, e := pySplitext(c[0]); r != c[1] || e != c[2] {
			t.Errorf("splitext %q: %q %q", c[0], r, e)
		}
	}
	if got := pyJoin("a", "/b", "c"); got != "/b/c" {
		t.Errorf("join %q", got)
	}
	for _, c := range [][2]string{{"5", "05"}, {"-5", "-5"}, {"+5", "+5"}, {"123", "123"}, {"", "00"}, {"None", "None"}, {"x", "0x"}} {
		if got := pyZfill(c[0], 2); got != c[1] {
			t.Errorf("zfill %q: %q", c[0], got)
		}
	}
	for _, c := range [][4]int64{{125, 60, 2, 5}, {-61, 60, -2, 59}, {0, 60, 0, 0}} {
		if q, r := floorDivmod(c[0], c[1]); q != c[2] || r != c[3] {
			t.Errorf("divmod %d: %d %d", c[0], q, r)
		}
	}
}

// gateAnswer builds a getFileUrl answer for getFormat.
func gateAnswer(kv ...any) *pyDict {
	d := newDict()
	for i := 0; i < len(kv); i += 2 {
		d.set(kv[i].(string), kv[i+1])
	}
	return d
}

func TestGetFormat(t *testing.T) {
	restriction := func(code any) *pyDict { return gateAnswer("code", code) }
	for _, c := range []struct {
		name     string
		quality  int
		answer   *pyDict
		format   string
		met      bool
		bd, rate any
	}{
		{"plain", 27, gateAnswer("bit_depth", int64(24), "sampling_rate", int64(96)), "FLAC", true, int64(24), int64(96)},
		{"mp3", 5, gateAnswer("bit_depth", int64(16), "sampling_rate", 44.1), "MP3", true, int64(16), 44.1},
		{"downgraded", 27, gateAnswer("restrictions", []any{restriction("Other"), restriction(qlDowngrade)}, "bit_depth", int64(24), "sampling_rate", int64(96)), "FLAC", false, int64(24), int64(96)},
		{"restrictions not a list", 27, gateAnswer("restrictions", restriction(qlDowngrade), "bit_depth", int64(24), "sampling_rate", int64(96)), "FLAC", true, int64(24), int64(96)},
		{"a restriction not a dict", 27, gateAnswer("restrictions", []any{"x"}, "bit_depth", int64(24)), "Unknown", true, nil, nil},
		{"met already false", 27, gateAnswer("restrictions", []any{restriction(qlDowngrade)}, "sampling_rate", int64(96)), "Unknown", false, nil, nil},
		{"no rate", 27, gateAnswer("bit_depth", int64(24)), "Unknown", true, nil, nil},
		{"rate None", 27, gateAnswer("bit_depth", int64(24), "sampling_rate", nil), "FLAC", true, int64(24), nil},
	} {
		o := DefaultOptions()
		o.Quality = c.quality
		d := newDownload(nil, o, "id", "", nil)
		format, met, bd, rate, err := d.getFormat(context.Background(), newDict(), true, c.answer)
		if err != nil || format != c.format || met != c.met || !pyEqual(bd, c.bd) || pyTypeName(bd) != pyTypeName(c.bd) ||
			!pyEqual(rate, c.rate) || pyTypeName(rate) != pyTypeName(c.rate) {
			t.Errorf("%s: got %s %v %v %v %v", c.name, format, met, bd, rate, err)
		}
	}

	d := newDownload(nil, DefaultOptions(), "id", "", nil)
	for _, album := range []*pyDict{
		newDict(),
		gateAnswer("tracks", gateAnswer("items", []any{})),
		gateAnswer("tracks", gateAnswer()),
	} {
		if _, _, _, _, err := d.getFormat(context.Background(), album, false, nil); !errors.Is(err, ErrNonStreamable) {
			t.Errorf("%s: got %v", pyRepr(album), err)
		}
	}
	if _, _, _, _, err := d.getFormat(context.Background(), gateAnswer("tracks", nil), false, nil); !isPy(err, pyAttributeError) {
		t.Errorf("tracks None: got %v", err)
	}
	// A first track without an ID fails inside the catch-all.
	format, _, _, _, err := d.getFormat(context.Background(), gateAnswer("tracks", gateAnswer("items", []any{newDict()})), false, nil)
	if err != nil || format != "Unknown" {
		t.Errorf("no ID: got %s %v", format, err)
	}
}

// recordingProgress keeps every TrackBytes call.
type recordingProgress struct {
	noProgress
	mu    sync.Mutex
	bytes [][2]int64
	at    []time.Duration
	start time.Time
}

func (p *recordingProgress) TrackBytes(_ int, done, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bytes = append(p.bytes, [2]int64{done, total})
	p.at = append(p.at, time.Since(p.start))
}

func TestProgressBytesThrottled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &recordingProgress{start: time.Now()}
		m := newByteMeter(p, 3)
		for i := 1; i <= 20; i++ {
			time.Sleep(100 * time.Millisecond)
			m.update(int64(i*10), 200)
		}
		m.finish()
		// Every 250 ms: at 300, 600, 900 ms and so on, then the last values.
		want := []time.Duration{300, 600, 900, 1200, 1500, 1800, 2000}
		if len(p.at) != len(want) {
			t.Fatalf("calls at %v", p.at)
		}
		for i, w := range want {
			if p.at[i] != w*time.Millisecond {
				t.Errorf("call %d at %v, want %v", i, p.at[i], w*time.Millisecond)
			}
		}
		if last := p.bytes[len(p.bytes)-1]; last != [2]int64{200, 200} {
			t.Errorf("last call %v", last)
		}
	})
}

// progressLog is a Progress that keeps its log lines.
type progressLog struct {
	noProgress
	mu    sync.Mutex
	lines []string
}

func (p *progressLog) Log(level slog.Level, msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lines = append(p.lines, level.String()+" "+msg)
}

func TestProgressGetsTheLogLines(t *testing.T) {
	var b strings.Builder
	p := &progressLog{}
	o := DefaultOptions()
	o.Logger = textLogger(&b)
	d := newDownload(nil, o, "id", "", p)
	d.log.Debug("debug only")
	d.info("an info line")
	d.warn("a warning")
	if want := []string{"INFO an info line", "WARN a warning"}; !slices.Equal(p.lines, want) {
		t.Errorf("progress got %q", p.lines)
	}
	for _, w := range []string{"debug only", "an info line", "a warning"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("the logger lost %q: %s", w, b.String())
		}
	}
}

func TestMakeDirsModes(t *testing.T) {
	old := setUmask(0o077)
	defer setUmask(old)
	root := t.TempDir()
	p := filepath.Join(root, "a", "b", "c")
	if err := makeDirs(p); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(root, "a"), filepath.Join(root, "a", "b"), p} {
		st, err := os.Stat(d)
		if err != nil || st.Mode().Perm() != 0o755 {
			t.Errorf("%s: %v %v", d, st.Mode(), err)
		}
	}
	f := filepath.Join(p, "f")
	if err := writeFile(f, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(f); st.Mode().Perm() != 0o644 {
		t.Errorf("file %v", st.Mode())
	}
	if err := makeDirs(f); err == nil {
		t.Error("a file in the way")
	}
}

// setUmask sets the process umask and returns the old one. Tests that call
// it don't run in parallel.
func setUmask(mask int) int { return syscall.Umask(mask) }

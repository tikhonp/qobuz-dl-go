package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
	"github.com/tikhonp/qobuz-dl-go/qobuzdltest"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var testEnv = envOf(map[string]string{"HOME": "/home/u"})

// pythonDLFlags are the dl flags of commands.py at 2.5.9: dl_args and
// add_common_arg.
var pythonDLFlags = []string{
	"b", "blacklist", "filter-label",
	"d", "directory", "no-lrc-files", "q", "quality", "albums-only", "no-m3u", "no-fallback", "no-db",
	"ff", "folder-format", "fbff", "fallback-folder-format", "tf", "track-format",
	"s", "smart-discography", "delay", "playlist-as-albums", "no-lyrics", "no-embed-lyrics",
	"multi-tags", "booklet-only", "native-lang", "no-credits", "with-credits",
	"no-album-artist-tag", "no-album-title-tag", "no-track-artist-tag", "no-track-title-tag",
	"no-release-date-tag", "no-media-type-tag", "no-genre-tag", "no-replaygain-tag", "no-album-url-tag",
	"no-track-number-tag", "no-track-total-tag", "no-disc-number-tag", "no-disc-total-tag",
	"no-composer-tag", "no-explicit-tag", "no-copyright-tag", "no-label-tag", "no-upc-tag", "no-isrc-tag",
	"e", "embed-art", "og-cover", "no-cover", "embedded-art-size", "saved-art-size",
	"multiple-disc-prefix", "multiple-disc-one-dir", "multiple-disc-track-format", "max-workers",
}

func mustParseDL(t *testing.T, args ...string) *dlConfig {
	t.Helper()
	cfg, err := parseDL(args, testEnv, io.Discard)
	if err != nil {
		t.Fatalf("parseDL(%q): %v", args, err)
	}
	return cfg
}

func TestFlagsMapToOptions(t *testing.T) {
	base := mustParseDL(t, "U")
	if !reflect.DeepEqual(base.opts, qobuzdl.DefaultOptions()) {
		t.Errorf("no flags: options differ from DefaultOptions:\n%+v", base.opts)
	}
	if base.dbPath != "/home/u/.config/qobuz-dl/qobuz_dl.db" || base.verbose || !slices.Equal(base.urls, []string{"U"}) {
		t.Errorf("no flags: db %q, verbose %v, urls %q", base.dbPath, base.verbose, base.urls)
	}

	type change func(c *dlConfig)
	cases := []struct {
		args []string
		want change
	}{
		{[]string{"--directory", "out"}, func(c *dlConfig) { c.opts.Directory = "out" }},
		{[]string{"--quality", "27"}, func(c *dlConfig) { c.opts.Quality = 27 }},
		{[]string{"--albums-only"}, func(c *dlConfig) { c.opts.IgnoreSinglesEPs = true }},
		{[]string{"--no-m3u"}, func(c *dlConfig) { c.opts.NoM3UForPlaylists = true }},
		{[]string{"--no-fallback"}, func(c *dlConfig) { c.opts.QualityFallback = false }},
		{[]string{"--no-db"}, func(c *dlConfig) { c.dbPath = "" }},
		{[]string{"--db", "/x/q.db"}, func(c *dlConfig) { c.dbPath = "/x/q.db" }},
		{[]string{"--db", "/x/q.db", "--no-db"}, func(c *dlConfig) { c.dbPath = "" }},
		{[]string{"--folder-format", "{album}"}, func(c *dlConfig) { c.opts.FolderFormat = "{album}" }},
		{[]string{"--fallback-folder-format", "{artist}"}, func(c *dlConfig) { c.opts.Settings.FallbackFolderFormat = "{artist}" }},
		{[]string{"--track-format", "{track_title}"}, func(c *dlConfig) { c.opts.TrackFormat = "{track_title}" }},
		{[]string{"--smart-discography"}, func(c *dlConfig) { c.opts.SmartDiscography = true }},
		{[]string{"--delay", "3"}, func(c *dlConfig) { c.opts.Settings.Delay = 3 * time.Second }},
		{[]string{"--delay", "-3"}, func(c *dlConfig) {}},
		{[]string{"--multi-tags"}, func(c *dlConfig) { c.opts.Settings.MultiValueTags = true }},
		{[]string{"--native-lang"}, func(c *dlConfig) { c.opts.ForceEnglish = false }},
		{[]string{"--no-credits"}, func(c *dlConfig) { c.opts.NoCredits = true }},
		{[]string{"--with-credits"}, func(c *dlConfig) {}},
		{[]string{"--no-credits", "--with-credits"}, func(c *dlConfig) {}},
		{[]string{"--embed-art"}, func(c *dlConfig) { c.opts.EmbedArt = true; c.opts.Settings.EmbedArt = true }},
		{[]string{"--og-cover"}, func(c *dlConfig) {}},
		{[]string{"--no-lyrics"}, func(c *dlConfig) {}},
		{[]string{"--no-lrc-files"}, func(c *dlConfig) {}},
		{[]string{"--no-embed-lyrics"}, func(c *dlConfig) {}},
		{[]string{"--no-cover"}, func(c *dlConfig) { c.opts.Settings.NoCover = true }},
		{[]string{"--embedded-art-size", "max"}, func(c *dlConfig) { c.opts.Settings.EmbeddedArtSize = "max" }},
		{[]string{"--saved-art-size", "300"}, func(c *dlConfig) { c.opts.Settings.SavedArtSize = "300" }},
		{[]string{"--multiple-disc-prefix", "Disc"}, func(c *dlConfig) { c.opts.Settings.MultipleDiscPrefix = "Disc" }},
		{[]string{"--multiple-disc-one-dir"}, func(c *dlConfig) { c.opts.Settings.MultipleDiscOneDir = true }},
		{[]string{"--multiple-disc-track-format", "{track_number}"}, func(c *dlConfig) { c.opts.Settings.MultipleDiscTrackFormat = "{track_number}" }},
		{[]string{"--max-workers", "5"}, func(c *dlConfig) { c.opts.Settings.MaxWorkers = 5 }},
		{[]string{"--max-workers", "0"}, func(c *dlConfig) {}},
		{[]string{"--legacy-charmap"}, func(c *dlConfig) { c.opts.Settings.LegacyCharmap = true }},
		{[]string{"--disable-smart-truncation"}, func(c *dlConfig) { c.opts.Settings.DisableSmartTruncation = true }},
		{[]string{"-v"}, func(c *dlConfig) { c.verbose = true }},
		// cli.py and settings.py take the default for an empty value.
		{[]string{"--directory", "", "--folder-format", "", "--track-format", "", "--fallback-folder-format", "",
			"--multiple-disc-prefix", "", "--multiple-disc-track-format", ""}, func(c *dlConfig) {}},
	}
	var tagSettings qobuzdl.Settings
	for _, tf := range tagFlags(&tagSettings) {
		cases = append(cases, struct {
			args []string
			want change
		}{[]string{"--" + tf.name}, func(c *dlConfig) {
			for _, f := range tagFlags(&c.opts.Settings) {
				if f.name == tf.name {
					*f.p = true
				}
			}
		}})
	}

	covered := map[string]bool{}
	for _, tc := range cases {
		got := mustParseDL(t, append(slices.Clone(tc.args), "U")...)
		want := mustParseDL(t, "U")
		tc.want(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q:\n got %+v\nwant %+v", tc.args, got, want)
		}
		covered[strings.TrimLeft(tc.args[0], "-")] = true
	}

	// Each set tag switch turns on exactly one setting.
	for _, tf := range tagFlags(&tagSettings) {
		got := mustParseDL(t, "--"+tf.name, "U")
		n := 0
		for _, f := range tagFlags(&got.opts.Settings) {
			if *f.p {
				n++
			}
		}
		if n != 1 {
			t.Errorf("--%s sets %d tag switches", tf.name, n)
		}
	}
	if n := len(tagFlags(&tagSettings)); n != 22 {
		t.Errorf("%d tag switches, want 22", n)
	}

	// Python's features that are not ported are refused, not ignored.
	for _, args := range [][]string{
		{"-b", "list.txt"}, {"--blacklist", "list.txt"}, {"--filter-label", "Naïve"},
		{"--playlist-as-albums"}, {"--booklet-only"},
	} {
		_, err := parseDL(append(slices.Clone(args), "U"), testEnv, io.Discard)
		var ue *usageError
		if !errors.As(err, &ue) || !strings.Contains(ue.msg, "not ported") {
			t.Errorf("%q: error %v, want a usage error saying not ported", args, err)
		}
		covered[strings.TrimLeft(args[0], "-")] = true
	}

	// Every long Python name is in a case above; short names are in
	// TestShortAndLongFlags.
	short := map[string]bool{"b": true, "d": true, "q": true, "ff": true, "fbff": true, "tf": true, "s": true, "e": true}
	for _, name := range pythonDLFlags {
		if !short[name] && !covered[name] {
			t.Errorf("Python flag --%s has no case", name)
		}
	}
}

func TestShortAndLongFlags(t *testing.T) {
	pairs := []struct{ short, long, value string }{
		{"d", "directory", "out"},
		{"q", "quality", "7"},
		{"ff", "folder-format", "{album}"},
		{"fbff", "fallback-folder-format", "{artist}"},
		{"tf", "track-format", "{track_title}"},
		{"s", "smart-discography", ""},
		{"e", "embed-art", ""},
	}
	for _, p := range pairs {
		var forms [][]string
		for _, name := range []string{"-" + p.short, "--" + p.short, "-" + p.long, "--" + p.long} {
			if p.value == "" {
				forms = append(forms, []string{name})
			} else {
				forms = append(forms, []string{name, p.value}, []string{name + "=" + p.value})
			}
		}
		first := mustParseDL(t, append(forms[0], "U")...)
		if reflect.DeepEqual(first, mustParseDL(t, "U")) {
			t.Errorf("%q changes nothing", forms[0])
		}
		for _, f := range forms[1:] {
			if got := mustParseDL(t, append(f, "U")...); !reflect.DeepEqual(got, first) {
				t.Errorf("%q gives %+v, %q gives %+v", f, got.opts, forms[0], first.opts)
			}
		}
	}
}

func TestFlagsAfterTheURLs(t *testing.T) {
	cfg := mustParseDL(t, "U1", "-q", "27", "U2", "--no-db", "--", "-not-a-flag")
	if cfg.opts.Quality != 27 || cfg.dbPath != "" || !slices.Equal(cfg.urls, []string{"U1", "U2", "-not-a-flag"}) {
		t.Errorf("quality %d, db %q, urls %q", cfg.opts.Quality, cfg.dbPath, cfg.urls)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"nope"},
		{"dl"},
		{"dl", "-q", "3", "U"},
		{"dl", "--embedded-art-size", "700", "U"},
		{"dl", "--max-workers", "-1", "U"},
		{"dl", "--unknown", "U"},
		{"dl", "-q", "x", "U"},
		{"search", "albums"},
		{"search", "songs", "so what"},
		{"search", "-l", "0", "albums", "so what"},
	} {
		var out, errb bytes.Buffer
		c := &cli{stdout: &out, stderr: &errb, getenv: envOf(map[string]string{"QOBUZ_TOKEN": "t"})}
		if code := c.run(t.Context(), args); code != exitUsage {
			t.Errorf("%q: exit %d, want 2; stderr %q", args, code, errb.String())
		}
		if errb.Len() == 0 {
			t.Errorf("%q: nothing on stderr", args)
		}
	}
}

func TestMissingToken(t *testing.T) {
	for _, args := range [][]string{
		{"dl", "https://play.qobuz.com/album/" + qobuzdltest.AlbumCD},
		{"search", "albums", "miles davis"},
	} {
		var out, errb bytes.Buffer
		c := &cli{stdout: &out, stderr: &errb, getenv: envOf(map[string]string{"HOME": t.TempDir()})}
		if code := c.run(t.Context(), args); code != exitUsage {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
		if out.Len() != 0 {
			t.Errorf("%q: stdout %q", args, out.String())
		}
		msg := errb.String()
		if !strings.Contains(msg, "QOBUZ_TOKEN is not set") || strings.Contains(msg, "=") {
			t.Errorf("%q: stderr %q, want the variable's name and no value", args, msg)
		}
	}
}

// fakeCLI is a cli against a fake Qobuz, with the token in QOBUZ_TOKEN.
type fakeCLI struct {
	srv         *qobuzdltest.Server
	home        string
	out, errOut bytes.Buffer
}

func newFakeCLI(t *testing.T) *fakeCLI {
	return &fakeCLI{srv: qobuzdltest.New(t), home: t.TempDir()}
}

func (f *fakeCLI) run(t *testing.T, args ...string) int {
	t.Helper()
	f.out.Reset()
	f.errOut.Reset()
	c := &cli{
		stdout: &f.out, stderr: &f.errOut,
		getenv:    envOf(map[string]string{"QOBUZ_TOKEN": qobuzdltest.Token, "HOME": f.home}),
		configure: f.srv.Configure,
	}
	return c.run(t.Context(), args)
}

func albumURL(id string) string { return "https://play.qobuz.com/album/" + id }

// files lists the files under dir, relative and sorted.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			r, _ := filepath.Rel(dir, p)
			out = append(out, r)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func albumTracks(t *testing.T, f *fakeCLI, id string) []qobuzdl.Track {
	t.Helper()
	c, err := qobuzdl.NewClient(t.Context(), f.srv.ClientOptions())
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.GetAlbumMeta(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a.Tracks
}

func TestDLAgainstFake(t *testing.T) {
	f := newFakeCLI(t)
	tracks := albumTracks(t, f, qobuzdltest.AlbumHiRes96)
	dir := t.TempDir()
	if code := f.run(t, "dl", "-q", "27", "--no-db", "-d", dir, albumURL(qobuzdltest.AlbumHiRes96)); code != exitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, f.out.String(), f.errOut.String())
	}
	var flacs []string
	folders := map[string]bool{}
	for _, p := range files(t, dir) {
		if strings.HasSuffix(p, ".flac") {
			flacs = append(flacs, p)
			folders[filepath.Dir(p)] = true
		}
	}
	if len(flacs) != len(tracks) || len(folders) != 1 {
		t.Fatalf("%d FLAC files in %d folders, want %d in one: %q", len(flacs), len(folders), len(tracks), files(t, dir))
	}
	for folder := range folders {
		if strings.Contains(folder, "[IN PROGRESS]") || strings.Contains(folder, "[INCOMPLETE]") {
			t.Errorf("folder %q", folder)
		}
	}
	out := f.out.String()
	if !strings.Contains(out, "== album "+qobuzdltest.AlbumHiRes96+": Begin Again, 7 tracks, in [IN PROGRESS] ") {
		t.Errorf("no release line:\n%s", out)
	}
	for i := range tracks {
		if !strings.Contains(out, "\n  ["+strconv.Itoa(i+1)+"] done: ") {
			t.Errorf("no done line for track %d:\n%s", i+1, out)
		}
	}
	if !strings.Contains(out, "FLAC 24-bit 96kHz") {
		t.Errorf("no format in the track lines:\n%s", out)
	}

	// The same run again finds every file there.
	if code := f.run(t, "dl", "-q", "27", "--no-db", "-d", dir, albumURL(qobuzdltest.AlbumHiRes96)); code != exitOK {
		t.Fatalf("second run: exit %d\n%s", code, f.out.String())
	}
	if n := strings.Count(f.out.String(), "] exists: "); n != len(tracks) {
		t.Errorf("second run: %d tracks exist, want %d:\n%s", n, len(tracks), f.out.String())
	}

	// A failed track fails the run.
	f.srv.Fail404(tracks[0].ID)
	dir2 := t.TempDir()
	if code := f.run(t, "dl", "-q", "27", "--no-db", "-d", dir2, albumURL(qobuzdltest.AlbumHiRes96)); code != exitFailed {
		t.Fatalf("with a failing track: exit %d, want 1\n%s", code, f.out.String())
	}
	if !strings.Contains(f.out.String(), "  [1] failed: ") {
		t.Errorf("no failed line:\n%s", f.out.String())
	}
}

func TestDLGoesOnAfterAnInvalidURL(t *testing.T) {
	f := newFakeCLI(t)
	dir := t.TempDir()
	code := f.run(t, "dl", "--no-db", "-d", dir, "https://example.com/nothing", albumURL(qobuzdltest.AlbumCD))
	if code != exitFailed {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(f.out.String(), `Invalid url: "https://example.com/nothing". Use urls from https://play.qobuz.com!`) {
		t.Errorf("no invalid-URL line:\n%s", f.out.String())
	}
	if !strings.Contains(f.out.String(), "== album "+qobuzdltest.AlbumCD) {
		t.Errorf("the second URL didn't download:\n%s", f.out.String())
	}
}

func TestDLReadsOpenQobuzLinks(t *testing.T) {
	f := newFakeCLI(t)
	code := f.run(t, "dl", "--no-db", "-d", t.TempDir(), "https://open.qobuz.com/album/"+qobuzdltest.AlbumCD)
	if code != exitOK || !strings.Contains(f.out.String(), "== album "+qobuzdltest.AlbumCD) {
		t.Errorf("exit %d:\n%s%s", code, f.out.String(), f.errOut.String())
	}
}

func TestDLUsesTheDownloadsDatabase(t *testing.T) {
	f := newFakeCLI(t)
	db := filepath.Join(t.TempDir(), "new", "qobuz_dl.db")
	url := albumURL(qobuzdltest.AlbumCD)
	if code := f.run(t, "dl", "--db", db, "-d", t.TempDir(), url); code != exitOK {
		t.Fatalf("exit %d\n%s%s", code, f.out.String(), f.errOut.String())
	}
	// A new folder gets the album again, since the database has it.
	if code := f.run(t, "dl", "--db", db, "-d", t.TempDir(), url); code != exitOK {
		t.Fatalf("second run: exit %d", code)
	}
	if !strings.Contains(f.out.String(), "This release ID ("+qobuzdltest.AlbumCD+") was already downloaded according to the local database.") {
		t.Errorf("second run didn't skip:\n%s", f.out.String())
	}

	// Without --db the database is Python's, under $HOME/.config.
	if code := f.run(t, "dl", "-d", t.TempDir(), url); code != exitOK {
		t.Fatalf("default database: exit %d\n%s", code, f.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(f.home, ".config", "qobuz-dl", "qobuz_dl.db")); err != nil {
		t.Error(err)
	}
}

func TestDLPlaylist(t *testing.T) {
	f := newFakeCLI(t)
	dir := t.TempDir()
	if code := f.run(t, "dl", "-q", "27", "--no-db", "-d", dir, "https://play.qobuz.com/playlist/"+qobuzdltest.Playlist); code != exitOK {
		t.Fatalf("exit %d\n%s%s", code, f.out.String(), f.errOut.String())
	}
	var m3u int
	for _, p := range files(t, dir) {
		if strings.HasSuffix(p, ".m3u8") {
			m3u++
		}
	}
	if m3u != 1 || !strings.Contains(f.out.String(), "== playlist "+qobuzdltest.Playlist+": Top 10 Operas, 10 tracks") {
		t.Errorf("%d m3u8 files\n%s", m3u, f.out.String())
	}
}

func TestSearchAgainstFake(t *testing.T) {
	f := newFakeCLI(t)
	if code := f.run(t, "search", "albums", "miles", "davis"); code != exitOK {
		t.Fatalf("exit %d\n%s", code, f.errOut.String())
	}
	// The lines are search_by_type's, which the search_text golden checks
	// in the library.
	o := qobuzdl.DefaultOptions()
	f.srv.Configure(&o)
	o.Directory = t.TempDir()
	d, err := qobuzdl.New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InitializeClient(t.Context(), "", "", qobuzdltest.AppID, []string{qobuzdltest.Secret}); err != nil {
		t.Fatal(err)
	}
	want, err := d.SearchByType(t.Context(), "miles davis", "album", 10)
	if err != nil {
		t.Fatal(err)
	}
	var wantOut strings.Builder
	for _, r := range want {
		wantOut.WriteString(r.Text + "   " + r.URL + "\n")
	}
	if len(want) == 0 || f.out.String() != wantOut.String() {
		t.Errorf("got\n%s\nwant\n%s", f.out.String(), wantOut.String())
	}
	if !strings.HasPrefix(f.out.String(), "Miles Davis") {
		t.Errorf("first line %q", strings.SplitN(f.out.String(), "\n", 2)[0])
	}
	var searched bool
	for _, r := range f.srv.Requests() {
		if strings.HasSuffix(r.Path, "/catalog/search") {
			searched = true
			if r.Query.Get("query") != "miles davis" || r.Query.Get("type") != "albums" || r.Query.Get("limit") != "10" {
				t.Errorf("search query %v", r.Query)
			}
		}
	}
	if !searched {
		t.Error("no catalog/search request")
	}

	if code := f.run(t, "search", "-l", "3", "playlists", "jazz"); code != exitOK || strings.Count(f.out.String(), "\n") != 3 {
		t.Errorf("exit %d, %d lines", code, strings.Count(f.out.String(), "\n"))
	}
	if code := f.run(t, "search", "tracks", "so"); code != exitUsage {
		t.Errorf("short query: exit %d, want 2", code)
	}
}

func TestCLINeverPrintsToken(t *testing.T) {
	f := newFakeCLI(t)
	dir := t.TempDir()
	check := func(name string) {
		t.Helper()
		for stream, s := range map[string]string{"stdout": f.out.String(), "stderr": f.errOut.String()} {
			if strings.Contains(s, qobuzdltest.Token) {
				t.Errorf("%s: the token is on %s", name, stream)
			}
		}
	}
	tracks := albumTracks(t, f, qobuzdltest.AlbumGoodies)

	f.run(t, "dl", "-v", "-q", "27", "--no-db", "-d", dir, albumURL(qobuzdltest.AlbumGoodies))
	if !strings.Contains(f.errOut.String(), "level=DEBUG") {
		t.Errorf("-v gave no debug lines:\n%s", f.errOut.String())
	}
	check("album with -v")

	f.srv.FailTimes(tracks[0].ID, 2, 500)
	f.srv.Fail404(tracks[1].ID)
	f.srv.Segmented(tracks[2].ID)
	f.run(t, "dl", "-v", "-q", "27", "--no-db", "-d", t.TempDir(), albumURL(qobuzdltest.AlbumGoodies))
	check("failing tracks with -v")

	f.run(t, "dl", "-v", "--no-db", "-d", dir, "https://example.com/x", "https://play.qobuz.com/album/missing")
	check("bad URLs with -v")

	f.run(t, "search", "-v", "tracks", "so what")
	check("search with -v")

	f.srv.ExpireToken()
	if code := f.run(t, "dl", "-v", "--no-db", "-d", dir, albumURL(qobuzdltest.AlbumCD)); code != exitFailed {
		t.Errorf("expired token: exit %d, want 1", code)
	}
	check("expired token with -v")
	if code := f.run(t, "search", "-v", "albums", "miles davis"); code != exitFailed {
		t.Errorf("search with an expired token: exit %d, want 1", code)
	}
	check("search with an expired token")
}

// TestParityAgainstFake runs make parity's three steps against qobuzdltest:
// parity.py, the CLI with -q 27 --no-db, then compare.py. It needs the venv
// and runs only when QOBUZDL_PARITY_FAKE is set, as make parity FAKE=1 does.
// PARITY_URL holds the URLs, separated by spaces, and PARITY_OUT the folder
// for the trees; each URL gets <n>/py and <n>/go there.
func TestParityAgainstFake(t *testing.T) {
	if os.Getenv("QOBUZDL_PARITY_FAKE") == "" {
		t.Skip("set QOBUZDL_PARITY_FAKE=1, or run make parity FAKE=1")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(root, "testdata", "gen", ".venv", "bin", "python")
	if _, err := os.Stat(python); err != nil {
		t.Fatalf("no venv, run make venv: %v", err)
	}
	urls := strings.Fields(os.Getenv("PARITY_URL"))
	if len(urls) == 0 {
		urls = []string{
			albumURL(qobuzdltest.AlbumMultiDisc),
			albumURL(qobuzdltest.AlbumCD),
			"https://play.qobuz.com/playlist/" + qobuzdltest.Playlist,
		}
	}
	out := os.Getenv("PARITY_OUT")
	if out == "" {
		out = t.TempDir()
	}

	// The fake's token replaces any real one, which must never reach it.
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "QOBUZ_TOKEN=") {
			env = append(env, kv)
		}
	}
	env = append(env, "QOBUZ_TOKEN="+qobuzdltest.Token, "NO_PROXY=127.0.0.1,localhost")

	for i, url := range urls {
		t.Run(strconv.Itoa(i+1), func(t *testing.T) {
			srv := qobuzdltest.New(t)
			dir := filepath.Join(out, strconv.Itoa(i+1))
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			pyDir, goDir := filepath.Join(dir, "py"), filepath.Join(dir, "go")

			cmd := exec.CommandContext(t.Context(), python, "-I", filepath.Join(root, "testdata", "gen", "parity.py"),
				"--fake", srv.URL, url, pyDir)
			cmd.Env = env
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("parity.py %s: %v\n%s", url, err, b)
			}

			var stdout, stderr bytes.Buffer
			c := &cli{stdout: &stdout, stderr: &stderr, getenv: envOf(map[string]string{"QOBUZ_TOKEN": qobuzdltest.Token}), configure: srv.Configure}
			if code := c.run(t.Context(), []string{"dl", "-q", "27", "--no-db", "-d", goDir, url}); code != exitOK {
				t.Errorf("qobuz-dl dl %s: exit %d\n%s%s", url, code, stdout.String(), stderr.String())
			}

			cmd = exec.CommandContext(t.Context(), python, "-I", filepath.Join(root, "testdata", "gen", "compare.py"), pyDir, goDir)
			b, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("compare.py %s: %v\n%s", url, err, b)
			} else {
				t.Logf("%s: %s", url, strings.TrimSpace(string(b)))
			}
		})
	}
}

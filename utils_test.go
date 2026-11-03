package qobuzdl

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func cleanFilenameCases(t *testing.T, legacy bool) {
	t.Helper()
	n := 0
	for _, c := range loadGolden(t, "clean_filename") {
		in := c.Input.(*pyDict)
		if in.get("legacy_charmap", nil) != legacy {
			continue
		}
		n++
		name := in.get("name", nil).(string)
		if got := CleanFilename(name, legacy); got != c.Output {
			t.Errorf("%s: CleanFilename(%q, %v)\n got %q\nwant %q", c.Name, name, legacy, got, c.Output)
		}
	}
	if n == 0 {
		t.Fatal("no cases")
	}
}

func TestCleanFilenameGolden(t *testing.T) {
	cleanFilenameCases(t, false)
}

func TestCleanFilenameLegacyGolden(t *testing.T) {
	cleanFilenameCases(t, true)
}

func TestCleanFilenameBracketSeparatorsUnicode(t *testing.T) {
	// From Python, also in clean_filename.json. Python's \b and \w are
	// Unicode: "_" is a word character, so "(_Ελληνικά)" has no boundary
	// after its separator and keeps it.
	cases := map[string]string{
		"(, Песня)":             "(Песня)",
		"(Песня ,)":             "(Песня)",
		"【, 音楽】":                "【音楽】",
		"(_Ελληνικά)":           "(_Ελληνικά)",
		"(Ελληνικά_)":           "(Ελληνικά_)",
		"(_ Ελληνικά _)":        "(Ελληνικά)",
		"Title (| Ünïcode |) x": "Title (Ünïcode) x",
		"(, ½)":                 "(½)",
	}
	for in, want := range cases {
		if got := CleanFilename(in, false); got != want {
			t.Errorf("CleanFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetURLInfoGolden(t *testing.T) {
	for _, c := range loadGolden(t, "url_info") {
		url := c.Input.(string)
		kind, id, err := GetURLInfo(url)
		if c.Error != "" {
			if c.Error != "AttributeError" {
				t.Fatalf("%s: golden error %s", c.Name, c.Error)
			}
			if !errors.Is(err, ErrInvalidURL) || !isPy(err, pyAttributeError) {
				t.Errorf("%s: GetURLInfo(%q) = %q, %q, %v; want ErrInvalidURL with an AttributeError", c.Name, url, kind, id, err)
			}
			continue
		}
		want := c.Output.([]any)
		if err != nil || kind != want[0] || id != want[1] {
			t.Errorf("%s: GetURLInfo(%q) = %q, %q, %v; want %v", c.Name, url, kind, id, err, want)
		}
	}
}

func TestGetURLInfoRejectsInterpreter(t *testing.T) {
	_, _, err := GetURLInfo("https://www.qobuz.com/gb-en/interpreter/miles-davis/50782")
	if !errors.Is(err, ErrInvalidURL) {
		t.Errorf("err = %v, want ErrInvalidURL", err)
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestGetAlbumArtistGolden(t *testing.T) {
	for _, c := range loadGolden(t, "album_artist") {
		album := c.Input.(*pyDict)
		got, err := getAlbumArtist(discardLogger(), album)
		if c.Error != "" {
			if pyErrKind(err) != c.Error {
				t.Errorf("%s: got %s, %v; want %s", c.Name, pyRepr(got), err, c.Error)
			}
			continue
		}
		if err != nil || pyRepr(got) != pyRepr(c.Output) {
			t.Errorf("%s: got %s, %v; want %s", c.Name, pyRepr(any(got)), err, pyRepr(c.Output))
		}
	}
}

func TestGetAlbumArtistLogsTheError(t *testing.T) {
	var logged []string
	log := slog.New(recordHandler{&logged})
	album := newDict()
	album.set("artists", int64(5))
	if _, err := getAlbumArtist(log, album); err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 || logged[0] != "ERROR Error getting album artist: 'int' object is not iterable" {
		t.Errorf("logged %q", logged)
	}
}

func TestSmartDiscographyGolden(t *testing.T) {
	for _, c := range loadGolden(t, "smart_discography") {
		in := c.Input.(*pyDict)
		var pages []*pyDict
		for _, p := range in.get("pages", nil).([]any) {
			pages = append(pages, p.(*pyDict))
		}
		saveSpace := in.get("save_space", nil).(bool)
		skipExtras := in.get("skip_extras", nil).(bool)
		got, err := SmartDiscographyFilter(pages, saveSpace, skipExtras)
		if c.Error != "" {
			if pyErrKind(err) != c.Error {
				t.Errorf("%s: got %d items, %v; want %s", c.Name, len(got), err, c.Error)
			}
			continue
		}
		ids := []any{}
		for _, a := range got {
			ids = append(ids, a.get("id", nil))
		}
		if err != nil || pyRepr(ids) != pyRepr(c.Output) {
			t.Errorf("%s: got %s, %v; want %s", c.Name, pyRepr(ids), err, pyRepr(c.Output))
		}
	}
}

func TestFormatDuration(t *testing.T) {
	// From Python's format_duration.
	cases := map[int]string{
		0: "00:00:00", 59: "00:00:59", 61: "00:01:01", 3599: "00:59:59", 3600: "01:00:00",
		86399: "23:59:59", 86400: "00:00:00", 90061: "01:01:01", -1: "23:59:59", -86401: "23:59:59",
		360000: "04:00:00",
	}
	for in, want := range cases {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateAndReturnDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home+"/")
	got, err := createAndReturnDir("~/music/./a/../Qobuz")
	if err != nil {
		t.Fatal(err)
	}
	if want := home + "/music/Qobuz"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// os.Mkdir with 0755 shows what the umask leaves of it.
	probe := filepath.Join(home, "probe")
	if err := os.Mkdir(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	want, _ := os.Stat(probe)
	for _, d := range []string{home + "/music", got} {
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != want.Mode().Perm() {
			t.Errorf("stat %q: %v, %v; want %v", d, fi.Mode(), err, want.Mode())
		}
	}
	// Existing folders are fine.
	if _, err := createAndReturnDir(got); err != nil {
		t.Error(err)
	}

	t.Chdir(home)
	got, err = createAndReturnDir("rel")
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(filepath.Join(home, "rel")); mustEval(t, got) != want {
		t.Errorf("relative: got %q, want %q", got, want)
	}

	if got := expandUser("~no-such-user-qobuzdl/x"); got != "~no-such-user-qobuzdl/x" {
		t.Errorf("unknown user: %q", got)
	}
	if got := expandUser("a/~"); got != "a/~" {
		t.Errorf("no tilde prefix: %q", got)
	}
	t.Setenv("HOME", "/")
	if got := expandUser("~"); got != "/" {
		t.Errorf("HOME=/: %q", got)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// pyErrKind is the Python exception class name of err, through wrapping.
func pyErrKind(err error) string {
	var pe *pyError
	if errors.As(err, &pe) {
		return pe.kind.String()
	}
	if err == nil {
		return "no error"
	}
	return "not a Python error"
}

// recordHandler keeps "LEVEL message" for each record.
type recordHandler struct{ lines *[]string }

func (h recordHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordHandler) Handle(_ context.Context, r slog.Record) error {
	*h.lines = append(*h.lines, r.Level.String()+" "+r.Message)
	return nil
}
func (h recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordHandler) WithGroup(string) slog.Handler      { return h }

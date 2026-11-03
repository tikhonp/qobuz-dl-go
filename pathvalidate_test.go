package qobuzdl

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func pathvalidateCases(t *testing.T, fn string) int {
	t.Helper()
	n := 0
	for _, c := range loadGolden(t, "pathvalidate") {
		in := c.Input.(*pyDict)
		if in.get("func", nil) != fn {
			continue
		}
		n++
		name := in.get("name", nil).(string)
		repl := in.get("replacement", nil).(string)
		var got string
		if fn == "sanitize_filename" {
			got = sanitizeFilename(name, repl)
		} else {
			got = sanitizeFilepath(name, repl)
		}
		if got != c.Output {
			t.Errorf("%s: %s(%q, %q)\n got %q\nwant %q", c.Name, fn, name, repl, got, c.Output)
		}
	}
	return n
}

func TestSanitizeFilenameGolden(t *testing.T) {
	if pathvalidateCases(t, "sanitize_filename") == 0 {
		t.Fatal("no cases")
	}
}

func TestSanitizeFilepathGolden(t *testing.T) {
	if pathvalidateCases(t, "sanitize_filepath") == 0 {
		t.Fatal("no cases")
	}
}

func TestSanitizeReservedNames(t *testing.T) {
	// From pathvalidate 3.3.1; reserved.txt in the golden covers more.
	cases := map[string]string{
		"CON":         "CON_",
		"con":         "con_",
		"cOm1":        "cOm1_",
		"LPT\u00b2":   "LPT\u00b2_",
		"CLOCK$":      "CLOCK$_",
		"nul.txt":     "nul_.txt",
		"con.con.txt": "con_.con_.txt",
		"CON. ":       "CON_. ",
		" CON":        "CON",
		"CONSOLE":     "CONSOLE",
		"xCON":        "xCON",
		".":           ".",
		"..":          "..",
	}
	for in, want := range cases {
		for _, repl := range []string{"", "_"} {
			if got := sanitizeFilename(in, repl); got != want {
				t.Errorf("sanitizeFilename(%q, %q) = %q, want %q", in, repl, got, want)
			}
		}
	}
	if got := sanitizeFilepath("a/$Mft/AUX/b", "_"); got != "a/$Mft_/AUX_/b" {
		t.Errorf("sanitizeFilepath = %q", got)
	}
}

func TestSanitizeCutsAtByteLimit(t *testing.T) {
	for _, unit := range []string{"a", "é", "音", "🎵"} {
		for pad := range 4 {
			in := strings.Repeat("x", pad) + strings.Repeat(unit, 300)
			got := sanitizeFilename(in, "_")
			if len(got) > 255 || !utf8.ValidString(got) {
				t.Errorf("%q x %d: %d bytes, valid %v", unit, pad, len(got), utf8.ValidString(got))
			}
			if len(got) <= 255-utf8.RuneLen([]rune(unit)[0]) {
				t.Errorf("%q x %d: cut too short, %d bytes", unit, pad, len(got))
			}
			// Each path entry gets 260 bytes.
			entry := sanitizeFilepath("d/"+in, "_")[2:]
			if len(entry) > 260 || len(entry) <= 260-utf8.RuneLen([]rune(unit)[0]) || !utf8.ValidString(entry) {
				t.Errorf("%q x %d: path entry of %d bytes", unit, pad, len(entry))
			}
		}
	}
}

func TestPosixNormpath(t *testing.T) {
	// From CPython 3.14's posixpath.normpath.
	cases := map[string]string{
		"":           ".",
		"/":          "/",
		"//":         "//",
		"///":        "/",
		"//a/b":      "//a/b",
		"///a//b/":   "/a/b",
		"a/./b/..":   "a",
		"../a/../..": "../..",
		"/../a":      "/a",
		"a/..":       ".",
		`a\..\b`:     `a\..\b`,
	}
	for in, want := range cases {
		if got := posixNormpath(in); got != want {
			t.Errorf("posixNormpath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNTSplitDrive(t *testing.T) {
	// From CPython 3.14's ntpath.splitdrive.
	cases := []struct{ in, drive, rest string }{
		{`C:\x`, "C:", `\x`},
		{"C:x", "C:", "x"},
		{"é:x", "é:", "x"},
		{"::", "::", ""},
		{`\\server\share\x`, `\\server\share`, `\x`},
		{"//server/share/x", "//server/share", "/x"},
		{`\\server`, `\\server`, ""},
		{`\\?\UNC\server\share\x`, `\\?\UNC\server\share`, `\x`},
		{`\\?\unc\server\share\x`, `\\?\unc\server\share`, `\x`},
		{`\x`, "", `\x`},
		{"x", "", "x"},
		{"", "", ""},
	}
	for _, c := range cases {
		if d, r := ntSplitDrive(c.in); d != c.drive || r != c.rest {
			t.Errorf("ntSplitDrive(%q) = %q, %q, want %q, %q", c.in, d, r, c.drive, c.rest)
		}
	}
}

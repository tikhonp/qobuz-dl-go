package qobuzdl

import (
	"fmt"
	"testing"
	"unicode"
)

func TestStringMethodsGolden(t *testing.T) {
	for _, c := range loadGolden(t, "strings") {
		s := c.Input.(string)
		want := c.Output.(*pyDict)
		got := map[string]any{
			"lower":        pyLower(s),
			"upper":        pyUpper(s),
			"casefold":     pyCasefold(s),
			"title":        pyTitle(s),
			"capitalize":   pyCapitalize(s),
			"strip":        pyStrip(s, ""),
			"strip_chars":  pyStrip(s, ` ."-_'`),
			"lstrip_chars": pyLstrip(s, ` ."-_'`),
			"rstrip_chars": pyRstrip(s, ` ."-_'`),
			"isdigit":      pyIsDigit(s),
			"len":          int64(pyLen(s)),
			"nfc":          pyNFC(s),
		}
		for _, k := range want.keysInOrder() {
			g, ok := got[k]
			if !ok {
				t.Fatalf("%s: no Go method for %s", c.Name, k)
			}
			if w := want.get(k, nil); g != w {
				t.Errorf("%s %q: %s = %s, want %s", c.Name, s, k, pyRepr(g), pyRepr(w))
			}
		}
	}
}

func TestCaseMappingSpecialCases(t *testing.T) {
	// From Python 3.14.
	cases := []struct {
		in, lower, upper, casefold, title string
	}{
		{"İstanbul", "i\u0307stanbul", "İSTANBUL", "i\u0307stanbul", "İstanbul"},
		{"straße", "straße", "STRASSE", "strasse", "Straße"},
		{"ǅemal ǆ", "ǆemal ǆ", "ǄEMAL Ǆ", "ǆemal ǆ", "ǅemal ǅ"},
		{"ﬁne ﬂow", "ﬁne ﬂow", "FINE FLOW", "fine flow", "Fine Flow"},
		{"ΣΊΣΥΦΟΣ", "σίσυφος", "ΣΊΣΥΦΟΣ", "σίσυφοσ", "Σίσυφος"},
		{"they're o'neil 3rd", "they're o'neil 3rd", "THEY'RE O'NEIL 3RD", "they're o'neil 3rd", "They'Re O'Neil 3Rd"},
	}
	for _, c := range cases {
		for _, m := range []struct {
			name string
			fn   func(string) string
			want string
		}{
			{"lower", pyLower, c.lower},
			{"upper", pyUpper, c.upper},
			{"casefold", pyCasefold, c.casefold},
			{"title", pyTitle, c.title},
		} {
			if got := m.fn(c.in); got != m.want {
				t.Errorf("%q.%s() = %q, want %q", c.in, m.name, got, m.want)
			}
		}
	}
	if got := pyCapitalize("ǆEMAL"); got != "ǅemal" {
		t.Errorf("capitalize = %q", got)
	}
}

func TestPyIsSpace(t *testing.T) {
	space := "\t\n\v\f\r\x1c\x1d\x1e\x1f \u0085\u00a0\u1680\u2000\u2005\u200a\u2028\u2029\u202f\u205f\u3000"
	for _, r := range space {
		if !pyIsSpace(r) {
			t.Errorf("U+%04X is not space", r)
		}
	}
	for _, r := range "\u200b\u180e\ufeffa_\x00\u2060" {
		if pyIsSpace(r) {
			t.Errorf("U+%04X is space", r)
		}
	}
	isspace := goldenByName(t, "unicode")["isspace"]
	want := goldenRanges(t, isspace.Output)
	eachRune(func(r rune) {
		if pyIsSpace(r) != inRanges(want, r) {
			t.Errorf("U+%04X: pyIsSpace %v", r, pyIsSpace(r))
		}
	})
}

// TestUnicodePropertiesGolden checks every code point against Python's
// tables, so the Unicode version gap between Go and Python shows here.
func TestUnicodePropertiesGolden(t *testing.T) {
	t.Parallel()
	g := goldenByName(t, "unicode")
	if v := g["unidata_version"].Output; v != "16.0.0" {
		t.Errorf("Python's Unicode is %v; check newerThanPython", v)
	}
	sets := []struct {
		name string
		fn   func(rune) bool
	}{
		{"assigned", func(r rune) bool {
			return inPython(r) && unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z,
				unicode.Cc, unicode.Cf, unicode.Co)
		}},
		{"isdigit", pyIsDigitRune},
		{"isdecimal", pyIsDecimal},
		{"isprintable", pyIsPrintable},
		{"cased", pyIsCased},
		{"case_ignorable", pyIsCaseIgnorable},
	}
	for _, s := range sets {
		want := goldenRanges(t, g[s.name].Output)
		bad := 0
		eachRune(func(r rune) {
			if s.fn(r) != inRanges(want, r) {
				if bad < 10 {
					t.Errorf("%s: U+%04X is %v in Go", s.name, r, s.fn(r))
				}
				bad++
			}
		})
		if bad > 0 {
			t.Errorf("%s: %d code points differ", s.name, bad)
		}
	}
	for _, d := range goldenRanges(t, g["isdecimal"].Output) {
		if d.lo == 0x30 {
			continue
		}
		for r := d.lo; r <= d.hi; r++ {
			if v, ok := pyDecimalValue(r); !ok || v != int(r-d.lo)%10 {
				t.Errorf("U+%04X: decimal value %d, %v", r, v, ok)
			}
		}
	}
}

func TestCaseMapsGolden(t *testing.T) {
	t.Parallel()
	g := goldenByName(t, "unicode")
	maps := []struct {
		name string
		fn   func(string) string
	}{
		{"lower", pyLower},
		{"upper", pyUpper},
		{"title", pyTitle},
		{"casefold", pyCasefold},
	}
	// Unassigned code points have no mappings in either language.
	assigned := goldenRanges(t, g["assigned"].Output)
	for _, m := range maps {
		want := g[m.name+" map"].Output.(*pyDict)
		bad := 0
		eachRune(func(r rune) {
			if !inRanges(assigned, r) {
				return
			}
			s := string(r)
			w := want.get(fmt.Sprintf("%04X", r), s)
			if got := m.fn(s); got != w {
				if bad < 10 {
					t.Errorf("%s(U+%04X) = %q, want %q", m.name, r, got, w)
				}
				bad++
			}
		})
		if bad > 0 {
			t.Errorf("%s: %d code points differ", m.name, bad)
		}
	}
}

func TestPySlice(t *testing.T) {
	// From Python: "añb🎵c"[a:b].
	s := "añb🎵c"
	cases := []struct {
		start, end int
		want       string
	}{
		{0, 5, s}, {1, 3, "ñb"}, {-2, 5, "🎵c"}, {0, -1, "añb🎵"}, {-10, 2, "añ"},
		{3, 100, "🎵c"}, {4, 2, ""}, {5, 6, ""}, {-1, -2, ""},
	}
	for _, c := range cases {
		if got := pySlice(s, c.start, c.end); got != c.want {
			t.Errorf("s[%d:%d] = %q, want %q", c.start, c.end, got, c.want)
		}
	}
}

func TestPyLjustPadsByCodePoint(t *testing.T) {
	if got := pyLjust("ñ🎵", 4); got != "ñ🎵  " {
		t.Errorf("got %q", got)
	}
	if got := pyLjust("abcdef", 4); got != "abcdef" {
		t.Errorf("got %q", got)
	}
}

func TestPyNFCLeavesCharactersPythonDoesNotKnow(t *testing.T) {
	// From Python 3.14, Unicode 16.0. U+1ADD is a combining mark in 17.0;
	// Python sees an unassigned starter, so U+0301 can't reach the e.
	cases := map[string]string{
		"e᫝́":  "e᫝́",
		"é᫝́": "é᫝́",
		"é":   "é",
		"᫝":    "᫝",
	}
	for in, want := range cases {
		if got := pyNFC(in); got != want {
			t.Errorf("pyNFC(%+q) = %+q, want %+q", in, got, want)
		}
	}
}

package qobuzdl

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// classRanges reads back the ranges of a class string from classChars.
func classRanges(t *testing.T, class string) []runeRange {
	t.Helper()
	var out []runeRange
	for _, part := range strings.SplitAfter(class, "}") {
		if part == "" {
			continue
		}
		var lo, hi rune
		if _, err := fmt.Sscanf(part, `\x{%x}-\x{%x}`, &lo, &hi); err == nil {
			out = append(out, runeRange{lo, hi})
			continue
		}
		if _, err := fmt.Sscanf(part, `\x{%x}`, &lo); err != nil {
			// A range's second half is its own part.
			if _, err := fmt.Sscanf(part, `-\x{%x}`, &hi); err != nil {
				t.Fatalf("part %q", part)
			}
			out[len(out)-1].hi = hi
			continue
		}
		out = append(out, runeRange{lo, lo})
	}
	return out
}

func TestRegexClassesGolden(t *testing.T) {
	g := goldenByName(t, "regex")
	classes := []struct {
		name  string
		chars string
		fn    func(rune) bool
	}{
		{`\w`, reWordChars, pyIsWord},
		{`\s`, reSpaceChars, pyIsSpace},
		{`\d`, reDigitChars, pyIsDecimal},
	}
	for _, c := range classes {
		want := goldenRanges(t, g[c.name].Output)
		got := classRanges(t, c.chars)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: class has %d ranges, Python %d", c.name, len(got), len(want))
		}
		bad := 0
		eachRune(func(r rune) {
			if c.fn(r) != inRanges(want, r) {
				bad++
			}
		})
		if bad > 0 {
			t.Errorf("%s: predicate differs from Python on %d code points", c.name, bad)
		}
	}
}

func TestRegexClassesCompileAndMatch(t *testing.T) {
	cases := []struct {
		class, in string
		want      bool
	}{
		{reW, "é", true}, {reW, "_", true}, {reW, "٣", true}, {reW, "²", true}, {reW, "-", false},
		{reW, "\u0301", false}, {reW, "\U00010940", false},
		{reNotW, "-", true}, {reNotW, "Я", false},
		{reS, "\u3000", true}, {reS, "\x1c", true}, {reS, "\u200b", false},
		{reNotS, "a", true}, {reNotS, " ", false},
		{reD, "٣", true}, {reD, "²", false}, {reD, "\U00011DE0", false},
		{reNotD, "x", true}, {reNotD, "7", false},
	}
	for _, c := range cases {
		re := regexp.MustCompile(`^` + c.class + `$`)
		if got := re.MatchString(c.in); got != c.want {
			t.Errorf("%q against %.20s...: %v", c.in, c.class, got)
		}
	}
}

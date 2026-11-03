// Python str methods that qobuz-dl 2.5.9 calls, by code point and with
// Python 3.14's Unicode 16.0 data. It ports no Python file; the rules come
// from CPython 3.14's Objects/unicodeobject.c and Objects/unicodectype.c.

package qobuzdl

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
)

// newerThanPython holds the code points that Go's Unicode 17.0 tables assign
// and Python 3.14's Unicode 16.0 doesn't. Python treats them as unassigned:
// not letters, digits, cased or printable, with no case mappings. Taking
// them out of Go's tables gives Python's sets; unicode.json checks that.
var newerThanPython = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x088f, Hi: 0x088f, Stride: 1},
		{Lo: 0x0c5c, Hi: 0x0c5c, Stride: 1},
		{Lo: 0x0cdc, Hi: 0x0cdc, Stride: 1},
		{Lo: 0x1acf, Hi: 0x1add, Stride: 1},
		{Lo: 0x1ae0, Hi: 0x1aeb, Stride: 1},
		{Lo: 0x20c1, Hi: 0x20c1, Stride: 1},
		{Lo: 0x2b96, Hi: 0x2b96, Stride: 1},
		{Lo: 0xa7ce, Hi: 0xa7cf, Stride: 1},
		{Lo: 0xa7d2, Hi: 0xa7d2, Stride: 1},
		{Lo: 0xa7d4, Hi: 0xa7d4, Stride: 1},
		{Lo: 0xa7f1, Hi: 0xa7f1, Stride: 1},
		{Lo: 0xfbc3, Hi: 0xfbd2, Stride: 1},
		{Lo: 0xfd90, Hi: 0xfd91, Stride: 1},
		{Lo: 0xfdc8, Hi: 0xfdce, Stride: 1},
	},
	R32: []unicode.Range32{
		{Lo: 0x10940, Hi: 0x10959, Stride: 1},
		{Lo: 0x10ec5, Hi: 0x10ec7, Stride: 1},
		{Lo: 0x10ed0, Hi: 0x10ed8, Stride: 1},
		{Lo: 0x10efa, Hi: 0x10efb, Stride: 1},
		{Lo: 0x11b60, Hi: 0x11b67, Stride: 1},
		{Lo: 0x11db0, Hi: 0x11ddb, Stride: 1},
		{Lo: 0x11de0, Hi: 0x11de9, Stride: 1},
		{Lo: 0x16ea0, Hi: 0x16eb8, Stride: 1},
		{Lo: 0x16ebb, Hi: 0x16ed3, Stride: 1},
		{Lo: 0x16ff2, Hi: 0x16ff6, Stride: 1},
		{Lo: 0x187f8, Hi: 0x187ff, Stride: 1},
		{Lo: 0x18d09, Hi: 0x18d1e, Stride: 1},
		{Lo: 0x18d80, Hi: 0x18df2, Stride: 1},
		{Lo: 0x1ccfa, Hi: 0x1ccfc, Stride: 1},
		{Lo: 0x1ceba, Hi: 0x1ced0, Stride: 1},
		{Lo: 0x1cee0, Hi: 0x1cef0, Stride: 1},
		{Lo: 0x1e6c0, Hi: 0x1e6de, Stride: 1},
		{Lo: 0x1e6e0, Hi: 0x1e6f5, Stride: 1},
		{Lo: 0x1e6fe, Hi: 0x1e6ff, Stride: 1},
		{Lo: 0x1f6d8, Hi: 0x1f6d8, Stride: 1},
		{Lo: 0x1f777, Hi: 0x1f77a, Stride: 1},
		{Lo: 0x1f8d0, Hi: 0x1f8d8, Stride: 1},
		{Lo: 0x1fa54, Hi: 0x1fa57, Stride: 1},
		{Lo: 0x1fa8a, Hi: 0x1fa8a, Stride: 1},
		{Lo: 0x1fa8e, Hi: 0x1fa8e, Stride: 1},
		{Lo: 0x1fac8, Hi: 0x1fac8, Stride: 1},
		{Lo: 0x1facd, Hi: 0x1facd, Stride: 1},
		{Lo: 0x1faea, Hi: 0x1faea, Stride: 1},
		{Lo: 0x1faef, Hi: 0x1faef, Stride: 1},
		{Lo: 0x1fbfa, Hi: 0x1fbfa, Stride: 1},
		{Lo: 0x2b73a, Hi: 0x2b73f, Stride: 1},
		{Lo: 0x2cea2, Hi: 0x2cead, Stride: 1},
		{Lo: 0x323b0, Hi: 0x33479, Stride: 1},
	},
}

// digitNotDecimal holds the characters with Numeric_Type=Digit, which
// str.isdigit accepts on top of the decimal digits: superscripts, circled
// digits and the like. Go has no Numeric_Type table.
var digitNotDecimal = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x00b2, Hi: 0x00b3, Stride: 1},
		{Lo: 0x00b9, Hi: 0x00b9, Stride: 1},
		{Lo: 0x1369, Hi: 0x1371, Stride: 1},
		{Lo: 0x19da, Hi: 0x19da, Stride: 1},
		{Lo: 0x2070, Hi: 0x2070, Stride: 1},
		{Lo: 0x2074, Hi: 0x2079, Stride: 1},
		{Lo: 0x2080, Hi: 0x2089, Stride: 1},
		{Lo: 0x2460, Hi: 0x2468, Stride: 1},
		{Lo: 0x2474, Hi: 0x247c, Stride: 1},
		{Lo: 0x2488, Hi: 0x2490, Stride: 1},
		{Lo: 0x24ea, Hi: 0x24ea, Stride: 1},
		{Lo: 0x24f5, Hi: 0x24fd, Stride: 1},
		{Lo: 0x24ff, Hi: 0x24ff, Stride: 1},
		{Lo: 0x2776, Hi: 0x277e, Stride: 1},
		{Lo: 0x2780, Hi: 0x2788, Stride: 1},
		{Lo: 0x278a, Hi: 0x2792, Stride: 1},
	},
	R32: []unicode.Range32{
		{Lo: 0x10a40, Hi: 0x10a43, Stride: 1},
		{Lo: 0x10e60, Hi: 0x10e68, Stride: 1},
		{Lo: 0x11052, Hi: 0x1105a, Stride: 1},
		{Lo: 0x1f100, Hi: 0x1f10a, Stride: 1},
	},
	LatinOffset: 2,
}

// caseIgnorableWordBreak holds the Case_Ignorable characters that come from
// Word_Break MidLetter, MidNumLet and Single_Quote rather than from a
// general category.
var caseIgnorableWordBreak = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x0027, Hi: 0x0027, Stride: 1},
		{Lo: 0x002e, Hi: 0x002e, Stride: 1},
		{Lo: 0x003a, Hi: 0x003a, Stride: 1},
		{Lo: 0x00b7, Hi: 0x00b7, Stride: 1},
		{Lo: 0x0387, Hi: 0x0387, Stride: 1},
		{Lo: 0x055f, Hi: 0x055f, Stride: 1},
		{Lo: 0x05f4, Hi: 0x05f4, Stride: 1},
		{Lo: 0x2018, Hi: 0x2019, Stride: 1},
		{Lo: 0x2024, Hi: 0x2024, Stride: 1},
		{Lo: 0x2027, Hi: 0x2027, Stride: 1},
		{Lo: 0xfe13, Hi: 0xfe13, Stride: 1},
		{Lo: 0xfe52, Hi: 0xfe52, Stride: 1},
		{Lo: 0xfe55, Hi: 0xfe55, Stride: 1},
		{Lo: 0xff07, Hi: 0xff07, Stride: 1},
		{Lo: 0xff0e, Hi: 0xff0e, Stride: 1},
		{Lo: 0xff1a, Hi: 0xff1a, Stride: 1},
	},
	LatinOffset: 4,
}

func inPython(r rune) bool {
	return !unicode.Is(newerThanPython, r)
}

// pyIsSpace is Py_UNICODE_ISSPACE: bidirectional class WS, B or S, or
// category Zs. It is str.isspace, str.split()'s separator, strip()'s default
// and re's \s.
func pyIsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, ' ', 0x85, 0xa0,
		0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// pyIsDecimal is str.isdecimal for one character, and re's \d.
func pyIsDecimal(r rune) bool {
	return unicode.Is(unicode.Nd, r) && inPython(r)
}

// pyDecimalValue is Py_UNICODE_TODECIMAL. Decimal digits come in runs of ten
// from zero, so the value is the offset in the run.
func pyDecimalValue(r rune) (int, bool) {
	if r >= '0' && r <= '9' {
		return int(r - '0'), true
	}
	if !pyIsDecimal(r) {
		return 0, false
	}
	for _, rg := range unicode.Nd.R16 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10, true
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10, true
		}
	}
	return 0, false
}

// pyIsDigitRune is str.isdigit for one character.
func pyIsDigitRune(r rune) bool {
	return pyIsDecimal(r) || unicode.Is(digitNotDecimal, r)
}

// pyIsWord is re's \w: str.isalnum() or '_'. isalnum is a letter or a
// character with a numeric value, which is the L and N categories.
func pyIsWord(r rune) bool {
	return r == '_' || (unicode.In(r, unicode.L, unicode.N) && inPython(r))
}

// pyIsPrintable is str.isprintable for one character: anything but the
// categories Cc, Cf, Cs, Co, Cn, Zl, Zp and Zs, with the space allowed.
func pyIsPrintable(r rune) bool {
	return r == ' ' || (unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S) && inPython(r))
}

// pyIsCased is the Cased property: Lowercase, Uppercase or Lt. U+0295 ʕ is
// Ll in Unicode 16 and Lo in 17.
func pyIsCased(r rune) bool {
	return r == 0x295 ||
		unicode.In(r, unicode.Lu, unicode.Ll, unicode.Lt, unicode.Other_Lowercase, unicode.Other_Uppercase) && inPython(r)
}

// pyIsCaseIgnorable is the Case_Ignorable property.
func pyIsCaseIgnorable(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk, caseIgnorableWordBreak) && inPython(r)
}

// pyLen is len(s).
func pyLen(s string) int {
	return utf8.RuneCountInString(s)
}

// pySlice is s[start:end] with Python's rules: negative indices count from
// the end and indices out of range are clamped.
func pySlice(s string, start, end int) string {
	rs := []rune(s)
	n := len(rs)
	clamp := func(i int) int {
		if i < 0 {
			i += n
			if i < 0 {
				return 0
			}
		}
		return min(i, n)
	}
	start, end = clamp(start), clamp(end)
	if start >= end {
		return ""
	}
	return string(rs[start:end])
}

// mapper applies x/text's full case mappings one character at a time, as
// CPython does. A Caser keeps state between calls, so each mapper is used by
// one goroutine. Casers are made on first use; ASCII needs none.
type mapper struct {
	lowerC, upperC, titleC, foldC *cases.Caser
}

func lazyCaser(p **cases.Caser, mk func() cases.Caser) cases.Caser {
	if *p == nil {
		c := mk()
		*p = &c
	}
	return **p
}

func (m *mapper) lower() cases.Caser {
	return lazyCaser(&m.lowerC, func() cases.Caser { return cases.Lower(language.Und) })
}

func (m *mapper) upper() cases.Caser {
	return lazyCaser(&m.upperC, func() cases.Caser { return cases.Upper(language.Und) })
}

func (m *mapper) title() cases.Caser {
	return lazyCaser(&m.titleC, func() cases.Caser { return cases.Title(language.Und, cases.NoLower) })
}

func (m *mapper) fold() cases.Caser {
	return lazyCaser(&m.foldC, func() cases.Caser { return cases.Fold() })
}

// mapRune maps one character. A mapping to a character Python doesn't know
// is one Unicode 16 doesn't have, so the character stays.
func mapRune(c cases.Caser, r rune) string {
	if !inPython(r) {
		return string(r)
	}
	out := c.String(string(r))
	if strings.IndexFunc(out, func(m rune) bool { return !inPython(m) }) >= 0 {
		return string(r)
	}
	return out
}

func (m *mapper) lowerAt(rs []rune, i int) string {
	r := rs[i]
	if r < utf8.RuneSelf {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		return string(r)
	}
	if r == 0x3a3 {
		return string(finalSigma(rs, i))
	}
	return mapRune(m.lower(), r)
}

func (m *mapper) titleOf(r rune) string {
	if r < utf8.RuneSelf {
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		return string(r)
	}
	return mapRune(m.title(), r)
}

// finalSigma is handle_capital_sigma: Σ lowers to ς when a cased character
// comes before it and none after, skipping case-ignorable ones both ways.
func finalSigma(rs []rune, i int) rune {
	j := i - 1
	for j >= 0 && pyIsCaseIgnorable(rs[j]) {
		j--
	}
	final := j >= 0 && pyIsCased(rs[j])
	if final {
		j = i + 1
		for j < len(rs) && pyIsCaseIgnorable(rs[j]) {
			j++
		}
		final = j == len(rs) || !pyIsCased(rs[j])
	}
	if final {
		return 0x3c2
	}
	return 0x3c3
}

// pyLower is s.lower().
func pyLower(s string) string {
	var m mapper
	rs := []rune(s)
	var b strings.Builder
	for i := range rs {
		b.WriteString(m.lowerAt(rs, i))
	}
	return b.String()
}

// pyUpper is s.upper().
func pyUpper(s string) string {
	var m mapper
	var b strings.Builder
	for _, r := range s {
		if r < utf8.RuneSelf {
			if r >= 'a' && r <= 'z' {
				r -= 'a' - 'A'
			}
			b.WriteRune(r)
			continue
		}
		b.WriteString(mapRune(m.upper(), r))
	}
	return b.String()
}

// pyCasefold is s.casefold().
func pyCasefold(s string) string {
	var m mapper
	var b strings.Builder
	for _, r := range s {
		if r < utf8.RuneSelf {
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			b.WriteRune(r)
			continue
		}
		if r >= 0x13a0 && r <= 0x13f5 {
			// Cherokee folds to its capitals, which x/text lowers.
			b.WriteRune(r)
			continue
		}
		b.WriteString(mapRune(m.fold(), r))
	}
	return b.String()
}

// pyTitle is s.title(): a character after a cased one lowers, any other
// takes its title case. Words are not split by spaces, so "they're" becomes
// "They'Re".
func pyTitle(s string) string {
	var m mapper
	rs := []rune(s)
	var b strings.Builder
	prevCased := false
	for i, r := range rs {
		if prevCased {
			b.WriteString(m.lowerAt(rs, i))
		} else {
			b.WriteString(m.titleOf(r))
		}
		prevCased = pyIsCased(r)
	}
	return b.String()
}

// pyCapitalize is s.capitalize(): the first character in title case, the
// rest lowered.
func pyCapitalize(s string) string {
	var m mapper
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		if i == 0 {
			b.WriteString(m.titleOf(r))
		} else {
			b.WriteString(m.lowerAt(rs, i))
		}
	}
	return b.String()
}

// stripSet reports whether r is stripped: chars "" means Python whitespace,
// as strip() with no argument.
func stripSet(chars string) func(rune) bool {
	if chars == "" {
		return pyIsSpace
	}
	return func(r rune) bool { return strings.ContainsRune(chars, r) }
}

// pyStrip is s.strip(chars), or s.strip() when chars is "".
func pyStrip(s, chars string) string {
	in := stripSet(chars)
	return strings.TrimRightFunc(strings.TrimLeftFunc(s, in), in)
}

// pyLstrip is s.lstrip(chars), or s.lstrip() when chars is "".
func pyLstrip(s, chars string) string {
	return strings.TrimLeftFunc(s, stripSet(chars))
}

// pyRstrip is s.rstrip(chars), or s.rstrip() when chars is "".
func pyRstrip(s, chars string) string {
	return strings.TrimRightFunc(s, stripSet(chars))
}

// pyIsDigit is s.isdigit(): not empty, and every character a digit.
func pyIsDigit(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !pyIsDigitRune(r) {
			return false
		}
	}
	return true
}

// pyLjust is s.ljust(width), which f"{s:<width}" also gives: spaces on the
// right up to width code points.
func pyLjust(s string, width int) string {
	n := pyLen(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

// pyNFC is unicodedata.normalize("NFC", s). x/text's tables are Unicode 17.0
// and give some characters new in 17.0 a combining class, so norm.NFC would
// reorder and compose across them. Python sees them as unassigned starters
// that nothing composes with, which is what normalizing the text between
// them, piece by piece, gives.
func pyNFC(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return !inPython(r) }) < 0 {
		return norm.NFC.String(s)
	}
	var b strings.Builder
	start := 0
	for i, r := range s {
		if inPython(r) {
			continue
		}
		b.WriteString(norm.NFC.String(s[start:i]))
		b.WriteRune(r)
		start = i + utf8.RuneLen(r)
	}
	b.WriteString(norm.NFC.String(s[start:]))
	return b.String()
}

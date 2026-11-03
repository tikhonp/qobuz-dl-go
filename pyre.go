// Python re's Unicode character classes for RE2 patterns. It ports no Python
// file; it stands in for CPython 3.14's sre with Unicode 16.0 data.
//
// RE2's \w, \s, \d and \b are ASCII, Python's are Unicode. Every pattern in
// the port spells its classes with the strings below and names its Python
// source in a comment.

package qobuzdl

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// The insides of Python's \w, \s and \d, for use within [...]. reW, reS and
// reD are the classes themselves, reNotW, reNotS and reNotD their negations
// \W, \S and \D.
var (
	reWordChars  = classChars([]*unicode.RangeTable{unicode.L, unicode.N}, '_')
	reSpaceChars = classChars(nil, '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, ' ', 0x85, 0xa0,
		0x1680, 0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006, 0x2007, 0x2008, 0x2009, 0x200a,
		0x2028, 0x2029, 0x202f, 0x205f, 0x3000)
	reDigitChars = classChars([]*unicode.RangeTable{unicode.Nd})

	reW    = "[" + reWordChars + "]"
	reNotW = "[^" + reWordChars + "]"
	reS    = "[" + reSpaceChars + "]"
	reNotS = "[^" + reSpaceChars + "]"
	reD    = "[" + reDigitChars + "]"
	reNotD = "[^" + reDigitChars + "]"
)

type runeRange struct{ lo, hi rune }

// classChars lists the code points of the tables plus extra, without those
// Python doesn't know, as RE2 class ranges such as `\x{30}-\x{39}`.
func classChars(tables []*unicode.RangeTable, extra ...rune) string {
	var rs []runeRange
	add := func(lo, hi, stride rune) {
		if stride == 1 {
			rs = append(rs, runeRange{lo, hi})
			return
		}
		for r := lo; r <= hi; r += stride {
			rs = append(rs, runeRange{r, r})
		}
	}
	for _, t := range tables {
		for _, r := range t.R16 {
			add(rune(r.Lo), rune(r.Hi), rune(r.Stride))
		}
		for _, r := range t.R32 {
			add(rune(r.Lo), rune(r.Hi), rune(r.Stride))
		}
	}
	for _, r := range extra {
		rs = append(rs, runeRange{r, r})
	}
	var b strings.Builder
	for _, r := range subtractNewer(mergeRanges(rs)) {
		if r.lo == r.hi {
			fmt.Fprintf(&b, `\x{%x}`, r.lo)
		} else {
			fmt.Fprintf(&b, `\x{%x}-\x{%x}`, r.lo, r.hi)
		}
	}
	return b.String()
}

func mergeRanges(rs []runeRange) []runeRange {
	slices.SortFunc(rs, func(a, b runeRange) int { return int(a.lo - b.lo) })
	var out []runeRange
	for _, r := range rs {
		if n := len(out); n > 0 && r.lo <= out[n-1].hi+1 {
			out[n-1].hi = max(out[n-1].hi, r.hi)
			continue
		}
		out = append(out, r)
	}
	return out
}

// subtractNewer removes newerThanPython from sorted, merged ranges.
func subtractNewer(rs []runeRange) []runeRange {
	var cut []runeRange
	for _, r := range newerThanPython.R16 {
		cut = append(cut, runeRange{rune(r.Lo), rune(r.Hi)})
	}
	for _, r := range newerThanPython.R32 {
		cut = append(cut, runeRange{rune(r.Lo), rune(r.Hi)})
	}
	out := rs
	for _, c := range cut {
		var next []runeRange
		for _, r := range out {
			if r.hi < c.lo || r.lo > c.hi {
				next = append(next, r)
				continue
			}
			if r.lo < c.lo {
				next = append(next, runeRange{r.lo, c.lo - 1})
			}
			if r.hi > c.hi {
				next = append(next, runeRange{c.hi + 1, r.hi})
			}
		}
		out = next
	}
	return out
}

// pyIsWordAt reports whether rs[i] is a word character; outside rs it is not.
func pyIsWordAt(rs []rune, i int) bool {
	return i >= 0 && i < len(rs) && pyIsWord(rs[i])
}

// pyBoundary is Python's Unicode \b at position i of rs.
func pyBoundary(rs []rune, i int) bool {
	if len(rs) == 0 {
		return false
	}
	return pyIsWordAt(rs, i-1) != pyIsWordAt(rs, i)
}

// cleanSeparator is the separator class [,\.\:\;\|/\\_] of clean_filename.
func cleanSeparator(r rune) bool {
	return strings.ContainsRune(`,.:;|/\_`, r)
}

// spaceRun is the length of the run of Python whitespace at rs[i:].
func spaceRun(rs []rune, i int) int {
	n := 0
	for i+n < len(rs) && pyIsSpace(rs[i+n]) {
		n++
	}
	return n
}

// matchSeparatorRun tries \s*[,\.\:\;\|/\\_]\s* at p, both \s* greedy, and
// returns the first end a backtracking engine would accept.
func matchSeparatorRun(rs []rune, p int, accept func(end int) bool) (int, bool) {
	for lead := spaceRun(rs, p); lead >= 0; lead-- {
		q := p + lead
		if q >= len(rs) || !cleanSeparator(rs[q]) {
			continue
		}
		for trail := spaceRun(rs, q+1); trail >= 0; trail-- {
			if end := q + 1 + trail; accept(end) {
				return end, true
			}
		}
	}
	return 0, false
}

// subSeparatorRuns is re.sub(pattern, "", s) for a pattern of the shape
// <before>(\s*[,\.\:\;\|/\\_]\s*)<after>, where before checks the start and
// after the end, both against the original string as Python's sub does.
func subSeparatorRuns(s string, before, after func(rs []rune, i int) bool) string {
	rs := []rune(s)
	var b strings.Builder
	for p := 0; p < len(rs); {
		if before(rs, p) {
			if end, ok := matchSeparatorRun(rs, p, func(end int) bool { return after(rs, end) }); ok {
				p = end
				continue
			}
		}
		b.WriteRune(rs[p])
		p++
	}
	return b.String()
}

// Openers and closers of clean_filename's step 4.
const (
	cleanOpeners = `([{<《〈「『（［【`
	cleanClosers = `】］）』」〉》>}])`
)

// subSeparatorAfterOpener is clean_filename's
//
//	re.sub(r'(?<=[\(\[\{<《〈「『（［【])(\s*[,\.\:\;\|/\\_]\s*)\b', '', s)
//
// which RE2 can't run: it has lookbehind and Unicode \b.
func subSeparatorAfterOpener(s string) string {
	return subSeparatorRuns(s,
		func(rs []rune, i int) bool { return i > 0 && strings.ContainsRune(cleanOpeners, rs[i-1]) },
		pyBoundary)
}

// subSeparatorBeforeCloser is clean_filename's
//
//	re.sub(r'\b(\s*[,\.\:\;\|/\\_]\s*)(?=[】］）』」〉》>\}\]\)])', '', s)
//
// which RE2 can't run: it has lookahead and Unicode \b.
func subSeparatorBeforeCloser(s string) string {
	return subSeparatorRuns(s,
		pyBoundary,
		func(rs []rune, i int) bool { return i < len(rs) && strings.ContainsRune(cleanClosers, rs[i]) })
}

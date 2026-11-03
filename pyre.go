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

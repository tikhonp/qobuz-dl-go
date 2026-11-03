// textwrap.fill with TextWrapper's defaults, as qobuz-dl 2.5.9's tracklist
// calls it. It ports no qobuz-dl file; it follows Lib/textwrap.py of Python
// 3.14.10.

package qobuzdl

import (
	"slices"
	"strings"
)

// textwrapFill is textwrap.fill(text, width): tabs expanded, ASCII
// whitespace turned to spaces, the text split into words and the spaces
// between them, and lines of at most width code points. width must be
// positive; Python raises ValueError otherwise.
func textwrapFill(text string, width int) string {
	return strings.Join(wrapChunks(wordsepSplit(mungeWhitespace(text)), width), "\n")
}

// textwrapSpace is textwrap's _whitespace. It is ASCII only, unlike str's.
func textwrapSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ':
		return true
	}
	return false
}

// mungeWhitespace is _munge_whitespace: str.expandtabs(8), then every
// textwrap whitespace character becomes a space.
func mungeWhitespace(text string) string {
	var b strings.Builder
	col := 0
	for _, r := range text {
		switch r {
		case '\t':
			n := 8 - col%8
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		case '\n', '\r':
			col = 0
		default:
			col++
		}
		if textwrapSpace(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	return b.String()
}

// wordsepSplit is _split with break_on_hyphens: wordsep_re.split(text) with
// the empty pieces dropped. The pattern needs lookbehind, so it is a scanner:
//
//	( [ws]+                                    # whitespace
//	| (?<=[\w!"'&.,?]) -{2,} (?=\w)            # em-dash between words
//	| [^ws]+? (?: -(?: (?<=lt{2}-) | (?<=lt-lt-)) (?= lt -? lt)  # hyphenated word
//	           | (?=[ws]|\z)                   # end of word
//	           | (?<=[\w!"'&.,?]) (?=-{2,}\w)  # word before an em-dash
//	          ) )
//
// with lt = [^\d\W], a word character that is not a decimal digit. Every
// position starts a match, so the matches cover the text.
func wordsepSplit(text string) []string {
	t := []rune(text)
	n := len(t)
	at := func(i int) rune {
		if i < 0 || i >= n {
			return -1
		}
		return t[i]
	}
	isWP := func(r rune) bool { return r >= 0 && (pyIsWord(r) || strings.ContainsRune(`!"'&.,?`, r)) }
	isLT := func(r rune) bool { return r >= 0 && pyIsWord(r) && !pyIsDecimal(r) }
	isWord := func(r rune) bool { return r >= 0 && pyIsWord(r) }
	// emDashAt reports a run of two or more '-' at i followed by \w.
	emDashAt := func(i int) (int, bool) {
		j := i
		for j < n && t[j] == '-' {
			j++
		}
		return j, j-i >= 2 && isWord(at(j))
	}
	var chunks []string
	for i := 0; i < n; {
		if textwrapSpace(t[i]) {
			j := i
			for j < n && textwrapSpace(t[j]) {
				j++
			}
			chunks = append(chunks, string(t[i:j]))
			i = j
			continue
		}
		if isWP(at(i - 1)) {
			if j, ok := emDashAt(i); ok {
				chunks = append(chunks, string(t[i:j]))
				i = j
				continue
			}
		}
		// The lazy [^ws]+? has taken t[i:j]; try the endings at j.
		j := i + 1
		for {
			if at(j) == '-' {
				behind := (isLT(at(j-2)) && isLT(at(j-1))) ||
					(isLT(at(j-3)) && at(j-2) == '-' && isLT(at(j-1)))
				ahead := isLT(at(j+1)) && (isLT(at(j+2)) || (at(j+2) == '-' && isLT(at(j+3))))
				if behind && ahead {
					j++
					break
				}
			}
			if j == n || textwrapSpace(t[j]) {
				break
			}
			if isWP(at(j - 1)) {
				if _, ok := emDashAt(j); ok {
					break
				}
			}
			j++
		}
		chunks = append(chunks, string(t[i:j]))
		i = j
	}
	return chunks
}

// wrapChunks is _wrap_chunks with no indents, drop_whitespace and
// break_long_words on, and no max_lines.
func wrapChunks(chunks []string, width int) []string {
	var lines []string
	rev := slices.Clone(chunks)
	slices.Reverse(rev)
	last := func() string { return rev[len(rev)-1] }
	pop := func() string {
		c := last()
		rev = rev[:len(rev)-1]
		return c
	}
	// Dropping uses str.strip, whose whitespace is Unicode's, while the
	// split uses textwrap's ASCII set.
	blank := func(s string) bool { return pyStrip(s, "") == "" }
	for len(rev) > 0 {
		var cur []string
		curLen := 0
		if blank(last()) && len(lines) > 0 {
			pop()
		}
		for len(rev) > 0 {
			l := pyLen(last())
			if curLen+l > width {
				break
			}
			cur = append(cur, pop())
			curLen += l
		}
		if len(rev) > 0 && pyLen(last()) > width {
			cur = handleLongWord(rev, cur, curLen, width)
			curLen = 0
			for _, c := range cur {
				curLen += pyLen(c)
			}
		}
		if len(cur) > 0 && blank(cur[len(cur)-1]) {
			cur = cur[:len(cur)-1]
		}
		if len(cur) > 0 {
			lines = append(lines, strings.Join(cur, ""))
		}
	}
	return lines
}

// handleLongWord is _handle_long_word: it puts as much of the next chunk as
// fits on the line, cutting after a hyphen when it can, and leaves the rest
// in place.
func handleLongWord(rev, cur []string, curLen, width int) []string {
	spaceLeft := width - curLen
	if width < 1 {
		spaceLeft = 1
	}
	if spaceLeft > 0 {
		chunk := []rune(rev[len(rev)-1])
		end := spaceLeft
		if len(chunk) > spaceLeft {
			hyphen := -1
			for k := spaceLeft - 1; k >= 0; k-- {
				if chunk[k] == '-' {
					hyphen = k
					break
				}
			}
			if hyphen > 0 && slices.ContainsFunc(chunk[:hyphen], func(r rune) bool { return r != '-' }) {
				end = hyphen + 1
			}
		}
		end = min(end, len(chunk))
		rev[len(rev)-1] = string(chunk[end:])
		return append(cur, string(chunk[:end]))
	}
	return cur
}

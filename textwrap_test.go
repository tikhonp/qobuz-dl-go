package qobuzdl

import (
	"strings"
	"testing"
)

func TestTextwrapGolden(t *testing.T) {
	for _, c := range loadGolden(t, "textwrap") {
		in := c.Input.(string)
		if got := textwrapFill(in, 70); got != c.Output {
			t.Errorf("%s: fill(%q)\n got %q\nwant %q", c.Name, in, got, c.Output)
		}
	}
}

func TestWordsepSplitChunks(t *testing.T) {
	// From the _split docstring in Lib/textwrap.py.
	got := wordsepSplit("Look, goof-ball -- use the -b option!")
	want := []string{"Look,", " ", "goof-", "ball", " ", "--", " ", "use", " ", "the", " ", "-b", " ", "option!"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

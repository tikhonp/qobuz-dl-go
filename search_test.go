package qobuzdl

import (
	"errors"
	"net/http"
	"testing"
)

func TestSearchByTypeGolden(t *testing.T) {
	for _, c := range loadGolden(t, "search_text") {
		t.Run(c.Name, func(t *testing.T) {
			in := c.Input.(*pyDict)
			items := in.get("items", nil).([]any)
			got, err := searchResults(in.get("type", nil).(string), items)
			if c.Error != "" {
				checkGoldenError(t, c.Name, err, c.Error)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := c.Output.([]any)
			if len(got) != len(want) {
				t.Fatalf("%d results, want %d", len(got), len(want))
			}
			for i, w := range want {
				w := w.(*pyDict)
				if got[i].Text != w.get("text", nil) || got[i].URL != w.get("url", nil) {
					t.Errorf("%d:\n got %q %q\nwant %q %q", i, got[i].Text, got[i].URL, w.get("text", nil), w.get("url", nil))
				}
			}
		})
	}
}

func TestSearchByTypeShortQuery(t *testing.T) {
	o := DefaultOptions()
	o.HTTPClient = http.DefaultClient
	o.Directory = t.TempDir()
	d, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	// Python counts code points: "ab" and "日本" are short, "日本語" isn't.
	for _, q := range []string{"", "a", "ab", "日本"} {
		if _, err := d.SearchByType(t.Context(), q, "album", 10); !errors.Is(err, ErrQueryTooShort) {
			t.Errorf("%q: %v", q, err)
		}
	}
	if _, err := d.SearchByType(t.Context(), "日本語", "album", 10); !errors.Is(err, errNoClient) {
		t.Errorf("a 3-character query: %v", err)
	}
	if _, err := d.SearchByType(t.Context(), "query", "favorites", 10); err == nil || errors.Is(err, errNoClient) {
		t.Errorf("unknown type: %v", err)
	}
}

func TestAlignText(t *testing.T) {
	for _, c := range []struct {
		in    string
		width int
		want  string
	}{
		{"abc", 5, "abc  "},
		{"abcde", 5, "abcde"},
		{"abcdef", 5, "ab..."},
		{"日本語日本語", 5, "日本..."},
		{"", 3, "   "},
	} {
		if got := alignText(c.in, c.width); got != c.want {
			t.Errorf("alignText(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

package qobuzdl

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewValidates(t *testing.T) {
	base := func() Options {
		o := DefaultOptions()
		o.HTTPClient = http.DefaultClient
		o.Directory = t.TempDir()
		return o
	}
	for _, c := range []struct {
		name   string
		change func(o *Options)
		is     error
	}{
		{"nil client", func(o *Options) { o.HTTPClient = nil }, nil},
		{"zero workers", func(o *Options) { o.Settings.MaxWorkers = 0 }, nil},
		{"negative workers", func(o *Options) { o.Settings.MaxWorkers = -1 }, nil},
		{"quality 4", func(o *Options) { o.Quality = 4 }, ErrInvalidQuality},
		{"quality 0", func(o *Options) { o.Quality = 0 }, ErrInvalidQuality},
		{"quality 28", func(o *Options) { o.Quality = 28 }, ErrInvalidQuality},
	} {
		o := base()
		c.change(&o)
		_, err := New(o)
		if err == nil || (c.is != nil && !errors.Is(err, c.is)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	for _, q := range []int{5, 6, 7, 27} {
		o := base()
		o.Quality = q
		if _, err := New(o); err != nil {
			t.Errorf("quality %d: %v", q, err)
		}
	}
}

func TestNewMakesTheDirectory(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	o := DefaultOptions()
	o.HTTPClient = http.DefaultClient
	o.Directory = "a/../Downloads//x/"
	old := setUmask(0o077)
	d, err := New(o)
	setUmask(old)
	if err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	if want := filepath.Join(cwd, "Downloads", "x"); d.opts.Directory != want {
		t.Errorf("directory %s, want %s", d.opts.Directory, want)
	}
	st, err := os.Stat(d.opts.Directory)
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("%v, %v", st, err)
	}
}

func TestNewCopiesTheOptions(t *testing.T) {
	o := DefaultOptions()
	o.HTTPClient = http.DefaultClient
	o.Directory = t.TempDir()
	d, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	o.Settings.VariousArtistsAliases[0] = "changed"
	o.RetryDelays[0] = 0
	if strings.Contains(strings.Join(d.opts.Settings.VariousArtistsAliases, ","), "changed") || d.opts.RetryDelays[0] == 0 {
		t.Error("New shares slices with the caller")
	}
}

func TestUnhandledKeepsTheChain(t *testing.T) {
	ae := &APIError{Endpoint: "playlist/get", Status: 401}
	err := unhandled(t.Context(), ae)
	var got *APIError
	if !errors.Is(err, ErrUnhandled) || !errors.As(err, &got) || got != ae {
		t.Errorf("%v", err)
	}
	if unhandled(t.Context(), err) != err {
		t.Error("wrapped twice")
	}
}

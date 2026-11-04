package qobuzdl

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// m3uFolder copies a case's files from testdata/fixtures/m3u into a new
// folder named after the case's dir.
func m3uFolder(t *testing.T, in *pyDict) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), in.get("dir", nil).(string))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := in.get("files", nil).(*pyDict)
	for _, dest := range files.keysInOrder() {
		data, err := os.ReadFile(filepath.Join("testdata", "fixtures", "m3u", files.get(dest, nil).(string)))
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, dest)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func m3uItems(t *testing.T, in *pyDict) []*pyDict {
	t.Helper()
	list, _ := in.get("items", nil).([]any)
	items := []*pyDict{}
	for _, it := range list {
		items = append(items, it.(*pyDict))
	}
	return items
}

func TestM3UOrderGolden(t *testing.T) {
	for _, c := range loadGolden(t, "m3u") {
		t.Run(c.Name, func(t *testing.T) {
			in := c.Input.(*pyDict)
			dir := m3uFolder(t, in)
			path, err := MakeM3U(dir, m3uItems(t, in))
			if c.Error != "" {
				checkGoldenError(t, c.Name, err, c.Error)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Output == nil {
				if path != "" {
					t.Fatalf("wrote %s, want no playlist", path)
				}
				return
			}
			if want := filepath.Join(dir, filepath.Base(dir)+".m3u8"); path != want {
				t.Fatalf("path %s, want %s", path, want)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != c.Output {
				t.Errorf("got:\n%s\nwant:\n%s", data, c.Output)
			}
		})
	}
}

func TestM3UFormat(t *testing.T) {
	alpha, err := os.ReadFile(filepath.Join("testdata", "fixtures", "m3u", "alpha.flac"))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("lines, lengths and relative paths, no trailing newline, 0644", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "List")
		for _, p := range []string{"List/b/x.flac", "List/a.flac"} {
			full := filepath.Join(filepath.Dir(dir), p)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, alpha, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		old := setUmask(0o077)
		path, err := MakeM3U(dir+"/", nil)
		setUmask(old)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(path) != "List.m3u8" {
			t.Fatalf("path %s", path)
		}
		data, _ := os.ReadFile(path)
		want := "#EXTM3U\n#EXTINF:61, Artist A - Alpha\na.flac\n#EXTINF:61, Artist A - Alpha\nb/x.flac"
		if string(data) != want {
			t.Errorf("got %q\nwant %q", data, want)
		}
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
			t.Errorf("mode %v", st.Mode().Perm())
		}
	})
	t.Run("no file when the folder holds no audio", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		path, err := MakeM3U(dir, []*pyDict{newDict()})
		if err != nil || path != "" {
			t.Fatalf("%q, %v", path, err)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Fatalf("files: %v", entries)
		}
	})
	t.Run("missing folder", func(t *testing.T) {
		path, err := MakeM3U(filepath.Join(t.TempDir(), "gone"), nil)
		if err != nil || path != "" {
			t.Fatalf("%q, %v", path, err)
		}
	})
	t.Run("an existing playlist is replaced", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "P")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "P.m3u8"), []byte(strings.Repeat("old\n", 100)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "a.flac"), alpha, 0o644); err != nil {
			t.Fatal(err)
		}
		path, err := MakeM3U(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		if string(data) != "#EXTM3U\n#EXTINF:61, Artist A - Alpha\na.flac" {
			t.Errorf("got %q", data)
		}
	})
	t.Run("misses are logged as a table", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "P")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "a.flac"), alpha, 0o644); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))
		item := newDict()
		item.set("id", int64(42))
		item.set("title", "Nowhere")
		if _, err := makeM3U(log, dir, []*pyDict{item}); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		for _, s := range []string{"MISSING LOCAL TRACKS", "Nowhere", "Unknown Artist", "42"} {
			if !strings.Contains(out, s) {
				t.Errorf("log lacks %q:\n%s", s, out)
			}
		}
	})
}

func TestMP3Length(t *testing.T) {
	for _, c := range []struct {
		file string
		want int
	}{
		{"omega.mp3", 1044}, {"lame.mp3", 99}, {"cbr.mp3", 1}, {"vbri.mp3", 130}, {"junk.mp3", 1044},
	} {
		got, err := mp3Length(filepath.Join("testdata", "fixtures", "m3u", c.file))
		if err != nil || int(got) != c.want {
			t.Errorf("%s: %v, %v; want %d", c.file, got, err, c.want)
		}
	}
	for _, file := range []string{"garbage.mp3", "empty.mp3"} {
		if _, err := mp3Length(filepath.Join("testdata", "fixtures", "m3u", file)); err == nil {
			t.Errorf("%s: no error", file)
		}
	}
}

func TestNaturalKey(t *testing.T) {
	a, _ := naturalKey("Track 007b")
	b, _ := naturalKey("track 7c")
	if compareNatural(a, b) >= 0 {
		t.Error("007b should sort before 7c")
	}
	c, _ := naturalKey("x 10")
	d, _ := naturalKey("x 9")
	if compareNatural(c, d) <= 0 {
		t.Error("10 should sort after 9")
	}
	big, _ := naturalKey("x 100000000000000000000000000001")
	if compareNatural(big, c) <= 0 {
		t.Error("big numbers")
	}
	if _, err := naturalKey("1²2"); pyErrKind(err) != "ValueError" {
		t.Errorf("1²2: %v", err)
	}
}

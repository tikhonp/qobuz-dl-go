package downloadsdb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
)

type goldenCase struct {
	Name   string
	Input  map[string]any
	Output map[string]any
	Error  string
}

// loadGolden reads a golden file from the repo's testdata, with Python's
// tagged values turned into int64, float64 and nil.
func loadGolden(t *testing.T, name string) []goldenCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "golden", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc struct {
		Cases []map[string]any `json:"cases"`
	}
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	var out []goldenCase
	for _, c := range doc.Cases {
		gc := goldenCase{Name: c["name"].(string), Input: untag(t, c["input"]).(map[string]any)}
		if e, ok := c["error"].(string); ok {
			gc.Error = e
		} else {
			gc.Output = untag(t, c["output"]).(map[string]any)
		}
		out = append(out, gc)
	}
	return out
}

func untag(t *testing.T, v any) any {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		if kind, ok := x["py"].(string); ok {
			switch kind {
			case "none":
				return nil
			case "int":
				n, err := x["v"].(json.Number).Int64()
				if err != nil {
					t.Fatal(err)
				}
				return n
			case "float":
				f, err := x["v"].(json.Number).Float64()
				if err != nil {
					t.Fatal(err)
				}
				return f
			}
			t.Fatalf("unknown tag %q", kind)
		}
		out := map[string]any{}
		for k, e := range x {
			out[k] = untag(t, e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = untag(t, e)
		}
		return out
	case json.Number:
		t.Fatalf("untagged number %s", x)
	}
	return v
}

// pyStr is str() of the Python values the cases bind.
func pyStr(t *testing.T, v any) *string {
	t.Helper()
	var s string
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		s = x
	case int64:
		s = strconv.FormatInt(x, 10)
	case float64:
		if math.Abs(x) >= 1e16 || (x != 0 && math.Abs(x) < 1e-4) {
			t.Fatalf("no str() for %v here", x)
		}
		s = strconv.FormatFloat(x, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
	default:
		t.Fatalf("no str() for %T", v)
	}
	return &s
}

// record is a DownloadRecord from handle_download_id's keyword arguments,
// with the values str()'d as the downloader passes them.
func record(t *testing.T, c map[string]any) qobuzdl.DownloadRecord {
	t.Helper()
	str := func(k string) string { return *pyStr(t, c[k]) }
	return qobuzdl.DownloadRecord{
		ID: str("item_id"), MediaType: str("media_type"), Quality: int(c["quality"].(int64)),
		FileFormat: str("file_format"), QualityMet: c["quality_met"].(bool),
		BitDepth: pyStr(t, c["bit_depth"]), SamplingRate: pyStr(t, c["sampling_rate"]),
		SavedPath: str("saved_path"), Status: str("status"), URL: str("url"), ReleaseDate: str("release_date"),
		Artist: str("artist"), Album: str("album"),
	}
}

// dump is gen.py's db_dump: the schema, the columns and the rows.
func dump(t *testing.T, path string) map[string]any {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	query := func(q string) ([]string, [][]any) {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		cols, _ := rows.Columns()
		var out [][]any
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out = append(out, vals)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return cols, out
	}
	list := func(rows [][]any) []any {
		out := []any{}
		for _, r := range rows {
			out = append(out, r)
		}
		return out
	}
	_, schema := query("SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name")
	_, columns := query("PRAGMA table_info(downloads)")
	names, rows := query("SELECT * FROM downloads ORDER BY id, quality")
	dicts := []any{}
	for _, r := range rows {
		d := map[string]any{}
		for i, n := range names {
			d[n] = r[i]
		}
		dicts = append(dicts, d)
	}
	return map[string]any{"schema": normalize(list(schema)), "columns": normalize(list(columns)), "rows": dicts}
}

// normalize turns [][]any rows into []any, as the golden holds them.
func normalize(v []any) []any {
	out := make([]any, len(v))
	for i, r := range v {
		out[i] = append([]any{}, r.([]any)...)
	}
	return out
}

func compare(t *testing.T, got, want map[string]any) {
	t.Helper()
	for _, k := range []string{"schema", "columns", "rows"} {
		if !reflect.DeepEqual(got[k], want[k]) {
			t.Errorf("%s:\n got %#v\nwant %#v", k, got[k], want[k])
		}
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixture(name string) string { return filepath.Join("..", "testdata", "fixtures", "db", name) }

func open(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestDownloadsDBSchemaAndRowsGolden(t *testing.T) {
	for _, c := range loadGolden(t, "db_rows") {
		t.Run(c.Name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "qobuz_dl.db")
			if file, ok := c.Input["file"].(string); ok {
				copyFile(t, fixture(file), path)
				_ = open(t, path)
				compare(t, dump(t, path), c.Output)
				return
			}
			db := open(t, path)
			has := []any{}
			for _, call := range c.Input["calls"].([]any) {
				call := call.(map[string]any)
				if call["add"].(bool) {
					if err := db.Add(t.Context(), record(t, call)); err != nil {
						t.Fatal(err)
					}
					continue
				}
				ok, err := db.Has(t.Context(), call["media_type"].(string), call["item_id"].(string), int(call["quality"].(int64)))
				if err != nil {
					t.Fatal(err)
				}
				has = append(has, ok)
			}
			compare(t, dump(t, path), c.Output)
			if !reflect.DeepEqual(has, c.Output["has"]) {
				t.Errorf("has: got %v, want %v", has, c.Output["has"])
			}
		})
	}
}

func TestDownloadsDBMigrationsGolden(t *testing.T) {
	for _, c := range loadGolden(t, "db_migrations") {
		t.Run(c.Name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "qobuz_dl.db")
			if file, ok := c.Input["file"].(string); ok {
				copyFile(t, fixture(file), path)
			}
			db := open(t, path)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			compare(t, dump(t, path), c.Output)
			// A second Open finds the current layout and changes nothing.
			_ = open(t, path)
			compare(t, dump(t, path), c.Output)
		})
	}
}

func TestDownloadsDBDuplicate(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "qobuz_dl.db"))
	ctx := t.Context()
	rec := qobuzdl.DownloadRecord{ID: "evz7usmruz35a", MediaType: "album", Quality: 27, FileFormat: "FLAC", Status: "downloaded"}
	for range 2 {
		if err := db.Add(ctx, rec); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	rec.Artist = "Other"
	if err := db.Add(ctx, rec); err != nil {
		t.Fatalf("Add with other values: %v", err)
	}
	var n int
	if err := db.db.QueryRow("SELECT count(*) FROM downloads").Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d rows, %v", n, err)
	}
}

func TestDownloadsDBSharedWithPython(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qobuz_dl.db")
	copyFile(t, fixture("shared.db"), path)
	db := open(t, path)
	ctx := t.Context()
	for _, c := range []struct {
		media, id string
		quality   int
		want      bool
	}{
		{"album", "evz7usmruz35a", 27, true},
		{"album", "evz7usmruz35a", 6, true},
		{"album", "evz7usmruz35a", 7, false},
		{"track", "20273753", 27, true},
		{"album", "20273753", 27, true},
		{"album", "0634904080853", 27, false},
	} {
		got, err := db.Has(ctx, c.media, c.id, c.quality)
		if err != nil || got != c.want {
			t.Errorf("Has(%s, %s, %d) = %v, %v; want %v", c.media, c.id, c.quality, got, err, c.want)
		}
	}
	bd, sr := "16", "44.1"
	rec := qobuzdl.DownloadRecord{ID: "0634904080853", MediaType: "album", Quality: 27, FileFormat: "FLAC",
		BitDepth: &bd, SamplingRate: &sr, SavedPath: "/music/Com Lag", Status: "downloaded", Artist: "Radiohead"}
	if err := db.Add(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.Has(ctx, "album", "0634904080853", 27); err != nil || !ok {
		t.Fatalf("Has after Add: %v, %v", ok, err)
	}
	// Python's rows are still there, and Go's row reads back with Python's
	// types: text for the bit depth and rate, 0 for quality_met.
	rows := dump(t, path)["rows"].([]any)
	if len(rows) != 4 {
		t.Fatalf("%d rows", len(rows))
	}
	got := rows[0].(map[string]any)
	want := map[string]any{"id": "0634904080853", "media_type": "album", "quality": int64(27), "file_format": "FLAC",
		"quality_met": int64(0), "bit_depth": "16", "sampling_rate": "44.1", "saved_path": "/music/Com Lag",
		"status": "downloaded", "url": "", "release_date": "", "artist": "Radiohead", "album": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("row:\n got %v\nwant %v", got, want)
	}
}

func TestDownloadsDBConcurrent(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "qobuz_dl.db"))
	ctx := t.Context()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 10 {
				rec := qobuzdl.DownloadRecord{ID: fmt.Sprint(j), MediaType: "track", Quality: 27, Status: "downloaded"}
				if err := db.Add(ctx, rec); err != nil {
					t.Error(err)
				}
				if ok, err := db.Has(ctx, "track", fmt.Sprint(j), 27); err != nil || !ok {
					t.Errorf("worker %d: Has(%d) = %v, %v", i, j, ok, err)
				}
			}
		})
	}
	wg.Wait()
}

func TestOpenKeepsOddFileNames(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a#b%20c")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "what?.db")
	db := open(t, path)
	if err := db.Add(context.Background(), qobuzdl.DownloadRecord{ID: "1", Quality: 27}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 || entries[0].Name() != "what?.db" {
		t.Fatalf("files: %v", entries)
	}
}

func TestOpenFailsOnAFolder(t *testing.T) {
	if _, err := Open(t.Context(), t.TempDir()); err == nil {
		t.Fatal("Open on a folder worked")
	}
}

var _ io.Closer = (*DB)(nil)

package qobuzdl

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hexArg(t *testing.T, v any) []byte {
	t.Helper()
	if v == nil {
		return nil
	}
	b, err := hex.DecodeString(v.(string))
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		b = []byte{}
	}
	return b
}

func segmentCases(t *testing.T, prefix string) []goldenCase {
	t.Helper()
	var out []goldenCase
	for _, c := range loadGolden(t, "segments") {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no %q cases", prefix)
	}
	return out
}

func readSegment(t *testing.T, in *pyDict) []byte {
	t.Helper()
	name := filepath.Join("testdata", "fixtures", "segments", pyStr(in.get("format", nil)), pyStr(in.get("segment", nil))+".m4s")
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func hexOrNone(b []byte) any {
	if b == nil {
		return nil
	}
	return hex.EncodeToString(b)
}

func TestQobuzSegmentUUID(t *testing.T) {
	for _, c := range segmentCases(t, "uuid: ") {
		t.Run(c.Name, func(t *testing.T) {
			got := hexOrNone(qobuzSegmentUUID(hexArg(t, c.Input.(*pyDict).get("data", nil))))
			if got != c.Output {
				t.Errorf("got %v, want %v", got, c.Output)
			}
		})
	}
	for _, c := range segmentCases(t, "fixture: ") {
		t.Run(c.Name, func(t *testing.T) {
			got := hexOrNone(qobuzSegmentUUID(readSegment(t, c.Input.(*pyDict))))
			if want := c.Output.(*pyDict).get("uuid", nil); got != want {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestDecryptSegmentsGolden(t *testing.T) {
	for _, c := range segmentCases(t, "fixture: ") {
		t.Run(c.Name, func(t *testing.T) {
			in, want := c.Input.(*pyDict), c.Output.(*pyDict)
			seg := readSegment(t, in)
			plain, err := decryptQobuzSegment(seg, hexArg(t, in.get("raw_key", nil)), hexArg(t, in.get("uuid", nil)))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(plain)
			if got := hex.EncodeToString(sum[:]); got != want.get("sha256", nil) || int64(len(plain)) != want.get("size", nil) {
				t.Errorf("got %d bytes %s, want %v", len(plain), got, pyRepr(want))
			}
		})
	}
}

// The decrypt cases each test takes; the two lists cover segments.json.
var (
	decryptFlagCases = []string{
		"flags", "zero flags only", "frames past the data", "frame inside the box", "frame over its own table",
		"frame after other boxes", "counter of 16 with a carry", "counter of 16 wrapping", "counter of 0",
		"counter of 17 unflagged", "frame count past the table", "two boxes", "other uuid untouched", "uuid None",
		"uuid shorter than 16", "AES-256 key", "key of 10 bytes, nothing flagged", "table with a 16-byte counter",
	}
	decryptMalformedCases = []string{
		"counter of 17", "counter cut by the end", "key of 10 bytes", "zero size stops", "truncated box stops",
		"size past the end stops", "header cut short", "small size steps", "uuid box of 24 at the end",
		"uuid box of 30 at the end", "uuid box of 33 at the end", "empty",
	}
)

func runDecryptCases(t *testing.T, names []string) {
	t.Helper()
	byName := map[string]goldenCase{}
	for _, c := range segmentCases(t, "decrypt: ") {
		byName[strings.TrimPrefix(c.Name, "decrypt: ")] = c
	}
	for _, name := range names {
		c, ok := byName[name]
		if !ok {
			t.Fatalf("no case %q", name)
		}
		t.Run(name, func(t *testing.T) {
			in := c.Input.(*pyDict)
			data := hexArg(t, in.get("data", nil))
			orig := string(data)
			got, err := decryptQobuzSegment(data, hexArg(t, in.get("raw_key", nil)), hexArg(t, in.get("uuid", nil)))
			if string(data) != orig {
				t.Error("the input changed")
			}
			if c.Error != "" {
				if pyErrKind(err) != c.Error {
					t.Errorf("got %v, want %s", err, c.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if h := hex.EncodeToString(got); h != c.Output {
				t.Errorf("got  %s\nwant %v", h, c.Output)
			}
		})
	}
}

func TestDecryptFlagsAndOffsets(t *testing.T) {
	runDecryptCases(t, decryptFlagCases)
}

func TestDecryptMalformedBoxes(t *testing.T) {
	runDecryptCases(t, decryptMalformedCases)
	// Every case in the golden belongs to one of the two tests.
	known := map[string]bool{}
	for _, n := range append(append([]string{}, decryptFlagCases...), decryptMalformedCases...) {
		known[n] = true
	}
	for _, c := range segmentCases(t, "decrypt: ") {
		if !known[strings.TrimPrefix(c.Name, "decrypt: ")] {
			t.Errorf("case %q is in no test", c.Name)
		}
	}
}

// FuzzDecryptQobuzSegment checks the walk never panics on any input.
func FuzzDecryptQobuzSegment(f *testing.F) {
	for _, c := range loadGolden(f, "segments") {
		if strings.HasPrefix(c.Name, "decrypt: ") || strings.HasPrefix(c.Name, "uuid: ") {
			b, _ := hex.DecodeString(c.Input.(*pyDict).get("data", nil).(string))
			f.Add(b)
		}
	}
	uuid := make([]byte, 16)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) >= 24 {
			copy(uuid, data[8:24])
		}
		_ = qobuzSegmentUUID(data)
		_, _ = decryptQobuzSegment(data, make([]byte, 16), uuid)
	})
}

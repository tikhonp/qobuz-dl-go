package qobuzdl

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/go-flac/flacpicture"
	goflac "github.com/go-flac/go-flac"
)

// rawFLAC splits a FLAC file as gen.py's flac_parts does: the bytes before
// "fLaC", the blocks as code and body, and the audio.
type rawFLAC struct {
	prefix []byte
	blocks []rawFLACBlock
	audio  []byte
}

type rawFLACBlock struct {
	code goflac.BlockType
	body []byte
}

func splitFLAC(t *testing.T, path string) rawFLAC {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := 0
	if bytes.HasPrefix(data, []byte("ID3")) {
		start = 10 + syncsafeInt(data[6:10])
	}
	if string(data[start:start+4]) != "fLaC" {
		t.Fatalf("%s: not FLAC", path)
	}
	r := rawFLAC{prefix: data[:start]}
	pos := start + 4
	for {
		h := data[pos]
		size := int(data[pos+1])<<16 | int(data[pos+2])<<8 | int(data[pos+3])
		r.blocks = append(r.blocks, rawFLACBlock{goflac.BlockType(h & 0x7f), data[pos+4 : pos+4+size]})
		pos += 4 + size
		if h&0x80 != 0 {
			break
		}
	}
	r.audio = data[pos:]
	return r
}

// flacResult is what gen.py's flac_result records.
func flacResult(t *testing.T, path string) *pyDict {
	t.Helper()
	raw := splitFLAC(t, path)
	b, err := readFLACBlocks(path)
	if err != nil {
		t.Fatal(err)
	}
	blocks := []any{}
	pictures := []any{}
	for _, blk := range raw.blocks {
		blocks = append(blocks, []any{int64(blk.code), int64(len(blk.body))})
		if blk.code == goflac.Picture {
			p, err := flacpicture.ParseFromMetaDataBlock(goflac.MetaDataBlock{Type: goflac.Picture, Data: blk.body})
			if err != nil {
				t.Fatal(err)
			}
			pictures = append(pictures, []any{int64(p.PictureType), p.MIME, p.Description, sha256Hex(p.ImageData)})
		}
	}
	out := newDict()
	out.set("prefix", sha256Hex(raw.prefix))
	out.set("blocks", blocks)
	comments := []any{}
	if b.tags != nil {
		out.set("vendor", b.tags.vendor)
		for _, c := range b.tags.comments {
			comments = append(comments, []any{c.key, c.value})
		}
	} else {
		out.set("vendor", nil)
	}
	out.set("comments", comments)
	out.set("pictures", pictures)
	out.set("audio", sha256Hex(raw.audio))
	out.set("sha256", fileSHA(t, path))
	return out
}

func compareFLACResult(t *testing.T, got, want *pyDict) {
	t.Helper()
	for _, k := range []string{"prefix", "blocks", "vendor", "comments", "pictures", "audio", "sha256"} {
		if g, w := pyRepr(got.get(k, nil)), pyRepr(want.get(k, nil)); g != w {
			t.Errorf("%s:\n got %s\nwant %s", k, g, w)
		}
	}
}

func runFLACGolden(t *testing.T, keep func(string) bool) {
	t.Helper()
	n := 0
	for _, c := range loadGolden(t, "flac_tagged") {
		if !keep(c.Name) {
			continue
		}
		n++
		t.Run(c.Name, func(t *testing.T) {
			r := runTagger(t, c, false)
			if c.Error != "" {
				checkGoldenError(t, c.Name, r.err, c.Error)
				return
			}
			if r.err != nil {
				t.Fatal(r.err)
			}
			compareFLACResult(t, flacResult(t, r.final), c.Output.(*pyDict))
		})
	}
	if n == 0 {
		t.Fatal("no cases")
	}
}

func TestTagFLACGolden(t *testing.T) {
	runFLACGolden(t, func(name string) bool {
		return !strings.HasPrefix(name, "cover/") && !strings.HasPrefix(name, "multi_value_tags/")
	})
}

func TestEmbedFLACImg(t *testing.T) { runFLACGolden(t, tagGroup("cover/")) }

func TestMultiValueTagsOnlyGenre(t *testing.T) {
	runFLACGolden(t, tagGroup("multi_value_tags/"))
	r := runTagger(t, goldenByName(t, "flac_tagged")["multi_value_tags/True"], false)
	if r.err != nil {
		t.Fatal(r.err)
	}
	b, err := readFLACBlocks(r.final)
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, c := range b.tags.comments {
		count[c.key]++
		if c.key != "GENRE" && strings.Contains(c.value.(string), ", ") && count[c.key] > 1 {
			t.Errorf("%s was split", c.key)
		}
	}
	if count["GENRE"] != 3 || count["ARTIST"] != 1 || count["COPYRIGHT"] != 1 || count["LABEL"] != 1 {
		t.Errorf("comment counts %v", count)
	}
}

func TestTagFLACBlockLayout(t *testing.T) {
	for _, c := range loadGolden(t, "flac_tagged") {
		if c.Error != "" {
			continue
		}
		r := runTagger(t, c, false)
		if r.err != nil {
			t.Fatalf("%s: %v", c.Name, r.err)
		}
		blocks := splitFLAC(t, r.final).blocks
		if blocks[0].code != goflac.StreamInfo {
			t.Errorf("%s: first block %v", c.Name, blocks[0].code)
		}
		last := blocks[len(blocks)-1]
		if last.code != goflac.Padding || len(last.body) != 8192 {
			t.Errorf("%s: last block %v of %d bytes", c.Name, last.code, len(last.body))
		}
		for _, b := range blocks[:len(blocks)-1] {
			if b.code == goflac.Padding {
				t.Errorf("%s: a padding block before the last", c.Name)
			}
		}
	}
	// blocks.flac: the comment block keeps its place, the new picture goes
	// after the second comment block, and both old padding blocks go.
	r := runTagger(t, goldenByName(t, "flac_tagged")["blocks/all"], false)
	if r.err != nil {
		t.Fatal(r.err)
	}
	var codes []goflac.BlockType
	for _, b := range splitFLAC(t, r.final).blocks {
		codes = append(codes, b.code)
	}
	want := []goflac.BlockType{goflac.StreamInfo, goflac.Application, goflac.VorbisComment, goflac.SeekTable,
		goflac.Picture, goflac.VorbisComment, goflac.Picture, goflac.Padding}
	if !slices.Equal(codes, want) {
		t.Errorf("blocks %v, want %v", codes, want)
	}
}

func TestTagFLACEmptiesVendor(t *testing.T) {
	src := filepath.Join("testdata", "fixtures", "audio", "16-44.1-tagged.flac")
	b, err := readFLACBlocks(src)
	if err != nil {
		t.Fatal(err)
	}
	if vendor, _, _ := b.comments(); vendor == "" {
		t.Fatal("the fixture's vendor string is empty")
	}
	for _, c := range loadGolden(t, "flac_tagged") {
		in := c.Input.(*pyDict)
		if c.Error != "" || in.get("file", nil) != "16-44.1-tagged.flac" {
			continue
		}
		r := runTagger(t, c, false)
		if r.err != nil {
			t.Fatalf("%s: %v", c.Name, r.err)
		}
		b, err := readFLACBlocks(r.final)
		if err != nil {
			t.Fatal(err)
		}
		vendor, list, ok := b.comments()
		if !ok || vendor != "" {
			t.Errorf("%s: vendor %q", c.Name, vendor)
		}
		for _, cm := range list {
			if k, _, _ := strings.Cut(cm, "="); strings.EqualFold(k, "ENCODER") {
				t.Errorf("%s: %s left", c.Name, cm)
			}
		}
	}
}

func TestTagFLACKeepsAudio(t *testing.T) {
	for _, c := range loadGolden(t, "flac_tagged") {
		if c.Error != "" {
			continue
		}
		r := runTagger(t, c, false)
		if r.err != nil {
			t.Fatalf("%s: %v", c.Name, r.err)
		}
		src, got := splitFLAC(t, r.src), splitFLAC(t, r.final)
		if !bytes.Equal(src.audio, got.audio) {
			t.Errorf("%s: the audio changed", c.Name)
		}
		if !bytes.Equal(src.prefix, got.prefix) {
			t.Errorf("%s: the bytes before fLaC changed", c.Name)
		}
	}
}

// TestTagFLACStreamsAudio tags a file with 100 MB of audio and checks that
// the tagger allocates far less than that.
func TestTagFLACStreamsAudio(t *testing.T) {
	raw := splitFLAC(t, filepath.Join("testdata", "fixtures", "audio", "16-44.1.flac"))
	root := t.TempDir()
	name := filepath.Join(root, "big.tmp")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	head := []byte("fLaC")
	for i, b := range raw.blocks {
		h := byte(b.code)
		if i == len(raw.blocks)-1 {
			h |= 0x80
		}
		head = append(head, h, byte(len(b.body)>>16), byte(len(b.body)>>8), byte(len(b.body)))
		head = append(head, b.body...)
	}
	if _, err := f.Write(head); err != nil {
		t.Fatal(err)
	}
	const audio = 100 << 20
	if err := f.Truncate(int64(len(head)) + audio); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	item := newDict()
	item.set("title", "T")
	album := newDict()
	album.set("title", "A")
	s := DefaultSettings()

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := TagFLAC(discardLogger(), name, root, filepath.Join(root, "big.flac"), item, album, false, false, &s); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 32<<20 {
		t.Errorf("tagging allocated %d MB", alloc>>20)
	}
	st, err := os.Stat(filepath.Join(root, "big.flac"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() < audio {
		t.Errorf("size %d", st.Size())
	}
}

func TestReadFLACBlocksRefusesOtherFiles(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"empty":            nil,
		"mp3":              []byte("ID3\x03\x00\x00\x00\x00\x00\x00\xff\xfb"),
		"short block":      []byte("fLaC\x80\x00\x00\x22\x00"),
		"no stream info":   []byte("fLaC\x81\x00\x00\x00"),
		"two seek tables":  []byte("fLaC\x03\x00\x00\x00\x83\x00\x00\x00"),
		"bad comment":      []byte("fLaC\x84\x00\x00\x04\xff\x00\x00\x00"),
		"zero sample rate": append([]byte("fLaC\x80\x00\x00\x22"), make([]byte, 34)...),
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readFLACBlocks(p); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestFLACStreamInfo(t *testing.T) {
	for _, tc := range []struct {
		file        string
		depth, rate int
		samples     int64
	}{
		{"16-44.1.flac", 16, 44100, 4410},
		{"24-96.flac", 24, 96000, 9600},
		{"24-192.flac", 24, 192000, 19200},
		{"id3-prefixed.flac", 16, 44100, 4410},
	} {
		depth, rate, samples, err := flacStreamInfo(filepath.Join("testdata", "fixtures", "audio", tc.file))
		if err != nil || depth != tc.depth || rate != tc.rate || samples != tc.samples {
			t.Errorf("%s: got %d, %d, %d, %v; want %d, %d, %d", tc.file, depth, rate, samples, err, tc.depth, tc.rate, tc.samples)
		}
	}
}

func TestParseVorbisCommentLikeMutagen(t *testing.T) {
	var body []byte
	add := func(s string) {
		body = binary.LittleEndian.AppendUint32(body, uint32(len(s)))
		body = append(body, s...)
	}
	add("vendor \xff")
	body = binary.LittleEndian.AppendUint32(body, 6)
	for _, c := range []string{"A=1", "noequals", "=x", "K~=y", "KÉ=z", "B=a=b"} {
		add(c)
	}
	vendor, comments, err := parseVorbisComment(body)
	if err != nil {
		t.Fatal(err)
	}
	if vendor != "vendor \ufffd" {
		t.Errorf("vendor %q", vendor)
	}
	want := []vorbisComment{{"A", "1"}, {"unknown1", "noequals"}, {"K?", "z"}, {"B", "a=b"}}
	if !slices.Equal(comments, want) {
		t.Errorf("comments %q, want %q", comments, want)
	}
	if _, _, err := parseVorbisComment(body[:len(body)-2]); err == nil {
		t.Error("a cut block parsed")
	}
}

func TestPyDecodeUTF8(t *testing.T) {
	// Outputs from Python 3.14's bytes.decode("utf-8", "replace").
	for in, want := range map[string]string{
		"\xff\xfeok\xe2\x82":   "\ufffd\ufffdok\ufffd",
		"\xe0\x80":             "\ufffd\ufffd",
		"\xf0\x9f\x98":         "\ufffd",
		"\xf0\x9f\x98a":        "\ufffda",
		"\xed\xa0\x80":         "\ufffd\ufffd\ufffd",
		"\xc0\xaf":             "\ufffd\ufffd",
		"\xf4\x90\x80\x80":     "\ufffd\ufffd\ufffd\ufffd",
		"a\xe2\x82\xacb":       "a\u20acb",
		"\xe2\x82":             "\ufffd",
		"\xf8\x88\x80\x80\x80": "\ufffd\ufffd\ufffd\ufffd\ufffd",
	} {
		if got := pyDecodeUTF8([]byte(in)); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

package qobuzdl

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/bogem/id3v2/v2"
)

// goldenSettings is QobuzDLSettings() with a golden case's overrides.
func goldenSettings(tb testing.TB, v any) *Settings {
	tb.Helper()
	s := DefaultSettings()
	flags := map[string]*bool{
		"no_album_artist_tag": &s.NoAlbumArtistTag, "no_album_title_tag": &s.NoAlbumTitleTag,
		"no_track_artist_tag": &s.NoTrackArtistTag, "no_track_title_tag": &s.NoTrackTitleTag,
		"no_release_date_tag": &s.NoReleaseDateTag, "no_media_type_tag": &s.NoMediaTypeTag,
		"no_genre_tag": &s.NoGenreTag, "no_track_number_tag": &s.NoTrackNumberTag,
		"no_track_total_tag": &s.NoTrackTotalTag, "no_disc_number_tag": &s.NoDiscNumberTag,
		"no_disc_total_tag": &s.NoDiscTotalTag, "no_composer_tag": &s.NoComposerTag,
		"no_conductor_tag": &s.NoConductorTag, "no_ensemble_tag": &s.NoEnsembleTag,
		"no_work_tag": &s.NoWorkTag, "no_explicit_tag": &s.NoExplicitTag,
		"no_copyright_tag": &s.NoCopyrightTag, "no_label_tag": &s.NoLabelTag,
		"no_upc_tag": &s.NoUPCTag, "no_isrc_tag": &s.NoISRCTag,
		"no_replaygain_tag": &s.NoReplayGainTag, "no_album_url_tag": &s.NoAlbumURLTag,
		"multi_value_tags": &s.MultiValueTags,
	}
	over := v.(*pyDict)
	for _, k := range over.keys {
		val := over.vals[k]
		if k == "various_artists_aliases" {
			s.VariousArtistsAliases = ParseAliases(val.(string))
			continue
		}
		p, ok := flags[k]
		if !ok {
			tb.Fatalf("setting %s is not mapped", k)
		}
		*p = val.(bool)
	}
	return &s
}

// checkGoldenError checks err against a golden error: the kind for Python's
// built-in exceptions, any error for mutagen's.
func checkGoldenError(t *testing.T, name string, err error, want string) {
	t.Helper()
	switch want {
	case "KeyError", "TypeError", "ValueError", "AttributeError", "IndexError", "OverflowError":
		if got := pyErrKind(err); got != want {
			t.Errorf("%s: got %s (%v), want %s", name, got, err, want)
		}
	default:
		if err == nil {
			t.Errorf("%s: no error, want %s", name, want)
		}
	}
}

// tagGroups are the tags.json cases the checklist names a test for.
var tagGroups = []string{"artist/", "composer/", "genre/", "copyright/", "replaygain/"}

func runTagsGolden(t *testing.T, keep func(name string) bool) {
	t.Helper()
	n := 0
	for _, c := range loadGolden(t, "tags") {
		if !keep(c.Name) {
			continue
		}
		n++
		in := c.Input.(*pyDict)
		item, _ := in.get("item", nil).(*pyDict)
		got, err := getTagsToAdd(discardLogger(), in.get("album", nil), item, goldenSettings(t, in.get("settings", nil)))
		if c.Error != "" {
			checkGoldenError(t, c.Name, err, c.Error)
			continue
		}
		if err != nil || pyRepr(got) != pyRepr(c.Output) {
			t.Errorf("%s:\n got %s, %v\nwant %s", c.Name, pyRepr(got), err, pyRepr(c.Output))
		}
	}
	if n == 0 {
		t.Fatal("no cases")
	}
}

func TestTagsGolden(t *testing.T) {
	runTagsGolden(t, func(name string) bool {
		return !slices.ContainsFunc(tagGroups, func(p string) bool { return strings.HasPrefix(name, p) })
	})
}

func tagGroup(prefix string) func(string) bool {
	return func(name string) bool { return strings.HasPrefix(name, prefix) }
}

func TestArtistTagGolden(t *testing.T)   { runTagsGolden(t, tagGroup("artist/")) }
func TestComposerTagGolden(t *testing.T) { runTagsGolden(t, tagGroup("composer/")) }
func TestGenreTagGolden(t *testing.T)    { runTagsGolden(t, tagGroup("genre/")) }

func TestReplayGainFormatting(t *testing.T) { runTagsGolden(t, tagGroup("replaygain/")) }

func TestCopyrightMissingAgainstNull(t *testing.T) {
	runTagsGolden(t, tagGroup("copyright/"))
	s := DefaultSettings()
	album := newDict()
	album.set("title", "A")
	item := newDict()
	item.set("title", "T")
	tags, err := getTagsToAdd(discardLogger(), album, item, &s)
	if err != nil || tags.get("COPYRIGHT", nil) != "n/a" {
		t.Errorf("missing: got %v, %v; want n/a", tags.get("COPYRIGHT", nil), err)
	}
	album.set("copyright", nil)
	tags, err = getTagsToAdd(discardLogger(), album, item, &s)
	if err != nil || !tags.has("COPYRIGHT") || tags.get("COPYRIGHT", "x") != nil {
		t.Errorf("null: got %v, %v; want None", tags.get("COPYRIGHT", "x"), err)
	}
}

func TestMetadataHelpersGolden(t *testing.T) {
	for _, c := range loadGolden(t, "metadata_helpers") {
		in := c.Input.(*pyDict)
		var got any
		var err error
		switch fn := in.get("fn", nil); fn {
		case "title_with_version":
			got, err = getTitleWithVersion(in.get("title", nil), in.get("version", nil))
		case "get_title":
			got, err = metadataGetTitle(in.get("track", nil).(*pyDict))
		case "format_copyright":
			got, err = formatCopyright(in.get("s", nil))
		case "format_genres":
			got, err = formatGenres(in.get("genres", nil).([]any))
		default:
			t.Fatalf("%s: unknown fn %v", c.Name, fn)
		}
		if c.Error != "" {
			checkGoldenError(t, c.Name, err, c.Error)
			continue
		}
		if err != nil || pyRepr(got) != pyRepr(c.Output) {
			t.Errorf("%s: got %s, %v; want %s", c.Name, pyRepr(got), err, pyRepr(c.Output))
		}
	}
}

func TestReleaseTypesGolden(t *testing.T) {
	for _, c := range loadGolden(t, "release_types") {
		in := c.Input.(*pyDict)
		album := in.get("album", nil).(*pyDict)
		var aliases []string
		for _, a := range in.get("aliases", nil).([]any) {
			aliases = append(aliases, a.(string))
		}
		types, err := releaseTypes(discardLogger(), album, aliases)
		var various bool
		if err == nil {
			various, err = isVariousArtists(discardLogger(), album, aliases)
		}
		if c.Error != "" {
			checkGoldenError(t, c.Name, err, c.Error)
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		got := newDict()
		got.set("types", types)
		got.set("various", various)
		if pyRepr(got) != pyRepr(c.Output) {
			t.Errorf("%s: got %s, want %s", c.Name, pyRepr(got), pyRepr(c.Output))
		}
	}
}

func TestDiscTrackTotalGolden(t *testing.T) {
	for _, c := range loadGolden(t, "disc_track_total") {
		in := c.Input.(*pyDict)
		got, err := discTrackTotal(in.get("album", nil), in.get("item", nil).(*pyDict))
		if c.Error != "" {
			checkGoldenError(t, c.Name, err, c.Error)
			continue
		}
		if err != nil || got != c.Output {
			t.Errorf("%s: got %q, %v; want %s", c.Name, got, err, pyRepr(c.Output))
		}
	}
}

// taggerRun is one golden tagging case laid out as gen.py lays it out:
// parent/album/~tmp_01.tmp, renamed to parent/album/01 - final.
type taggerRun struct {
	src, root, filename, final string
	err                        error
}

func runTagger(t *testing.T, c goldenCase, mp3 bool) taggerRun {
	t.Helper()
	in := c.Input.(*pyDict)
	root := filepath.Join(t.TempDir(), "parent", "album")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	placeCover(t, root, in.get("cover", nil).(string))
	r := taggerRun{
		src:      filepath.Join("testdata", "fixtures", "audio", in.get("file", nil).(string)),
		root:     root,
		filename: filepath.Join(root, "~tmp_01.tmp"),
		final:    filepath.Join(root, "01 - final"),
	}
	data, err := os.ReadFile(r.src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.filename, data, 0o644); err != nil {
		t.Fatal(err)
	}
	d := in.get("d", nil).(*pyDict)
	album, _ := in.get("album", nil).(*pyDict)
	s := goldenSettings(t, in.get("settings", nil))
	tagger := TagFLAC
	if mp3 {
		tagger = TagMP3
	}
	r.err = tagger(discardLogger(), r.filename, r.root, r.final, d, album, in.get("istrack", nil).(bool), in.get("em_image", nil).(bool), s)
	return r
}

// placeCover puts embed_cover.jpg where gen.py's place_cover does.
func placeCover(t *testing.T, root, where string) {
	t.Helper()
	cover, err := os.ReadFile(filepath.Join("testdata", "fixtures", "audio", "cover.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(dir string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, embCoverName), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	big := func(n int) { write(root, bytes.Repeat([]byte{0xff}, n)) }
	switch where {
	case "folder":
		write(root, cover)
	case "parent":
		write(filepath.Dir(root), cover)
	case "both":
		write(root, cover)
		rev := slices.Clone(cover)
		slices.Reverse(rev)
		write(filepath.Dir(root), rev)
	case "large":
		big(flacMaxCoverSize + 1)
	case "limit":
		big(flacMaxCoverSize)
	case "none":
	default:
		t.Fatalf("cover %q", where)
	}
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// mp3Result is what gen.py's mp3_result records: the ID3 version, the frames
// as mutagen reads them, a hash of each raw frame, and a hash of the audio.
func mp3Result(t *testing.T, path string) *pyDict {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 10 || string(data[:3]) != "ID3" {
		t.Fatalf("%s: no ID3 tag", path)
	}
	end := 10 + syncsafeInt(data[6:10])
	var frames, raws []any
	for pos := 10; pos+10 <= end && !bytes.Equal(data[pos:pos+4], make([]byte, 4)); {
		size := int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		raws = append(raws, sha256Hex(data[pos:pos+10+size]))
		if size > 0 {
			frames = append(frames, decodeID3Frame(t, string(data[pos:pos+4]), data[pos+10:pos+10+size]))
		}
		pos += 10 + size
	}
	out := newDict()
	out.set("version", int64(data[3]))
	out.set("frames", frames)
	out.set("raw", raws)
	out.set("audio", sha256Hex(data[end:]))
	return out
}

func sortByRepr(xs []any) {
	slices.SortFunc(xs, func(a, b any) int { return strings.Compare(jsonish(a), jsonish(b)) })
}

// jsonish is a sort key for frames, which compare as sets.
func jsonish(v any) string {
	if d, ok := v.(*pyDict); ok {
		b, _ := d.MarshalJSON()
		return string(b)
	}
	return pyRepr(v)
}

// decodeID3Frame reads a frame body the way mutagen does for the frames the
// taggers write, into gen.py's mp3_frame shape.
func decodeID3Frame(t *testing.T, id string, body []byte) *pyDict {
	t.Helper()
	enc := body[0]
	rest := body[1:]
	text := func(b []byte) (string, []byte) {
		if enc == 0 {
			i := bytes.IndexByte(b, 0)
			if i < 0 {
				return latin1(b), nil
			}
			return latin1(b[:i]), b[i+1:]
		}
		i := 0
		for ; i+1 < len(b); i += 2 {
			if b[i] == 0 && b[i+1] == 0 {
				break
			}
		}
		s := b[:min(i, len(b))]
		var u []uint16
		le := len(s) < 2 || s[0] != 0xfe || s[1] != 0xff
		if len(s) >= 2 && (s[0] == 0xff && s[1] == 0xfe || s[0] == 0xfe && s[1] == 0xff) {
			s = s[2:]
		}
		for j := 0; j+1 < len(s); j += 2 {
			if le {
				u = append(u, binary.LittleEndian.Uint16(s[j:]))
			} else {
				u = append(u, binary.BigEndian.Uint16(s[j:]))
			}
		}
		if i+2 > len(b) {
			return string(utf16.Decode(u)), nil
		}
		return string(utf16.Decode(u)), b[i+2:]
	}
	values := func(b []byte) []any {
		var out []any
		for len(b) > 0 {
			var s string
			s, b = text(b)
			out = append(out, s)
			if len(bytes.Trim(b, "\x00")) == 0 {
				break
			}
		}
		return out
	}
	out := newDict()
	key := id
	switch id {
	case "APIC":
		i := bytes.IndexByte(rest, 0)
		mime := string(rest[:i])
		ptype := rest[i+1]
		desc, data := text(rest[i+2:])
		out.set("key", "APIC:"+desc)
		out.set("encoding", int64(enc))
		out.set("mime", mime)
		out.set("type", int64(ptype))
		out.set("data", sha256Hex(data))
		return out
	case "TXXX":
		var desc string
		desc, rest = text(rest)
		key = "TXXX:" + desc
	case "COMM":
		lang := string(rest[:3])
		var desc string
		desc, rest = text(rest[3:])
		key = "COMM:" + desc + ":" + lang
	}
	out.set("key", key)
	out.set("encoding", int64(enc))
	out.set("text", values(rest))
	return out
}

func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func TestTagMP3Golden(t *testing.T) {
	for _, c := range loadGolden(t, "mp3_tagged") {
		t.Run(c.Name, func(t *testing.T) {
			r := runTagger(t, c, true)
			if c.Error != "" {
				checkGoldenError(t, c.Name, r.err, c.Error)
				return
			}
			if r.err != nil {
				t.Fatal(r.err)
			}
			got := mp3Result(t, r.final)
			want := c.Output.(*pyDict)
			for _, k := range []string{"version", "frames", "audio", "raw"} {
				g, w := got.get(k, nil), want.get(k, nil)
				if gl, ok := g.([]any); ok {
					wl, _ := w.([]any)
					wl = slices.Clone(wl)
					sortByRepr(gl)
					sortByRepr(wl)
					w = wl
				}
				if g, w := pyRepr(g), pyRepr(w); g != w {
					t.Errorf("%s:\n got %s\nwant %s", k, g, w)
				}
			}
		})
	}
}

func TestTagMP3RemovesEncoderFrames(t *testing.T) {
	c := goldenByName(t, "mp3_tagged")["existing/no cover"]
	r := runTagger(t, c, true)
	if r.err != nil {
		t.Fatal(r.err)
	}
	src, err := id3v2.Open(r.src, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	if len(src.GetFrames("TENC")) == 0 || len(src.GetFrames("TSSE")) == 0 {
		t.Fatal("the fixture lacks TENC or TSSE")
	}
	out, err := id3v2.Open(r.final, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	for _, id := range []string{"TENC", "TSSE"} {
		if fs := out.GetFrames(id); len(fs) > 0 {
			t.Errorf("%s left: %v", id, fs)
		}
	}
	if out.Version() != 3 {
		t.Errorf("version %d, want 3", out.Version())
	}
}

// mp3Audio is the file after its ID3v2 tag.
func mp3Audio(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) >= 10 && string(data[:3]) == "ID3" {
		return data[10+syncsafeInt(data[6:10]):]
	}
	return data
}

func TestTagMP3KeepsAudio(t *testing.T) {
	for _, c := range loadGolden(t, "mp3_tagged") {
		if c.Error != "" {
			continue
		}
		r := runTagger(t, c, true)
		if r.err != nil {
			t.Fatalf("%s: %v", c.Name, r.err)
		}
		if !bytes.Equal(mp3Audio(t, r.final), mp3Audio(t, r.src)) {
			t.Errorf("%s: the audio changed", c.Name)
		}
	}
}

func TestTagRenamesToFinal(t *testing.T) {
	for _, tc := range []struct {
		golden, name string
		mp3          bool
	}{{"flac_tagged", "cover/folder", false}, {"mp3_tagged", "cover/folder", true}} {
		r := runTagger(t, goldenByName(t, tc.golden)[tc.name], tc.mp3)
		if r.err != nil {
			t.Fatal(r.err)
		}
		if _, err := os.Stat(r.filename); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: the temp file is still there: %v", tc.golden, err)
		}
		st, err := os.Stat(r.final)
		if err != nil {
			t.Fatalf("%s: %v", tc.golden, err)
		}
		if st.Mode().Perm() != 0o644 {
			t.Errorf("%s: mode %v, want 0644", tc.golden, st.Mode().Perm())
		}
		entries, err := os.ReadDir(r.root)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if want := []string{"01 - final", embCoverName}; !slices.Equal(names, want) {
			t.Errorf("%s: folder holds %v, want %v", tc.golden, names, want)
		}
	}
}

func TestTagFailureKeepsTheFile(t *testing.T) {
	for _, tc := range []struct {
		golden, name string
		mp3          bool
	}{{"flac_tagged", "values/isrc a number", false}, {"flac_tagged", "cover/limit", false}, {"mp3_tagged", "values/isrc a number", true}} {
		r := runTagger(t, goldenByName(t, tc.golden)[tc.name], tc.mp3)
		if r.err == nil {
			t.Fatalf("%s %s: no error", tc.golden, tc.name)
		}
		if fileSHA(t, r.filename) != fileSHA(t, r.src) {
			t.Errorf("%s %s: the temp file changed", tc.golden, tc.name)
		}
		entries, _ := os.ReadDir(r.root)
		for _, e := range entries {
			if e.Name() != "~tmp_01.tmp" && e.Name() != embCoverName {
				t.Errorf("%s %s: left %s", tc.golden, tc.name, e.Name())
			}
		}
	}
}

func TestEmbedCoverLogs(t *testing.T) {
	var lines []string
	log := slog.New(recordHandler{&lines})
	c := goldenByName(t, "flac_tagged")["cover/large"]
	in := c.Input.(*pyDict)
	root := filepath.Join(t.TempDir(), "parent", "album")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := readFLACBlocks(filepath.Join("testdata", "fixtures", "audio", in.get("file", nil).(string)))
	if err != nil {
		t.Fatal(err)
	}
	embedFLACImg(log, root, b)
	placeCover(t, root, "large")
	embedFLACImg(log, root, b)
	want := []string{
		"DEBUG Cover image not found to embed: " + filepath.Join(filepath.Dir(root), embCoverName),
		"ERROR Error embedding image: " + coverTooLargeMsg,
	}
	if !slices.Equal(lines, want) {
		t.Errorf("logged %q, want %q", lines, want)
	}
}

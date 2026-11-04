// Ports qobuz_dl/utils.py from qobuz-dl 2.5.9. read_config_file,
// label_matches and PartialFormatter are not ported.

package qobuzdl

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"regexp"
	"slices"
	"strings"

	"github.com/bogem/id3v2/v2"
)

// GetURLInfo is get_url_info: the kind and the ID in a Qobuz URL. A URL it
// can't read is ErrInvalidURL around the AttributeError Python raises, which
// handle_url catches.
func GetURLInfo(url string) (kind, id string, err error) {
	m := reURLInfo.FindStringSubmatch(url)
	if m == nil {
		return "", "", fmt.Errorf("%w: %w", ErrInvalidURL,
			pyErr(pyAttributeError, "'NoneType' object has no attribute 'groups'"))
	}
	return m[1], m[2], nil
}

// reURLInfo is get_url_info's
//
//	(?:https:\/\/(?:w{3}|open|play)\.qobuz\.com)?(?:\/[a-z]{2}-[a-z]{2})?\/(album|artist|track|playlist|label)(?:\/[-\w\d]+)?\/([\w\d]+)
//
// with Python's Unicode \w and \d.
var reURLInfo = regexp.MustCompile(
	`(?:https://(?:w{3}|open|play)\.qobuz\.com)?(?:/[a-z]{2}-[a-z]{2})?/(album|artist|track|playlist|label)` +
		`(?:/[-` + reWordChars + reDigitChars + `]+)?/([` + reWordChars + reDigitChars + `]+)`)

// getAlbumArtist is get_album_artist: the names of the album's main artists,
// else the album artist's name, else nothing. The names are whatever the API
// holds, as in Python, so callers see a non-string as Python would.
func getAlbumArtist(log *slog.Logger, album *pyDict) ([]any, error) {
	single := func() ([]any, error) {
		artist, err := album.dict("artist")
		if err != nil {
			return nil, err
		}
		name := artist.get("name", "")
		if pyTruthy(name) {
			return []any{name}, nil
		}
		return []any{}, nil
	}
	names, err := mainArtists(album, single)
	if err != nil {
		// Python's except Exception runs the fallback again, outside the try,
		// so its AttributeError escapes.
		log.Error(fmt.Sprintf("Error getting album artist: %s", pyExceptionStr(err)))
		return single()
	}
	return names, nil
}

// mainArtists is the try block of get_album_artist.
func mainArtists(album *pyDict, single func() ([]any, error)) ([]any, error) {
	if !pyTruthy(album.get("artists", nil)) {
		return single()
	}
	artists, err := album.list("artists")
	if err != nil {
		return nil, err
	}
	var main []any
	for _, a := range artists {
		ad, err := asDict(a)
		if err != nil {
			return nil, err
		}
		in, err := pyContains(ad.get("roles", []any{}), "main-artist")
		if err != nil {
			return nil, err
		}
		if in {
			main = append(main, a)
		}
	}
	if len(main) == 0 {
		return single()
	}
	names := make([]any, 0, len(main))
	for _, a := range main {
		name, err := a.(*pyDict).item("name")
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

// pyContains is `item in container` for a string item.
func pyContains(container any, item string) (bool, error) {
	switch c := container.(type) {
	case string:
		return strings.Contains(c, item), nil
	case []any:
		for _, v := range c {
			if pyEqual(v, item) {
				return true, nil
			}
		}
		return false, nil
	case *pyDict:
		return c.has(item), nil
	}
	return false, pyErr(pyTypeError, "argument of type '%s' is not a container or iterable", pyTypeName(container))
}

// pyExceptionStr is str(e) for an exception: the message without the class
// name, which is what f"{e}" prints.
func pyExceptionStr(err error) string {
	var pe *pyError
	if errors.As(err, &pe) {
		return pe.msg
	}
	return err.Error()
}

// Separator runs, bracket pairs and whitespace for clean_filename, with
// Python's Unicode \s and \W.
var (
	// (?:\s*([,\.\:\;\|/\\_])\s*){2,}
	reCleanSeparatorRun = regexp.MustCompile(`(?:` + reS + `*([,.:;|/\\_])` + reS + `*){2,}`)
	// \(\s*\W*\s*\) and the same for each pair, in upstream's order.
	reCleanEmptyBrackets = func() []*regexp.Regexp {
		var out []*regexp.Regexp
		for _, pair := range []string{"()", "[]", "{}", "<>", "《》", "〈〉", "「」", "『』", "（）", "［］", "【】"} {
			rs := []rune(pair)
			out = append(out, regexp.MustCompile(
				regexp.QuoteMeta(string(rs[0]))+reS+"*"+reNotW+"*"+reS+"*"+regexp.QuoteMeta(string(rs[1]))))
		}
		return out
	}()
	// \s+
	reCleanSpaces = regexp.MustCompile(reS + `+`)
	// apply_legacy_charmap's \s*-\s*-+
	reLegacyDashes = regexp.MustCompile(reS + `*-` + reS + `*-+`)
)

// applyLegacyCharmap is apply_legacy_charmap: ASCII stand-ins for the
// characters Windows forbids in names.
func applyLegacyCharmap(name string) string {
	for _, r := range [][2]string{
		{":", "-"}, {"?", ""}, {"/", "-"}, {`\`, "-"}, {"*", "-"},
		{`"`, "'"}, {"<", "["}, {">", "]"}, {"|", "-"},
	} {
		name = strings.ReplaceAll(name, r[0], r[1])
	}
	return reLegacyDashes.ReplaceAllString(name, " -")
}

// CleanFilename is clean_filename: NFC, separator runs merged, empty bracket
// pairs and separators inside brackets dropped, whitespace collapsed, ends
// stripped, and the characters Windows forbids replaced.
func CleanFilename(name string, legacyCharmap bool) string {
	name = pyNFC(name)
	name = reCleanSeparatorRun.ReplaceAllString(name, "${1} ")
	for _, re := range reCleanEmptyBrackets {
		name = re.ReplaceAllString(name, "")
	}
	name = subSeparatorAfterOpener(name)
	name = subSeparatorBeforeCloser(name)
	name = reCleanSpaces.ReplaceAllString(name, " ")
	name = pyStrip(pyStrip(pyStrip(name, ""), "."), "")
	if legacyCharmap {
		return applyLegacyCharmap(name)
	}
	return invalidCharsToFullwidth(name)
}

// invalidCharsToFullwidth is invalid_chars_to_fullwidth.
var fullwidthReplacer = strings.NewReplacer(
	"/", "／", `\`, "＼", ":", "：", "*", "＊", "?", "？", `"`, "＂", "<", "＜", ">", "＞", "|", "｜")

func invalidCharsToFullwidth(name string) string {
	return fullwidthReplacer.Replace(name)
}

// SmartDiscographyFilter is smart_discography_filter over an artist's pages:
// one release per title, with the best bit depth and then the highest rate,
// or the lowest with saveSpace; releases by other artists dropped, remasters
// preferred, and with skipExtras no live, deluxe and similar editions.
func SmartDiscographyFilter(pages []*pyDict, saveSpace, skipExtras bool) ([]*pyDict, error) {
	if len(pages) == 0 {
		return nil, pyErr(pyIndexError, "list index out of range")
	}
	requested, err := pages[0].item("name")
	if err != nil {
		return nil, err
	}
	var items []*pyDict
	for _, page := range pages {
		albums, err := page.item("albums")
		if err != nil {
			return nil, err
		}
		list, err := pyGetItem(albums, "items")
		if err != nil {
			return nil, err
		}
		vals, err := pyIter(list)
		if err != nil {
			return nil, err
		}
		for _, v := range vals {
			// Python's item["title"] comes next and fails on a non-dict.
			if _, err := pyGetItem(v, "title"); err != nil {
				return nil, err
			}
			items = append(items, v.(*pyDict))
		}
	}

	var order []string
	groups := map[string][]*pyDict{}
	for _, item := range items {
		title, _ := item.item("title")
		e, err := essence(title)
		if err != nil {
			return nil, err
		}
		if _, ok := groups[e]; !ok {
			order = append(order, e)
		}
		groups[e] = append(groups[e], item)
	}

	var out []*pyDict
	for _, key := range order {
		pick, err := bestOfGroup(groups[key], requested, saveSpace, skipExtras)
		if err != nil {
			return nil, err
		}
		if pick != nil {
			out = append(out, pick)
		}
	}
	return out, nil
}

// bestOfGroup is the loop body of smart_discography_filter for one title.
func bestOfGroup(albums []*pyDict, requested any, saveSpace, skipExtras bool) (*pyDict, error) {
	depths := make([]any, len(albums))
	for i, a := range albums {
		d, err := a.item("maximum_bit_depth")
		if err != nil {
			return nil, err
		}
		depths[i] = d
	}
	bestDepth, err := pyMinMax(depths, true)
	if err != nil {
		return nil, err
	}
	var rates []any
	for i, a := range albums {
		if pyEqual(depths[i], bestDepth) {
			r, err := a.item("maximum_sampling_rate")
			if err != nil {
				return nil, err
			}
			rates = append(rates, r)
		}
	}
	bestRate, err := pyMinMax(rates, !saveSpace)
	if err != nil {
		return nil, err
	}
	remasterExists := false
	for _, a := range albums {
		if isReleaseType(reRemaster, a) {
			remasterExists = true
			break
		}
	}
	// tuple(filter(is_valid, albums)) checks every album, so a later album's
	// KeyError still surfaces.
	var pick *pyDict
	for _, a := range albums {
		ok, err := isValidRelease(a, bestDepth, bestRate, requested, remasterExists, skipExtras)
		if err != nil {
			return nil, err
		}
		if ok && pick == nil {
			pick = a
		}
	}
	return pick, nil
}

// isValidRelease is is_valid inside smart_discography_filter.
func isValidRelease(a *pyDict, bestDepth, bestRate, requested any, remasterExists, skipExtras bool) (bool, error) {
	d, err := a.item("maximum_bit_depth")
	if err != nil || !pyEqual(d, bestDepth) {
		return false, err
	}
	r, err := a.item("maximum_sampling_rate")
	if err != nil || !pyEqual(r, bestRate) {
		return false, err
	}
	artist, err := a.item("artist")
	if err != nil {
		return false, err
	}
	name, err := pyGetItem(artist, "name")
	if err != nil || !pyEqual(name, requested) {
		return false, err
	}
	if remasterExists && !isReleaseType(reRemaster, a) {
		return false, nil
	}
	return !skipExtras || !isReleaseType(reExtra, a), nil
}

// TYPE_REGEXES of smart_discography_filter. Python's IGNORECASE also matches
// i with ı and İ, which RE2's case folding doesn't, so i is spelled out.
var (
	// (?i)(re)?master(ed)?
	reRemaster = regexp.MustCompile(`(?i)(re)?master(ed)?`)
	// (?i)(anniversary|deluxe|live|collector|demo|expanded)
	reExtra = regexp.MustCompile(`(?i)(ann[iı\x{130}]versary|deluxe|l[iı\x{130}]ve|collector|demo|expanded)`)
)

// isReleaseType is is_type: the pattern searched in f"{title} {version}".
func isReleaseType(re *regexp.Regexp, album *pyDict) bool {
	return re.MatchString(pyStr(album.get("title", "")) + " " + pyStr(album.get("version", "")))
}

var (
	// ^\s*(?:\([^)]*\)\s*)+
	reLeadingParens = regexp.MustCompile(`^` + reS + `*(?:\([^)]*\)` + reS + `*)+`)
	// [^\(]+ at the start, as re.match
	reBeforeParen = regexp.MustCompile(`^[^(]+`)
)

// essence is essence inside smart_discography_filter: the title without
// leading parenthesized groups, up to the first "(", stripped and lowered.
// A title that leaves nothing falls back to the whole title.
func essence(v any) (string, error) {
	album, ok := v.(string)
	if !ok {
		return "", pyErr(pyTypeError, "expected string or bytes-like object, got '%s'", pyTypeName(v))
	}
	title := reLeadingParens.ReplaceAllString(album, "")
	r := reBeforeParen.FindString(title)
	if r == "" {
		r = album
	}
	return pyLower(pyStrip(r, "")), nil
}

// FormatDuration is format_duration: seconds as HH:MM:SS through
// time.gmtime, so it wraps at 24 hours.
func FormatDuration(seconds int) string {
	s := ((seconds % 86400) + 86400) % 86400
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s/60%60, s%60)
}

// createAndReturnDir is create_and_return_dir: ~ expanded, made absolute and
// normalized as Python's os.path does, and created 0755.
func createAndReturnDir(dir string) (string, error) {
	p := expandUser(dir)
	if !strings.HasPrefix(p, "/") {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// os.path.join(cwd, p)
		if !strings.HasSuffix(cwd, "/") {
			cwd += "/"
		}
		p = cwd + p
	}
	p = posixNormpath(p)
	if err := makeDirs(p); err != nil {
		return "", err
	}
	return p, nil
}

// expandUser is posixpath.expanduser: ~ is $HOME, or the user database's home
// when HOME is unset; ~name is that user's home; an unknown user leaves the
// path as it is.
func expandUser(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	i := strings.IndexByte(p[1:], '/') + 1
	if i == 0 {
		i = len(p)
	}
	var home string
	if i == 1 {
		h, ok := os.LookupEnv("HOME")
		if !ok {
			u, err := user.Current()
			if err != nil {
				return p
			}
			h = u.HomeDir
		}
		home = h
	} else {
		u, err := user.Lookup(p[1:i])
		if err != nil {
			return p
		}
		home = u.HomeDir
	}
	home = strings.TrimRight(home, "/")
	if out := home + p[i:]; out != "" {
		return out
	}
	return "/"
}

// m3uFile is one audio file make_m3u found, with what it read from it.
// Python's None and "" are both "" here, since only truthiness is used.
type m3uFile struct {
	rel, name     string // the path relative to the folder, and the base name
	title, artist string
	isrc, qobuzID string
	duration      int64
}

// MakeM3U ports make_m3u: it writes <folder name>.m3u8 into dir, listing
// the FLAC and MP3 files under it in the order of items, the playlist's
// tracks, matched by track ID, ISRC, title, then file name. Without items,
// or when none matched, the files go in natural order. It returns the file
// written, or "" when dir holds no audio. Unlike Python, which takes the
// file system's order, files are listed by name.
func MakeM3U(dir string, items []*pyDict) (string, error) {
	return makeM3U(slog.New(slog.DiscardHandler), dir, items)
}

func makeM3U(log *slog.Logger, dir string, items []*pyDict) (string, error) {
	var files []*m3uFile
	walkAudio(dir, "", func(rel, name string) {
		files = append(files, readM3UFile(log, pyPathJoin(dir, rel), rel, name))
	})

	var ordered []*m3uFile
	if len(items) > 0 {
		var err error
		if ordered, err = matchM3U(log, files, items); err != nil {
			return "", err
		}
	}
	if len(items) == 0 || len(ordered) == 0 {
		keys := make(map[*m3uFile][]natPart, len(files))
		for _, f := range files {
			k, err := naturalKey(f.name)
			if err != nil {
				return "", err
			}
			keys[f] = k
		}
		ordered = slices.Clone(files)
		slices.SortStableFunc(ordered, func(a, b *m3uFile) int { return compareNatural(keys[a], keys[b]) })
	}

	lines := []string{"#EXTM3U"}
	for _, f := range ordered {
		title, artist := cmpOr(f.title, "Unknown Title"), cmpOr(f.artist, "Unknown Artist")
		lines = append(lines, fmt.Sprintf("#EXTINF:%d, %s - %s\n%s", f.duration, artist, title, f.rel))
	}
	if len(lines) == 1 {
		return "", nil
	}
	_, base := pySplit(posixNormpath(dir))
	path := pyPathJoin(dir, base+".m3u8")
	if err := writeFile(path, []byte(strings.Join(lines, "\n"))); err != nil {
		return "", err
	}
	return path, nil
}

// walkAudio is make_m3u's os.walk: each folder's .mp3 and .flac files by
// name, then its subfolders by name, not following symlinks to folders.
// Folders that can't be read are passed over, as os.walk does.
func walkAudio(dir, rel string, visit func(rel, name string)) {
	entries, err := os.ReadDir(pyPathJoin(dir, rel))
	if err != nil {
		return
	}
	var dirs []string
	for _, e := range entries {
		full := pyPathJoin(pyPathJoin(dir, rel), e.Name())
		if isDir(full) {
			if e.Type()&os.ModeSymlink == 0 {
				dirs = append(dirs, e.Name())
			}
			continue
		}
		if _, ext := pySplitext(e.Name()); ext != "" {
			if ext = pyLower(ext); ext == ".mp3" || ext == ".flac" {
				visit(joinRel(rel, e.Name()), e.Name())
			}
		}
	}
	for _, d := range dirs {
		walkAudio(dir, joinRel(rel, d), visit)
	}
}

func joinRel(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

// readM3UFile is the body of make_m3u's scan for one file. mutagen.File
// gives the length, but only for a file with tags, since a FileType without
// any is false. Any failure keeps what was read so far and takes the file
// name without its extension as the title.
func readM3UFile(log *slog.Logger, full, rel, name string) *m3uFile {
	f := &m3uFile{rel: rel, name: name}
	var err error
	if strings.HasSuffix(pyLower(full), ".flac") {
		err = readM3UFLAC(full, f)
	} else {
		err = readM3UMP3(full, f)
	}
	if err != nil {
		log.Debug(fmt.Sprintf("Error reading tags for %s: %v", name, err))
		f.title, _ = pySplitext(name)
	}
	return f
}

func readM3UFLAC(full string, f *m3uFile) error {
	b, err := readFLACBlocks(full)
	if err != nil {
		return err
	}
	s := b.streamInfo().body
	rate := int64(s[10])<<12 | int64(s[11])<<4 | int64(s[12])>>4
	samples := int64(s[13]&0x0f)<<32 | int64(binary.BigEndian.Uint32(s[14:18]))
	if rate == 0 {
		return errors.New("a sample rate value of 0 is invalid")
	}
	if b.tags != nil && len(b.tags.comments) > 0 {
		f.duration = int64(float64(samples) / float64(rate))
	}
	first := func(key string) string {
		if b.tags != nil {
			for _, c := range b.tags.comments {
				if strings.EqualFold(c.key, key) {
					return pyStr(c.value)
				}
			}
		}
		return ""
	}
	f.qobuzID, f.isrc, f.title, f.artist = first("QOBUZTRACKID"), first("ISRC"), first("TITLE"), first("ARTIST")
	return nil
}

func readM3UMP3(full string, f *m3uFile) error {
	tag, err := readID3(full)
	if err != nil {
		return err
	}
	length, err := mp3Length(full)
	if err != nil {
		return err
	}
	if tag != nil && tag.Count() > 0 {
		f.duration = int64(length)
	}
	if tag == nil {
		return errors.New("doesn't start with an ID3 tag")
	}
	// mutagen splits a frame's text on NUL into its values; the first is
	// read.
	firstValue := func(s string) string {
		v, _, _ := strings.Cut(s, "\x00")
		return v
	}
	for _, fr := range tag.GetFrames("TXXX") {
		if u, ok := fr.(id3v2.UserDefinedTextFrame); ok && pyUpper(u.Description) == "QOBUZTRACKID" {
			f.qobuzID = firstValue(u.Value)
			break
		}
	}
	text := func(id string) string {
		if len(tag.GetFrames(id)) == 0 {
			return ""
		}
		return firstValue(tag.GetTextFrame(id).Text)
	}
	f.isrc, f.title, f.artist = text("TSRC"), text("TIT2"), text("TPE1")
	return nil
}

// readID3 reads a file's ID3v2 tag, or nil when the file doesn't start with
// one.
func readID3(path string) (*id3v2.Tag, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	var head [3]byte
	_, err = io.ReadFull(fh, head[:])
	_ = fh.Close()
	if err != nil || string(head[:]) != "ID3" {
		return nil, nil
	}
	t, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return nil, err
	}
	_ = t.Close()
	return t, nil
}

// matchM3U is make_m3u's four passes: each item's file by track ID, ISRC,
// stripped lower-case title, then the first file whose name holds the
// lower-case title. A later file wins a clash in the first three. Misses
// are logged as a table.
func matchM3U(log *slog.Logger, files []*m3uFile, items []*pyDict) ([]*m3uFile, error) {
	byTID, byISRC, byTitle := map[string]*m3uFile{}, map[string]*m3uFile{}, map[string]*m3uFile{}
	for _, f := range files {
		if f.qobuzID != "" {
			byTID[f.qobuzID] = f
		}
		if f.isrc != "" {
			byISRC[f.isrc] = f
		}
		if f.title != "" {
			byTitle[pyLower(pyStrip(f.title, ""))] = f
		}
	}
	getDict := func(d *pyDict, key string) (*pyDict, error) {
		v := d.get(key, newDict())
		sub, ok := v.(*pyDict)
		if !ok {
			return nil, pyErr(pyAttributeError, "'%s' object has no attribute 'get'", pyTypeName(v))
		}
		return sub, nil
	}
	var ordered []*m3uFile
	missing := 0
	for _, item := range items {
		tid := pyStr(item.get("id", ""))
		isrc := pyStr(item.get("isrc", ""))
		title := item.get("title", "Unknown Title")
		album, err := getDict(item, "album")
		if err != nil {
			return nil, err
		}
		albumArtist, err := getDict(album, "artist")
		if err != nil {
			return nil, err
		}
		performer, err := getDict(item, "performer")
		if err != nil {
			return nil, err
		}
		finalArtist := albumArtist.get("name", nil)
		if finalArtist == nil || pyEqual(finalArtist, "Various Artists") {
			finalArtist = performer.get("name", "Unknown Artist")
		}

		best := byTID[tid]
		if best == nil {
			best = byISRC[isrc]
		}
		var titleStr string
		if best == nil {
			s, ok := title.(string)
			if !ok {
				return nil, pyErr(pyAttributeError, "'%s' object has no attribute 'strip'", pyTypeName(title))
			}
			titleStr = s
			best = byTitle[pyLower(pyStrip(s, ""))]
		}
		if best == nil && titleStr != "Unknown Title" {
			for _, f := range files {
				if strings.Contains(pyLower(f.name), pyLower(titleStr)) {
					best = f
					break
				}
			}
		}
		if best != nil {
			ordered = append(ordered, best)
			continue
		}
		artist, ok := finalArtist.(string)
		if !ok {
			return nil, pyErr(pyTypeError, "'%s' object is not subscriptable", pyTypeName(finalArtist))
		}
		if missing == 0 {
			log.Warn(m3uMissingHeader)
		}
		log.Warn(fmt.Sprintf("%s │ %s │ %s", pyLjust(pySlice(titleStr, 0, 35), 35), pyLjust(pySlice(artist, 0, 25), 25),
			pyLjust(tid, 12)))
		missing++
	}
	if missing > 0 {
		log.Warn(strings.Repeat("━", 80) + "\n")
	}
	return ordered, nil
}

// m3uMissingHeader is the head of make_m3u's table of missing tracks,
// without its colors.
var m3uMissingHeader = "\n" + strings.Repeat("━", 80) + "\n" + strings.Repeat(" ", 30) + "MISSING LOCAL TRACKS" +
	strings.Repeat(" ", 30) + "\n" + strings.Repeat("━", 80) + "\n" + pyLjust("TITLE", 35) + " │ " +
	pyLjust("ARTIST", 25) + " │ " + pyLjust("ID", 12) + "\n" + strings.Repeat("─", 80)

// natPart is one element of make_m3u's natural sort key: a number or a
// lower-case text.
type natPart struct {
	num    bool
	digits string // the number's decimal value, without leading zeros
	text   string
}

// naturalKey is natural_sort_key: the name split at runs of decimal digits,
// the runs as numbers, the rest in lower case. Text that str.isdigit
// accepts but int() can't read, such as "²", is Python's ValueError.
func naturalKey(s string) ([]natPart, error) {
	var parts []natPart
	rs := []rune(s)
	i := 0
	for {
		j := i
		for j < len(rs) && !pyIsDecimal(rs[j]) {
			j++
		}
		text := string(rs[i:j])
		if pyIsDigit(text) {
			return nil, pyErr(pyValueError, "invalid literal for int() with base 10: %s", pyRepr(text))
		}
		parts = append(parts, natPart{text: pyLower(text)})
		if j == len(rs) {
			return parts, nil
		}
		k := j
		var b strings.Builder
		for k < len(rs) && pyIsDecimal(rs[k]) {
			d, _ := pyDecimalValue(rs[k])
			if d != 0 || b.Len() > 0 {
				b.WriteByte(byte('0' + d))
			}
			k++
		}
		parts = append(parts, natPart{num: true, digits: b.String()})
		i = k
	}
}

// compareNatural compares two keys as Python compares the lists: item by
// item, the shorter first when one is the other's start.
func compareNatural(a, b []natPart) int {
	for i := range min(len(a), len(b)) {
		x, y := a[i], b[i]
		var c int
		if x.num {
			c = cmp.Or(cmp.Compare(len(x.digits), len(y.digits)), strings.Compare(x.digits, y.digits))
		} else {
			c = strings.Compare(x.text, y.text)
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

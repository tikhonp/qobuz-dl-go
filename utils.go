// Ports qobuz_dl/utils.py from qobuz-dl 2.5.9. make_m3u comes with the core
// step; read_config_file, label_matches and PartialFormatter are not ported.

package qobuzdl

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"regexp"
	"strings"
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
	if err := os.MkdirAll(p, 0o755); err != nil {
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

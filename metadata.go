// Ports qobuz_dl/metadata.py from qobuz-dl 2.5.9, with the parts of mutagen
// 1.48.1 the taggers rely on. FLAC blocks are in flac.go; MP3 frames go
// through id3v2 with the frame writers below.

package qobuzdl

import (
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/bogem/id3v2/v2"
	"github.com/go-flac/flacpicture"
)

const (
	copyrightSign     = "\u00a9"
	phonCopyrightSign = "\u2117"
	// flacMaxCoverSize is FLAC_MAX_BLOCKSIZE: a bigger cover can't fit a block.
	flacMaxCoverSize     = flacMaxBlockSize
	embCoverName         = "embed_cover.jpg"
	flacSavePadding      = 8192
	coverTooLargeMsg     = "downloaded cover size too large to embed. lower `embedded_art_size` (e.g. 600) to avoid error"
	musicBrainzAlbumType = "MusicBrainz Album Type"
)

// id3Legend is ID3_LEGEND: tag keys, looked up lower-cased first, to frame IDs.
var id3Legend = map[string]string{
	"albumartist":           "TPE2",
	"album":                 "TALB",
	"artist":                "TPE1",
	"title":                 "TIT2",
	"mediatype":             "TMED",
	"genre":                 "TCON",
	"composer":              "TCOM",
	"itunesadvisory":        "TXXX",
	"copyright":             "TCOP",
	"label":                 "TPUB",
	"barcode":               "TXXX",
	"isrc":                  "TSRC",
	"comment":               "COMM",
	"year":                  "TYER",
	"performer":             "TOPE",
	"compilation":           "TCMP",
	"QOBUZTRACKID":          "TXXX",
	"QOBUZALBUMID":          "TXXX",
	"QOBUZ ALBUM URL":       "TXXX",
	"replaygain_track_gain": "TXXX",
	"replaygain_track_peak": "TXXX",
	"conductor":             "TPE3",
	"ensemble":              "TXXX",
	"work":                  "TIT1",
}

// primaryReleaseTypes are PRIMARY_RELEASE_TYPES, the MusicBrainz primary types.
var primaryReleaseTypes = []string{"album", "single", "ep"}

// localGenreMap is LOCAL_GENRE_MAP: Qobuz's French genre names in English.
var localGenreMap = map[string]string{
	"Électronique":                 "Electronic",
	"Ambiance":                     "Ambient",
	"Classique":                    "Classical",
	"Musique de chambre":           "Chamber Music",
	"Opéra":                        "Opera",
	"Chorale":                      "Choral",
	"Symphonique":                  "Symphonic",
	"Bande Originale":              "Soundtrack",
	"Musique de film":              "Soundtrack",
	"Comédie Musicale":             "Musical",
	"Bande originale de jeu vidéo": "Video Game Soundtrack",
	"Séries TV":                    "TV Series",
	"Bandes originales de films":   "Film Soundtracks",
	"Jeux vidéo":                   "Video Games",
	"Anime/Jeux vidéo":             "Anime/Video Game",
	"Jazz Vocal":                   "Vocal Jazz",
	"Jazz Contemporain":            "Contemporary Jazz",
	"Musiques du monde":            "World",
	"Musique celtique":             "Celtic",
	"Musique latine":               "Latin",
	"Variété Française":            "French Pop",
	"Alternatif et Indé":           "Alternative & Indie",
	"Asie":                         "Asia",
	"Musique indienne":             "Indian Music",
	"Russie":                       "Russia",
	"Enfants":                      "Children's Music",
	"Berceuses":                    "Lullabies",
	"Poésie et Littérature":        "Spoken Word",
	"Livres Audio":                 "Audiobooks",
	"Humour":                       "Comedy",
	"Religieux":                    "Religious",
	"Détente":                      "Relaxation",
	"Fêtes":                        "Holiday",
	"Musiques de Noël":             "Christmas Music",
	"Rock progressif":              "Progressive Rock",
}

var (
	// \s+ in the LABEL tag, with Python's Unicode \s.
	reTagSpaces = regexp.MustCompile(reS + `+`)
	// [^a-z0-9]+ in the album URL's slug.
	reNotSlug = regexp.MustCompile(`[^a-z0-9]+`)
)

// noAttr is the AttributeError of calling a str method on another type.
func noAttr(v any, name string) error {
	return pyErr(pyAttributeError, "'%s' object has no attribute '%s'", pyTypeName(v), name)
}

// strForMethod is v used as the str whose method name is called.
func strForMethod(v any, name string) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", noAttr(v, name)
	}
	return s, nil
}

// getTitleWithVersion is _get_title_with_version: "title (version)" unless
// the version is already in the title, ignoring case.
func getTitleWithVersion(title, version any) (any, error) {
	if !pyTruthy(version) {
		return title, nil
	}
	v, err := strForMethod(version, "lower")
	if err != nil {
		return nil, err
	}
	t, err := strForMethod(title, "lower")
	if err != nil {
		return nil, err
	}
	if strings.Contains(pyLower(t), pyLower(v)) {
		return title, nil
	}
	return t + " (" + v + ")", nil
}

// metadataGetTitle is metadata.py's _get_title, which nothing upstream calls:
// the title with its version and the classical work in front.
func metadataGetTitle(track *pyDict) (any, error) {
	title, err := track.item("title")
	if err != nil {
		return nil, err
	}
	if version := track.get("version", nil); pyTruthy(version) {
		title = pyStr(title) + " (" + pyStr(version) + ")"
	}
	if work := track.get("work", nil); pyTruthy(work) {
		title = pyStr(work) + ": " + pyStr(title)
	}
	return title, nil
}

// isVariousArtists is _is_various_artists: whether an album artist is one
// of the aliases.
func isVariousArtists(log *slog.Logger, album *pyDict, aliases []string) (bool, error) {
	names, err := getAlbumArtist(log, album)
	if err != nil {
		return false, err
	}
	for _, name := range names {
		if slices.Contains(aliases, pyCasefold(pyStrip(pyStr(name), ""))) {
			return true, nil
		}
	}
	return false, nil
}

// releaseTypes is _release_types: the primary type, then "compilation" for
// compilations, with "album" in front when there is no primary type.
func releaseTypes(log *slog.Logger, album *pyDict, aliases []string) ([]any, error) {
	v := album.get("release_type", nil)
	if !pyTruthy(v) {
		v = album.get("product_type", nil)
	}
	if !pyTruthy(v) {
		v = ""
	}
	raw := pyLower(pyStr(v))
	types := []any{}
	if slices.Contains(primaryReleaseTypes, raw) {
		types = append(types, raw)
	}
	various := raw == "compilation"
	if !various {
		var err error
		if various, err = isVariousArtists(log, album, aliases); err != nil {
			return nil, err
		}
	}
	if various {
		if len(types) == 0 {
			types = append(types, "album")
		}
		types = append(types, "compilation")
	}
	return types, nil
}

// discTrackTotal is _disc_track_total: the number of tracks on the item's
// disc when the album has several discs and its whole track list, else the
// album's track count.
func discTrackTotal(album any, item *pyDict) (string, error) {
	a, err := asDict(album)
	if err != nil {
		return "", err
	}
	total := a.get("tracks_count", nil)
	if !pyTruthy(total) {
		total = int64(1)
	}
	tracks := a.get("tracks", nil)
	if !pyTruthy(tracks) {
		tracks = newDict()
	}
	td, err := asDict(tracks)
	if err != nil {
		return "", err
	}
	items := td.get("items", nil)
	if !pyTruthy(items) {
		items = []any{}
	}
	discs := a.get("media_count", nil)
	if !pyTruthy(discs) {
		discs = int64(1)
	}
	several, err := pyLess(int64(1), discs)
	if err != nil {
		return "", pyErr(pyTypeError, "'>' not supported between instances of '%s' and 'int'", pyTypeName(discs))
	}
	if several {
		n, err := pyLenValue(items)
		if err != nil {
			return "", err
		}
		if pyEqual(n, total) {
			disc := item.get("media_number", int64(1))
			list, err := pyIter(items)
			if err != nil {
				return "", err
			}
			onDisc := 0
			for _, t := range list {
				td, err := asDict(t)
				if err != nil {
					return "", err
				}
				if pyEqual(td.get("media_number", int64(1)), disc) {
					onDisc++
				}
			}
			if onDisc > 0 {
				return strconv.Itoa(onDisc), nil
			}
		}
	}
	return pyStr(total), nil
}

// formatCopyright is _format_copyright: (P) and (C) as ℗ and ©.
func formatCopyright(s any) (any, error) {
	if !pyTruthy(s) {
		return s, nil
	}
	t, err := strForMethod(s, "replace")
	if err != nil {
		return nil, err
	}
	t = strings.ReplaceAll(t, "(P)", phonCopyrightSign)
	return strings.ReplaceAll(t, "(C)", copyrightSign), nil
}

// formatGenres is _format_genres, which nothing upstream calls: the genres
// split on → and /, without repeats, joined with ", ".
func formatGenres(genres []any) (string, error) {
	strs, err := pyJoinItems(genres)
	if err != nil {
		return "", err
	}
	var out []string
	for _, g := range strings.FieldsFunc(strings.Join(strs, "/"), func(r rune) bool { return r == '→' || r == '/' }) {
		if !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return strings.Join(out, ", "), nil
}

// pyJoinItems checks that every item can go into str.join.
func pyJoinItems(items []any) ([]string, error) {
	out := make([]string, len(items))
	for i, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, pyErr(pyTypeError, "sequence item %d: expected str instance, %s found", i, pyTypeName(it))
		}
		out[i] = s
	}
	return out, nil
}

// performerCredits splits item["performers"], such as "Name, Role, Role -
// Name, Role", into name and roles. Blocks with a name only are left out,
// as every loop over them upstream skips them.
func performerCredits(item *pyDict) ([][]string, error) {
	v := item.get("performers", "")
	if !pyTruthy(v) {
		return nil, nil
	}
	s, err := strForMethod(v, "split")
	if err != nil {
		return nil, err
	}
	var out [][]string
	for _, block := range strings.Split(s, " - ") {
		parts := strings.Split(block, ", ")
		if len(parts) < 2 {
			continue
		}
		for i := range parts {
			parts[i] = pyStrip(parts[i], "")
		}
		out = append(out, parts)
	}
	return out, nil
}

// hasRole is `any(role in roles for role in want)`.
func hasRole(roles []string, want ...string) bool {
	for _, w := range want {
		if slices.Contains(roles, w) {
			return true
		}
	}
	return false
}

// oneOrList is the tag shape upstream uses: one value alone, several as a
// list, none as "".
func oneOrList(vals []any) any {
	switch len(vals) {
	case 0:
		return ""
	case 1:
		return vals[0]
	}
	return vals
}

// getTagsToAdd is _get_tags_to_add: the tags for a track as an ordered dict,
// each value a string, a list or whatever the API held. Each key is left out
// when its no_*_tag setting is on.
func getTagsToAdd(log *slog.Logger, album any, item *pyDict, s *Settings) (*pyDict, error) {
	tags := newDict()
	if !pyTruthy(album) || item == nil || !pyTruthy(item) {
		return tags, nil
	}
	// Python raises at the first album.get; that happens by DATE at the latest.
	a, err := asDict(album)
	if err != nil {
		return nil, err
	}

	if !s.NoAlbumTitleTag {
		v, err := getTitleWithVersion(a.get("title", ""), a.get("version", ""))
		if err != nil {
			return nil, err
		}
		tags.set("ALBUM", v)
	}
	if !s.NoTrackTitleTag {
		v, err := getTitleWithVersion(item.get("title", ""), item.get("version", ""))
		if err != nil {
			return nil, err
		}
		tags.set("TITLE", v)
	}

	if !s.NoAlbumArtistTag {
		names, err := getAlbumArtist(log, a)
		if err != nil {
			return nil, err
		}
		tags.set("ALBUMARTIST", append([]any{}, names...))
	}
	if !s.NoTrackArtistTag {
		v, err := artistTag(a, item)
		if err != nil {
			return nil, err
		}
		tags.set("ARTIST", v)
	}
	if !s.NoComposerTag {
		v, err := composerTag(item)
		if err != nil {
			return nil, err
		}
		tags.set("COMPOSER", v)
	}

	releaseDate := a.get("release_date_original", "")
	if !s.NoReleaseDateTag {
		tags.set("DATE", releaseDate)
	}
	if !s.NoGenreTag {
		v, err := genreTag(a)
		if err != nil {
			return nil, err
		}
		tags.set("GENRE", v)
	}
	if !s.NoCopyrightTag {
		v, err := formatCopyright(a.get("copyright", "n/a"))
		if err != nil {
			return nil, err
		}
		tags.set("COPYRIGHT", v)
	}
	if !s.NoLabelTag {
		label, err := a.dict("label")
		if err != nil {
			return nil, err
		}
		name := label.get("name", "")
		ns, ok := name.(string)
		if !ok {
			return nil, pyErr(pyTypeError, "expected string or bytes-like object, got '%s'", pyTypeName(name))
		}
		tags.set("LABEL", reTagSpaces.ReplaceAllString(ns, " "))
	}
	if !s.NoISRCTag {
		tags.set("ISRC", item.get("isrc", ""))
	}
	if !s.NoUPCTag {
		tags.set("BARCODE", a.get("upc", ""))
	}

	if !s.NoMediaTypeTag {
		pt, err := strForMethod(a.get("product_type", ""), "upper")
		if err != nil {
			return nil, err
		}
		tags.set("MEDIATYPE", pyUpper(pt))
	}
	if !s.NoExplicitTag {
		explicit := ""
		if pyTruthy(item.get("parental_warning", false)) {
			explicit = "1"
		}
		tags.set("ITUNESADVISORY", explicit)
	}

	aliases := s.VariousArtistsAliases
	various, err := isVariousArtists(log, a, aliases)
	if err != nil {
		return nil, err
	}
	if various {
		tags.set("COMPILATION", "1")
	}
	types, err := releaseTypes(log, a, aliases)
	if err != nil {
		return nil, err
	}
	if len(types) > 0 {
		tags.set("RELEASETYPE", types)
	}

	if !s.NoReplayGainTag {
		if info := item.get("audio_info", newDict()); pyTruthy(info) {
			d, err := asDict(info)
			if err != nil {
				return nil, err
			}
			if gain := d.get("replaygain_track_gain", nil); gain != nil {
				tags.set("REPLAYGAIN_TRACK_GAIN", pyStr(gain)+" dB")
			}
			if peak := d.get("replaygain_track_peak", nil); peak != nil {
				tags.set("REPLAYGAIN_TRACK_PEAK", pyStr(peak))
			}
		}
	}

	if work := item.get("work", nil); pyTruthy(work) && !s.NoWorkTag {
		tags.set("WORK", work)
	}
	credits, err := performerCredits(item)
	if err != nil {
		return nil, err
	}
	var conductors, ensembles []any
	for _, c := range credits {
		if hasRole(c[1:], "Conductor") {
			conductors = append(conductors, c[0])
		}
		if hasRole(c[1:], "Orchestra", "Ensemble", "Choir") {
			ensembles = append(ensembles, c[0])
		}
	}
	if len(conductors) > 0 && !s.NoConductorTag {
		tags.set("CONDUCTOR", oneOrList(conductors))
	}
	if len(ensembles) > 0 && !s.NoEnsembleTag {
		tags.set("ENSEMBLE", oneOrList(ensembles))
	}

	if id := item.get("id", nil); pyTruthy(id) {
		tags.set("QOBUZTRACKID", pyStr(id))
	}
	albumID := a.get("id", nil)
	if pyTruthy(albumID) {
		tags.set("QOBUZALBUMID", pyStr(albumID))
	}
	if !s.NoAlbumURLTag && pyTruthy(albumID) {
		title := pyStr(a.get("title", "album"))
		slug := strings.Trim(reNotSlug.ReplaceAllString(pyLower(title), "-"), "-")
		tags.set("QOBUZ ALBUM URL", "https://www.qobuz.com/album/"+slug+"/"+pyStr(albumID))
	}
	return tags, nil
}

// artistTag is the ARTIST part of _get_tags_to_add: the performer, then the
// main and featured artists from the credits, without a combined credit
// such as "A & B" when A and B are listed too.
func artistTag(album, item *pyDict) (any, error) {
	performer, err := item.dict("performer")
	if err != nil {
		return nil, err
	}
	main := performer.get("name", "")
	if !pyTruthy(main) {
		artist, err := album.dict("artist")
		if err != nil {
			return nil, err
		}
		main = artist.get("name", "")
	}
	var artists []any
	if pyTruthy(main) {
		artists = append(artists, main)
	}
	credits, err := performerCredits(item)
	if err != nil {
		return nil, err
	}
	for _, c := range credits {
		name, roles := c[0], c[1:]
		// "Earth, Wind & Fire, MainArtist" reads as "Earth": skip such
		// cut copies of the main artist's name.
		if pyTruthy(main) {
			m, err := strForMethod(main, "casefold")
			if err != nil {
				return nil, err
			}
			if strings.HasPrefix(pyCasefold(m), pyCasefold(name)+",") {
				continue
			}
		}
		if !hasRole(roles, "FeaturedArtist", "MainArtist") {
			continue
		}
		listed := false
		for _, x := range artists {
			xs, err := strForMethod(x, "casefold")
			if err != nil {
				return nil, err
			}
			if pyCasefold(xs) == pyCasefold(name) {
				listed = true
				break
			}
		}
		if !listed {
			artists = append(artists, name)
		}
	}
	if pyTruthy(main) && len(artists) > 1 {
		m, err := strForMethod(main, "replace")
		if err != nil {
			return nil, err
		}
		parts := map[string]bool{}
		for _, p := range strings.Split(strings.ReplaceAll(m, " & ", ","), ",") {
			if p = pyStrip(p, ""); p != "" {
				parts[pyCasefold(p)] = true
			}
		}
		others := map[string]bool{}
		for _, x := range artists[1:] {
			others[pyCasefold(x.(string))] = true
		}
		subset := true
		for p := range parts {
			subset = subset && others[p]
		}
		if len(parts) > 1 && subset {
			// list.remove takes the first equal item, which is main itself.
			artists = artists[1:]
		}
	}
	return oneOrList(artists), nil
}

// composerTag is the COMPOSER part of _get_tags_to_add.
func composerTag(item *pyDict) (any, error) {
	credits, err := performerCredits(item)
	if err != nil {
		return nil, err
	}
	var composers []any
	for _, c := range credits {
		if !hasRole(c[1:], "Composer", "ComposerLyricist") {
			continue
		}
		listed := slices.ContainsFunc(composers, func(x any) bool { return pyCasefold(x.(string)) == pyCasefold(c[0]) })
		if !listed {
			composers = append(composers, c[0])
		}
	}
	if len(composers) == 0 {
		composer, err := item.dict("composer")
		if err != nil {
			return nil, err
		}
		if name := composer.get("name", ""); pyTruthy(name) {
			composers = append(composers, name)
		}
	}
	return oneOrList(composers), nil
}

// genreTag is the GENRE part of _get_tags_to_add. Python writes the main
// genre into the album's genres_list; every call writes the same value, so
// a copy gives the same tags without a data race between the workers that
// tag one album's tracks.
func genreTag(album *pyDict) (any, error) {
	genre, err := album.dict("genre")
	if err != nil {
		return nil, err
	}
	var main any
	if raw := genre.get("name", nil); pyTruthy(raw) {
		switch t := raw.(type) {
		case []any, *pyDict:
			return nil, pyErr(pyTypeError, "unhashable type: '%s'", pyTypeName(raw))
		case string:
			main = t
			if m, ok := localGenreMap[t]; ok {
				main = m
			}
		default:
			main = raw
		}
	}
	rawGenres := album.get("genres_list", []any{})
	if pyTruthy(main) {
		if pyTruthy(rawGenres) {
			switch t := rawGenres.(type) {
			case []any:
				t = slices.Clone(t)
				t[0] = main
				rawGenres = t
			case *pyDict:
				// d[0] = main adds an int key, which the join below refuses.
				return nil, pyErr(pyTypeError, "sequence item %d: expected str instance, int found", len(t.keys))
			default:
				return nil, pyErr(pyTypeError, "'%s' object does not support item assignment", pyTypeName(rawGenres))
			}
		} else {
			rawGenres = []any{main}
		}
	}
	items, err := pyIter(rawGenres)
	if err != nil {
		return nil, pyErr(pyTypeError, "can only join an iterable")
	}
	strs, err := pyJoinItems(items)
	if err != nil {
		return nil, err
	}
	var final []string
	for _, g := range strings.Split(strings.Join(strs, " → "), "→") {
		if g == "" {
			continue
		}
		clean := pyStrip(g, "")
		if t, ok := localGenreMap[clean]; ok {
			clean = t
		}
		if !slices.Contains(final, clean) {
			final = append(final, clean)
		}
	}
	return strings.Join(final, ", "), nil
}

// tagAlbum is the album tag_flac and tag_mp3 work with: the track's own
// album for a single track, else the album given.
func tagAlbum(d, album *pyDict, isTrack bool) any {
	if isTrack {
		return d.get("album", newDict())
	}
	if album == nil {
		return nil
	}
	return album
}

// TagFLAC is tag_flac: it writes the tags into filename, embeds the cover
// with emImage, and renames the file to finalName. d is the track; album is
// the album, or unused for a single track, which carries its own.
func TagFLAC(log *slog.Logger, filename, rootDir, finalName string, d, album *pyDict, isTrack, emImage bool, s *Settings) error {
	audio, err := readFLACBlocks(filename)
	if err != nil {
		return err
	}
	qAlbum := tagAlbum(d, album, isTrack)
	tags, err := getTagsToAdd(log, qAlbum, d, s)
	if err != nil {
		return err
	}
	if !s.NoTrackNumberTag {
		tags.set("TRACKNUMBER", pyStr(d.get("track_number", "1")))
	}
	if !s.NoTrackTotalTag {
		total, err := discTrackTotal(qAlbum, d)
		if err != nil {
			return err
		}
		tags.set("TRACKTOTAL", total)
	}
	if !s.NoDiscNumberTag {
		tags.set("DISCNUMBER", pyStr(d.get("media_number", "1")))
	}
	if !s.NoDiscTotalTag {
		a, err := asDict(qAlbum)
		if err != nil {
			return err
		}
		tags.set("DISCTOTAL", pyStr(a.get("media_count", "1")))
	}

	for _, k := range tags.keys {
		v := tags.vals[k]
		if !pyTruthy(v) {
			continue
		}
		if g, ok := v.(string); ok && s.MultiValueTags && k == "GENRE" && strings.Contains(g, ", ") {
			var parts []any
			for _, p := range strings.Split(g, ", ") {
				parts = append(parts, p)
			}
			v = parts
		}
		audio.setTag(k, v)
	}
	if emImage {
		embedFLACImg(log, rootDir, audio)
	}
	for _, junk := range []string{"ENCODER", "ENCODED-BY", "ENCODED_BY"} {
		if audio.hasTag(junk) {
			audio.delTag(junk)
		}
	}
	audio.setVendor("")
	if err := audio.save(filename, flacSavePadding); err != nil {
		return err
	}
	return os.Rename(filename, finalName)
}

// embedCoverPath is where the taggers look for the cover: embed_cover.jpg in
// rootDir, else in its parent, for multi-disc albums.
func embedCoverPath(rootDir string) string {
	emb := pyPathJoin(rootDir, embCoverName)
	if isFile(emb) {
		return emb
	}
	return pyPathJoin(pyAbspath(pyPathJoin(rootDir, "..")), embCoverName)
}

// embedFLACImg is _embed_flac_img: a missing cover is logged at debug level,
// any other failure as an error, and neither fails the tagging.
func embedFLACImg(log *slog.Logger, rootDir string, audio *flacBlocks) {
	cover := embedCoverPath(rootDir)
	if !isFile(cover) {
		log.Debug("Cover image not found to embed: " + cover)
		return
	}
	fail := func(msg string) { log.Error("Error embedding image: " + msg) }
	st, err := os.Stat(cover)
	if err != nil {
		fail(err.Error())
		return
	}
	if st.Size() > flacMaxCoverSize {
		fail(coverTooLargeMsg)
		return
	}
	data, err := os.ReadFile(cover)
	if err != nil {
		fail(err.Error())
		return
	}
	audio.addPicture(&flacpicture.MetadataBlockPicture{
		PictureType: flacpicture.PictureTypeFrontCover,
		MIME:        "image/jpeg",
		Description: "cover",
		ImageData:   data,
	})
}

// embedID3Img is _embed_id3_img. Unlike the FLAC one it has no size limit
// and its errors fail the tagging.
func embedID3Img(log *slog.Logger, rootDir string, t *id3v2.Tag) error {
	cover := embedCoverPath(rootDir)
	if !isFile(cover) {
		log.Debug("Cover image not found to embed: " + cover)
		return nil
	}
	data, err := os.ReadFile(cover)
	if err != nil {
		return err
	}
	t.AddFrame("APIC", id3Picture{enc: id3UTF16, mime: "image/jpeg", ptype: 3, data: data})
	return nil
}

// TagMP3 is tag_mp3: an ID3v2.3 tag with the track's tags, through id3v2,
// then the rename to finalName.
func TagMP3(log *slog.Logger, filename, rootDir, finalName string, d, album *pyDict, isTrack, emImage bool, s *Settings) error {
	t, err := id3v2.Open(filename, id3v2.Options{Parse: true})
	if err != nil {
		return err
	}
	// Save reopens the file, so Close is called again after it.
	defer func() { _ = t.Close() }()
	t.SetVersion(3)
	adoptID3Frames(t)

	qAlbum := tagAlbum(d, album, isTrack)
	tags, err := getTagsToAdd(log, qAlbum, d, s)
	if err != nil {
		return err
	}

	// ID3v2.3 keeps the year in TYER and the day and month, DDMM, in TDAT.
	date := tags.pop("DATE", "")
	if !pyTruthy(date) {
		date = ""
	}
	rd, err := releaseDateString(date)
	if err != nil {
		return err
	}
	if pyIsDigit(pySlice(rd, 0, 4)) {
		t.AddFrame("TYER", id3TextFrom(pySlice(rd, 0, 4)))
		if pyIsDigit(pySlice(rd, 5, 7)) && pyIsDigit(pySlice(rd, 8, 10)) {
			t.AddFrame("TDAT", id3TextFrom(pySlice(rd, 8, 10)+pySlice(rd, 5, 7)))
		}
	}

	// Navidrome and Picard read an MP3's release type from this frame.
	if types := tags.pop("RELEASETYPE", nil); pyTruthy(types) {
		strs, err := pyJoinItems(types.([]any))
		if err != nil {
			return err
		}
		t.AddFrame("TXXX", id3UserText{enc: id3UTF16, desc: musicBrainzAlbumType, values: splitNUL(strings.Join(strs, ","))})
	}

	for _, k := range tags.keys {
		v := tags.vals[k]
		if !pyTruthy(v) {
			continue
		}
		id, ok := id3Legend[pyLower(k)]
		if !ok {
			id, ok = id3Legend[k]
		}
		if !ok {
			continue
		}
		values, err := id3TextValues(v)
		if err != nil {
			return err
		}
		if id == "TXXX" {
			t.AddFrame(id, id3UserText{enc: id3UTF16, desc: k, values: values})
		} else {
			t.AddFrame(id, id3Text{enc: id3UTF16, values: values})
		}
	}

	if !s.NoTrackNumberTag {
		track := pyStr(d.get("track_number", "1"))
		if !s.NoTrackTotalTag {
			total, err := discTrackTotal(qAlbum, d)
			if err != nil {
				return err
			}
			track += "/" + total
		}
		t.AddFrame("TRCK", id3TextFrom(track))
	}
	if !s.NoDiscNumberTag {
		disc := pyStr(d.get("media_number", "1"))
		if !s.NoDiscTotalTag {
			a, err := asDict(qAlbum)
			if err != nil {
				return err
			}
			disc += "/" + pyStr(a.get("media_count", "1"))
		}
		t.AddFrame("TPOS", id3TextFrom(disc))
	}

	if emImage {
		if err := embedID3Img(log, rootDir, t); err != nil {
			return err
		}
	}
	t.DeleteFrames("TENC")
	t.DeleteFrames("TSSE")
	dropEmptyID3Frames(t)
	if err := t.Save(); err != nil {
		return err
	}
	if err := t.Close(); err != nil {
		return err
	}
	// id3v2 saves through a new file, which takes the umask's mode.
	if err := os.Chmod(filename, 0o644); err != nil {
		return err
	}
	return os.Rename(filename, finalName)
}

// releaseDateString is the DATE tag as tag_mp3 slices it; release_date[:4]
// fails on anything but a string, each type in its own way.
func releaseDateString(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case []any:
		return "", noAttr(v, "isdigit")
	case *pyDict:
		return "", pyErr(pyKeyError, "slice(None, 4, None)")
	}
	return "", pyErr(pyTypeError, "'%s' object is not subscriptable", pyTypeName(v))
}

// id3TextValues is mutagen's MultiSpec validation of a text frame's text: a
// str splits on NUL, a list's items go through str(), and anything else is
// a ValueError.
func id3TextValues(v any) ([]string, error) {
	switch t := v.(type) {
	case string:
		return splitNUL(t), nil
	case []any:
		out := make([]string, len(t))
		for i, x := range t {
			out[i] = pyStr(x)
		}
		return out, nil
	}
	return nil, pyErr(pyValueError, "Invalid MultiSpec data: %s", pyRepr(v))
}

func splitNUL(s string) []string { return strings.Split(s, "\x00") }

func id3TextFrom(s string) id3Text { return id3Text{enc: id3UTF16, values: splitNUL(s)} }

// ID3 text encodings. ID3v2.3 has no UTF-8, so mutagen writes the frames it
// creates as UTF-16 and turns other encodings into it on save.
const (
	id3Latin1 byte = 0
	id3UTF16  byte = 1
)

// id3v2's UTF-16 writer puts a big-endian BOM on the text and an extra zero
// byte after it, which shifts the value of a TXXX frame and the data of an
// APIC frame by one byte. These frame types write what mutagen writes: a
// little-endian BOM, the text, and a terminator after each string. Lists of
// values are joined with "/", as mutagen does for v2.3.

// id3Encode is a string in enc with its terminator.
func id3Encode(enc byte, s string) []byte {
	if enc == id3Latin1 {
		out := make([]byte, 0, len(s)+1)
		for _, r := range s {
			if r > 0xff {
				r = '?'
			}
			out = append(out, byte(r))
		}
		return append(out, 0)
	}
	u := utf16.Encode([]rune(s))
	out := make([]byte, 2, 2*len(u)+4)
	out[0], out[1] = 0xff, 0xfe
	for _, c := range u {
		out = binary.LittleEndian.AppendUint16(out, c)
	}
	return append(out, 0, 0)
}

// id3TextEncoding is the encoding mutagen keeps on a v2.3 save: Latin-1, or
// UTF-16 for anything else.
func id3TextEncoding(e id3v2.Encoding) byte {
	if e.Equals(id3v2.EncodingISO) {
		return id3Latin1
	}
	return id3UTF16
}

func writeID3Parts(w io.Writer, parts ...[]byte) (int64, error) {
	var n int64
	for _, p := range parts {
		m, err := w.Write(p)
		n += int64(m)
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// id3Text is a text frame other than TXXX.
type id3Text struct {
	enc    byte
	values []string
}

func (f id3Text) body() []byte {
	return append([]byte{f.enc}, id3Encode(f.enc, strings.Join(f.values, "/"))...)
}
func (f id3Text) Size() int                          { return len(f.body()) }
func (f id3Text) UniqueIdentifier() string           { return "" }
func (f id3Text) WriteTo(w io.Writer) (int64, error) { return writeID3Parts(w, f.body()) }

// id3UserText is TXXX; one per description.
type id3UserText struct {
	enc    byte
	desc   string
	values []string
}

func (f id3UserText) body() []byte {
	b := append([]byte{f.enc}, id3Encode(f.enc, f.desc)...)
	return append(b, id3Encode(f.enc, strings.Join(f.values, "/"))...)
}
func (f id3UserText) Size() int                          { return len(f.body()) }
func (f id3UserText) UniqueIdentifier() string           { return f.desc }
func (f id3UserText) WriteTo(w io.Writer) (int64, error) { return writeID3Parts(w, f.body()) }

// id3LangText is COMM or USLT: a language, a description and the text.
type id3LangText struct {
	enc    byte
	lang   string
	desc   string
	values []string
}

func (f id3LangText) body() []byte {
	b := append([]byte{f.enc}, []byte(f.lang)...)
	b = append(b, id3Encode(f.enc, f.desc)...)
	return append(b, id3Encode(f.enc, strings.Join(f.values, "/"))...)
}
func (f id3LangText) Size() int                          { return len(f.body()) }
func (f id3LangText) UniqueIdentifier() string           { return f.lang + f.desc }
func (f id3LangText) WriteTo(w io.Writer) (int64, error) { return writeID3Parts(w, f.body()) }

// id3Picture is APIC; one per description, as mutagen keys them.
type id3Picture struct {
	enc   byte
	mime  string
	ptype byte
	desc  string
	data  []byte
}

func (f id3Picture) head() []byte {
	b := append([]byte{f.enc}, id3Encode(id3Latin1, f.mime)...)
	b = append(b, f.ptype)
	return append(b, id3Encode(f.enc, f.desc)...)
}
func (f id3Picture) Size() int                { return len(f.head()) + len(f.data) }
func (f id3Picture) UniqueIdentifier() string { return f.desc }
func (f id3Picture) WriteTo(w io.Writer) (int64, error) {
	return writeID3Parts(w, f.head(), f.data)
}

// adoptID3Frames swaps the frames id3v2 read for the writers above, so they
// are saved as mutagen saves them, in their order.
func adoptID3Frames(t *id3v2.Tag) {
	for id, frames := range t.AllFrames() {
		adopted := make([]id3v2.Framer, len(frames))
		for i, f := range frames {
			adopted[i] = adoptID3Frame(f)
		}
		t.DeleteFrames(id)
		for _, f := range adopted {
			t.AddFrame(id, f)
		}
	}
}

func adoptID3Frame(f id3v2.Framer) id3v2.Framer {
	switch t := f.(type) {
	case id3v2.TextFrame:
		return id3Text{enc: id3TextEncoding(t.Encoding), values: splitNUL(t.Text)}
	case id3v2.UserDefinedTextFrame:
		return id3UserText{enc: id3TextEncoding(t.Encoding), desc: t.Description, values: splitNUL(t.Value)}
	case id3v2.CommentFrame:
		return id3LangText{enc: id3TextEncoding(t.Encoding), lang: t.Language, desc: t.Description, values: splitNUL(t.Text)}
	case id3v2.UnsynchronisedLyricsFrame:
		return id3LangText{enc: id3TextEncoding(t.Encoding), lang: t.Language, desc: t.ContentDescriptor, values: []string{t.Lyrics}}
	case id3v2.PictureFrame:
		return id3Picture{enc: id3TextEncoding(t.Encoding), mime: t.MimeType, ptype: t.PictureType, desc: t.Description, data: t.Picture}
	}
	return f
}

// dropEmptyID3Frames leaves out text frames with no text, which mutagen's
// save_frame writes as nothing.
func dropEmptyID3Frames(t *id3v2.Tag) {
	empty := func(values []string) bool { return strings.Join(values, "\x00") == "" }
	for id, frames := range t.AllFrames() {
		kept := slices.DeleteFunc(slices.Clone(frames), func(f id3v2.Framer) bool {
			switch x := f.(type) {
			case id3Text:
				return empty(x.values)
			case id3UserText:
				return empty(x.values)
			case id3LangText:
				return id == "COMM" && empty(x.values)
			}
			return false
		})
		if len(kept) == len(frames) {
			continue
		}
		t.DeleteFrames(id)
		for _, f := range kept {
			t.AddFrame(id, f)
		}
	}
}

// pyPathJoin is os.path.join with two parts on POSIX.
func pyPathJoin(a, b string) string {
	switch {
	case strings.HasPrefix(b, "/"):
		return b
	case a == "" || strings.HasSuffix(a, "/"):
		return a + b
	}
	return a + "/" + b
}

// pyAbspath is os.path.abspath on POSIX.
func pyAbspath(p string) string {
	if !strings.HasPrefix(p, "/") {
		if cwd, err := os.Getwd(); err == nil {
			p = pyPathJoin(cwd, p)
		}
	}
	return posixNormpath(p)
}

// isFile is os.path.isfile: a regular file, following symlinks.
func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

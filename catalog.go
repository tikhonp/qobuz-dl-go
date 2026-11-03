// The typed views of API answers for callers. It ports no Python file:
// upstream hands out the dicts. The port keeps working on the dicts, and
// these views never feed back into names or tags.

package qobuzdl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
)

// Album is an album/get answer, or an album inside a track, a search or an
// artist or label page.
type Album struct {
	ID, Title, Version          string
	Artist                      ArtistRef
	Artists                     []ArtistRef // artists[] with roles
	ReleaseDateOriginal         string
	Label                       LabelRef
	UPC, Copyright              string
	Genre                       string // genre.name
	GenresList                  []string
	ParentalWarning             bool
	TracksCount, MediaCount     int
	Duration                    int
	Image                       Image
	URL                         string
	Streamable, HiresStreamable bool
	MaximumBitDepth             int
	MaximumSamplingRate         float64
	ReleaseType, ProductType    string
	Tracks                      []Track // tracks.items, when the answer has them
	TracksTotal                 int     // tracks.total
	Goodies                     []Goody
	Raw                         json.RawMessage
}

// Track is a track/get answer or a track inside an album, playlist or search.
type Track struct {
	ID, Title, Version, ISRC, Work     string
	TrackNumber, MediaNumber, Duration int
	Performer                          ArtistRef
	Composer                           ArtistRef
	Performers                         string
	ParentalWarning                    bool
	MaximumBitDepth                    int
	MaximumSamplingRate                float64
	HiresStreamable, Streamable        bool
	ReleaseDateOriginal                string
	Album                              *Album // the album inside track/get and search results
	Raw                                json.RawMessage
}

// Playlist is a playlist with the tracks of every page.
type Playlist struct {
	ID, Name, Description string
	Owner                 string // owner.name
	TracksCount, Duration int
	Images                []string
	Tracks                []Track
	Raw                   json.RawMessage // the first page
}

// Artist is an artist with the albums of every page.
type Artist struct {
	ID, Name    string
	AlbumsCount int
	Image       Image
	Albums      []Album
	Raw         json.RawMessage // the first page
}

// Label is a label with the albums of every page.
type Label struct {
	ID, Name    string
	AlbumsCount int
	Albums      []Album
	Raw         json.RawMessage // the first page
}

// ArtistRef is an artist named inside another item.
type ArtistRef struct {
	ID, Name string
	Roles    []string
}

// LabelRef is a label named inside an album.
type LabelRef struct{ ID, Name string }

// Image holds cover URLs. Artists have small and large; albums all four.
type Image struct{ Small, Thumbnail, Large, Back string }

// Goody is an extra file of an album, such as a PDF booklet.
type Goody struct{ ID, Name, URL string }

// SearchPage is one page of a catalog search.
type SearchPage[T any] struct {
	Items         []T
	Total         int
	Limit, Offset int
}

// TrackURL is a getFileUrl answer.
type TrackURL struct {
	URL          string // direct; empty on the segmented path
	FormatID     int
	MimeType     string
	BitDepth     int
	SamplingRate float64
	Sample       bool
	Restrictions []string // restriction codes
	Segmented    bool
	NSegments    int
	// Raw is the answer without url, url_template, key, raw_key and blob,
	// which are signed or secret.
	Raw json.RawMessage
}

// The view helpers read leniently: a missing, null or mistyped field gives
// the zero value. The port reads the dicts with Python's strictness.

func viewStr(d *pyDict, key string) string {
	switch v := d.get(key, nil).(type) {
	case string:
		return v
	case int64, float64:
		return pyStr(v)
	}
	return ""
}

func viewInt(d *pyDict, key string) int {
	switch v := d.get(key, nil).(type) {
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func viewFloat(d *pyDict, key string) float64 {
	switch v := d.get(key, nil).(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	}
	return 0
}

func viewBool(d *pyDict, key string) bool {
	return pyTruthy(d.get(key, nil))
}

func viewDict(d *pyDict, key string) *pyDict {
	if v, ok := d.get(key, nil).(*pyDict); ok {
		return v
	}
	return newDict()
}

func viewList(d *pyDict, key string) []any {
	v, _ := d.get(key, nil).([]any)
	return v
}

func viewStrings(d *pyDict, key string) []string {
	var out []string
	for _, v := range viewList(d, key) {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func viewDicts(d *pyDict, key string) []*pyDict {
	var out []*pyDict
	for _, v := range viewList(d, key) {
		if x, ok := v.(*pyDict); ok {
			out = append(out, x)
		}
	}
	return out
}

func rawJSON(d *pyDict) (json.RawMessage, error) {
	b, err := d.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("qobuzdl: raw answer: %w", err)
	}
	return b, nil
}

func artistRef(d *pyDict) ArtistRef {
	return ArtistRef{ID: viewStr(d, "id"), Name: viewStr(d, "name"), Roles: viewStrings(d, "roles")}
}

func imageFrom(d *pyDict) Image {
	return Image{Small: viewStr(d, "small"), Thumbnail: viewStr(d, "thumbnail"), Large: viewStr(d, "large"), Back: viewStr(d, "back")}
}

// albumFromDict builds an Album from an album dict.
func albumFromDict(d *pyDict) (*Album, error) {
	raw, err := rawJSON(d)
	if err != nil {
		return nil, err
	}
	a := &Album{
		ID:                  viewStr(d, "id"),
		Title:               viewStr(d, "title"),
		Version:             viewStr(d, "version"),
		Artist:              artistRef(viewDict(d, "artist")),
		ReleaseDateOriginal: viewStr(d, "release_date_original"),
		Label:               LabelRef{ID: viewStr(viewDict(d, "label"), "id"), Name: viewStr(viewDict(d, "label"), "name")},
		UPC:                 viewStr(d, "upc"),
		Copyright:           viewStr(d, "copyright"),
		Genre:               viewStr(viewDict(d, "genre"), "name"),
		GenresList:          viewStrings(d, "genres_list"),
		ParentalWarning:     viewBool(d, "parental_warning"),
		TracksCount:         viewInt(d, "tracks_count"),
		MediaCount:          viewInt(d, "media_count"),
		Duration:            viewInt(d, "duration"),
		Image:               imageFrom(viewDict(d, "image")),
		URL:                 viewStr(d, "url"),
		Streamable:          viewBool(d, "streamable"),
		HiresStreamable:     viewBool(d, "hires_streamable"),
		MaximumBitDepth:     viewInt(d, "maximum_bit_depth"),
		MaximumSamplingRate: viewFloat(d, "maximum_sampling_rate"),
		ReleaseType:         viewStr(d, "release_type"),
		ProductType:         viewStr(d, "product_type"),
		TracksTotal:         viewInt(viewDict(d, "tracks"), "total"),
		Raw:                 raw,
	}
	for _, x := range viewDicts(d, "artists") {
		a.Artists = append(a.Artists, artistRef(x))
	}
	for _, x := range viewDicts(viewDict(d, "tracks"), "items") {
		t, err := trackFromDict(x)
		if err != nil {
			return nil, err
		}
		a.Tracks = append(a.Tracks, *t)
	}
	for _, x := range viewDicts(d, "goodies") {
		a.Goodies = append(a.Goodies, Goody{ID: viewStr(x, "id"), Name: viewStr(x, "name"), URL: viewStr(x, "url")})
	}
	return a, nil
}

// trackFromDict builds a Track from a track dict.
func trackFromDict(d *pyDict) (*Track, error) {
	raw, err := rawJSON(d)
	if err != nil {
		return nil, err
	}
	t := &Track{
		ID:                  viewStr(d, "id"),
		Title:               viewStr(d, "title"),
		Version:             viewStr(d, "version"),
		ISRC:                viewStr(d, "isrc"),
		Work:                viewStr(d, "work"),
		TrackNumber:         viewInt(d, "track_number"),
		MediaNumber:         viewInt(d, "media_number"),
		Duration:            viewInt(d, "duration"),
		Performer:           artistRef(viewDict(d, "performer")),
		Composer:            artistRef(viewDict(d, "composer")),
		Performers:          viewStr(d, "performers"),
		ParentalWarning:     viewBool(d, "parental_warning"),
		MaximumBitDepth:     viewInt(d, "maximum_bit_depth"),
		MaximumSamplingRate: viewFloat(d, "maximum_sampling_rate"),
		HiresStreamable:     viewBool(d, "hires_streamable"),
		Streamable:          viewBool(d, "streamable"),
		ReleaseDateOriginal: viewStr(d, "release_date_original"),
		Raw:                 raw,
	}
	if ad, ok := d.get("album", nil).(*pyDict); ok {
		if t.Album, err = albumFromDict(ad); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// playlistFromDict builds a Playlist from one playlist dict.
func playlistFromDict(d *pyDict) (*Playlist, error) {
	raw, err := rawJSON(d)
	if err != nil {
		return nil, err
	}
	p := &Playlist{
		ID:          viewStr(d, "id"),
		Name:        viewStr(d, "name"),
		Description: viewStr(d, "description"),
		Owner:       viewStr(viewDict(d, "owner"), "name"),
		TracksCount: viewInt(d, "tracks_count"),
		Duration:    viewInt(d, "duration"),
		Images:      viewStrings(d, "images"),
		Raw:         raw,
	}
	if err := appendTracks(&p.Tracks, d); err != nil {
		return nil, err
	}
	return p, nil
}

func appendTracks(dst *[]Track, page *pyDict) error {
	for _, x := range viewDicts(viewDict(page, "tracks"), "items") {
		t, err := trackFromDict(x)
		if err != nil {
			return err
		}
		*dst = append(*dst, *t)
	}
	return nil
}

func appendAlbums(dst *[]Album, page *pyDict) error {
	for _, x := range viewDicts(viewDict(page, "albums"), "items") {
		a, err := albumFromDict(x)
		if err != nil {
			return err
		}
		*dst = append(*dst, *a)
	}
	return nil
}

// playlistFromPages builds a Playlist from multi_meta's pages: the first
// page's fields and every page's tracks.
func playlistFromPages(pages []*pyDict) (*Playlist, error) {
	if len(pages) == 0 {
		return nil, fmt.Errorf("%w: playlist without tracks", ErrEmptyContent)
	}
	p, err := playlistFromDict(pages[0])
	if err != nil {
		return nil, err
	}
	for _, page := range pages[1:] {
		if err := appendTracks(&p.Tracks, page); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// artistFromDict builds an Artist from one artist dict.
func artistFromDict(d *pyDict) (*Artist, error) {
	raw, err := rawJSON(d)
	if err != nil {
		return nil, err
	}
	a := &Artist{
		ID:          viewStr(d, "id"),
		Name:        viewStr(d, "name"),
		AlbumsCount: viewInt(d, "albums_count"),
		Image:       imageFrom(viewDict(d, "image")),
		Raw:         raw,
	}
	if err := appendAlbums(&a.Albums, d); err != nil {
		return nil, err
	}
	return a, nil
}

// artistFromPages builds an Artist from multi_meta's pages.
func artistFromPages(pages []*pyDict) (*Artist, error) {
	if len(pages) == 0 {
		return nil, fmt.Errorf("%w: artist without albums", ErrEmptyContent)
	}
	a, err := artistFromDict(pages[0])
	if err != nil {
		return nil, err
	}
	for _, page := range pages[1:] {
		if err := appendAlbums(&a.Albums, page); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// labelFromPages builds a Label from multi_meta's pages.
func labelFromPages(pages []*pyDict) (*Label, error) {
	if len(pages) == 0 {
		return nil, fmt.Errorf("%w: label without albums", ErrEmptyContent)
	}
	d := pages[0]
	raw, err := rawJSON(d)
	if err != nil {
		return nil, err
	}
	l := &Label{
		ID:          viewStr(d, "id"),
		Name:        viewStr(d, "name"),
		AlbumsCount: viewInt(d, "albums_count"),
		Raw:         raw,
	}
	for _, page := range pages {
		if err := appendAlbums(&l.Albums, page); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// searchPageFromDict builds a SearchPage from catalog/search's answer under
// the type's key.
func searchPageFromDict[T any](d *pyDict, key string, build func(*pyDict) (*T, error)) (*SearchPage[T], error) {
	sub := viewDict(d, key)
	page := &SearchPage[T]{Total: viewInt(sub, "total"), Limit: viewInt(sub, "limit"), Offset: viewInt(sub, "offset")}
	for _, x := range viewDicts(sub, "items") {
		item, err := build(x)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, *item)
	}
	return page, nil
}

// signedKeys are the getFileUrl fields TrackURL.Raw leaves out.
var signedKeys = []string{"url", "url_template", "key", "raw_key", "blob"}

// trackURLFromDict builds a TrackURL from a getFileUrl or file/url answer.
func trackURLFromDict(d *pyDict) (*TrackURL, error) {
	clean := newDict()
	for _, k := range d.keys {
		if !slices.Contains(signedKeys, k) {
			clean.set(k, d.vals[k])
		}
	}
	raw, err := rawJSON(clean)
	if err != nil {
		return nil, err
	}
	u := &TrackURL{
		URL:          viewStr(d, "url"),
		FormatID:     viewInt(d, "format_id"),
		MimeType:     viewStr(d, "mime_type"),
		BitDepth:     viewInt(d, "bit_depth"),
		SamplingRate: viewFloat(d, "sampling_rate"),
		Sample:       viewBool(d, "sample"),
		NSegments:    viewInt(d, "n_segments"),
		Raw:          raw,
	}
	u.Segmented = u.URL == "" && d.has("url_template")
	for _, x := range viewDicts(d, "restrictions") {
		u.Restrictions = append(u.Restrictions, viewStr(x, "code"))
	}
	return u, nil
}

// GetAlbumArtist ports get_album_artist over the typed view: the main
// artists' names, else the album artist's, else none. It runs the port's
// getAlbumArtist on Raw, so both agree; an Album without a readable Raw
// falls back to its fields.
func GetAlbumArtist(a *Album) []string {
	if v, err := decodeJSON(bytes.NewReader(a.Raw)); err == nil {
		if d, ok := v.(*pyDict); ok {
			if names, err := getAlbumArtist(slog.New(slog.DiscardHandler), d); err == nil {
				out := make([]string, len(names))
				for i, n := range names {
					out[i] = pyStr(n)
				}
				return out
			}
		}
	}
	var out []string
	for _, r := range a.Artists {
		if slices.Contains(r.Roles, "main-artist") {
			out = append(out, r.Name)
		}
	}
	if len(out) > 0 {
		return out
	}
	if a.Artist.Name != "" {
		return []string{a.Artist.Name}
	}
	return []string{}
}

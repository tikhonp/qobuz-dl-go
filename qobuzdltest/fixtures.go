// The recorded answers the fake serves, and the JSON value that carries them
// with their key order and number text intact.

package qobuzdltest

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"
)

// make golden copies testdata/fixtures into testdata/ here. Embedding them
// keeps the package usable from another module's cache, where the repo's
// own testdata is out of reach.
//
//go:embed testdata
var embedded embed.FS

// object is a JSON object with its keys in order. Values are nil, bool,
// json.Number, string, []any or *object; numbers keep their text, so 96
// stays 96 and 44.1 stays 44.1, as Qobuz sent them.
type object struct {
	keys []string
	vals map[string]any
}

func newObject() *object { return &object{vals: map[string]any{}} }

func (o *object) get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// set replaces a value in place or adds the key at the end.
func (o *object) set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

func (o *object) del(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *object) object(key string) *object {
	v, _ := o.vals[key].(*object)
	return v
}

func (o *object) str(key string) string {
	switch v := o.vals[key].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case int:
		return strconv.Itoa(v)
	}
	return ""
}

// clone copies the object deeply, so a served answer never changes the
// fixture it came from.
func clone(v any) any {
	switch t := v.(type) {
	case *object:
		out := &object{keys: append([]string(nil), t.keys...), vals: make(map[string]any, len(t.vals))}
		for k, x := range t.vals {
			out.vals[k] = clone(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = clone(x)
		}
		return out
	}
	return v
}

// walkStrings replaces every string value, keys left alone.
func walkStrings(v any, f func(string) string) any {
	switch t := v.(type) {
	case string:
		return f(t)
	case *object:
		for _, k := range t.keys {
			t.vals[k] = walkStrings(t.vals[k], f)
		}
	case []any:
		for i, x := range t {
			t[i] = walkStrings(x, f)
		}
	}
	return v
}

func decode(data []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := decodeValue(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("qobuzdltest: trailing data after JSON value")
	}
	return v, nil
}

func decodeValue(d *json.Decoder) (any, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := newObject()
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return nil, err
				}
				v, err := decodeValue(d)
				if err != nil {
					return nil, err
				}
				o.set(k.(string), v)
			}
			_, err := d.Token()
			return o, err
		case '[':
			a := []any{}
			for d.More() {
				v, err := decodeValue(d)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err := d.Token()
			return a, err
		}
		return nil, fmt.Errorf("qobuzdltest: unexpected %v", t)
	}
	return tok, nil
}

func encode(v any) []byte {
	var b bytes.Buffer
	encodeValue(&b, v)
	return b.Bytes()
}

func encodeValue(b *bytes.Buffer, v any) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case json.Number:
		b.WriteString(t.String())
	case int:
		b.WriteString(strconv.Itoa(t))
	case string:
		encodeString(b, t)
	case []any:
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			encodeValue(b, x)
		}
		b.WriteByte(']')
	case *object:
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			encodeString(b, k)
			b.WriteByte(':')
			encodeValue(b, t.vals[k])
		}
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("qobuzdltest: cannot encode %T", v))
	}
}

func encodeString(b *bytes.Buffer, s string) {
	e := json.NewEncoder(b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)         // a string always encodes
	b.Truncate(b.Len() - 1) // Encode ends with a newline
}

// fixture reads and decodes one embedded file from testdata/api; ok is false
// when there is none.
func fixture(name string) (*object, bool, error) {
	data, err := fs.ReadFile(embedded, "testdata/api/"+name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	v, err := decode(data)
	if err != nil {
		return nil, false, fmt.Errorf("qobuzdltest: %s: %w", name, err)
	}
	o, ok := v.(*object)
	if !ok {
		return nil, false, fmt.Errorf("qobuzdltest: %s is not an object", name)
	}
	return o, true, nil
}

// fixtureFile is the recorded answer's file name, as record.py names it:
// the endpoint with - for /, the ID, then the offset or format if any.
func fixtureFile(endpoint, id string, more ...string) string {
	parts := append([]string{strings.ReplaceAll(endpoint, "/", "-"), id}, more...)
	return strings.Join(parts, "-") + ".json"
}

// trackSource is a track the fake knows: a recorded track/get answer, a
// recorded album's track, or a recorded playlist's track.
type trackSource struct {
	item  *object // the track as an album or playlist lists it
	album *object // the album/get answer it belongs to, or nil for a playlist item
}

// catalog is the index the fake builds once per server: every track it can
// answer track/get for and the synthetic collections.
type catalog struct {
	tracks      map[string]trackSource
	collections map[string]*object // "<endpoint> <id>" to the whole collection
}

// buildCatalog indexes the recorded albums and playlist, and builds the
// long and empty collections from the recorded ones.
func buildCatalog() (*catalog, error) {
	c := &catalog{tracks: map[string]trackSource{}, collections: map[string]*object{}}
	entries, err := fs.ReadDir(embedded, "testdata/api")
	if err != nil {
		return nil, err
	}
	// Recorded tracks first, then albums, so a track found in an album and
	// a playlist keeps the full album.
	var playlists []string
	for _, e := range entries {
		if name := e.Name(); strings.HasPrefix(name, "track-get-") {
			t, _, err := fixture(name)
			if err != nil {
				return nil, err
			}
			c.tracks[t.str("id")] = trackSource{item: t}
		}
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "album-get-"):
			a, _, err := fixture(name)
			if err != nil {
				return nil, err
			}
			tracks := a.object("tracks")
			if tracks == nil {
				continue
			}
			items, _ := tracks.vals["items"].([]any)
			for _, it := range items {
				if t, ok := it.(*object); ok {
					if _, seen := c.tracks[t.str("id")]; !seen {
						c.tracks[t.str("id")] = trackSource{item: t, album: a}
					}
				}
			}
		case strings.HasPrefix(name, "playlist-get-"):
			playlists = append(playlists, name)
		}
	}
	for _, name := range playlists {
		p, _, err := fixture(name)
		if err != nil {
			return nil, err
		}
		items, _ := p.object("tracks").vals["items"].([]any)
		for _, it := range items {
			if t, ok := it.(*object); ok {
				if _, seen := c.tracks[t.str("id")]; !seen {
					c.tracks[t.str("id")] = trackSource{item: t}
				}
			}
		}
	}

	for _, s := range []struct {
		endpoint, from, id string
		times              int
	}{
		{"playlist/get", Playlist, PlaylistLong, 12},
		{"artist/get", Artist, ArtistLong, 15},
		{"playlist/get", Playlist, PlaylistEmpty, 0},
		{"artist/get", Artist, ArtistEmpty, 0},
		{"label/get", Label, LabelEmpty, 0},
	} {
		o, ok, err := fixture(fixtureFile(s.endpoint, s.from, "0"))
		if err != nil || !ok {
			return nil, fmt.Errorf("qobuzdltest: no first page of %s %s: %v", s.endpoint, s.from, err)
		}
		c.collections[s.endpoint+" "+s.id] = repeatCollection(o, s.endpoint, s.id, s.times)
	}
	page, ok, err := fixture(fixtureFile("artist/get", Artist, "0"))
	if err != nil || !ok {
		return nil, fmt.Errorf("qobuzdltest: no first page of artist/get %s: %v", Artist, err)
	}
	var albums []*object
	for _, id := range []string{AlbumHiRes96, AlbumWithVersion, AlbumCD} {
		a, ok, err := fixture(fixtureFile("album/get", id))
		if err != nil || !ok {
			return nil, fmt.Errorf("qobuzdltest: no album/get %s: %v", id, err)
		}
		albums = append(albums, a)
	}
	c.collections["artist/get "+ArtistAlbums] = albumsCollection(page, ArtistAlbums, "Norah Jones", albums)
	return c, nil
}

// albumsCollection is an artist's recorded first page listing the given
// albums, as album/get answers them less their tracks, under a new ID and
// name.
func albumsCollection(page *object, id, name string, albums []*object) *object {
	o := clone(page).(*object)
	items := make([]any, len(albums))
	for i, a := range albums {
		a = clone(a).(*object)
		a.del("tracks")
		items[i] = a
	}
	sub := o.object("albums")
	sub.set("items", items)
	sub.set("total", len(items))
	o.set("id", json.Number(id))
	o.set("albums_count", len(items))
	o.set("name", name)
	return o
}

// itemsKey is the key multi_meta reads a collection's items from.
func itemsKey(endpoint string) string {
	if endpoint == "playlist/get" {
		return "tracks"
	}
	return "albums"
}

// countKey is the collection's own count of its items.
func countKey(endpoint string) string {
	if endpoint == "playlist/get" {
		return "tracks_count"
	}
	return "albums_count"
}

// repeatCollection is a recorded first page with its items repeated times
// times, under a new ID and with the counts to match.
func repeatCollection(page *object, endpoint, id string, times int) *object {
	o := clone(page).(*object)
	sub := o.object(itemsKey(endpoint))
	items, _ := sub.vals["items"].([]any)
	all := []any{}
	for range times {
		all = append(all, clone(items).([]any)...)
	}
	sub.set("items", all)
	sub.set("total", len(all))
	o.set("id", json.Number(id))
	o.set(countKey(endpoint), len(all))
	if name, ok := o.vals["name"].(string); ok {
		if times == 0 {
			o.set("name", name+" (empty)")
		} else {
			o.set("name", name+" (long)")
		}
	}
	return o
}

// trackGet is the track/get answer for an unrecorded track: the album's
// track with the album/get answer, less its tracks, as "album" in front; or
// a playlist's track, which carries its album already, less the keys only a
// playlist has.
func (c *catalog) trackGet(id string) (*object, bool) {
	src, ok := c.tracks[id]
	if !ok {
		return nil, false
	}
	t := clone(src.item).(*object)
	if src.album == nil {
		for _, k := range []string{"position", "created_at", "playlist_track_id"} {
			t.del(k)
		}
		return t, true
	}
	album := clone(src.album).(*object)
	album.del("tracks")
	out := newObject()
	out.set("album", album)
	for _, k := range t.keys {
		out.set(k, t.vals[k])
	}
	return out, true
}

// duration is a known track's length, for answers built without a recording.
func (c *catalog) duration(id string) any {
	if src, ok := c.tracks[id]; ok {
		if d, ok := src.item.vals["duration"]; ok {
			return d
		}
	}
	return json.Number("180")
}

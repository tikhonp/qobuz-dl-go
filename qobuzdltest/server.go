// Package qobuzdltest is a fake Qobuz for tests: the web player's login page
// and bundle, the API endpoints qobuzdl calls, and a CDN, served from answers
// recorded from Qobuz and embedded in the package.
//
// A test starts one with New and points a client at it with ClientOptions,
// or a download with Configure. The switches make it misbehave the ways
// Qobuz and its CDN do; Requests tells what the code under test asked for.
//
// What it serves:
//
//   - GET /login and the bundle script it names, carrying AppID, Secret and
//     BadSecret in Python's order: berlin (BadSecret), london (Secret),
//     abidjan (empty).
//   - The API under /api.json/0.2/. Every call needs the X-App-Id AppID,
//     or it gets 400, and the X-User-Auth-Token Token, or it gets 401.
//     user/get answers a Studio account. album/get, track/get and the
//     first pages of playlist/get, artist/get and label/get answer the
//     recorded fixtures; pages honor offset and limit. track/get also
//     answers for every track of a recorded album, with that album in
//     front as "album", and of the recorded playlist, as the playlist lists
//     it. catalog/search answers the recorded search of the requested type,
//     whatever the query. track/getFileUrl checks request_sig against
//     Secret and answers the recorded answer for the track and format, or
//     one built from the format table; the url points at the fake CDN and
//     names the delivered format.
//   - The CDN under /cdn/file/<track>/<format>, one file per format: a
//     16/44.1, 24/96 or 24/192 FLAC, or an MP3. It honors Range with 206.
//   - Covers and goodies under /static/: every recorded
//     https://static.qobuz.com/ URL is rewritten to it, and it answers a JPEG
//     for .jpg and a PDF for .pdf.
//
// The answers are the recorded bytes' values with their key order and
// number text, so 96 stays an int and 44.1 a float.
package qobuzdltest

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
)

// The credentials the fake accepts. AppID, Secret and BadSecret are those of
// the synthetic bundle that make golden writes.
const (
	Token     = "qobuzdltest-user-auth-token-0123456789abcdef0123456789abcdef0123456789"
	AppID     = "123456789"
	Secret    = "5e1f0c2a9b7d4e3f8a6c1b0d2e4f6a8c"
	BadSecret = "0f9e8d7c6b5a49382716051423324150"
	// UserID is the account user/get answers.
	UserID = "1000001"
)

// The recorded items; docs/status.md lists why each was picked.
const (
	AlbumMultiDisc   = "evz7usmruz35a" // Pink Floyd, Ummagumma: 2 discs, 24/192
	AlbumHiRes96     = "k43q1q3kx88fa" // Norah Jones, Begin Again: 24/96
	AlbumCD          = "0634904080853" // Radiohead, Com Lag: 16/44.1, a colon in the title
	AlbumCompilation = "zcs2h686vhj4b" // Various Artists, 20 tracks
	AlbumWithVersion = "b2d4k8h8pu6zb" // version "Live", a title starting with "..."
	AlbumClassical   = "xf2zfbfjz0w1b" // Mahler 2: work and conductor credits, a booklet
	AlbumGoodies     = "el3qez8zpc8bc" // Beethoven 3: a PDF booklet
	TrackSingle      = "20273753"      // Miles Davis, So What (Mono Version): 24/96
	Playlist         = "2082798"       // Top 10 Operas: 10 tracks
	Artist           = "50782"         // Tord Gustavsen Trio: 8 albums
	Label            = "572190"        // Naked Jain Records: 15 albums
)

// Collections the fake builds from the recorded ones, for paging and for
// empty answers.
const (
	PlaylistLong  = "9000001" // Playlist's 10 tracks twelve times over: pages at offsets 0, 50 and 100
	ArtistLong    = "9000001" // Artist's 8 albums fifteen times over: pages at offsets 0, 50 and 100
	PlaylistEmpty = "9000000" // a first page without tracks
	ArtistEmpty   = "9000000" // a first page without albums
	LabelEmpty    = "9000000" // a first page without albums
)

const (
	apiPrefix    = "/api.json/0.2/"
	cdnPrefix    = "/cdn/file/"
	staticPrefix = "/static/"
	staticHost   = "https://static.qobuz.com/"
	// cdnQuery stands for the signature on Qobuz's CDN URLs, which nothing
	// may log.
	cdnQuery = "?etsp=946684800&hmac=qobuzdltest-cdn-signature"
	// downgradeCode is the restriction Qobuz sets on a downgraded answer.
	downgradeCode = "FormatRestrictedByFormatAvailability"
)

// Server is a running fake Qobuz.
type Server struct {
	// URL is the base of everything: the PlayBase, the API under
	// /api.json/0.2/, the CDN and the static files.
	URL string

	srv     *httptest.Server
	catOnce sync.Once
	cat     *catalog
	catErr  error

	mu  sync.Mutex
	sw  switches
	log []Request
}

// Request is one request the fake received, as it came: the query keeps
// request_sig and the header the token, since this is test code.
type Request struct {
	Method, Path string
	Query        url.Values
	Range        string
	Header       http.Header
	// Status is the answer's status code; 0 while the answer is being
	// written.
	Status int
}

// New starts a fake Qobuz, closed by tb.Cleanup. It calls only tb's Helper,
// Cleanup and Fatalf.
func New(tb testing.TB) *Server {
	tb.Helper()
	s := &Server{sw: newSwitches()}
	if _, err := fs.Stat(embedded, "testdata/api/index.json"); err != nil {
		tb.Fatalf("qobuzdltest: fixtures missing, run make golden: %v", err)
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	s.URL = s.srv.URL
	tb.Cleanup(s.srv.Close)
	return s
}

// Configure points the options at the fake: the API and play bases, its
// HTTP client and Token. It sets the track pause and the retry delays to
// zero. DL.GetTokens then reads AppID and the secrets from the fake's bundle.
func (s *Server) Configure(o *qobuzdl.Options) {
	o.APIBase = s.URL + apiPrefix
	o.PlayBase = s.URL
	o.HTTPClient = s.srv.Client()
	o.Settings.UserAuthToken = Token
	o.TrackPause = 0
	o.RetryDelays = make([]time.Duration, len(o.RetryDelays))
}

// ClientOptions are the options for qobuzdl.NewClient against the fake: the
// app ID and the non-empty secrets, BadSecret first, as DL.GetTokens would
// read them from the bundle, and Token.
func (s *Server) ClientOptions() qobuzdl.ClientOptions {
	return qobuzdl.ClientOptions{
		AppID:         AppID,
		Secrets:       []string{BadSecret, Secret},
		UserAuthToken: Token,
		ForceEnglish:  true,
		HTTPClient:    s.srv.Client(),
		APIBase:       s.URL + apiPrefix,
		PlayBase:      s.URL,
	}
}

// Requests returns a copy of the request log, in the order the requests
// arrived.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.log))
	for i, r := range s.log {
		r.Query = cloneValues(r.Query)
		r.Header = r.Header.Clone()
		out[i] = r
	}
	return out
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// statusWriter notes the status the handler answers with.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	i := len(s.log)
	s.log = append(s.log, Request{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Range:  r.Header.Get("Range"),
		Header: r.Header.Clone(),
	})
	s.mu.Unlock()
	sw := &statusWriter{ResponseWriter: w}
	// A cut answer ends in a panic; the status is noted all the same.
	defer func() {
		s.mu.Lock()
		if i < len(s.log) { // Reset may have cleared the log meanwhile
			s.log[i].Status = sw.status
		}
		s.mu.Unlock()
	}()

	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, apiPrefix):
		s.serveAPI(sw, r, strings.TrimPrefix(p, apiPrefix))
	case strings.HasPrefix(p, cdnPrefix):
		s.serveCDN(sw, r, strings.TrimPrefix(p, cdnPrefix))
	case strings.HasPrefix(p, staticPrefix):
		s.serveStatic(sw, r, p)
	case p == "/login":
		serveFile(sw, r, "testdata/bundle/login.html", "text/html; charset=utf-8")
	case strings.HasPrefix(p, "/resources/") && strings.HasSuffix(p, "/bundle.js"):
		serveFile(sw, r, "testdata/bundle/bundle.js", "application/javascript")
	default:
		http.NotFound(sw, r)
	}
}

func serveFile(w http.ResponseWriter, r *http.Request, name, contentType string) {
	data, err := fs.ReadFile(embedded, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// apiError answers as Qobuz does when a call fails.
func apiError(w http.ResponseWriter, status int, message string) {
	o := newObject()
	o.set("status", "error")
	o.set("code", status)
	o.set("message", message)
	writeJSON(w, status, o)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encode(v))
}

func (s *Server) catalog() (*catalog, error) {
	s.catOnce.Do(func() { s.cat, s.catErr = buildCatalog() })
	return s.cat, s.catErr
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request, endpoint string) {
	if r.Header.Get("X-App-Id") != AppID {
		apiError(w, http.StatusBadRequest, "Invalid or missing app_id parameter (app_id)")
		return
	}
	s.mu.Lock()
	expired := s.sw.expired
	s.mu.Unlock()
	if expired || r.Header.Get("X-User-Auth-Token") != Token {
		apiError(w, http.StatusUnauthorized, "User authentication is required.")
		return
	}
	cat, err := s.catalog()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	q := r.URL.Query()
	var answer *object
	switch endpoint {
	case "user/get":
		answer = userGet()
	case "album/get":
		answer, err = s.recorded(endpoint, q.Get("album_id"))
	case "track/get":
		id := q.Get("track_id")
		answer, err = s.recorded(endpoint, id)
		if err == nil && answer == nil {
			answer, _ = cat.trackGet(id)
		}
	case "playlist/get":
		answer, err = s.page(cat, endpoint, q.Get("playlist_id"), q)
	case "artist/get":
		answer, err = s.page(cat, endpoint, q.Get("artist_id"), q)
	case "label/get":
		answer, err = s.page(cat, endpoint, q.Get("label_id"), q)
	case "catalog/search":
		answer, err = s.search(q)
	case "track/getFileUrl":
		s.serveFileURL(w, cat, q)
		return
	default:
		apiError(w, http.StatusNotFound, "Method not found: "+endpoint)
		return
	}
	switch {
	case err != nil:
		apiError(w, http.StatusInternalServerError, err.Error())
	case answer == nil:
		apiError(w, http.StatusNotFound, "No result matching given argument")
	default:
		writeJSON(w, http.StatusOK, s.rewriteStatic(answer))
	}
}

// userGet is a Studio account without the personal fields record.py never
// saves.
func userGet() *object {
	params := newObject()
	params.set("short_label", "Studio")
	cred := newObject()
	cred.set("parameters", params)
	o := newObject()
	o.set("id", json.Number(UserID))
	o.set("login", "qobuzdltest")
	o.set("credential", cred)
	return o
}

// recorded is the recorded answer for an ID, or nil without one.
func (s *Server) recorded(endpoint, id string) (*object, error) {
	if id == "" {
		return nil, nil
	}
	o, ok, err := fixture(fixtureFile(endpoint, id))
	if !ok {
		return nil, err
	}
	return o, nil
}

// page is one page of a collection: the recorded first page, or a built
// collection, with its items cut by offset and limit as Qobuz pages them.
func (s *Server) page(cat *catalog, endpoint, id string, q url.Values) (*object, error) {
	if id == "" {
		return nil, nil
	}
	whole, ok := cat.collections[endpoint+" "+id]
	if ok {
		whole = clone(whole).(*object)
	} else {
		var err error
		whole, ok, err = fixture(fixtureFile(endpoint, id, "0"))
		if !ok {
			return nil, err
		}
	}
	offset, limit := intParam(q, "offset", 0), intParam(q, "limit", 50)
	sub := whole.object(itemsKey(endpoint))
	if sub == nil {
		return whole, nil
	}
	items, _ := sub.vals["items"].([]any)
	sub.set("offset", offset)
	sub.set("limit", limit)
	sub.set("items", window(items, offset, limit))
	return whole, nil
}

func intParam(q url.Values, key string, def int) int {
	n, err := strconv.Atoi(q.Get(key))
	if err != nil || n < 0 {
		return def
	}
	return n
}

func window(items []any, offset, limit int) []any {
	if offset > len(items) {
		offset = len(items)
	}
	end := min(offset+limit, len(items))
	return append([]any{}, items[offset:end]...)
}

// search answers the recorded search of the requested type, whatever the
// query, cut by offset and limit.
func (s *Server) search(q url.Values) (*object, error) {
	typ := q.Get("type")
	switch typ {
	case "albums", "tracks", "artists", "playlists":
	default:
		return nil, nil
	}
	o, ok, err := fixture(fixtureFile("catalog/search", typ))
	if !ok {
		return nil, err
	}
	if sub := o.object(typ); sub != nil {
		items, _ := sub.vals["items"].([]any)
		offset, limit := intParam(q, "offset", 0), intParam(q, "limit", 50)
		sub.set("offset", offset)
		sub.set("limit", limit)
		sub.set("items", window(items, offset, limit))
	}
	return o, nil
}

// rewriteStatic points every cover and goody URL at the fake.
func (s *Server) rewriteStatic(o *object) *object {
	walkStrings(o, func(v string) string {
		if rest, ok := strings.CutPrefix(v, staticHost); ok {
			return s.URL + staticPrefix + rest
		}
		return v
	})
	return o
}

// getFileURLSig is the signature qopy's api_call builds for getFileUrl.
func getFileURLSig(trackID, fmtID, ts, secret string) string {
	sum := md5.Sum([]byte("trackgetFileUrlformat_id" + fmtID + "intentstreamtrack_id" + trackID + ts + secret))
	return hex.EncodeToString(sum[:])
}

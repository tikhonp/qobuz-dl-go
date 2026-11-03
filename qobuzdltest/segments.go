// The segmented path: session/start, file/url and the encrypted segments the
// web player streams, which make golden builds and checks with upstream's
// own decryption.

package qobuzdltest

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// segPrefix serves /cdn/seg/<track>/<format>/<segment>.
const segPrefix = "/cdn/seg/"

// segmentIndex is testdata/segments/index.json.
type segmentIndex struct {
	SessionID string `json:"session_id"`
	Infos     string `json:"infos"`
	Key       string `json:"key"`
	Formats   map[string]struct {
		NSegments int    `json:"n_segments"`
		MimeType  string `json:"mime_type"`
	} `json:"formats"`
}

func loadSegmentIndex() (*segmentIndex, error) {
	data, err := fs.ReadFile(embedded, "testdata/segments/index.json")
	if err != nil {
		return nil, err
	}
	var idx segmentIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("segments/index.json: %w", err)
	}
	return &idx, nil
}

// Segmented makes getFileUrl answer the track without a url at every
// format, as Qobuz does when its CDN refuses the direct download, so the
// client takes the web player's segmented path. file/url and the segments
// work for every track; the switch only takes the direct path away.
func (s *Server) Segmented(trackID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sw.segmented[trackID] = true
}

// modernSig is the signature qopy's _modern_sig builds: the endpoint without
// its slash, each parameter but request_ts and request_sig in key order,
// then the timestamp and the secret.
func modernSig(endpoint string, params url.Values, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != "request_ts" && k != "request_sig" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var b strings.Builder
	b.WriteString(strings.Replace(endpoint, "/", "", 1))
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(params.Get(k))
	}
	b.WriteString(params.Get("request_ts"))
	b.WriteString(secret)
	sum := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// serveSessionStart answers session/start: a POST with a form, signed.
func (s *Server) serveSessionStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "session/start takes a POST")
		return
	}
	if err := r.ParseForm(); err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.PostForm.Get("request_sig") != modernSig("session/start", r.PostForm, Secret) {
		apiError(w, http.StatusBadRequest, "Invalid Request Signature parameter (request_sig)")
		return
	}
	idx, err := loadSegmentIndex()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	o := newObject()
	o.set("session_id", idx.SessionID)
	o.set("profile", r.PostForm.Get("profile"))
	o.set("infos", idx.Infos)
	o.set("expires_at", json.Number(strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)))
	writeJSON(w, http.StatusOK, o)
}

// serveFileURLSegmented answers file/url: signed, with the session's
// X-Session-Id, for formats 6, 7 and 27. It delivers the format getFileUrl
// would, with its restrictions, though at least 6, since the web player has
// no MP3. The rate is in Hz and the bit depth under bits_depth, as the web
// player's API gives them.
func (s *Server) serveFileURLSegmented(w http.ResponseWriter, r *http.Request, cat *catalog) {
	q := r.URL.Query()
	if q.Get("request_sig") != modernSig("file/url", q, Secret) {
		apiError(w, http.StatusBadRequest, "Invalid Request Signature parameter (request_sig)")
		return
	}
	idx, err := loadSegmentIndex()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if r.Header.Get("X-Session-Id") != idx.SessionID {
		apiError(w, http.StatusBadRequest, "Invalid or missing session (X-Session-Id)")
		return
	}
	id, fmtStr := q.Get("track_id"), q.Get("format_id")
	if _, ok := idx.Formats[fmtStr]; !ok {
		apiError(w, http.StatusBadRequest, "Invalid or missing format_id parameter (format_id)")
		return
	}
	if id == "" {
		apiError(w, http.StatusBadRequest, "Invalid or missing track_id parameter (track_id)")
		return
	}
	fmtID, _ := strconv.Atoi(fmtStr)

	s.mu.Lock()
	empty := s.sw.empty[id]
	sample := s.sw.sample[trackFormat{id, fmtID}]
	s.mu.Unlock()
	if empty {
		writeJSON(w, http.StatusOK, newObject())
		return
	}

	plain, delivered, err := s.fileURLAnswer(cat, id, fmtID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if delivered == 5 {
		delivered = 6
	}
	f := formats[delivered]
	seg := idx.Formats[strconv.Itoa(delivered)]
	rate, _ := strconv.ParseFloat(string(f.samplingRate), 64)
	restrictions, ok := plain.vals["restrictions"]
	if !ok {
		restrictions = []any{}
	}
	o := newObject()
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		o.set("track_id", json.Number(strconv.FormatInt(n, 10)))
	} else {
		o.set("track_id", id)
	}
	o.set("format_id", delivered)
	o.set("mime_type", seg.MimeType)
	o.set("sampling_rate", json.Number(strconv.FormatFloat(rate*1000, 'f', -1, 64)))
	o.set("bits_depth", f.bitDepth)
	o.set("n_segments", seg.NSegments)
	o.set("url_template", s.URL+segPrefix+url.PathEscape(id)+"/"+strconv.Itoa(delivered)+"/$SEGMENT$"+cdnQuery)
	o.set("key", idx.Key)
	o.set("restrictions", restrictions)
	if sample {
		o.set("sample", true)
	}
	writeJSON(w, http.StatusOK, o)
}

// serveSegment serves /cdn/seg/<track>/<format>/<segment>. Fail404,
// FailTimes and CutAfter apply as they do to the track's file.
func (s *Server) serveSegment(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	trackID, fmtStr, n := parts[0], parts[1], parts[2]
	if _, err := strconv.Atoi(n); err != nil {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(embedded, "testdata/segments/"+fmtStr+"/"+n+".m4s")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w, ok := s.misbehave(w, r, trackID)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

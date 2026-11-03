// getFileUrl's answers and the CDN that serves the files they point at.

package qobuzdltest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// format is one row of the format table: what Qobuz answers for a format and
// the file the CDN serves for it.
type format struct {
	mimeType, file string
	samplingRate   json.Number
	bitDepth       json.Number
	lower          int // the next tier down, or itself for 5
}

// formats follows the recorded answers' number types: 44.1 is a float, 96
// and 192 are ints.
var formats = map[int]format{
	5:  {"audio/mpeg", "320.mp3", "44.1", "16", 5},
	6:  {"audio/flac", "16-44.1.flac", "44.1", "16", 5},
	7:  {"audio/flac", "24-96.flac", "96", "24", 6},
	27: {"audio/flac", "24-192.flac", "192", "24", 7},
}

func (s *Server) cdnURL(trackID string, fmtID int) string {
	return s.URL + cdnPrefix + url.PathEscape(trackID) + "/" + strconv.Itoa(fmtID) + cdnQuery
}

// serveFileURL answers track/getFileUrl.
func (s *Server) serveFileURL(w http.ResponseWriter, cat *catalog, q url.Values) {
	id, fmtStr, ts := q.Get("track_id"), q.Get("format_id"), q.Get("request_ts")
	if q.Get("request_sig") != getFileURLSig(id, fmtStr, ts, Secret) {
		apiError(w, http.StatusBadRequest, "Invalid Request Signature parameter (request_sig)")
		return
	}
	fmtID, err := strconv.Atoi(fmtStr)
	if _, ok := formats[fmtID]; err != nil || !ok {
		apiError(w, http.StatusBadRequest, "Invalid or missing format_id parameter (format_id)")
		return
	}
	if id == "" {
		apiError(w, http.StatusBadRequest, "Invalid or missing track_id parameter (track_id)")
		return
	}

	s.mu.Lock()
	empty := s.sw.empty[id]
	deliver, delivering := s.sw.deliver[id]
	restrict := s.sw.restrict[trackFormat{id, fmtID}]
	sample := s.sw.sample[trackFormat{id, fmtID}]
	s.mu.Unlock()

	if empty {
		writeJSON(w, http.StatusOK, newObject())
		return
	}
	var answer *object
	switch {
	case delivering:
		answer = builtFileURL(cat, id, deliver, false)
	case restrict:
		answer = builtFileURL(cat, id, formats[fmtID].lower, true)
	default:
		o, ok, err := fixture(fixtureFile("track/getFileUrl", id, fmtStr))
		if err != nil {
			apiError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ok {
			answer = o
		} else {
			answer = builtFileURL(cat, id, fmtID, false)
		}
	}
	delivered, err := strconv.Atoi(answer.str("format_id"))
	if err != nil {
		delivered = fmtID
	}
	answer.set("url", s.cdnURL(id, delivered))
	if answer.str("blob") != "" {
		answer.set("blob", "qobuzdltest-blob")
	}
	if sample {
		answer.set("sample", true)
	}
	writeJSON(w, http.StatusOK, answer)
}

// builtFileURL is an answer for a track and format nothing recorded, with
// the recorded answers' keys in their order.
func builtFileURL(cat *catalog, id string, fmtID int, restricted bool) *object {
	f := formats[fmtID]
	o := newObject()
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		o.set("track_id", json.Number(strconv.FormatInt(n, 10)))
	} else {
		o.set("track_id", id)
	}
	o.set("duration", cat.duration(id))
	o.set("url", "")
	o.set("format_id", fmtID)
	o.set("mime_type", f.mimeType)
	if restricted {
		code := newObject()
		code.set("code", downgradeCode)
		o.set("restrictions", []any{code})
	}
	o.set("sampling_rate", f.samplingRate)
	o.set("bit_depth", f.bitDepth)
	o.set("blob", "qobuzdltest-blob")
	return o
}

// cutWriter passes n bytes of the body through and then drops the
// connection, so the client sees a body shorter than Content-Length.
type cutWriter struct {
	http.ResponseWriter
	left int64
}

func (w *cutWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.left {
		_, _ = w.ResponseWriter.Write(p[:w.left])
		_ = http.NewResponseController(w.ResponseWriter).Flush()
		// The server drops the connection and logs nothing.
		panic(http.ErrAbortHandler)
	}
	w.left -= int64(len(p))
	return w.ResponseWriter.Write(p)
}

func (w *cutWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// serveCDN serves /cdn/file/<track>/<format>.
func (s *Server) serveCDN(w http.ResponseWriter, r *http.Request, rest string) {
	trackID, fmtStr, ok := strings.Cut(rest, "/")
	fmtID, err := strconv.Atoi(fmtStr)
	f, known := formats[fmtID]
	if !ok || err != nil || !known {
		http.NotFound(w, r)
		return
	}

	s.mu.Lock()
	fail404 := s.sw.fail404[trackID]
	failStatus := 0
	if ft, ok := s.sw.failTimes[trackID]; ok && ft.left > 0 {
		failStatus = ft.status
		ft.left--
		s.sw.failTimes[trackID] = ft
	}
	ignoreRange := s.sw.ignoreRange[trackID]
	cut, cutting := s.sw.cutAfter[trackID]
	// A HEAD has no body to cut, so it leaves the switch for the GET.
	cutting = cutting && r.Method == http.MethodGet && !fail404 && failStatus == 0
	if cutting {
		delete(s.sw.cutAfter, trackID) // once
	}
	s.mu.Unlock()

	switch {
	case fail404:
		http.NotFound(w, r)
		return
	case failStatus != 0:
		http.Error(w, http.StatusText(failStatus), failStatus)
		return
	}
	data, err := fs.ReadFile(embedded, "testdata/audio/"+f.file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ignoreRange {
		r.Header.Del("Range")
	}
	if cutting {
		w = &cutWriter{ResponseWriter: w, left: cut}
	}
	w.Header().Set("Content-Type", f.mimeType)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// serveStatic answers a cover for .jpg and a booklet for .pdf.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request, p string) {
	s.mu.Lock()
	down := s.sw.staticDown
	s.mu.Unlock()
	if down {
		http.NotFound(w, r)
		return
	}
	var name, contentType string
	switch strings.ToLower(path.Ext(p)) {
	case ".jpg", ".jpeg":
		name, contentType = "cover.jpg", "image/jpeg"
	case ".pdf":
		name, contentType = "booklet.pdf", "application/pdf"
	default:
		http.NotFound(w, r)
		return
	}
	serveFile(w, r, "testdata/audio/"+name, contentType)
}

// mustFormat stops a test that asks for a format Qobuz doesn't have.
func mustFormat(fmtID int) {
	if _, ok := formats[fmtID]; !ok {
		panic(fmt.Sprintf("qobuzdltest: no format %d; use 5, 6, 7 or 27", fmtID))
	}
}

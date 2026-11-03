package qobuzdltest

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// signedRequest builds an API request signed with secret, as qopy's
// _modern_sig signs.
func signedRequest(t *testing.T, s *Server, method, endpoint string, params url.Values, secret, session string) *http.Request {
	t.Helper()
	params.Set("request_ts", "946684800")
	params.Set("request_sig", modernSig(endpoint, params, secret))
	var req *http.Request
	var err error
	if method == http.MethodPost {
		req, err = http.NewRequestWithContext(t.Context(), method, s.URL+apiPrefix+endpoint, strings.NewReader(params.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req, err = http.NewRequestWithContext(t.Context(), method, s.URL+apiPrefix+endpoint+"?"+params.Encode(), nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-App-Id", AppID)
	req.Header.Set("X-User-Auth-Token", Token)
	if session != "" {
		req.Header.Set("X-Session-Id", session)
	}
	return req
}

func status(t *testing.T, s *Server, req *http.Request) (int, string) {
	t.Helper()
	resp, err := s.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestFakeSessionAndFileURL(t *testing.T) {
	s := New(t)
	idx, err := loadSegmentIndex()
	if err != nil {
		t.Fatal(err)
	}
	start := url.Values{"profile": {"qbz-1"}}
	if code, body := status(t, s, signedRequest(t, s, http.MethodPost, "session/start", start, BadSecret, "")); code != http.StatusBadRequest {
		t.Errorf("session/start with a bad signature: %d %s", code, body)
	}
	if code, body := status(t, s, signedRequest(t, s, http.MethodPost, "session/start", start, Secret, "")); code != http.StatusOK ||
		!strings.Contains(body, idx.Infos) || !strings.Contains(body, idx.SessionID) {
		t.Errorf("session/start: %d %s", code, body)
	}
	file := func() url.Values {
		return url.Values{"track_id": {"1"}, "format_id": {"27"}, "intent": {"import"}}
	}
	cases := []struct {
		name    string
		params  url.Values
		secret  string
		session string
		code    int
	}{
		{"good", file(), Secret, idx.SessionID, http.StatusOK},
		{"bad signature", file(), BadSecret, idx.SessionID, http.StatusBadRequest},
		{"no session", file(), Secret, "", http.StatusBadRequest},
		{"wrong session", file(), Secret, "other", http.StatusBadRequest},
		{"format 5", url.Values{"track_id": {"1"}, "format_id": {"5"}, "intent": {"import"}}, Secret, idx.SessionID, http.StatusBadRequest},
		{"no track", url.Values{"format_id": {"27"}, "intent": {"import"}}, Secret, idx.SessionID, http.StatusBadRequest},
	}
	for _, c := range cases {
		if code, body := status(t, s, signedRequest(t, s, http.MethodGet, "file/url", c.params, c.secret, c.session)); code != c.code {
			t.Errorf("file/url %s: %d %s", c.name, code, body)
		}
	}
}

func TestFakeSegmented(t *testing.T) {
	s := New(t)
	c := newClient(t, s)
	ctx := t.Context()
	idx, err := loadSegmentIndex()
	if err != nil {
		t.Fatal(err)
	}

	// Without the switch getFileUrl has a url; forced, the client gets the
	// segments of the format getFileUrl would deliver.
	if u, err := c.GetTrackURL(ctx, TrackSingle, 27, false); err != nil || u.Segmented || u.URL == "" {
		t.Errorf("before Segmented: %+v, %v", u, err)
	}
	u, err := c.GetTrackURL(ctx, TrackSingle, 27, true)
	if err != nil || !u.Segmented || u.FormatID != 7 || u.BitDepth != 24 || u.SamplingRate != 96 ||
		u.NSegments != idx.Formats["7"].NSegments || len(u.Restrictions) != 1 {
		t.Errorf("forced at 27: %+v, %v", u, err)
	}

	s.Segmented(TrackSingle)
	for _, fmtID := range []int{27, 7, 6} {
		u, err := c.GetTrackURL(ctx, TrackSingle, fmtID, false)
		if err != nil || !u.Segmented || u.URL != "" {
			t.Errorf("Segmented at %d: %+v, %v", fmtID, u, err)
		}
	}
	if u, err := c.GetTrackURL(ctx, TrackSingle, 5, false); err != nil || u.URL != "" || u.FormatID != 5 || u.Segmented {
		t.Errorf("Segmented at 5: %+v, %v", u, err)
	}
	s.Sample(TrackSingle, 6)
	if u, err := c.GetTrackURL(ctx, TrackSingle, 6, false); err != nil || !u.Sample {
		t.Errorf("Sample on file/url: %+v, %v", u, err)
	}
	if n := len(apiRequests(s, "session/start")); n != 1 {
		t.Errorf("%d session/start calls", n)
	}

	// Each segment is served with its size, so HEADs can add them up, and
	// the CDN switches apply to them.
	seg := s.URL + segPrefix + TrackSingle + "/7/"
	for i := range idx.Formats["7"].NSegments + 1 {
		resp, body, err := get(t, s, seg+strconv.Itoa(i)+cdnQuery, "")
		if err != nil || resp.StatusCode != http.StatusOK || resp.ContentLength != int64(len(body)) || len(body) == 0 {
			t.Errorf("segment %d: %d, %d bytes, %v", i, resp.StatusCode, len(body), err)
		}
	}
	for _, bad := range []string{"7/99", "5/0", "7/x", "7"} {
		if resp, _, _ := get(t, s, s.URL+segPrefix+TrackSingle+"/"+bad, ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", bad, resp.StatusCode)
		}
	}
	s.Fail404(TrackSingle)
	if resp, _, _ := get(t, s, seg+"1", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("Fail404 on a segment: %d", resp.StatusCode)
	}
	s.Reset()
	s.CutAfter(TrackSingle, 10)
	if _, body, err := get(t, s, seg+"1", ""); !errors.Is(err, io.ErrUnexpectedEOF) || len(body) != 10 {
		t.Errorf("CutAfter on a segment: %d bytes, %v", len(body), err)
	}

	s.Reset()
	s.EmptyAnswer(TrackSingle)
	if u, err := c.GetTrackURL(ctx, TrackSingle, 27, true); err != nil || string(u.Raw) != "{}" {
		t.Errorf("EmptyAnswer on file/url: %+v, %v", u, err)
	}
}

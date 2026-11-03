package qobuzdl

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Values from gen.py.
const (
	testAppID  = "123456789"
	testSecret = "5e1f0c2a9b7d4e3f8a6c1b0d2e4f6a8c"
	badSecret  = "0f9e8d7c6b5a49382716051423324150"
	testToken  = "fake-user-auth-token-for-tests-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
)

func TestSignaturesGolden(t *testing.T) {
	for _, c := range loadGolden(t, "signatures") {
		in := c.Input.(*pyDict)
		secret := in.get("secret", nil).(string)
		var got string
		if strings.HasPrefix(c.Name, "getFileUrl") {
			got = getFileURLSig(in.get("track_id", nil).(string), int(in.get("fmt_id", nil).(int64)),
				in.get("ts", nil).(int64), secret)
		} else {
			params := in.get("params", nil).(*pyDict)
			var kw []kv
			for _, k := range params.keys {
				kw = append(kw, kv{k, params.vals[k]})
			}
			got = modernSig(in.get("epoint", nil).(string), kw, params.get("request_ts", nil).(int64), secret)
		}
		if got != c.Output {
			t.Errorf("%s: got %s, want %v", c.Name, got, c.Output)
		}
	}
}

func TestModernSigSkipsKeys(t *testing.T) {
	params := []kv{
		{"request_sig", "x"}, {"z", "1"}, {"list", []any{"a"}}, {"none", nil},
		{"dict", newDict()}, {"request_ts", int64(9)}, {"b", true}, {"f", 96.0},
	}
	want := md5Hex("objmethodbTruef96.0z1" + "5" + "sec")
	if got := modernSig("obj/method", params, 5, "sec"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestDeriveSessionKeyGolden(t *testing.T) {
	for _, c := range loadGolden(t, "session_key") {
		if !strings.HasPrefix(c.Name, "derive") {
			continue
		}
		in := c.Input.(*pyDict)
		key, err := deriveSessionKey(in.get("secret", nil).(string), in.get("infos", nil).(string))
		checkGoldenResult(t, c, hex.EncodeToString(key), err)
	}
}

func TestUnwrapTrackKeyGolden(t *testing.T) {
	for _, c := range loadGolden(t, "session_key") {
		if !strings.HasPrefix(c.Name, "unwrap") {
			continue
		}
		in := c.Input.(*pyDict)
		sk, err := hex.DecodeString(in.get("session_key", nil).(string))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := unwrapTrackKey(sk, in.get("key", nil).(string))
		checkGoldenResult(t, c, hex.EncodeToString(raw), err)
	}
}

func TestNormalizeJSONStringsGolden(t *testing.T) {
	for _, c := range loadGolden(t, "normalize_json") {
		got := normalizeJSONStrings(c.Input)
		if !pyEqual(got, c.Output) || pyRepr(got) != pyRepr(c.Output) {
			t.Errorf("%s:\n got %s\nwant %s", c.Name, pyRepr(got), pyRepr(c.Output))
		}
	}
}

// exchange is one request and answer from request_params.json.
type exchange struct {
	method, url string
	headers     [][2]string
	body        any
	status      int
	respBody    string
}

func exchangesFrom(t *testing.T, v any) []exchange {
	t.Helper()
	var out []exchange
	for _, x := range v.([]any) {
		req := x.(*pyDict).get("request", nil).(*pyDict)
		resp := x.(*pyDict).get("response", nil).(*pyDict)
		e := exchange{
			method:   req.get("method", nil).(string),
			url:      req.get("url", nil).(string),
			body:     req.get("body", nil),
			status:   int(resp.get("status", nil).(int64)),
			respBody: resp.get("body", nil).(string),
		}
		for _, h := range req.get("headers", nil).([]any) {
			p := h.([]any)
			e.headers = append(e.headers, [2]string{p[0].(string), p[1].(string)})
		}
		out = append(out, e)
	}
	return out
}

// transportHeaders are the headers requests sends that Go's transport adds
// on its own.
var transportHeaders = map[string]bool{"Accept-Encoding": true, "Connection": true, "Content-Length": true}

// replay answers requests from a recorded transcript and checks that each
// request matches the one Python sent.
type replay struct {
	t  *testing.T
	mu sync.Mutex
	ex []exchange
	i  int
}

func (r *replay) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.i >= len(r.ex) {
		r.t.Errorf("request %d beyond the transcript: %s %s", r.i, req.Method, req.URL)
		return nil, errors.New("beyond the transcript")
	}
	e := r.ex[r.i]
	r.i++
	if req.Method != e.method || req.URL.String() != e.url {
		r.t.Errorf("request %d: got %s %s\nwant %s %s", r.i-1, req.Method, req.URL, e.method, e.url)
	}
	var want []string
	for _, h := range e.headers {
		if transportHeaders[h[0]] {
			continue
		}
		want = append(want, h[0])
		if got := req.Header.Get(h[0]); got != h[1] {
			r.t.Errorf("request %d %s: header %s = %q, want %q", r.i-1, e.url, h[0], got, h[1])
		}
	}
	if len(req.Header) != len(want) {
		r.t.Errorf("request %d %s: headers %v, want %v", r.i-1, e.url, req.Header, want)
	}
	var body any
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		body = string(b)
	}
	if !pyEqual(body, e.body) {
		r.t.Errorf("request %d %s: body %s, want %s", r.i-1, e.url, pyRepr(body), pyRepr(e.body))
	}
	return &http.Response{
		StatusCode: e.status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(e.respBody)),
		Request:    req,
	}, nil
}

// logRecorder keeps every log line with its attributes, as text.
type logRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logRecorder) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(lockedWriter{l}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (l *logRecorder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type lockedWriter struct{ l *logRecorder }

func (w lockedWriter) Write(p []byte) (int, error) {
	w.l.mu.Lock()
	defer w.l.mu.Unlock()
	return w.l.buf.Write(p)
}

func clientOptionsFrom(in *pyDict) ClientOptions {
	o := ClientOptions{
		Email:        in.get("email", nil).(string),
		Password:     in.get("pwd", nil).(string),
		AppID:        in.get("app_id", nil).(string),
		ForceEnglish: in.get("force_english", nil).(bool),
	}
	if tok, ok := in.get("token", nil).(string); ok {
		o.UserAuthToken = tok
	}
	for _, s := range in.get("secrets", nil).([]any) {
		o.Secrets = append(o.Secrets, s.(string))
	}
	return o
}

// replayResult is what one request_params case did in Go.
type replayResult struct {
	client *Client
	result any
	err    error
	logs   string
	ex     []exchange
}

// runReplay runs one request_params case under testing/synctest, whose
// clock starts at the timestamp gen.py signed with.
func runReplay(t *testing.T, c goldenCase) replayResult {
	t.Helper()
	in := c.Input.(*pyDict)
	out := c.Output.(*pyDict)
	var res replayResult
	res.ex = exchangesFrom(t, out.get("exchanges", nil))
	synctest.Test(t, func(t *testing.T) {
		rt := &replay{t: t, ex: res.ex}
		var logs logRecorder
		o := clientOptionsFrom(in.get("client", nil).(*pyDict))
		o.HTTPClient = &http.Client{Transport: rt}
		o.Logger = logs.logger()
		ctx := context.Background()
		res.client, res.err = NewClient(ctx, o)
		if res.err == nil && in.has("call") {
			res.result, res.err = callFromGolden(ctx, res.client, in.get("call", nil).([]any))
		}
		if rt.i != len(rt.ex) {
			t.Errorf("%d requests, want %d", rt.i, len(rt.ex))
		}
		res.logs = logs.String()
	})
	return res
}

func callFromGolden(ctx context.Context, c *Client, call []any) (any, error) {
	name := call[0].(string)
	switch name {
	case "get_album_meta":
		return c.albumMeta(ctx, call[1].(string))
	case "get_track_meta":
		return c.trackMeta(ctx, call[1].(string))
	case "get_plist_meta", "get_artist_meta", "get_label_meta":
		ep := map[string][2]string{
			"get_plist_meta":  {"playlist/get", "tracks_count"},
			"get_artist_meta": {"artist/get", "albums_count"},
			"get_label_meta":  {"label/get", "albums_count"},
		}[name]
		pages, err := c.multiMeta(ctx, ep[0], ep[1], call[1].(string))
		out := make([]any, len(pages))
		for i, p := range pages {
			out[i] = p
		}
		return out, err
	case "search_albums", "search_tracks", "search_artists", "search_playlists":
		limit := 20
		if len(call) > 2 {
			limit = int(call[2].(int64))
		}
		return c.search(ctx, strings.TrimPrefix(name, "search_"), call[1].(string), limit, 0)
	case "get_track_url":
		force := len(call) > 3 && call[3].(bool)
		d, err := c.getTrackURL(ctx, pyStr(call[1]), int(call[2].(int64)), force)
		if err == nil && d.has("raw_key") {
			// The golden holds the key as hex, as gen.py writes bytes.
			d.set("raw_key", hex.EncodeToString(d.get("raw_key", nil).([]byte)))
		}
		return d, err
	case "seq":
		var out []any
		for _, sub := range call[1:] {
			r, err := callFromGolden(ctx, c, sub.([]any))
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, nil
	case "api_call":
		kwargs := call[2].(*pyDict)
		var kw []kv
		for _, k := range kwargs.keys {
			kw = append(kw, kv{k, kwargs.vals[k]})
		}
		return c.apiCallValue(ctx, call[1].(string), kw)
	}
	return nil, fmt.Errorf("unknown call %q", name)
}

// raisedMatches reports whether a Go error stands for the exception Python
// raised.
func raisedMatches(raised string, err error) bool {
	var ae *APIError
	switch raised {
	case "SystemExit(1)":
		return errors.Is(err, ErrAuthentication)
	case "InvalidAppSecretError":
		return errors.Is(err, ErrInvalidAppSecret)
	case "InvalidQuality":
		return errors.Is(err, ErrInvalidQuality)
	case "HTTPError":
		return errors.As(err, &ae) && !errors.Is(err, ErrInvalidAppSecret)
	}
	return pyErrKind(err) == raised
}

func TestRequestParamsGolden(t *testing.T) {
	for _, c := range loadGolden(t, "request_params") {
		t.Run(c.Name, func(t *testing.T) {
			res := runReplay(t, c)
			out := c.Output.(*pyDict)
			if raised, ok := out.get("raised", nil).(string); ok {
				if !raisedMatches(raised, res.err) {
					t.Errorf("got %v, want %s", res.err, raised)
				}
			} else if c.Name == "call: search error" {
				// Python answers {}; the port returns the error.
				var ae *APIError
				if !errors.As(res.err, &ae) || ae.Status != 500 {
					t.Errorf("got %v, want an APIError with 500", res.err)
				}
			} else if res.err != nil {
				t.Fatalf("unexpected error: %v", res.err)
			}
			if state, ok := out.get("state", nil).(*pyDict); ok && res.client != nil {
				checkClientState(t, res.client, state)
			}
			if want := out.get("result", nil); out.has("result") && c.Name != "call: search error" {
				if !pyEqual(res.result, want) {
					t.Errorf("result\n got %s\nwant %s", pyRepr(res.result), pyRepr(want))
				}
			}
			if c.Name == "new client" || c.Name == "new client without force_english" {
				checkHeaderOrder(t, res.client, res.ex[0].headers)
			}
		})
	}
}

func checkClientState(t *testing.T, c *Client, state *pyDict) {
	t.Helper()
	var secrets []any
	for _, s := range c.secrets {
		secrets = append(secrets, s)
	}
	got := newDict()
	got.set("app_id", c.id)
	got.set("secrets", append([]any{}, secrets...))
	got.set("sec", c.sec)
	got.set("label", c.label)
	got.set("user_id", c.userID)
	if !pyEqual(got, state) {
		t.Errorf("state\n got %s\nwant %s", pyRepr(got), pyRepr(state))
	}
}

// checkHeaderOrder compares the header list with the order requests sends,
// which Go's transport doesn't keep but the list documents.
func checkHeaderOrder(t *testing.T, c *Client, want [][2]string) {
	t.Helper()
	var w, g []string
	for _, h := range want {
		if !transportHeaders[h[0]] {
			w = append(w, h[0]+": "+h[1])
		}
	}
	for _, h := range c.headers() {
		g = append(g, h.k+": "+pyStr(h.v))
	}
	if strings.Join(g, "\n") != strings.Join(w, "\n") {
		t.Errorf("header order\n got %q\nwant %q", g, w)
	}
}

func TestNoSecretsInErrorsOrLogs(t *testing.T) {
	for _, c := range loadGolden(t, "request_params") {
		t.Run(c.Name, func(t *testing.T) {
			res := runReplay(t, c)
			secrets := []string{testToken, testSecret, badSecret, strings.Repeat("p", 61)}
			for _, e := range res.ex {
				if u, err := url.Parse(e.url); err == nil {
					if sig := u.Query().Get("request_sig"); sig != "" {
						secrets = append(secrets, sig)
					}
				}
				if q, err := url.ParseQuery(fmt.Sprint(e.body)); err == nil && q.Get("request_sig") != "" {
					secrets = append(secrets, q.Get("request_sig"))
				}
			}
			text := res.logs
			if res.err != nil {
				text += "\n" + res.err.Error()
			}
			for _, s := range secrets {
				if strings.Contains(text, s) {
					t.Errorf("logs or error contain %q:\n%s", s, text)
				}
			}
		})
	}
}

// fakeAPI is a small Qobuz API for the tests below: user/get from a
// function, getFileUrl checked against testSecret, the rest from handlers.
type fakeAPI struct {
	userGet  func(w http.ResponseWriter)
	handlers map[string]http.HandlerFunc
	mu       sync.Mutex
	queries  []string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ep := strings.TrimPrefix(r.URL.Path, "/api.json/0.2/")
	f.mu.Lock()
	f.queries = append(f.queries, ep+"?"+r.URL.RawQuery)
	f.mu.Unlock()
	if h, ok := f.handlers[ep]; ok {
		h(w, r)
		return
	}
	q := r.URL.Query()
	switch ep {
	case "user/get":
		if f.userGet != nil {
			f.userGet(w)
			return
		}
		_, _ = io.WriteString(w, `{"id": 1, "credential": {"parameters": {"short_label": "Studio"}}}`)
	case "track/getFileUrl":
		want := md5Hex("trackgetFileUrlformat_id" + q.Get("format_id") + "intentstreamtrack_id" + q.Get("track_id") +
			q.Get("request_ts") + testSecret)
		if q.Get("request_sig") != want {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message": "Invalid Request Signature parameter (request_sig)"}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"track_id": %s, "url": "https://cdn.test/f?sig=1", "format_id": %s, "bit_depth": 16, "sampling_rate": 44.1}`,
			q.Get("track_id"), q.Get("format_id"))
	case "album/get":
		_, _ = fmt.Fprintf(w, `{"id": %q, "title": "Album..."}`, q.Get("album_id"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAPI) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.queries...)
}

func startFake(t *testing.T, f *fakeAPI) ClientOptions {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return ClientOptions{
		AppID:         testAppID,
		Secrets:       []string{testSecret},
		UserAuthToken: testToken,
		ForceEnglish:  true,
		HTTPClient:    srv.Client(),
		APIBase:       srv.URL + "/api.json/0.2/",
		PlayBase:      srv.URL,
	}
}

func TestGetFileURLRejectsQuality(t *testing.T) {
	f := &fakeAPI{}
	c, err := NewClient(context.Background(), startFake(t, f))
	if err != nil {
		t.Fatal(err)
	}
	before := len(f.seen())
	for _, fmtID := range []int64{0, 4, 8, 26, 28} {
		_, err := c.apiCallValue(context.Background(), "track/getFileUrl", []kv{{"id", "1"}, {"fmt_id", fmtID}})
		if !errors.Is(err, ErrInvalidQuality) {
			t.Errorf("format %d: got %v, want ErrInvalidQuality", fmtID, err)
		}
	}
	if n := len(f.seen()) - before; n != 0 {
		t.Errorf("%d requests, want none", n)
	}
}

func TestGetFileURL400IsInvalidSecret(t *testing.T) {
	f := &fakeAPI{}
	c, err := NewClient(context.Background(), startFake(t, f))
	if err != nil {
		t.Fatal(err)
	}
	c.sec = badSecret
	_, err = c.apiCallValue(context.Background(), "track/getFileUrl", []kv{{"id", "1"}, {"fmt_id", int64(27)}})
	var ae *APIError
	if !errors.Is(err, ErrInvalidAppSecret) || !errors.As(err, &ae) || ae.Status != 400 {
		t.Fatalf("got %v, want ErrInvalidAppSecret around a 400 APIError", err)
	}
}

func TestAuthExpired(t *testing.T) {
	f := &fakeAPI{userGet: func(w http.ResponseWriter) { w.WriteHeader(http.StatusUnauthorized) }}
	_, err := NewClient(context.Background(), startFake(t, f))
	var ae *APIError
	if !errors.Is(err, ErrAuthentication) || !errors.As(err, &ae) || ae.Status != 401 {
		t.Fatalf("got %v, want ErrAuthentication around a 401", err)
	}
}

func TestAuthBypassed(t *testing.T) {
	for _, status := range []int{400, 403, 500} {
		f := &fakeAPI{userGet: func(w http.ResponseWriter) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"id": 5, "credential": {"parameters": {"short_label": "Sublime"}}}`)
		}}
		c, err := NewClient(context.Background(), startFake(t, f))
		if err != nil {
			t.Fatalf("%d: %v", status, err)
		}
		if c.Label() != "Studio" || c.UserID() != "" {
			t.Errorf("%d: label %q, user ID %q; want Studio and none", status, c.Label(), c.UserID())
		}
	}
}

func TestAuthNeedsToken(t *testing.T) {
	f := &fakeAPI{}
	o := startFake(t, f)
	o.UserAuthToken = ""
	o.Password = strings.Repeat("p", 60)
	if _, err := NewClient(context.Background(), o); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("got %v, want ErrAuthentication", err)
	}
	if n := len(f.seen()); n != 0 {
		t.Errorf("%d requests, want none", n)
	}
}

func TestAuthPasswordAsToken(t *testing.T) {
	pwd := strings.Repeat("é", 61) // characters, not bytes, as Python's len
	var got string
	f := &fakeAPI{handlers: map[string]http.HandlerFunc{"user/get": func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-User-Auth-Token")
		_, _ = io.WriteString(w, `{}`)
	}}}
	o := startFake(t, f)
	o.UserAuthToken = ""
	o.Password = pwd
	if _, err := NewClient(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if got != pwd {
		t.Errorf("token %q, want the password", got)
	}
}

func TestAuthLabelAndUserID(t *testing.T) {
	for _, tc := range []struct{ body, label, id string }{
		{`{"id": 12, "credential": {"parameters": {"short_label": "Studio Premier"}}}`, "Studio Premier", "12"},
		{`{"user": {"id": 34, "credential": {"parameters": {"short_label": "Sublime"}}}}`, "Sublime", "34"},
		{`{"id": 0, "user": {"id": 56}, "credential": {}}`, "Studio", "56"},
		{`{}`, "Studio", ""},
	} {
		f := &fakeAPI{userGet: func(w http.ResponseWriter) { _, _ = io.WriteString(w, tc.body) }}
		c, err := NewClient(context.Background(), startFake(t, f))
		if err != nil {
			t.Fatal(err)
		}
		if c.Label() != tc.label || c.UserID() != tc.id {
			t.Errorf("%s: label %q, user ID %q; want %q, %q", tc.body, c.Label(), c.UserID(), tc.label, tc.id)
		}
	}
}

func TestCfgSetupSecrets(t *testing.T) {
	for _, tc := range []struct {
		secrets []string
		want    string
		err     error
	}{
		{[]string{badSecret, testSecret}, testSecret, nil},
		{[]string{"", testSecret}, testSecret, nil},
		{[]string{badSecret, "abc"}, badSecret, nil},
		{[]string{""}, "", ErrInvalidAppSecret},
		{nil, "", ErrInvalidAppSecret},
	} {
		o := startFake(t, &fakeAPI{})
		o.Secrets = tc.secrets
		c, err := NewClient(context.Background(), o)
		if tc.err != nil {
			if !errors.Is(err, tc.err) {
				t.Errorf("%q: got %v, want %v", tc.secrets, err, tc.err)
			}
			continue
		}
		if err != nil || c.sec != tc.want {
			t.Errorf("%q: got %v, secret %q; want %q", tc.secrets, err, c.sec, tc.want)
		}
	}
}

func TestMultiMetaPaging(t *testing.T) {
	page := func(w http.ResponseWriter, off, n int, tail string) {
		var items []string
		for i := range n {
			items = append(items, fmt.Sprintf(`{"id": %d}`, off+i))
		}
		_, _ = fmt.Fprintf(w, `{"id": 1, "name": "P", "tracks": {"offset": %d, "items": [%s]}%s}`, off, strings.Join(items, ","), tail)
	}
	for _, tc := range []struct {
		name  string
		serve func(w http.ResponseWriter, off int)
		pages int
	}{
		{"until the total", func(w http.ResponseWriter, off int) {
			var items []string
			n := min(50, 120-off)
			for i := range n {
				items = append(items, fmt.Sprintf(`{"id": %d}`, off+i))
			}
			_, _ = fmt.Fprintf(w, `{"tracks": {"total": 120, "items": [%s]}}`, strings.Join(items, ","))
		}, 3},
		{"empty page stops", func(w http.ResponseWriter, off int) {
			n := 50
			if off > 0 {
				n = 0
			}
			page(w, off, n, `, "tracks_count": 500`)
		}, 1},
		{"tracks_count without total", func(w http.ResponseWriter, off int) {
			page(w, off, min(50, 70-off), `, "tracks_count": 70`)
		}, 2},
	} {
		f := &fakeAPI{handlers: map[string]http.HandlerFunc{"playlist/get": func(w http.ResponseWriter, r *http.Request) {
			var off int
			_, _ = fmt.Sscan(r.URL.Query().Get("offset"), &off)
			tc.serve(w, off)
		}}}
		c, err := NewClient(context.Background(), startFake(t, f))
		if err != nil {
			t.Fatal(err)
		}
		pages, err := c.multiMeta(context.Background(), "playlist/get", "tracks_count", "1")
		if err != nil || len(pages) != tc.pages {
			t.Errorf("%s: %d pages, %v; want %d", tc.name, len(pages), err, tc.pages)
		}
		for _, q := range f.seen() {
			if strings.HasPrefix(q, "playlist/get?") && !strings.Contains(q, "&limit=50&playlist_id=1&extra=tracks") {
				t.Errorf("%s: query %s", tc.name, q)
			}
		}
	}
}

func TestSearchOffsetAndErrors(t *testing.T) {
	fail := atomic.Bool{}
	f := &fakeAPI{handlers: map[string]http.HandlerFunc{"catalog/search": func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "{}", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"albums": {"limit": 5, "offset": 10, "total": 99, "items": [{"id": "a", "title": "T..."}]}}`)
	}}}
	c, err := NewClient(context.Background(), startFake(t, f))
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchAlbums(context.Background(), "kind of blue", 5, 10)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 99 || page.Offset != 10 || len(page.Items) != 1 || page.Items[0].Title != "T…" {
		t.Errorf("page %+v", page)
	}
	if _, err := c.SearchAlbums(context.Background(), "kind of blue", 5, 0); err != nil {
		t.Fatal(err)
	}
	seen := f.seen()
	if got := seen[len(seen)-2]; !strings.HasSuffix(got, "&query=kind+of+blue&type=albums&limit=5&offset=10") {
		t.Errorf("query with offset: %s", got)
	}
	if got := seen[len(seen)-1]; !strings.HasSuffix(got, "&query=kind+of+blue&type=albums&limit=5") {
		t.Errorf("query without offset: %s", got)
	}
	fail.Store(true)
	var ae *APIError
	if _, err := c.SearchTracks(context.Background(), "so what", 10, 0); !errors.As(err, &ae) || ae.Status != 500 {
		t.Errorf("got %v, want an APIError with 500", err)
	}
}

// blockingTransport answers nothing until the request's context ends.
type blockingTransport struct{}

func (blockingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func testClient(rt http.RoundTripper) *Client {
	return &Client{
		hc:       &http.Client{Transport: rt},
		log:      slog.New(slog.DiscardHandler),
		base:     defaultAPIBase,
		timeouts: Timeouts{API: 30 * time.Second},
		id:       testAppID,
		uat:      testToken,
		sec:      testSecret,
	}
}

func TestAPICallTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testClient(blockingTransport{})
		start := time.Now()
		_, err := c.albumMeta(context.Background(), "a")
		var te *timeoutError
		if !errors.As(err, &te) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v, want a timeoutError", err)
		}
		if d := time.Since(start); d != 30*time.Second {
			t.Errorf("timed out after %s, want 30s", d)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = c.albumMeta(ctx, "a")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v, want the caller's deadline", err)
		}
	})
}

func TestAPICallTimeoutDuringAuthPropagates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(time.Second)
			cancel()
		}()
		_, err := NewClient(ctx, ClientOptions{AppID: testAppID, UserAuthToken: testToken, Secrets: []string{testSecret},
			HTTPClient: &http.Client{Transport: blockingTransport{}}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	})
}

func TestClientConcurrentCalls(t *testing.T) {
	c, err := NewClient(context.Background(), startFake(t, &fakeAPI{}))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 20 {
		wg.Go(func() {
			if _, err := c.GetAlbumMeta(context.Background(), fmt.Sprint("a", i)); err != nil {
				errs <- err
			}
		})
		wg.Go(func() {
			if _, err := c.GetTrackURL(context.Background(), fmt.Sprint(i), 27, false); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestGetTrackURLWithoutURLGoesSegmented(t *testing.T) {
	f := &fakeAPI{handlers: map[string]http.HandlerFunc{"track/getFileUrl": func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"track_id": 1, "format_id": 27}`)
	}}}
	o := startFake(t, f)
	c, err := NewClient(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	// The fake has no session/start, so the segmented branch stops there.
	var ae *APIError
	if _, err := c.GetTrackURL(context.Background(), "1", 27, false); !errors.As(err, &ae) || ae.Endpoint != "session/start" {
		t.Errorf("got %v, want session/start's 404", err)
	}
	if _, err := c.GetTrackURL(context.Background(), "1", 5, false); err != nil {
		t.Errorf("format 5: %v", err)
	}
	seen := f.seen()
	if last := seen[len(seen)-1]; !strings.HasPrefix(last, "track/getFileUrl?") {
		t.Errorf("format 5 called %s", last)
	}
}

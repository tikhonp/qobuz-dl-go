// Ports qobuz_dl/qopy.py from qobuz-dl 2.5.9: the Qobuz API client. The
// segmented branch of get_track_url comes with step 8. The port leaves out
// the Last.fm matcher, favorites, lyrics and user/login, which nothing ported calls.

package qobuzdl

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultAPIBase = "https://www.qobuz.com/api.json/0.2/"
	// legacyAppID is the app ID Client.__init__ replaces with the bundle's.
	legacyAppID = "798273057"
	// cfgSetupTrackID is the track cfg_setup signs a request for to test a secret.
	cfgSetupTrackID = "5966783"
)

// ClientOptions holds Client's arguments and the Go-only knobs.
type ClientOptions struct {
	Email, Password string
	AppID           string
	Secrets         []string
	UserAuthToken   string
	ForceEnglish    bool
	HTTPClient      *http.Client  // required; without a Timeout, which would cut long transfers
	Logger          *slog.Logger  // nil discards
	APITimeout      time.Duration // per call; 0 means 30s
	APIBase         string        // "" means https://www.qobuz.com/api.json/0.2/
	PlayBase        string        // "" means https://play.qobuz.com
}

// Client is the Qobuz API client. It is safe for concurrent use: NewClient
// sets everything but the segmented session, which mu guards. Step 8 adds
// the session's infos and key next to sessionID.
type Client struct {
	hc           *http.Client
	log          *slog.Logger
	base         string
	timeouts     Timeouts
	id           string
	secrets      []string
	forceEnglish bool
	uat          string
	sec          string
	label        any
	userID       any

	mu        sync.Mutex
	sessionID string
}

// kv is one keyword argument or query parameter. A slice of them keeps
// Python's order, where url.Values would sort.
type kv struct {
	k string
	v any
}

// NewClient ports Client.__init__, auth and cfg_setup. An empty or legacy app
// ID makes it fetch the bundle and take the bundle's app ID and every secret,
// empty ones included; a failed fetch keeps the given values.
func NewClient(ctx context.Context, o ClientOptions) (*Client, error) {
	if o.HTTPClient == nil {
		return nil, errors.New("qobuzdl: ClientOptions.HTTPClient is nil")
	}
	c := &Client{
		hc:           o.HTTPClient,
		log:          o.Logger,
		base:         o.APIBase,
		timeouts:     Timeouts{API: o.APITimeout},
		id:           o.AppID,
		secrets:      slices.Clone(o.Secrets),
		forceEnglish: o.ForceEnglish,
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.base == "" {
		c.base = defaultAPIBase
	}
	c.log.Info("Logging...")
	if c.id == "" || c.id == legacyAppID {
		if err := c.useBundle(ctx, o.PlayBase); err != nil {
			return nil, err
		}
	} else {
		c.log.Info("[+] Using custom legacy App ID: " + c.id)
	}
	if err := c.auth(ctx, o.Email, o.Password, o.UserAuthToken); err != nil {
		return nil, err
	}
	if err := c.cfgSetup(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// useBundle is the bundle branch of Client.__init__. Python ignores every
// failure; only the caller's context ending stops the client here.
func (c *Client) useBundle(ctx context.Context, playBase string) error {
	b, err := fetchBundle(ctx, c.hc, playBase, c.timeouts)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		c.log.Debug("bundle not used", "err", err)
		return nil
	}
	fresh, err := b.AppID()
	if err != nil || fresh == "" {
		c.log.Debug("bundle not used", "err", err)
		return nil
	}
	// Python sets the ID before reading the secrets, so a failure there
	// keeps the new ID with the old secrets.
	c.id = fresh
	ss, err := b.Secrets()
	if err != nil {
		c.log.Debug("bundle secrets not used", "err", err)
		return nil
	}
	c.secrets = make([]string, len(ss))
	for i, s := range ss {
		c.secrets[i] = s.Value
	}
	c.log.Info("[+] App ID dynamically updated: " + c.id)
	return nil
}

// AppID is the app ID the client signs with.
func (c *Client) AppID() string { return c.id }

// Label is the subscription label, "Studio" when user/get gave none.
func (c *Client) Label() string {
	if c.label == nil {
		return ""
	}
	return pyStr(c.label)
}

// UserID is the account's ID, "" when user/get gave none.
func (c *Client) UserID() string {
	if c.userID == nil {
		return ""
	}
	return pyStr(c.userID)
}

// auth ports Client.auth. Python's sys.exit(1) on a missing token or a 401
// becomes ErrAuthentication; other failures of user/get go on as Python's
// "Profile validation bypassed".
func (c *Client) auth(ctx context.Context, email, pwd, token string) error {
	_ = email // upstream takes it for config.ini and never sends it
	switch {
	case token != "":
		c.uat = token
	case pyLen(pwd) > 60:
		c.uat = pwd
	default:
		c.log.Error("[!] Authentication Error: Auth Token missing or not configured.")
		return fmt.Errorf("%w: no user auth token", ErrAuthentication)
	}
	info, err := c.apiCall(ctx, "user/get", nil)
	if err == nil {
		err = c.readProfile(info)
	}
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		var ae *APIError
		if errors.As(err, &ae) {
			if ae.Status == http.StatusUnauthorized {
				c.log.Error("[!] 401 Unauthorized: Your Auth Token is expired or invalid.")
				return fmt.Errorf("%w: %w", ErrAuthentication, err)
			}
			c.log.Info(fmt.Sprintf("[!] Profile validation bypassed (%v).", err))
		} else {
			c.log.Info("[!] Profile validation bypassed.")
		}
		c.label = "Studio"
		c.userID = nil
		return nil
	}
	c.log.Info("Logged: OK (Membership: " + pyStr(c.label) + ")")
	return nil
}

// readProfile is the body of auth's try block after user/get.
func (c *Client) readProfile(info *pyDict) error {
	// cred = user_info.get("credential") or user_info.get("user", {}).get("credential", {})
	cred := info.get("credential", nil)
	if !pyTruthy(cred) {
		user, err := info.dict("user")
		if err != nil {
			return err
		}
		cred = user.get("credential", newDict())
	}
	credDict, err := asDict(cred)
	if err != nil {
		return err
	}
	params, err := credDict.dict("parameters")
	if err != nil {
		return err
	}
	c.label = params.get("short_label", "Studio")
	// user_info.get("id") or user_info.get("user", {}).get("id")
	id := info.get("id", nil)
	if !pyTruthy(id) {
		user, err := info.dict("user")
		if err != nil {
			return err
		}
		id = user.get("id", nil)
	}
	c.userID = id
	return nil
}

// cfgSetup ports cfg_setup: the first secret that getFileUrl accepts wins.
// When none does, it keeps the first and logs a warning Python doesn't give.
func (c *Client) cfgSetup(ctx context.Context) error {
	found := false
	for i, s := range c.secrets {
		_, err := c.apiCallValue(ctx, "track/getFileUrl", []kv{{"id", cfgSetupTrackID}, {"fmt_id", int64(5)}, {"sec", s}})
		if err == nil {
			c.sec, found = s, true
			break
		}
		if ctx.Err() != nil {
			return err
		}
		c.log.Debug("app secret rejected", "index", i, "err", err)
	}
	if c.sec == "" && len(c.secrets) > 0 {
		c.sec = c.secrets[0]
	}
	if c.sec == "" {
		return fmt.Errorf("%w: no secret found", ErrInvalidAppSecret)
	}
	if !found {
		c.log.Warn("no app secret accepted; using the first, so signed calls will likely fail")
	}
	return nil
}

// redactions are the values no error or log may contain.
func (c *Client) redactions(more ...string) []string {
	out := make([]string, 0, len(c.secrets)+len(more)+2)
	out = append(out, c.uat, c.sec)
	out = append(out, c.secrets...)
	return append(out, more...)
}

// kwarg is kwargs.get(key) with a found flag.
func kwarg(kw []kv, key string) (any, bool) {
	for _, p := range kw {
		if p.k == key {
			return p.v, true
		}
	}
	return nil, false
}

// apiCall ports api_call for an answer used as a dict.
func (c *Client) apiCall(ctx context.Context, epoint string, kw []kv) (*pyDict, error) {
	v, err := c.apiCallValue(ctx, epoint, kw)
	if err != nil {
		return nil, err
	}
	return asDict(v)
}

// apiCallValue ports api_call: it builds the parameters for the endpoint,
// signs them where Qobuz wants a signature, sends them and returns the
// normalized answer, which may be any JSON value.
func (c *Client) apiCallValue(ctx context.Context, epoint string, kw []kv) (any, error) {
	var params []kv
	method := http.MethodGet
	var sig string
	sec := c.sec
	if v, ok := kwarg(kw, "sec"); ok {
		sec = pyStr(v)
	}
	switch epoint {
	case "track/getFileUrl":
		id, _ := kwarg(kw, "id")
		fmtID, _ := kwarg(kw, "fmt_id")
		n, err := pyInt(fmtID)
		if err != nil {
			return nil, err
		}
		if n != 5 && n != 6 && n != 7 && n != 27 {
			return nil, fmt.Errorf("%w: invalid quality id: choose between 5, 6, 7 or 27", ErrInvalidQuality)
		}
		ts := time.Now().Unix()
		sig = getFileURLSig(pyStr(id), int(n), ts, sec)
		params = []kv{{"track_id", id}, {"format_id", fmtID}, {"intent", "stream"}, {"request_ts", ts}, {"request_sig", sig}}
	case "session/start":
		method = http.MethodPost
		ts := time.Now().Unix()
		params = []kv{{"profile", "qbz-1"}, {"request_ts", ts}}
		sig = modernSig(epoint, params, ts, sec)
		params = append(params, kv{"request_sig", sig})
	case "file/url":
		id, _ := kwarg(kw, "id")
		fmtID, _ := kwarg(kw, "fmt_id")
		n, err := pyInt(fmtID)
		if err != nil {
			return nil, err
		}
		if n != 6 && n != 7 && n != 27 {
			return nil, fmt.Errorf("%w: invalid quality id: choose between 6, 7 or 27", ErrInvalidQuality)
		}
		ts := time.Now().Unix()
		params = []kv{{"track_id", id}, {"format_id", fmtID}, {"intent", "import"}, {"request_ts", ts}}
		sig = modernSig(epoint, params, ts, sec)
		params = append(params, kv{"request_sig", sig})
	default:
		params = []kv{{"app_id", c.id}}
		if c.forceEnglish {
			params = append(params, kv{"lang", "en"}, kv{"locale", "en_US"})
		}
		id, _ := kwarg(kw, "id")
		for _, p := range kw {
			if p.k != "id" && p.k != "sec" && p.k != "fmt_id" {
				params = append(params, p)
			}
		}
		switch epoint {
		case "album/get":
			params = append(params, kv{"album_id", id})
		case "track/get":
			params = append(params, kv{"track_id", id})
		case "playlist/get":
			params = append(params, kv{"playlist_id", id}, kv{"extra", "tracks"})
		case "artist/get":
			params = append(params, kv{"artist_id", id}, kv{"extra", "albums"})
		case "label/get":
			params = append(params, kv{"label_id", id}, kv{"extra", "albums"})
		}
	}

	req, err := c.newRequest(ctx, method, epoint, params)
	if err != nil {
		return nil, err
	}
	status, body, err := fetch(ctx, c.hc, c.timeouts, epoint, req)
	if err != nil {
		return nil, err
	}
	c.log.Debug("api call", "endpoint", epoint, "status", status)
	if (epoint == "track/getFileUrl" || epoint == "file/url") && status == http.StatusBadRequest {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAppSecret, newAPIError(epoint, status, body, c.redactions(sig, sec)...))
	}
	if epoint == "user/get" && status == http.StatusBadRequest {
		return newDict(), nil
	}
	if status >= 400 && status < 600 {
		return nil, newAPIError(epoint, status, body, c.redactions(sig, sec)...)
	}
	v, err := decodeJSON(bytes.NewReader(body))
	if err != nil {
		return nil, &requestError{op: epoint, err: fmt.Errorf("answer is not JSON: %w", err)}
	}
	return normalizeJSONStrings(v), nil
}

// newRequest builds the request requests would send: the session's headers
// in Python's order, the parameters in the query or, for session/start, in a
// form body. requests drops parameters whose value is None.
func (c *Client) newRequest(ctx context.Context, method, epoint string, params []kv) (*http.Request, error) {
	var b strings.Builder
	for _, p := range params {
		if p.v == nil {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p.k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(pyStr(p.v)))
	}
	u := c.base + epoint
	var body io.Reader
	if method == http.MethodGet {
		if b.Len() > 0 {
			u += "?" + b.String()
		}
	} else {
		body = strings.NewReader(b.String())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("qobuzdl: %s: %w", epoint, err)
	}
	for _, h := range c.headers() {
		req.Header.Set(h.k, pyStr(h.v))
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return req, nil
}

// headers are the session headers in the order requests sends them, without
// Accept-Encoding and Connection, which Go's transport sets.
func (c *Client) headers() []kv {
	var h []kv
	if c.forceEnglish {
		h = []kv{
			{"User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"},
			{"Accept", "application/json, text/plain, */*"},
			{"Accept-Language", "en-US,en;q=0.9"},
			{"X-App-Language", "en"},
			{"X-App-Region", "US"},
			{"Sec-Ch-Ua", `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`},
			{"Sec-Ch-Ua-Mobile", "?0"},
			{"Sec-Ch-Ua-Platform", `"Windows"`},
			{"Sec-Fetch-Dest", "empty"},
			{"Sec-Fetch-Mode", "cors"},
			{"Sec-Fetch-Site", "same-site"},
			{"X-App-Id", c.id},
		}
	} else {
		h = []kv{
			{"User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"},
			{"Accept", "*/*"},
			{"X-App-Id", c.id},
		}
	}
	if c.uat != "" {
		h = append(h, kv{"X-User-Auth-Token", c.uat})
	}
	c.mu.Lock()
	if c.sessionID != "" {
		h = append(h, kv{"X-Session-Id", c.sessionID})
	}
	c.mu.Unlock()
	return h
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// getFileURLSig is the signature api_call builds for track/getFileUrl.
func getFileURLSig(trackID string, fmtID int, ts int64, secret string) string {
	return md5Hex("trackgetFileUrlformat_id" + strconv.Itoa(fmtID) + "intentstreamtrack_id" + trackID +
		strconv.FormatInt(ts, 10) + secret)
}

// modernSig ports _modern_sig: the object and method, then each parameter
// with a str, int or float value in sorted key order, then ts and the secret.
func modernSig(epoint string, params []kv, ts int64, secret string) string {
	object, method, _ := strings.Cut(epoint, "/")
	sorted := slices.Clone(params)
	// Byte order of UTF-8 is code point order, which sorted() uses.
	slices.SortStableFunc(sorted, func(a, b kv) int { return strings.Compare(a.k, b.k) })
	var b strings.Builder
	b.WriteString(object)
	b.WriteString(method)
	for _, p := range sorted {
		if p.k == "request_ts" || p.k == "request_sig" {
			continue
		}
		switch p.v.(type) {
		case string, int64, float64, bool, int:
			b.WriteString(p.k)
			b.WriteString(pyStr(p.v))
		}
	}
	b.WriteString(strconv.FormatInt(ts, 10))
	b.WriteString(secret)
	return md5Hex(b.String())
}

// b64urlDecode ports _b64url_decode: padding restored, then Python's lenient
// urlsafe_b64decode.
func b64urlDecode(s string) ([]byte, error) {
	pad := (4 - pyLen(s)%4) % 4
	return pyB64Decode(s+strings.Repeat("=", pad), true)
}

// pyFromHex is bytes.fromhex: pairs of hex digits, ASCII whitespace allowed
// between pairs.
func pyFromHex(s string) ([]byte, error) {
	out := make([]byte, 0, len(s)/2)
	for i := 0; i < len(s); {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			i++
			continue
		}
		if i+1 >= len(s) {
			return nil, pyErr(pyValueError, "non-hexadecimal number found in fromhex() arg at position %d", utf8.RuneCountInString(s[:i+1]))
		}
		hi, ok1 := hexValue(s[i])
		lo, ok2 := hexValue(s[i+1])
		if !ok1 || !ok2 {
			pos := i
			if ok1 {
				pos = i + 1
			}
			return nil, pyErr(pyValueError, "non-hexadecimal number found in fromhex() arg at position %d", utf8.RuneCountInString(s[:pos]))
		}
		out = append(out, hi<<4|lo)
		i += 2
	}
	return out, nil
}

func hexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// deriveSessionKey ports _derive_session_key: HKDF-SHA256 over the hex
// secret, with the two halves of infos as salt and info, 16 bytes long.
func deriveSessionKey(secret, infos string) ([]byte, error) {
	parts := strings.Split(infos, ".")
	if len(parts) != 2 {
		return nil, pyErr(pyValueError, "expected 2 values to unpack, got %d", len(parts))
	}
	salt, err := b64urlDecode(parts[0])
	if err != nil {
		return nil, err
	}
	info, err := b64urlDecode(parts[1])
	if err != nil {
		return nil, err
	}
	ikm, err := pyFromHex(secret)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, ikm, salt, string(info), 16)
	if err != nil {
		return nil, pyErr(pyValueError, "%v", err)
	}
	return key, nil
}

// unwrapTrackKey ports _unwrap_track_key: the middle part of the key token
// decrypted with AES-128-CBC under the session key and the last part as IV,
// then PKCS7-unpadded.
func unwrapTrackKey(sessionKey []byte, key string) ([]byte, error) {
	parts := strings.Split(key, ".")
	if len(parts) != 3 {
		return nil, pyErr(pyValueError, "expected 3 values to unpack, got %d", len(parts))
	}
	block, err := aes.NewCipher(sessionKey)
	if err != nil {
		return nil, pyErr(pyValueError, "%v", err)
	}
	iv, err := b64urlDecode(parts[2])
	if err != nil {
		return nil, err
	}
	if len(iv) != aes.BlockSize {
		return nil, pyErr(pyValueError, "Invalid IV size (%d) for CBC.", len(iv))
	}
	wrapped, err := b64urlDecode(parts[1])
	if err != nil {
		return nil, err
	}
	if len(wrapped)%aes.BlockSize != 0 {
		return nil, pyErr(pyValueError, "The length of the provided data is not a multiple of the block length.")
	}
	padded := make([]byte, len(wrapped))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(padded, wrapped)
	n := len(padded)
	if n == 0 {
		return nil, pyErr(pyValueError, "Invalid padding bytes.")
	}
	p := int(padded[n-1])
	if p == 0 || p > aes.BlockSize {
		return nil, pyErr(pyValueError, "Invalid padding bytes.")
	}
	for _, b := range padded[n-p:] {
		if int(b) != p {
			return nil, pyErr(pyValueError, "Invalid padding bytes.")
		}
	}
	return padded[:n-p], nil
}

// normalizeJSONStrings ports _normalize_json_strings: in every string that
// has "..." and no "://", "..." becomes "…"; then every string is NFC. Keys
// are left alone.
func normalizeJSONStrings(v any) any {
	switch t := v.(type) {
	case string:
		if strings.Contains(t, "...") && !strings.Contains(t, "://") {
			t = strings.ReplaceAll(t, "...", "…")
		}
		return pyNFC(t)
	case *pyDict:
		out := newDict()
		for _, k := range t.keys {
			out.set(k, normalizeJSONStrings(t.vals[k]))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalizeJSONStrings(x)
		}
		return out
	}
	return v
}

// multiMeta ports the multi_meta generator, collecting its pages: pages of
// 50 from offset 0 until an empty page or the total.
func (c *Client) multiMeta(ctx context.Context, epoint, key, id string) ([]*pyDict, error) {
	itemsKey := "albums"
	if strings.Contains(epoint, "playlist") {
		itemsKey = "tracks"
	}
	var pages []*pyDict
	offset := int64(0)
	for {
		res, err := c.apiCall(ctx, epoint, []kv{{"id", id}, {"offset", offset}, {"limit", int64(50)}, {"type", nil}})
		if err != nil {
			return pages, err
		}
		sub, err := res.dict(itemsKey)
		if err != nil {
			return pages, err
		}
		items := sub.get("items", []any{})
		if !pyTruthy(items) {
			return pages, nil
		}
		pages = append(pages, res)
		n, err := pyLenValue(items)
		if err != nil {
			return pages, err
		}
		offset += n
		total := sub.get("total", res.get(key, int64(0)))
		if _, _, _, ok := pyNumber(total); !ok {
			return pages, pyErr(pyTypeError, "'>=' not supported between instances of 'int' and '%s'", pyTypeName(total))
		}
		if cmp, ok := cmpNumbers(offset, total); ok && cmp >= 0 {
			return pages, nil
		}
	}
}

// pyLenValue is len(v).
func pyLenValue(v any) (int64, error) {
	switch t := v.(type) {
	case string:
		return int64(pyLen(t)), nil
	case []any:
		return int64(len(t)), nil
	case *pyDict:
		return int64(len(t.keys)), nil
	}
	return 0, pyErr(pyTypeError, "object of type '%s' has no len()", pyTypeName(v))
}

// albumMeta ports get_album_meta.
func (c *Client) albumMeta(ctx context.Context, id string) (*pyDict, error) {
	return c.apiCall(ctx, "album/get", []kv{{"id", id}})
}

// trackMeta ports get_track_meta.
func (c *Client) trackMeta(ctx context.Context, id string) (*pyDict, error) {
	return c.apiCall(ctx, "track/get", []kv{{"id", id}})
}

// errSegmentedNotPorted stands in for the segmented branch until step 8.
var errSegmentedNotPorted = errors.New("qobuzdl: the segmented path is not ported yet")

// getTrackURL ports get_track_url. Format 5 returns getFileUrl's answer
// whatever it is. Other formats return it when it has a url; any failure
// there goes on to the segmented path, as in Python.
func (c *Client) getTrackURL(ctx context.Context, id string, fmtID int, force bool) (*pyDict, error) {
	kw := []kv{{"id", id}, {"fmt_id", int64(fmtID)}}
	if fmtID == 5 {
		return c.apiCall(ctx, "track/getFileUrl", kw)
	}
	if !force {
		v, err := c.apiCallValue(ctx, "track/getFileUrl", kw)
		if err == nil {
			if d, ok := v.(*pyDict); ok && d.has("url") {
				return d, nil
			}
		} else if ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w: track %s at format %d", errSegmentedNotPorted, id, fmtID)
}

// GetAlbumMeta ports get_album_meta.
func (c *Client) GetAlbumMeta(ctx context.Context, id string) (*Album, error) {
	d, err := c.albumMeta(ctx, id)
	if err != nil {
		return nil, err
	}
	return albumFromDict(d)
}

// GetTrackMeta ports get_track_meta.
func (c *Client) GetTrackMeta(ctx context.Context, id string) (*Track, error) {
	d, err := c.trackMeta(ctx, id)
	if err != nil {
		return nil, err
	}
	return trackFromDict(d)
}

// GetPlaylistMeta ports get_plist_meta with every page read. A playlist
// whose first page has no tracks is ErrEmptyContent, since multi_meta
// yields nothing for it.
func (c *Client) GetPlaylistMeta(ctx context.Context, id string) (*Playlist, error) {
	pages, err := c.multiMeta(ctx, "playlist/get", "tracks_count", id)
	if err != nil {
		return nil, err
	}
	return playlistFromPages(pages)
}

// GetArtistMeta ports get_artist_meta with every page read.
func (c *Client) GetArtistMeta(ctx context.Context, id string) (*Artist, error) {
	pages, err := c.multiMeta(ctx, "artist/get", "albums_count", id)
	if err != nil {
		return nil, err
	}
	return artistFromPages(pages)
}

// GetLabelMeta ports get_label_meta with every page read.
func (c *Client) GetLabelMeta(ctx context.Context, id string) (*Label, error) {
	pages, err := c.multiMeta(ctx, "label/get", "albums_count", id)
	if err != nil {
		return nil, err
	}
	return labelFromPages(pages)
}

// search ports the search_* helpers with an offset, sent when non-zero.
// Python answers {} on any error; the port returns the error.
func (c *Client) search(ctx context.Context, typ, query string, limit, offset int) (*pyDict, error) {
	kw := []kv{{"query", query}, {"type", typ}, {"limit", int64(limit)}}
	if offset != 0 {
		kw = append(kw, kv{"offset", int64(offset)})
	}
	return c.apiCall(ctx, "catalog/search", kw)
}

// SearchAlbums ports search_albums.
func (c *Client) SearchAlbums(ctx context.Context, query string, limit, offset int) (*SearchPage[Album], error) {
	d, err := c.search(ctx, "albums", query, limit, offset)
	if err != nil {
		return nil, err
	}
	return searchPageFromDict(d, "albums", albumFromDict)
}

// SearchTracks ports search_tracks.
func (c *Client) SearchTracks(ctx context.Context, query string, limit, offset int) (*SearchPage[Track], error) {
	d, err := c.search(ctx, "tracks", query, limit, offset)
	if err != nil {
		return nil, err
	}
	return searchPageFromDict(d, "tracks", trackFromDict)
}

// SearchArtists ports search_artists.
func (c *Client) SearchArtists(ctx context.Context, query string, limit, offset int) (*SearchPage[Artist], error) {
	d, err := c.search(ctx, "artists", query, limit, offset)
	if err != nil {
		return nil, err
	}
	return searchPageFromDict(d, "artists", artistFromDict)
}

// SearchPlaylists ports search_playlists.
func (c *Client) SearchPlaylists(ctx context.Context, query string, limit, offset int) (*SearchPage[Playlist], error) {
	d, err := c.search(ctx, "playlists", query, limit, offset)
	if err != nil {
		return nil, err
	}
	return searchPageFromDict(d, "playlists", playlistFromDict)
}

// GetTrackURL ports get_track_url.
func (c *Client) GetTrackURL(ctx context.Context, id string, fmtID int, forceSegments bool) (*TrackURL, error) {
	d, err := c.getTrackURL(ctx, id, fmtID, forceSegments)
	if err != nil {
		return nil, err
	}
	return trackURLFromDict(d)
}

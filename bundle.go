// Ports qobuz_dl/bundle.py from qobuz-dl 2.5.9: the web player's bundle,
// which holds the app ID and the seeds of the app secrets.

package qobuzdl

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

const defaultPlayBase = "https://play.qobuz.com"

// Bundle is the text of the web player's bundle.js.
type Bundle struct {
	text string
}

// Secret is one app secret from the bundle and the timezone it is filed under.
type Secret struct {
	Timezone, Value string
}

// The bundle's regexes, with Python's Unicode \w and \d.
var (
	// <script src="(/resources/\d+\.\d+\.\d+-[a-z]\d{3}/bundle\.js)"></script>
	reBundleURL = regexp.MustCompile(`<script src="(/resources/` + reD + `+\.` + reD + `+\.` + reD + `+-[a-z]` +
		reD + `{3}/bundle\.js)"></script>`)
	// production:{api:{appId:"(?P<app_id>\d{9})",appSecret:"\w{32}"
	reAppID = regexp.MustCompile(`production:\{api:\{appId:"(` + reD + `{9})",appSecret:"` + reW + `{32}"`)
	// [a-z]\.initialSeed\("(?P<seed>[\w=]+)",window\.utimezone\.(?P<timezone>[a-z]+)\)
	reSeedTimezone = regexp.MustCompile(`[a-z]\.initialSeed\("([` + reWordChars + `=]+)",window\.utimezone\.([a-z]+)\)`)
)

// FetchBundle ports Bundle.__init__: it reads the login page and then the
// bundle script it names. playBase "" means https://play.qobuz.com.
func FetchBundle(ctx context.Context, hc *http.Client, playBase string) (*Bundle, error) {
	return fetchBundle(ctx, hc, playBase, DefaultTimeouts())
}

func fetchBundle(ctx context.Context, hc *http.Client, playBase string, t Timeouts) (*Bundle, error) {
	if hc == nil {
		return nil, fmt.Errorf("qobuzdl: FetchBundle: nil *http.Client")
	}
	if playBase == "" {
		playBase = defaultPlayBase
	}
	page, err := getText(ctx, hc, t, "login page", playBase+"/login")
	if err != nil {
		return nil, err
	}
	m := reBundleURL.FindStringSubmatch(page)
	if m == nil {
		return nil, fmt.Errorf("%w: no bundle script in the login page", ErrBundle)
	}
	text, err := getText(ctx, hc, t, "bundle", playBase+m[1])
	if err != nil {
		return nil, err
	}
	return &Bundle{text: text}, nil
}

// getText is a GET with response.raise_for_status().
func getText(ctx context.Context, hc *http.Client, t Timeouts, op, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("qobuzdl: %s: %w", op, err)
	}
	// A plain requests Session sends these; Go's transport adds the rest.
	req.Header.Set("User-Agent", "python-requests/2.34.2")
	req.Header.Set("Accept", "*/*")
	status, body, err := fetch(ctx, hc, t, op, req)
	if err != nil {
		return "", err
	}
	if status >= 400 && status < 600 {
		return "", newAPIError(op, status, body)
	}
	return string(body), nil
}

// AppID ports get_app_id.
func (b *Bundle) AppID() (string, error) {
	m := reAppID.FindStringSubmatch(b.text)
	if m == nil {
		return "", fmt.Errorf("%w: no app ID in the bundle", ErrBundle)
	}
	return m[1], nil
}

// Secrets ports get_secrets: every timezone's secret in Python's order, the
// second timezone first, empty values included.
func (b *Bundle) Secrets() ([]Secret, error) {
	var order []string
	parts := map[string][]string{}
	for _, m := range reSeedTimezone.FindAllStringSubmatch(b.text, -1) {
		seed, tz := m[1], m[2]
		if _, ok := parts[tz]; !ok {
			order = append(order, tz)
		}
		parts[tz] = []string{seed}
	}
	if len(order) < 2 {
		return nil, fmt.Errorf("%w: %w", ErrBundle, pyErr(pyIndexError, "list index out of range"))
	}
	// secrets.move_to_end(keypairs[1][0], last=False)
	order[0], order[1] = order[1], order[0]

	names := make([]string, len(order))
	for i, tz := range order {
		names[i] = regexp.QuoteMeta(pyCapitalize(tz))
	}
	// name:"\w+/(?P<timezone>{timezones})",info:"(?P<info>[\w=]+)",extras:"(?P<extras>[\w=]+)"
	reInfo, err := regexp.Compile(`name:"` + reW + `+/(` + strings.Join(names, "|") + `)",info:"([` + reWordChars +
		`=]+)",extras:"([` + reWordChars + `=]+)"`)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBundle, err)
	}
	for _, m := range reInfo.FindAllStringSubmatch(b.text, -1) {
		tz := pyLower(m[1])
		parts[tz] = append(parts[tz], m[2], m[3])
	}

	out := make([]Secret, 0, len(order))
	for _, tz := range order {
		joined := strings.Join(parts[tz], "")
		raw, err := pyB64Decode(pySlice(joined, 0, -44), false)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrBundle, tz, err)
		}
		if !utf8.Valid(raw) {
			return nil, fmt.Errorf("%w: %s: %w", ErrBundle, tz,
				pyErr(pyValueError, "'utf-8' codec can't decode the secret"))
		}
		out = append(out, Secret{Timezone: tz, Value: string(raw)})
	}
	return out, nil
}

// pyB64Decode is base64.b64decode(s), or base64.urlsafe_b64decode(s) when
// urlsafe is set, in Python 3.14's lenient mode: characters outside the
// alphabet are dropped, and padding only counts after the last data
// character. Errors are binascii.Error, a ValueError.
func pyB64Decode(s string, urlsafe bool) ([]byte, error) {
	out := make([]byte, 0, len(s)*3/4)
	var quad, n, pads int
	var left byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= utf8.RuneSelf {
			return nil, pyErr(pyValueError, "string argument should contain only ASCII characters")
		}
		if urlsafe {
			switch c {
			case '-':
				c = '+'
			case '_':
				c = '/'
			}
		}
		if c == '=' {
			pads++
			continue
		}
		v, ok := b64Value(c)
		if !ok {
			continue
		}
		pads = 0
		n++
		switch quad {
		case 0:
			left = v
		case 1:
			out = append(out, left<<2|v>>4)
			left = v & 0x0f
		case 2:
			out = append(out, left<<4|v>>2)
			left = v & 0x03
		case 3:
			out = append(out, left<<6|v)
		}
		quad = (quad + 1) % 4
	}
	switch {
	case quad == 1:
		return nil, pyErr(pyValueError,
			"Invalid base64-encoded string: number of data characters (%d) cannot be 1 more than a multiple of 4", n)
	case quad > 1 && quad+pads < 4:
		return nil, pyErr(pyValueError, "Incorrect padding")
	}
	return out, nil
}

func b64Value(c byte) (byte, bool) {
	switch {
	case c >= 'A' && c <= 'Z':
		return c - 'A', true
	case c >= 'a' && c <= 'z':
		return c - 'a' + 26, true
	case c >= '0' && c <= '9':
		return c - '0' + 52, true
	case c == '+':
		return 62, true
	case c == '/':
		return 63, true
	}
	return 0, false
}

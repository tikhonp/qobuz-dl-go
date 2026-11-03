package qobuzdl

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAPIErrorRedacts(t *testing.T) {
	token := strings.Repeat("t0k3n", 16)
	secret := "abcdef0123456789abcdef0123456789"
	body := `{"status":"error","code":400,"message":"Invalid token ` + token + `","secret":"` + secret + `"}`
	e := newAPIError("track/getFileUrl", 400, []byte(body), token, secret, "")
	for _, s := range []string{e.Error(), e.Body} {
		if strings.Contains(s, token) || strings.Contains(s, secret) {
			t.Errorf("secret in %q", s)
		}
	}
	if want := `qobuzdl: track/getFileUrl: HTTP 400: {"status":"error","code":400,"message":"Invalid token [redacted]","secret":"[redacted]"}`; e.Error() != want {
		t.Errorf("Error() = %q\nwant %q", e.Error(), want)
	}

	// A secret across the 200-byte cut goes before the cut, so no prefix of
	// it remains.
	long := strings.Repeat("x", 190) + token
	e = newAPIError("album/get", 500, []byte(long), token)
	if strings.Contains(e.Body, "t0k3") || len(e.Body) > 200 {
		t.Errorf("Body = %q", e.Body)
	}
}

func TestAPIErrorBodyCutKeepsUTF8(t *testing.T) {
	e := newAPIError("album/get", 404, []byte("x"+strings.Repeat("音", 100)), "")
	if len(e.Body) > apiErrorBodyMax || !utf8.ValidString(e.Body) || len(e.Body) < apiErrorBodyMax-2 {
		t.Errorf("Body of %d bytes, valid %v", len(e.Body), utf8.ValidString(e.Body))
	}
	e = newAPIError("album/get", 404, []byte{'a', 0xff, 'b'})
	if e.Body != "a�b" {
		t.Errorf("invalid UTF-8: %q", e.Body)
	}
	if got := newAPIError("album/get", 503, nil).Error(); got != "qobuzdl: album/get: HTTP 503" {
		t.Errorf("empty body: %q", got)
	}
}

func TestSentinelsWrap(t *testing.T) {
	api := newAPIError("track/getFileUrl", 400, []byte("bad"))
	err := fmt.Errorf("get track url: %w", fmt.Errorf("%w: %w", ErrInvalidAppSecret, api))
	if !errors.Is(err, ErrInvalidAppSecret) {
		t.Error("errors.Is(ErrInvalidAppSecret) failed")
	}
	var got *APIError
	if !errors.As(err, &got) || got.Status != 400 {
		t.Errorf("errors.As(*APIError) = %v", got)
	}
	sentinels := []error{
		ErrAuthentication, ErrIneligible, ErrInvalidAppID, ErrInvalidAppSecret, ErrInvalidQuality,
		ErrNonStreamable, ErrBundle, ErrInvalidURL, ErrEmptyContent, ErrQueryTooShort, ErrUnhandled,
	}
	for i, s := range sentinels {
		w := fmt.Errorf("%w: %v", ErrUnhandled, fmt.Errorf("ctx: %w", s))
		if !errors.Is(w, ErrUnhandled) {
			t.Errorf("%v: ErrUnhandled lost", s)
		}
		if !strings.HasPrefix(s.Error(), "qobuzdl: ") {
			t.Errorf("%q lacks the package prefix", s)
		}
		for j, o := range sentinels {
			if i != j && errors.Is(fmt.Errorf("x: %w", s), o) {
				t.Errorf("%v matches %v", s, o)
			}
		}
	}
	_, _, err = GetURLInfo("nope")
	if !errors.Is(err, ErrInvalidURL) || !isPy(err, pyAttributeError) {
		t.Errorf("GetURLInfo: %v", err)
	}
}

package qobuzdl

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFixture(tb testing.TB, parts ...string) string {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{"testdata", "fixtures"}, parts...)...))
	if err != nil {
		tb.Fatal(err)
	}
	return string(data)
}

// goldenErrorKind maps the Python exception classes in the bundle and
// session key goldens to the kinds the port returns. binascii.Error and
// UnicodeDecodeError are ValueErrors in Python.
func goldenErrorKind(name string) string {
	switch name {
	case "Error", "UnicodeDecodeError":
		return "ValueError"
	}
	return name
}

func secretPairs(ss []Secret) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = []any{s.Timezone, s.Value}
	}
	return out
}

func TestSecretsGolden(t *testing.T) {
	fixture := readFixture(t, "bundle", "bundle.js")
	n := 0
	for _, c := range loadGolden(t, "secrets") {
		in := c.Input.(*pyDict)
		text, _ := in.get("text", "").(string)
		if in.has("bundle") {
			text = fixture
		}
		b := &Bundle{text: text}
		var got any
		var err error
		switch {
		case strings.HasPrefix(c.Name, "fixture bundle app ID"), strings.HasPrefix(c.Name, "app ID: "):
			got, err = b.AppID()
		case strings.HasPrefix(c.Name, "fixture bundle secrets"), strings.HasPrefix(c.Name, "secrets: "):
			var ss []Secret
			ss, err = b.Secrets()
			got = secretPairs(ss)
		default:
			continue
		}
		n++
		checkGoldenResult(t, c, got, err)
	}
	if n < 10 {
		t.Fatalf("only %d cases", n)
	}
}

// checkGoldenResult compares a result with a golden case. NotImplementedError
// is ErrBundle; other Python exceptions compare by kind.
func checkGoldenResult(t *testing.T, c goldenCase, got any, err error) {
	t.Helper()
	switch {
	case c.Error == "NotImplementedError":
		if !errors.Is(err, ErrBundle) {
			t.Errorf("%s: got %s, %v; want ErrBundle", c.Name, pyRepr(got), err)
		}
	case c.Error != "":
		if pyErrKind(err) != goldenErrorKind(c.Error) {
			t.Errorf("%s: got %s, %v; want %s", c.Name, pyRepr(got), err, c.Error)
		}
	case err != nil:
		t.Errorf("%s: %v", c.Name, err)
	case !pyEqual(got, c.Output):
		t.Errorf("%s:\n got %s\nwant %s", c.Name, pyRepr(got), pyRepr(c.Output))
	}
}

func TestBundleURLGolden(t *testing.T) {
	for _, c := range loadGolden(t, "secrets") {
		if !strings.HasPrefix(c.Name, "login page: ") {
			continue
		}
		page := c.Input.(*pyDict).get("page", nil).(string)
		var got any
		if m := reBundleURL.FindStringSubmatch(page); m != nil {
			got = m[1]
		}
		checkGoldenResult(t, c, got, nil)
	}
}

func TestB64DecodeGolden(t *testing.T) {
	n := 0
	for _, c := range loadGolden(t, "secrets") {
		var urlsafe bool
		switch {
		case strings.HasPrefix(c.Name, "b64 standard"):
		case strings.HasPrefix(c.Name, "b64 urlsafe"):
			urlsafe = true
		default:
			continue
		}
		n++
		s := c.Input.(*pyDict).get("s", nil).(string)
		var got any
		var err error
		if urlsafe {
			var b []byte
			b, err = b64urlDecode(s)
			got = hex.EncodeToString(b)
		} else {
			var b []byte
			b, err = pyB64Decode(s, false)
			got = hex.EncodeToString(b)
		}
		checkGoldenResult(t, c, got, err)
	}
	if n < 800 {
		t.Fatalf("only %d cases", n)
	}
}

func TestSecretsNeedTwoTimezones(t *testing.T) {
	b := &Bundle{text: `a.initialSeed("QUJD",window.utimezone.paris)`}
	_, err := b.Secrets()
	if !errors.Is(err, ErrBundle) || pyErrKind(err) != "IndexError" {
		t.Fatalf("got %v, want ErrBundle around an IndexError", err)
	}
}

func bundleServer(t *testing.T, login string) *httptest.Server {
	t.Helper()
	bundle := readFixture(t, "bundle", "bundle.js")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write([]byte(login))
		case "/resources/8.1.0-b019/bundle.js":
			_, _ = w.Write([]byte(bundle))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchBundleReadsFixture(t *testing.T) {
	srv := bundleServer(t, readFixture(t, "bundle", "login.html"))
	b, err := FetchBundle(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	id, err := b.AppID()
	if err != nil || id != "123456789" {
		t.Errorf("AppID() = %q, %v", id, err)
	}
	ss, err := b.Secrets()
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 3 || ss[0].Timezone != "berlin" || ss[2].Value != "" {
		t.Errorf("Secrets() = %+v", ss)
	}
}

func TestFetchBundleNoScript(t *testing.T) {
	srv := bundleServer(t, "<html><script src='/resources/8.1.0-b019/bundle.js'></script></html>")
	_, err := FetchBundle(context.Background(), srv.Client(), srv.URL)
	if !errors.Is(err, ErrBundle) {
		t.Fatalf("got %v, want ErrBundle", err)
	}
}

func TestFetchBundleHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err := FetchBundle(context.Background(), srv.Client(), srv.URL)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusBadGateway {
		t.Fatalf("got %v, want an APIError with 502", err)
	}
}

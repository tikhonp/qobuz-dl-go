package qobuzdl

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.qobuz.com/api.json/0.2/track/getFileUrl?track_id=1&request_sig=abc": "https://www.qobuz.com/api.json/0.2/track/getFileUrl",
		"https://user:pass@cdn.test/file/1/27?token=x#frag":                              "https://cdn.test/file/1/27",
		"https://cdn.test": "https://cdn.test",
		"::not a url":      "[unreadable URL]",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAPIContextDefaultsTo30Seconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := apiContext(context.Background(), Timeouts{})
		defer cancel()
		dl, ok := ctx.Deadline()
		if !ok || time.Until(dl) != 30*time.Second {
			t.Errorf("deadline in %s, want 30s", time.Until(dl))
		}
	})
}

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestFetchErrorsHideTheURL(t *testing.T) {
	hc := &http.Client{Transport: failingTransport{&net.OpError{Op: "dial", Err: errors.New("connection refused")}}}
	req, err := http.NewRequest(http.MethodGet, "https://www.qobuz.com/api.json/0.2/track/getFileUrl?request_sig=secretsig", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = fetch(context.Background(), hc, Timeouts{}, "track/getFileUrl", req)
	var re *requestError
	if !errors.As(err, &re) {
		t.Fatalf("got %T %v, want a requestError", err, err)
	}
	if strings.Contains(err.Error(), "secretsig") || strings.Contains(err.Error(), "qobuz.com") {
		t.Errorf("error shows the URL: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error lost its cause: %v", err)
	}
}

func TestFetchKeepsCallerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequest(http.MethodGet, "https://cdn.test/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = fetch(ctx, &http.Client{Transport: blockingTransport{}}, Timeouts{}, "x", req)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

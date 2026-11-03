// Timeouts and URL redaction for the network calls. It ports no Python file:
// upstream's requests calls have no timeouts, and the port gives every call
// one, as CLAUDE.md asks.

package qobuzdl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Timeouts bounds each network call. A zero field takes its value from
// DefaultTimeouts.
type Timeouts struct {
	API         time.Duration // per API call, answer included
	Connect     time.Duration // from asking for a connection to having one
	Idle        time.Duration // without a byte, headers included
	SegmentHead time.Duration // per HEAD of a segment
	Segment     time.Duration // connect and idle per segment GET
}

// DefaultTimeouts mirrors requests' timeout=(10, 60) for transfers, with 30
// seconds per API call.
func DefaultTimeouts() Timeouts {
	return Timeouts{
		API:         30 * time.Second,
		Connect:     10 * time.Second,
		Idle:        60 * time.Second,
		SegmentHead: 5 * time.Second,
		Segment:     15 * time.Second,
	}
}

// apiContext bounds one API call by t.API.
func apiContext(ctx context.Context, t Timeouts) (context.Context, context.CancelFunc) {
	d := t.API
	if d <= 0 {
		d = DefaultTimeouts().API
	}
	return context.WithTimeout(ctx, d)
}

// redactURL keeps the scheme, host and path. Query strings carry request
// signatures and CDN tokens, which must stay out of logs and errors.
func redactURL(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return "[unreadable URL]"
	}
	return (&url.URL{Scheme: p.Scheme, Host: p.Host, Path: p.Path}).String()
}

// timeoutError is a call that ran past its own timeout while the caller's
// context was still live. Python would have raised requests' Timeout, which
// upstream handles as any other request failure, so it doesn't match
// context.DeadlineExceeded, which always propagates.
type timeoutError struct {
	op    string
	after time.Duration
}

func (e *timeoutError) Error() string {
	return fmt.Sprintf("qobuzdl: %s: no answer within %s", e.op, e.after)
}

// Timeout lets callers test for it as they would for a net.Error.
func (e *timeoutError) Timeout() bool { return true }

// requestError is a failed request without an HTTP status, such as a refused
// connection or an answer that isn't JSON: requests' RequestException. It
// holds no URL, since the URL may carry a signature.
type requestError struct {
	op  string
	err error
}

func (e *requestError) Error() string { return fmt.Sprintf("qobuzdl: %s: %v", e.op, e.err) }
func (e *requestError) Unwrap() error { return e.err }

// maxAnswerSize caps an API answer or page read into memory. The largest
// recorded answer is about 120 KB.
const maxAnswerSize = 32 << 20

// fetch sends req under its own timeout and reads the whole answer. The
// caller's context ending returns its error; the timeout returns a
// timeoutError; other failures a requestError.
func fetch(ctx context.Context, hc *http.Client, t Timeouts, op string, req *http.Request) (status int, body []byte, err error) {
	callCtx, cancel := apiContext(ctx, t)
	defer cancel()
	resp, err := hc.Do(req.WithContext(callCtx))
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxAnswerSize))
	}
	if err != nil {
		return 0, nil, callError(ctx, callCtx, t, op, err)
	}
	return resp.StatusCode, body, nil
}

// callError sorts a failed call into the caller's context error, a
// timeoutError or a requestError.
func callError(ctx, callCtx context.Context, t Timeouts, op string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("qobuzdl: %s: %w", op, ctx.Err())
	}
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		d := t.API
		if d <= 0 {
			d = DefaultTimeouts().API
		}
		return &timeoutError{op: op, after: d}
	}
	// url.Error prints the URL, query string included.
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return &requestError{op: op, err: err}
}

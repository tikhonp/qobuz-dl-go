package qobuzdl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// rtFunc is an in-memory transport, so transfers run inside a synctest
// bubble.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// cdnLog records the Range header of each request.
type cdnLog struct {
	mu     sync.Mutex
	ranges []string
}

func (l *cdnLog) add(r *http.Request) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ranges = append(l.ranges, r.Header.Get("Range"))
	return len(l.ranges)
}

func (l *cdnLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ranges...)
}

// answer builds a response; body may hold more or fewer bytes than
// contentLength says.
func answer(r *http.Request, status int, contentLength int64, body io.Reader) *http.Response {
	h := http.Header{}
	if contentLength >= 0 {
		h.Set("Content-Length", strconv.FormatInt(contentLength, 10))
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(body), ContentLength: contentLength, Request: r}
}

// brokenReader returns its error once the data before it is read, as a
// dropped connection does.
type brokenReader struct{ err error }

func (b brokenReader) Read([]byte) (int, error) { return 0, b.err }

func cutBody(data []byte, n int) io.Reader {
	return io.MultiReader(bytes.NewReader(data[:n]), brokenReader{io.ErrUnexpectedEOF})
}

// rangeStart is the N of "bytes=N-".
func rangeStart(t *testing.T, r *http.Request) int {
	t.Helper()
	v := strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("Range"), "bytes="), "-")
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Errorf("Range %q", r.Header.Get("Range"))
	}
	return n
}

// testFile is 200,000 bytes that differ by position.
func testFile() []byte {
	b := make([]byte, 200000)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func transferDownload(rt rtFunc, delays ...time.Duration) *download {
	o := DefaultOptions()
	o.HTTPClient = &http.Client{Transport: rt}
	if delays != nil {
		o.RetryDelays = delays
	}
	return newDownload(nil, o, "id", "", nil)
}

func TestResume(t *testing.T) {
	data := testFile()
	t.Run("206 appends", func(t *testing.T) {
		var log cdnLog
		d := transferDownload(func(r *http.Request) (*http.Response, error) {
			if log.add(r) == 1 {
				return answer(r, 200, int64(len(data)), cutBody(data, 70000)), nil
			}
			start := rangeStart(t, r)
			return answer(r, 206, int64(len(data)-start), bytes.NewReader(data[start:])), nil
		}, 0)
		fname := filepath.Join(t.TempDir(), "f")
		if err := d.transfer(context.Background(), "http://cdn/f", fname, nil); err != nil {
			t.Fatal(err)
		}
		// urllib3 loses the unfinished 64 KiB chunk of a broken body.
		if got := log.get(); fmt.Sprint(got) != "[bytes=0- bytes=65536-]" {
			t.Errorf("ranges %v", got)
		}
		if got, _ := os.ReadFile(fname); !bytes.Equal(got, data) {
			t.Error("file differs")
		}
	})
	t.Run("200 starts over", func(t *testing.T) {
		var log cdnLog
		d := transferDownload(func(r *http.Request) (*http.Response, error) {
			if log.add(r) == 1 {
				return answer(r, 200, int64(len(data)), cutBody(data, 140000)), nil
			}
			return answer(r, 200, int64(len(data)), bytes.NewReader(data)), nil
		}, 0)
		fname := filepath.Join(t.TempDir(), "f")
		if err := d.transfer(context.Background(), "http://cdn/f", fname, nil); err != nil {
			t.Fatal(err)
		}
		if got := log.get(); fmt.Sprint(got) != "[bytes=0- bytes=131072-]" {
			t.Errorf("ranges %v", got)
		}
		if got, _ := os.ReadFile(fname); !bytes.Equal(got, data) {
			t.Errorf("file has %d bytes, want the %d of one copy", len(got), len(data))
		}
	})
}

func TestTransfer416(t *testing.T) {
	var log cdnLog
	d := transferDownload(func(r *http.Request) (*http.Response, error) {
		log.add(r)
		return answer(r, 416, 0, strings.NewReader("")), nil
	})
	fname := filepath.Join(t.TempDir(), "f")
	if err := d.transfer(context.Background(), "http://cdn/f", fname, nil); err != nil {
		t.Fatalf("416: %v", err)
	}
	if len(log.get()) != 1 {
		t.Errorf("%d requests", len(log.get()))
	}
}

func TestTransferRetries(t *testing.T) {
	data := testFile()[:1000]
	t.Run("404 fails at once", func(t *testing.T) {
		var log cdnLog
		d := transferDownload(func(r *http.Request) (*http.Response, error) {
			log.add(r)
			return answer(r, 404, 0, strings.NewReader("")), nil
		})
		fname := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(fname, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := d.transfer(context.Background(), "http://cdn/f", fname, nil)
		if !errors.Is(err, errFileNotFound) || len(log.get()) != 1 {
			t.Errorf("got %v after %d requests", err, len(log.get()))
		}
		if pathExists(fname) {
			t.Error("file left")
		}
	})
	for _, failures := range []int{4, 5} {
		t.Run(fmt.Sprintf("%d failures", failures), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var log cdnLog
				d := transferDownload(func(r *http.Request) (*http.Response, error) {
					if log.add(r) <= failures {
						return answer(r, 503, 0, strings.NewReader("")), nil
					}
					return answer(r, 200, int64(len(data)), bytes.NewReader(data)), nil
				})
				fname := filepath.Join(t.TempDir(), "f")
				start := time.Now()
				err := d.transfer(context.Background(), "http://cdn/f", fname, nil)
				if elapsed := time.Since(start); elapsed != 30*time.Second {
					t.Errorf("slept %v, want 2+4+8+16 s", elapsed)
				}
				if failures == 4 {
					if err != nil {
						t.Fatal(err)
					}
					if got, _ := os.ReadFile(fname); !bytes.Equal(got, data) {
						t.Error("file differs")
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), "Definitive timeout after 5 attempts. Last error: qobuzdl: transfer: Status Server: 503") {
					t.Errorf("got %v", err)
				}
				if len(log.get()) != 5 || pathExists(fname) {
					t.Errorf("%d requests, file left %v", len(log.get()), pathExists(fname))
				}
			})
		})
	}
	t.Run("an error mentioning 404 counts as one", func(t *testing.T) {
		var log cdnLog
		d := transferDownload(func(r *http.Request) (*http.Response, error) {
			log.add(r)
			return nil, errors.New("dial tcp 127.0.0.1:40404: connection refused")
		}, 0)
		err := d.transfer(context.Background(), "http://cdn/f", filepath.Join(t.TempDir(), "f"), nil)
		if !errors.Is(err, errFileNotFound) || len(log.get()) != 1 {
			t.Errorf("got %v after %d requests", err, len(log.get()))
		}
	})
}

func TestTransferShortReads(t *testing.T) {
	data := testFile()
	run := func(t *testing.T, final bool) {
		synctest.Test(t, func(t *testing.T) {
			var log cdnLog
			d := transferDownload(func(r *http.Request) (*http.Response, error) {
				n := log.add(r)
				if n == 1 {
					// A broken body raises in Python, and the attempt sleeps.
					return answer(r, 200, int64(len(data)), cutBody(data, 70000)), nil
				}
				start := rangeStart(t, r)
				if final && n == 5 {
					return answer(r, 206, int64(len(data)-start), bytes.NewReader(data[start:])), nil
				}
				// A shorter answer that keeps its own Content-Length ends
				// the attempt without an error.
				return answer(r, 206, 1000, bytes.NewReader(data[start:start+1000])), nil
			})
			fname := filepath.Join(t.TempDir(), "f")
			start := time.Now()
			err := d.transfer(context.Background(), "http://cdn/f", fname, nil)
			if elapsed := time.Since(start); elapsed != 2*time.Second {
				t.Errorf("slept %v, want only the 2 s after the broken body", elapsed)
			}
			want := "[bytes=0- bytes=65536- bytes=66536- bytes=67536- bytes=68536-]"
			if got := log.get(); fmt.Sprint(got) != want {
				t.Errorf("ranges %v", got)
			}
			if final {
				if err != nil {
					t.Fatal(err)
				}
				if got, _ := os.ReadFile(fname); !bytes.Equal(got, data) {
					t.Error("file differs")
				}
				return
			}
			if !errors.Is(err, errIncomplete) || pathExists(fname) {
				t.Errorf("got %v, file left %v", err, pathExists(fname))
			}
		})
	}
	t.Run("resume", func(t *testing.T) { run(t, true) })
	t.Run("five short reads", func(t *testing.T) { run(t, false) })
}

func TestTransferOvershootKeepsOffset(t *testing.T) {
	data := testFile()
	var log cdnLog
	d := transferDownload(func(r *http.Request) (*http.Response, error) {
		switch log.add(r) {
		case 1:
			return answer(r, 200, int64(len(data)), cutBody(data, 70000)), nil
		case 2:
			// A 206 with the whole file: more bytes than announced.
			return answer(r, 206, int64(len(data)), bytes.NewReader(data)), nil
		}
		return answer(r, 416, 0, strings.NewReader("")), nil
	}, 0)
	fname := filepath.Join(t.TempDir(), "f")
	if err := d.transfer(context.Background(), "http://cdn/f", fname, nil); err != nil {
		t.Fatal(err)
	}
	// The file was deleted, but the count kept 65536+200000 bytes, so the
	// next attempt asks from there and the 416 counts as done.
	if got := log.get(); fmt.Sprint(got) != "[bytes=0- bytes=65536- bytes=265536-]" {
		t.Errorf("ranges %v", got)
	}
	if pathExists(fname) {
		t.Error("file left")
	}
}

// blockingBody blocks every read until the request's context ends.
type blockingBody struct{ ctx context.Context }

func (b blockingBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

// slowBody gives one byte per read, each after a pause.
type slowBody struct {
	left  int
	pause time.Duration
}

func (b *slowBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, io.EOF
	}
	time.Sleep(b.pause)
	b.left--
	p[0] = 'x'
	return 1, nil
}

func TestTransferTimeouts(t *testing.T) {
	t.Run("connect", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := transferDownload(func(r *http.Request) (*http.Response, error) {
				httptrace.ContextClientTrace(r.Context()).GetConn("cdn:443")
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
			start := time.Now()
			err := d.transfer(context.Background(), "http://cdn/f", filepath.Join(t.TempDir(), "f"), nil)
			if err == nil || !strings.Contains(err.Error(), "Last error: qobuzdl: transfer: connect: no answer within 10s") {
				t.Errorf("got %v", err)
			}
			if elapsed := time.Since(start); elapsed != 5*10*time.Second+30*time.Second {
				t.Errorf("took %v", elapsed)
			}
		})
	})
	t.Run("idle", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := transferDownload(func(r *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(r.Context())
				trace.GetConn("cdn:443")
				trace.GotConn(httptrace.GotConnInfo{})
				return answer(r, 200, 1000, blockingBody{r.Context()}), nil
			}, 0)
			start := time.Now()
			err := d.transfer(context.Background(), "http://cdn/f", filepath.Join(t.TempDir(), "f"), nil)
			if err == nil || !strings.Contains(err.Error(), "Last error: qobuzdl: transfer: read: no answer within 1m0s") {
				t.Errorf("got %v", err)
			}
			if elapsed := time.Since(start); elapsed != 5*time.Minute {
				t.Errorf("took %v", elapsed)
			}
		})
	})
	t.Run("idle restarts on every read", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := transferDownload(func(r *http.Request) (*http.Response, error) {
				return answer(r, 200, 10, &slowBody{left: 10, pause: 50 * time.Second}), nil
			})
			fname := filepath.Join(t.TempDir(), "f")
			if err := d.transfer(context.Background(), "http://cdn/f", fname, nil); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(fname); string(got) != strings.Repeat("x", 10) {
				t.Errorf("file %q", got)
			}
		})
	})
	t.Run("the caller's cancel stops at once", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			d := transferDownload(func(r *http.Request) (*http.Response, error) {
				return answer(r, 200, 1000, blockingBody{r.Context()}), nil
			})
			go func() {
				time.Sleep(5 * time.Second)
				cancel()
			}()
			start := time.Now()
			err := d.transfer(ctx, "http://cdn/f", filepath.Join(t.TempDir(), "f"), nil)
			if !errors.Is(err, context.Canceled) || time.Since(start) != 5*time.Second {
				t.Errorf("got %v after %v", err, time.Since(start))
			}
		})
	})
}

func TestTransferHeaders(t *testing.T) {
	var got http.Header
	d := transferDownload(func(r *http.Request) (*http.Response, error) {
		got = r.Header.Clone()
		return answer(r, 200, 3, strings.NewReader("abc")), nil
	})
	if err := d.transfer(context.Background(), "http://cdn/f", filepath.Join(t.TempDir(), "f"), nil); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"User-Agent": transferUserAgent, "Accept": transferAccept, "Range": "bytes=0-", "Connection": "keep-alive"} {
		if got.Get(k) != v {
			t.Errorf("%s: %q", k, got.Get(k))
		}
	}
}

func TestIsRequestError(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&timeoutError{op: "x"}, true},
		{&requestError{op: "x", err: io.EOF}, true},
		{newAPIError("album/get", 404, nil), true},
		{fmt.Errorf("wrapped: %w", newAPIError("album/get", 500, nil)), true},
		{fmt.Errorf("%w: %w", ErrInvalidAppSecret, newAPIError("track/getFileUrl", 400, nil)), false},
		{ErrNonStreamable, false},
		{pyErr(pyKeyError, "'x'"), false},
		{context.Canceled, false},
	} {
		if got := isRequestError(c.err); got != c.want {
			t.Errorf("%v: got %v", c.err, got)
		}
	}
}

# 6: fake Qobuz

## Read first

- [design.md](../design.md) section qobuzdltest.
- shelf's `docs/build/1d.md`, for what its tests need.
- `qopy.go` and its request-order tests from step 4.
- The As built sections of steps 1 and 4.

## Done when

`qobuzdltest` serves the login page and bundle, the API endpoints the port calls, and the CDN, from the recorded fixtures; a `Client` built with `ClientOptions()` authenticates and reads every fixture; every switch works. The segmented switch comes in step 8.

## Decisions

| Topic | Decision | Why |
| --- | --- | --- |
| Fixtures | Copied from `testdata/fixtures/api/` into `qobuzdltest/testdata/` by `make golden`, embedded with `go:embed`. | The package must work from shelf's module cache, where the repo's `testdata/` above it is out of reach. |
| Track files | One audio file per format: `audio/16-44.flac` for 6, `24-96.flac` for 7, `24-192.flac` for 27, `320.mp3` for 5. | Sizes stay small. |
| Signatures | The fake recomputes `request_sig` with `Secret` and the request's `request_ts`, and answers 400 on a mismatch. | `cfgSetup`'s secret choice gets tested. |
| Quality | `getFileUrl` answers the recorded `getFileUrl` fixture for the track and format; without one it builds an answer from the format table. | Recorded where possible. |
| Restrictions | `Restrict(track, fmt)` answers that format with the restriction code and the next tier's `format_id`, bit depth and rate. | How Qobuz answers a downgrade. |
| Request log | The fake logs every request with method, path, query and `Range`, keeping `request_sig` and the token, since this is test code. | Tests assert call order and counts. |
| Concurrency | Switches and the log behind one mutex. | Tests flip switches during downloads. |
| Imports | `qobuzdltest` imports `qobuzdl` for `Options` and `ClientOptions`; nothing in the root imports it. | No cycle. |

## Files

```
qobuzdltest/server.go  fixtures.go  cdn.go  switches.go
qobuzdltest/server_test.go
qobuzdltest/testdata/api/*.json  audio/*  bundle/*
```

## Go signatures

```go
func New(tb testing.TB) *Server
func (s *Server) Configure(o *qobuzdl.Options)
func (s *Server) ClientOptions() qobuzdl.ClientOptions
func (s *Server) Fail404(trackID string)
func (s *Server) FailTimes(trackID string, n, status int)
func (s *Server) CutAfter(trackID string, n int64)
func (s *Server) IgnoreRange(trackID string)
func (s *Server) Sample(trackID string, fmtID int)
func (s *Server) EmptyAnswer(trackID string)
func (s *Server) Restrict(trackID string, fmtID int)
func (s *Server) Deliver(trackID string, fmtID int)
func (s *Server) ExpireToken()
func (s *Server) Requests() []Request
func (s *Server) Reset()   // clears switches and the log
```

## Checklist

| Edge case | Test |
| --- | --- |
| A client authenticates against the fake and picks `Secret` over a bad first secret | `TestFakeAuthAndSecretChoice` |
| `FetchBundle` against the fake gives `AppID` and the secrets | `TestFakeBundle` |
| Every fixture album, track, playlist, artist and label reads through the typed methods | `TestFakeServesFixtures` |
| Playlist and artist pages of 50 with `offset` | `TestFakePaging` |
| A wrong signature gets a 400 | `TestFakeRejectsBadSignature` |
| CDN honors `Range` with 206; `IgnoreRange` answers 200 | `TestFakeCDNRange` |
| `Fail404`, `FailTimes`, `CutAfter` | `TestFakeCDNFailures` |
| `Sample`, `EmptyAnswer`, `Restrict`, `Deliver` change `getFileUrl` | `TestFakeQualitySwitches` |
| `ExpireToken` gives 401 everywhere | `TestFakeExpireToken` |
| The fake works from another module: an example test in package `qobuzdltest_test` | `ExampleNew` |

## As built

Filled in at the end of the step.

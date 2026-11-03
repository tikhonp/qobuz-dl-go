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

Done on 2026-11-03. `make check` and `make lint` pass: 0 golangci-lint issues, no vulnerabilities.

### Differences from the contract

- `qobuzdl.Options` didn't exist yet. `core.go` now has `Options`, `DefaultOptions`, `DownloadsDB` and `DownloadRecord` as design.md defines them; step 9 adds the rest of `core.go`.
- `Configure` sets `APIBase`, `PlayBase`, `HTTPClient`, `Settings.UserAuthToken`, a zero `TrackPause` and `RetryDelays` of zeros, keeping their number. Options has no field for an app ID or secrets; `DL.GetTokens` reads them from the fake's bundle.
- `ClientOptions` gives `AppID`, the secrets `[BadSecret, Secret]` as `get_tokens` would read them from the bundle, `Token`, `ForceEnglish` and the fake's client. A new constant `BadSecret` names the bundle's first secret, and `UserID` the account `user/get` answers.
- `Request` also has `Header`, a copy of the request's headers, and `Status`, the answer's status code, so tests can check the token and which call got a 400.
- The audio keeps the fixture names from step 1: `16-44.1.flac` for 6, `24-96.flac` for 7, `24-192.flac` for 27 and `320.mp3` for 5. `cover.jpg` and `booklet.pdf` serve every cover and goody.
- `make golden` copies the fixtures through a new `write_fake_fixtures` in `gen.py`, which replaces `qobuzdltest/testdata/` on every run.
- The fake checks the X-App-Id and the token on every API call: 400 for a wrong app ID, 401 for a wrong or missing token. `ExpireToken` gives 401 on every API call; the bundle and the CDN keep working, since they don't use the token.
- `track/get` also answers for every track of a recorded album and of the recorded playlist, built as design.md says. A playlist download calls `track/get` for each of its tracks, and only one track/get answer is recorded.
- New constants for paging and empty answers: `PlaylistLong` and `ArtistLong`, the recorded playlist's and artist's items repeated to 120, and `PlaylistEmpty`, `ArtistEmpty` and `LabelEmpty`, whose first page has no items. Every collection is cut by `offset` and `limit`.
- `AlbumHiRes96` names the 24/96 album, which design.md's constant list lacked.
- `catalog/search` answers the recorded search of the requested type whatever the query.
- `getFileUrl` for a track nothing recorded builds an answer from the format table, so `cfg_setup`'s track 5966783 gets one. Its `url` names the delivered format, so the CDN serves that format's file; the recorded `url` named the requested one. `blob` becomes `qobuzdltest-blob`.
- `Restrict` at format 5 keeps format 5 and adds the restriction, since no tier is lower. `Deliver` panics on a format other than 5, 6, 7 or 27. `CutAfter` leaves HEAD requests alone, for step 8's segment sizes.
- `ExampleNew` runs against a small stand-in for `testing.TB`, since an Example has no `t`; `New` calls only `Helper`, `Cleanup` and `Fatalf`. A throwaway module outside the repo, not committed, also ran the fake through a `replace` directive.
- Extra tests: `TestFakeRejectsWrongAppIDAndToken`, `TestFakeConstantsMatchIndex`, `TestFakeTrackGetForListedTracks`, `TestFakeConfigure`, `TestFakeSwitchesUnderConcurrency`.

### Checks

`make golden` twice leaves the goldens and the copied fixtures unchanged. `TestFakeServesFixtures` reads all 367 recorded answers through the typed methods and compares each getFileUrl answer's format, bit depth, rate, MIME type and restrictions with its file.

### Notes for later steps

- Step 7: tests in package `qobuzdl` can't import `qobuzdltest`, which imports `qobuzdl`; that is an import cycle. The scenario tests go in package `qobuzdl_test`, with an `export_test.go` in package `qobuzdl` that exports `downloadIDByType` and whatever else they need.
- Step 7: no switch fails a cover or a goody. `TestExtras`' "cover failure not fatal" can point an album's image at a missing path, or step 7 can add a switch to design.md.
- Step 7: the CDN's URLs carry `?etsp=946684800&hmac=qobuzdltest-cdn-signature`; `TestDownloadLogsAreClean` can look for `hmac` in the logs.
- Step 7: the end-to-end goldens' Python side has to serve the same answers as the fake: the static URL rewrite doesn't change names or tags, but the getFileUrl `url` names the delivered format.
- Step 8: add `Segmented`, `session/start` and `file/url` to `serveAPI`, which answers 404 for them today, and check `request_sig` with `modernSig`'s rule.
- Step 9: Python's playlist download calls `get_track_meta` for every track. `gen.py`'s fake client has to build those answers as the fake does: the album's track with the album/get answer less `tracks` in front as `album`, or the playlist's item less `position`, `created_at` and `playlist_track_id`.

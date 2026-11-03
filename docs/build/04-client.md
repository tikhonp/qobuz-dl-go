# 4: bundle and API client

## Read first

- [design.md](../design.md) sections qopy.go and bundle.go, catalog.go, Errors, Transfer engine (the timeout part), Logs and secrets.
- [port-spec.md](../port-spec.md) sections Bundle and Client.
- Upstream `bundle.py`, `qopy.py`.
- The As built sections of steps 1 to 3.

## Done when

`bundle.go`, `qopy.go` (all but the segmented branch of `get_track_url`, which step 8 adds), `catalog.go` and `transport.go` exist; their goldens pass; tests run against small `httptest` handlers in `qopy_test.go`.

## Decisions

| Topic | Decision | Why |
| --- | --- | --- |
| Clock | `apiCall` reads `time.Now().Unix()`; the signature functions take `ts` as an argument, so goldens use fixed values. | No clock in `Options`. |
| Query order | Parameters are sent in Python's insertion order through a `[]kv`, not `url.Values`, which sorts. | Same request as Python. |
| Headers | Set per request from a fixed list in Python's order; the `force_english` set only when on. | Same request as Python. |
| `normalizeJSONStrings` | Applied to every decoded answer before anything reads it. | Tags and names depend on it. |
| Auth | `sys.exit` paths return `ErrAuthentication`; other `user/get` failures go on as "bypassed". | Upstream behavior without exiting. |
| Bundle inside `NewClient` | Ported: an empty or legacy app ID fetches the bundle, ignoring failures. | Same as Python. The bot hits it while the bundle carries `798273057`, as it did on 2026-11-03: two bundle fetches, and the client keeps the empty secret. |
| `cfg_setup` | Each secret tried in order; failure on all keeps the first, with a warning log the Python lacks. | Callers should see why later calls fail. |
| Search | Typed `SearchPage` with `offset` passed when non-zero; errors returned. | shelf pages search; deliberate difference. |
| Raw | `Raw` is the normalized dict marshaled in key order; `TrackURL.Raw` drops `url`, `url_template` and `key`. | Signed values stay out of callers' logs. |
| Segmented session state | Fields and a mutex exist now; the branch that uses them comes in step 8. | Keeps the struct stable. |

## Files

```
bundle.go  qopy.go  catalog.go  transport.go
bundle_test.go  qopy_test.go  catalog_test.go  transport_test.go
testdata/fixtures/bundle/login.html  bundle.js   (synthetic, written by gen.py)
testdata/golden/secrets.json  signatures.json  session_key.json  request_params.json
```

## Go signatures

```go
func FetchBundle(ctx context.Context, hc *http.Client, playBase string) (*Bundle, error)
func (b *Bundle) AppID() (string, error)
func (b *Bundle) Secrets() ([]Secret, error)

func NewClient(ctx context.Context, o ClientOptions) (*Client, error)
func (c *Client) AppID() string
func (c *Client) Label() string
func (c *Client) UserID() string
func (c *Client) GetAlbumMeta(ctx context.Context, id string) (*Album, error)
func (c *Client) GetTrackMeta(ctx context.Context, id string) (*Track, error)
func (c *Client) GetPlaylistMeta(ctx context.Context, id string) (*Playlist, error)
func (c *Client) GetArtistMeta(ctx context.Context, id string) (*Artist, error)
func (c *Client) GetLabelMeta(ctx context.Context, id string) (*Label, error)
func (c *Client) SearchAlbums(ctx context.Context, query string, limit, offset int) (*SearchPage[Album], error)
func (c *Client) SearchTracks(ctx context.Context, query string, limit, offset int) (*SearchPage[Track], error)
func (c *Client) SearchArtists(ctx context.Context, query string, limit, offset int) (*SearchPage[Artist], error)
func (c *Client) SearchPlaylists(ctx context.Context, query string, limit, offset int) (*SearchPage[Playlist], error)
func (c *Client) GetTrackURL(ctx context.Context, id string, fmtID int, forceSegments bool) (*TrackURL, error)

func (c *Client) apiCall(ctx context.Context, epoint string, kw []kv) (*pyDict, error)
func (c *Client) auth(ctx context.Context, email, pwd, token string) error
func (c *Client) cfgSetup(ctx context.Context) error
func (c *Client) multiMeta(ctx context.Context, epoint, key, id string) ([]*pyDict, error)
func (c *Client) getTrackURL(ctx context.Context, id string, fmtID int, force bool) (*pyDict, error)
func (c *Client) albumMeta(ctx context.Context, id string) (*pyDict, error)
func (c *Client) trackMeta(ctx context.Context, id string) (*pyDict, error)
func modernSig(epoint string, params []kv, ts int64, secret string) string
func getFileURLSig(trackID string, fmtID int, ts int64, secret string) string
func b64urlDecode(s string) ([]byte, error)
func deriveSessionKey(secret, infos string) ([]byte, error)
func unwrapTrackKey(sessionKey []byte, key string) ([]byte, error)
func normalizeJSONStrings(v any) any

func GetAlbumArtist(a *Album) []string   // get_album_artist over the typed view; getAlbumArtist returns []any, the names as the API holds them
func albumFromDict(d *pyDict) (*Album, error)   // and trackFromDict, playlistFromPages, artistFromPages, labelFromPages, trackURLFromDict

func apiContext(ctx context.Context, t Timeouts) (context.Context, context.CancelFunc)
func redactURL(u string) string
```

`deriveSessionKey` and `unwrapTrackKey` are written and tested here against goldens; step 8 calls them.

## Checklist

| Edge case | Test |
| --- | --- |
| Secret derivation from the fixture bundle, second timezone first, empty secret kept | `TestSecretsGolden` |
| A bundle with one timezone is an error | `TestSecretsNeedTwoTimezones` |
| No bundle script in the login page is `ErrBundle` | `TestFetchBundleNoScript` |
| `getFileUrl` and `_modern_sig` signatures for fixed timestamps | `TestSignaturesGolden` |
| `_modern_sig` skips `request_ts`, `request_sig` and non-scalar values | `TestModernSigSkipsKeys` |
| Session key and track key unwrap against `cryptography`'s output | `TestDeriveSessionKeyGolden`, `TestUnwrapTrackKeyGolden` |
| Query parameters and headers in Python's order, `force_english` on and off | `TestRequestParamsGolden` |
| A format outside 5, 6, 7, 27 is `ErrInvalidQuality` before any request | `TestGetFileURLRejectsQuality` |
| A 400 from `getFileUrl` is `ErrInvalidAppSecret` and an `*APIError` | `TestGetFileURL400IsInvalidSecret` |
| A 401 from `user/get` returns `ErrAuthentication` | `TestAuthExpired` |
| A 400 from `user/get` and a 500 both go on with `Studio` | `TestAuthBypassed` |
| No token and a short password is `ErrAuthentication` | `TestAuthNeedsToken` |
| A password over 60 characters is the token | `TestAuthPasswordAsToken` |
| Label and user ID from the top level or under `user` | `TestAuthLabelAndUserID` |
| The first working secret wins; none working keeps the first; an empty list fails | `TestCfgSetupSecrets` |
| `multi_meta` pages until `total`, stops on an empty page, falls back to `tracks_count` | `TestMultiMetaPaging` |
| `...` becomes `…` except in URLs, NFC applied, keys untouched | `TestNormalizeJSONStringsGolden` |
| Search sends the offset and returns the error | `TestSearchOffsetAndErrors` |
| API calls time out after `Timeouts.API` | `TestAPICallTimeout`, under synctest |
| Errors and logs never contain the token, secrets or signatures | `TestNoSecretsInErrorsOrLogs` |
| `GetAlbumArtist` on typed albums equals `getAlbumArtist` on their dicts | `TestGetAlbumArtistTyped` |
| Typed views from recorded answers: album with tracks, track with album, playlist and artist across pages | `TestCatalogFromFixtures` |
| `TrackURL.Raw` has no signed fields | `TestTrackURLRawIsClean` |
| Concurrent calls on one `Client` | `TestClientConcurrentCalls`, under `-race` |

## As built

Done on 2026-11-03. `make check` and `make lint` pass: 0 golangci-lint issues, no vulnerabilities.

### Differences from the contract

- `request_params.json` is a transcript. `gen.py` runs upstream's real `Client` offline: a requests transport answers each request from a small fake API and records it, `requests.Session` and `bundle.Session` are patched to use it, and `qopy.time` returns a fixed time. Each of the 58 cases holds the client's options, an optional call, every request with its URL, headers and body, every answer, and the client's state or the exception. `TestRequestParamsGolden` replays the answers through an `http.RoundTripper` and checks each request against Python's. The cases cover the bundle branch, every auth outcome, the secret choice, each endpoint the port calls and `multi_meta` paging.
- The transcripts sign with 946684800, midnight UTC on 2000-01-01, where `testing/synctest`'s clock starts. The replay runs under `synctest.Test`, so `apiCall` reads `time.Now()` with no clock hook.
- `Accept-Encoding`, `Connection` and `Content-Length` are left to Go's transport, and the replay skips them. Go's `http.Header` doesn't keep order on the wire; `(*Client).headers` lists the headers in requests' order, and the test checks that list.
- The bundle fetch sends requests' default `User-Agent: python-requests/2.34.2` and `Accept: */*`, as Python's plain `Session` does.
- `pyB64Decode` ports Python 3.14's lenient base64 decoding, which the bundle secrets and `_b64url_decode` go through. It drops characters outside the alphabet, keeps decoding after a `=`, and counts padding only after the last data character. `secrets.json` has 800 random strings through both decoders. `pyFromHex` ports `bytes.fromhex`.
- `apiCallValue` returns any JSON value, as `api_call` does, and `apiCall` wraps it for answers used as dicts. `cfg_setup` accepts any answer, so it uses `apiCallValue`.
- `apiCall` ports only the endpoints the port calls: `track/getFileUrl`, `session/start`, `file/url` and the standard calls. The `user/login`, `favorite/*` and `track/lyricsUrl` branches are not ported.
- The segmented branch of `getTrackURL` returns `errSegmentedNotPorted` until step 8. The `session/start` and `file/url` requests already exist in `apiCallValue`, and the golden covers them.
- `Client` has `mu` and `sessionID` but not yet the session's infos and key, because the linter rejects unused fields. Step 8 adds them.
- `Timeouts` and `DefaultTimeouts` live in `transport.go`. `fetch` gives each call its own timeout and sorts failures: the caller's context ending wraps `ctx.Err()`; the call's own timeout is a `timeoutError`, which doesn't match `context.DeadlineExceeded`, so auth's bypass and `get_track_url`'s fallback treat it like requests' `Timeout`; other failures are a `requestError` without the URL, since `url.Error` would print the signature. An answer that isn't JSON is a `requestError` too, like requests' `JSONDecodeError`.
- `TrackURL.Raw` also drops `blob`, a signed token, and `raw_key`.
- `GetPlaylistMeta`, `GetArtistMeta` and `GetLabelMeta` return `ErrEmptyContent` when the first page has no items.
- `GetAlbumArtist(*Album)` decodes `Raw` and runs `getAlbumArtist`, so it agrees with the dict version by construction. Without a readable `Raw` it falls back to the typed fields.
- `Label()` and `UserID()` return "" when Python's value is None.
- Extra tests: `TestBundleURLGolden`, `TestB64DecodeGolden`, `TestFetchBundleReadsFixture`, `TestFetchBundleHTTPError`, `TestAPICallTimeoutDuringAuthPropagates`, `TestGetTrackURLWithoutURLNeedsSegments`, `TestCatalogEmptyCollections`, `TestCatalogRawKeepsOrder`, `TestRedactURL`, `TestAPIContextDefaultsTo30Seconds`, `TestFetchErrorsHideTheURL`, `TestFetchKeepsCallerCancel`.

### Checks

`make golden` twice gives the same files. Changing one header value in `qopy.go` fails the replay on every request that carries it.

### Notes for later steps

- Step 6: `testdata/fixtures/bundle/` carries app ID `123456789`, the good secret `5e1f0c2a9b7d4e3f8a6c1b0d2e4f6a8c` and the bad one `0f9e8d7c6b5a49382716051423324150`. In Python's order they are berlin (bad), london (good) and abidjan (empty). `qobuzdltest.AppID` and `Secret` should use these values so the fake serves the files as they are. The login page names `/resources/8.1.0-b019/bundle.js`.
- Step 6: `fakeAPI` in `qopy_test.go` checks getFileUrl signatures the way the fake should.
- Step 7: the downloader calls `getTrackURL` and `albumMeta` for dicts. A `timeoutError` or `requestError` from them is a `RequestException` in Python terms; `isRequestError` should match both, plus `*APIError`.
- Step 8: add `sessionInfos` and `sessionKey` to `Client`, `startSession` under `mu`, and replace `errSegmentedNotPorted`. For a format outside 5, 6, 7 and 27, Python's `get_track_url` sends `session/start` before `file/url` raises `InvalidQuality`. `request_params.json` needs cases for the segmented branch; `file_url_answer` in `gen.py` already builds a wrapped key under the session key from `session_infos(1)`.

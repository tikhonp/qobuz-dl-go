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

func GetAlbumArtist(a *Album) []string   // get_album_artist over the typed view
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

Filled in at the end of the step.

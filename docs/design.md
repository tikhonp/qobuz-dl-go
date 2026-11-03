# Design

How qobuz-dl-go ports qobuz-dl 2.5.9. Upstream behavior is in [port-spec.md](port-spec.md); the steps that build this are in [build/](build/). Each section answers questions from [design-questions.md](design-questions.md).

## Package layout

One package, `qobuzdl`, at the repo root. Python's modules call each other freely, and one package keeps those calls without exporting internals. Subpackages exist only for parts with their own dependencies or users.

```
constants.go      constants.py
errors.go         exceptions.py, plus APIError and the Go-only sentinels
settings.go       settings.py
bundle.go         bundle.py
qopy.go           qopy.py
utils.go          utils.py
metadata.go       metadata.py
downloader.go     downloader.py
core.go           core.py

pyvalue.go        the JSON value model, dict.get semantics, str(), truthiness, Python exception kinds
pystr.go          Python string methods: casefold, lower, title, capitalize, strip, isdigit, slicing by code point
pyre.go           Unicode \w, \W, \s, \b, and the hand-written matchers for the regexes RE2 can't run
format.go         str.format
pathvalidate.go   pathvalidate 3.3.1 sanitize_filename and sanitize_filepath, universal platform
textwrap.go       textwrap.fill
flac.go           FLAC metadata blocks, read and written without loading the audio
catalog.go        the typed views: Album, Track, Playlist, Artist, Label, SearchPage, TrackURL
progress.go       Progress, Result and their types
transport.go      timeouts, retries' sleeps, URL redaction

downloadsdb/      the SQLite downloads table, Python-compatible
qobuzdltest/      a fake Qobuz API and CDN for tests here and in shelf
cmd/qobuz-dl/     the CLI
testdata/gen/     gen.py, record.py, parity.py, compare.py, requirements.txt
testdata/fixtures/  inputs to gen.py: recorded API answers, string corpora, template matrices
testdata/golden/  gen.py's output, committed
```

The root package never imports `downloadsdb`, so it stays free of SQLite. `qobuzdltest` and `downloadsdb` import the root package for its types.

Go 1.27.3, as in the user's other projects. Module `github.com/tikhonp/qobuz-dl-go`. `CGO_ENABLED=0` everywhere; modernc SQLite is pure Go.

Dependencies:

| Module | For |
| --- | --- |
| `golang.org/x/text` | NFC, case folding, full Unicode lower and upper case |
| `github.com/go-flac/go-flac`, `flacvorbis`, `flacpicture` | FLAC block types and the VORBIS_COMMENT and PICTURE codecs |
| `github.com/bogem/id3v2/v2` | ID3v2.3 |
| `modernc.org/sqlite` | `downloadsdb` only |

HKDF comes from the standard library's `crypto/hkdf`.

## Python compatibility

### Values

Inside the package, API answers are dynamic values, ported line by line from Python. The typed views in `catalog.go` are built from them for callers. The user chose this over typed structs everywhere: Python's behavior depends on a key being missing, null or set, and on `96` against `96.0`, and typed structs lose both.

```go
// A Python value decoded from JSON: nil (None), bool, int64, float64, string, []any or *pyDict.
type pyDict struct {
    keys []string          // insertion order, as Python 3.7+ dicts keep it
    vals map[string]any
}

func decodeJSON(r io.Reader) (any, error)   // a number without '.', 'e' or 'E' is int64, else float64, as Python's json
func newDict() *pyDict
func (d *pyDict) get(key string, def any) any     // dict.get: def only when the key is missing
func (d *pyDict) item(key string) (any, error)    // d[key]: pyKeyError when missing
func (d *pyDict) set(key string, v any)
func (d *pyDict) has(key string) bool
func (d *pyDict) dict(key string) (*pyDict, error) // d.get(key, {}) used as a dict: pyAttributeError on None or a non-dict
func (d *pyDict) list(key string) ([]any, error)   // d.get(key, []) used as a list
func (d *pyDict) clone() *pyDict                   // deep copy
func (d *pyDict) MarshalJSON() ([]byte, error)     // for Raw, keys in order

func pyStr(v any) string         // str(v): "None", "True", "96", "96.0", "44.1", "1e+16"
func pyTruthy(v any) bool        // "", 0, 0.0, [], {}, None are false
func pyInt(v any) (int64, error) // int(v), pyValueError or pyTypeError as Python
```

`pyStr` prints floats as Python's `repr`: the shortest digits that round-trip, fixed notation when the decimal exponent is from −4 to 15 with `.0` added to whole numbers, scientific notation otherwise with at least two exponent digits. Goldens cover the edges.

### Exceptions as errors

The user chose explicit error returns over panics. Where Python would raise, the Go code returns an error of the same kind, and each Python `try/except` becomes a check for that kind. The code reads like the Python with an `if err != nil` after each fallible line.

```go
type pyKind int
const (
    pyKeyError pyKind = iota + 1
    pyTypeError
    pyValueError
    pyAttributeError
    pyIndexError
    pyOverflowError // int(inf), format(-1, "c")
)

type pyError struct{ kind pyKind; msg string }
func (e *pyError) Error() string           // "KeyError: 'title'", as Python prints it
func pyErr(kind pyKind, format string, args ...any) error
func isPy(err error, kinds ...pyKind) bool  // errors.As, then the kind
```

Catch points map as follows:

| Python | Go |
| --- | --- |
| `except KeyError` | `isPy(err, pyKeyError)` |
| `except (KeyError, ValueError)` | `isPy(err, pyKeyError, pyValueError)` |
| `except requests.exceptions.RequestException` | `isRequestError(err)`: `*APIError`, `*url.Error`, a transport error, a timeout |
| `except NonStreamable` | `errors.Is(err, ErrNonStreamable)` |
| `except Exception` | any error except `context.Canceled` and `context.DeadlineExceeded`, which always propagate |

A Python exception that nothing catches would crash the Python CLI. In Go it leaves the exported function wrapped in `ErrUnhandled`, with the kind and message: `fmt.Errorf("%w: %v", ErrUnhandled, err)`. A worker goroutine returns its error like a Python future does.

### Strings

`pystr.go` holds the string methods the port calls, each with goldens over a Unicode corpus:

| Python | Go | Note |
| --- | --- | --- |
| `len(s)`, `s[a:b]` | `pyLen`, `pySlice` | code points, negative indices |
| `s.lower()`, `s.upper()` | `pyLower`, `pyUpper` | full case mapping from `x/text/cases`, one character at a time as CPython does, so `İ` lowers to `i̇`; final sigma by CPython's rule |
| `s.casefold()` | `pyCasefold` | `cases.Fold()`, except Cherokee capitals, which fold to themselves |
| `s.title()`, `s.capitalize()` | `pyTitle`, `pyCapitalize` | Python's rules: title case after any non-letter |
| `s.strip(chars)`, `lstrip`, `rstrip` | `pyStrip`, … | no argument strips Python's whitespace set, which is wider than `unicode.IsSpace` |
| `s.isdigit()` | `pyIsDigit` | Unicode digits, `²` included |
| `s.split(sep)` | `strings.Split` | with `sep` given they agree |
| `s.ljust(n)`, `f"{s:<60}"` | `pyLjust` | pads by code points |
| `unicodedata.normalize("NFC", s)` | `pyNFC` | `norm.NFC` between the code points Unicode 17.0 added, which Python sees as unassigned starters |

### Regular expressions

Python's `re` treats `\w`, `\W`, `\s`, `\d` and `\b` as Unicode classes; RE2 treats them as ASCII. Patterns use explicit classes from `pyre.go`:

```go
var reW, reNotW, reS, reNotS, reD, reNotD string // [...] and [^...]
var reWordChars, reSpaceChars, reDigitChars string // the insides, to combine with other characters
```

Python's `\w` is `str.isalnum()` or `_`, which is the L and N categories and no marks. Go 1.27's tables are Unicode 17.0 and Python 3.14's are 16.0, so the classes are built from Go's tables minus `newerThanPython`, the code points 17.0 added. The `regex.json` and `unicode.json` goldens check them code point by code point.

`clean_filename`'s step 4 uses lookbehind, lookahead and `\b`, which RE2 lacks. It is a hand-written scanner in `pyre.go` with goldens over random strings. Every regex in the port lists its Python source in a comment.

### str.format

Templates are applied with plain `str.format(**attrs)`; `PartialFormatter` is defined upstream but never used, so it is not ported. `format.go` implements the parts of the format language a template can reach:

```go
type pyAttrs map[string]any
func pyFormat(tmpl string, attrs pyAttrs) (string, error)
```

- Literal text, `{{` and `}}`.
- Fields `{name}`, `{name[index]}` and `{name.attr}`. A missing name is `pyKeyError`; `.attr` on a value without it is `pyAttributeError`; an index on a string takes a code point and out of range is `pyIndexError`. Positional fields `{}` and `{0}` raise `pyIndexError`, since there are no positional arguments.
- Conversions `!s`, `!r`, `!a`.
- The format spec `[[fill]align][sign][z][#][0][width][grouping][.precision][type]` for `str`, `int`, `float`, `bool` and `None`, with Python's errors: `format("1", "02")` is `10`, `format(None, "02")` is a `pyTypeError`, `format("x", "d")` a `pyValueError`.
- Nested fields in specs, such as `{x:{w}}`.
- An unbalanced `{` or `}` is `pyValueError`.

Goldens run a matrix of templates and values through Python and compare the result or the exception kind.

### pathvalidate

`pathvalidate.go` ports `sanitize_filename` and `sanitize_filepath` from pathvalidate 3.3.1, the version the golden venv pins, for the universal platform with the default `max_len` and `fs_encoding`:

```go
func sanitizeFilename(name, replacement string) string
func sanitizeFilepath(path, replacement string) string
```

The port follows pathvalidate's source: invalid characters, reserved names, trailing spaces and dots, and the cut to 255 UTF-8 bytes, 260 per entry in a path. The port also has the validation `sanitize_filepath` ends with, which empties a relative path that has a blank entry. Goldens over a corpus with random Unicode, control characters, reserved names in any case and with extensions, and names up to 1,000 bytes decide the details.

### textwrap

`textwrap.go` ports `textwrap.fill(text, width=70)` with its defaults: tabs expanded, each ASCII whitespace character turned into a space, words split by `TextWrapper.wordsep_re` with hyphen breaking, whitespace dropped at line ends but kept inside lines, long words broken. `wordsep_re` needs lookbehind, so a scanner replaces it. The tracklist uses it; goldens over album descriptions cover it.

## Public API

Signatures below are the contract; the steps may add unexported helpers only.

### core.go

```go
type DL struct{ /* opts, client, appID, secrets; no other state */ }

// New ports QobuzDL.__init__. It makes Directory absolute and creates it, as create_and_return_dir does.
func New(opts Options) (*DL, error)

// GetTokens ports get_tokens: fetches the bundle, keeps the app ID and the non-empty secrets.
func (d *DL) GetTokens(ctx context.Context) error
func (d *DL) AppID() string
func (d *DL) Secrets() []string

// InitializeClient ports initialize_client. The token is Settings.UserAuthToken, or pwd when it is longer than 60 characters.
func (d *DL) InitializeClient(ctx context.Context, email, pwd, appID string, secrets []string) error
func (d *DL) Client() *Client

// DownloadFromID ports download_from_id.
func (d *DL) DownloadFromID(ctx context.Context, req DownloadRequest) (*Result, error)

// HandleURL ports handle_url: it parses the URL and calls HandleID.
func (d *DL) HandleURL(ctx context.Context, url string, p Progress) (*Result, error)

// HandleID is handle_url after get_url_info. Kind is album, track, playlist, artist or label.
func (d *DL) HandleID(ctx context.Context, kind, id string, p Progress) (*Result, error)

// SearchByType ports search_by_type without lucky mode; Text is the line Python shows.
func (d *DL) SearchByType(ctx context.Context, query, itemType string, limit int) ([]SearchResult, error)

type DownloadRequest struct {
    ID            string
    Album         bool     // album=True in Python; false downloads a track
    AltPath       string   // alt_path; empty means Options.Directory
    IsPlaylist    bool
    PlaylistIndex int      // 1-based; 0 means none
    IgnoreDB      bool
    Progress      Progress // nil allowed
}

type SearchResult struct{ Text, URL string }
```

`HandleID` is a Go-only split of `handle_url`, so shelf can download a playlist by ID with its m3u8; the user chose it over widening `DownloadFromID`. Shelf's 1d docs say playlists go through `DownloadFromID` and need that line changed.

### qopy.go and bundle.go

```go
type ClientOptions struct {
    Email, Password string
    AppID           string
    Secrets         []string
    UserAuthToken   string
    ForceEnglish    bool
    HTTPClient      *http.Client  // required
    Logger          *slog.Logger  // nil discards
    APITimeout      time.Duration // per call; 0 means 30s
    APIBase         string        // "" means https://www.qobuz.com/api.json/0.2/
    PlayBase        string        // "" means https://play.qobuz.com
}

// NewClient ports Client.__init__, auth and cfg_setup.
func NewClient(ctx context.Context, o ClientOptions) (*Client, error)

func (c *Client) AppID() string
func (c *Client) Label() string   // the subscription label, "Studio" by default
func (c *Client) UserID() string  // "" when user/get gave none

func (c *Client) GetAlbumMeta(ctx context.Context, id string) (*Album, error)
func (c *Client) GetTrackMeta(ctx context.Context, id string) (*Track, error)
func (c *Client) GetPlaylistMeta(ctx context.Context, id string) (*Playlist, error) // every page
func (c *Client) GetArtistMeta(ctx context.Context, id string) (*Artist, error)     // every page
func (c *Client) GetLabelMeta(ctx context.Context, id string) (*Label, error)       // every page
func (c *Client) SearchAlbums(ctx context.Context, query string, limit, offset int) (*SearchPage[Album], error)
func (c *Client) SearchTracks(ctx context.Context, query string, limit, offset int) (*SearchPage[Track], error)
func (c *Client) SearchArtists(ctx context.Context, query string, limit, offset int) (*SearchPage[Artist], error)
func (c *Client) SearchPlaylists(ctx context.Context, query string, limit, offset int) (*SearchPage[Playlist], error)
func (c *Client) GetTrackURL(ctx context.Context, id string, fmtID int, forceSegments bool) (*TrackURL, error)

type Bundle struct{ /* the bundle text */ }
func FetchBundle(ctx context.Context, hc *http.Client, playBase string) (*Bundle, error)
func (b *Bundle) AppID() (string, error)
func (b *Bundle) Secrets() ([]Secret, error) // in Python's order, empty values included
type Secret struct{ Timezone, Value string }
```

Unexported, ported one to one: `apiCall(ctx, epoint string, kw []kv) (*pyDict, error)`, `auth`, `cfgSetup`, `modernSig`, `b64urlDecode`, `deriveSessionKey`, `unwrapTrackKey`, `normalizeJSONStrings`, `multiMeta(ctx, epoint, key, id string) ([]*pyDict, error)`, and `getTrackURL(ctx, id string, fmtID int, force bool) (*pyDict, error)`, which the downloader uses. `kv` keeps keyword arguments in Python's order, so query strings match.

The `Client` is safe for concurrent use. `NewClient` sets the secret once; the first segmented download creates the session under a mutex, where Python races.

`sys.exit(1)` on a missing token or a 401 becomes `ErrAuthentication`. Python's "Profile validation bypassed" for other `user/get` failures is kept: label `Studio`, no user ID, no error.

### catalog.go

Typed views for callers. The client builds each from the normalized `*pyDict`; `Raw` holds that dict as JSON, so a caller can reach a field the view lacks. IDs are strings: Qobuz sends album IDs as strings and track IDs as numbers, and `pyStr` turns both into text.

```go
type Album struct {
    ID, Title, Version  string
    Artist              ArtistRef
    Artists             []ArtistRef      // artists[] with roles
    ReleaseDateOriginal string
    Label               LabelRef
    UPC, Copyright      string
    Genre               string           // genre.name
    GenresList          []string
    ParentalWarning     bool
    TracksCount, MediaCount, Duration int
    Image               Image
    URL                 string
    Streamable, HiresStreamable bool
    MaximumBitDepth     int
    MaximumSamplingRate float64
    ReleaseType, ProductType string
    Tracks              []Track          // tracks.items, when the answer has them
    TracksTotal         int              // tracks.total
    Goodies             []Goody
    Raw                 json.RawMessage
}

type Track struct {
    ID, Title, Version, ISRC, Work string
    TrackNumber, MediaNumber, Duration int
    Performer           ArtistRef
    Composer            ArtistRef
    Performers          string
    ParentalWarning     bool
    MaximumBitDepth     int
    MaximumSamplingRate float64
    HiresStreamable, Streamable bool
    ReleaseDateOriginal string
    Album               *Album           // album inside track/get and search results
    Raw                 json.RawMessage
}

type Playlist struct {
    ID, Name, Description string
    Owner                 string
    TracksCount, Duration int
    Images                []string
    Tracks                []Track
    Raw                   json.RawMessage // the first page
}

type Artist struct {
    ID, Name    string
    AlbumsCount int
    Image       Image
    Albums      []Album
    Raw         json.RawMessage
}

type Label struct {
    ID, Name    string
    AlbumsCount int
    Albums      []Album
    Raw         json.RawMessage
}

type ArtistRef struct{ ID, Name string; Roles []string }
type LabelRef struct{ ID, Name string }
type Image struct{ Small, Thumbnail, Large, Back string }
type Goody struct{ ID, Name, URL string }

type SearchPage[T any] struct {
    Items         []T
    Total         int
    Limit, Offset int
}

type TrackURL struct {
    URL          string  // direct; empty on the segmented path
    FormatID     int
    MimeType     string
    BitDepth     int
    SamplingRate float64
    Sample       bool
    Restrictions []string // restriction codes
    Segmented    bool
    NSegments    int
    Raw          json.RawMessage // with url, url_template, key, raw_key and blob removed: they are signed or secret
}
```

The typed views never feed back into the port; names and tags always come from the dicts.

### Errors

`errors.go`:

```go
var (
    ErrAuthentication   = errors.New("qobuzdl: authentication failed")     // AuthenticationError, and Python's sys.exit on auth
    ErrIneligible       = errors.New("qobuzdl: account not eligible")      // IneligibleError, unused upstream
    ErrInvalidAppID     = errors.New("qobuzdl: invalid app ID")            // InvalidAppIdError, unused upstream
    ErrInvalidAppSecret = errors.New("qobuzdl: invalid app secret")        // InvalidAppSecretError
    ErrInvalidQuality   = errors.New("qobuzdl: invalid quality")           // InvalidQuality
    ErrNonStreamable    = errors.New("qobuzdl: release not streamable")    // NonStreamable

    ErrBundle           = errors.New("qobuzdl: bundle not understood")     // bundle.py's NotImplementedError
    ErrInvalidURL       = errors.New("qobuzdl: not a Qobuz URL")
    ErrEmptyContent     = errors.New("qobuzdl: content empty or unavailable")
    ErrQueryTooShort    = errors.New("qobuzdl: search query shorter than 3 characters")
    ErrUnhandled        = errors.New("qobuzdl: exception upstream does not handle")
)

type APIError struct {
    Endpoint string // "album/get"
    Status   int
    Body     string // at most 200 bytes, with the token and secrets cut out
}
func (e *APIError) Error() string // "qobuzdl: album/get: HTTP 404: <body>"
func newAPIError(endpoint string, status int, body []byte, secrets ...string) *APIError
```

`newAPIError` replaces each secret with `[redacted]` before it cuts the body at 200 bytes, so no prefix of a secret survives the cut.

A 400 from the signed endpoints wraps `ErrInvalidAppSecret` around an `*APIError`, so both `errors.Is` and `errors.As` work. Errors never contain the token, a secret, a signature or a CDN query string.

## Settings and options

`settings.go` ports `QobuzDLSettings`; `Options` in `core.go` holds `QobuzDL`'s arguments and the Go-only knobs. Zero values are not Python's defaults, so callers start from `DefaultOptions()`.

```go
type Settings struct {
    FallbackFolderFormat    string   // DEFAULT_FOLDER
    LegacyCharmap           bool
    NoAlbumArtistTag, NoAlbumTitleTag, NoTrackArtistTag, NoTrackTitleTag     bool
    NoReleaseDateTag, NoMediaTypeTag, NoGenreTag                               bool
    NoTrackNumberTag, NoTrackTotalTag, NoDiscNumberTag, NoDiscTotalTag         bool
    NoComposerTag, NoConductorTag, NoEnsembleTag, NoWorkTag                    bool
    NoExplicitTag, NoCopyrightTag, NoLabelTag, NoUPCTag, NoISRCTag             bool
    NoReplayGainTag, NoAlbumURLTag                                             bool
    MultiValueTags          bool
    DisableSmartTruncation  bool
    VariousArtistsAliases   []string // ParseAliases(DEFAULT_VARIOUS_ARTISTS_ALIASES)
    EmbedArt                bool     // settings.embed_art: fetch embed_cover.jpg
    NoCover                 bool
    EmbeddedArtSize         string   // "600"
    SavedArtSize            string   // "org"
    MultipleDiscPrefix      string   // "CD"
    MultipleDiscOneDir      bool
    MultipleDiscTrackFormat string   // DEFAULT_MULTIPLE_DISC_TRACK
    MaxWorkers              int      // 3
    UserAuthToken           string
    Delay                   time.Duration // whole seconds in Python; negative means 0
}
func DefaultSettings() Settings
func ParseAliases(s string) []string   // parse_aliases: split on commas, strip, casefold, drop empties, de-duplicate

type Options struct {
    Directory         string // "QobuzDownloads"
    Quality           int    // 6
    EmbedArt          bool   // QobuzDL.embed_art: embed embed_cover.jpg when present
    IgnoreSinglesEPs  bool
    NoM3UForPlaylists bool
    QualityFallback   bool   // true
    DownloadsDB       DownloadsDB // nil: no database
    FolderFormat      string // "{artist} - {album} ({year}) [{bit_depth}B-{sampling_rate}kHz]"
    TrackFormat       string // "{track_number} - {track_title}"
    SmartDiscography  bool
    ForceEnglish      bool   // true
    NoCredits         bool
    Settings          Settings

    HTTPClient  *http.Client    // required
    Logger      *slog.Logger    // nil discards
    FFmpegPath  string          // "ffmpeg"
    Timeouts    Timeouts
    TrackPause  time.Duration   // 1s, the sleep before each track
    RetryDelays []time.Duration // 2s, 4s, 8s, 16s
    APIBase, PlayBase string    // "" for the real services
}
func DefaultOptions() Options

type Timeouts struct {
    API         time.Duration // 30s per API call
    Connect     time.Duration // 10s from asking for a connection to having one
    Idle        time.Duration // 60s without a byte, headers included
    SegmentHead time.Duration // 5s per HEAD
    Segment     time.Duration // 15s connect and idle per segment GET
}
```

`New` rejects `MaxWorkers` below 1, a nil `HTTPClient` and a quality outside 5, 6, 7 and 27.

Both embed flags stay, because upstream has both: `Settings.EmbedArt` fetches `embed_cover.jpg`, `Options.EmbedArt` embeds it. The bot runs with both off. The two disc-format settings, `multiple_disc_one_dir` and the folder template, are copied per call before a playlist overrides them, where Python changes the shared object; the files come out the same and the `DL` stays safe for concurrent calls.

Not ported: the settings only the Python CLI and config file read (`email`, `password`, `default_*`, `no_m3u`, `albums_only`, `no_fallback`, `no_database`, `app_id`, `secrets`, `folder_format`, `track_format`, `smart_discography` as config mirrors), the lyrics settings, `og_cover` and `QobuzDL`'s `cover_og_quality` and `no_cover`, which nothing reads, `filter_label`, `playlist_as_albums`, `booklet_only` and the blacklist. `UPSTREAM.md` lists them.

## Progress and Result

`progress.go`. One interface replaces tqdm, `safe_print` and the colored lines. Calls come from several goroutines; implementations must be safe for that. A nil `Progress` is allowed.

```go
type Progress interface {
    ReleaseStart(r ReleaseInfo)
    TrackStart(t TrackInfo)
    TrackBytes(position int, done, total int64)
    TrackEnd(t TrackResult)
    Log(level slog.Level, msg string)
}

type ReleaseInfo struct {
    Kind     string        // album, track, playlist
    ID       string
    Title    string
    Folder   string        // the work folder: [IN PROGRESS] <name> for albums
    Album    *Album        // nil for a playlist
    Playlist *Playlist
    Tracks   []PlannedTrack
}

type PlannedTrack struct {
    Position                 int // 1-based: the index in tracks.items, or the playlist position
    ID, Title, ISRC          string
    DiscNumber, TrackNumber  int
    Duration                 int
}

type TrackInfo struct {
    Position  int
    ID, Title string
    FormatID  int  // the tier being transferred
    Segmented bool
}
```

- `ReleaseStart` fires once per album or single track, after the folder exists and before the extras. A playlist fires it once with every track planned, and its tracks fire no `ReleaseStart` of their own. Artists and labels fire one per album.
- `TrackStart` fires when a transfer starts, once per tier tried.
- `TrackBytes` fires at most every 250 ms per track and once more when the transfer ends. `total` is 0 when unknown. Covers and goodies don't report bytes.
- `TrackEnd` fires once per planned track, also for tracks that fail before a transfer or never start because the context ended. Its `Path` is in the work folder; the `Result` has the final one.
- `Log` gets the lines Python prints or logs at info level and above, without colors. The same lines go to the `*slog.Logger`, which also gets debug lines.

```go
type Result struct {
    Kind     string    // album, track, playlist, artist, label
    ID       string
    Folder   string    // the album or track folder, or the playlist, artist or label folder
    Releases []Release // one for an album or track; one per item for collections
    M3U      string    // the playlist file, when written
}

func (r *Result) Tracks() []TrackResult // every release's tracks, in order

type Release struct {
    Kind         string // album or track
    ID           string
    Status       Status
    Skip         SkipReason
    Folder       string // final: <name> or [INCOMPLETE] <name>
    Album        *Album // the album/get answer, or the album inside track/get
    Track        *Track // single tracks
    Format       string // the gate's format: FLAC, MP3 or Unknown
    QualityMet   bool
    BitDepth     int     // the gate's values; 0 when None
    SamplingRate float64
    Tracks       []TrackResult
    Err          error   // why it failed or stopped
}

type Status int
const (
    StatusDone Status = iota + 1 // every track done or already there
    StatusPartial                // some tracks failed
    StatusFailed                 // the release could not start, or every track failed
    StatusSkipped                // see Skip
)

type SkipReason int
const (
    SkipNone SkipReason = iota
    SkipInDatabase      // already in the downloads database
    SkipQuality         // below the requested quality without fallback
    SkipAlbumsOnly      // a single, EP or various-artists release with IgnoreSinglesEPs
    SkipDemo            // a single track whose getFileUrl answer was empty
)

type TrackResult struct {
    Position                int
    ID, Title, ISRC         string
    DiscNumber, TrackNumber int
    Path                    string  // the final file, in the final folder; "" when failed
    FormatID                int     // delivered
    Codec                   string  // FLAC or MP3
    BitDepth                int     // delivered, from the tier's answer
    SamplingRate            float64
    Outcome                 Outcome
    Err                     error
}

type Outcome int
const (
    OutcomeDone Outcome = iota + 1
    OutcomeSkipped // the file existed
    OutcomeFailed
)
```

- Every download function returns a `*Result`, also when it fails partway. The error is non-nil only when nothing could start (bad URL, auth, a `NonStreamable` album, the downloads database failing), when the context ended, or on `ErrUnhandled`.
- Paths are absolute. Track paths point into the final folder: when `[IN PROGRESS]` is renamed, the paths are rewritten to match.
- A skipped track's format comes from its file: FLAC STREAMINFO gives bit depth and rate, an `.mp3` gives format 5.
- shelf's `Delivered` line is the highest `BitDepth` and `SamplingRate` over the tracks; the library leaves that to the caller.

## Downloads

### Flow

`downloader.go` ports `Download` as an unexported `download` struct, built per call with copies of the options it needs. It holds no state between calls. Its methods are the Python ones: `downloadIDByType`, `downloadRelease`, `downloadTrack`, `downloadAndTag`, `getFormat`, `determineFormats`, `generateTracklist`, and the helpers. Port-spec's Downloads section is the behavior.

The Python `abort_event` becomes the context. Every place that checks `abort_event.is_set()` checks `ctx.Err()`.

### Worker pool

The main loop calls `getTrackURL` for each track in order, then hands the track to a pool of `MaxWorkers` goroutines, or one when `Delay` is above 0. Submission never blocks, as with `ThreadPoolExecutor`. The main loop puts each track's index on a queue with room for every track, and the workers take from it in order, so tracks start in submission order. The main loop collects the results in submission order. Files, temp files and log lines then come in Python's order.

### Cancellation

When the context ends:

1. The main loop starts no new track and skips the rest.
2. Running transfers stop at their next read; the request's context is the call's.
3. Each track's temp file and `.mp4` are removed.
4. The downloader renames an album folder in `[IN PROGRESS]` to `[INCOMPLETE]`, as Python does on Ctrl+C. A resumed download continues from it.
5. The call returns its `Result` and an error wrapping `ctx.Err()`.

Python sleeps 1.5 seconds and calls `os._exit(1)`; the port does neither.

### Temp files

A track that fails after its temp file exists removes it, with its `.mp4`. Python leaves them for the CLI to clean up; that difference is in `UPSTREAM.md`. A tagging failure removes the temp file too.

### Transfer engine

`transfer(ctx, url, fname string, onBytes func(done, total int64)) error` ports `tqdm_download` with its attempts, `Range` resume, the 2.5.8 restart on a 200, 416, the 404 rule, the overshoot and short-read rules and the kept offset bug. Port-spec's Transfers section is the behavior.

Timeouts mirror requests' `timeout=(10, 60)` on the caller's client, without touching its transport:

- An `httptrace.ClientTrace` starts the `Connect` timer at `GetConn` and stops it at `GotConn`.
- An idle timer of `Idle` runs from `GotConn` to the first response byte, then restarts on every body read.
- Either timer cancels a context derived from the call's. The error says which timed out.

API calls get `context.WithTimeout(ctx, Timeouts.API)`. shelf must hand over a client without `http.Client.Timeout`, since that would cut long transfers; shelf's 1d contract says otherwise and needs that line changed.

Sleeps between attempts use `RetryDelays` and end early when the context does. `TrackPause` is the one-second sleep before each track. Tests set both to zero or run under `testing/synctest`.

Every request goes through `Options.HTTPClient`, including covers, goodies and segments, where Python opens a new session per attempt.

### Segmented path

`transferSegments` ports `tqdm_download_segments`: sizes by HEAD with 8 workers, segments 0 and 1 in order, the rest with 8 workers written in order, decryption with `qobuzSegmentUUID` and `decryptQobuzSegment`, then the remux:

```go
exec.CommandContext(ctx, opts.FFmpegPath, "-nostdin", "-v", "error", "-y", "-i", tmp+".mp4", "-c:a", "copy", "-f", "flac", tmp)
```

The remux is the only external process. A missing ffmpeg fails the tier, and the next tier runs, as in Python. Decryption is pure Go: `crypto/hkdf`, `crypto/aes` with `cipher.NewCBCDecrypter` and `cipher.NewCTR`. The CTR counter is the counter bytes zero-padded to 16, incremented as a 128-bit big-endian number, as `cryptography`'s CTR mode does.

### Naming

`processFolderFormatWithSubdirs`, `getAlbumAttr`, `getTrackAttr`, `getFilenameAttr`, `determineFormats`, `cleanFormatStr`, `isNameTooLong`, `shortenUTF8` and `fitName` port their Python namesakes over `pyAttrs`. Attribute values keep their Python types, so `str.format` prints `96` and `96.0` as Python does. `isNameTooLong` calls `os.Stat` and checks for `syscall.ENAMETOOLONG`, as Python does with `errno`.

### Extras and m3u8

`getExtra`, `downloadGoodies`, `cleanEmbedArt` and `generateTracklist` port their namesakes. The tracklist uses `pyLjust` and `textwrap.fill`. `MakeM3U(dir string, items []*pyDict) error` reads tags back through `flac.go` and `id3v2`, the length from FLAC STREAMINFO as `int(samples / rate)`, and for MP3 from the Xing header, else from the frame count of a constant bitrate; the goldens on fixture files decide.

Python lists files in the order the OS gives them, which only matters when two files tie on a title or for unmatched items. The port sorts names, and `UPSTREAM.md` lists that.

Files are created 0644 and folders 0755, whatever the umask leaves.

## Tagging

`metadata.go` ports `getTagsToAdd`, which returns the tags as a `*pyDict`, the ordered dict Python builds, with values still strings, lists or whatever the API held, so the golden compares it directly. `TagFLAC` and `TagMP3` write it.

The user asked for the libraries, with workarounds only where they are needed. Three are:

- **FLAC.** `go-flac`'s `File` reads every audio frame into memory, which for three 24/192 tracks at once is hundreds of megabytes. `flac.go` reads the `fLaC` marker and the metadata blocks itself, builds the new blocks with `flacvorbis` and `flacpicture`, and writes them, followed by the audio copied with `io.Copy`, to a sibling file that then replaces the temp file. It keeps mutagen's rules: the blocks stay in file order, a VORBIS_COMMENT block added to a file without one goes after them, new PICTURE blocks after that, the old PADDING blocks go and one of 8,192 bytes comes last; a key set again loses its old comments, ignoring case, and goes to the end; the vendor string is empty. The output is byte for byte mutagen's.
- **MP3 order and padding.** `id3v2` writes frames in map order, so frame order varies from run to run, and adds no padding. Neither shows in a player, so both stay.
- **MP3 frames.** `id3v2` v2.1.4 writes UTF-16 text with a big-endian BOM and an extra zero byte after it, which shifts the value of a TXXX frame and the data of an APIC frame by one byte. `metadata.go` has its own frame types for text, TXXX, COMM, USLT and APIC, which `id3v2` writes through its `Framer` interface. They write what mutagen writes for v2.3: Latin-1 kept, other encodings as UTF-16 with a little-endian BOM, a terminator after each string, list values joined with `/`. Frames read from the file are converted to them, so every frame's bytes are mutagen's.

Goldens compare FLAC files by hash, with the comments as an ordered list for readable failures, and MP3 frames as a set of frame ID, description, encoding and text, plus a hash of each raw frame. A golden test also checks that the audio frames come out byte for byte.

## downloadsdb

The interface lives in the root package; `downloadsdb` implements it in SQLite, and shelf implements it over `downloaded_items`.

```go
// in qobuzdl
type DownloadsDB interface {
    // Has ports handle_download_id without add_id. Python matches ID and quality only; the media type is
    // passed for implementations that key on it, and downloadsdb ignores it.
    Has(ctx context.Context, mediaType, id string, quality int) (bool, error)
    // Add ports handle_download_id with add_id. A duplicate is not an error.
    Add(ctx context.Context, rec DownloadRecord) error
}

type DownloadRecord struct {
    ID, MediaType          string
    Quality                int
    FileFormat             string
    QualityMet             bool
    BitDepth, SamplingRate *string // str() of Python's value; nil for None
    SavedPath, Status      string  // Status is "downloaded"
    URL, ReleaseDate       string
    Artist, Album          string
}
```

```go
package downloadsdb

func Open(ctx context.Context, path string) (*DB, error) // create_db, both migrations included
func (db *DB) Has(ctx context.Context, mediaType, id string, quality int) (bool, error)
func (db *DB) Add(ctx context.Context, rec qobuzdl.DownloadRecord) error
func (db *DB) Close() error
```

- modernc SQLite through `database/sql`, with a 5-second busy timeout like Python's `sqlite3`. The journal mode is left alone, so Python and Go can share one `qobuz_dl.db`.
- `Open` ports `create_db` with both migrations. They are short, and the goldens test them on databases built by Python's code from older layouts.
- `Has` and `Add` run the same SQL as Python.
- A failing `Has` stops the download with its error. A failing `Add` is logged, as Python logs it, and leaves the release done.

## qobuzdltest

A fake Qobuz for tests in this repo and in shelf's 1d.

```go
package qobuzdltest

const (
    Token     = "…"                                // longer than 60 characters
    AppID     = "123456789"
    Secret    = "5e1f0c2a9b7d4e3f8a6c1b0d2e4f6a8c" // the one the fake accepts
    BadSecret = "0f9e8d7c6b5a49382716051423324150" // the bundle's first secret
    UserID    = "1000001"                          // the account user/get answers
)

// Fixture IDs, from the recorded answers.
const (
    AlbumMultiDisc, AlbumHiRes96, AlbumCD, AlbumCompilation, AlbumWithVersion, AlbumClassical, AlbumGoodies string = …
    TrackSingle, Playlist, Artist, Label string = …
)

// Collections built from the recorded ones.
const (
    PlaylistLong, ArtistLong                string = … // 120 items, pages at 0, 50 and 100
    PlaylistEmpty, ArtistEmpty, LabelEmpty  string = … // a first page without items
)

type Server struct {
    URL string
    // unexported: httptest server, the track index, switches and the request log under one mutex
}

func New(tb testing.TB) *Server          // closed by tb.Cleanup; calls only Helper, Cleanup and Fatalf
func (s *Server) Configure(o *qobuzdl.Options) // APIBase, PlayBase, HTTPClient, Settings.UserAuthToken, zero sleeps
func (s *Server) ClientOptions() qobuzdl.ClientOptions // AppID, BadSecret and Secret, Token, the bases, the client

// Switches, safe to call while requests run.
func (s *Server) Fail404(trackID string)
func (s *Server) FailTimes(trackID string, n, status int) // n failures, then success
func (s *Server) CutAfter(trackID string, n int64)        // close the body after n bytes, once; HEADs don't count
func (s *Server) IgnoreRange(trackID string)               // answer 200 with the whole file to Range requests
func (s *Server) Sample(trackID string, fmtID int)         // getFileUrl adds sample: true at that format
func (s *Server) EmptyAnswer(trackID string)               // getFileUrl answers {} at every format: a demo
func (s *Server) Restrict(trackID string, fmtID int)       // FormatRestrictedByFormatAvailability, delivers the next tier
func (s *Server) Deliver(trackID string, fmtID int)        // answers that format_id whatever is asked, without restrictions
func (s *Server) Segmented(trackID string)                  // step 8: getFileUrl without url; file/url and segments instead
func (s *Server) FailStatic()                               // 404 on every cover and goody
func (s *Server) ExpireToken()                             // 401 on every API call
func (s *Server) Requests() []Request                      // in arrival order
func (s *Server) Reset()                                   // clears switches and the log

type Request struct {
    Method, Path string
    Query        url.Values
    Range        string
    Header       http.Header // the token and app ID included
    Status       int         // the answer's; 0 while it is written
}
```

- The fixtures are the recorded answers from `testdata/fixtures/api/`, which `make golden` copies into `qobuzdltest/testdata/` with the bundle files and the audio, and the package embeds. Answers keep their key order and the text of their numbers, so `96` stays an int and `44.1` a float.
- Every API call needs the X-App-Id `AppID`, else 400, and the X-User-Auth-Token `Token`, else 401. Errors have Qobuz's shape, `{"status": "error", "code": …, "message": …}`; unknown IDs get 404.
- `user/get` answers a Studio account with `UserID`.
- `album/get` and `track/get` answer the recorded files. `track/get` also answers for every track of a recorded album, as the album lists it with the album/get answer, less its tracks, in front as `album`; and for every track of the recorded playlist, as the playlist lists it, less `position`, `created_at` and `playlist_track_id`. A playlist download calls `track/get` for each track, so it needs these; step 9's Python generator has to build the same answers.
- `playlist/get`, `artist/get` and `label/get` cut the recorded first page, or a built collection, by `offset` and `limit`, keeping `total`.
- `catalog/search` answers the recorded search of the requested type, whatever the query, cut by `offset` and `limit`.
- `track/getFileUrl` checks `request_sig` with `Secret` and answers 400 on a mismatch, as Qobuz does. It answers the recorded file for the track and format, or one built from the format table: 5 is MP3 16/44.1, 6 FLAC 16/44.1, 7 FLAC 24/96, 27 FLAC 24/192. Its `url` points at the fake CDN, `/cdn/file/<track>/<format>` with a query standing for the signature, and names the delivered format, not the requested one.
- The CDN serves one file per format, the tiny FLACs at 16/44.1, 24/96 and 24/192 and the MP3 from step 1, through `http.ServeContent`: `Range` gives 206, and a range past the end 416.
- The fake rewrites every `https://static.qobuz.com/` URL in an answer to its own `/static/`, which serves the JPEG for `.jpg` and the PDF for `.pdf`.
- The segmented path, from step 8, serves encrypted segments that `gen.py` builds and checks with upstream's own decryption, together with the `session/start` infos and the wrapped key that go with `Secret`.
- The bundle at `/login` and its script carry `AppID` and secrets that decode to `BadSecret`, `Secret` and an empty one, in Python's order.
- Tests in the root package can't import `qobuzdltest`, since it imports the root package. Scenario tests live in package `qobuzdl_test` and reach unexported functions through an `export_test.go`.

## CLI

`cmd/qobuz-dl`, the only code that prints or exits.

```
qobuz-dl dl [flags] URL...
qobuz-dl search [-l limit] albums|tracks|artists|playlists QUERY...
```

The token comes only from `QOBUZ_TOKEN`; the CLI passes it as `Settings.UserAuthToken`. Progress lines go to stdout, logs to stderr, `-v` for debug. Exit status 0 when every release is done or skipped, 1 otherwise, 2 for usage errors.

`dl` flags follow Python's `dl` names where a ported setting exists. Go's `flag` package accepts `-ff` and `--folder-format` alike:

| Flag | Sets |
| --- | --- |
| `-d`, `--directory` | `Directory`, default `QobuzDownloads` |
| `-q`, `--quality` | `Quality`, default 6 |
| `--albums-only` | `IgnoreSinglesEPs` |
| `--no-m3u` | `NoM3UForPlaylists` |
| `--no-fallback` | `QualityFallback` off |
| `--db PATH` | the downloads database, default `~/.config/qobuz-dl/qobuz_dl.db` as Python's CLI |
| `--no-db` | no database |
| `-ff`, `--folder-format`; `-fbff`, `--fallback-folder-format`; `-tf`, `--track-format` | the templates |
| `-s`, `--smart-discography` | `SmartDiscography` |
| `--delay N` | `Settings.Delay` |
| `--no-credits` | `NoCredits` |
| `--native-lang` | `ForceEnglish` off |
| `--multi-tags` | `Settings.MultiValueTags` |
| `--no-<name>-tag`, all 22 | the tag switches |
| `-e`, `--embed-art` | both embed flags, as Python's CLI |
| `--no-cover`, `--embedded-art-size`, `--saved-art-size` | cover settings |
| `--multiple-disc-prefix`, `--multiple-disc-one-dir`, `--multiple-disc-track-format` | disc settings |
| `--max-workers` | `Settings.MaxWorkers` |
| `--legacy-charmap`, `--disable-smart-truncation` | config-only in Python; flags here |

Defaults are `QobuzDL`'s and `QobuzDLSettings`', not those of Python's `config.ini`, so `qobuz-dl dl -q 27 --db <path> <url>` runs what the bot runs.

`search` prints `SearchByType`'s lines and URLs, 10 by default as in `search_by_type`.

## Golden generation

`testdata/gen/` holds four scripts, run with `testdata/gen/.venv/bin/python -I`:

| Script | Does | Network |
| --- | --- | --- |
| `gen.py` | reads `testdata/fixtures/`, calls qobuz-dl 2.5.9 functions, writes `testdata/golden/*.json` | none; one test binds a localhost port |
| `record.py` | fetches the fixture API answers with `QOBUZ_TOKEN` through upstream's `Client`, cleans them, writes `testdata/fixtures/api/` | Qobuz |
| `parity.py` | runs `QobuzDL` with the bot's settings on one URL into a folder | Qobuz |
| `compare.py` | compares two download trees: names, sizes of non-audio files, FLAC and MP3 tags read with mutagen | none |

- `requirements.txt` pins the venv to `pip freeze` from the design session: `qobuz-dl-ultimate==2.5.9`, `mutagen==1.48.1`, `pathvalidate==3.3.1`, `cryptography==50.0.2`, `requests==2.34.2` and the rest. Goldens depend on mutagen's and pathvalidate's versions, so the pins move only with an upstream sync.
- `gen.py` may call private functions such as `qobuz_dl.utils.clean_filename` or `qobuz_dl.metadata._get_tags_to_add`, construct `Bundle` without `__init__` and set its text, and run `Download` with a fake client object that answers from fixtures and a localhost HTTP server for files. The end-to-end goldens, file trees with tags, come from that.
- Each golden is one JSON file with a `cases` list of `{"name", "input", "output"}` or `{"name", "input", "error"}`, where `error` is the Python exception class name. Go tests load them with a small helper in `golden_test.go`.
- `record.py` cleans: signed `url` values become `{{CDN}}/<kind>/<id>/<format>`, `url_template` likewise, user fields and the token go, and the answers keep their key order. The recording step picks the IDs by search and lists them in `docs/status.md` under Fixture items.
- Goldens are committed, so CI never runs Python. `make golden` rebuilds them; a diff after `make golden` is a bug or an upstream change.

Fixture recording needs the token; the user chose to record in step 01, so that session needs `QOBUZ_TOKEN` exported.

## Testing

- Standard `testing` only. Test names are sentences in CamelCase.
- Unit tests per file, golden tests per golden file, and scenario tests on `qobuzdltest` for the flows.
- Retries, backoff, timeouts and the 250 ms throttle run under `testing/synctest`; no test sleeps more than 100 ms.
- The remux test runs ffmpeg on a tiny fMP4 it makes on the spot, and skips when `ffmpeg` is not on `PATH`.
- No network outside `httptest` and localhost. `go test -race ./...` passes.
- Each step's checklist maps edge cases to test names; shelf's 1c checklist is included in the steps.

## CI and Make

`.github/workflows/ci.yaml`, from proxier's job shape:

- `test`: checkout, `setup-go` with `go-version-file: go.mod`, `go mod download`, gofmt check, `go build ./...`, `go vet ./...`, `go test -race ./...`. ffmpeg is installed with apt so the remux test runs.
- `lint`: `golangci/golangci-lint-action` with `version: v2.15.0`, no config file; `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`.
- On pushes to `main`, tags `v*` and pull requests.

`Makefile`:

| Target | Runs |
| --- | --- |
| `check` | gofmt, vet, `go test -race ./...` |
| `lint` | golangci-lint v2.15.0 and govulncheck in Docker |
| `golden` | `gen.py` in the venv |
| `record` | `record.py`; needs `QOBUZ_TOKEN` |
| `upstream-diff TO=<tag>` | fetches tags in `.upstream/qobuz-dl` and diffs the ported files from `UPSTREAM.md`'s tag to `TO` |
| `parity URL=<url>` | `parity.py` into `out/parity/py`, the CLI into `out/parity/go`, then `compare.py` |

## Logs and secrets

- slog. Info for release and track milestones, the lines Python shows; debug for requests, tiers and retries; warn for what Python logs as warnings.
- Never logged and never in errors: the user token, app secrets, request signatures, the session key, `raw_key`, and CDN query strings. URLs are logged as scheme, host and path through `redactURL`.
- `APIError.Body` is cut to 200 bytes after removing any occurrence of the token and the secrets.

## Versions

- Tags are this repo's semver, `v0.x` until shelf's phase 1 is deployed. `UPSTREAM.md` maps each tag to the qobuz-dl version it ports.
- `go 1.27.3` in `go.mod`, as proxier.

## Deliberate differences

Added to `UPSTREAM.md` by this design, beyond the ones already there:

| Difference | Why |
| --- | --- |
| `HandleID` is `handle_url` without the URL parsing. | Callers that already have a kind and an ID, such as shelf's playlist downloads. |
| A context end leaves `[INCOMPLETE]` and returns an error; Python exits the process. | A library must not exit. |
| Each call copies the settings a playlist changes. | Concurrent calls on one `DL`. |
| `make_m3u` sorts file names. | Python's order is the file system's and varies between machines. |
| A tagging failure removes the temp file. | Python leaves it for the CLI to clean. |
| `DownloadsDB.Has` gets the media type. | shelf keys its table on it; `downloadsdb` ignores it. |
| Search errors are returned. | Python returns `{}` and logs "Invalid type". |
| `ErrUnhandled` where Python would crash. | A library must not crash its caller. |
| A skipped track reports its format from the file. | shelf records the delivered format for every file. |
| MP3 frame order and padding differ from mutagen's. | `id3v2` writes in map order and pads nothing; players don't show either. |

## Steps

| Step | Contract | Builds |
| --- | --- | --- |
| 1 | [01-golden.md](build/01-golden.md) | module, Makefile, CI, venv pins, `record.py`, fixtures, `gen.py` with the Python-helper goldens |
| 2 | [02-pycompat.md](build/02-pycompat.md) | `pyvalue.go`, `pystr.go`, `pyre.go`, `format.go`, `textwrap.go` |
| 3 | [03-utils.md](build/03-utils.md) | `constants.go`, `errors.go`, `settings.go`, `pathvalidate.go`, `utils.go` without `make_m3u` |
| 4 | [04-client.md](build/04-client.md) | `bundle.go`, `qopy.go`, `catalog.go`, `transport.go` |
| 5 | [05-tags.md](build/05-tags.md) | `flac.go`, `metadata.go` |
| 6 | [06-fake.md](build/06-fake.md) | `qobuzdltest` without the segmented path |
| 7 | [07-downloader.md](build/07-downloader.md) | `downloader.go` direct path, `progress.go` |
| 8 | [08-segments.md](build/08-segments.md) | the segmented path in `qopy.go`, `downloader.go` and `qobuzdltest` |
| 9 | [09-core.md](build/09-core.md) | `core.go`, `MakeM3U`, `SmartDiscographyFilter`, `downloadsdb/` |
| 10 | [10-cli.md](build/10-cli.md) | `cmd/qobuz-dl`, README usage, `parity.py`, `compare.py` |
| 11 | [11-parity.md](build/11-parity.md) | the parity runs and `v0.1.0` |

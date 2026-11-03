# 3: utils, settings, errors, pathvalidate

## Read first

- [design.md](../design.md) sections Python compatibility (pathvalidate, regular expressions), Settings and options, Errors.
- [port-spec.md](../port-spec.md) sections Sanitizing, get_url_info, smart_discography_filter, Settings.
- Upstream `utils.py`, `settings.py`, `constants.py`, `exceptions.py`; pathvalidate 3.3.1 `_filename.py`, `_filepath.py`, `_base.py`, `_common.py` from the venv.
- The As built sections of steps 1 and 2.

## Done when

`constants.go`, `errors.go`, `settings.go`, `pathvalidate.go` and `utils.go` (all but `make_m3u`) exist, and their goldens pass.

## Decisions

| Topic | Decision | Why |
| --- | --- | --- |
| `clean_filename` regexes | Steps 2, 3 and 5 as RE2 patterns over the `pyre.go` classes; step 4 as a scanner in `pyre.go`. Each with its Python pattern in a comment. | RE2 lacks lookaround and Unicode `\b`. |
| pathvalidate | Port the universal-platform branches only; `platform`, `fs_encoding` and `max_len` fixed at the defaults. Validation errors that `sanitize_*` swallows are not ported. | The only paths upstream reaches. |
| `get_url_info` | The regex with Python's `\w`; no match is `ErrInvalidURL` wrapping a `pyAttributeError`. | `handle_url` catches `AttributeError`. |
| `GetAlbumArtist` | `getAlbumArtist(*pyDict)` here; the exported `GetAlbumArtist(*Album)` comes with the typed views in step 4. | shelf shows album artists. |
| `smart_discography_filter` | Over `[]*pyDict` pages; its goldens use recorded artist pages plus synthetic groups. | Python iterates the raw pages. |
| `label_matches` | Not ported. | `filter_label` is not ported. |
| `format_duration` | Ported as `FormatDuration`; `time.gmtime` wraps at 24 hours, so the Go version does too. | Same output. |
| `create_and_return_dir` | Expands `~`, makes the path absolute, creates it 0755. | Python's behavior, CLAUDE.md's mode. |
| `Settings.Delay` | `time.Duration`; `DefaultSettings` gives 0. | Go style; Python's int seconds become `time.Duration(n) * time.Second` in the CLI. |

## Files

```
constants.go  errors.go  settings.go  pathvalidate.go  utils.go
utils_test.go  settings_test.go  pathvalidate_test.go  errors_test.go
testdata/golden/album_artist.json  smart_discography.json  release_type.json   (gen.py extended)
```

## Go signatures

```go
const (
    DefaultFolder                = "{album_artist} - {album_title} ({year}) [{format} {bit_depth}]"
    DefaultTrack                 = "{track_number} - {track_title_base}"
    DefaultMultipleDiscTrack     = "{disc_number}.{track_number} - {track_title_base}"
    DefaultVariousArtistsAliases = "various artists, various, va, artistes divers, verschiedene interpreten, varios artistas, artisti vari, vari"
    OKMaxCharacterLength         = 180
)

func DefaultSettings() Settings
func ParseAliases(s string) []string

func CleanFilename(name string, legacyCharmap bool) string
func applyLegacyCharmap(name string) string
func invalidCharsToFullwidth(name string) string
func sanitizeFilename(name, replacement string) string
func sanitizeFilepath(path, replacement string) string
func GetURLInfo(url string) (kind, id string, err error)
func getAlbumArtist(album *pyDict) ([]string, error)
func SmartDiscographyFilter(pages []*pyDict, saveSpace, skipExtras bool) ([]*pyDict, error)
func FormatDuration(seconds int) string
func createAndReturnDir(dir string) (string, error)

var ErrAuthentication, ErrIneligible, ErrInvalidAppID, ErrInvalidAppSecret, ErrInvalidQuality, ErrNonStreamable error
var ErrBundle, ErrInvalidURL, ErrEmptyContent, ErrQueryTooShort, ErrUnhandled error
type APIError struct{ Endpoint string; Status int; Body string }
```

## Checklist

| Edge case | Test |
| --- | --- |
| `clean_filename`: fullwidth characters, separators, empty brackets, NFC, over the corpus | `TestCleanFilenameGolden` |
| `clean_filename` with `legacy_charmap` | `TestCleanFilenameLegacyGolden` |
| A separator after `(` or before `)` next to a non-Latin word | `TestCleanFilenameBracketSeparatorsUnicode` |
| `sanitize_filename` and `sanitize_filepath` with both replacements | `TestSanitizeFilenameGolden`, `TestSanitizeFilepathGolden` |
| Reserved names in any case and with extensions get `_` | `TestSanitizeReservedNames` |
| Names cut to 255 UTF-8 bytes without splitting a character | `TestSanitizeCutsAtByteLimit` |
| `get_url_info` on every URL form, `/interpreter/` rejected | `TestGetURLInfoGolden` |
| `get_album_artist` with `artists` missing, without main artists, with several | `TestGetAlbumArtistGolden` |
| `format_release_type` for empty, `ep`, `single`, `album`, `compilation` | `TestFormatReleaseTypeGolden`, in step 7 where the function lives; this step writes the golden |
| Smart discography: grouping by essence, leading parentheses, best depth then lowest rate, other artists dropped, remasters preferred, extras dropped | `TestSmartDiscographyGolden` |
| `parse_aliases` strips, case-folds, drops empties | `TestParseAliases` |
| `DefaultSettings` matches `QobuzDLSettings()` | `TestDefaultSettingsMatchPython`, against a golden of the Python object's attributes |
| `APIError` text has no token or secret | `TestAPIErrorRedacts` |
| `errors.Is` through the wrapping of the sentinels | `TestSentinelsWrap` |
| `createAndReturnDir` expands `~` and creates 0755 | `TestCreateAndReturnDir` |

## As built

Filled in at the end of the step.

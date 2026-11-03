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

Done on 2026-11-03. `make check` and `make lint` pass: 0 golangci-lint issues, no vulnerabilities.

### Differences from the contract

- `getAlbumArtist(log *slog.Logger, album *pyDict) ([]any, error)`. The names come back as the API holds them, because downstream `", ".join(names)` and `str(name)` act on their types. The logger gets Python's `Error getting album artist: ...` line when the try block fails.
- `sanitize_filepath` passes `max_len=-1`, which becomes the universal platform's 260, so each entry of a path is cut to 260 bytes, not 255.
- `sanitize_filepath` ends by validating the joined path, and the port does the same. A blank entry empties a relative path. `\u00a0.` sanitizes to the entry `\u00a0`, which Python's `strip()` sees as blank. Absolute paths stop at the absolute-path check first, and an earlier entry with another error, such as `CON_. `, stops the check too. `FileNameValidator.validate` is `pvValidateFilename`, which returns the reason of the one error it raises; both sanitizers act on that reason.
- `pyNFC` in `pystr.go`. x/text's `norm` is Unicode 17.0 and gives 34 code points new in 17.0 a combining class, so `norm.NFC` reorders and composes across them. Python sees them as unassigned starters. `pyNFC` normalizes the text between them piece by piece. `TestStringMethodsGolden` now checks the `nfc` key of `strings.json` with `pyNFC`.
- New helpers in `pyvalue.go`: `pyGetItem`, `pyEqual`, `pyLess` and `pyMinMax`, with Python's exact comparison of ints and floats. `utils.go` has `pyContains` and `pyExceptionStr`.
- Python's IGNORECASE matches `i` with `ı` and `İ`, and RE2's case folding doesn't, so the extras pattern spells `i` as `[iı\x{130}]`. The other letters in the two patterns fold the same in both; `ſ` matches `s` in both.
- `newAPIError(endpoint, status, body, secrets...)` builds an `APIError`. It replaces each secret with `[redacted]` before cutting at 200 bytes on a character boundary, and turns invalid UTF-8 into U+FFFD. Without a body, `Error()` ends after the status.
- A fourth new golden, `settings.json`, holds `vars(QobuzDLSettings())` with the alias set sorted. `TestDefaultSettingsMatchPython` fails when upstream adds an attribute that is neither ported nor in its not-ported list.
- `handpicked.txt` has 12 new strings with separators inside brackets next to non-Latin words, so the `strings`, `clean_filename`, `pathvalidate`, `name_length` and `textwrap` goldens grew. `gen.py`'s path list has UNC paths, odd drives such as `::` and `é:`, and blank entries.
- The step-4 scanner in `pyre.go` is `subSeparatorAfterOpener` and `subSeparatorBeforeCloser`, both on `matchSeparatorRun`, which tries the two greedy `\s*` in a backtracking engine's order.
- Extra tests: `TestGetURLInfoRejectsInterpreter`, `TestGetAlbumArtistLogsTheError`, `TestFormatDuration`, `TestPosixNormpath`, `TestNTSplitDrive`, `TestPyCompareLikePython`, `TestPyGetItem`, `TestPyNFCLeavesCharactersPythonDoesNotKnow`, `TestAPIErrorBodyCutKeepsUTF8`.

### Checks

Throwaway differential runs against the venv's Python, not committed: 530,000 random strings over brackets, separators, Python whitespace, reserved names, drives and long runs, through `clean_filename` both ways and both sanitizers with both replacements; 200,000 random URLs through `get_url_info`. The first run found the blank-entry rule above; after the fix there are no differences.

### Notes for later steps

- Step 4: `normalizeJSONStrings` must use `pyNFC`, not `norm.NFC`.
- Step 4: `GetAlbumArtist(*Album)` turns the `[]any` into strings; `getAlbumArtist` keeps whatever the API sent.
- Step 7: `_flatten_artists` joins the names with `", ".join`, a TypeError for a non-string name, so check the types of `getAlbumArtist`'s result there.
- Step 7: `format_release_type` is in `release_type.json`, and `TestFormatReleaseTypeGolden` comes with `formatReleaseType`.
- Step 9: `handle_url` catches nothing around `smart_discography_filter`, so its errors leave `HandleURL` in `ErrUnhandled`. No helper for that wrapping exists yet.
- Step 9: `createAndReturnDir` normalizes with `posixNormpath`, which keeps a leading `//` as Python does.
- `format_duration` is never called upstream; the port has `FormatDuration` because the contract asks for it.

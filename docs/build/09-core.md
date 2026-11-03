# 9: core, m3u8, downloadsdb

## Read first

- [design.md](../design.md) sections core.go, Progress and Result, Extras and m3u8, downloadsdb.
- [port-spec.md](../port-spec.md) sections handle_url, download_from_id, make_m3u, search_by_type, Downloads database.
- Upstream `core.py`, `utils.py` `make_m3u`, `db.py`.
- shelf's `docs/sources/qobuz.md` sections Downloads and Already downloaded.
- The As built sections of steps 3, 7 and 8.

## Done when

`core.go` with `New`, `GetTokens`, `InitializeClient`, `DownloadFromID`, `HandleURL`, `HandleID` and `SearchByType`; `MakeM3U` in `utils.go`; `downloadsdb` with `Open`, `Has`, `Add`, `Close`; playlist, artist and label trees match the goldens; the scenario tests pass.

## Decisions

| Topic | Decision | Why |
| --- | --- | --- |
| Playlist overrides | The folder template `.` and `multiple_disc_one_dir` set on the per-call copy. | Concurrent calls; no visible change. |
| Playlist progress | One `ReleaseStart` for the playlist with every track planned; its tracks report positions from 1. | shelf shows one download with N tracks. |
| Uncaught exceptions | An `ErrUnhandled` from one item stops the loop and returns, as the Python crash would. | Faithful; callers see the error. |
| `MakeM3U` file order | Directories and files sorted by name. | Python's order is the file system's; listed in `UPSTREAM.md`. |
| MP3 length | Xing or Info header frame count, else CBR from file size and bitrate, truncated to whole seconds. | mutagen's method; goldens on the fixture MP3. |
| `downloadsdb` driver | modernc `sqlite` through `database/sql`, `_pragma=busy_timeout(5000)`, one connection pool per `DB`. | Pure Go; same locking behavior as Python's default. |
| Migrations | Both ported; goldens from databases that `gen.py` builds in the v1 and v2 layouts and runs `create_db` on. | Short code, testable. |
| Value types in the table | `bit_depth` and `sampling_rate` bound as text from `pyStr`; `quality_met` as 0 or 1. | SQLite stores Python's values as text in these columns. |
| `SearchByType` | No lucky mode; short queries return `ErrQueryTooShort`; unknown types an error. | The CLI prints it; shelf uses the typed search. |

## Files

```
core.go  utils.go (MakeM3U)
core_test.go  m3u_test.go  search_test.go  scenario_core_test.go
downloadsdb/downloadsdb.go  downloadsdb_test.go
testdata/golden/m3u.json  layout_playlist.json  layout_artist.json  search_text.json  db_rows.json  db_migrations.json
testdata/fixtures/db/v1.db  v2.db   (built by gen.py)
```

## Go signatures

```go
func New(opts Options) (*DL, error)
func DefaultOptions() Options
func (d *DL) GetTokens(ctx context.Context) error
func (d *DL) AppID() string
func (d *DL) Secrets() []string
func (d *DL) InitializeClient(ctx context.Context, email, pwd, appID string, secrets []string) error
func (d *DL) Client() *Client
func (d *DL) DownloadFromID(ctx context.Context, req DownloadRequest) (*Result, error)
func (d *DL) HandleURL(ctx context.Context, url string, p Progress) (*Result, error)
func (d *DL) HandleID(ctx context.Context, kind, id string, p Progress) (*Result, error)
func (d *DL) SearchByType(ctx context.Context, query, itemType string, limit int) ([]SearchResult, error)
func alignText(text string, width int) string
func MakeM3U(dir string, items []*pyDict) (string, error)

type DownloadsDB interface {
    Has(ctx context.Context, mediaType, id string, quality int) (bool, error)
    Add(ctx context.Context, rec DownloadRecord) error
}

// package downloadsdb
func Open(ctx context.Context, path string) (*DB, error)
func (db *DB) Has(ctx context.Context, mediaType, id string, quality int) (bool, error)
func (db *DB) Add(ctx context.Context, rec qobuzdl.DownloadRecord) error
func (db *DB) Close() error
```

## Checklist

| Edge case | Test |
| --- | --- |
| A playlist lands in a flat folder with an ordered m3u8 | `TestPlaylistM3U8` |
| The playlist tree equals Python's: names, positions as track numbers, no covers | `TestPlaylistLayoutGolden` |
| m3u8 order by track ID, then ISRC, then title, then file name; duplicates kept; nothing matched falls back to natural order | `TestM3UOrderGolden` |
| m3u8 lines, lengths, relative paths, no trailing newline, no file when empty | `TestM3UFormat` |
| An artist downloads each album into its folder; with smart discography only the filtered ones | `TestArtistLayoutGolden` |
| A label downloads each album | `TestLabelDownload` |
| An empty playlist or artist is `ErrEmptyContent` | `TestEmptyContent` |
| An invalid URL is `ErrInvalidURL` and downloads nothing | `TestHandleURLInvalid` |
| An album in the downloads database is skipped | `TestDownloadsDBSkips` |
| `IgnoreDB` downloads it anyway | `TestIgnoreDB` |
| An album with a failed track is not recorded; a done one is, with Python's values | `TestDownloadsDBRecords` |
| `NonStreamable` and request errors are reported in the result; other errors stop | `TestDownloadFromIDErrors` |
| `downloadsdb` schema equals Python's; rows Python writes and Go writes compare equal | `TestDownloadsDBSchemaAndRowsGolden` |
| v1 and v2 databases migrate as Python migrates them | `TestDownloadsDBMigrationsGolden` |
| A duplicate `Add` is not an error | `TestDownloadsDBDuplicate` |
| Python and Go share one database file | `TestDownloadsDBSharedWithPython`, on a file `gen.py` wrote |
| Search text lines for albums, tracks, artists, playlists, with the release-type guesses and quality columns | `TestSearchByTypeGolden` |
| A query under 3 characters | `TestSearchByTypeShortQuery` |
| Two downloads on one `DL` at once | `TestConcurrentDownloads`, under `-race` |
| `New` rejects a nil client, zero workers, a bad quality | `TestNewValidates` |

## As built

Filled in at the end of the step.

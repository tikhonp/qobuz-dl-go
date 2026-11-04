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

Done on 2026-11-04. `make check` and `make lint` pass: 0 golangci-lint issues, no vulnerabilities. `make golden` twice gives the same files, the new fixtures included.

### Differences from the contract

- `HandleURL` and `HandleID` share `(*DL).handle`. `HandleID` builds `https://play.qobuz.com/<kind>/<id>` for the lines that name the URL. An unknown kind is `ErrInvalidURL`, as Python's `possibles[url_type]` KeyError lands in the invalid-URL branch. An invalid URL returns an empty `*Result`, not nil.
- `ErrUnhandled` wraps with `%w: %w`, not `%w: %v`, so `errors.As` still finds the `*APIError` inside, such as the 401 of an expired token on `playlist/get`. `unhandled` leaves the context's end alone. design.md now says so.
- A playlist's tracks that never reach a transfer, such as one in the database, a demo, or the ones after the loop stopped, get their `TrackEnd` from `handle`, as skipped or failed, so every track its `ReleaseStart` planned ends. design.md's Progress section says so.
- `MakeM3U` returns the path it wrote, or "". `makeM3U` takes a logger for the table of missing tracks; `MakeM3U` discards it.
- mutagen's `FileType` is false when the file has no tags, so `make_m3u`'s `if audio_gen and audio_gen.info` leaves the length at 0 for an untagged file. The port does the same. port-spec.md and design.md now say so.
- The MP3 length is a port of mutagen's `MPEGInfo` in a new file, `mp3.go`: the sync search in the first MiB after any ID3v2 tags, four frames in a row or one with a Xing, Info or VBRI header, LAME's delay and padding taken off, else the bitrate over the file size. The contract had a simpler rule; the goldens on built files decide, and they include LAME, VBRI, CBR, junk before the first frame and broken files.
- `createAndReturnDir` now makes folders through `makeDirs`, 0755 whatever the umask; it used `os.MkdirAll`.
- The fake has a new built collection, `ArtistAlbums`: "Norah Jones" listing `AlbumHiRes96`, `AlbumWithVersion` and `AlbumCD`, as `album/get` answers them less their tracks. The recorded artist's albums have no recorded `album/get`, so a download of it is all 404s; the artist golden needs albums that download. Smart discography keeps only Begin Again. gen.py builds the same page.
- The playlist and artist goldens also hold the downloads database's rows, from a database Python's run wrote, with `saved_path` relative to the download folder. `LayoutCase.CheckDB` compares them.
- `TestDownloadFromIDErrors` changes answers with a wrapping `http.RoundTripper`, since the fake has no switch for a non-streamable album or a broken answer. Removing `tracks` won't work, because `_get_format` raises NonStreamable for an album without tracks; the test breaks `media_count` instead.
- `downloadsdb` binds `quality_met` as 0 or 1, opens the file as a SQLite URI with `_pragma=busy_timeout(5000)`, runs the v1 migration in one transaction, and returns nil for any constraint failure. `Open` logs nothing; it takes no logger.
- `.gitignore` ignored `*.db`; it now lets `testdata/fixtures/db/*.db` through.
- Extra tests: `TestGetTokensAndInitializeClient`, `TestPlaylistWithoutM3U`, `TestPlaylistPaging`, `TestHandleURLAlbumAndTrack`, `TestDownloadsDBFailingHasStops`, `TestCancelMidPlaylist`, `TestSearchByType` on the fake, `TestMP3Length`, `TestNaturalKey`, `TestAlignText`, `TestNewMakesTheDirectory`, `TestNewCopiesTheOptions`, `TestUnhandledKeepsTheChain`, `TestDownloadsDBConcurrent`, `TestOpenKeepsOddFileNames`, `TestOpenFailsOnAFolder`.

### Checks

`make golden` twice gives the same files byte for byte, the SQLite fixtures included; Python 3.14's sqlite3 is SQLite 3.54.1. The Go playlist and artist trees match Python's, FLAC files byte for byte, and so do the database rows.

### Notes for later steps

- Step 10: the CLI builds `Options` from flags, opens `--db` with `downloadsdb.Open`, calls `New`, `GetTokens`, `InitializeClient(ctx, "", "", d.AppID(), d.Secrets())`, then `HandleURL` per URL. Exit 1 when any release in the `Result` is not done or skipped, or on an error. `SearchByType` takes the singular type, `album`, `track`, `artist` or `playlist`; the CLI's `albums` maps to it.
- Step 10: `HandleURL` for an invalid URL returns `ErrInvalidURL` and logs Python's line; the CLI can print the error and go on to the next URL, as Python does.
- Step 11: a real playlist's m3u8 lengths are whole seconds of the delivered files; the parity run compares them with Python's.

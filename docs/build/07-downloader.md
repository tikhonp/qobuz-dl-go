# 7: downloader, direct path

## Read first

- [design.md](../design.md) sections Progress and Result, Downloads, Naming, Extras and m3u8.
- [port-spec.md](../port-spec.md) sections Quality, Downloads, Transfers (Direct).
- Upstream `downloader.py`.
- The As built sections of steps 2 to 6.

## Done when

`downloader.go` (all but the segmented functions) and `progress.go` exist. An album or a track downloads from `qobuzdltest` with Python's names, folder states, extras, tags, quality gate and fallback; the end-to-end goldens of the file trees pass; the scenario tests below pass.

## Decisions

| Topic | Decision | Why |
| --- | --- | --- |
| Entry point | A test helper calls `downloadIDByType` until step 9 adds `DownloadFromID`. | Steps stay small. |
| Per-call state | A `download` struct per call, holding copies of the templates and the disc settings it may change. | `_determine_formats` and playlists change them; the `DL` must not change. |
| Pool | A goroutine per submitted track, gated by a semaphore of `MaxWorkers`; results read in submission order. | `ThreadPoolExecutor` shape, Python's order. |
| Two URL calls per track | Kept: the main loop's call and the tier calls. | Call order is part of the behavior; the demo check uses the first. |
| Temp files | Removed on any failure after creation, `.mp4` included; on cancel too. | Deliberate difference in `UPSTREAM.md`. |
| Track paths in results | Rewritten from the work folder to the final folder after the last rename. | Callers read files after the call. |
| Skipped tracks' format | From FLAC STREAMINFO, or format 5 for `.mp3`. | shelf records it. |
| Timeouts | `httptrace` connect timer and an idle timer reset on every read. | requests' `(10, 60)` without changing the caller's transport. |
| Progress throttle | Per track, at most every 250 ms, and a final call. | Design. |
| Name too long | `isNameTooLong` uses `os.Stat` and `syscall.ENAMETOOLONG`; tests use a name over 255 bytes on the temp dir. | As Python. |
| End-to-end goldens | `gen.py` runs Python's `Download` with a fake client over the fixtures and a localhost file server, and records the tree: relative paths, tags per audio file, the tracklist text, which files exist. | Behavior pinned by Python, not by reading. |

## Files

```
downloader.go  progress.go
downloader_test.go  transfer_test.go  naming_test.go  scenario_test.go
testdata/golden/naming.json  tracklist.json  layout_album.json  layout_track.json   (gen.py extended)
```

## Go signatures

```go
type download struct{ /* client, opts copy, settings copy, ids, path, progress, logger */ }

func formatReleaseType(rt string) string
func isNameTooLong(parent, name string) bool
func shortenUTF8(name string, maxBytes int) string
func fitName(parent, name, suffix string, reserve int) string
func processFolderFormatWithSubdirs(format string, attrs pyAttrs, path *string, legacyCharmap, disableTruncation bool) (string, error)
func (d *download) downloadIDByType(ctx context.Context, track bool) (*Release, error)
func (d *download) downloadRelease(ctx context.Context) (*Release, error)
func (d *download) downloadTrack(ctx context.Context) (*Release, error)
func (d *download) downloadAndTag(ctx context.Context, rootDir string, tmpCount int, track, albumOrTrack *pyDict, isTrack bool, multiple any, pos int) TrackResult
func getFilenameAttr(trackArtist any, track, album *pyDict) (pyAttrs, error)
func getTrackAttr(meta *pyDict, trackTitle string, bitDepth, samplingRate any, fileFormat string) (pyAttrs, error)
func getAlbumAttr(meta *pyDict, albumTitle, fileFormat string, bitDepth, samplingRate any) (pyAttrs, error)
func (d *download) getFormat(ctx context.Context, item *pyDict, isTrackID bool, trackURL *pyDict) (format string, qualityMet bool, bitDepth, samplingRate any, err error)
func (d *download) determineFormats(album *pyDict, albumAttr pyAttrs, tracks []*pyDict, trackAttr pyAttrs, isTrack bool, fileFormat string) error
func (d *download) generateTracklist(ctx context.Context, meta *pyDict, dir, albumTitle, fileFormat string, bitDepth, samplingRate any) error
func getDescription(item *pyDict, trackTitle string, multiple any) string
func downloaderGetTitle(item *pyDict) (string, error)
func (d *download) getExtra(ctx context.Context, url, dir, name, artSize string) error
func cleanFormatStr(folder, track string) (string, string)
func safeGet(d *pyDict, def any, keys ...string) any
func (d *download) transfer(ctx context.Context, url, fname string, onBytes func(done, total int64)) error
func (d *download) downloadGoodies(ctx context.Context, album *pyDict, dir string)
func cleanEmbedArt(dir string)
```

## Checklist

| Edge case | Test |
| --- | --- |
| Folder and track names over the template matrix, including `96` against `96.0`, `None` after a failed gate, versions in titles, multi-disc names | `TestNamingGolden` |
| Template fallback: a ValueError in the folder template, a KeyError in the track template, a TypeError escapes | `TestDetermineFormatsFallback` |
| A folder part's KeyError keeps its own text | `TestFolderPartKeyErrorKeepsText` |
| Paths over 180 and parts over 120 characters cut in the middle; off with `DisableSmartTruncation` | `TestSmartTruncation` |
| A name over 255 bytes in Cyrillic is shortened until the file system takes it | `TestFitNameShortensUTF8` |
| The tracklist text for single- and multi-disc albums, with performers, descriptions and nulls | `TestTracklistGolden` |
| A multi-disc album lands in `CD 01` and `CD 02` with the right tags | `TestMultiDiscAlbum` |
| The album tree equals Python's for every fixture album | `TestAlbumLayoutGolden` |
| A single track's tree equals Python's, `~tmp_01.tmp` used and gone | `TestTrackLayoutGolden` |
| `[IN PROGRESS]` becomes the final name, or `[INCOMPLETE]` when a track failed; an existing `[INCOMPLETE]` or final folder is resumed | `TestFolderStates` |
| A folder name that only fits without the prefix skips the states | `TestFolderStatesNameTooLong` |
| With fallback off, a release below the requested quality is skipped and no folder exists | `TestNoFallbackSkips` |
| A failed gate lets the release through as `Unknown` | `TestGateFailureIsUnknown` |
| With fallback on, the delivered format decides extension and tagger | `TestFallbackDeliveredFormat` |
| A sample answer moves to the next tier | `TestSampleMovesToNextTier` |
| Demo tracks in an album are skipped and counted as failed; a demo single track is skipped, not failed | `TestDemoTracksSkipped` |
| An existing final file is skipped and reported with its format | `TestExistingFileSkipped` |
| A resumed transfer with 206 appends; one answered with 200 starts over | `TestResume` |
| A 416 counts as done | `TestTransfer416` |
| A 404 fails at once; other errors retry with backoff up to 5 times | `TestTransferRetries`, under synctest |
| Short reads resume without sleeping; five short reads fail as incomplete | `TestTransferShortReads` |
| An overshoot deletes the file and keeps the offset | `TestTransferOvershootKeepsOffset` |
| Connect and idle timeouts | `TestTransferTimeouts`, under synctest |
| A failed track leaves no temp file | `TestTempFileRemoved` |
| Cancel during an album: no new tracks, temp files gone, `[INCOMPLETE]`, the context error returned with the partial result | `TestCancelMidAlbum` |
| Workers: three at once by default, one with a delay; files created in Python's order | `TestWorkerPoolOrder` |
| Extras in order, covers at `org` and embedded size, goodies named as Python, existing files kept, cover failure not fatal | `TestExtras` |
| `embed_cover.jpg` removed at the end | `TestEmbedCoverRemoved` |
| `IgnoreSinglesEPs` skips singles, EPs and Various Artists | `TestAlbumsOnly` |
| Progress: release start with planned tracks, track start per tier, bytes throttled, track end per track | `TestProgressEvents`, under synctest |
| Results: final folder, track paths in it, delivered format and outcome per track | `TestResultFields` |
| Files 0644, folders 0755 | `TestFileModes` |
| Nothing logs the token or a CDN query | `TestDownloadLogsAreClean` |

## As built

Done on 2026-11-03. `make check` and `make lint` pass: 0 golangci-lint issues, no vulnerabilities. `make golden` twice gives the same files.

### Differences from the contract

- Signatures follow Python's values where the contract had strings. `formatReleaseType(rt any) (string, error)`, `downloaderGetTitle(*pyDict) (any, error)`, `safeGet(...) (any, error)` and `getExtra(ctx, item any, ...)` raise where Python raises. `processFolderFormatWithSubdirs`, `getAlbumAttr`, `getTrackAttr` and `getFilenameAttr` take a `*slog.Logger` first, for `get_album_artist` and the KeyError warning. `determineFormats` takes the album as `any` and the tracks as `[]any`, since Python passes `track.get("album", {})` and iterates whatever `items` holds. `generateTracklist` takes the title as `any`.
- `downloadAndTag(ctx, rootDir, tmpCount, track, albumOrTrack, isTrack, multiple, pos) (TrackResult, error)`. The error is a Python exception that `_download_and_tag` doesn't catch: the worker pool logs it as `Track download failed` and a single track returns it. Python's unused `track_url_dict` and `is_mp3` arguments are gone.
- The naming part of `_download_and_tag` is `trackPath`, which `TestNamingGolden` calls directly; one pass of the tier loop is `tryTier`; the `try` block of `download_release` is `releaseBody`; one pass of `tqdm_download` is `transferAttempt`.
- The pool is a queue read by `MaxWorkers` goroutines, not a goroutine per track behind a semaphore. Goroutines reach a semaphore in any order; a queue keeps `ThreadPoolExecutor`'s FIFO start order. design.md now says so.
- `newDownload(c, opts, itemID, path, p)` builds the per-call struct. `isPlaylist` and `playlistTrackNumber` are fields step 9 sets. The struct's logger tees records of info level and above into `Progress.Log` through `progressHandler`, so every function that takes a logger feeds `Progress` too.
- `cleanFormatStr(folder, track)` drops Python's unused third argument.
- On a dropped connection urllib3 raises `IncompleteRead` and loses the unfinished 64 KiB chunk, so the attempt sleeps like any error. port-spec.md said a short body goes on without sleeping, which holds only for a short body that matches its own `Content-Length`. The port keeps both rules. `readChunk` drops a chunk cut by an error, and `TestTransferShortReads` drives the no-sleep path with short 206 answers that match their `Content-Length`. Runs of upstream's `tqdm_download` in the venv showed both, and port-spec.md now says so.
- The fake has a new switch, `FailStatic`, 404 on every cover and goody, for the cover failure in `TestExtras`. design.md lists it.
- `genreTag` no longer writes into the album's `genres_list`; it changes a copy. Python's write is the same on every call, but the workers tag one album at once and the race detector caught it.
- `TagMP3` sets the file to 0644 before the rename, since `id3v2` saves through a new file that takes the umask's mode.
- `Settings.NoCover`'s comment said nothing reads it; `download_release` and `download_track` do.
- `TestProgressEvents` runs on the fake in real time, since the fake's httptest server can't run in a synctest bubble. The throttle is checked under synctest in `TestProgressBytesThrottled`.
- Four goldens are new. `naming.json` (186 cases) runs upstream's `_get_album_attr` or `_get_track_attr`, `_determine_formats`, `process_folder_format_with_subdirs` and `_download_and_tag` up to its `_fit_name` call, which a wrapper records while `abort_event` is set, so nothing downloads. `tracklist.json` (27 cases) runs `_generate_tracklist` on the recorded albums and built ones. `layout_album.json` (21 cases) and `layout_track.json` (7 cases) run `Download.download_id_by_type` with a client that answers as `qobuzdltest` does and a localhost server for the CDN and the static files; each records every folder and file, with `flac_result`, `mp3_result`, the tracklist's text or a SHA-256. The Go side compares FLAC files byte for byte.
- Extra tests: `TestTracklistKeepsExistingFile`, `TestTransferHeaders`, `TestIsRequestError`, `TestSafeGet`, `TestCleanFormatStr`, `TestGetDescription`, `TestPythonPaths`, `TestGetFormat`, `TestProgressGetsTheLogLines`, `TestMakeDirsModes`, `TestShortenUTF8Golden`, `TestFormatReleaseTypeGolden`, `TestFakeFailStatic`.
- UPSTREAM.md lists four new differences: the 404 rule reads Go's error text, progress lines are shorter, `Delay` takes any duration, and the GENRE copy. It also notes that Python's `_determine_formats` writes its multi-disc choice into the shared settings, which carries over between the albums of an artist; the per-call copy doesn't.

### Notes for later steps

- Step 8: `tryTier` already calls `transferSegments(ctx, answer, fname, onBytes)` for an answer with `url_template` and fires `TrackStart` with `Segmented: true`; replace the stub that returns `errSegmentedNotPorted`. `getFormat` and the main loop treat `errSegmentedNotPorted` as any failure, which is what Python does with a failed segmented call.
- Step 8: `EmptyAnswer` makes only getFileUrl answer `{}`. At formats other than 5, Python then goes segmented, and a demo shows as an empty `file/url` answer. The demo tests run at format 5 for now; make `file/url` answer `{}` for such tracks too, and the album test can run at 27.
- Step 8: `downloadAndTag` removes `<temp>.mp4` with the temp file on any failure already.
- Step 9: `downloadIDByType` returns raw errors: `ErrNonStreamable`, request errors (`isRequestError`), the context's error wrapped by `stopped`, and Python exceptions such as a `TypeError` from a missing `media_number`. `DownloadFromID` reports the first two in the result and wraps the others in `ErrUnhandled`, as design.md says. The `Release` comes back for every error, with `Status` and `Err` set.
- Step 9: the downloader already calls `Options.DownloadsDB.Add` with Python's values when a release finishes without a failed track, and logs a failure. `Has` is step 9's, in `DownloadFromID`.
- Step 9: for a playlist, set `isPlaylist` and `playlistTrackNumber` on the `download`; `downloadTrack` then fires no `ReleaseStart`, reports the playlist position, names the file `{artist} - {track_title}`, and skips `cover.jpg`. Set the folder template `.` and `MultipleDiscOneDir` on the options copy before `newDownload`.
- Step 9: `DefaultOptions().Quality` is Python's 6. The scenario tests set 27, the bot's.
- Step 10: the CLI can print `Progress.Log` lines; they carry no colors.

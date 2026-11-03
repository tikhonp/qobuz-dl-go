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

Filled in at the end of the step.

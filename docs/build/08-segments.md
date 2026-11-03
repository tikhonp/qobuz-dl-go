# 8: segmented path

## Read first

- [design.md](../design.md) section Segmented path.
- [port-spec.md](../port-spec.md) sections Track URL and Transfers (Segmented).
- Upstream `qopy.py` `get_track_url` and `downloader.py` `tqdm_download_segments`, `_get_qobuz_segment_uuid`, `_decrypt_qobuz_segment`.
- The As built sections of steps 4, 6 and 7.

## Done when

`getTrackURL` has its segmented branch; `transferSegments`, `qobuzSegmentUUID` and `decryptQobuzSegment` exist; the fake serves the segmented path; a track forced onto it downloads, decrypts, remuxes and tags.

## Decisions

| Topic | Decision | Why |
| --- | --- | --- |
| Test segments | `gen.py` builds them: ffmpeg makes a fragmented MP4 with FLAC audio from the 24/96 fixture; the builder adds a `uuid` box with a frame table to each fragment, encrypts the flagged frames with AES-CTR under a fixed `raw_key`, wraps `raw_key` under the session key derived from `qobuzdltest.Secret` and fixed `infos`, and checks the result with upstream's `_decrypt_qobuz_segment`. | Both sides agree on the format through upstream's own code. |
| Golden | `segments.json`: the segment UUID and the SHA-256 of each decrypted segment. | Decryption checked without ffmpeg. |
| Remux | `exec.CommandContext` with `FFmpegPath`; stderr kept, cut to 500 bytes, in the error. | The only external process; cancel kills it. |
| Session | Created once per `Client` under its mutex; `X-Session-Id` sent from then on. | Python's lazy setup without its race. |
| `sampling_rate` | Divided by 1000 into a float, so names print `96.0`. | Python's behavior, kept. |
| HEAD sizes | Summed for `TrackBytes`' total; errors count as 0. | Progress only. |
| Workers | 8 for HEADs and for segments 2 to n; written in order. | Upstream. |

## Files

```
qopy.go  downloader.go   (extended)
segments_test.go
qobuzdltest/segments.go  testdata/segments/*
testdata/golden/segments.json
```

## Go signatures

```go
func (c *Client) startSession(ctx context.Context) error
func (d *download) transferSegments(ctx context.Context, answer *pyDict, fname string, onBytes func(done, total int64)) error
func qobuzSegmentUUID(seg []byte) []byte
func decryptQobuzSegment(seg, rawKey, uuid []byte) ([]byte, error)

func (s *Server) Segmented(trackID string)   // qobuzdltest
```

## Checklist

| Edge case | Test |
| --- | --- |
| Segment UUID from the first `uuid` box; none is an error | `TestQobuzSegmentUUID` |
| Decryption matches upstream's for every built segment | `TestDecryptSegmentsGolden` |
| Frames with zero flags stay as they are; a frame outside the box is decrypted in place | `TestDecryptFlagsAndOffsets` |
| A truncated or zero-size box stops the walk without a panic | `TestDecryptMalformedBoxes` |
| The segmented path decrypts a synthetic segment and remuxes it | `TestSegmentedPath`, skipped without ffmpeg |
| `getFileUrl` without `url` goes segmented; a failed direct transfer retries segmented in the same tier | `TestSegmentedFallbacks` |
| Format 5 never goes segmented | `TestMP3NeverSegmented` |
| `session/start` runs once for concurrent tracks | `TestSessionStartsOnce`, under `-race` |
| `bits_depth` and a `sampling_rate` of 96000 become `bit_depth` 24 and `96.0` in names | `TestSegmentedQualityFields` |
| The `.mp4` is removed after success, failure and cancel | `TestSegmentTempRemoved` |
| An ffmpeg failure fails the tier and the next tier runs | `TestRemuxFailureNextTier` |

## As built

Done on 2026-11-04. `make check` and `make lint` pass: 0 golangci-lint issues, no vulnerabilities. `make golden` twice gives the same files.

### Differences from the contract

- ffmpeg made three fragmented MP4s once, `16-44.1.mp4`, `24-96.mp4` and `24-192.mp4` in `testdata/fixtures/audio/`, with 30 ms fragments and `default_base_moof`; the gen README has the command. `gen.py` doesn't run ffmpeg: it cuts them into segments, adds the `uuid` boxes and encrypts on every run, so `make golden` stays reproducible without ffmpeg. Segments exist for formats 6, 7 and 27, not only 24/96, so `file/url` can deliver what getFileUrl would. The README's Segments section describes the layout, the frame sizes and flags, the 16-byte counters with a carry and the decoy box.
- The segments live in `testdata/fixtures/segments/`, with `index.json`, and `make golden` copies them into `qobuzdltest/testdata/segments/`.
- `segments.json` has 54 cases: the UUID, size and SHA-256 of all 14 fixture segments, 10 built buffers for `_get_qobuz_segment_uuid`, and 30 for `_decrypt_qobuz_segment`, with Python's errors: IndexError for a `uuid` box that ends before its counter length, ValueError for a counter that isn't 16 bytes or a key that isn't 16, 24 or 32. `TestDecryptFlagsAndOffsets` and `TestDecryptMalformedBoxes` split the built cases between them, and the second checks that none is left out.
- `request_params.json` has 26 new `segmented:` cases: the fallbacks after a getFileUrl without url, a 400, a 500 and a list; forced; format 4, which sends `session/start` before `file/url` raises InvalidQuality; format 5 without url; one session for two calls; `bit_depth` kept or missing; rates 44.1, 1000, 1001, 192000.0, missing, None and a string; no key, a key that isn't a string, a key in two parts; an empty answer; `file/url` 400 and 404; `session/start` 500, without `infos` and with bad `infos`. `run_client` gained `seq` for several calls on one client, and the getFileUrl overrides leave `cfg_setup`'s call alone so the client picks the secret the wrapped key needs.
- `Client` has `sessionMu` beside `mu`. `startSession` holds `sessionMu` for the `session/start` call and sets `sessionID`, `sessionInfos` and `sessionKey` under `mu`, which `headers` takes; holding `mu` across the call would deadlock. It makes the session whole or not at all, where Python sets its fields one by one; UPSTREAM.md lists it.
- `transferSegments` takes Python's values: `n_segments` must be an int or bool, `url_template` a string and `raw_key` bytes, else TypeError or AttributeError, as Python fails. Its helpers are `writeSegments`, `segmentSizes`, `headSize`, `fetchSegment` and `remux`. The HEADs use a copy of the caller's client that doesn't follow redirects, as `requests.head` doesn't. Segment requests send requests' default `User-Agent` and `Accept`.
- Segments 2 and on run through 8 workers with at most 16 waiting unwritten; a failed segment cancels the rest. Python fetches them all first. UPSTREAM.md lists both.
- `tryTier` logs `[+] In progress` before the segmented transfer and `  L Completed` after it, as for the direct one; `transferSegments` logs `  > Assembling the final FLAC file...`.
- The fake: `file/url` delivers the format getFileUrl would, with its restrictions, at least 6, so a 24/96 track asked at 27 comes as 7, as the direct path does. It needs the `X-Session-Id` from `session/start`, else 400. `Sample` and `EmptyAnswer` apply to `file/url` too; `Restrict` and `Deliver` reach it through the delivered format. `Fail404`, `FailTimes` and `CutAfter` apply to the segments. `file/url` and the segments work for every track; `Segmented` only drops getFileUrl's `url`.
- The tests are in `segments_test.go` (package `qobuzdl`: the goldens and `FuzzDecryptQobuzSegment`) and `segments_scenario_test.go` (package `qobuzdl_test`, on the fake). `export_test.go` gained `FLACAudio` and `FLACStreamInfo`. `TestSegmentedPath` compares the remuxed audio frames with `24-192.flac` byte for byte. `TestRemuxFailureNextTier` uses a shell script for ffmpeg, so it needs no ffmpeg when the next tier is direct, and skips on Windows.
- A throwaway differential run, not committed: 3,000 random buffers through upstream's two functions and the Go ones, 357 of them raising. No differences.
- `TestDemoTracksSkipped` and `TestGateFailureIsUnknown` now also run at 27, where the empty answer goes through `file/url`. `TestProgressEvents` expects the segmented retry in the first tier; it sets a missing ffmpeg so the retry fails and the next tier delivers.
- Extra tests: `TestFakeSessionAndFileURL`, `TestFakeSegmented`, `TestGetTrackURLWithoutURLGoesSegmented`, which replaces `TestGetTrackURLWithoutURLNeedsSegments`.

### Notes for later steps

- Step 9: a direct transfer that fails now retries segmented in the same tier, as Python does. A test that wants a track to fail outright uses `Fail404` or `FailTimes` with enough failures for the segments too, or a missing `FFmpegPath`.
- Step 9: the main loop's `getTrackURL` call for each track may start the session and call `file/url`; with the fake that is one `session/start` per client.
- Step 10: the parity run needs ffmpeg on PATH for tracks Qobuz serves only segmented; `Options.FFmpegPath` sets another.
- Step 11: whether Qobuz sends `bits_depth` and the rate in Hz from `file/url`, as upstream assumes and the fake does, shows only against the real service.

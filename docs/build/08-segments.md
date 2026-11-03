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

Filled in at the end of the step.

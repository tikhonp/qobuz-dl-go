# Design questions

Questions the design session answered before any library code was written. Each has the context, the options and the recommendation it started from, then the decision with a link to the section of [design.md](design.md) that holds it. All 24 are decided.

## 1. Package layout

Python has ten modules that call each other freely. One option is one Go package at the root with one file per module; the other is one Go package per module.

Recommendation: one package, `qobuzdl`, at the root. Subpackages only for `downloadsdb`, `qobuzdltest` and `cmd/qobuz-dl`. One package keeps Python's cross-module calls without exporting internals.

Decided: one package at the root, as recommended. See [design.md](design.md#package-layout).

## 2. Types for API answers

Python reads nested dicts through `.get` chains with defaults. Go can decode into typed structs or into `map[string]any`.

Recommendation: typed structs with the fields the port reads, chosen so that missing, null and empty behave as Python's `.get(...)` and `or` chains do, plus a `Raw json.RawMessage` on top-level objects so a later port can reach a new field without a type change. Write down the Python default for every field the code reads.

Decided by the user: dynamic values inside, typed views outside, with `Raw`. See [design.md](design.md#values).

## 3. `str.format` and `PartialFormatter`

Folder and track names come from user templates through Python's `str.format`, with `PartialFormatter` handling missing keys and bad specs.

Recommendation: a small formatter that supports `{name}`, `{name:spec}` with the spec subset the templates and code use, `{{` and `}}`, and `PartialFormatter`'s fallbacks. Goldens over a matrix of templates and values decide the details.

Decided: a `str.format` port with the full spec language. `PartialFormatter` is never used upstream, so it is not ported. See [design.md](design.md#strformat).

## 4. Printing Python values

Templates print ints, floats, strings, `None` and booleans the way Python's `str()` does: `96`, `96.0`, `44.1`, `None`, `True`.

Recommendation: one helper, such as `pyStr(v any) string`, with its own goldens, and values carried with their Python type so `96` and `96.0` stay different.

Decided: `pyStr`, with values kept as `int64` and `float64` from the JSON. See [design.md](design.md#values).

## 5. `pathvalidate.sanitize_filename`

`clean_filename` ends with pathvalidate's `sanitize_filename`, a separate library with its own rules: platform, reserved names, length, trailing characters.

Recommendation: port the parts reachable after `clean_filename`'s own mapping, for the platform pathvalidate uses by default. Generate goldens from a broad corpus, including random Unicode, reserved names and long names, to find the rest.

Decided: port pathvalidate 3.3.1's universal-platform code, pinned in the venv, with corpus goldens. See [design.md](design.md#pathvalidate).

## 6. Unicode

NFC normalization, `str.casefold`, and `\w` in regexes, which is Unicode in Python and ASCII in Go.

Recommendation: `golang.org/x/text/unicode/norm` for NFC, `golang.org/x/text/cases` Fold for casefold, and `[\p{L}\p{N}_]` for Python's `\w`, all confirmed by goldens with non-Latin titles.

Decided: `x/text` for NFC and case mapping, hand-built classes for `\w` and `\s`, a scanner for the lookaround pattern. See [design.md](design.md#regular-expressions).

## 7. Writing FLAC tags

mutagen rewrites the Vorbis comment block, empties the vendor string, removes `ENCODER`, `ENCODED-BY` and `ENCODED_BY`, adds a PICTURE block when a cover is embedded, and saves with 8,192 bytes of padding.

Options: `github.com/go-flac/go-flac` with `flacvorbis` and `flacpicture`, or a small writer of our own.

Recommendation: the go-flac packages, after a spike that checks padding, vendor string, field order, and that the audio frames come out byte for byte. Golden comparison is on the tag list and its order, not on whole files.

Decided by the user: libraries, with workarounds only where needed. `go-flac` loads all audio into memory, so block I/O is our own and `flacvorbis` and `flacpicture` encode the blocks. See [design.md](design.md#tagging).

## 8. Multi-value tags

`ALBUMARTIST`, `ARTIST`, `COMPOSER`, `CONDUCTOR`, `ENSEMBLE` and `RELEASETYPE` can be lists. `settings.multi_value_tags` is off by default.

Recommendation: read in the source how lists are written with the setting off and on, port both, and pin both with goldens.

Decided: lists become several values with the setting off; the setting only splits `GENRE`. See [design.md](design.md#tagging).

## 9. MP3 tags

Python writes ID3v2.3 through mutagen: TYER and TDAT dates, TXXX for custom fields, APIC for the cover.

Recommendation: `github.com/bogem/id3v2/v2` set to version 3, after a spike that has mutagen read the Go-written file back and compare frames.

Decided: `bogem/id3v2/v2` at version 3 with UTF-16; frame order and padding differ from mutagen and are listed. See [design.md](design.md#tagging).

## 10. HTTP clients and timeouts

API calls want a short overall timeout. Transfers stream large files and need an idle timeout instead. The caller passes the client, which may carry a proxy.

Recommendation: the caller passes one `*http.Client` without a timeout. API calls get a per-call context deadline, 30 seconds by default. Transfers get a 10-second connect limit and a 60-second idle-read limit, as Python has. Options can override each.

Decided: as recommended, with `httptrace` timers for the connect and idle limits. See [design.md](design.md#transfer-engine).

## 11. Transfer engine

Python's direct transfer: `Range` resume, 206 appends, since 2.5.8 a 200 to a resumed request starts over, 416 means done, other statuses retry, 5 attempts with 2, 4, 8 and 16 seconds between, a 404 fails at once, success needs Content-Length bytes. Before each track it sleeps one second, a value hard-coded upstream.

Recommendation: port it as it is. Keep the one-second pause, but as a setting with one second as its default, so tests can set it to zero. Throttle progress callbacks, not the transfer.

Decided: ported as it is; `TrackPause` and `RetryDelays` are options. See [design.md](design.md#transfer-engine).

## 12. Concurrency

Python asks for each track's URL in order on the main thread and hands the transfers to a pool of `max_workers` threads, or one thread when `delay` is above zero.

Recommendation: the same shape, a sequential URL loop feeding a worker pool, with the same ordering of side effects, so file creation order and logs match.

Decided: as recommended. See [design.md](design.md#worker-pool).

## 13. Cancellation

The context ending must stop new tracks, abort transfers and remove temp files. Python has an `abort_event` for Ctrl+C.

Recommendation: read what Python does with the folder on abort and do the same. If it leaves `[IN PROGRESS]`, keep that, because a resumed download continues from it.

Decided: the context; `[IN PROGRESS]` becomes `[INCOMPLETE]` as Python does on Ctrl+C. See [design.md](design.md#cancellation).

## 14. Progress

Music Shelf needs release start with the planned tracks, track start, bytes done and total, track end with the file and its format or the error, and log lines.

Recommendation: one interface with those methods. The library calls the bytes method at most every 250 ms per track, plus once at the end of the track. A nil `Progress` is allowed.

Decided: one interface, called from several goroutines, nil allowed. See [design.md](design.md#progress-and-result).

## 15. Result

Music Shelf's provenance file needs the final folder, the release metadata used, and per track the position, file path, track ID, ISRC, delivered format, bit depth, sampling rate, and the outcome.

Recommendation: return a `Result` with exactly that, from every download function, including when it fails partway. Return an error only when the release could not start or the context ended.

Decided: `Result` with `Release` and `TrackResult`, returned also on failure. See [design.md](design.md#progress-and-result).

## 16. Errors

The upstream exceptions, HTTP failures with a status and an endpoint, and per-track failures inside a result.

Recommendation: sentinel errors for the upstream exceptions, an `*APIError` with status, endpoint and a short body excerpt that never includes tokens, and `errors.Is` and `errors.As` for both.

Decided: as recommended, plus `ErrUnhandled` for exceptions upstream doesn't catch. See [design.md](design.md#errors).

## 17. Segmented path

Session start, key unwrap with HKDF and AES-CBC, per-frame AES-CTR in the segments, then an ffmpeg remux to FLAC.

Recommendation: port the decryption into pure Go with `golang.org/x/crypto/hkdf`. Test it on a synthetic segment that a small Python builder in `gen.py` encrypts with upstream's own code, so both sides agree. Test the remux on a tiny fMP4 that ffmpeg makes on the spot, and skip that test when ffmpeg is missing.

Decided: pure Go with `crypto/hkdf`; segments built by `gen.py` and checked with upstream's decryption. See [design.md](design.md#segmented-path).

## 18. downloadsdb

Python creates the table and migrates older layouts. Music Shelf brings its own implementation of the interface.

Recommendation: `downloadsdb` with modernc SQLite creates the current schema and opens databases that Python 2.5.9 made. Port the old-layout migrations only if a real old database turns up.

Decided: modernc SQLite; both migrations ported, since they are short. See [design.md](design.md#downloadsdb).

## 19. qobuzdltest

Music Shelf's tests and this repo's scenario tests need a fake Qobuz.

Recommendation: an `httptest` server with recorded and cleaned answers for a multi-disc hi-res album, a CD album, a playlist, an artist and a label; a tiny valid FLAC served for every track; and switches for a 404, failures before success, a server that ignores `Range`, a demo answer, a quality restriction, and the segmented path.

Decided: as recommended, with fixtures recorded in step 1. See [design.md](design.md#qobuzdltest).

## 20. Golden generation

Recommendation: `testdata/gen/gen.py` reads inputs from `testdata/fixtures/` and writes `testdata/golden/*.json`. Goldens are committed, so CI never runs Python. `make golden` rebuilds them in the venv from `docs/reference.md`.

Decided: as recommended, plus `record.py`, `parity.py` and `compare.py`, and pinned venv requirements. See [design.md](design.md#golden-generation).

## 21. CLI

Recommendation: `qobuz-dl dl [flags] <url>...` with the Python `dl` flags that map to ported settings, such as quality, no fallback, directory, folder and track formats, embedded art, original cover and the database path. Also `qobuz-dl search <albums|tracks|artists|playlists> <query>`. The token comes only from `QOBUZ_TOKEN`.

Decided: as recommended; defaults are `QobuzDL`'s, not the Python CLI's. See [design.md](design.md#cli).

## 22. Logs and secrets

Python logs a lot, with colors. The CDN URLs carry signed tokens.

Recommendation: slog, info for release and track milestones and debug for the rest. Never log the user token, the app secrets or full CDN URLs; log the path without the query instead.

Decided: as recommended. See [design.md](design.md#logs-and-secrets).

## 23. Versions

Recommendation: semver of this repo, `v0.x` until Music Shelf's phase 1 is deployed. `UPSTREAM.md` maps each tag to the qobuz-dl version it ports. The Go version follows the user's other projects.

Decided: as recommended, `go 1.27.3`. See [design.md](design.md#versions).

## 24. Step order

A suggested order for `docs/build/`, to be changed if the design needs it:

1. Golden generator, fixtures and the first goldens.
2. `format.go`, `utils.go` and `constants.go`: naming, sanitizing, URL parsing.
3. `bundle.go` and `qopy.go`: client, auth, signing, endpoints, search.
4. `metadata.go`: FLAC and MP3 tags.
5. `downloader.go`: direct transfer, folder states, extras, worker pool.
6. The segmented path.
7. `core.go`: `HandleURL`, `DownloadFromID`, search by type, smart discography.
8. `downloadsdb/` and `qobuzdltest/`.
9. `cmd/qobuz-dl` and CI.
10. Parity run and the `v0.1.0` tag.

Decided: eleven steps; the fake server comes before the downloader, and CI starts in step 1. See [design.md](design.md#steps).

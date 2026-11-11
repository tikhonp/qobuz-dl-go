# Requirements

What qobuz-dl-go must do and who uses it. `docs/design.md` describes how it does it.

## Users

- Music Shelf's Qobuz module, `internal/modules/qobuz` in `~/projects/shelf`. It searches, resolves items for its UI, downloads into the music library with live progress, asks its own database whether an item was downloaded before, and sends traffic through its own `*http.Client`, which may use a proxy. Its needs are in `~/projects/shelf/docs/sources/qobuz.md` and `docs/provenance.md` there.
- The `qobuz-dl` CLI in this repo, for parity runs against Python qobuz-dl and for manual downloads.

## Parity target

qobuz-dl 2.5.9, run with the settings Music Shelf's predecessor, a Python Telegram bot, uses in production:

| Setting | Value |
| --- | --- |
| `quality` | 27 |
| `quality_fallback` | on |
| `settings.max_workers` | 3 |
| `settings.delay` | 0 |
| `settings.embed_art` | off, so `cover.jpg` is saved and nothing is embedded |
| `settings.saved_art_size` | `org` |
| `no_credits` | off, so the tracklist file is written |
| `no_m3u_for_playlists` | off |
| `smart_discography` | off |
| `fetch_lyrics` | off |
| `force_english` | on |
| `folder_format` | `{artist} - {album} ({year}) [{bit_depth}B-{sampling_rate}kHz]` |
| `track_format` | `{track_number} - {track_title}` |
| `downloads_db` | set |

For the same album, the port must write the same folder names, file names, tags, cover and extras as Python. The only allowed differences are the ones listed in `UPSTREAM.md`.

## Functions

1. Setup: fetch the web player bundle, get the app ID and the secrets, sign in with a user auth token through `user/get`, pick the working secret. Expose the subscription label and the user ID.
2. Catalog reads, as typed values: an album with all its tracks, a track, a playlist with all its tracks, an artist with all its albums, a label with all its albums. They carry at least what Music Shelf shows and records: title, version, artists, release date, label, UPC, genres, explicit flag, track count, duration, image URLs, hi-res flag, maximum bit depth and sampling rate, and per track the ID, ISRC, track number, disc number and duration.
3. Search for albums, tracks, artists and playlists with a query, a limit and an offset, answering the items and the total.
4. URL parsing exactly as `get_url_info`.
5. Downloads through `DownloadFromID` for albums and tracks and `HandleURL` for every URL kind, with Python's folder states, naming, extras, playlist m3u8, multi-disc layout, quality gate and fallback tiers, the demo handling from 2.5.6, the resume rule from 2.5.8, transfer retries, and the segmented path with its ffmpeg remux.
6. A result for every download: the final folder; the release metadata that was used, so Music Shelf can write its provenance file; and per track the file path, the delivered format with bit depth and sampling rate, and the outcome, which is done, skipped because the file existed, or failed with the error.
7. Progress through an interface: release start, track start, bytes, track end, log lines.
8. Cancellation through the context: no new tracks start, transfers stop, temp files are removed.
9. FLAC and MP3 tags as `metadata.py` writes them in 2.5.9.
10. The downloads database behind an interface with Python's semantics, an ID and a quality, plus a SQLite implementation whose schema matches Python's, so the CLI can share a `qobuz_dl.db` with Python.
11. `qobuzdltest`: a fake API and CDN that Music Shelf's tests reuse.
12. The CLI: `qobuz-dl dl <url>...` and `qobuz-dl search <type> <query>`, with the Python `dl` flags that map to ported settings. The token comes from the environment.

## Constraints

- Go 1.27.4, module `github.com/tikhonp/qobuz-dl-go`, package `qobuzdl` at the repo root. Pure Go, builds with `CGO_ENABLED=0`.
- No package-level mutable state. A `Client` is safe for concurrent calls.
- Every network call takes a `context.Context` and has a timeout.
- No `os.Exit`, no printing outside `cmd/qobuz-dl`. Logs go to an injected `*slog.Logger`.
- Folders 0755, files 0644, temp files removed on failure.
- The token, the app secrets, and signed CDN URLs never appear in logs or error messages.
- Few dependencies. SQLite only in `downloadsdb`.
- GPL-3.0, with credit to Sei969/qobuz-dl and vitiko98/qobuz-dl.
- Tests use the standard `testing` package only, with no network in unit tests, and pass under `-race`. Golden data comes from running Python 2.5.9.
- CI on GitHub Actions: gofmt, vet, `go test -race`, golangci-lint v2.15.0, govulncheck.

## Parity checks

- Golden tests cover `clean_filename`, `get_url_info`, folder and track naming, the FLAC and MP3 tag maps, request signatures, secret derivation, the smart discography filter, m3u8 ordering, release types and per-disc track totals.
- `make parity` on a multi-disc hi-res album, a CD-quality album and a playlist shows only the differences in `UPSTREAM.md`. `docs/status.md` lists the reference albums.

## Not ported

Everything under Not ported in `UPSTREAM.md`: the Python CLI and its config file, lyrics, sync and imports, interactive and lucky modes, batch files, favorites, the blacklist and booklet-only downloads.

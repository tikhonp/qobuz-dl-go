# Status

## Now

The docs exist; no code yet. Next: the port itself, which is step 1c of Music Shelf's phase 1. Its contract is `~/projects/proj-x/docs/build/1c.md`.

Waiting on the user: the repositories `code.tikhonnnnn.com/tikhon/qobuz-dl-go` and `github.com/tikhonp/qobuz-dl-go`, both public, the second as a push mirror of the first.

## Plan for the port

1. `testdata/gen/gen.py` and `make golden` against qobuz-dl 2.5.9, so every later piece has golden data.
2. `utils.go`, `format.go` and `constants.go`: naming, sanitizing and URL parsing, checked by the goldens.
3. `bundle.go` and `qopy.go`: the client, auth, signing, endpoints, the segmented path.
4. `metadata.go`: FLAC and MP3 tags.
5. `downloader.go`: transfers, folder states, extras.
6. `core.go`: `HandleURL`, `DownloadFromID`, search, smart discography.
7. `downloadsdb/`, `qobuzdltest/`, `cmd/qobuz-dl`, CI, then `make parity` on real albums and the `v0.1.0` tag.

## Log

### 2026-11-03

- Repo initialised. `CLAUDE.md`, `README.md`, `UPSTREAM.md`, `docs/port-spec.md`, `LICENSE` from qobuz-dl 2.5.9.

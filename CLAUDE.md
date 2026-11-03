# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

qobuz-dl-go is a Go library and a small CLI that port qobuz-dl, a Python Qobuz downloader, file by file. The first user is Music Shelf, in `~/projects/proj-x` with the module `github.com/tikhonp/shelf`, which imports the package. Docs, code and logs are in English.

State: the docs exist and no code does yet. Step 1c of Music Shelf's phase 1 builds the port; its contract is `~/projects/proj-x/docs/build/1c.md`. `docs/status.md` says what is done and what comes next.

## Read first

- `docs/status.md`: where the work stands.
- `UPSTREAM.md`: the ported version, the map from Python functions to Go functions, and the deliberate differences.
- `docs/port-spec.md`: how qobuz-dl behaves, as notes for the port.
- The upstream source at the tag in `UPSTREAM.md`. When the notes and the source disagree, the source wins.

## Rules for all code

- One Go file per ported Python file, with the same base name. Each file starts with a comment naming the Python file and tag it ports.
- Function names follow the Python ones: `get_track_url` becomes `GetTrackURL`, `_get_format` becomes `getFormat`.
- Behavior follows Python, byte for byte where it shows: folder names, file names, sanitizing, tags, quality gates and fallback. Every difference is listed in `UPSTREAM.md` with its reason.
- No package-level state. Every network call takes a `context.Context` and has a timeout. The caller passes the `*http.Client`.
- The core package imports nothing from Music Shelf and does not depend on SQLite. The downloads database lives in `downloadsdb/` behind an interface.
- Folders are created 0755 and files 0644. Temp files are removed on failure.
- Nothing calls `os.Exit` or prints to stdout outside `cmd/qobuz-dl`. Progress goes through the `Progress` interface, logs through a `*slog.Logger`.
- Golden test data comes from running the Python code, never from guessing.
- Comments say why, not what.

## How to work

- Run `/tikhon:unslop` on all prose before committing: docs, this file, the README and code comments.
- Commit messages are one short line, such as `Port bundle.py`, with no body. Commit on `main` and create no branches.
- At the end of a work session update `docs/status.md`; after porting upstream changes update `UPSTREAM.md`.
- When tests, lint, a build or a subagent's work fails, fix it and go on. Ask only when it can't be solved, and mention the fix in the summary.
- Never run `caffeinate` or `pkill caffeinate`.

## Commands

Added with the code. Planned:

```sh
make check                # gofmt, vet, go test -race
make lint                 # golangci-lint and govulncheck in Docker
make golden               # regenerate testdata/golden from qobuz-dl in testdata/gen/.venv
make upstream-diff TO=v2.6.0   # diff of the ported upstream files since UPSTREAM.md's tag
make parity URL=<album>   # one album through Python qobuz-dl and the Go CLI, then compare files and tags
```

## Layout

Planned:

```
bundle.go qopy.go core.go downloader.go metadata.go utils.go settings.go constants.go errors.go format.go progress.go
downloadsdb/              the Python-compatible downloads table, in SQLite
qobuzdltest/              a fake API and CDN for tests here and in Music Shelf
cmd/qobuz-dl/             dl and search, for parity runs
testdata/gen/gen.py       writes golden files from Python qobuz-dl
testdata/golden/          the golden files
```

## Releases

Tags are this repo's own semver, starting at `v0.1.0`. `UPSTREAM.md` records which qobuz-dl version each tag ports. The repo lives only on GitHub, at `github.com/tikhonp/qobuz-dl-go`, public, and `origin` points there. CI runs on GitHub Actions from `.github/workflows/ci.yaml`: gofmt, vet, `go test -race`, golangci-lint v2.15.0 and govulncheck.

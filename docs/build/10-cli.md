# 10: CLI and parity tools

## Read first

- [design.md](../design.md) sections CLI, Golden generation (`parity.py`, `compare.py`), CI and Make.
- Upstream `commands.py` `dl_args` and the shared flags, `cli.py` from `main` to the `QobuzDL` call.
- [reference.md](../reference.md) section Qobuz token.
- The As built section of step 9.

## Done when

`cmd/qobuz-dl` downloads and searches; `parity.py` and `compare.py` work; `make parity` runs end to end against `qobuzdltest` in a test mode, and against Qobuz once the token is exported; the README shows library and CLI usage.

## Decisions

| Topic | Decision | Why |
| --- | --- | --- |
| Flags | Standard `flag` with every Python name registered, long and short; defaults from `DefaultOptions`. | No dependency; Python's names work. |
| Token | Only `QOBUZ_TOKEN`; missing is a usage error. Never printed. | reference.md. |
| Output | A progress printer on stdout: one line per release start, per finished track, per log line at info and above; `-v` adds debug logs on stderr. No bars. | Parity logs stay diffable. |
| Exit status | 0 when every release is done or skipped, 1 otherwise, 2 for usage. | Scripts can tell. |
| Database default | `~/.config/qobuz-dl/qobuz_dl.db`, created when missing. | Python CLI's location, so both can share it. |
| `parity.py` | Builds `QobuzDL` like the bot: `QobuzDL(directory, quality=27, quality_fallback=True, downloads_db=None, settings=QobuzDLSettings(user_auth_token=token))`, then `get_tokens`, `initialize_client("", token, app_id, secrets)`, `handle_url(url)`. | The production path, not the Python CLI's config. |
| `compare.py` | Walks both trees; reports paths only on one side, size differences of non-audio files, tag differences per audio file (FLAC comments in order, MP3 frames as sets), and stream info. Exits non-zero on any difference not in an allow list file, `testdata/gen/parity-allow.txt`, whose lines reference `UPSTREAM.md` rows. | Differences must be listed to pass. |
| `make parity` | `URL=` required; runs Python into `out/parity/py`, the CLI with `-q 27 --no-db` into `out/parity/go`, then `compare.py`. | One command per album. |

## Files

```
cmd/qobuz-dl/main.go  dl.go  search.go  printer.go
cmd/qobuz-dl/main_test.go
testdata/gen/parity.py  compare.py  parity-allow.txt
Makefile  README.md
```

## Checklist

| Edge case | Test |
| --- | --- |
| Every Python `dl` flag maps to its setting | `TestFlagsMapToOptions` |
| `-ff` and `--folder-format` both work | `TestShortAndLongFlags` |
| Missing `QOBUZ_TOKEN` is exit 2 with a message that names the variable and not a value | `TestMissingToken` |
| `dl` against `qobuzdltest` writes the album tree and exits 0; a failed track exits 1 | `TestDLAgainstFake` |
| `search albums` prints Python's lines | `TestSearchAgainstFake` |
| The token never reaches stdout or stderr, also with `-v` | `TestCLINeverPrintsToken` |
| `compare.py` finds a renamed file, a changed tag and an extra file, and passes identical trees | `testdata/gen/compare_test.py`, run in the session, result in As built |

## As built

Done on 2026-11-04. `make check` and `make lint` pass, with 0 golangci-lint issues and no vulnerabilities. `make golden` didn't run, since this step adds no golden.

### Differences from the contract

- `make parity`'s test mode is `make parity FAKE=1 [URL="..."]`. It runs `TestParityAgainstFake` in `cmd/qobuz-dl`, which starts a fresh `qobuzdltest` per URL. In that test, `parity.py --fake <server>` runs Python, the CLI runs in process with the fake's `Configure`, and `compare.py` compares the trees in `out/parity/<n>/`. The test skips unless `QOBUZDL_PARITY_FAKE` is set, so `make check` and CI, which have no venv, skip it. The CLI binary has no hidden switch to another server. Without `URL` the test runs the multi-disc album, the CD album and the playlist.
- `parity.py --fake` patches requests' `HTTPAdapter.send` to send the API and web player URLs to the fake, and makes `time.sleep` return at once. The fake already points CDN and static URLs at itself.
- In both modes the Python and Go download lines in the Makefile carry `-`, so a failed download still leaves its tree for `compare.py`.
- `compare.py` also reports `content` for non-audio files of the same size with other bytes, `unreadable` for audio mutagen can't read, the FLAC vendor string and pictures, the ID3 version, and a SHA-256 of the audio frames under `stream`. The contract named sizes only.
- The allow list's format is `KIND PATH-PATTERN [KEY-PATTERN] -- UPSTREAM.md: TEXT`, split as a shell splits. `compare.py` refuses a line whose `TEXT` isn't in `UPSTREAM.md`, and prints the lines nothing used. `parity-allow.txt` has no entries yet.
- `make compare-test` runs `compare_test.py`.
- `parity.py --db FILE` passes a downloads database, for step 11's shared-database run. The default stays `None`.
- Every Python `dl` flag is registered. Those of ported settings map to them. `--with-credits` cancels `--no-credits`, as in `cli.py`. `--og-cover`, `--no-lyrics`, `--no-lrc-files` and `--no-embed-lyrics` change nothing, as nothing here reads them. `-b`, `--blacklist`, `--filter-label`, `--playlist-as-albums` and `--booklet-only` are usage errors that say "not ported". `-v` is new.
- Python's argparse reads flags after the positional arguments; `parseInterleaved` does the same over Go's `flag`, and `--` ends the flags.
- An empty value for `-d`, `-ff`, `-fbff`, `-tf`, `--multiple-disc-prefix` or `--multiple-disc-track-format` takes the default, as Python's `or` chains do. `--max-workers 0` means 3, as in `settings.py`. A negative `--delay` is 0.
- The default database follows `cli.py`: `$CONFIG_DIR/qobuz-dl/qobuz_dl.db`, else `%APPDATA%` on Windows, else `$HOME/.config`. The folder is made 0755 when missing.
- `dl` turns `open.qobuz.com` into `play.qobuz.com`, as `download_list_of_urls` does. Text files of URLs and Last.fm links stay unported. After `ErrInvalidURL` or `ErrEmptyContent` it goes on to the next URL; other errors stop the run with status 1, where Python's CLI dies.
- `search` prints `text   url` per result, takes plural or singular types, and treats a query under 3 characters as a usage error. It runs with `Directory` set to `.`, since `New` creates the folder.
- The printer's lines are `== <kind> <id>: <title>, <n> tracks, in <folder>` per release start, `  [<pos>] done: <path> (FLAC 24-bit 96kHz)`, `  [<pos>] exists: ...` or `  [<pos>] failed: <title>: <error>` per track, and each `Progress.Log` line as it comes, a trailing newline included. Paths are relative to the download folder. A track's path is in the work folder, `[IN PROGRESS] <name>`, as `TrackEnd` reports it.
- Without `-v` the CLI has no logger, so the lines `NewClient` and `InitializeClient` log, such as `Set max quality: 27 - 24 bit, >96kHz`, don't show. They don't pass through a `Progress`.
- Extra tests: `TestFlagsAfterTheURLs`, `TestUsageErrors`, `TestDLGoesOnAfterAnInvalidURL`, `TestDLReadsOpenQobuzLinks`, `TestDLUsesTheDownloadsDatabase`, `TestDLPlaylist`, `TestParityAgainstFake`. `TestDLAgainstFake` also checks that a second run finds every file there.
- `Makefile`'s `PORTED` now includes `commands.py`, so `make upstream-diff` shows changes to the flags.

### Checks

- `compare_test.py`: 16 tests pass. Identical trees exit 0. A renamed file gives `only-py` and `only-go`, an extra file `only-go`, an extra folder `only-py`. A changed FLAC comment gives `tags` with the key, a reordered comment list `(order)`, an added picture `(pictures)`. A changed MP3 frame gives `tags` with its hash key, the same frames in another order with other padding give nothing, and a v2.4 save gives `(version)`. Another FLAC gives `stream`, a flipped byte in the audio `audio-sha256`, a broken MP3 `unreadable`. A text file gives `size` or `content`. The allow list passes what it names, keeps its key pattern, reports unused lines, and refuses lines without a known `UPSTREAM.md` reference.
- `make parity FAKE=1`: the default three URLs give 0 differences. So do Begin Again, the compilation, the album with a version, Mahler 2, Beethoven 3, the single track and `ArtistAlbums`. Every tree had all its files on both sides. A tag changed by hand in the Go tree of the CD album made `compare.py` exit 1 and name the key.

### Only with a token

`make parity URL=<url>` against Qobuz has not run; that is step 11. Not checked here: `parity.py` without `--fake`, the real bundle, and whatever real answers do that the fake doesn't, such as segmented-only tracks, restrictions, `bits_depth` from `file/url`, and m3u8 lengths of real files.

### Notes for later steps

- Step 11: run Python first, then the CLI, as the Makefile does, from a clean `out/parity`. A third run over the finished Go tree checks the resume: every track line should say `exists`. For the shared-database run, give `parity.py --db <file>` and the CLI `--db <file>` the same file. Without `--db`, `parity.py` runs with `downloads_db=None`, as the bot does.
- Step 11: a difference goes into `parity-allow.txt` only with an `UPSTREAM.md` row whose text the line quotes.
- `make parity` downloads to `out/parity/` and deletes it first.

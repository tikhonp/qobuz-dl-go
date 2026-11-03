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

Filled in at the end of the step.

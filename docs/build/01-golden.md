# 1: golden generator and fixtures

## Read first

- `CLAUDE.md`, [design.md](../design.md) sections Package layout, Golden generation, Testing, CI and Make.
- [reference.md](../reference.md): the venv, the token rules.
- Upstream `utils.py`, `downloader.py` up to `process_folder_format_with_subdirs`, `qopy.py` `_normalize_json_strings`.
- pathvalidate 3.3.1's `_filename.py` and `_filepath.py` in `testdata/gen/.venv`, to know what the corpus has to reach.

## Needs the user

`QOBUZ_TOKEN` exported in the shell before the session starts, for `make record`. Without it, do everything else, leave `testdata/fixtures/api/` empty, and say so in As built.

## Done when

- `go.mod` (`go 1.27.3`), an empty `doc.go` for package `qobuzdl` with the GPL and credit header, `Makefile`, `.github/workflows/ci.yaml`, and CI green on GitHub.
- `testdata/gen/requirements.txt` pins the venv; `make golden` recreates every golden file byte for byte.
- `record.py` has recorded and cleaned the fixture answers into `testdata/fixtures/api/`, and the chosen IDs are in `docs/status.md` under Fixture items.
- `gen.py` writes the Python-helper goldens listed below, and a Go test helper loads golden files.
- The tiny audio and image fixtures exist.

## Decisions

| Topic | Decision | Why |
| --- | --- | --- |
| Golden format | One JSON file per topic: `{"generator": "qobuz-dl 2.5.9", "cases": [{"name", "input", "output"} or {"name", "input", "error"}]}`, `error` being the Python exception class name. JSON written with `ensure_ascii=False, indent=1, sort_keys=False`. | Readable diffs; one loader in Go. |
| Python values in goldens | A value whose type matters is tagged: `{"py": "float", "v": 96.0}`, `{"py": "int", "v": 96}`, `{"py": "none"}`. | JSON can't tell `96` from `96.0` once parsed in Go. |
| Corpora | `testdata/fixtures/strings/*.txt`, one input per line with `\uXXXX` escapes allowed: hand-picked cases plus 500 random strings from a fixed seed, drawn from ASCII, Latin-1, Cyrillic, CJK, Arabic, combining marks, emoji, the separators and brackets `clean_filename` touches, and control characters. | Unicode edge cases come from random input, not from guessing. |
| Recorded IDs | Picked by `record.py --search` and confirmed in the session: a multi-disc 24/192 album, a 24/96 album, a CD album, a compilation with "Various Artists", an album with a version in the title, a classical album with `work` and conductor credits, an album with goodies, a single track, a playlist of about 10 tracks with a multi-disc track in it, an artist with a few albums, a small label. | Tag and naming edge cases from real data. |
| Cleaning | Signed URLs become `{{CDN}}/file/<track id>/<format>`; `user/get` is not recorded; `getFileUrl` answers are recorded per track and format with `url` cleaned. Key order is kept. | The repo is public; the fake rewrites `{{CDN}}`. |
| Audio fixtures | `testdata/fixtures/audio/`: 0.1-second sine FLACs at 16/44.1, 24/96 and 24/192, one with existing Vorbis comments including `ENCODER`; an MP3 320 with a `TSSE` frame; a 1×1 JPEG; a one-page PDF. Made with the ffmpeg commands written into `testdata/gen/README.md`. | Small enough to commit; real enough for mutagen and ffmpeg. |
| CI | proxier's job shape, without templ and images. ffmpeg from apt in the test job. | Same conventions as the user's other repos. |

## Files

```
go.mod  doc.go  Makefile  .github/workflows/ci.yaml
golden_test.go                      loadGolden(t, name) and the Python value decoder for tagged values
testdata/gen/requirements.txt  README.md  gen.py  record.py
testdata/fixtures/api/*.json  strings/*.txt  templates/*.json  audio/*
testdata/golden/pystr.json  format.json  strings.json  textwrap.json  regex.json
testdata/golden/clean_filename.json  pathvalidate.json  url_info.json  normalize_json.json  name_length.json
```

## Goldens

| File | Cases |
| --- | --- |
| `pystr.json` | `str()` of ints, floats near the fixed/scientific switch, negative zero, `None`, booleans |
| `format.json` | templates × attribute sets: every key in the attribute tables, specs `02`, `>5`, `.1f`, `d` on strings, `!r`, `[0]`, `.attr`, `{}`, `{{`, unbalanced braces; output or exception class |
| `strings.json` | `lower`, `upper`, `casefold`, `title`, `capitalize`, `strip()`, `strip(' ."-_\'')`, `isdigit`, `len`, NFC over the corpus |
| `textwrap.json` | `textwrap.fill(width=70)` over album descriptions from the fixtures and the corpus |
| `regex.json` | which corpus characters Python's `\w` and `\s` match |
| `clean_filename.json` | `clean_filename` with and without `legacy_charmap` over the corpus and the recorded titles |
| `pathvalidate.json` | `sanitize_filename` and `sanitize_filepath`, replacement `""` and `"_"`, over the corpus, reserved names, names up to 1,000 bytes |
| `url_info.json` | `get_url_info` over every URL form in shelf's sources/qobuz.md, with locales, slugs, `open.`, `play.`, `/interpreter/`, Unicode slugs, junk; groups or exception |
| `normalize_json.json` | `_normalize_json_strings` on nested values with `...`, `://` and decomposed characters |
| `name_length.json` | `_shorten_utf8` over corpus names and byte limits |

## Checklist

| Edge case | Test |
| --- | --- |
| A golden file loads, tagged values keep int against float | `TestLoadGoldenKeepsPythonTypes` |
| `make golden` twice gives the same files | checked by hand in the session, noted in As built |
| CI runs gofmt, vet, tests, lint, govulncheck | the first green run, linked in As built |

## As built

Filled in at the end of the step.

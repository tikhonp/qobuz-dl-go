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

Done on 2026-11-03. First CI run, green: [41662609568](https://github.com/tikhonp/qobuz-dl-go/actions/runs/41662609568), with gofmt, build, vet, `go test -race` with ffmpeg installed, golangci-lint v2.15.0 and govulncheck v1.9.0.

### Differences from the contract

- `record.py` also replaces the `blob` field of `getFileUrl` answers with `{{BLOB}}`. It is a signed token that upstream never reads.
- `record.py` saves each raw response body, which a `requests` response hook captures. What `api_call` returns has already been through `_normalize_json_strings`, and the Go client runs that itself.
- `testdata/fixtures/api/index.json` lists every recorded request: endpoint, parameters, status and file. File names are `<endpoint with - for />-<id>[-<offset or format>].json`, such as `album-get-evz7usmruz35a.json`, `playlist-get-2082798-0.json` and `track-getFileUrl-20273753-27.json`. Searches are `catalog-search-<type>.json`.
- `record.py` records `getFileUrl` for every track of every recorded album, the single track and the playlist, at formats 5, 6, 7 and 27: 352 answers. All 367 answers are 200s.
- `gen.py` tags every int, float and None in the goldens, not only those whose type matters, so the loader can refuse a plain number or null as a generator bug. Non-finite floats are `{"py": "float", "v": "inf"}`, `"-inf"` or `"nan"`.
- `regex.json` gives the full code-point sets of `\w`, `\s` and `\d` as hex ranges such as `"0030-0039"`, a superset of "which corpus characters".
- `format.json` runs the full template list only against the album sets at quality 27, the first track of each album, the single track and its file set. The other sets get every key at once plus the bot and default templates, which keeps the file at 1.7 MB.
- `strings.json` also has `lstrip` and `rstrip` with `' ."-_\''`, which `_shorten_utf8` uses.
- The corpus format allows `\\`, `\uXXXX` and `\UXXXXXXXX`, and any other escape stops `gen.py`. `strings/reserved.txt` and `strings/urls.txt` sit next to `handpicked.txt` and `random.txt`.
- The audio fixtures are `16-44.1.flac`, `24-96.flac`, `24-192.flac`, `16-44.1-tagged.flac`, `320.mp3`, `cover.jpg` and `booklet.pdf`. ffmpeg overwrites any `ENCODER` or `TSSE` given with `-metadata` with its own `Lavf63.1.102`, so the tagged FLAC's `ENCODER` and the MP3's `TSSE` are that value. The FLAC also has `TITLE`, `ARTIST` and `DESCRIPTION`; the MP3 is ID3v2.3 with `TIT2` and `TSSE`.
- `make parity` exists, but the `parity.py` and `compare.py` it runs come with step 10.

### Checks

- `make golden` twice gives the same files byte for byte, `random.txt` included. `diff -r` between two runs, each with its own hash seed, shows no difference.
- The README's ffmpeg and PDF commands, run again, give byte-identical audio fixtures.
- `make check` and `make lint` pass: 0 golangci-lint issues, no vulnerabilities.

### Notes for later steps

- `golden_test.go` decodes objects into `*goldenDict`, which keeps key order. Step 2 can make the loader build `*pyDict` instead, or convert, and drop `goldenDict`.
- Go 1.27.3's `unicode` tables are Unicode 17.0.0; Python 3.14's `unicodedata` is 16.0.0. Code points assigned in 17.0 are in Go's `\p{L}` but not in Python's `\w`, so `pyre.go`'s classes have to follow `regex.json`, not `unicode` categories alone. The random corpus draws only code points assigned in Python's tables. x/text's `norm` and `cases` have their own Unicode version; check it in step 2.
- `getFileUrl` answers mix number types: `"sampling_rate": 96` is an int and `44.1` a float, and a request at 27 for a 24/96 track answers `format_id` 7 with a `FormatRestrictedByFormatAvailability` restriction. `url` in the fixture names the requested format, not the delivered one.
- `get_url_info` reads a URL with an extra path segment, such as `/label/<slug>/download-streaming-albums/<id>`, as `('label', 'download')`, and `/playlists/...` (plural) not at all. That is upstream's behavior and the goldens keep it; shelf may want to know.
- On 2026-11-03 the bundle's app ID was `798273057`, which `Client.__init__` treats as the legacy ID: the bot fetches the bundle twice and keeps the empty secret. This session corrected `port-spec.md` and step 4's contract.
- `album/get` answers have `tracks_count`, not `track_count`, so `{track_count}` is 1 in every album set, as port-spec says.

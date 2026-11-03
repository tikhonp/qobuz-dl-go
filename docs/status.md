# Status

## Now

Steps 1 and 2 are done: the module, CI, goldens and fixtures, and the Python compatibility layer (values, string methods, regex classes, `str.format`, `textwrap.fill`). No qobuz-dl file is ported yet.

Next: step 3, [utils, settings, errors, pathvalidate](build/03-utils.md). Start Claude Code in this repo and paste the prompt from [prompts/02-step.md](prompts/02-step.md). It needs no token.

## Steps

| Step | Contract | State |
| --- | --- | --- |
| 1 | [golden generator and fixtures](build/01-golden.md) | done |
| 2 | [Python compatibility layer](build/02-pycompat.md) | done |
| 3 | [utils, settings, errors, pathvalidate](build/03-utils.md) | next |
| 4 | [bundle and API client](build/04-client.md) | |
| 5 | [tags](build/05-tags.md) | |
| 6 | [fake Qobuz](build/06-fake.md) | |
| 7 | [downloader, direct path](build/07-downloader.md) | |
| 8 | [segmented path](build/08-segments.md) | |
| 9 | [core, m3u8, downloadsdb](build/09-core.md) | |
| 10 | [CLI and parity tools](build/10-cli.md) | |
| 11 | [parity and v0.1.0](build/11-parity.md) | needs `QOBUZ_TOKEN` |

## Fixture items

`record.py` recorded these on 2026-11-03. The IDs come from `record.py --search` and are also in `ITEMS` in `testdata/gen/record.py` and in `testdata/fixtures/api/index.json`.

| Kind | ID | Item | Why |
| --- | --- | --- | --- |
| multi-disc 24/192 album | `evz7usmruz35a` | Pink Floyd, Ummagumma | 2 discs, 16 tracks |
| 24/96 album | `k43q1q3kx88fa` | Norah Jones, Begin Again | 7 tracks |
| CD album | `0634904080853` | Radiohead, Com Lag: 2+2=5 | 16/44.1, `release_type` `epmini`, a colon in the title |
| Various Artists compilation | `zcs2h686vhj4b` | Now That's Not What I Call Music | 20 tracks, a label with 15 albums |
| album with a version | `b2d4k8h8pu6zb` | Norah Jones, ...'Til We Meet Again (Live) | version `Live`, a title starting with `...` and a curly quote |
| classical album | `xf2zfbfjz0w1b` | Budapest Festival Orchestra, Mahler: Symphony No. 2 | `work` and conductor credits on every track, 2 discs, 24/192, `product_type` `single`, a booklet |
| album with goodies | `el3qez8zpc8bc` | Budapest Festival Orchestra, Beethoven: Symphony No. 3 "Eroica" & Coriolan Overture | a PDF booklet, 24/192, quotes in the title |
| single track | `20273753` | Miles Davis, So What (Mono Version) | a track version, 24/96, an album that isn't recorded |
| playlist | `2082798` | Top 10 Operas | 10 tracks, three of them from discs 2 and 3 |
| artist | `50782` | Tord Gustavsen Trio | 8 albums, one page |
| label | `572190` | Naked Jain Records | 15 albums, one page |

Searches, limit 10: albums `miles davis kind of blue`, tracks `so what miles davis`, artists `miles davis`, playlists `jazz`. `record.py` also records `getFileUrl` for every track above at formats 5, 6, 7 and 27.

## Reference albums

Filled in by the parity run in step 11.

## Log

### 2026-11-03

- Repo initialised. `CLAUDE.md`, `README.md`, `UPSTREAM.md`, `docs/port-spec.md`, `LICENSE` from qobuz-dl 2.5.9.
- `origin` set to `git@github.com:tikhonp/qobuz-dl-go.git` and pushed.
- `docs/requirements.md`, `docs/design-questions.md`, `docs/reference.md`, and the session prompts in `docs/prompts/`.
- Design session. The upstream source at `v2.5.9` matches the PyPI build. `docs/port-spec.md` checked line by line against it and rewritten: about 40 corrections and additions, among them auth bypassing non-401 failures, `COMPILATION` set only for various-artists albums, the tracklist format, the transfer's short-read and overshoot rules, and which names use which pathvalidate replacement.
- Decisions by the user: dynamic values inside with typed views outside; explicit error returns for Python exceptions; tag libraries with workarounds only where needed; fixtures recorded in step 1; `HandleID` next to `HandleURL` for playlists.
- `docs/design.md`, eleven contracts in `docs/build/`, every question in `docs/design-questions.md` marked decided.
- For shelf: its 1d contract has playlists going through `DownloadFromID` (they go through `HandleID`) and an `http.Client` with a 30-second timeout (the client must have none; the library sets per-call timeouts). Its 1c contract mentions `PartialFormatter`, which upstream never uses, and typed structs, which are now the outer layer only.
- Step 1. `go.mod`, `doc.go`, `Makefile`, CI, `testdata/gen/` with `record.py`, `gen.py`, the venv pins and a README, 367 recorded answers, the string corpora, ten golden files and the audio fixtures. `make golden` is reproducible byte for byte. Details and notes for later steps are in [01-golden.md](build/01-golden.md) under As built.
- The bundle's app ID is `798273057` today, the ID `Client.__init__` treats as legacy, so the bot fetches the bundle twice and keeps the empty secret. `port-spec.md` and step 4's contract said the branch never runs. Both now say it does.
- For shelf: `docs/sources/qobuz.md` says the module fetches the bundle once, which holds only while the bundle's app ID isn't `798273057`. `get_url_info` reads label URLs with an extra segment, such as `/label/<slug>/download-streaming-albums/<id>`, as the ID `download`.
- Step 2. `pyvalue.go`, `pystr.go`, `pyre.go`, `format.go` and `textwrap.go` with their tests; every case of `pystr`, `format`, `strings`, `textwrap` and `regex` passes. A new golden, `unicode.json`, checks every code point's properties and case mappings against Python. Go and x/text are on Unicode 17.0 and Python 3.14 on 16.0; `newerThanPython` and three small fixes close the gap. Details are in [02-pycompat.md](build/02-pycompat.md) under As built.
- design.md had combining marks in `\w`, which Python's `\w` lacks, and said textwrap collapses whitespace runs, which it doesn't. Both now follow the source. UPSTREAM.md has three new deliberate differences: template attributes, a cap on format widths, and JSON ints beyond int64.

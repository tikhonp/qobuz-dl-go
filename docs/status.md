# Status

## Now

Steps 1 to 7 are done: the module, CI, goldens and fixtures, the Python compatibility layer, `constants.py`, `exceptions.py`, `settings.py`, `utils.py` without `make_m3u`, pathvalidate's sanitizers, the API client from `bundle.py` and `qopy.py` without the segmented branch, with the typed views, the taggers from `metadata.py`, `qobuzdltest`, the fake Qobuz, with `Options` from `core.py`, and `downloader.py` without the segmented transfer, with `Progress` and the results.

Next: step 8, [segmented path](build/08-segments.md). Start Claude Code in this repo and paste the prompt from [prompts/02-step.md](prompts/02-step.md). It needs no token.

## Steps

| Step | Contract | State |
| --- | --- | --- |
| 1 | [golden generator and fixtures](build/01-golden.md) | done |
| 2 | [Python compatibility layer](build/02-pycompat.md) | done |
| 3 | [utils, settings, errors, pathvalidate](build/03-utils.md) | done |
| 4 | [bundle and API client](build/04-client.md) | done |
| 5 | [tags](build/05-tags.md) | done |
| 6 | [fake Qobuz](build/06-fake.md) | done |
| 7 | [downloader, direct path](build/07-downloader.md) | done |
| 8 | [segmented path](build/08-segments.md) | next |
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
- Step 3. `constants.go`, `errors.go`, `settings.go`, `pathvalidate.go` and `utils.go` without `MakeM3U`, with their tests; every case of `clean_filename`, `pathvalidate`, `url_info`, `album_artist`, `smart_discography` and `settings` passes. Four new goldens: `album_artist.json`, `smart_discography.json`, `release_type.json` and `settings.json`. Details are in [03-utils.md](build/03-utils.md) under As built.
- `sanitize_filepath` cuts each entry to 260 bytes and empties a relative path with a blank entry; design.md and port-spec.md now say so. design.md's NFC row now names `pyNFC`, since x/text's Unicode 17.0 tables give 34 new code points a combining class that Python doesn't know.
- Step 4. `bundle.go`, `qopy.go` without the segmented branch, `catalog.go` and `transport.go`, with their tests. Four new goldens: `secrets.json`, `signatures.json`, `session_key.json`, and `request_params.json`, a transcript of 58 runs of upstream's `Client` against a recording transport that the Go client must match request by request. `gen.py` also writes a synthetic login page and bundle into `testdata/fixtures/bundle/`. Details are in [04-client.md](build/04-client.md) under As built.
- Python 3.14's base64 decoding is lenient in ways the bundle depends on; `pyB64Decode` ports it. port-spec.md now says how, and that requests keeps `User-Agent` first and `X-App-Id` last on the wire.
- Step 5. `metadata.go` and `flac.go` with their tests. Six new goldens: `tags.json`, `metadata_helpers.json`, `release_types.json`, `disc_track_total.json`, `flac_tagged.json` and `mp3_tagged.json`, and five audio fixtures `gen.py` builds from the ffmpeg ones. Tagged FLAC files are byte for byte mutagen's, and every MP3 frame's bytes are too. Details are in [05-tags.md](build/05-tags.md) under As built.
- mutagen keeps FLAC blocks in file order and adds a new comment block and pictures at the end; design.md and port-spec.md had VORBIS_COMMENT and PICTURE always last. Both now follow the source, and port-spec.md also says how mutagen translates existing ID3 frames on load.
- `id3v2` v2.1.4 writes UTF-16 TXXX values and APIC data one byte off. `metadata.go` writes those frames with its own `Framer` types through `id3v2`; design.md now says so.
- For shelf: `docs/sources/qobuz.md` says MP3 frame order and padding differ from mutagen's. That still holds, and each frame's bytes match mutagen's. Two more differences show only on MP3s that arrive with a tag: frames mutagen translates to v2.4 on load stay as they are, and an ID3v2.2 tag fails the track. Tagged FLAC files are byte for byte Python's.
- Step 6. `qobuzdltest` with `server.go`, `fixtures.go`, `cdn.go` and `switches.go`, and `Options`, `DefaultOptions` and `DownloadsDB` in `core.go`, which `Configure` needs. `make golden` copies the fixtures into `qobuzdltest/testdata/`, which the package embeds. A client built from `ClientOptions()` reads all 367 recorded answers. Details are in [06-fake.md](build/06-fake.md) under As built.
- The fake also answers `track/get` for the tracks of the recorded albums and playlist, since a playlist download calls it for each track and only one answer is recorded. It has built collections for paging and empty answers, and `Request` has the headers and the status. design.md's qobuzdltest section now says all this.
- For shelf: `qobuzdltest` has `PlaylistEmpty`, `ArtistEmpty` and `LabelEmpty` for 1d's `TestResolveEmptyCollection`, and `ExpireToken` for `TestHealthTokenExpired`. `catalog/search` answers the recorded search of each type whatever the query, so search tests can't check that a query reached Qobuz except through `Requests()`. `Configure` replaces `Options.HTTPClient` with the fake's; tests of shelf's own client, such as `TestHTTPClientHasNoTimeout` and `TestProxyOptIn`, can instead set `APIBase`, `PlayBase` and `Settings.UserAuthToken` by hand, since the fake is plain HTTP on localhost.
- Step 7. `downloader.go` without the segmented transfer, `progress.go`, `isRequestError`, and a `FailStatic` switch in the fake. Four new goldens: `naming.json`, `tracklist.json`, and `layout_album.json` and `layout_track.json`, whole download trees from upstream's `Download` run against a client that answers as the fake does. The Go trees match them, FLAC files byte for byte. Details are in [07-downloader.md](build/07-downloader.md) under As built.
- port-spec.md said a short body retries without sleeping. That holds only when a short body matches its own `Content-Length`. A dropped connection raises urllib3's `IncompleteRead`, which loses the unfinished 64 KiB chunk and sleeps before the retry. Python runs in the venv showed both, and port-spec.md now says so.
- `genreTag` now changes a copy of `genres_list`, where Python writes into the shared album; the workers raced on it. `TagMP3` now sets 0644 on the file `id3v2` saves.
- The commits of this step have no `Co-Authored-By` line, at the user's request, and were not pushed.
- For shelf: `DefaultOptions().Quality` is Python's 6; the bot runs 27. `Progress.TrackEnd` gets the track's path in the work folder, `[IN PROGRESS] <name>` for albums, and the `Result` has the final path. `TrackEnd` also fires for tracks that never started because the context ended. `qobuzdltest` has `FailStatic`, 404 on every cover and goody.

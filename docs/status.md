# Status

## Now

qobuz-dl-go ports qobuz-dl 2.5.9. `v0.1.1` is the latest tag, at `7ae8648`; it has the dependency update of 2026-11-11 and the same API as `v0.1.0`. Music Shelf builds its Qobuz module on it.

Parity runs against Qobuz on 2026-11-04 gave the same files from Python and Go, byte for byte. Reference albums lists them.

Next: port the next upstream tag with `docs/prompts/upstream-sync.md` once it appears. Upstream's last tag is still `v2.5.9`.

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

The parity runs of 2026-11-04, with qobuz-dl 2.5.9 in `testdata/gen/.venv` and the CLI at quality 27. Later parity runs use the same links. `make parity` runs Python, then Go, then Go again over Go's tree; `make parity-db` is the shared-database run.

| Run | Link | Item | Result |
| --- | --- | --- | --- |
| `make parity N=1` | https://open.qobuz.com/album/evz7usmruz35a | Pink Floyd, Ummagumma: 2 discs, 16 tracks, FLAC 24/192, 3.6 GB | 18 files, all byte for byte the same, the tracks in `CD 01` and `CD 02`, `cover.jpg` and the tracklist. `compare.py` found 0 differences. The resumed run found all 16 tracks. |
| `make parity N=2` | https://open.qobuz.com/album/0634904080853 | Radiohead, Com Lag: 2+2=5: 10 tracks, FLAC 16/44.1, 234 MB | 12 files, all byte for byte the same; the colon in the folder name became `：` on both sides. 0 differences. The resumed run found all 10. |
| `make parity N=3` | https://open.qobuz.com/playlist/2082798 | Top 10 Operas: 10 tracks, 7 at 16/44.1, 2 at 24/96, 1 at 24/44.1, 314 MB | 11 files, all byte for byte the same, `Top 10 Operas.m3u8` included. 0 differences. The resumed run found all 10, and the m3u8 stayed the same. |
| `make parity N=5` (2026-11-11) | https://open.qobuz.com/album/xf2zfbfjz0w1b | Budapest Festival Orchestra, Mahler: Symphony No. 2: 2 discs, 5 tracks, FLAC 24/192, 2.1 GB, a booklet | 8 files, all byte for byte the same, `Mahler： Symphony No. 2 (156455).pdf` (321075 bytes) included. 0 differences. The resumed run found all 5. |
| `make parity-db N=4` | https://open.qobuz.com/album/0634904080853 | the CD album | Python downloaded it and recorded `0634904080853`, `album`, quality 27, `FLAC`. The CLI, given the same `qobuz_dl.db`, logged "This release ID (0634904080853) was already downloaded according to the local database." and wrote nothing. |

Run 5 is the only real run with a booklet PDF; the fake runs also compare the booklets of Mahler 2 and Beethoven 3 in `make parity FAKE=1`. No track took the segmented path. `parity-allow.txt` stays empty.

## Log

### 2026-11-11

- Ran `make parity N=5` on Mahler 2, the first real run with a booklet. Python and Go gave the same 8 files, byte for byte, the PDF included, and the resumed run found all 5 tracks. The CLI's `done:` lines show the `[IN PROGRESS]` folder, because the downloader renames it after the last track. The folder on disk and the paths in the results have the final name.
- Checked upstream. The last tag is still `v2.5.9`, and PyPI's last release of `qobuz-dl-ultimate` is 2.5.9, so there is nothing to port. `master` is at `a57a8ad`, with commits named v2.6.0 to v2.6.7 that were never tagged or published. They add lyrics options, formats for the `ALBUM`, `DATE` and `GENRE` tags, and an interactive menu for an artist's albums. They write an `ALBUMVERSION` tag by default, add the format keys `{primary_genre}`, `{album_version}` and `{version_tag}`, and drop `{disc_number}`. `handle_url` on `master` calls `download_from_id` twice for each item, once in the new queue loop and again in the old one, so `master` is not ready to port. When a 2.6 tag appears, port it with `docs/prompts/upstream-sync.md`.
- Updated the dependencies: Go 1.27.4 in `go.mod` and the lint image, as in Music Shelf; `golang.org/x/text` v0.44.0, `golang.org/x/sys` v0.50.0, `modernc.org/sqlite` v1.62.0 and `modernc.org/libc` v1.81.0; `soupsieve` 4.1 in the golden venv. `pick` stays 1.6.0, which qobuz-dl pins. golangci-lint v2.15.0, govulncheck v1.9.0 and the GitHub actions were already the latest. `make golden` gives the same files, and `make check`, `make lint` and `make compare-test` pass.
- Homebrew had removed Python 3.14, so `make venv` failed; `brew install python@3.14` brought it back. reference.md now says so.
- For shelf: nothing in the API changed. The module now needs Go 1.27.4, which shelf already uses.
- `v0.1.1` is tagged at `7ae8648` and pushed. For shelf: move to `v0.1.1` for the new dependencies; the API is the same.
- CI's test job hung on `apt-get install ffmpeg` until the step timed out, twice in a row, while the same step had taken 22 seconds 20 minutes earlier. Short apt timeouts didn't help. The job now downloads BtbN's static ffmpeg 9.0 build from GitHub and checks it against the release's `checksums.sha256`. CI is green at `140e645`.
- Removed what only the build needed: `docs/design-questions.md`, the steps in `docs/design.md`, and the per-step log here. `git show ce371e4:<path>` shows them.

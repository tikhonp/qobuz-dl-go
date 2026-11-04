# 11: parity and v0.1.0

## Read first

- [requirements.md](../requirements.md) section Done for v0.1.0.
- [prompts/03-parity.md](../prompts/03-parity.md) and [prompts/04-release.md](../prompts/04-release.md).
- `UPSTREAM.md`, `docs/status.md` Fixture items.
- The As built sections of every step.

## Needs the user

`QOBUZ_TOKEN` exported. The runs download real albums, a few GB.

## Done when

- `make parity` on a multi-disc hi-res album, a CD album and a playlist, chosen as prompt 03 says and by default the fixture ones, shows only differences in `parity-allow.txt`, each backed by a row in `UPSTREAM.md`.
- Every golden and scenario test passes, CI is green.
- `UPSTREAM.md` records `v0.1.0` porting 2.5.9; `docs/status.md` lists the runs under Reference albums.
- `v0.1.0` is tagged and pushed, and shelf's 1c As built is told the tag.

## Decisions

| Topic | Decision | Why |
| --- | --- | --- |
| A difference found | Fix the port and add a golden or scenario that would have caught it; only a difference upstream makes on purpose by environment, such as file listing order, may go into `UPSTREAM.md`. | Parity means the same files. |
| Re-runs | Each run into a fresh `out/parity/<n>`; Python first, Go second, then Go again over the finished folder to check that a resumed run skips everything. | Resume is part of production use. |
| Downloads database | One run with a shared `qobuz_dl.db`: Python downloads album A, Go is asked for A and skips it. | The shared-database promise. |
| Tag | `git tag v0.1.0` on `main` after CI is green on that commit. | Release rule in CLAUDE.md. |

## Checklist

| Edge case | Test |
| --- | --- |
| Multi-disc 24/192 album | `make parity URL=…`, output in As built |
| CD album | `make parity URL=…` |
| Playlist with m3u8 | `make parity URL=…` |
| A resumed run skips every file | the third run of each |
| Go skips what Python recorded | the shared-database run |
| shelf's step 1d can build on `qobuzdltest` | `ExampleNew` from step 6, plus a check that shelf's 1d checklist needs nothing more |

## As built

The parity runs took place on 2026-11-04. The release followed on the same day with [prompt 4](../prompts/04-release.md). `v0.1.0` is tagged at `e6861b4`, proxy.golang.org resolves it, and shelf's 1c As built has the version. `make check` and `make lint` pass, with 0 golangci-lint issues and no vulnerabilities.

### Runs

The user chose the fixture items. Each run went into a fresh `out/parity/<n>` and ran Python, then Go, then `compare.py`, then Go again over Go's finished tree, then `compare.py` again. `cmp` also compared every file.

| Item | Link | Files | Result |
| --- | --- | --- | --- |
| Multi-disc 24/192 album: Pink Floyd, Ummagumma | https://open.qobuz.com/album/evz7usmruz35a | 16 FLACs at 24/192 in `CD 01` and `CD 02`, `cover.jpg`, `Ummagumma - Tracklist.txt`; 3.6 GB | 0 differences, every file byte for byte the same |
| CD album: Radiohead, Com Lag: 2+2=5 | https://open.qobuz.com/album/0634904080853 | 10 FLACs at 16/44.1, `cover.jpg`, `Com Lag 2+2=5 - Tracklist.txt`, in `Radiohead - Com Lag： 2+2=5 (2004) [16B-44.1kHz]`; 234 MB | 0 differences, byte for byte |
| Playlist: Top 10 Operas | https://open.qobuz.com/playlist/2082798 | 10 FLACs, 7 at 16/44.1, 2 at 24/96 and 1 at 24/44.1, and `Top 10 Operas.m3u8`; 314 MB | 0 differences, byte for byte, the m3u8 included |

- The second Go run of each item printed `exists` for every track and nothing else. It renamed the album folder to `[IN PROGRESS]` and back, as Python does, and kept `cover.jpg` and the tracklist it found there. The second compare gave 0 differences, the playlist's m3u8 included.
- The shared-database run was `make parity-db N=4` on the CD album. Python downloaded it with `--db` and wrote the row `0634904080853`, `album`, 27, `FLAC`. The CLI, given the same file and an empty folder, logged "This release ID (0634904080853) was already downloaded according to the local database." and wrote nothing.
- The Python and Go logs show the same lines, apart from tqdm's bars and sizes and the CLI's own track lines. Neither shows an error or a warning, and `QOBUZ_TOKEN` appears in none.

### Differences found

None, so nothing in the port changed, `parity-allow.txt` stays empty and `UPSTREAM.md` gains no row.

These runs didn't cover booklet PDFs, since none of the three items has one, or the segmented path, which no track took. Both match Python against the fake, booklets on Mahler 2 and Beethoven 3 in step 10's `make parity FAKE=1` and the segmented path in step 8's tests.

### Changes to the tools

- `make parity` takes `N`, empties and fills `out/parity/<N>`, and keeps each run's output in `py.log`, `go.log` and `resume.log`. Its last steps are the resumed run, which fails the target if a track downloads or fails or none is found, and a second compare.
- `make parity-db URL=<album> N=<n>` is the shared-database run. It fails unless the CLI says the album is in the database and leaves its folder empty.
- `make parity FAKE=1` writes to `out/parity/fake/<n>`. Before, it shared `out/parity/<n>` with the real runs and replaced their trees.

### shelf's step 1d

Read against `~/projects/shelf/docs/build/1d.md`. Every checklist line can build on `qobuzdltest` as it is, and `ExampleNew` passes. `TestResolveEmptyCollection` has `PlaylistEmpty`, `ArtistEmpty` and `LabelEmpty`, `TestHealthTokenExpired` has `ExpireToken`, the download and m3u8 tests have `Configure` and `HandleID`, and the client tests can set `APIBase`, `PlayBase` and the token by hand. 1d's contract now sends playlists through `HandleID` and gives the client no `Timeout`. Two things are left to shelf. `GetURLInfo` refuses `/interpreter/` links, as upstream does, and the port exports no version string for the qobuz-dl version in `DownloadResult.Tool`.

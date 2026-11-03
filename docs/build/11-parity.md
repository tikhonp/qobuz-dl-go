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

Filled in at the end of the step.

# Status

## Now

The design is done: [design.md](design.md) and the step contracts in [build/](build/). No code yet.

Next: step 1, [golden generator and fixtures](build/01-golden.md). Export `QOBUZ_TOKEN` in the shell before starting the session, for the fixture recording. Start Claude Code in this repo and paste the prompt from [prompts/02-step.md](prompts/02-step.md).

## Steps

| Step | Contract | State |
| --- | --- | --- |
| 1 | [golden generator and fixtures](build/01-golden.md) | next; needs `QOBUZ_TOKEN` |
| 2 | [Python compatibility layer](build/02-pycompat.md) | |
| 3 | [utils, settings, errors, pathvalidate](build/03-utils.md) | |
| 4 | [bundle and API client](build/04-client.md) | |
| 5 | [tags](build/05-tags.md) | |
| 6 | [fake Qobuz](build/06-fake.md) | |
| 7 | [downloader, direct path](build/07-downloader.md) | |
| 8 | [segmented path](build/08-segments.md) | |
| 9 | [core, m3u8, downloadsdb](build/09-core.md) | |
| 10 | [CLI and parity tools](build/10-cli.md) | |
| 11 | [parity and v0.1.0](build/11-parity.md) | needs `QOBUZ_TOKEN` |

## Fixture items

Filled in by step 1: the Qobuz IDs whose answers `record.py` recorded.

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

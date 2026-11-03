# Build contracts

One contract per step. A step runs in a fresh session started with [prompt 02](../prompts/02-step.md): read its contract and everything under Read first, build it, and finish with green checks.

Each contract has these sections:

- Read first: docs and code to read before starting.
- Done when: the result that ends the step.
- Decisions: topic, decision, why.
- Files: what the step adds or changes.
- Go signatures, when the step has them. The full set is in [design.md](../design.md).
- Checklist: each edge case with the name of its test.
- As built: filled in at the end of the step, with what differs from the contract and notes for later steps.

A step is finished when `make check` and `make lint` pass, CI is green, `docs/status.md` is updated, the As built section is filled, and the work is committed on `main` with one-line messages and pushed.

| Step | Contract | Depends on |
| --- | --- | --- |
| 1 | [golden generator and fixtures](01-golden.md) | |
| 2 | [Python compatibility layer](02-pycompat.md) | 1 |
| 3 | [utils, settings, errors, pathvalidate](03-utils.md) | 2 |
| 4 | [bundle and API client](04-client.md) | 3 |
| 5 | [tags](05-tags.md) | 3 |
| 6 | [fake Qobuz](06-fake.md) | 4 |
| 7 | [downloader, direct path](07-downloader.md) | 5, 6 |
| 8 | [segmented path](08-segments.md) | 7 |
| 9 | [core, m3u8, downloadsdb](09-core.md) | 8 |
| 10 | [CLI and parity tools](10-cli.md) | 9 |
| 11 | [parity and v0.1.0](11-parity.md) | 10 |

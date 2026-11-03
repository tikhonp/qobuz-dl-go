# Prompt 2: one step

One session per step from `docs/build/`. To pick a step other than the next one, name it on the last line.

```text
Implement the next step of qobuz-dl-go.

Read CLAUDE.md and docs/status.md, then the step's contract in docs/build/ and everything it lists under Read first, including docs/design.md. Keep the 2.5.9 upstream source open next to each Go file you write; when the source and the docs disagree, the source wins and you fix the docs.

Work until the contract's Done when is true: every checklist line has its test, and make check and make lint pass. Commit with one-line messages as you go.

At the end, fill in the contract's As built section with what differs from the contract and notes for later steps. Update docs/status.md, and UPSTREAM.md if the function map changed. Run /tikhon:unslop on the prose you changed, code comments included. Commit and push.

If a decision in docs/design.md turns out to be wrong, stop and ask me before changing it.

Step: the next one in docs/status.md
```

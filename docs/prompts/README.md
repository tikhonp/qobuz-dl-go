# Session prompts

Prompts to paste into Claude Code sessions started in this repo:

```sh
cd ~/projects/qobuz-dl-go && claude
```

Each session starts fresh. `CLAUDE.md` loads by itself, and the prompt points the session to the rest.

| Prompt | When |
| --- | --- |
| [01-design.md](01-design.md) | Once, first. Writes `docs/design.md` and the step contracts in `docs/build/`. Plan mode suits it, because it writes docs only. |
| [02-step.md](02-step.md) | Once per step from `docs/build/`, each in a new session, in the order `docs/status.md` gives. |
| [03-parity.md](03-parity.md) | When the CLI exists, before the release. Needs `QOBUZ_TOKEN` exported in the shell. |
| [04-release.md](04-release.md) | After parity passes. Tags `v0.1.0`. |
| [05-upstream-sync.md](05-upstream-sync.md) | Later, for each new qobuz-dl release worth porting. |

A session that runs out of context can be continued with prompt 02 in a new session: `docs/status.md` and the contract's As built section say where it stopped.

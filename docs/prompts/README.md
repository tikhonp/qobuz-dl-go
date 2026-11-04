# Session prompts

The prompt to paste into a Claude Code session started in this repo:

```sh
cd ~/projects/qobuz-dl-go && claude
```

Each session starts fresh. `CLAUDE.md` loads by itself, and the prompt points the session to the rest.

| Prompt | When |
| --- | --- |
| [05-upstream-sync.md](05-upstream-sync.md) | For each new qobuz-dl release worth porting. |

Prompts 01 to 04 ran the design, the build steps, the parity runs and the `v0.1.0` release. That work is done and the repo no longer has them; `git show 39d1736:docs/prompts/<file>` shows one.

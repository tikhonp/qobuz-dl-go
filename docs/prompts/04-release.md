# Prompt 4: release v0.1.0

```text
Release qobuz-dl-go v0.1.0.

Check first: docs/status.md shows every step done and the parity results recorded; CI is green on main; UPSTREAM.md says qobuz-dl 2.5.9 is ported in v0.1.0; README.md shows a short usage example that matches the real API. Fix what is missing, with one-line commits.

Then tag v0.1.0 on main, push the tag, and check that the module resolves:

GOPROXY=https://proxy.golang.org go list -m github.com/tikhonp/qobuz-dl-go@v0.1.0

Update docs/status.md here. In ~/projects/shelf, mark step 1c done in docs/status.md and fill in the As built section of docs/build/1c.md with the version and anything step 1d should know. Run /tikhon:unslop on the changed docs, commit in each repo with one-line messages, and push both.
```

# Prompt 5: port an upstream release

For a new qobuz-dl release. Put its tag on the last line.

```text
qobuz-dl has a new release. Port it into qobuz-dl-go.

Read CLAUDE.md, UPSTREAM.md and its Updating section, and docs/status.md. Run make upstream-diff TO=<tag> and go through the changes file by file. Port each change to a ported file into the Go file with the same base name. A change to something we don't port gets a line under Not ported in UPSTREAM.md, with the reason. Ask me before porting anything new and large.

Where new behavior needs golden data, extend testdata/gen/gen.py, install the new version in testdata/gen/.venv, and run make golden. Then run make check, make lint, and make parity on the reference albums listed in docs/status.md. If QOBUZ_TOKEN is not exported, skip parity and say so.

Update UPSTREAM.md with the tag, the commit, the function map and the deliberate differences, and update docs/status.md. Run /tikhon:unslop on the changed prose, commit with one-line messages, tag the next qobuz-dl-go version, and push. Tell me the version so Music Shelf can move to it.

Tag: <tag>
```

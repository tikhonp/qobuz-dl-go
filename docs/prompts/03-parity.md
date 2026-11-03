# Prompt 3: parity run

Compares the Go port with Python qobuz-dl 2.5.9 on real albums. Before starting the session, export the token in the shell, as `docs/reference.md` describes:

```sh
export QOBUZ_TOKEN=...   # the user auth token, never committed
```

```text
Run the parity check of qobuz-dl-go against Python qobuz-dl 2.5.9.

The Qobuz token is in QOBUZ_TOKEN in this shell. If it is missing, stop and ask me. Never print it, never write it to a file in the repo, never put it on a command line that gets logged, and keep it out of error messages.

Albums: pick a multi-disc hi-res album, a CD-quality album and a playlist, and show me the three links before downloading anything. If I list links on the next line, use those instead.

For each one, run make parity URL=<link>. Compare the file trees, file names, tags, covers, booklets, tracklists and m3u8 files. Every difference must either be listed under Deliberate differences in UPSTREAM.md or get fixed, with a test that pins the fix.

Record the links, the date and the results in docs/status.md as the reference albums for later parity runs. Run /tikhon:unslop on the changed docs, commit with one-line messages and push.
```

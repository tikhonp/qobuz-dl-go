# Prompt 1: design

The first session in this repo. It writes docs and commits them; it writes no library code.

```text
We're starting qobuz-dl-go, a Go port of qobuz-dl 2.5.9 that Music Shelf will import. This session designs it. Write no library code yet.

Read first: CLAUDE.md, docs/requirements.md, docs/design-questions.md, docs/reference.md, UPSTREAM.md, docs/port-spec.md and docs/status.md. Fetch the upstream source as docs/reference.md describes, and read bundle.py, qopy.py, core.py, downloader.py, metadata.py, utils.py, settings.py, constants.py, exceptions.py and db.py at v2.5.9. Then read what Music Shelf needs from the library: ~/projects/proj-x/docs/sources/qobuz.md, docs/provenance.md, docs/build/1c.md and docs/build/1d.md.

Then:

1. Check docs/port-spec.md against the 2.5.9 source. Fix whatever is wrong, and add what the port needs but the file lacks.
2. Go through docs/design-questions.md. Where the source settles a question, decide it and note why. Where it is a judgment call, ask me with AskUserQuestion, a few questions at a time, with your recommendation first. You may write throwaway spikes in the scratchpad to try a library or check a Python behavior, but commit none of them.
3. Write docs/design.md: the package layout; every exported type and function with its Go signature; the error model; Progress and Result; the settings and their defaults; the formatter and the Python-compatibility helpers; the transfer engine; tagging; the segmented path; downloadsdb; qobuzdltest; the CLI; golden generation; testing; CI.
4. Split the work into steps in docs/build/, one file per step, named like 01-golden.md. Each has Read first, Done when, Decisions, Files, Go signatures, a checklist that maps every edge case to a test name, and an empty As built section. The first step builds the golden generator; the last runs parity and tags v0.1.0. Keep each step small enough for one session.
5. Mark each question in docs/design-questions.md as decided, with a link to its section in docs/design.md.
6. Update docs/status.md with the step list, UPSTREAM.md where the function map changed, and the Layout and Commands sections of CLAUDE.md.
7. Run /tikhon:unslop on every doc you wrote or changed. Commit with one-line messages and push.

Finish by telling me which step to start next and anything that needs me first.
```

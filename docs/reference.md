# Reference

Where the sources are, which tools exist on this machine, and the conventions to follow.

## Upstream source

- Repository: https://github.com/Sei969/qobuz-dl, tag `v2.5.9`, commit `e21f92a`.
- Fetch it into the ignored `.upstream/` folder:

  ```sh
  git clone --depth 1 --branch v2.5.9 https://github.com/Sei969/qobuz-dl.git .upstream/qobuz-dl
  ```

- The PyPI build is `qobuz-dl-ultimate==2.5.9`. The golden venv installs it, so the goldens come from the same code production runs.
- `~/projects/music-download-bot/env` holds qobuz-dl 2.5.5, not 2.5.9. Don't use it as the reference.
- `main` upstream is ahead of the last tag, with 2.6.x lyrics work. Port tags only.

## Python for golden data

- Use Python 3.14, the version the production bot runs: `/opt/homebrew/opt/python@3.14/bin/python3.14`. The default `python3` here is 3.15.
- The venv lives in `testdata/gen/.venv`, ignored by git:

  ```sh
  /opt/homebrew/opt/python@3.14/bin/python3.14 -m venv testdata/gen/.venv
  testdata/gen/.venv/bin/pip install qobuz-dl-ultimate==2.5.9
  testdata/gen/.venv/bin/python -I testdata/gen/gen.py
  ```

- `-I` keeps the current folder and user site-packages out of Python's import path.
- `gen.py` may call private functions such as `qobuz_dl.utils.clean_filename` or `qobuz_dl.metadata._get_tags_to_add` directly with fixture data. It must not touch the network.

## Tools on this machine

| Tool | State |
| --- | --- |
| Go | 1.27.3 |
| ffmpeg, ffprobe | 9.0.2 |
| Docker | 29.9.2, used by `make lint` |
| golangci-lint | not installed on the host; run it in Docker or in CI |
| metaflac | not installed; `brew install flac`, or read tags with `ffprobe -show_format` |
| gh | not installed |

## Qobuz token for real runs

Unit tests never need a token. The parity run and manual CLI runs do.

- The production token is `QOBUZ_PASSWORD` in `~/projects/sh-blackberry/secrets/blackberry/music-download.env`. qobuz-dl treats it as the user auth token because it is longer than 60 characters.
- The user exports it as `QOBUZ_TOKEN` before a session that needs it. Never print it, never write it into the repo, never pass it as a command-line argument that ends up in logs, and keep it out of error messages.
- Production reaches Qobuz without a proxy. If the API is unreachable from this machine, ask the user.

## Conventions

These come from the user's other Go projects, mainly `~/projects/proxier` and `~/projects/vk2tg`:

- Standard `testing` only, no testify. Test names are CamelCase sentences, such as `TestResumeAfter200StartsOver`. Fakes and `httptest` servers instead of mocks. `testing/synctest` for time-based code. No sleeps over 100 ms.
- `go test -race`.
- Errors wrapped with `%w`, sentinel errors for conditions callers check, `errors.Join` for several at once.
- slog for logs.
- golangci-lint v2.15.0 with its default linters and no config file, plus govulncheck v1.9.0.
- For the CI job shape, read `~/projects/proxier/.forgejo/workflows/ci.yaml` and rewrite it for GitHub Actions: `actions/checkout`, `actions/setup-go` with `go-version-file: go.mod`, `golangci/golangci-lint-action` pinned to v2.15.0, then govulncheck.

## Python behaviors that trip a port

Check each against the source and the goldens:

- `\w` in Python 3 matches Unicode letters, digits and the underscore. Go's RE2 `\w` is ASCII only, so a direct copy breaks non-Latin titles.
- `str.format` with format specs, and the `PartialFormatter` behavior for missing keys.
- `str()` of a float keeps `.0`, so `96.0` prints as `96.0`. `str(None)` is `None`, `str(True)` is `True`.
- `str.casefold` is not `strings.ToLower`.
- NFC normalization, from `golang.org/x/text/unicode/norm`.
- Dict order is insertion order, which decides tag order and m3u8 order.
- `sorted()` is stable.
- Truthiness: `""`, `0`, `[]`, `{}` and `None` are all false, which `.get(...) or default` chains rely on.
- `dict.get(key, default)` returns the default only when the key is missing, not when its value is `None`.
- RE2 has no lookaround and no backreferences.

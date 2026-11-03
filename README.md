# qobuz-dl-go

A Go port of [qobuz-dl](https://github.com/Sei969/qobuz-dl), the Qobuz downloader by Sei969, which builds on [vitiko98/qobuz-dl](https://github.com/vitiko98/qobuz-dl). It ports version 2.5.9 file by file, so later upstream releases can be ported as diffs. [UPSTREAM.md](UPSTREAM.md) tracks what is ported and where the Go code differs on purpose.

Status: in development, see [docs/status.md](docs/status.md). The first user is [Music Shelf](https://github.com/tikhonp/shelf).

What it does:

- Gets the app ID and secrets from the Qobuz web player, and signs in with a user auth token.
- Searches the catalog and reads albums, tracks, playlists, artists and labels.
- Downloads albums, tracks and playlists in FLAC or MP3 with quality fallback, writes the same folder names, file names and tags as qobuz-dl, and saves covers, booklets, tracklists and playlist m3u8 files.

```go
import "github.com/tikhonp/qobuz-dl-go"
```

The API is described in [docs/port-spec.md](docs/port-spec.md) until the package exists.

Licence: GPL-3.0, like qobuz-dl.

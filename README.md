# qobuz-dl-go

A Go port of [qobuz-dl](https://github.com/Sei969/qobuz-dl), the Qobuz downloader by Sei969, which builds on [vitiko98/qobuz-dl](https://github.com/vitiko98/qobuz-dl). It ports version 2.5.9 file by file, so later upstream releases can be ported as diffs. [UPSTREAM.md](UPSTREAM.md) tracks what is ported and where the Go code differs on purpose.

Status: `v0.1.0`, see [docs/status.md](docs/status.md). The first user is [Music Shelf](https://github.com/tikhonp/shelf).

What it does:

- Gets the app ID and secrets from the Qobuz web player, and signs in with a user auth token.
- Searches the catalog and reads albums, tracks, playlists, artists and labels.
- Downloads albums, tracks and playlists in FLAC or MP3 with quality fallback, writes the same folder names, file names and tags as qobuz-dl, and saves covers, booklets, tracklists and playlist m3u8 files.

## Library

```sh
go get github.com/tikhonp/qobuz-dl-go
```

```go
import (
	qobuzdl "github.com/tikhonp/qobuz-dl-go"
	"github.com/tikhonp/qobuz-dl-go/downloadsdb"
)

opts := qobuzdl.DefaultOptions() // QobuzDL's defaults; the zero Options is not
opts.Directory = "/music"
opts.Quality = 27
opts.HTTPClient = &http.Client{} // no Timeout: the library times each call itself
opts.Settings.UserAuthToken = token
opts.Logger = slog.Default()

db, err := downloadsdb.Open(ctx, "qobuz_dl.db") // optional; Python's table
if err != nil { ... }
defer db.Close()
opts.DownloadsDB = db

d, err := qobuzdl.New(opts)
if err != nil { ... }
if err := d.GetTokens(ctx); err != nil { ... } // app ID and secrets from the web player
if err := d.InitializeClient(ctx, "", "", d.AppID(), d.Secrets()); err != nil { ... }

res, err := d.HandleURL(ctx, "https://play.qobuz.com/album/0634904080853", nil)
for _, t := range res.Tracks() {
	if t.Outcome == qobuzdl.OutcomeFailed {
		fmt.Println("failed:", t.Title, t.Err)
		continue
	}
	fmt.Println(t.Path, t.BitDepth, t.SamplingRate)
}
```

`HandleURL` takes album, track, playlist, artist and label URLs; `HandleID` takes a kind and an ID; `DownloadFromID` downloads one album or track. Pass a `Progress` instead of nil to follow releases, tracks and bytes. `Client` has typed reads and searches of the catalog. The package prints nothing and never exits.

For tests, `qobuzdltest` is a fake Qobuz with recorded answers, a CDN and switches that make it fail the ways Qobuz does:

```go
srv := qobuzdltest.New(t)
opts := qobuzdl.DefaultOptions()
srv.Configure(&opts)
```

[docs/design.md](docs/design.md) describes the API and the reasons behind it.

## CLI

```sh
go install github.com/tikhonp/qobuz-dl-go/cmd/qobuz-dl@latest
export QOBUZ_TOKEN=...   # the user auth token from the web player

qobuz-dl dl -q 27 https://play.qobuz.com/album/0634904080853
qobuz-dl dl -d ~/Music --no-db -ff "{artist}/{album} ({year})" URL...
qobuz-dl search albums miles davis kind of blue
qobuz-dl search -l 5 playlists jazz
```

`dl` takes qobuz-dl's `dl` flags for the ported settings, such as `-q`, `-d`, `-ff`, `-tf`, `--no-fallback`, `--albums-only`, `--no-m3u`, `-s`, `-e` and the `--no-<name>-tag` switches, with one dash or two; `qobuz-dl dl -h` lists them. Defaults are those of qobuz-dl's `QobuzDL` class, not of its `config.ini`. The downloads database is qobuz-dl's own, `~/.config/qobuz-dl/qobuz_dl.db`, so both skip what either downloaded; `--db` picks another file and `--no-db` none. Flags for features not ported, such as `--blacklist` or `--booklet-only`, are refused.

The token is read only from `QOBUZ_TOKEN` and never printed. Progress goes to stdout, one line per release, track and log line; `-v` adds debug logs on stderr. The exit status is 0 when every release is done or skipped, 1 otherwise, and 2 for a usage error.

Licence: GPL-3.0, like qobuz-dl.

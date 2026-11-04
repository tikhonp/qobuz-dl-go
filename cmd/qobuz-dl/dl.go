// The dl command. It ports the dl and shared flags of commands.py and
// download_list_of_urls of core.py, qobuz-dl 2.5.9, for Qobuz URLs only.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
	"github.com/tikhonp/qobuz-dl-go/downloadsdb"
)

// dlConfig is what the dl command line asks for.
type dlConfig struct {
	opts    qobuzdl.Options
	dbPath  string // "" for --no-db
	verbose bool
	urls    []string
}

// artSizes are argparse's choices for --embedded-art-size and
// --saved-art-size.
var artSizes = []string{"50", "100", "150", "300", "600", "max", "org"}

// tagFlags are the --no-<name>-tag switches, the 19 of commands.py and the
// three that only config.ini sets upstream.
func tagFlags(s *qobuzdl.Settings) []struct {
	name string
	p    *bool
} {
	return []struct {
		name string
		p    *bool
	}{
		{"no-album-artist-tag", &s.NoAlbumArtistTag},
		{"no-album-title-tag", &s.NoAlbumTitleTag},
		{"no-track-artist-tag", &s.NoTrackArtistTag},
		{"no-track-title-tag", &s.NoTrackTitleTag},
		{"no-release-date-tag", &s.NoReleaseDateTag},
		{"no-media-type-tag", &s.NoMediaTypeTag},
		{"no-genre-tag", &s.NoGenreTag},
		{"no-replaygain-tag", &s.NoReplayGainTag},
		{"no-album-url-tag", &s.NoAlbumURLTag},
		{"no-track-number-tag", &s.NoTrackNumberTag},
		{"no-track-total-tag", &s.NoTrackTotalTag},
		{"no-disc-number-tag", &s.NoDiscNumberTag},
		{"no-disc-total-tag", &s.NoDiscTotalTag},
		{"no-composer-tag", &s.NoComposerTag},
		{"no-explicit-tag", &s.NoExplicitTag},
		{"no-copyright-tag", &s.NoCopyrightTag},
		{"no-label-tag", &s.NoLabelTag},
		{"no-upc-tag", &s.NoUPCTag},
		{"no-isrc-tag", &s.NoISRCTag},
		{"no-conductor-tag", &s.NoConductorTag},
		{"no-ensemble-tag", &s.NoEnsembleTag},
		{"no-work-tag", &s.NoWorkTag},
	}
}

// defaultDBPath is cli.py's QOBUZ_DB, so this command and Python's share
// one downloads database.
func defaultDBPath(getenv func(string) string) string {
	dir := getenv("CONFIG_DIR")
	if dir == "" {
		if runtime.GOOS == "windows" {
			dir = getenv("APPDATA")
		} else {
			dir = filepath.Join(getenv("HOME"), ".config")
		}
	}
	return filepath.Join(dir, "qobuz-dl", "qobuz_dl.db")
}

// parseDL reads the dl command line. Defaults are QobuzDL's and
// QobuzDLSettings', not those of Python's config.ini.
func parseDL(args []string, getenv func(string) string, out io.Writer) (*dlConfig, error) {
	def := qobuzdl.DefaultOptions()
	o := qobuzdl.DefaultOptions()
	s := &o.Settings
	cfg := &dlConfig{}

	fs := flag.NewFlagSet("dl", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		_, _ = fmt.Fprint(out, "usage: qobuz-dl dl [flags] URL...\n\n"+
			"Downloads Qobuz album, track, playlist, artist and label URLs.\n"+
			"Flags take one or two dashes; Python's short names work too.\n\n")
		fs.PrintDefaults()
	}
	str := func(p *string, help string, names ...string) {
		for _, n := range names {
			fs.StringVar(p, n, *p, help)
		}
	}
	boolean := func(p *bool, help string, names ...string) {
		for _, n := range names {
			fs.BoolVar(p, n, *p, help)
		}
	}

	str(&o.Directory, "directory for downloads", "d", "directory")
	fs.IntVar(&o.Quality, "q", o.Quality, "audio quality: 5, 6, 7 or 27")
	fs.IntVar(&o.Quality, "quality", o.Quality, "audio quality: 5, 6, 7 or 27")
	boolean(&o.IgnoreSinglesEPs, "don't download singles, EPs and VA releases", "albums-only")
	boolean(&o.NoM3UForPlaylists, "don't create .m3u files when downloading playlists", "no-m3u")
	var noFallback, noDB bool
	boolean(&noFallback, "disable quality fallback (skip releases not available in set quality)", "no-fallback")
	cfg.dbPath = defaultDBPath(getenv)
	str(&cfg.dbPath, "the downloads database", "db")
	boolean(&noDB, "don't call the database", "no-db")
	str(&o.FolderFormat, "pattern for formatting folder names", "ff", "folder-format")
	str(&s.FallbackFolderFormat, "fallback pattern for folder names when the main pattern fails", "fbff", "fallback-folder-format")
	str(&o.TrackFormat, "pattern for formatting track names", "tf", "track-format")
	boolean(&o.SmartDiscography, "filter spam-like albums out of an artist's discography", "s", "smart-discography")
	var delay int
	fs.IntVar(&delay, "delay", 0, "seconds to wait between track downloads; above 0 also downloads one track at a time")
	boolean(&s.MultiValueTags, "split comma-separated metadata into multiple tag fields", "multi-tags")
	var nativeLang, withCredits bool
	boolean(&nativeLang, "do not force English; metadata in the account's language", "native-lang")
	boolean(&o.NoCredits, "don't write the Digital Booklet.txt", "no-credits")
	boolean(&withCredits, "write the Digital Booklet.txt even with --no-credits", "with-credits")
	for _, t := range tagFlags(s) {
		boolean(t.p, "don't add the "+strings.TrimSuffix(strings.TrimPrefix(t.name, "no-"), "-tag")+" tag", t.name)
	}
	var embedArt bool
	boolean(&embedArt, "embed cover art into audio files", "e", "embed-art")
	boolean(&s.NoCover, "don't download cover art", "no-cover")
	str(&s.EmbeddedArtSize, "size of embedded artwork: "+strings.Join(artSizes, ", "), "embedded-art-size")
	str(&s.SavedArtSize, "size of saved artwork: "+strings.Join(artSizes, ", "), "saved-art-size")
	str(&s.MultipleDiscPrefix, "folder prefix for each disc of a multi-disc album", "multiple-disc-prefix")
	boolean(&s.MultipleDiscOneDir, "store multiple disc releases in one directory", "multiple-disc-one-dir")
	str(&s.MultipleDiscTrackFormat, "track format for multiple disc releases", "multiple-disc-track-format")
	var maxWorkers int
	fs.IntVar(&maxWorkers, "max-workers", 0, "maximum number of parallel downloads (default 3)")
	boolean(&s.LegacyCharmap, "replace characters file systems refuse with ASCII, not fullwidth ones", "legacy-charmap")
	boolean(&s.DisableSmartTruncation, "cut long names without keeping their tags in brackets", "disable-smart-truncation")
	boolean(&cfg.verbose, "debug logs on stderr", "v")

	// These Python flags change nothing here. Lyrics are not ported and
	// never fetched, and nothing reads og_cover.
	var ignored bool
	for _, n := range []string{"og-cover", "no-lyrics", "no-lrc-files", "no-embed-lyrics"} {
		boolean(&ignored, "accepted for Python's sake; does nothing", n)
	}
	// Python flags whose features are not ported; using one is an error.
	var blacklist, filterLabel string
	var playlistAsAlbums, bookletOnly bool
	str(&blacklist, "not ported", "b", "blacklist")
	str(&filterLabel, "not ported", "filter-label")
	boolean(&playlistAsAlbums, "not ported", "playlist-as-albums")
	boolean(&bookletOnly, "not ported", "booklet-only")

	urls, err := parseInterleaved(fs, args)
	if err != nil {
		return nil, err
	}
	notPorted := map[string]bool{"b": true, "blacklist": true, "filter-label": true, "playlist-as-albums": true, "booklet-only": true}
	var bad []string
	fs.Visit(func(f *flag.Flag) {
		if notPorted[f.Name] {
			bad = append(bad, "--"+f.Name)
		}
	})
	if len(bad) > 0 {
		return nil, usagef("%s: not ported, see UPSTREAM.md", strings.Join(bad, ", "))
	}
	if len(urls) == 0 {
		return nil, usagef("dl needs at least one URL")
	}
	if !slices.Contains([]int{5, 6, 7, 27}, o.Quality) {
		return nil, usagef("-q/--quality: invalid choice: %d (choose from 5, 6, 7, 27)", o.Quality)
	}
	if maxWorkers < 0 {
		return nil, usagef("--max-workers: %d is below 1", maxWorkers)
	}

	// argparse leaves an empty value to "or" in cli.py and settings.py,
	// which then take the default.
	for _, p := range []struct {
		got *string
		def string
	}{
		{&o.Directory, def.Directory},
		{&o.FolderFormat, def.FolderFormat},
		{&o.TrackFormat, def.TrackFormat},
		{&s.FallbackFolderFormat, def.Settings.FallbackFolderFormat},
		{&s.MultipleDiscPrefix, def.Settings.MultipleDiscPrefix},
		{&s.MultipleDiscTrackFormat, def.Settings.MultipleDiscTrackFormat},
	} {
		if *p.got == "" {
			*p.got = p.def
		}
	}
	for _, p := range []struct {
		name string
		v    string
	}{{"--embedded-art-size", s.EmbeddedArtSize}, {"--saved-art-size", s.SavedArtSize}} {
		if !slices.Contains(artSizes, p.v) {
			return nil, usagef("%s: invalid choice: %q (choose from %s)", p.name, p.v, strings.Join(artSizes, ", "))
		}
	}

	o.QualityFallback = !noFallback
	o.ForceEnglish = !nativeLang
	o.NoCredits = o.NoCredits && !withCredits
	// cli.py hands -e to QobuzDL and to the settings. The settings fetch
	// embed_cover.jpg and QobuzDL embeds it.
	o.EmbedArt = embedArt
	s.EmbedArt = embedArt
	s.Delay = time.Duration(max(0, delay)) * time.Second
	if maxWorkers > 0 {
		s.MaxWorkers = maxWorkers
	}
	if noDB {
		cfg.dbPath = ""
	}
	cfg.opts = o
	cfg.urls = urls
	return cfg, nil
}

// dl ports download_list_of_urls for Qobuz URLs, which sends each URL
// through handle_url in turn.
func (c *cli) dl(ctx context.Context, args []string) int {
	cfg, err := parseDL(args, c.getenv, c.stderr)
	if err != nil {
		return c.exitCode(err)
	}
	token, err := c.token()
	if err != nil {
		return c.exitCode(err)
	}
	opts := cfg.opts
	opts.Logger = c.logger(cfg.verbose)
	if cfg.dbPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.dbPath), 0o755); err != nil {
			return c.exitCode(err)
		}
		db, err := downloadsdb.Open(ctx, cfg.dbPath)
		if err != nil {
			return c.exitCode(err)
		}
		defer func() { _ = db.Close() }()
		opts.DownloadsDB = db
	}
	d, err := c.open(ctx, opts, token)
	if err != nil {
		return c.exitCode(err)
	}
	p := newPrinter(c.stdout, absDir(opts.Directory))

	code := exitOK
	for _, url := range cfg.urls {
		// download_list_of_urls reads Qobuz's newer share links as the web
		// player's.
		url = strings.ReplaceAll(url, "open.qobuz.com", "play.qobuz.com")
		res, err := d.HandleURL(ctx, url, p)
		if !resultOK(res, err) {
			code = exitFailed
		}
		switch {
		case err == nil:
		case ctx.Err() != nil:
			_, _ = fmt.Fprintln(c.stderr, "qobuz-dl: interrupted")
			return exitFailed
		case errors.Is(err, qobuzdl.ErrInvalidURL), errors.Is(err, qobuzdl.ErrEmptyContent):
			// handle_url logs these and goes on to the next URL.
		default:
			// Python's CLI dies on whatever handle_url doesn't catch.
			_, _ = fmt.Fprintf(c.stderr, "qobuz-dl: %s: %v\n", url, err)
			return exitFailed
		}
	}
	return code
}

// absDir is the folder New makes of dir, for the printer's relative paths.
func absDir(dir string) string {
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home + dir[1:]
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// resultOK is true when every release is done or skipped.
func resultOK(res *qobuzdl.Result, err error) bool {
	if err != nil || res == nil {
		return false
	}
	for _, r := range res.Releases {
		if r.Status != qobuzdl.StatusDone && r.Status != qobuzdl.StatusSkipped {
			return false
		}
	}
	return true
}

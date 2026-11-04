// The search command. qobuz-dl 2.5.9 has none; it prints core.py's
// search_by_type lines, which its interactive mode shows in a menu.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
)

// searchTypes maps the command's type words to search_by_type's item types.
var searchTypes = map[string]string{
	"albums": "album", "tracks": "track", "artists": "artist", "playlists": "playlist",
	"album": "album", "track": "track", "artist": "artist", "playlist": "playlist",
}

// search prints search_by_type's lines, each followed by the item's URL.
func (c *cli) search(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprint(c.stderr, "usage: qobuz-dl search [-l limit] albums|tracks|artists|playlists QUERY...\n\n")
		fs.PrintDefaults()
	}
	// search_by_type's default limit; the interactive mode's 20 is for its menu.
	limit := 10
	fs.IntVar(&limit, "l", limit, "limit of search results")
	fs.IntVar(&limit, "limit", limit, "limit of search results")
	var verbose bool
	fs.BoolVar(&verbose, "v", false, "debug logs on stderr")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return c.exitCode(err)
	}
	if len(pos) < 2 {
		return c.exitCode(usagef("search needs a type and a query"))
	}
	itemType, ok := searchTypes[pos[0]]
	if !ok {
		return c.exitCode(usagef("unknown search type %q: use albums, tracks, artists or playlists", pos[0]))
	}
	if limit < 1 {
		return c.exitCode(usagef("-l/--limit: %d is below 1", limit))
	}
	query := strings.Join(pos[1:], " ")
	token, err := c.token()
	if err != nil {
		return c.exitCode(err)
	}

	opts := qobuzdl.DefaultOptions()
	// New creates the download folder; a search downloads nothing, so it
	// gets the current one, which exists.
	opts.Directory = "."
	opts.Logger = c.logger(verbose)
	d, err := c.open(ctx, opts, token)
	if err != nil {
		return c.exitCode(err)
	}
	results, err := d.SearchByType(ctx, query, itemType, limit)
	if errors.Is(err, qobuzdl.ErrQueryTooShort) {
		return c.exitCode(usagef("the query %q is shorter than 3 characters", query))
	}
	if err != nil {
		return c.exitCode(err)
	}
	for _, r := range results {
		_, _ = fmt.Fprintf(c.stdout, "%s   %s\n", r.Text, r.URL)
	}
	return exitOK
}

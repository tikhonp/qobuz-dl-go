// Command qobuz-dl downloads albums, tracks, playlists, artists and labels
// from Qobuz with qobuz-dl-go, and searches the catalog. It is for parity
// runs against Python qobuz-dl and for manual downloads.
//
// It ports no Python file whole. The cli.py and commands.py of qobuz-dl
// 2.5.9 read a config.ini and the keyring and offer modes this port lacks.
// This command keeps the dl flags whose settings are ported, with QobuzDL's
// defaults, and reads the user auth token only from QOBUZ_TOKEN.
//
// Usage:
//
//	qobuz-dl dl [flags] URL...
//	qobuz-dl search [-l limit] albums|tracks|artists|playlists QUERY...
//
// Exit status: 0 when every release is done or skipped, 1 otherwise, 2 for
// usage errors.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
)

const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

const tokenEnv = "QOBUZ_TOKEN"

const usage = `usage:
  qobuz-dl dl [flags] URL...
  qobuz-dl search [-l limit] albums|tracks|artists|playlists QUERY...

The user auth token comes from the QOBUZ_TOKEN environment variable.
Run "qobuz-dl dl -h" or "qobuz-dl search -h" for the flags.
`

// cli is one run of the command. Tests build it with their own streams and
// environment.
type cli struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	// configure changes the options after the flags are read; tests point
	// them at qobuzdltest with it.
	configure func(*qobuzdl.Options)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	c := &cli{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv}
	code := c.run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

func (c *cli) run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(c.stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "dl":
		return c.dl(ctx, args[1:])
	case "search":
		return c.search(ctx, args[1:])
	case "-h", "-help", "--help", "help":
		_, _ = fmt.Fprint(c.stdout, usage)
		return exitOK
	}
	_, _ = fmt.Fprintf(c.stderr, "qobuz-dl: unknown command %q\n%s", args[0], usage)
	return exitUsage
}

// usageError is a mistake in the command line, exit status 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error {
	return &usageError{msg: fmt.Sprintf(format, a...)}
}

// exitCode reports err on stderr and picks the status for it. flag has
// already printed its own errors and the help text.
func (c *cli) exitCode(err error) int {
	var ue *usageError
	switch {
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case errors.As(err, &ue):
		_, _ = fmt.Fprintf(c.stderr, "qobuz-dl: %s\n", ue.msg)
		return exitUsage
	case errors.Is(err, errFlags):
		return exitUsage
	}
	_, _ = fmt.Fprintf(c.stderr, "qobuz-dl: %v\n", err)
	return exitFailed
}

// errFlags marks an error the flag package has already printed.
var errFlags = errors.New("bad flags")

// parseInterleaved parses flags that come before, between and after the
// positional arguments, as argparse does; Go's flag package stops at the
// first positional one. Everything after "--" is positional.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %w", errFlags, err)
		}
		rest := fs.Args()
		consumed := len(args) - len(rest)
		if consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rest...), nil
		}
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// token reads QOBUZ_TOKEN. Its value never reaches an error or a log line.
func (c *cli) token() (string, error) {
	t := c.getenv(tokenEnv)
	if t == "" {
		return "", usagef("%s is not set; export the Qobuz user auth token in it", tokenEnv)
	}
	return t, nil
}

// logger is the -v logger, which writes debug lines to stderr. Without -v the lines
// Python shows still reach stdout through the printer.
func (c *cli) logger(verbose bool) *slog.Logger {
	if !verbose {
		return nil
	}
	return slog.New(slog.NewTextHandler(c.stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// open runs what cli.py and the bot run before a download: QobuzDL's
// constructor, get_tokens and initialize_client. The token goes in as
// Settings.UserAuthToken, which initialize_client prefers to the password.
func (c *cli) open(ctx context.Context, opts qobuzdl.Options, token string) (*qobuzdl.DL, error) {
	opts.Settings.UserAuthToken = token
	// The library sets a timeout per call; a client-wide one would cut long
	// transfers.
	opts.HTTPClient = &http.Client{}
	if c.configure != nil {
		c.configure(&opts)
	}
	d, err := qobuzdl.New(opts)
	if err != nil {
		return nil, err
	}
	if err := d.GetTokens(ctx); err != nil {
		return nil, fmt.Errorf("getting the app ID and secrets: %w", err)
	}
	if err := d.InitializeClient(ctx, "", "", d.AppID(), d.Secrets()); err != nil {
		return nil, fmt.Errorf("signing in: %w", err)
	}
	return d, nil
}

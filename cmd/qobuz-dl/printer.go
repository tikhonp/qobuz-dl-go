// The progress printer. It ports no Python file, since upstream shows tqdm
// bars and colored lines.

package main

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"

	qobuzdl "github.com/tikhonp/qobuz-dl-go"
)

// printer is the Progress that writes stdout, a line per release start, per
// finished track and per line Python shows. It draws no bars, so the output
// of two runs can be diffed.
type printer struct {
	mu   sync.Mutex
	w    io.Writer
	base string // paths print relative to it
}

func newPrinter(w io.Writer, base string) *printer {
	return &printer{w: w, base: base}
}

func (p *printer) line(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintln(p.w, s)
}

func (p *printer) rel(path string) string {
	if r, err := filepath.Rel(p.base, path); err == nil {
		return r
	}
	return path
}

func (p *printer) ReleaseStart(r qobuzdl.ReleaseInfo) {
	p.line(fmt.Sprintf("== %s %s: %s, %d tracks, in %s", r.Kind, r.ID, r.Title, len(r.Tracks), p.rel(r.Folder)))
}

func (p *printer) TrackStart(qobuzdl.TrackInfo) {}

func (p *printer) TrackBytes(int, int64, int64) {}

func (p *printer) TrackEnd(t qobuzdl.TrackResult) {
	switch t.Outcome {
	case qobuzdl.OutcomeDone, qobuzdl.OutcomeSkipped:
		verb := "done"
		if t.Outcome == qobuzdl.OutcomeSkipped {
			verb = "exists"
		}
		p.line(fmt.Sprintf("  [%d] %s: %s (%s)", t.Position, verb, p.rel(t.Path), trackFormat(t)))
	default:
		p.line(fmt.Sprintf("  [%d] failed: %s: %v", t.Position, t.Title, t.Err))
	}
}

// trackFormat is the delivered format as Qobuz names it, such as
// "FLAC 24-bit 96kHz".
func trackFormat(t qobuzdl.TrackResult) string {
	if t.Codec == "MP3" || t.BitDepth == 0 {
		return t.Codec
	}
	return fmt.Sprintf("%s %d-bit %skHz", t.Codec, t.BitDepth, strconv.FormatFloat(t.SamplingRate, 'f', -1, 64))
}

// Log prints the line as Python's logging does with "%(message)s", so a
// message ending in a newline leaves a blank line, as upstream's do.
func (p *printer) Log(_ slog.Level, msg string) {
	p.line(msg)
}

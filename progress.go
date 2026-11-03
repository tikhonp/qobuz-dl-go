// Progress and results of a download. They port no Python file: upstream
// shows progress with tqdm bars and colored prints, and returns nothing.

package qobuzdl

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Progress receives a download's events. Calls come from several goroutines,
// so implementations must be safe for concurrent use.
type Progress interface {
	// ReleaseStart fires once per album or single track, after its folder
	// exists and before the extras.
	ReleaseStart(r ReleaseInfo)
	// TrackStart fires when a transfer starts, once per tier tried.
	TrackStart(t TrackInfo)
	// TrackBytes fires at most every 250 ms per track and once more when
	// the transfer ends. total is 0 when the server didn't say.
	TrackBytes(position int, done, total int64)
	// TrackEnd fires once per planned track, also for tracks that fail
	// before a transfer.
	TrackEnd(t TrackResult)
	// Log gets the lines Python prints or logs at info level and above.
	Log(level slog.Level, msg string)
}

// ReleaseInfo describes a release about to download.
type ReleaseInfo struct {
	Kind     string // album, track or playlist
	ID       string
	Title    string
	Folder   string // the work folder: [IN PROGRESS] <name> for albums
	Album    *Album // nil for a playlist
	Playlist *Playlist
	Tracks   []PlannedTrack
}

// PlannedTrack is a track a release will try.
type PlannedTrack struct {
	Position                int // 1-based: the index in tracks.items, or the playlist position
	ID, Title, ISRC         string
	DiscNumber, TrackNumber int
	Duration                int
}

// TrackInfo describes one transfer.
type TrackInfo struct {
	Position  int
	ID, Title string
	FormatID  int // the tier being transferred
	Segmented bool
}

// Result is what a download call did.
type Result struct {
	Kind     string // album, track, playlist, artist or label
	ID       string
	Folder   string    // the album or track folder, or the playlist, artist or label folder
	Releases []Release // one for an album or track; one per item for collections
	M3U      string    // the playlist file, when written
}

// Tracks returns every release's tracks, in order.
func (r *Result) Tracks() []TrackResult {
	var out []TrackResult
	for _, rel := range r.Releases {
		out = append(out, rel.Tracks...)
	}
	return out
}

// Release is one album or single track.
type Release struct {
	Kind         string // album or track
	ID           string
	Status       Status
	Skip         SkipReason
	Folder       string // final: <name> or [INCOMPLETE] <name>
	Album        *Album // the album/get answer, or the album inside track/get
	Track        *Track // single tracks
	Format       string // the gate's format: FLAC, MP3 or Unknown
	QualityMet   bool
	BitDepth     int // the gate's values; 0 when None
	SamplingRate float64
	Tracks       []TrackResult
	Err          error // why it failed or stopped
}

// Status is how a release ended.
type Status int

const (
	StatusDone    Status = iota + 1 // every track done or already there
	StatusPartial                   // some tracks failed
	StatusFailed                    // the release could not start, or every track failed
	StatusSkipped                   // see Skip
)

// SkipReason is why a release was skipped.
type SkipReason int

const (
	SkipNone       SkipReason = iota
	SkipInDatabase            // already in the downloads database
	SkipQuality               // below the requested quality without fallback
	SkipAlbumsOnly            // a single, EP or various-artists release with IgnoreSinglesEPs
	SkipDemo                  // a single track whose getFileUrl answer was empty
)

// TrackResult is how one track ended.
type TrackResult struct {
	Position                int
	ID, Title, ISRC         string
	DiscNumber, TrackNumber int
	Path                    string // the final file, in the final folder; "" when failed
	FormatID                int    // delivered
	Codec                   string // FLAC or MP3
	BitDepth                int    // delivered, from the tier's answer
	SamplingRate            float64
	Outcome                 Outcome
	Err                     error
}

// Outcome is how a track ended.
type Outcome int

const (
	OutcomeDone    Outcome = iota + 1
	OutcomeSkipped         // the file existed
	OutcomeFailed
)

// noProgress stands in for a nil Progress.
type noProgress struct{}

func (noProgress) ReleaseStart(ReleaseInfo)     {}
func (noProgress) TrackStart(TrackInfo)         {}
func (noProgress) TrackBytes(int, int64, int64) {}
func (noProgress) TrackEnd(TrackResult)         {}
func (noProgress) Log(slog.Level, string)       {}

func orNoProgress(p Progress) Progress {
	if p == nil {
		return noProgress{}
	}
	return p
}

// bytesInterval is the shortest gap between two TrackBytes calls of a track.
const bytesInterval = 250 * time.Millisecond

// byteMeter throttles one transfer's TrackBytes calls.
type byteMeter struct {
	p        Progress
	position int

	mu          sync.Mutex
	last        time.Time
	done, total int64
}

func newByteMeter(p Progress, position int) *byteMeter {
	return &byteMeter{p: p, position: position, last: time.Now()}
}

// update notes the progress and reports it when 250 ms have passed since
// the last report or the start.
func (m *byteMeter) update(done, total int64) {
	m.mu.Lock()
	m.done, m.total = done, total
	now := time.Now()
	report := now.Sub(m.last) >= bytesInterval
	if report {
		m.last = now
	}
	m.mu.Unlock()
	if report {
		m.p.TrackBytes(m.position, done, total)
	}
}

// finish reports the last values once the transfer ends.
func (m *byteMeter) finish() {
	m.mu.Lock()
	done, total := m.done, m.total
	m.mu.Unlock()
	m.p.TrackBytes(m.position, done, total)
}

// progressHandler sends the records of info level and above to Progress.Log
// as well as to the caller's handler, so every line Python shows reaches
// both.
type progressHandler struct {
	slog.Handler
	p Progress
}

func (h progressHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo || h.Handler.Enabled(ctx, l)
}

func (h progressHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelInfo {
		h.p.Log(r.Level, r.Message)
	}
	if h.Handler.Enabled(ctx, r.Level) {
		return h.Handler.Handle(ctx, r)
	}
	return nil
}

func (h progressHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return progressHandler{Handler: h.Handler.WithAttrs(attrs), p: h.p}
}

func (h progressHandler) WithGroup(name string) slog.Handler {
	return progressHandler{Handler: h.Handler.WithGroup(name), p: h.p}
}

// Ports qobuz_dl/core.py from qobuz-dl 2.5.9: QobuzDL. Step 6 adds Options,
// which qobuzdltest configures; step 9 adds the rest.

package qobuzdl

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Options holds QobuzDL's arguments and the Go-only knobs. Its zero value is
// not upstream's default; start from DefaultOptions.
type Options struct {
	Directory         string // "QobuzDownloads"
	Quality           int    // 6
	EmbedArt          bool   // QobuzDL.embed_art: embed embed_cover.jpg when present
	IgnoreSinglesEPs  bool
	NoM3UForPlaylists bool
	QualityFallback   bool        // true
	DownloadsDB       DownloadsDB // nil: no database
	FolderFormat      string      // "{artist} - {album} ({year}) [{bit_depth}B-{sampling_rate}kHz]"
	TrackFormat       string      // "{track_number} - {track_title}"
	SmartDiscography  bool
	ForceEnglish      bool // true
	NoCredits         bool
	Settings          Settings

	HTTPClient  *http.Client // required; without a Timeout, which would cut long transfers
	Logger      *slog.Logger // nil discards
	FFmpegPath  string       // "ffmpeg"
	Timeouts    Timeouts
	TrackPause  time.Duration   // 1s, the sleep before each track
	RetryDelays []time.Duration // 2s, 4s, 8s, 16s
	APIBase     string          // "" for https://www.qobuz.com/api.json/0.2/
	PlayBase    string          // "" for https://play.qobuz.com
}

// DefaultOptions returns QobuzDL's defaults with DefaultSettings, and the
// Go-only knobs set as upstream behaves. HTTPClient stays nil.
func DefaultOptions() Options {
	return Options{
		Directory:       "QobuzDownloads",
		Quality:         6,
		QualityFallback: true,
		FolderFormat:    "{artist} - {album} ({year}) [{bit_depth}B-{sampling_rate}kHz]",
		TrackFormat:     "{track_number} - {track_title}",
		ForceEnglish:    true,
		Settings:        DefaultSettings(),
		FFmpegPath:      "ffmpeg",
		Timeouts:        DefaultTimeouts(),
		TrackPause:      time.Second,
		RetryDelays:     []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second},
	}
}

// DownloadsDB is the downloads database. The downloadsdb package implements
// it in SQLite with Python's table; callers may keep their own.
type DownloadsDB interface {
	// Has ports handle_download_id without add_id. Python matches ID and
	// quality only; the media type is there for implementations that key on
	// it.
	Has(ctx context.Context, mediaType, id string, quality int) (bool, error)
	// Add ports handle_download_id with add_id. A duplicate is not an error.
	Add(ctx context.Context, rec DownloadRecord) error
}

// DownloadRecord is one row of the downloads table.
type DownloadRecord struct {
	ID, MediaType          string
	Quality                int
	FileFormat             string
	QualityMet             bool
	BitDepth, SamplingRate *string // str() of Python's value; nil for None
	SavedPath, Status      string  // Status is "downloaded"
	URL, ReleaseDate       string
	Artist, Album          string
}

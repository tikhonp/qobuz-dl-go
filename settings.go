// Ports qobuz_dl/settings.py from qobuz-dl 2.5.9: QobuzDLSettings without the
// fields only the Python CLI, config.ini and the lyrics code read.

package qobuzdl

import (
	"strings"
	"time"
)

// Settings is QobuzDLSettings. Its zero value is not upstream's default;
// start from DefaultSettings.
type Settings struct {
	FallbackFolderFormat string // DefaultFolder
	LegacyCharmap        bool   // ASCII replacements instead of fullwidth characters in names

	NoAlbumArtistTag bool
	NoAlbumTitleTag  bool
	NoTrackArtistTag bool
	NoTrackTitleTag  bool
	NoReleaseDateTag bool
	NoMediaTypeTag   bool
	NoGenreTag       bool
	NoTrackNumberTag bool
	NoTrackTotalTag  bool
	NoDiscNumberTag  bool
	NoDiscTotalTag   bool
	NoComposerTag    bool
	NoConductorTag   bool
	NoEnsembleTag    bool
	NoWorkTag        bool
	NoExplicitTag    bool
	NoCopyrightTag   bool
	NoLabelTag       bool
	NoUPCTag         bool
	NoISRCTag        bool
	NoReplayGainTag  bool
	NoAlbumURLTag    bool

	MultiValueTags         bool
	DisableSmartTruncation bool
	// VariousArtistsAliases are album artist names, case-folded, that mark a
	// compilation; ParseAliases builds them.
	VariousArtistsAliases []string

	EmbedArt        bool   // fetch embed_cover.jpg; Options.EmbedArt embeds it
	NoCover         bool   // no cover.jpg
	EmbeddedArtSize string // "600"
	SavedArtSize    string // "org"

	MultipleDiscPrefix      string // "CD"
	MultipleDiscOneDir      bool
	MultipleDiscTrackFormat string // DefaultMultipleDiscTrack

	MaxWorkers    int // 3
	UserAuthToken string
	// Delay is the pause between tracks and albums; above 0 it also turns
	// the worker pool off. Python takes whole seconds and treats negative
	// values as 0.
	Delay time.Duration
}

// DefaultSettings returns QobuzDLSettings() with no arguments.
func DefaultSettings() Settings {
	return Settings{
		FallbackFolderFormat:    DefaultFolder,
		VariousArtistsAliases:   ParseAliases(DefaultVariousArtistsAliases),
		EmbeddedArtSize:         "600",
		SavedArtSize:            "org",
		MultipleDiscPrefix:      "CD",
		MultipleDiscTrackFormat: DefaultMultipleDiscTrack,
		MaxWorkers:              3,
	}
}

// ParseAliases is QobuzDLSettings.parse_aliases on a string: split on commas,
// strip, case-fold, drop empties. Python returns a set; the slice keeps the
// first place of each name.
func ParseAliases(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, item := range strings.Split(s, ",") {
		item = pyStrip(item, "")
		if item == "" {
			continue
		}
		a := pyCasefold(item)
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// Ports qobuz_dl/constants.py from qobuz-dl 2.5.9.

package qobuzdl

// Default templates and limits.
const (
	DefaultFolder            = "{album_artist} - {album_title} ({year}) [{format} {bit_depth}]"
	DefaultTrack             = "{track_number} - {track_title_base}"
	DefaultMultipleDiscTrack = "{disc_number}.{track_number} - {track_title_base}"
	// DefaultVariousArtistsAliases lists the album artist names that mark a
	// compilation of various artists.
	DefaultVariousArtistsAliases = "various artists, various, va, artistes divers, verschiedene interpreten, " +
		"varios artistas, artisti vari, vari"
	// OKMaxCharacterLength is the longest album track file name allowed, in
	// characters: folder name, track name and extension.
	OKMaxCharacterLength = 180
)

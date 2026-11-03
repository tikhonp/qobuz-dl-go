package qobuzdl

import (
	"slices"
	"testing"
	"time"
)

// settingsNotPorted are QobuzDLSettings attributes that only the Python CLI,
// config.ini or the lyrics code read.
var settingsNotPorted = []string{
	"email", "password", "default_folder", "default_quality", "default_limit", "no_m3u",
	"albums_only", "no_fallback", "no_database", "app_id", "secrets", "folder_format",
	"track_format", "smart_discography", "lrc_files", "embed_lyrics", "cover_og_quality",
}

func TestDefaultSettingsMatchPython(t *testing.T) {
	s := DefaultSettings()
	aliases := make([]any, 0, len(s.VariousArtistsAliases))
	for _, a := range slices.Sorted(slices.Values(s.VariousArtistsAliases)) {
		aliases = append(aliases, a)
	}
	ported := map[string]any{
		"fallback_folder_format":     s.FallbackFolderFormat,
		"legacy_charmap":             s.LegacyCharmap,
		"no_album_artist_tag":        s.NoAlbumArtistTag,
		"no_album_title_tag":         s.NoAlbumTitleTag,
		"no_track_artist_tag":        s.NoTrackArtistTag,
		"no_track_title_tag":         s.NoTrackTitleTag,
		"no_release_date_tag":        s.NoReleaseDateTag,
		"no_media_type_tag":          s.NoMediaTypeTag,
		"no_genre_tag":               s.NoGenreTag,
		"no_track_number_tag":        s.NoTrackNumberTag,
		"no_track_total_tag":         s.NoTrackTotalTag,
		"no_disc_number_tag":         s.NoDiscNumberTag,
		"no_disc_total_tag":          s.NoDiscTotalTag,
		"no_composer_tag":            s.NoComposerTag,
		"no_conductor_tag":           s.NoConductorTag,
		"no_ensemble_tag":            s.NoEnsembleTag,
		"no_work_tag":                s.NoWorkTag,
		"no_explicit_tag":            s.NoExplicitTag,
		"no_copyright_tag":           s.NoCopyrightTag,
		"no_label_tag":               s.NoLabelTag,
		"no_upc_tag":                 s.NoUPCTag,
		"no_isrc_tag":                s.NoISRCTag,
		"no_replaygain_tag":          s.NoReplayGainTag,
		"no_album_url_tag":           s.NoAlbumURLTag,
		"multi_value_tags":           s.MultiValueTags,
		"disable_smart_truncation":   s.DisableSmartTruncation,
		"various_artists_aliases":    aliases,
		"embed_art":                  s.EmbedArt,
		"no_cover":                   s.NoCover,
		"embedded_art_size":          s.EmbeddedArtSize,
		"saved_art_size":             s.SavedArtSize,
		"multiple_disc_prefix":       s.MultipleDiscPrefix,
		"multiple_disc_one_dir":      s.MultipleDiscOneDir,
		"multiple_disc_track_format": s.MultipleDiscTrackFormat,
		"max_workers":                int64(s.MaxWorkers),
		"user_auth_token":            s.UserAuthToken,
		"delay":                      int64(s.Delay / time.Second),
	}
	if s.Delay%time.Second != 0 {
		t.Errorf("Delay %v is not whole seconds", s.Delay)
	}
	cases := loadGolden(t, "settings")
	if len(cases) != 1 {
		t.Fatalf("%d cases, want 1", len(cases))
	}
	want := cases[0].Output.(*pyDict)
	seen := map[string]bool{}
	for _, k := range want.keysInOrder() {
		seen[k] = true
		if slices.Contains(settingsNotPorted, k) {
			continue
		}
		got, ok := ported[k]
		if !ok {
			t.Errorf("Python attribute %s is neither ported nor listed as not ported", k)
			continue
		}
		if w := want.get(k, nil); pyRepr(got) != pyRepr(w) {
			t.Errorf("%s = %s, want %s", k, pyRepr(got), pyRepr(w))
		}
	}
	for k := range ported {
		if !seen[k] {
			t.Errorf("%s is not a QobuzDLSettings attribute", k)
		}
	}
	for _, k := range settingsNotPorted {
		if !seen[k] {
			t.Errorf("not-ported %s is not a QobuzDLSettings attribute", k)
		}
	}
}

func TestParseAliases(t *testing.T) {
	// From Python's QobuzDLSettings.parse_aliases, sorted.
	got := slices.Sorted(slices.Values(ParseAliases(" Various Artists,,VA , va,\tSTRASSE ,Straße, ,ǅ")))
	want := []string{"strasse", "va", "various artists", "ǆ"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := ParseAliases(""); len(got) != 0 {
		t.Errorf("empty: %q", got)
	}
}

# Upstream

## Ported version

| | |
| --- | --- |
| Repository | https://github.com/Sei969/qobuz-dl |
| Tag | `v2.5.9` |
| Commit | `e21f92a`, 2026-10-07 |
| PyPI | `qobuz-dl-ultimate` 2.5.9 |
| Ported in | qobuz-dl-go `v0.1.0`, not tagged yet |

## Files

| Python | Go | State |
| --- | --- | --- |
| `bundle.py` | `bundle.go` | planned |
| `qopy.py` | `qopy.go` | planned |
| `core.py` | `core.go` | planned |
| `downloader.py` | `downloader.go` | planned |
| `metadata.py` | `metadata.go` | planned |
| `utils.py` | `utils.go` | planned |
| `settings.py` | `settings.go` | planned |
| `constants.py` | `constants.go` | planned |
| `exceptions.py` | `errors.go` | planned |
| `db.py` | `downloadsdb/` | planned |
| `cli.py`, `commands.py`, `color.py`, `__main__.py`, `radar.py`, `sync.py`, `sync_playlist.py`, `retro_tagger.py`, `lastfm_parser.py`, `lyrics_engine.py` | none | not ported, see below |

## Functions

When two Python modules have private functions with the same name, the Go name gets the module as a prefix. Today that is only `_get_title`, in `downloader.py` and `metadata.py`.

### bundle.py

| Python | Go |
| --- | --- |
| `Bundle.__init__` | `FetchBundle(ctx, hc, playBase)` |
| `Bundle.get_app_id` | `(*Bundle).AppID` |
| `Bundle.get_secrets` | `(*Bundle).Secrets`, ordered by timezone as in Python |

### qopy.py

| Python | Go |
| --- | --- |
| `Client.__init__` | `NewClient(ctx, ClientOptions)`, which also runs `auth` and `cfg_setup` |
| `Client.label`, `Client.user_id` | `(*Client).Label`, `(*Client).UserID` |
| `_normalize_json_strings` | `normalizeJSONStrings` |
| `auth` | `(*Client).auth` |
| `_modern_sig` | `modernSig` |
| `_b64url_decode` | `b64urlDecode` |
| `_derive_session_key` | `deriveSessionKey` |
| `_unwrap_track_key` | `unwrapTrackKey` |
| `api_call` | `(*Client).apiCall` |
| `multi_meta` | `(*Client).multiMeta`, returning every page |
| `get_track_meta` | `(*Client).GetTrackMeta`, typed; `trackMeta` gives the dict |
| `get_album_meta`, `get_artist_meta`, `get_plist_meta`, `get_label_meta` | `GetAlbumMeta`, `GetArtistMeta`, `GetPlaylistMeta`, `GetLabelMeta`, typed; the downloader uses `albumMeta` and `multiMeta` |
| `search_albums`, `search_tracks`, `search_artists`, `search_playlists` | `SearchAlbums`, `SearchTracks`, `SearchArtists`, `SearchPlaylists`, typed, with an offset |
| `get_track_url` | `(*Client).getTrackURL`; `GetTrackURL` is its typed view |
| `session/start` branch of `get_track_url` | `(*Client).startSession` |
| `cfg_setup` | `(*Client).cfgSetup` |
| `get_track_ids_from_list`, `get_favorites`, `add_favorite_album` | not ported |

### core.py

| Python | Go |
| --- | --- |
| `QobuzDL.__init__` | `New(Options) (*DL, error)` |
| `initialize_client` | `(*DL).InitializeClient` |
| `get_tokens` | `(*DL).GetTokens` |
| `download_from_id` | `(*DL).DownloadFromID(ctx, DownloadRequest)` |
| `handle_url` | `(*DL).HandleURL`, which parses the URL and calls `(*DL).HandleID` with the rest of the body |
| `search_by_type` | `(*DL).SearchByType`, without lucky mode or favorites |
| `_align_text` | `alignText` |
| `mark_url_done_in_file`, `download_list_of_urls`, `download_from_txt_file`, `lucky_mode`, `interactive`, `download_lastfm_pl` | not ported |

### downloader.py

| Python | Go |
| --- | --- |
| `format_release_type` | `formatReleaseType` |
| `_is_name_too_long`, `_shorten_utf8`, `_fit_name` | `isNameTooLong`, `shortenUTF8`, `fitName` |
| `process_folder_format_with_subdirs` | `processFolderFormatWithSubdirs` |
| `Download` | `download` |
| `download_id_by_type`, `download_release`, `download_track` | `downloadIDByType`, `downloadRelease`, `downloadTrack` |
| `_download_and_tag` | `downloadAndTag` |
| `_get_filename_attr`, `_get_track_attr`, `_get_album_attr` | `getFilenameAttr`, `getTrackAttr`, `getAlbumAttr` |
| `_get_format`, `_determine_formats` | `getFormat`, `determineFormats` |
| `_generate_tracklist` | `generateTracklist` |
| `_get_description`, `_get_title`, `_get_extra`, `_clean_format_str`, `_safe_get` | `getDescription`, `downloaderGetTitle`, `getExtra`, `cleanFormatStr`, `safeGet` |
| `tqdm_download` | `transfer` |
| `tqdm_download_segments` with `get_seg_size`, `fetch_segment_fluid` | `transferSegments` |
| `Download.__init__` | the unexported `download` struct, one per call |
| `_get_qobuz_segment_uuid`, `_decrypt_qobuz_segment` | `qobuzSegmentUUID`, `decryptQobuzSegment` |
| `_download_goodies`, `_clean_embed_art` | `downloadGoodies`, `cleanEmbedArt` |
| `safe_print`, `_append_lyrics_to_booklet` | not ported |

### metadata.py

| Python | Go |
| --- | --- |
| `_get_title_with_version`, `_get_title` | `getTitleWithVersion`, `metadataGetTitle` |
| `_various_artists_aliases`, `_is_various_artists`, `_release_types`, `_disc_track_total` | `variousArtistsAliases`, `isVariousArtists`, `releaseTypes`, `discTrackTotal` |
| `_format_copyright`, `_format_genres` | `formatCopyright`, `formatGenres` |
| `_embed_flac_img`, `_embed_id3_img` | `embedFLACImg`, `embedID3Img` |
| `tag_flac`, `tag_mp3` | `TagFLAC`, `TagMP3` |
| `_get_tags_to_add` | `getTagsToAdd` |
| mutagen's FLAC block handling | `flac.go`, around `flacvorbis` and `flacpicture` |

### utils.py

| Python | Go |
| --- | --- |
| `make_m3u` | `MakeM3U` |
| `smart_discography_filter` | `SmartDiscographyFilter` |
| `format_duration` | `FormatDuration` |
| `create_and_return_dir` | `createAndReturnDir` |
| `get_url_info` | `GetURLInfo` |
| `get_album_artist` | `getAlbumArtist`, and `GetAlbumArtist` over a typed `Album` |
| `apply_legacy_charmap` | `applyLegacyCharmap` |
| `clean_filename` | `CleanFilename` |
| `invalid_chars_to_fullwidth` | `invalidCharsToFullwidth` |
| `read_config_file`, `label_matches`, `PartialFormatter` | not ported; `PartialFormatter` is never used upstream, `label_matches` serves `filter_label` |
| `str.format` | `pyFormat` in `format.go` |
| `pathvalidate.sanitize_filename`, `sanitize_filepath` 3.3.1 | `sanitizeFilename`, `sanitizeFilepath` in `pathvalidate.go` |
| `textwrap.fill` | `textwrapFill` in `textwrap.go` |

### settings.py, constants.py, exceptions.py, db.py

| Python | Go |
| --- | --- |
| `QobuzDLSettings` | `Settings`, with `DefaultSettings()` |
| `parse_aliases` | `parseAliases` |
| `from_arguments_configparser` | not ported |
| `DEFAULT_FOLDER`, `DEFAULT_TRACK`, `DEFAULT_MULTIPLE_DISC_TRACK`, `DEFAULT_VARIOUS_ARTISTS_ALIASES`, `OK_MAX_CHARACTER_LENGTH` | the same names in Go style |
| `AuthenticationError`, `IneligibleError`, `InvalidAppIdError`, `InvalidAppSecretError`, `InvalidQuality`, `NonStreamable` | `ErrAuthentication`, `ErrIneligible`, `ErrInvalidAppID`, `ErrInvalidAppSecret`, `ErrInvalidQuality`, `ErrNonStreamable` |
| `create_db` | `downloadsdb.Open`, both migrations included |
| `handle_download_id` | the `DownloadsDB` interface, `Has` and `Add`; `downloadsdb.DB` implements it |
| `get_stats` | not ported |

## Deliberate differences

| Difference | Why |
| --- | --- |
| Every network call takes a `context.Context` and has a timeout. Python's API calls have none. | A stalled call must not block a server forever. |
| Errors are returned. Python calls `sys.exit` on a 401 during auth and only logs most other failures. | A library must not exit. |
| Secrets are a list, scraped once by `GetTokens`. | The old bot passed a comma-joined string and scraped up to three times. |
| The `Progress` interface replaces tqdm, `safe_print` and colored log lines. | Callers show progress their own way. |
| Download functions return the final folder and an outcome per track. Python only logs them. | Callers need the result. |
| Temp files `~tmp_NN.tmp` are removed when a track fails. Python leaves them for the CLI to clean. | Nothing else cleans up after a library. |
| `SearchAlbums` and the other search methods take an offset and return errors; Python's return `{}` on any error. | Paging in a UI, and errors a caller can show. |
| Every request, transfers included, uses the caller's `*http.Client`. Python makes a new session per transfer attempt and reads proxies from the environment. | The caller decides about proxies and connection reuse. |
| Private functions with clashing names get the module as a prefix. | One Go package holds several Python modules. |
| `HandleID` is `handle_url` without the URL parsing. | Callers that already have a kind and an ID, such as shelf's playlist downloads. |
| A context end renames `[IN PROGRESS]` to `[INCOMPLETE]` and returns an error; Python does the rename on Ctrl+C and exits the process. | A library must not exit. |
| Each call copies the settings a playlist changes; Python changes the shared settings object for the loop. | Concurrent calls on one `DL`. |
| `MakeM3U` reads file names sorted; Python takes the file system's order. | Python's order varies between machines. It shows only when two files tie on a title or nothing matches. |
| A tagging failure removes the temp file. | Python leaves it for the CLI to clean. |
| `DownloadsDB.Has` gets the media type. | shelf keys its table on it; `downloadsdb` ignores it, as Python does. |
| `ErrUnhandled` is returned where an uncaught Python exception would crash the process. | A library must not crash its caller. |
| A track skipped because its file exists reports its format, read from the file. | shelf records the delivered format for every file. |
| MP3 frame order and ID3 padding differ from mutagen's. | `id3v2` writes frames in map order and adds no padding; players show neither. |
| `cfg_setup` logs a warning when no secret works. | Python silently uses the first, and later calls fail without a clue. |

## Not ported

- The CLI and its helpers: `cli.py`, `commands.py`, `color.py`, `__main__.py`, `read_config_file`, `from_arguments_configparser`. `cmd/qobuz-dl` has its own small CLI for parity runs.
- Lyrics: `lyrics_engine.py` and `_append_lyrics_to_booklet`. Music Shelf runs with lyrics off.
- Sync and imports: `sync.py`, `sync_playlist.py`, `retro_tagger.py`, `radar.py`, `lastfm_parser.py`, `get_track_ids_from_list`.
- Interactive and lucky modes, batch downloads from text files, favorites, the blacklist, and booklet-only downloads.
- `filter_label` and `label_matches`, `playlist_as_albums`.
- Settings only the Python CLI and `config.ini` read, the lyrics settings, `og_cover`, and `QobuzDL`'s `cover_og_quality` and `no_cover` arguments, which nothing reads.
- `get_stats`.

Any of these can be ported later in the same file-by-file way.

## Updating

1. `make upstream-diff TO=<tag>` prints what changed in the ported files since the tag above.
2. Port each change into the Go file with the same base name. A change to something not ported gets a line under Not ported, or a port.
3. If new functions need golden data, extend `testdata/gen/gen.py`. Install the new version in `testdata/gen/.venv` and run `make golden`.
4. Run `make check` and `make parity` on the reference albums.
5. Update this file: the tag, the commit, the function map and the differences.
6. Tag a new qobuz-dl-go version, then bump it in Music Shelf.

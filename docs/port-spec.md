# Port spec

Notes on how qobuz-dl behaves, for the port. They were drawn from version 2.5.5 and checked against 2.5.9 for the changes listed under each part. Read the 2.5.9 source next to them: where they disagree, the source wins, and this file gets fixed.

## Bundle: app ID and secrets

1. `GET https://play.qobuz.com/login` with a plain session; any HTTP error fails.
2. Find the bundle path with `<script src="(/resources/\d+\.\d+\.\d+-[a-z]\d{3}/bundle\.js)"></script>` and fetch `https://play.qobuz.com<path>`.
3. App ID: `production:{api:{appId:"(?P<app_id>\d{9})",appSecret:"\w{32}"`. The `appSecret` in the bundle is not used.
4. Secrets:
   - Seeds: `[a-z]\.initialSeed\("(?P<seed>[\w=]+)",window\.utimezone\.(?P<timezone>[a-z]+)\)`, collected in document order into an ordered map from timezone to seeds.
   - The second timezone found moves to the front. Fewer than two seeds is an error.
   - Info and extras: a regex built per run, `name:"\w+/(?P<timezone>Tz1|Tz2|…)",info:"(?P<info>[\w=]+)",extras:"(?P<extras>[\w=]+)"`, where each `TzN` is a timezone with its first letter in upper case. Each match appends info and extras to that timezone.
   - Secret: concatenate seed, info and extras, drop the last 44 characters, base64-decode, read as UTF-8. The result is a 32-character hex string.
5. Empty secrets are dropped.

## Client

Base URL `https://www.qobuz.com/api.json/0.2/`.

Headers on every API call, in effect with `force_english` on, which is the default:

```
User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36
Accept: application/json, text/plain, */*
Accept-Language: en-US,en;q=0.9
X-App-Language: en
X-App-Region: US
Sec-Ch-Ua: "Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"
Sec-Ch-Ua-Mobile: ?0
Sec-Ch-Ua-Platform: "Windows"
Sec-Fetch-Dest: empty
Sec-Fetch-Mode: cors
Sec-Fetch-Site: same-site
X-App-Id: <app id>
X-User-Auth-Token: <token>     after auth
X-Session-Id: <session id>     after session/start
```

Every JSON string in a response is NFC-normalized. In strings that don't contain `://`, `...` becomes `…`. Both change tags and file names.

### Auth

- Token only. `user/login` is never called.
- The token is `user_auth_token` from the settings when set. Otherwise a password longer than 60 characters is taken as the token. Otherwise auth fails.
- Check: `GET user/get` with `app_id`, `lang=en`, `locale=en_US`. A 401 is `ErrAuthentication`. A 400 gives an empty user.
- The subscription label is `credential.parameters.short_label`, or under `user.`, default `Studio`. The user ID is `id`, or `user.id`.

### Secret selection

For each secret in order, call `track/getFileUrl` with track `5966783`, format 5, signed with that secret. The first one that answers wins. When none does, Python silently uses the first; the port does the same and logs a warning.

### Signing

`ts` is the Unix time in whole seconds. Signatures are the MD5 hex digest of:

| Call | String |
| --- | --- |
| `track/getFileUrl` | `trackgetFileUrlformat_id<fmt>intentstreamtrack_id<id><ts><secret>` |
| any call through `_modern_sig` | `<object><method>` + each sorted scalar parameter as key then value, without `request_ts` and `request_sig`, + `<ts><secret>` |
| `session/start` | `sessionstartprofileqbz-1<ts><secret>`, sent as a POST form with `profile=qbz-1` |
| `file/url` | `fileurlformat_id<fmt>intentimporttrack_id<id><ts><secret>` |
| `favorite/getUserFavorites` | `favoritegetUserFavorites<ts><secret>` |

`track/getFileUrl` sends `track_id`, `format_id`, `intent=stream`, `request_ts` and `request_sig`, and no `app_id` parameter. Formats outside 5, 6, 7 and 27 are `ErrInvalidQuality`; `file/url` accepts 6, 7 and 27.

### Endpoints

Unsigned GETs send `app_id`, `lang=en`, `locale=en_US` and:

| Endpoint | Parameters |
| --- | --- |
| `album/get` | `album_id` |
| `track/get` | `track_id` |
| `playlist/get` | `playlist_id`, `extra=tracks`, `offset`, `limit=50` |
| `artist/get` | `artist_id`, `extra=albums`, `offset`, `limit=50` |
| `label/get` | `label_id`, `extra=albums`, `offset`, `limit=50` |
| `catalog/search` | `query`, `type` of `albums`, `tracks`, `artists` or `playlists`, and `limit`, 20 by default. Python sends no offset; the port adds it. |

Paging in `multi_meta`: limit 50, `offset` grows by the items received, stop on an empty page or when `offset` reaches `<key>.total`. The key is `tracks` for playlists and `albums` otherwise. `album/get` returns its tracks without paging; check albums with more than 50 tracks against the real API.

### Errors

- A 400 from `track/getFileUrl`, `file/url` or `favorite/getUserFavorites` is `ErrInvalidAppSecret`, whatever the cause.
- Everything else fails on any HTTP error status. Python has no retries for API calls, no 429 handling and no timeouts; the port adds timeouts only.
- Python's search helpers swallow every error and return an empty result. The port returns the error.

### Segmented path

Used when `getFileUrl` gives no `url`, or when a direct transfer fails.

1. Once per client: `session/start` gives `session_id` and `infos`. The session key is `HKDF-SHA256(ikm = hex-decoded secret, salt = base64url of infos' first dot-separated part, info = base64url of the second part, length 16)`. `X-Session-Id` is set.
2. `file/url` answers `url_template` with `$SEGMENT$`, `n_segments`, and `key`. `bits_depth` becomes `bit_depth`; a `sampling_rate` above 1000 is divided by 1000.
3. `raw_key` is `key`'s `"_.wrapped.iv"` parts, base64url with padding restored, decrypted with AES-128-CBC under the session key, PKCS7 unpadded.

## Quality

- Format IDs: 5 MP3 320; 6 FLAC 16-bit 44.1 kHz; 7 FLAC 24-bit up to 96 kHz; 27 FLAC 24-bit above 96 kHz.
- Release gate, in `_get_format`: call `get_track_url` for the first track at the requested format. A `restrictions[].code` of `FormatRestrictedByFormatAvailability` means the quality is not met. Without fallback, a release that doesn't meet it is skipped before any folder exists. Any exception lets the release through as `Unknown`.
- Track tiers, in `_download_and_tag`: with fallback, try `[27, 7, 6, 5]` from the requested tier down; without, only the requested tier. Since 2.5.6, a tier whose answer has `sample` set moves on to the next tier instead of failing the track. Each tier tries the direct transfer first, then the segmented path.
- The delivered `format_id` decides the extension and the tagger: 5 gives `.mp3` and ID3 tags, anything else `.flac` and Vorbis comments.
- A release that isn't `streamable` or has no `tracks.items` is `ErrNonStreamable`.

## Downloads

### Album

1. `album/get`; check streamable, the albums-only and label filters.
2. The release gate.
3. Album attributes and the naming template.
4. Folder states: work happens in `[IN PROGRESS] <folder>`. An existing `[INCOMPLETE] <folder>` or `<folder>` is renamed into it first. Playlists, and names too long for the prefix, skip the states.
5. Extras, in this order: `<album title> - Tracklist.txt` unless `no_credits`; `cover.jpg` from `image.large` with `_600.` replaced by `_<saved_art_size>.`, `org` by default; `embed_cover.jpg` at `embedded_art_size`, 600 by default, only when `settings.embed_art`; goodies as `<album title> (<goody id>).pdf`.
6. A pool of `settings.max_workers` workers, 3 by default, or 1 when `settings.delay` is above 0. The main loop calls `get_track_url` per track in order and submits the transfers.
7. `embed_cover.jpg` is deleted. The folder becomes `<folder>` when no track failed, `[INCOMPLETE] <folder>` otherwise.

### Track

`get_track_url` and `track/get`. Inside a playlist the track number becomes the playlist position, which shows in the tags. `cover.jpg` is saved unless the track is part of a playlist. No tracklist, goodies or folder states.

### Per track

1. File name: playlists use `{artist} - {track_title}`; multi-disc albums with `multiple_disc_one_dir` use the multi-disc template; otherwise the track template, under `CD 0N/` for multi-disc albums. The prefix is `multiple_disc_prefix`, `CD` by default, and the disc number has two digits.
2. Paths over 180 characters keep the first 110, `...` and the last 60. A name the file system rejects as too long is cut to 255 UTF-8 bytes.
3. An existing final file is skipped and counts as success.
4. Sleep one second.
5. Transfer to `<dir>/~tmp_NN.tmp`, NN being the track's index.
6. Tag the temp file, then rename it to the final name.
7. Sleep `settings.delay` when above 0.

### Direct transfer

- Headers: the short user agent, `Accept: audio/webm,audio/ogg,audio/wav,audio/*;q=0.9,*/*;q=0.5`, and `Range: bytes=0-`, or `bytes=N-` with append mode when part of the file exists.
- Timeouts of 10 seconds to connect and 60 to read, 64 KiB chunks.
- Up to 5 attempts, sleeping 2, 4, 8 and 16 seconds between them.
- A 416 means done. A status other than 200 or 206 retries.
- Since 2.5.8, a 200 to a resumed request starts the file over instead of appending.
- An error mentioning 404 deletes the file and fails at once.
- Success needs at least Content-Length bytes.
- Covers and goodies use the same transfer.

### Segmented transfer

1. HEAD segments 0 to `n_segments` inclusive with 8 workers to get the size.
2. GET segment 0 and write it as it is; GET segment 1.
3. The usertype of the `uuid` box in segment 1 is the segment UUID.
4. GET segments 2 to n with 8 workers and write them in order to `<tmp>.mp4`.
5. Decrypt: walk each segment's top-level boxes. In each `uuid` box with the segment UUID, start at box + 28: `data_off = box + u32`, then `counter_len` u8 and `frame_count` u24; per frame, `frame_len` u32, skip 6 bytes, `flags` u16; a frame with non-zero flags is AES-CTR decrypted with `raw_key` and an IV of the next `counter_len` bytes zero-padded to 16.
6. Remux: `ffmpeg -nostdin -v error -y -i <tmp>.mp4 -c:a copy -f flac <tmp>`.
7. No retries on this path.

### Naming

Templates the bot gets from `QobuzDL`'s defaults: folder `{artist} - {album} ({year}) [{bit_depth}B-{sampling_rate}kHz]`, track `{track_number} - {track_title}`. Constants: `DEFAULT_FOLDER` `{album_artist} - {album_title} ({year}) [{format} {bit_depth}]`, `DEFAULT_TRACK` `{track_number} - {track_title_base}`, `DEFAULT_MULTIPLE_DISC_TRACK` `{disc_number}.{track_number} - {track_title_base}`. A template falls back to `settings.fallback_folder_format`, then the default, only when formatting raises a key or value error.

Folder variables: `artist`, `album`, `album_id`, `album_url`, `album_title`, `album_title_base`, `album_artist`, `albumartist`, `album_genre`, `album_composer`, `label`, `copyright`, `upc`, `barcode`, `release_date`, `year`, `media_type`, `album_version`, `version_tag`, `disc_count`, `track_count`, `ExplicitFlag`, `explicit`, `release_type`, `format`, `bit_depth`, `sampling_rate`, `quality_tag`.

Track variables: `artist`, `albumartist`, `album_artist`, `track_artist`, `track_composer`, `tracktitle`, `track_title`, `track_title_base`, `album_title`, `album_title_base`, `version`, `track_id`, `isrc`, `track_number`, `disc_number`, `year`, `release_date`, `bit_depth`, `sampling_rate`, `ExplicitFlag`, `explicit`. Track and disc numbers have two digits. Bit depth and sampling rate are the track's maximum.

- `bit_depth` and `sampling_rate` in folder names come from the release gate's `getFileUrl` answer. Python prints an int as `96` and a float as `96.0`; format floats with `strconv.FormatFloat(v, 'f', -1, 64)` only where Python had an int, so goldens decide. A failed gate prints `None`.
- `title (version)` unless the title already contains the version, ignoring case.

Sanitizing, `clean_filename`, in order: NFC; collapse repeated separators from `,.:;|/\_`; drop bracket pairs with no word characters inside; collapse whitespace and strip spaces and dots; map `/ \ : * ? " < > |` to the fullwidth `／ ＼ ： ＊ ？ ＂ ＜ ＞ ｜`; then pathvalidate's `sanitize_filename` with `_` as the replacement. Folder templates split on `/` into subfolders, and a part over 120 characters keeps the first 60, `...` and the last 50.

## Tags

FLAC Vorbis comments, each switchable with a `no_*_tag` setting; empty values are skipped:

| Tag | Source |
| --- | --- |
| `ALBUM` | album title plus ` (version)` |
| `TITLE` | track title plus ` (version)` |
| `ALBUMARTIST` | `get_album_artist`: the album's `main-artist` names, else `artist.name`; several values |
| `ARTIST` | `performer.name` or the album artist, plus `performers` entries with role MainArtist or FeaturedArtist, de-duplicated; an "A & B" entry is dropped when A and B appear on their own |
| `COMPOSER` | `performers` with role Composer or ComposerLyricist, else `composer.name` |
| `DATE` | `release_date_original` |
| `GENRE` | `genre.name` first, then `genres_list` split on `→`, French names mapped to English, de-duplicated, joined with `, ` |
| `COPYRIGHT` | `copyright` with `(P)` as ℗ and `(C)` as ©, `n/a` when missing |
| `LABEL` | `label.name` with whitespace collapsed |
| `ISRC`, `BARCODE` | `isrc`; the album's `upc` |
| `MEDIATYPE` | `product_type` in upper case |
| `ITUNESADVISORY` | `1` when `parental_warning` |
| `COMPILATION` | since 2.5.7, `1` when the release type is `compilation` or the album artist is a various-artists alias |
| `RELEASETYPE` | since 2.5.7, `album`, `single` or `ep` from `release_type` or `product_type`, plus `compilation` for compilations; several values |
| `REPLAYGAIN_TRACK_GAIN`, `REPLAYGAIN_TRACK_PEAK` | `audio_info`, the gain as `<value> dB` |
| `WORK`, `CONDUCTOR`, `ENSEMBLE` | `work`; `performers` with role Conductor; roles Orchestra, Ensemble or Choir |
| `QOBUZTRACKID`, `QOBUZALBUMID` | the IDs |
| `QOBUZ ALBUM URL` | `https://www.qobuz.com/album/<slug>/<album id>`, slug being the lower-case title with other runs than a-z and 0-9 turned into `-` |
| `TRACKNUMBER`, `DISCNUMBER`, `DISCTOTAL` | `track_number` unpadded, `media_number`, `media_count` |
| `TRACKTOTAL` | since 2.5.7, the number of tracks on the track's disc when the album has several discs and its track list is complete; otherwise `tracks_count` |

Various-artists aliases default to `various artists, various, va, artistes divers, verschiedene interpreten, varios artistas, artisti vari, vari`, compared after trimming and case folding.

- The cover is a PICTURE block, type 3, `image/jpeg`, description `cover`, only when `embed_cover.jpg` exists in the track's folder or its parent, at most 16,777,215 bytes.
- `ENCODER`, `ENCODED-BY` and `ENCODED_BY` are removed, the vendor string is emptied, and the file is saved with 8,192 bytes of padding.
- MP3 uses ID3v2.3 frames: TPE2, TALB, TPE1, TIT2, TMED, TCON, TCOM, TCOP, TPUB, TSRC, TPE3, TIT1, TYER and TDAT for the date, `TRCK n/total`, `TPOS d/total`, TXXX for the rest, APIC for the cover; TENC and TSSE removed.
- Album downloads read album fields from the full `album/get`; single tracks from the album inside `track/get`.

## URLs, collections and search

- `get_url_info`: `(?:https:\/\/(?:w{3}|open|play)\.qobuz\.com)?(?:\/[a-z]{2}-[a-z]{2})?\/(album|artist|track|playlist|label)(?:\/[-\w\d]+)?\/([\w\d]+)`, answering the kind and the ID. `/interpreter/` links are not matched.
- `handle_url`: albums and tracks go to `download_from_id`. Playlists, artists and labels load every page first, create `<dir>/<sanitized name>/`, and download each item. Artists with smart discography pass through `smart_discography_filter`. Playlists use a flat folder with `multiple_disc_one_dir` on, then write `<name>.m3u8` ordered by matching `QOBUZTRACKID`, then ISRC, then title, then a file name substring, with `#EXTINF:<seconds>, <artist> - <title>` lines.
- `smart_discography_filter`, with save-space and skip-extras on: group by title without bracketed text, lower case; keep the highest bit depth, then the lowest sampling rate; keep releases whose artist is the requested one; prefer remasters; drop anniversary, deluxe, live, collector, demo and expanded editions.
- `download_from_id` checks the downloads database for the ID at the requested quality first, and only catches request and not-streamable errors.
- `search_by_type`: `catalog/search` with `type=<kind>s`, at least 3 characters. Display fields: artist, title with version and `[E]`, year, the release type, and `[HI-RES] <bit>b/<rate>kHz` when `hires_streamable`, else CD. Without a release type, 1740 seconds or 7 tracks and more make an album, 1 track a single, and anything else an EP.

## Downloads database

One table, `downloads`, with the primary key `(id, quality)`:

| Column | Default |
| --- | --- |
| `id` | |
| `media_type` | `album` |
| `quality` | 27 |
| `file_format` | `FLAC` |
| `quality_met` | 0 |
| `bit_depth`, `sampling_rate` | null |
| `saved_path` | |
| `status` | `downloaded` |
| `url`, `release_date`, `artist`, `album` | empty |

- `handle_download_id` without `add_id` selects by ID and quality; with it, inserts and only logs a duplicate.
- Albums are recorded only after every track succeeded, with the gate's format, bit depth and rate, the final folder, the album URL, the release date, the album artists joined, and the title. Single tracks are recorded with `media_type = 'track'`. Playlists, artists and labels are never recorded.

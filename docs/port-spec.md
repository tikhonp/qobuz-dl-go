# Port spec

Notes on how qobuz-dl 2.5.9 behaves, for the port. They were checked line by line against the source at tag `v2.5.9`, commit `e21f92a`, which matches the PyPI build `qobuz-dl-ultimate==2.5.9`. Where they disagree with the source, the source wins and this file gets fixed.

Quirks that look like upstream bugs are kept on purpose and marked "kept". The port copies them so the files come out the same.

## Bundle: app ID and secrets

`bundle.py`.

1. `GET https://play.qobuz.com/login` with a plain session; any HTTP error fails.
2. Find the bundle path with `<script src="(/resources/\d+\.\d+\.\d+-[a-z]\d{3}/bundle\.js)"></script>` and fetch `https://play.qobuz.com<path>`. No match raises `NotImplementedError`.
3. App ID: `production:{api:{appId:"(?P<app_id>\d{9})",appSecret:"\w{32}"`. The `appSecret` in the bundle is not used.
4. Secrets:
   - Seeds: `[a-z]\.initialSeed\("(?P<seed>[\w=]+)",window\.utimezone\.(?P<timezone>[a-z]+)\)`, collected in document order into an ordered map from timezone to `[seed]`. A timezone seen twice keeps its first position and its last seed.
   - The second timezone moves to the front. Fewer than two timezones raises `IndexError`.
   - Info and extras: a regex built per run, `name:"\w+/(?P<timezone>Tz1|Tz2|…)",info:"(?P<info>[\w=]+)",extras:"(?P<extras>[\w=]+)"`, where each `TzN` is the timezone through `str.capitalize`: first letter upper case, the rest lower case. Each match appends info and extras to the timezone's list.
   - Secret: concatenate the list, drop the last 44 characters, decode with standard base64, read as UTF-8. Python 3.14's decoder is lenient: it drops characters outside the alphabet, such as `_`, keeps decoding after a `=`, and wants enough `=` only after the last data character.
5. `get_secrets` returns the map with every timezone, empty secrets included. `QobuzDL.get_tokens` keeps the values in order and drops the empty ones.

`\w` in these regexes is Python's Unicode `\w`; bundle text is ASCII, so `[A-Za-z0-9_]` is equivalent here.

## Client

`qopy.py`. Base URL `https://www.qobuz.com/api.json/0.2/`.

### Construction

`Client(email, pwd, app_id, secrets, user_auth_token, force_english)`:

1. When `app_id` is empty or `798273057`, the client fetches the bundle itself and takes its app ID and all its secret values, empty ones included. Any failure there is ignored and the given values stay, except that a failure in `get_secrets` comes after the new app ID is set, which then stays with the old secrets. `QobuzDL` passes the app ID from `get_tokens`, but on 2026-11-03 the web player's bundle itself carried `798273057`, so the bot runs this branch: a second bundle fetch, and the secrets `get_tokens` filtered are replaced by all of the bundle's values, empty ones included, in the same order. `cfg_setup` then tries an empty secret like any other, and it fails.
2. Session headers. With `force_english`, the default:

   ```
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
   ```

   Always, so this user agent wins:

   ```
   User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36
   X-App-Id: <app id>
   ```

   requests' header dict keeps a key's place when it is set again, so on the wire `User-Agent` comes first and `X-App-Id` last of these. Without `force_english` the session sends `User-Agent`, requests' default `Accept: */*` and `X-App-Id`. `auth` adds `X-User-Auth-Token`, and `session/start` adds `X-Session-Id`. requests adds `Accept-Encoding` and `Connection: keep-alive` on its own. `testdata/golden/request_params.json` has every request as sent.
3. `auth`, then `cfg_setup`.

Every JSON answer passes through `_normalize_json_strings`: each string that contains `...` and not `://` gets `...` replaced by `…`, then every string is NFC-normalized. Keys are left alone. Both steps change tags and file names.

### Auth

- Token only; `user/login` is never called.
- The token is `user_auth_token` when set. Otherwise a password longer than 60 characters is the token. Otherwise Python prints a message and calls `sys.exit(1)`.
- `X-User-Auth-Token` is set, then `GET user/get` with the standard parameters.
- A 401 prints a message and calls `sys.exit(1)`.
- A 400 answers `{}`. Any other HTTP error, and any other exception, logs "Profile validation bypassed" and goes on with label `Studio` and no user ID. Auth does not fail on them.
- The label is `credential.parameters.short_label`, where `credential` is the top-level one or `user.credential`; default `Studio`. The user ID is `id`, or `user.id`.

### Secret selection

`cfg_setup` calls `track/getFileUrl` for track `5966783` at format 5, signed with each secret in turn. The first that answers without an exception wins. When none does, the first secret is used without a warning. An empty list raises `InvalidAppSecretError("No secret found.")`.

### Signing

`ts` is the Unix time in whole seconds. Signatures are the MD5 hex digest of:

| Call | String |
| --- | --- |
| `track/getFileUrl` | `trackgetFileUrlformat_id<fmt>intentstreamtrack_id<id><ts><secret>` |
| `session/start`, `file/url`, `track/lyricsUrl` through `_modern_sig` | `<object><method>`, then for each parameter in sorted key order, except `request_ts` and `request_sig` and values that are not str, int or float, the key and `str(value)`; then `<ts><secret>` |
| `favorite/getUserFavorites` | `favoritegetUserFavorites<ts><secret>` |

For `session/start` that gives `sessionstartprofileqbz-1<ts><secret>`; for `file/url`, `fileurlformat_id<fmt>intentimporttrack_id<id><ts><secret>`.

### Requests

| Endpoint | Method and parameters |
| --- | --- |
| `track/getFileUrl` | GET `track_id`, `format_id`, `intent=stream`, `request_ts`, `request_sig`. No `app_id`, `lang` or `locale`. A format outside 5, 6, 7 and 27 raises `InvalidQuality` before any request. |
| `session/start` | POST form `profile=qbz-1`, `request_ts`, `request_sig`, with `Content-Type: application/x-www-form-urlencoded`. |
| `file/url` | GET `track_id`, `format_id`, `intent=import`, `request_ts`, `request_sig`. Formats 6, 7 and 27 only. |
| everything else | GET `app_id`, then `lang=en` and `locale=en_US` when `force_english`, then the call's keyword arguments other than `id`, `sec` and `fmt_id`, then the ID parameter below. requests drops parameters whose value is `None`. |

| Endpoint | ID parameter and extras |
| --- | --- |
| `album/get` | `album_id` |
| `track/get` | `track_id` |
| `playlist/get` | `playlist_id`, `extra=tracks` |
| `artist/get` | `artist_id`, `extra=albums` |
| `label/get` | `label_id`, `extra=albums` |
| `catalog/search` | `query`, `type`, `limit`, from the keyword arguments |
| `user/get` | none |

`multi_meta` passes `offset`, `limit=50` and `type=None`, so the query is `app_id, lang, locale, offset, limit, <id param>, extra`.

### Errors

- A 400 from `track/getFileUrl`, `file/url` or `favorite/getUserFavorites` raises `InvalidAppSecretError`, whatever the cause.
- `user/get` with a 400 answers `{}`.
- Every other error status raises `requests.HTTPError` through `raise_for_status`. There are no retries, no 429 handling and no timeouts.
- The search helpers swallow every exception and answer `{}`; `search_by_type` then fails on the missing key and logs "Invalid type". The port returns the error instead.

### Paging

`multi_meta(epoint, key, id, type)` loops:

1. Call with `offset` and `limit=50`.
2. `items` is `<items_key>.items`, where `items_key` is `tracks` when the endpoint contains `playlist` and `albums` otherwise. No items ends the loop before the page is yielded.
3. Yield the page, add `len(items)` to `offset`.
4. Stop when `offset` reaches `<items_key>.total`, or, when that is missing, the page's `key`: `tracks_count` or `albums_count`, default 0.

`album/get` returns its tracks without paging. Albums with more than 50 tracks need a check against the real API.

### Track URL

`get_track_url(id, fmt_id, force_segments=False)`:

1. Format 5: `track/getFileUrl` and return its answer, whatever it is.
2. Otherwise, unless `force_segments`: `track/getFileUrl`; an answer with a `url` key is returned. Any exception, the 400 `InvalidAppSecretError` included, is swallowed, and so is an answer without `url`.
3. Segmented: on the first use per client, `session/start` gives `session_id` and `infos`; the session key is derived and `X-Session-Id` set. Then `file/url`. `bits_depth` is copied to `bit_depth` when `bit_depth` is missing. A `sampling_rate` above 1000 is divided by 1000, which makes it a float: `96000` becomes `96.0`. When `key` is present, `raw_key` is added.

Session key: `HKDF-SHA256(ikm = bytes.fromhex(secret), salt = b64url(infos part 1), info = b64url(infos part 2), length 16)`, with `infos` split on its one dot. `b64url` restores the padding first.

Track key: `key` splits on dots into three parts, `_`, `wrapped` and `iv`; `raw_key` is `wrapped` decrypted with AES-128-CBC under the session key and that IV, then PKCS7-unpadded.

## Quality

- Format IDs: 5 MP3 320; 6 FLAC 16-bit 44.1 kHz; 7 FLAC 24-bit up to 96 kHz; 27 FLAC 24-bit above 96 kHz.
- `QL_DOWNGRADE` is `FormatRestrictedByFormatAvailability`.

### Release gate

`_get_format(item, is_track_id, track_url_dict)`:

1. For an album, no `tracks.items` raises `NonStreamable`.
2. The answer is `track_url_dict` when given, which single tracks do; otherwise `get_track_url(first track, requested format)`, which may go segmented.
3. Inside a catch-all: an empty answer raises `KeyError`. `quality_met` starts true and turns false when `restrictions` is a list with an entry whose `code` is `QL_DOWNGRADE`. The answer is `("MP3" if requested is 5 else "FLAC", quality_met, answer["bit_depth"], answer["sampling_rate"])`.
4. Any exception answers `("Unknown", quality_met, None, None)`, so a failed gate never skips a release and prints `None` in names.

The format string comes from the requested format, not the delivered one. Without fallback, a release whose `quality_met` is false is skipped before any folder exists.

### Track tiers

In `_download_and_tag`: the tiers are `[27, 7, 6, 5]` from the requested one down with fallback, or only the requested one without. A requested format outside the list starts at 27. For each tier:

1. `get_track_url(track, tier)`.
2. An answer with a truthy `sample` moves on to the next tier (2.5.6).
3. With a `url`: direct transfer. On success the tier wins. On failure: `get_track_url(track, tier, force_segments=True)`.
4. With a `url_template`: segmented transfer, and the tier wins.
5. Otherwise an exception, which the loop swallows; next tier.

Any exception in a tier, from the API, the transfer or the segmented path, moves on to the next tier. When no tier wins, the track fails.

After a win, the delivered format is `int(answer["format_id"] or tier)`, from the last answer. Format 5 gives `.mp3` and the MP3 tagger, anything else `.flac` and the FLAC tagger. The final name swaps its extension accordingly.

## Downloads

### Release

`download_release`, in order:

1. `album/get`. Not `streamable` raises `NonStreamable`.
2. With `albums_only`, a `release_type` other than `album`, or `artist.name` equal to `Various Artists`, logs "Ignoring Single/EP/VA" and returns.
3. With `filter_label`, a label mismatch returns.
4. `album_title` is `downloader._get_title`: `title (version)` unless the version is already in the title, ignoring case.
5. The release gate. A skipped release returns here.
6. Album attributes, then `_determine_formats` picks the templates.
7. The folder: `process_folder_format_with_subdirs(folder_format, album_attr, path)`.
8. Folder states, below.
9. Extras, below.
10. Tracks through the worker pool, below.
11. `embed_cover.jpg` is deleted, unless aborted.
12. The folder gets its final state.
13. On abort, `os._exit(1)`.
14. Any failed track: not recorded in the database, return. Otherwise recorded.

### Folder states

- `target` is the formatted folder; `[IN PROGRESS] <name>` and `[INCOMPLETE] <name>` sit next to it.
- Playlist downloads, and a folder whose `[IN PROGRESS]` name the file system rejects as too long, skip the states and work in `target`.
- Otherwise the work folder is `[IN PROGRESS]`. An existing `[INCOMPLETE]` is renamed into it; failing that, an existing `target`. A leftover `[IN PROGRESS]` is reused as it is. A rename error logs a warning and falls back to working in `target`.
- At the end the work folder becomes `target` when no track failed and nothing aborted, `[INCOMPLETE]` otherwise. A rename error leaves it where it is.

### Extras

In this order, each skipped when the context is aborted:

1. Tracklist, unless `no_credits`. Below.
2. `cover.jpg` from `image.large`, unless `settings.no_cover`.
3. `embed_cover.jpg` from `image.large` at `settings.embedded_art_size`, only with `settings.embed_art`.
4. Goodies, when the album has a `goodies` key: for each with a `url`, `sanitize_filename(clean_filename("<title> (<id>).pdf"))` with pathvalidate's default replacement, the empty string. `<title>` is the raw `title`, without the version. Exceptions are logged.

`_get_extra(url, dir, name, art_size)`: an existing file is kept. When `art_size` is one of `50`, `100`, `150`, `300`, `600`, `max` or `org`, `_600.` in the URL becomes `_<size>.`. The transfer is the direct one with its retries; a failure is logged and the download goes on. `og_quality` is never passed, so `cover_og_quality` has no effect.

### Tracklist

`<sanitize_filename(album_title)> - Tracklist.txt`, sanitized with the empty replacement and no `clean_filename`, then fitted to the file system with `_fit_name`. An existing file is kept. UTF-8, LF. Contents:

```
======================================================================
ALBUM      : <album_title>[ [E]]
COMPOSER   : <composer.name>                 only when the album has one
MAIN ART.  : <artist.name, default "Unknown Artist">
LABEL      : <label.name, default "Independent">
GENRE      : <genre.name mapped through LOCAL_GENRE_MAP, default "Unknown Genre">
RELEASE    : <release_date_original, default "Unknown Date">
QUALITY    : <format> (<bit_depth>-Bit / <sampling_rate> kHz)
======================================================================

```

Then the tracks. When the highest `media_number` is above 1, each disc starts with `--- DISC <n> ---` and an empty line, and every disc header but the first has an empty line before it. Per track:

```
<NN. title[ [E]], left-aligned to 60 characters> [MM:SS]
    * <line>          per non-empty piece of performers split on \r?\n or \s+-\s+
    <performer.name>  instead, when performers is empty; default the album artist
<empty line>
```

Then, when the album has a `description`: an empty line, a `=`×70 line, `ALBUM REVIEW / NOTES`, a `=`×70 line and an empty line; then the description with `<br>` as newlines and other tags removed, each non-empty paragraph stripped, wrapped with `textwrap.fill(width=70)` and followed by an empty line.

`_safe_get` returns the default only when a key is missing; a null value comes back as `None` and prints `None`. Durations are `int(duration)`, `divmod` by 60, two digits each.

### Worker pool

- `max_workers` workers, 3 by default, or 1 when `settings.delay` is above 0.
- The main loop goes through `tracks.items` in order. For each it calls `get_track_url(id, requested format)`. An exception counts the track as failed and moves on. A falsy answer is a demo: logged, counted as failed. Otherwise the track is submitted with its index.
- The index `count` starts at 0 and grows for every track, failed ones included, so it is the track's position in `tracks.items` counting from 0.
- The submitted job is `_download_and_tag(dir, count, …, multiple)`, where `multiple` is the track's `media_number` when the album's `media_count` is above 1, else `None`. It calls `get_track_url` again per tier, so a track costs at least two `getFileUrl` calls.
- After the loop the main thread waits for every job in order. A job that returns `False` or raises counts as failed.

### Single track

`download_track`:

1. `get_track_url(id, requested format)`. A falsy answer logs "Demo. Skipping" and returns without failing.
2. `track/get`. Inside a playlist the track number becomes the playlist position, which shows in tags and names.
3. With `filter_label`, a mismatch on `album.label` returns.
4. The release gate with the answer from step 1.
5. Track attributes, `_determine_formats` with the track's `album` as the album.
6. The folder: `process_folder_format_with_subdirs(folder_format, track_attr, path)`, created directly, without folder states.
7. `cover.jpg` from `album.image.large`, unless in a playlist or `settings.no_cover`.
8. With `settings.embed_art`, an old `embed_cover.jpg` is removed and a new one fetched.
9. `_download_and_tag(dir, 1, …, multiple=False)`, so the temp file is `~tmp_01.tmp`.
10. `embed_cover.jpg` is deleted.
11. A failed track is not recorded. Otherwise recorded as `track`, with the folder as `saved_path`.

### Per track

`_download_and_tag(root_dir, tmp_count, …, multiple)`:

1. The extension comes from the requested format: `.mp3` for 5, else `.flac`.
2. The file name:
   - In a playlist: `sanitize_filename(clean_filename("{artist} - {track_title}".format(attrs)), "_")`.
   - Multi-disc with `multiple_disc_one_dir`: the multi-disc template, cleaned the same way.
   - Otherwise the track template, cleaned the same way, and for a multi-disc album prefixed with `<multiple_disc_prefix> <NN>/`, `NN` being the disc number with two digits.
3. A name over 180 characters, the disc folder included, keeps its first 110 characters right-stripped of ` ."-_'`, then `...`, then its last 60 left-stripped of the same set. Off with `disable_smart_truncation`.
4. The last component goes through `_fit_name` with the extension.
5. An existing final file logs "Skipping … (Already exists)" and counts as success. The check uses the requested extension, so an MP3 delivered by fallback is not found on the next run (kept).
6. Abort check, then a one-second sleep.
7. For a multi-disc album without `multiple_disc_one_dir`, the work folder becomes `<root>/<prefix> <NN>`, created when missing.
8. The temp file is `<work folder>/~tmp_<tmp_count:02>.tmp`.
9. The tiers.
10. The tagger writes the temp file and renames it to the final name. A tagging exception fails the track and leaves the temp file (kept upstream; the port removes it).
11. A sleep of `settings.delay` seconds when above 0.

Characters are counted in code points everywhere Python uses `len()` on a string.

### Name length

- `_is_name_too_long(parent, name)`: walk up from `parent` to the nearest existing folder, `stat` the name there, and answer whether the error is `ENAMETOOLONG`. Nothing is created.
- `_shorten_utf8(name, max_bytes)`: for `keep` from `len(name)-1` down to 0, head is the first `(keep+1)//2` characters right-stripped of ` ."-_'`, tail the last `keep//2` left-stripped of the same, and the first `head...tail` that fits in `max_bytes` UTF-8 bytes wins; else `...`.
- `_fit_name(parent, name, suffix, reserve)`: the name as it is when accepted. Otherwise shorten to `255 - reserve - len(suffix in bytes)` bytes, and 16 bytes less each round, until accepted or the budget is 64 bytes or less.
- `process_folder_format_with_subdirs` fits each part with `reserve` 14, the length of `[IN PROGRESS] `.

### Folder templates

`process_folder_format_with_subdirs(fmt, attrs, path)`:

1. Split `fmt` on `/` and skip empty parts.
2. Each part: `part.format(**attrs)`, then `sanitize_filepath(clean_filename(…), replacement_text="_")`. A `KeyError` uses the part's own text in place of the formatted one. Other exceptions propagate.
3. A part over 120 characters keeps its first 60 right-stripped, `...`, and its last 50 left-stripped. Off with `disable_smart_truncation`.
4. Empty parts are dropped.
5. With `path`, each part goes through `_fit_name`.
6. The parts are joined; with `path`, under it.

A playlist sets the folder template to `.`, which cleans to nothing, so its tracks land in the playlist folder itself.

### Choosing templates

`_determine_formats` tries, in order: the configured folder, track and multi-disc templates; the fallback folder with the configured track and multi-disc; the fallback folder with `DEFAULT_TRACK` and `DEFAULT_MULTIPLE_DISC_TRACK`; then all three defaults. Each folder and track template first loses a trailing `.mp3` or `.flac` and surrounding spaces. A combination is taken when the folder and every track name format without `KeyError` or `ValueError`. Since folder parts catch their own `KeyError`, only a `ValueError` there moves on. An exception of another type, such as the `TypeError` from a missing `media_number`, propagates out of the download. When nothing works, the defaults are used.

### Naming attributes

Templates the bot gets from `QobuzDL`'s defaults: folder `{artist} - {album} ({year}) [{bit_depth}B-{sampling_rate}kHz]`, track `{track_number} - {track_title}`. Constants: `DEFAULT_FOLDER` `{album_artist} - {album_title} ({year}) [{format} {bit_depth}]`, `DEFAULT_TRACK` `{track_number} - {track_title_base}`, `DEFAULT_MULTIPLE_DISC_TRACK` `{disc_number}.{track_number} - {track_title_base}`. `settings.fallback_folder_format` defaults to `DEFAULT_FOLDER`; the `Download` class falls back to `DEFAULT_FOLDER` and `DEFAULT_TRACK` when given empty templates.

Album attributes, `_get_album_attr(meta, album_title, file_format, bit_depth, sampling_rate)`:

| Key | Value |
| --- | --- |
| `artist` | `artist.name`, default `""` |
| `album`, `album_title` | `album_title` |
| `album_id`, `album_url`, `album_title_base` | `id`, `url`, `title`, default `""` |
| `album_artist`, `albumartist` | `get_album_artist` joined with `, ` |
| `album_genre`, `album_composer` | `genre.name`, `composer.name`, default `""` |
| `label` | `label.name` with whitespace runs collapsed, then each `;` or `/` with spaces around it, and each ` - `, replaced by ` ∕ ` (U+2215), stripped |
| `copyright`, `upc`, `barcode` | `copyright`, `upc`, `upc` |
| `release_date` | `release_date_original`, default `""` |
| `year` | `release_date_original` up to the first `-` |
| `media_type` | `product_type` through `str.capitalize` |
| `format`, `bit_depth`, `sampling_rate` | from the release gate |
| `quality_tag` | `MP3` for MP3, else `<format> <bit_depth>` when `bit_depth` is truthy, else `<format>` |
| `album_version` | `version`, default `""` |
| `version_tag` | ` - <version>` when truthy, else `""` |
| `disc_count`, `track_count` | `media_count`, `track_count`, default 1 each; Qobuz sends `tracks_count`, so `track_count` is usually 1 (kept) |
| `ExplicitFlag`, `explicit` | `[E]` when `parental_warning`, else `""` |
| `release_type` | `format_release_type(release_type)`: `Unknown` when empty, `EP` for `ep`, else `str.title` |

Single-track attributes, `_get_track_attr(meta, …)`, use the track's `album` for `album`, `album_title`, `album_title_base`, the artists, the explicit flag and the release type, and the track itself for everything else, with these differences (kept):

- `artist` is the album artist string, or `performer.name` when there is none.
- `album_id` and `album_url` are the track's `id` and `url`; `url` is usually missing, so `""`.
- `album_genre`, `album_composer`, `copyright`, `upc`, `media_type`, `album_version`, `version_tag` read the track, which mostly lacks them.
- `label` uses ` ／ ` (U+FF0F) as the separator.
- `disc_count` and `track_count` default to `""`.
- It adds `tracktitle`, `track_title`, `track_title_base`.

File attributes, `_get_filename_attr(track_artist, track, album)`:

| Key | Value |
| --- | --- |
| `artist`, `track_artist` | `performer.name` through `_safe_get`, may be `None` |
| `albumartist`, `album_artist` | `get_album_artist(album)` joined with `, `, or the track artist when empty |
| `tracktitle`, `track_title` | `downloader._get_title(track)` |
| `album_title`, `album_title_base` | `_get_title(album)`, `album.title` |
| `track_id`, `isrc`, `version`, `release_date` | the track's values, may be `None` |
| `track_composer` | `composer.name` through `_safe_get` |
| `track_number` | `f"{track_number:02}"`, default 0 |
| `disc_number` | `f"{media_number:02}"`; a missing `media_number` raises `TypeError` |
| `bit_depth`, `sampling_rate` | the track's `maximum_bit_depth`, `maximum_sampling_rate` |
| `year` | `release_date_original` up to the first `-`, default `""` |
| `ExplicitFlag`, `explicit` | `[E]` or `""` |

In folder names, `bit_depth` and `sampling_rate` come from the release gate and print with Python's `str()`: `24`, `96`, `44.1`, `96.0` from the segmented path, `None` after a failed gate.

### Sanitizing

`clean_filename(name, legacy_charmap)`:

1. NFC.
2. `(?:\s*([,\.\:\;\|/\\_])\s*){2,}` becomes `\1 `: a run of two or more separators becomes its last separator and a space.
3. Bracket pairs with only non-word characters inside are removed, for `()`, `[]`, `{}`, `<>`, `《》`, `〈〉`, `「」`, `『』`, `（）`, `［］`, `【】`, in that order.
4. A separator right after an opening bracket and before a word boundary is removed; so is one after a word boundary and right before a closing bracket.
5. `\s+` becomes one space; strip spaces, then dots, then spaces.
6. Without `legacy_charmap`, `/ \ : * ? " < > |` become `／ ＼ ： ＊ ？ ＂ ＜ ＞ ｜`. With it, `apply_legacy_charmap`.

`\w`, `\W`, `\s` and `\b` are Python's Unicode classes, and step 4 uses lookbehind and lookahead. The callers then apply pathvalidate 3.3.1:

- `sanitize_filename(name, replacement_text)`, for the universal platform, which removes or replaces invalid characters, appends `_` to reserved names such as `CON` or `NUL.txt`, strips trailing spaces and dots, and cuts to 255 bytes of UTF-8.
- `sanitize_filepath(path, replacement_text)` for folder parts, which also normalizes the path. It keeps a drive such as `C:` as it is, sanitizes each entry as a file name cut to 260 bytes, and validates the joined path: an entry left blank, such as `\u00a0` from `\u00a0.`, empties a relative path unless an earlier entry fails validation for another reason. Absolute paths skip that check.

Which wrapper and which replacement apply where:

| Name | Pipeline |
| --- | --- |
| folder parts | `sanitize_filepath(clean_filename(x), "_")` |
| track files | `sanitize_filename(clean_filename(x), "_")` |
| goodies | `sanitize_filename(clean_filename(x))`, empty replacement |
| tracklist | `sanitize_filename(album_title)`, empty replacement |
| playlist, artist and label folders | `sanitize_filename(name)`, empty replacement |

## Tags

`metadata.py`. `_get_tags_to_add(album, item, settings)` builds an ordered dict. It answers `{}` when either is empty. Each key is skipped when its `no_*_tag` setting is on. In insertion order:

| Tag | Value |
| --- | --- |
| `ALBUM` | `_get_title_with_version(album.title, album.version)`: `title (version)` unless the version is in the title, ignoring case |
| `TITLE` | the same for the track |
| `ALBUMARTIST` | `get_album_artist(album)`, a list |
| `ARTIST` | below |
| `COMPOSER` | below |
| `DATE` | `album.release_date_original`, default `""` |
| `GENRE` | below |
| `COPYRIGHT` | `album.copyright` with `(P)` as ℗ and `(C)` as ©; `n/a` only when the key is missing |
| `LABEL` | `label.name` with each whitespace run as one space, not stripped |
| `ISRC` | `item.isrc` |
| `BARCODE` | `album.upc` |
| `MEDIATYPE` | `album.product_type` in upper case |
| `ITUNESADVISORY` | `1` when `item.parental_warning`, else `""` |
| `COMPILATION` | `1` when the album artist is a various-artists alias; no setting turns it off |
| `RELEASETYPE` | `_release_types`: `[raw]` when `raw`, the lower-cased `release_type` or `product_type`, is `album`, `single` or `ep`; plus `compilation`, with `album` in front when the list is empty, when `raw` is `compilation` or the album artist is an alias |
| `REPLAYGAIN_TRACK_GAIN` | `audio_info.replaygain_track_gain` as `<str(value)> dB`, when not null |
| `REPLAYGAIN_TRACK_PEAK` | `str(audio_info.replaygain_track_peak)`, when not null |
| `WORK` | `item.work`, when truthy |
| `CONDUCTOR` | performers with role `Conductor`; one value or a list |
| `ENSEMBLE` | performers with role `Orchestra`, `Ensemble` or `Choir`; one value or a list |
| `QOBUZTRACKID` | `str(item.id)`, when truthy |
| `QOBUZALBUMID` | `str(album.id)`, when truthy |
| `QOBUZ ALBUM URL` | `https://www.qobuz.com/album/<slug>/<album id>`, when the album ID is truthy; `slug` is the lower-cased title with runs other than `a-z0-9` as `-`, stripped of `-` |

`performers` is one string: blocks split on ` - `, each block split on `, ` into a name and roles. Blocks with only a name are ignored.

ARTIST:

1. `main` is `item.performer.name`, else `album.artist.name`.
2. Start with `[main]` when it is non-empty.
3. For each block with role `MainArtist` or `FeaturedArtist`, add the name unless already present ignoring case. Skip a name when `main`, case-folded, starts with the name plus `,`: that is a band name like "Earth, Wind & Fire" cut at its comma.
4. When `main` is set and the list has more than one name: split `main` on ` & ` and `,`, case-fold the parts; when there are two or more and every part is among the other names, remove `main`.
5. One name is a string, several a list, none `""`.

COMPOSER: performers with role `Composer` or `ComposerLyricist`, de-duplicated ignoring case; when none, `item.composer.name`. One name a string, several a list.

GENRE:

1. `main` is `album.genre.name` through `LOCAL_GENRE_MAP`.
2. `genres_list` with its first entry replaced by `main`, or `[main]` when the list is empty. This changes the album dict in place (kept; it is the same on every call).
3. Join with ` → `, split on `→`, strip each piece, map through `LOCAL_GENRE_MAP`, drop repeats, join with `, `.

`LOCAL_GENRE_MAP` maps 33 French genre names to English; the source has the list.

Various-artists aliases default to `various artists, various, va, artistes divers, verschiedene interpreten, varios artistas, artisti vari, vari`, split on commas, stripped and case-folded; album artist names are stripped and case-folded before the lookup.

Album fields come from the full `album/get` for album downloads, and from the `album` inside `track/get` for single tracks and playlist tracks.

### FLAC

`tag_flac`:

1. Then, unless switched off: `TRACKNUMBER` `str(track_number)`, default `1`; `TRACKTOTAL` `_disc_track_total`; `DISCNUMBER` `str(media_number)`, default `1`; `DISCTOTAL` `str(album.media_count)`, default `1`.
2. For each key with a truthy value, `audio[key] = value`. mutagen removes earlier comments with that key, ignoring case, and appends the new ones at the end, one comment per list item. Comments already in the file under other keys keep their place in front. The save fails with ValueError when a value isn't a str, such as an int ISRC.
3. With `multi_value_tags`, a `GENRE` string containing `, ` is split into a list. Nothing else changes with the setting.
4. With the `em_image` argument, which is `QobuzDL.embed_art`, not `settings.embed_art`: a PICTURE block, type 3, `image/jpeg`, description `cover`, from `embed_cover.jpg` in the track's folder, else in its parent; missing is silent; over 16,777,215 bytes is logged and skipped.
5. `ENCODER`, `ENCODED-BY` and `ENCODED_BY` are removed, the vendor string emptied.
6. Saved with 8,192 bytes of padding. The blocks keep their order in the file; a VORBIS_COMMENT block mutagen adds to a file without one goes after them, then the new PICTURE; the old PADDING blocks go, and one PADDING block comes last. An ffmpeg file comes out as STREAMINFO, VORBIS_COMMENT, PICTURE, PADDING. mutagen writes STREAMINFO back as 34 bytes and a SEEKTABLE as whole seek points, and rewrites every VORBIS_COMMENT block as it loaded it: invalid UTF-8 as U+FFFD, a comment without `=` named `unknown<i>`, non-ASCII characters of a name as `?`, comments with other invalid names dropped. An ID3v2 tag before `fLaC` stays.
7. The temp file is renamed to the final name.

`_disc_track_total(album, item)`: when `album.media_count` is above 1 and `len(album.tracks.items)` equals `album.tracks_count`, the number of those tracks with the item's `media_number`, if not zero; otherwise `album.tracks_count`, default 1. Playlist and single tracks get the album from `track/get`, without a track list, so they get `tracks_count`.

### MP3

`tag_mp3`, ID3v2.3 through mutagen:

1. Read the existing ID3 tag, or start an empty one. mutagen translates what it reads to v2.4: TYER, TDAT and TIME become TDRC; TRDA, TSIZ, RVAD and EQUA go; numeric TCON genres are spelled out. The save doesn't translate back, and it drops unknown frames read from a v2.4 tag.
2. The tagger takes `DATE` out. When its first 4 characters are digits, it writes `TYER`; when characters 6 and 7 and characters 9 and 10 are digits too, `TDAT` as `DDMM`.
3. `RELEASETYPE` is taken out and added as `TXXX:MusicBrainz Album Type`, the list joined with `,`.
4. Every other truthy key is looked up in `ID3_LEGEND` by its lower-case name, then as it is: `ALBUMARTIST` TPE2, `ALBUM` TALB, `ARTIST` TPE1, `TITLE` TIT2, `MEDIATYPE` TMED, `GENRE` TCON, `COMPOSER` TCOM, `COPYRIGHT` TCOP, `LABEL` TPUB, `ISRC` TSRC, `COMPILATION` TCMP, `CONDUCTOR` TPE3, `WORK` TIT1; TXXX with the original key as its description for `ITUNESADVISORY`, `BARCODE`, `QOBUZTRACKID`, `QOBUZALBUMID`, `QOBUZ ALBUM URL`, `REPLAYGAIN_TRACK_GAIN`, `REPLAYGAIN_TRACK_PEAK`, `ENSEMBLE`.
5. `TRCK` is `<n>/<disc track total>` and `TPOS` `<d>/<media_count>`, each part following its setting.
6. With `em_image`: APIC with encoding 3, `image/jpeg`, type 3 and an empty description, from the same file as for FLAC. mutagen rewrites the encoding for v2.3 as it does for text frames; the goldens show the byte.
7. `TENC` and `TSSE` are removed.
8. Saved as v2.3. mutagen writes text frames as UTF-16 with a little-endian BOM, since v2.3 has no UTF-8, and keeps Latin-1 frames; ends each string with a terminator; joins list values with `/`; leaves out text frames with no text; orders frames with its own priority list; adds padding; and updates an ID3v1 tag if the file has one.

A frame's text goes through mutagen's `MultiSpec`: a str is split on NUL, each list item goes through `str()`, and any other value is a ValueError.

## URLs, collections and search

### get_url_info

`re.search(r"(?:https:\/\/(?:w{3}|open|play)\.qobuz\.com)?(?:\/[a-z]{2}-[a-z]{2})?\/(album|artist|track|playlist|label)(?:\/[-\w\d]+)?\/([\w\d]+)", url).groups()`. No match raises `AttributeError`, which `handle_url` treats as an invalid URL. `/interpreter/` links don't match. `\w` is Unicode.

### handle_url

1. Parse the URL; an invalid one is logged and ignored.
2. Albums and tracks go to `download_from_id(id, album)`.
3. Playlists, artists and labels load every page through `multi_meta`. No pages logs "Content empty or unavailable" and returns.
4. The folder is `<directory>/<sanitize_filename(pages[0].name)>`, created.
5. Artists with `filter_label`: items with a `label` key that doesn't match are dropped.
6. Artists with `smart_discography` go through `smart_discography_filter(pages, save_space=True, skip_extras=True)`. Otherwise the items of every page, in order.
7. Playlists set the folder template to `.` and `multiple_disc_one_dir` to true for the loop, and restore them after.
8. Each item, numbered from 1: `download_from_id(item.id, album = kind is not playlist, alt_path = folder, is_playlist, playlist_index)`.
9. Playlists, unless `no_m3u_for_playlists`: `make_m3u(folder, queued items)`.
10. Nothing here catches an exception: a failed page, a page without `name`, an item without `id`, a failure in `smart_discography_filter` or `make_m3u`, or one `download_from_id` lets through, ends the call, and the items after it are not downloaded. A playlist's item IDs are ints, an album's strings; both reach the database as text.

### download_from_id

1. When the database has the ID at the requested quality, log and return.
2. `Download(...).download_id_by_type(not album)`.
3. `requests.RequestException` and `NonStreamable` are logged. Every other exception escapes.

### make_m3u

1. Walk the folder, directories sorted, files in the order the OS lists them. Keep `.mp3` and `.flac`, by extension in lower case.
2. For each: the length is `int(info.length)` from `mutagen.File`, but only when the file has at least one tag, since `if audio_gen and audio_gen.info` tests the `FileType`, which is false without tags; an untagged file gets 0. FLAC reads `QOBUZTRACKID`, `ISRC`, `TITLE` and `ARTIST`, ignoring the case of the keys; a missing ID or ISRC is None, a missing title or artist "". MP3 reads `TXXX` frames whose description is `QOBUZTRACKID` in upper case, then `TSRC`, `TIT2`, `TPE1`. Each takes its first value. A read error, such as an MP3 without an ID3 tag or a file mutagen can't parse, keeps what was read before it and uses the file name without its extension as the title.
   - mutagen's MP3 length: past any ID3v2 tags, it searches the first MiB for syncs. At each, it reads up to four frames in a row; a layer III frame with a Xing or Info header at offset 36, 21 or 13 (by version and mode), or a VBRI header at 36, is enough on its own. The header's frame count times 1,152 samples (576 for MPEG 2 and 2.5), less a LAME tag's encoder delay and padding, over the rate gives the length. Without a frame count, the first frame's bitrate over the file size from that frame does. Two frames found at some sync are kept in case nothing better follows.
3. Index by track ID, by ISRC, and by stripped lower-case title; a later file wins a clash.
4. For each queued item in order: match by ID, then ISRC, then title, then the first file whose name contains the lower-case title. Duplicates are allowed. Misses are logged in a table. `str(item.get("isrc", ""))` makes a missing ISRC the text `None` when the key holds None. The title's `strip()` runs only when ID and ISRC miss, so a non-string title fails only then; an `album` or `performer` that isn't a dict always fails.
5. Without queued items, or when nothing matched, the files in natural order by name: the base name split at runs of Unicode decimal digits, the runs compared as numbers, the rest in lower case. A run of digits that aren't decimal, such as `²`, makes `int()` raise ValueError.
6. Lines: `#EXTM3U`, then per file `#EXTINF:<length>, <artist or "Unknown Artist"> - <title or "Unknown Title">` and the path relative to the folder. Joined with `\n`, no trailing newline. Written as `<folder name>.m3u8` in UTF-8, only when there is at least one file.

### smart_discography_filter

With `save_space` and `skip_extras` on, as `handle_url` calls it:

1. The requested artist is `pages[0].name`; the items are every page's albums.
2. Group by essence: leading parenthesized groups removed, then the text before the first `(`, stripped and lower-cased.
3. Per group, in first-seen order: the best bit depth is the highest `maximum_bit_depth`; the best rate the lowest `maximum_sampling_rate` among those.
4. Keep the first album with that depth and rate, `artist.name` equal to the requested artist, and that is a remaster when any album in the group is one, and is not an extra. Remaster is `(?i)(re)?master(ed)?` on `title version`; extra is `(?i)(anniversary|deluxe|live|collector|demo|expanded)`.

### search_by_type

1. A query under 3 characters logs and returns `None`.
2. Calls the search with the limit, 10 by default here.
3. Albums and tracks, per item: `<artist:20>   <title:35>   <type:8>   <year>   <quality>`. `artist` is `artist.name`, else `performer.name`, else `Unknown`. `title` adds ` (version)` and ` [E]`. `year` is the first 4 characters of `release_date_original`, else `release_date`, else four spaces. `type` is `release_type` or `product_type`; for albums without one, `Album` when the duration is at least 1740 seconds or there are 7 tracks or more, `Single` for 1 track, else `EP`, and only when either is non-zero; otherwise the item type. `ep` prints as `EP`, others through `str.title`. `quality` is `[HI-RES] <maximum_bit_depth, default 24>b/<maximum_sampling_rate, default 96.0>kHz` when `hires_streamable`, else `[ CD ] 16b/44.1kHz`.
4. Artists and playlists: `<name:50>   <albums_count> albums`, or `<tracks_count> tracks`.
5. Fields wider than their column are cut to width − 3 and get `...`; narrower ones are padded with spaces.
6. Each result is `{"text", "url"}` with the URL `https://play.qobuz.com/<type>/<id>`, or only the URL in lucky mode.

## Transfers

### Direct

`tqdm_download(url, file)`:

- Headers: `User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36`, `Accept: audio/webm,audio/ogg,audio/wav,audio/*;q=0.9,*/*;q=0.5`, `Connection: keep-alive`, and `Range: bytes=0-`, or `bytes=<N>-` with append mode once N bytes are saved.
- A fresh session per attempt, redirects followed, timeouts of 10 seconds to connect and 60 between reads, 64 KiB chunks.
- Up to 5 attempts.
- 416 returns as success, whatever the file holds.
- A status other than 200 or 206 raises `Status Server: <code>`.
- A 200 to a resumed request starts the file over (2.5.8).
- The expected total is set once: the bytes already saved plus `Content-Length`, default 0.
- After the body: more bytes than expected deletes the file and raises; at least the expected bytes returns as success; fewer goes straight to the next attempt, with no sleep, resuming from what is saved. The short case happens only when the server answers a range with a shorter body that matches its own `Content-Length`.
- A connection that drops before `Content-Length` is reached raises urllib3's `IncompleteRead` inside the read loop, so the attempt sleeps and retries like any other error. urllib3 drops the bytes of the unfinished 64 KiB chunk, so a body cut at byte 70,000 keeps 65,536 bytes and the next attempt asks for `bytes=65536-`.
- Any exception whose text contains `404` deletes the file and raises `HTTP 404: File not found on server.` at once. That includes `Status Server: 404`, and also an overshoot message or a connection error whose numbers happen to contain `404`.
- Other exceptions sleep 2, 4, 8 or 16 seconds and retry. After the fifth the file is deleted and the error raised.
- When the attempts run out on a short read, the file is deleted and `Incomplete download` raised.
- When a resumed attempt overshoots, the file is deleted but the saved count is not reset, so the next attempt appends to a new file from that offset (kept).

Covers and goodies use the same transfer.

### Segmented

`tqdm_download_segments(answer, file)`:

1. HEAD segments 0 through `n_segments`, inclusive, with 8 workers and a 5-second timeout, to sum the sizes for the progress bar. Errors count as 0.
2. GET segment 0, timeout 15 seconds, error on any HTTP error status. Its UUID is not known yet, so it is written as it is.
3. GET segment 1. The segment UUID is the usertype of its first top-level `uuid` box; none raises.
4. When `n_segments` is 2 or more, GET segments 2 through `n_segments` with 8 workers, written in order.
5. Every segment from 1 on is decrypted before writing to `<file>.mp4`.
6. `ffmpeg -nostdin -v error -y -i <file>.mp4 -c:a copy -f flac <file>`. A non-zero exit raises.
7. `<file>.mp4` is removed in all cases. No retries on this path.

Decryption, per segment: walk the top-level boxes while 8 header bytes remain; a size of 0 or one past the end stops the walk. In each `uuid` box whose usertype is the segment UUID:

1. `p = box + 28`. `data_end = box + u32(p)`; `p += 4`.
2. `counter_len = u8(p)`; `p += 1`. `frame_count = u24(p)`; `p += 3`.
3. Per frame: `frame_len = u32(p)`; `p += 6`; `flags = u16(p)`; `p += 2`. The frame is the bytes from `data_end` to `data_end + frame_len`, and `data_end` moves to its end. When `flags` is non-zero, the frame is AES-128-CTR decrypted with `raw_key` and an initial counter of the `counter_len` bytes at `p` padded with zeros to 16. `p += counter_len`.

The decryption runs in place over the whole segment buffer, so a frame may sit outside the `uuid` box, and a frame that overlaps the table changes the entries read after it. Reads past the end give fewer bytes, as Python's slices do: an entry past the end reads as zeros, a frame past the end is cut short. A `uuid` box that ends before its counter-length byte raises IndexError. A flagged frame whose counter is not 16 bytes after padding, because `counter_len` is above 16 or the table runs out, raises ValueError, as does a key that isn't 16, 24 or 32 bytes; unflagged frames check neither. Segment 0 goes through with no UUID and comes out unchanged.

`_get_qobuz_segment_uuid` walks while 24 bytes remain, not 8, and returns bytes 8 to 24 of the first box typed `uuid` even when the box is shorter than 24.

## Downloads database

`db.py`. One table, `downloads`:

```sql
CREATE TABLE downloads (
  "id" text NOT NULL,
  "media_type" text NOT NULL DEFAULT 'album',
  "quality" integer NOT NULL DEFAULT 27,
  "file_format" text NOT NULL DEFAULT 'FLAC',
  "quality_met" integer NOT NULL DEFAULT 0,
  "bit_depth" text,
  "sampling_rate" text,
  "saved_path" text NOT NULL DEFAULT '',
  "status" text NOT NULL DEFAULT 'downloaded',
  "url" text NOT NULL DEFAULT '',
  "release_date" text NOT NULL DEFAULT '',
  "artist" text NOT NULL DEFAULT '',
  "album" text NOT NULL DEFAULT '',
  PRIMARY KEY ("id", "quality")
);
```

- `create_db`: a missing table is created. A table without `quality` is the v1 layout: renamed, recreated, its IDs copied, the old one dropped. A table without `artist` gets `artist` and `album` added.
- `handle_download_id` without `add_id` selects by ID and quality, whatever the media type. With it, inserts; a duplicate logs "Already in the database".
- Values are bound as Python gives them: `bit_depth` 24 and `sampling_rate` 44.1 or 96.0 go into text columns, which SQLite stores as `'24'`, `'44.1'`, `'96.0'`; `None` as NULL; `quality_met` true as 1. SQLite 3.54 writes a float as its shortest round trip, as `str()` does, but with its own forms for large values: `1e+16` becomes `'10000000000000000.0'` and `1e+20` becomes `'1.0e+20'`. No sampling rate is that large.
- Albums are recorded only when no track failed, with the gate's format, `quality_met`, bit depth and rate, the final folder, `url`, `release_date_original`, the album artists joined with `, ` and the album title with its version.
- Tracks are recorded as `track`, with the track folder, the album's `url`, the track's `release_date_original`, `track_attr.artist` and `track_attr.album`.
- Playlists, artists and labels are never recorded; their tracks and albums are.

## Settings

`QobuzDL.__init__` defaults: directory `QobuzDownloads`, quality 6, `embed_art` off, `ignore_singles_eps` off, `no_m3u_for_playlists` off, `quality_fallback` on, `downloads_db` none, folder and track templates above, `smart_discography` off, `fetch_lyrics` off, `force_english` on, `no_credits` off.

`QobuzDLSettings` defaults that the ported code reads: `fallback_folder_format` `DEFAULT_FOLDER`, `legacy_charmap` off, every `no_*_tag` off, `multi_value_tags` off, `disable_smart_truncation` off, the various-artists aliases above, `embed_art` off, `no_cover` off, `embedded_art_size` `600`, `saved_art_size` `org`, `multiple_disc_prefix` `CD`, `multiple_disc_one_dir` off, `multiple_disc_track_format` `DEFAULT_MULTIPLE_DISC_TRACK`, `max_workers` 3, `user_auth_token` empty, `delay` 0.

`settings.no_fallback` holds the opposite of its name and nothing reads it. `cover_og_quality` and the `no_cover` argument of `QobuzDL` are passed along and never read.

The Python CLI sets both `embed_art` flags from `-e`, with `embed_art` on by default in `config.ini`. The bot builds `QobuzDL` directly and gets the defaults above.

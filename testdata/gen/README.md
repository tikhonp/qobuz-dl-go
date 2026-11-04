# Golden generation

The scripts here run qobuz-dl 2.5.9 to make the test data. Go tests read only the committed output; CI never runs Python.

| Script | Does | Network |
| --- | --- | --- |
| `gen.py` | reads `testdata/fixtures/` and writes `testdata/golden/*.json`, `testdata/fixtures/strings/random.txt`, the bundle, built audio, segment, m3u8 and database fixtures, and the copy of the fixtures in `qobuzdltest/testdata/` | none |
| `record.py` | records the Qobuz API answers into `testdata/fixtures/api/`; `--search` lists candidate IDs | Qobuz, needs `QOBUZ_TOKEN` |

`parity.py` and `compare.py` come with step 10.

## The venv

Python 3.14, the version the production bot runs. `requirements.txt` is `pip freeze` of the venv. The goldens depend on the versions of qobuz-dl, pathvalidate and mutagen, so the pins change only with an upstream sync.

```sh
/opt/homebrew/opt/python@3.14/bin/python3.14 -m venv testdata/gen/.venv
testdata/gen/.venv/bin/pip install -r testdata/gen/requirements.txt
```

`make venv` does both. Run the scripts with `-I`, as the Makefile does, so the current folder and user site-packages stay out of Python's import path.

## Goldens

`make golden` runs `gen.py`. Running it twice gives the same files byte for byte, and a diff after a run is either a bug or an upstream change.

Each golden is `{"generator", "cases": [{"name", "input", "output"} or {"name", "input", "error"}]}`, where `error` is the Python exception class name. JSON can't tell `96` from `96.0`, so `gen.py` tags every int, float and None: `{"py": "int", "v": 96}`, `{"py": "float", "v": 96.0}`, `{"py": "float", "v": "inf"}`, `{"py": "none"}`. Strings, booleans, lists and objects are plain JSON. `golden_test.go` loads them.

| File | Cases |
| --- | --- |
| `pystr.json` | `str()` of ints, floats around the fixed and scientific switch, negative zero, inf, nan, None, booleans |
| `format.json` | `str.format`: the attribute dicts upstream's `_get_album_attr`, `_get_track_attr` and `_get_filename_attr` build from the recorded answers, against every key at once, the default and bot templates and `templates/templates.json`; every spec and conversion in that file against a set of values of each type; malformed templates |
| `strings.json` | `lower`, `upper`, `casefold`, `title`, `capitalize`, `strip()`, `strip(' ."-_\'')` and its `lstrip` and `rstrip`, `isdigit`, `len`, NFC over the corpus |
| `textwrap.json` | `textwrap.fill(width=70)` over the recorded descriptions, raw and split into paragraphs as the tracklist does, hand-picked wrapping cases and the corpus |
| `regex.json` | the code points Python's `\w`, `\s` and `\d` match, as hex ranges |
| `unicode.json` | Python's Unicode version; the assigned, `isdigit`, `isdecimal`, `isspace`, `isprintable`, Cased and Case_Ignorable code points as hex ranges; every character that `lower`, `upper`, `title` or `casefold` changes, with its mapping |
| `clean_filename.json` | `clean_filename` with and without `legacy_charmap` over the corpus, the recorded titles and names, and folder names formatted from the recorded albums |
| `pathvalidate.json` | `sanitize_filename` and `sanitize_filepath` with replacement `""` and `"_"` over the corpus, reserved names, long names up to 1,000 bytes, cleaned recorded titles and paths |
| `url_info.json` | `get_url_info` over `strings/urls.txt`: the groups, or `AttributeError` when nothing matches |
| `normalize_json.json` | `Client._normalize_json_strings` on built values, every recorded string it changes, and one whole album answer |
| `name_length.json` | `downloader._shorten_utf8` over the corpus and long names at several byte limits |
| `tags.json` | `metadata._get_tags_to_add` on every recorded track with its album, on built cases for each tag's rules and odd value types, and with each `no_*_tag` setting on |
| `metadata_helpers.json` | `_get_title_with_version`, `_get_title`, `_format_copyright` and `_format_genres` |
| `release_types.json` | `_release_types` and `_is_various_artists` on the recorded albums and on every mix of release type, product type and album artist |
| `disc_track_total.json` | `_disc_track_total` on every recorded track and on built albums |
| `flac_tagged.json` | `tag_flac` on copies of the FLAC fixtures: the blocks, the comments, the pictures and hashes of the audio and of the whole file |
| `mp3_tagged.json` | `tag_mp3` on copies of the MP3 fixtures: the frames as mutagen reads them, a hash of each raw frame and a hash of the audio |
| `naming.json` | the folder and file names of each recorded album and the single track, through `_get_album_attr` or `_get_track_attr`, `_determine_formats`, `process_folder_format_with_subdirs` and `_download_and_tag` up to its `_fit_name` call, over gates at 27, 6 and 5, a failed gate and a float rate, and a matrix of templates and settings |
| `tracklist.json` | `_generate_tracklist` on the recorded albums and on built ones with discs, nulls, missing keys, odd types and descriptions |
| `segments.json` | `_get_qobuz_segment_uuid` and `_decrypt_qobuz_segment` on every segment the fake serves, as a size and a SHA-256, and on built buffers for the flags, offsets, counters, keys and broken boxes |
| `layout_album.json`, `layout_track.json` | whole downloads by `Download.download_id_by_type`, with a client that answers as `qobuzdltest` does and a localhost server for the CDN and the static files: every folder and file, with the FLAC and MP3 results above, the tracklist's text, or a SHA-256 |
| `layout_playlist.json`, `layout_artist.json` | `QobuzDL.handle_url` on the recorded playlist and on `qobuzdltest.ArtistAlbums`, with a downloads database: the tree as above, the m3u8's text, and the database's rows with `saved_path` relative to the download folder. The client also answers `track/get` for the playlist's tracks and the artist's built page as the fake does. Each folder's files are listed by name, as the port lists them |
| `m3u.json` | `make_m3u` on folders of files from `testdata/fixtures/m3u/`, with and without playlist items: the m3u8's text, or none |
| `search_text.json` | `search_by_type` on the recorded searches and on built items for each column's rules: the lines and URLs |
| `db_rows.json` | `create_db`, then `handle_download_id` adds and lookups on a new database: the schema, the columns, the rows and the lookups' answers; and the rows of `testdata/fixtures/db/shared.db` |
| `db_migrations.json` | `create_db` on copies of `v1.db`, `v2.db` and `shared.db`, and on no file: the schema, the columns and the rows after |

## Segments

`gen.py` cuts each fragmented MP4 into what the fake's `/cdn/seg/` serves, in `testdata/fixtures/segments/<format>/<n>.m4s`: segment 0 is `ftyp` and `moov`, and each later one a `uuid` box, a `moof` and an `mdat`. The `uuid` box holds the frame table `_decrypt_qobuz_segment` reads; the frames cover the `mdat` payload in sizes of 1,000, 250, 640, 0 and 77 bytes in turn, with flags 1, 0, 0x0100, 1 and 0. Flagged frames are encrypted with AES-CTR under a fixed `raw_key`, with 8-byte counters at formats 6 and 7 and 16-byte ones at 27, whose first frame carries over the low 64 bits. From segment 2 on, format 7 has a decoy `uuid` box with another usertype in front. `gen.py` checks that upstream's `_decrypt_qobuz_segment` gives each one back and that `_get_qobuz_segment_uuid` finds the usertype in segment 1.

`index.json` holds the session ID and `infos` the fake's `session/start` answers, the key `file/url` answers, which is `raw_key` wrapped under the session key that `qobuzdltest.Secret` and the `infos` derive, `raw_key` itself for tests, and each format's `n_segments`.

## Corpora

`testdata/fixtures/strings/*.txt` hold one input per line. `\\` is a backslash and `\uXXXX` or `\UXXXXXXXX` a code point; any other escape stops `gen.py`. Lines starting with `#` are comments, so an input starting with `#` is written `#`, and blank lines are skipped. `gen.py` adds the empty string itself.

- `handpicked.txt`: the cases `clean_filename`, the string methods and pathvalidate are known to trip on.
- `reserved.txt`: the names pathvalidate reserves, in several cases, with extensions and inside longer names.
- `urls.txt`: URL forms for `get_url_info`.
- `random.txt`: 500 strings from a fixed seed, drawn from ASCII, Latin-1, Cyrillic, CJK, Arabic, combining marks, emoji, the separators and brackets `clean_filename` touches, control characters and unusual whitespace. `gen.py` rewrites it on every run and checks that it reads back as written.

`testdata/fixtures/templates/templates.json` holds the templates, values, specs, conversions and malformed templates for `format.json`.

## API fixtures

`make record` runs `record.py` with `QOBUZ_TOKEN` from the environment. It goes through upstream's own `Bundle` and `Client` and saves each raw response body, before `_normalize_json_strings`, because the Go client normalizes the answers itself. `index.json` lists every request with its endpoint, parameters, status and file.

The repo is public, so `record.py` cleans the answers:

- `url` and `url_template` in `track/getFileUrl` answers become `{{CDN}}/file/<track id>/<requested format id>`, and the fake Qobuz rewrites `{{CDN}}` at serve time.
- The signed `blob` in those answers becomes `{{BLOB}}`. Upstream never reads it.
- It never saves `user/get`.
- A file that still contains the token or an app secret stops the run before it is written.

`gen.py` copies `api/`, `bundle/` and the audio files the fake serves into `qobuzdltest/testdata/`, which the package embeds, so it works from another module's cache. Edit the originals; the copy is replaced on every run.

The IDs are in `ITEMS` in `record.py` and in `docs/status.md` under Fixture items. To choose new ones, run `record.py --search`, which prints candidates and saves nothing.

## Audio and image fixtures

ffmpeg 9.0.2 made the files in `testdata/fixtures/audio/` once, with these commands:

```sh
cd testdata/fixtures/audio
sine() { echo "sine=frequency=440:sample_rate=$1:duration=0.1"; }
exact="-fflags +bitexact -flags:a +bitexact -map_metadata -1"

ffmpeg -y -f lavfi -i "$(sine 44100)" -ac 2 -c:a flac -sample_fmt s16 $exact 16-44.1.flac
ffmpeg -y -f lavfi -i "$(sine 96000)" -ac 2 -c:a flac -sample_fmt s32 -bits_per_raw_sample 24 $exact 24-96.flac
ffmpeg -y -f lavfi -i "$(sine 192000)" -ac 2 -c:a flac -sample_fmt s32 -bits_per_raw_sample 24 $exact 24-192.flac

# Vorbis comments a tagger has to replace; ffmpeg writes its own ENCODER.
ffmpeg -y -f lavfi -i "$(sine 44100)" -ac 2 -c:a flac -sample_fmt s16 \
  -metadata TITLE="Old Title" -metadata ARTIST="Old Artist" -metadata COMMENT="kept?" 16-44.1-tagged.flac

# ID3v2.3 with TIT2 and TSSE, the frame the MP3 tagger keeps.
ffmpeg -y -f lavfi -i "$(sine 44100)" -ac 2 -c:a libmp3lame -b:a 320k -id3v2_version 3 \
  -metadata title="Old Title" 320.mp3

ffmpeg -y -f lavfi -i "color=c=red:s=1x1" -frames:v 1 -update 1 -fflags +bitexact -flags +bitexact cover.jpg

# Fragmented MP4s, 30 ms a fragment, for the segmented path. default_base_moof
# keeps each fragment's offsets valid when gen.py puts boxes in front of it.
for f in 16-44.1 24-96 24-192; do
  ffmpeg -y -i $f.flac -c:a copy $exact -movflags +empty_moov+default_base_moof -frag_duration 30000 -f mp4 $f.mp4
done
```

This script, run in the same folder, writes `booklet.pdf`, a one-page PDF with no content:

```sh
../../gen/.venv/bin/python -I - <<'EOF'
objs = [b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 72 72] >>"]
out, offsets = bytearray(b"%PDF-1.4\n"), []
for i, o in enumerate(objs, 1):
    offsets.append(len(out))
    out += b"%d 0 obj\n" % i + o + b"\nendobj\n"
xref = len(out)
out += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objs) + 1)
out += b"".join(b"%010d 00000 n \n" % off for off in offsets)
out += b"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (len(objs) + 1, xref)
open("booklet.pdf", "wb").write(out)
EOF
```

`gen.py` writes five more on every run, built from the ones above, so the taggers meet layouts ffmpeg doesn't write:

- `blocks.flac`: two PADDING blocks, an APPLICATION block, a SEEKTABLE with a cut seek point, a PICTURE, and two VORBIS_COMMENT blocks, the first with comments mutagen rewrites on load: invalid UTF-8, no `=`, an empty or invalid name, `encoder` in lower case.
- `no-comments.flac`: STREAMINFO and PADDING only.
- `id3-prefixed.flac`: `16-44.1-tagged.flac` behind an ID3v2.4 tag.
- `320-bare.mp3`: `320.mp3` without its tag.
- `320-tagged.mp3`: `320-bare.mp3` with an ID3v2.3 tag from mutagen: TENC, TSSE, a stale `TXXX:QOBUZTRACKID`, other TXXX, COMM and APIC frames that tagging keeps, in UTF-16 and Latin-1.

## m3u8 fixtures

`gen.py` writes `testdata/fixtures/m3u/` on every run: FLAC and MP3 files tagged by mutagen as `make_m3u` reads them, IDs, ISRCs, titles and artists in upper and lower case, several values, UTF-16 and v2.4 frames. Their lengths are whole seconds, from patched headers: a FLAC STREAMINFO sample count; in `320-bare.mp3`'s first frame, the Info header's frame count, a LAME tag with encoder delay and padding, a VBRI header, or no frame count at 32 kbps so the size gives the length. Some files are broken on purpose: no tag, junk before the first frame, not audio at all, empty. The `m3u.json` cases copy them into folders under other names.

## Database fixtures

`gen.py` writes `testdata/fixtures/db/` on every run. `v1.db` is the first qobuz-dl's table, `id TEXT UNIQUE NOT NULL`, with three IDs. `v2.db` is db.py's table before `artist` and `album`, with two rows. `shared.db` is a database `create_db` made and `handle_download_id` filled with two albums and a track, for the test that Go reads and writes Python's file.

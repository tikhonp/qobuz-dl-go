# Golden generation

The scripts here run qobuz-dl 2.5.9 to make the test data. Go tests read only the committed output; CI never runs Python.

| Script | Does | Network |
| --- | --- | --- |
| `gen.py` | reads `testdata/fixtures/` and writes `testdata/golden/*.json`, `testdata/fixtures/strings/random.txt`, the bundle and built audio fixtures, and the copy of the fixtures in `qobuzdltest/testdata/` | none |
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

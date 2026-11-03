# 5: tags

## Read first

- [design.md](../design.md) section Tagging.
- [port-spec.md](../port-spec.md) section Tags.
- Upstream `metadata.py`; mutagen 1.48.1's `flac.py` (`VCFLACDict`, `save`, padding) and `_vorbis.py` (`__setitem__`) in the venv.
- `go-flac`, `flacvorbis`, `flacpicture` and `bogem/id3v2/v2` sources.
- The As built sections of steps 1 to 3.

## Done when

`flac.go` and `metadata.go` exist; `getTagsToAdd`, `TagFLAC` and `TagMP3` match their goldens; the audio frames of a tagged file equal those of the input.

## Decisions

| Topic | Decision | Why |
| --- | --- | --- |
| FLAC writer | Own block reader and writer in `flac.go`; `flacvorbis` and `flacpicture` encode the blocks. `flac.go` writes the new file beside the temp file and renames it over the temp file. | `go-flac` loads all audio into memory; the user wants libraries with workarounds only where needed. |
| FLAC order | STREAMINFO, other blocks in order, VORBIS_COMMENT, PICTURE, one 8,192-byte PADDING last; old PADDING blocks dropped. A key set again loses its old comments, ignoring case, and goes to the end. | mutagen's output, confirmed by spike. |
| MP3 writer | `id3v2` with version 3, `EncodingUTF16` for text frames and descriptions; lists joined with `/` before writing. Frame order and padding left to the library. | Order and padding don't show; the user wants the library. |
| Golden for MP3 | Frames compared as a set of ID, description, encoding and text. | Frame order varies with `id3v2`. |
| Cover embedding | Ported with `Options.EmbedArt` as `em_image`. Production runs with it off, so it gets a golden and a test but no parity run. | Faithful, cheap. |
| Tag list type | `[]tag{key string; values []string}` from `getTagsToAdd`'s dict, keeping insertion order and the single-or-list shape for the golden. | The golden compares the dict as Python builds it. |
| Errors | Tagging errors fail the track; `embedFLACImg` errors are logged as in Python. | Upstream. |

## Files

```
flac.go  metadata.go
flac_test.go  metadata_test.go
testdata/golden/tags.json  flac_tagged.json  mp3_tagged.json  disc_track_total.json  release_types.json   (gen.py extended)
```

## Go signatures

```go
func getTitleWithVersion(title, version string) string
func metadataGetTitle(track *pyDict) (string, error)
func isVariousArtists(album *pyDict, aliases []string) (bool, error)
func releaseTypes(album *pyDict, aliases []string) ([]string, error)
func discTrackTotal(album, item *pyDict) (string, error)
func formatCopyright(s string) string
func formatGenres(genres []string) string
func getTagsToAdd(album, item *pyDict, s *Settings) ([]tag, error)
func embedFLACImg(rootDir string, blocks *flacBlocks, log *slog.Logger)
func embedID3Img(rootDir string, t *id3v2.Tag) error
func TagFLAC(filename, rootDir, finalName string, d, album *pyDict, isTrack, emImage bool, s *Settings) error
func TagMP3(filename, rootDir, finalName string, d, album *pyDict, isTrack, emImage bool, s *Settings) error

type flacBlocks struct{ /* STREAMINFO and the other blocks, audio offset */ }
func readFLACBlocks(path string) (*flacBlocks, error)
func (b *flacBlocks) comments() (vendor string, list []string)
func (b *flacBlocks) setComments(vendor string, list []string)
func (b *flacBlocks) addPicture(p *flacpicture.MetadataBlockPicture) error
func (b *flacBlocks) writeTo(src, dst string, padding int) error
func flacStreamInfo(path string) (bitDepth int, sampleRate int, samples int64, err error)
```

`TagFLAC` and `TagMP3` are exported because Python's are public; they take the internal dicts, so they stay usable only inside the package until a later version gives them typed arguments.

## Checklist

| Edge case | Test |
| --- | --- |
| The tag map for albums, tracks, compilations, multi-disc and playlist tracks, with each `no_*_tag` off and a few on | `TestTagsGolden` |
| ARTIST: featured artists added, "A & B" dropped when both are listed, "Earth, Wind & Fire" not cut | `TestArtistTagGolden` |
| COMPOSER from performers, else `composer.name` | `TestComposerTagGolden` |
| GENRE: main genre replaces the first entry, French names mapped, repeats dropped | `TestGenreTagGolden` |
| COMPILATION only for a various-artists alias; RELEASETYPE with `compilation` | `TestReleaseTypesGolden` |
| TRACKTOTAL per disc only with a complete track list | `TestDiscTrackTotalGolden` |
| COPYRIGHT `n/a` only when the key is missing | `TestCopyrightMissingAgainstNull` |
| ReplayGain gain printed as Python prints the number | `TestReplayGainFormatting` |
| `multi_value_tags` splits only GENRE | `TestMultiValueTagsOnlyGenre` |
| FLAC comments after tagging, in order, on a file with existing comments and `ENCODER` | `TestTagFLACGolden` |
| FLAC block order and an 8,192-byte padding block last | `TestTagFLACBlockLayout` |
| The vendor string is empty | `TestTagFLACEmptiesVendor` |
| Audio frames unchanged byte for byte | `TestTagFLACKeepsAudio`, `TestTagMP3KeepsAudio` |
| PICTURE from the folder, then the parent; over 16,777,215 bytes skipped | `TestEmbedFLACImg` |
| MP3 frames as a set: TYER and TDAT, TRCK and TPOS totals, TXXX descriptions, `MusicBrainz Album Type`, UTF-16 | `TestTagMP3Golden` |
| TENC and TSSE removed | `TestTagMP3RemovesEncoderFrames` |
| The temp file is renamed to the final name | `TestTagRenamesToFinal` |
| Tagging a 100 MB file keeps the heap under 32 MB | `TestTagFLACStreamsAudio` |

## As built

Done on 2026-11-03. `make check` and `make lint` pass: 0 golangci-lint issues, no vulnerabilities.

### Differences from the contract

- `getTagsToAdd(log, album any, item *pyDict, s *Settings) (*pyDict, error)`. The tags are a `*pyDict`, the ordered dict Python builds, not `[]tag`. Values keep their Python types, None and ints included. `tag_mp3` pops `DATE` and `RELEASETYPE` from it, so `pyDict` gained `pop`. `album` is `any` because `tag_flac` passes `d.get("album", {})`, which can be None.
- The helpers take Python values: `getTitleWithVersion(title, version any) (any, error)`, `metadataGetTitle(*pyDict) (any, error)`, `formatCopyright(any) (any, error)`, `formatGenres([]any) (string, error)` and `discTrackTotal(album any, item *pyDict)`. `releaseTypes` and `isVariousArtists` take a logger, since `get_album_artist` logs.
- `TagFLAC`, `TagMP3`, `embedFLACImg` and `embedID3Img` take a `*slog.Logger` first.
- The contract's FLAC order holds for ffmpeg's files only. mutagen keeps the blocks in file order: an existing VORBIS_COMMENT block stays where it is, one added to a file without it goes after the others, new PICTURE blocks after that, and the old PADDING blocks give way to one of 8,192 bytes at the end. mutagen also writes STREAMINFO back as 34 bytes and SEEKTABLE as whole seek points, and rewrites every VORBIS_COMMENT block with its load rules: invalid UTF-8 as U+FFFD, `unknown<i>` for a comment without `=`, `?` for non-ASCII characters of a name, invalid names dropped. `flac.go` does the same; `pyDecodeUTF8` ports Python's `replace` decoding. design.md and port-spec.md now say so.
- The FLAC output is byte for byte mutagen's. `TestTagFLACGolden` compares the whole file's SHA-256, besides the blocks, comments, pictures and audio.
- `id3v2` v2.1.4's UTF-16 writer is wrong. It uses a big-endian BOM and writes an extra zero byte after text that doesn't end in one, so the value of a TXXX frame and the data of an APIC frame start one byte late. mutagen read a TXXX value back as `'\ufe00ÿ12345'` and the cover with a leading zero byte. `metadata.go` has its own `Framer` types, `id3Text`, `id3UserText`, `id3LangText` for COMM and USLT, and `id3Picture`, which write what mutagen writes for v2.3. `adoptID3Frames` converts the frames `id3v2` read from the file to them, keeping Latin-1 and turning other encodings into UTF-16, as mutagen does. Every frame's bytes equal mutagen's; `TestTagMP3Golden` compares a hash of each raw frame besides the decoded set. Frame order and padding still differ. design.md now says so.
- `dropEmptyID3Frames` leaves out text frames whose text is empty, as mutagen's `save_frame` does, and `id3TextValues` ports `MultiSpec`'s validation: a str splits on NUL, list items go through `str()`, any other value is a ValueError.
- `id3v2` keys APIC frames by type and description; `id3Picture` keys them by description, as mutagen's `HashKey` does.
- A new golden, `metadata_helpers.json`, covers `_get_title_with_version`, `_get_title`, `_format_copyright` and `_format_genres`. `release_types.json` also records `_is_various_artists`. Built cases in `tags.json` cover each tag's rules and odd value types; the fixture cases keep only the album keys the function reads.
- `gen.py` writes five audio fixtures: `blocks.flac`, `no-comments.flac`, `id3-prefixed.flac`, `320-bare.mp3` and `320-tagged.mp3`. The README lists what each holds.
- mutagen reads a v2.3 APIC frame whose data is all zero bytes as empty, since `EncodedTextSpec` drops an all-zero remainder as padding. The oversized covers in the goldens are `0xff` bytes for that reason.
- Extra tests: `TestMetadataHelpersGolden`, `TestTagFailureKeepsTheFile`, `TestEmbedCoverLogs`, `TestReadFLACBlocksRefusesOtherFiles`, `TestFLACStreamInfo`, `TestParseVorbisCommentLikeMutagen`, `TestPyDecodeUTF8`.
- UPSTREAM.md lists four new differences: existing MP3 frames are not translated to v2.4 and an ID3v1 tag isn't updated; ID3v2.2 tags fail; PICTURE and CUESHEET blocks are kept as read and block sizes trusted; FLAC tagging writes a new file and renames it.

### Checks

`make golden` twice gives the same files, the new fixtures included. A throwaway differential run, not committed: 6,000 random mutations of the recorded and built albums and tracks, with random settings, through `_get_tags_to_add` and `_disc_track_total`. No differences.

### Notes for later steps

- Step 7: `downloadAndTag` calls `TagFLAC(log, ...)` or `TagMP3(log, ...)` and treats any error as Python's `Error tagging`, which fails the track. For a single track Python passes the track as both `d` and `album`; with `isTrack` the album argument is unused and the track's own `album` is read. A failed tag leaves the temp file untouched; the downloader removes it.
- Step 7: `TagMP3` on a FLAC prepends an ID3 tag, as mutagen does, so the delivered format must pick the tagger.
- Step 9: `flacStreamInfo` gives the bit depth, the rate and the sample count for `make_m3u`'s `int(samples / rate)`, and `readFLACBlocks(path)` then `comments()` gives the comments as `KEY=value`. For MP3, `id3v2.Open` with `Parse: true` reads the frames this port writes correctly, since its reader handles little-endian BOMs.
- Step 10: `compare.py` reads tags with mutagen; the frames this port writes read back the same as Python's.

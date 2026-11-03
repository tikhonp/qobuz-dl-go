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

Filled in at the end of the step.

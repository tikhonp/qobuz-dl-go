// FLAC metadata blocks as mutagen 1.48.1 reads and saves them for tag_flac in
// qobuz_dl/metadata.py from qobuz-dl 2.5.9. It ports no qobuz-dl file. go-flac
// reads every audio frame into memory, so the block I/O is our own and the
// audio is copied with io.Copy; flacvorbis and flacpicture encode the blocks.

package qobuzdl

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-flac/flacpicture"
	"github.com/go-flac/flacvorbis"
	goflac "github.com/go-flac/go-flac"
)

// flacMaxBlockSize is the largest body a block header can state, mutagen's
// MetadataBlock._MAX_SIZE and upstream's FLAC_MAX_BLOCKSIZE.
const flacMaxBlockSize = 1<<24 - 1

// errFLACNoHeader is mutagen's FLACNoHeaderError.
var errFLACNoHeader = errors.New("not a valid FLAC file")

// vorbisComment is one comment of a VORBIS_COMMENT block. value is a string
// when read from a file; tag_flac can set anything, and saving refuses what
// isn't a string, as mutagen's validate does.
type vorbisComment struct {
	key   string
	value any
}

// flacBlock is one metadata block. VORBIS_COMMENT blocks are kept parsed and
// written back from vendor and comments; the rest keep their body.
type flacBlock struct {
	code     goflac.BlockType
	body     []byte
	vendor   string
	comments []vorbisComment
}

// flacBlocks is a FLAC file's metadata, mutagen's FLAC.metadata_blocks.
type flacBlocks struct {
	start  int64 // offset of "fLaC"; an ID3 tag before it stays
	audio  int64 // offset of the first audio frame
	blocks []*flacBlock
	tags   *flacBlock // the first VORBIS_COMMENT block, mutagen's FLAC.tags
}

// readFLACBlocks reads the metadata of a FLAC file and stops where the audio
// starts.
func readFLACBlocks(path string) (*flacBlocks, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(f)
	b := &flacBlocks{}
	if err := b.readHeader(f, r, path); err != nil {
		return nil, err
	}
	pos := b.start + 4
	var seektables, cuesheets int
	for {
		var h [4]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return nil, fmt.Errorf("%s: metadata block header: %w", path, err)
		}
		code := goflac.BlockType(h[0] & 0x7f)
		size := int(h[1])<<16 | int(h[2])<<8 | int(h[3])
		body := make([]byte, size)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, fmt.Errorf("%s: metadata block of %d bytes: %w", path, size, err)
		}
		pos += 4 + int64(size)
		blk, err := parseFLACBlock(code, body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		switch code {
		case goflac.VorbisComment:
			if b.tags == nil {
				b.tags = blk
			}
		case goflac.SeekTable:
			seektables++
		case goflac.CueSheet:
			cuesheets++
		}
		if seektables > 1 || cuesheets > 1 {
			return nil, fmt.Errorf("%s: more than one %v block", path, code)
		}
		b.blocks = append(b.blocks, blk)
		if h[0]&0x80 != 0 {
			break
		}
	}
	b.audio = pos
	if b.streamInfo() == nil {
		return nil, fmt.Errorf("%s: stream info block not found", path)
	}
	return b, nil
}

// readHeader finds "fLaC", skipping an ID3v2 tag before it as mutagen does.
func (b *flacBlocks) readHeader(f *os.File, r *bufio.Reader, path string) error {
	notFLAC := fmt.Errorf("%w: %s", errFLACNoHeader, path)
	var head [10]byte
	if _, err := io.ReadFull(r, head[:4]); err != nil {
		return notFLAC
	}
	if string(head[:4]) == "fLaC" {
		return nil
	}
	if string(head[:3]) != "ID3" {
		return notFLAC
	}
	if _, err := io.ReadFull(r, head[4:]); err != nil {
		return notFLAC
	}
	b.start = 10 + int64(syncsafeInt(head[6:10]))
	if _, err := f.Seek(b.start, io.SeekStart); err != nil {
		return err
	}
	r.Reset(f)
	if _, err := io.ReadFull(r, head[:4]); err != nil || string(head[:4]) != "fLaC" {
		return notFLAC
	}
	return nil
}

// syncsafeInt is mutagen's BitPaddedInt: seven bits from each byte.
func syncsafeInt(b []byte) int {
	n := 0
	for _, c := range b {
		n = n<<7 | int(c&0x7f)
	}
	return n
}

// parseFLACBlock applies what mutagen's block classes do on load and write:
// STREAMINFO keeps its 34 bytes, SEEKTABLE its whole seek points, and
// VORBIS_COMMENT is parsed. Other blocks are written back as they are.
func parseFLACBlock(code goflac.BlockType, body []byte) (*flacBlock, error) {
	blk := &flacBlock{code: code, body: body}
	switch code {
	case goflac.StreamInfo:
		if len(body) < 34 {
			return nil, fmt.Errorf("stream info block of %d bytes", len(body))
		}
		blk.body = body[:34]
		if body[10] == 0 && body[11] == 0 && body[12]>>4 == 0 {
			return nil, errors.New("a sample rate value of 0 is invalid")
		}
	case goflac.SeekTable:
		blk.body = body[:len(body)/18*18]
	case goflac.VorbisComment:
		vendor, comments, err := parseVorbisComment(body)
		if err != nil {
			return nil, err
		}
		blk.body, blk.vendor, blk.comments = nil, vendor, comments
	}
	return blk, nil
}

// parseVorbisComment is mutagen's VComment.load with errors="replace":
// invalid UTF-8 becomes U+FFFD, a comment without "=" is named unknown<i>,
// non-ASCII characters in a name become "?", and comments with other invalid
// names are dropped.
func parseVorbisComment(body []byte) (string, []vorbisComment, error) {
	invalid := errors.New("not a valid Vorbis comment")
	next := func() ([]byte, bool) {
		if len(body) < 4 {
			return nil, false
		}
		n := binary.LittleEndian.Uint32(body)
		if uint64(n) > uint64(len(body)-4) {
			return nil, false
		}
		s := body[4 : 4+n]
		body = body[4+n:]
		return s, true
	}
	v, ok := next()
	if !ok {
		return "", nil, invalid
	}
	vendor := pyDecodeUTF8(v)
	if len(body) < 4 {
		return "", nil, invalid
	}
	count := binary.LittleEndian.Uint32(body)
	body = body[4:]
	var comments []vorbisComment
	for i := uint32(0); i < count; i++ {
		c, ok := next()
		if !ok {
			return "", nil, invalid
		}
		s := pyDecodeUTF8(c)
		key, value, found := strings.Cut(s, "=")
		if !found {
			key, value = fmt.Sprintf("unknown%d", i), s
		}
		key = strings.Map(func(r rune) rune {
			if r > 0x7f {
				return '?'
			}
			return r
		}, key)
		if isVorbisKey(key) {
			comments = append(comments, vorbisComment{key, value})
		}
	}
	return vendor, comments, nil
}

// isVorbisKey is mutagen's is_valid_key: printable ASCII up to "}", no "=",
// not empty.
func isVorbisKey(key string) bool {
	for _, r := range key {
		if r < ' ' || r > '}' || r == '=' {
			return false
		}
	}
	return key != ""
}

// streamInfo is the first STREAMINFO block, mutagen's FLAC.info.
func (b *flacBlocks) streamInfo() *flacBlock {
	for _, blk := range b.blocks {
		if blk.code == goflac.StreamInfo {
			return blk
		}
	}
	return nil
}

// setTag is FLAC.__setitem__: a Vorbis comment block is added when the file
// has none, the key's old comments go, ignoring case, and the new ones are
// appended. A list gives one comment per item.
func (b *flacBlocks) setTag(key string, value any) {
	if b.tags == nil {
		b.tags = &flacBlock{code: goflac.VorbisComment, vendor: "Mutagen 1.48.1"}
		b.blocks = append(b.blocks, b.tags)
	}
	b.delTag(key)
	values, ok := value.([]any)
	if !ok {
		values = []any{value}
	}
	for _, v := range values {
		b.tags.comments = append(b.tags.comments, vorbisComment{key, v})
	}
}

// hasTag is `key in audio`.
func (b *flacBlocks) hasTag(key string) bool {
	if b.tags == nil {
		return false
	}
	for _, c := range b.tags.comments {
		if strings.EqualFold(c.key, key) {
			return true
		}
	}
	return false
}

// delTag removes every comment named key, ignoring case.
func (b *flacBlocks) delTag(key string) {
	if b.tags == nil {
		return
	}
	kept := b.tags.comments[:0]
	for _, c := range b.tags.comments {
		if !strings.EqualFold(c.key, key) {
			kept = append(kept, c)
		}
	}
	b.tags.comments = kept
}

// comments returns the vendor string and the comments as "KEY=value", or
// ok false when the file has no Vorbis comment block.
func (b *flacBlocks) comments() (vendor string, list []string, ok bool) {
	if b.tags == nil {
		return "", nil, false
	}
	for _, c := range b.tags.comments {
		list = append(list, c.key+"="+pyStr(c.value))
	}
	return b.tags.vendor, list, true
}

// setVendor sets the vendor string of the comment block, if there is one.
func (b *flacBlocks) setVendor(vendor string) {
	if b.tags != nil {
		b.tags.vendor = vendor
	}
}

// addPicture is FLAC.add_picture: the block goes after the others.
func (b *flacBlocks) addPicture(p *flacpicture.MetadataBlockPicture) {
	m := p.Marshal()
	b.blocks = append(b.blocks, &flacBlock{code: goflac.Picture, body: m.Data})
}

// encode is a block's body as mutagen writes it.
func (blk *flacBlock) encode() ([]byte, error) {
	if blk.code != goflac.VorbisComment {
		return blk.body, nil
	}
	vc := flacvorbis.MetaDataBlockVorbisComment{Vendor: blk.vendor, Comments: make([]string, 0, len(blk.comments))}
	for _, c := range blk.comments {
		s, ok := c.value.(string)
		if !ok {
			return nil, pyErr(pyValueError, "%s needs to be str for key %s", pyRepr(c.value), pyRepr(c.key))
		}
		vc.Comments = append(vc.Comments, c.key+"="+s)
	}
	return vc.Marshal().Data, nil
}

// metadata renders the blocks as mutagen's _writeblocks does: every block
// but PADDING in order, then one PADDING block of padding bytes, the last.
func (b *flacBlocks) metadata(padding int) ([]byte, error) {
	var out bytes.Buffer
	write := func(code goflac.BlockType, body []byte, last bool) error {
		if len(body) > flacMaxBlockSize {
			return fmt.Errorf("block is too long to write: %d bytes", len(body))
		}
		h := byte(code)
		if last {
			h |= 0x80
		}
		out.Write([]byte{h, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))})
		out.Write(body)
		return nil
	}
	for _, blk := range b.blocks {
		if blk.code == goflac.Padding {
			continue
		}
		body, err := blk.encode()
		if err != nil {
			return nil, err
		}
		if err := write(blk.code, body, false); err != nil {
			return nil, err
		}
	}
	if err := write(goflac.Padding, make([]byte, min(padding, flacMaxBlockSize)), true); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// writeTo writes src with the new metadata to dst: what came before "fLaC",
// the marker, the blocks with padding bytes of padding, and src's audio
// copied as it is. A failed dst is removed.
func (b *flacBlocks) writeTo(src, dst string, padding int) (err error) {
	meta, err := b.metadata(padding)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	if _, err := io.CopyN(out, in, b.start); err != nil {
		return err
	}
	if _, err := out.Write(append([]byte("fLaC"), meta...)); err != nil {
		return err
	}
	if _, err := in.Seek(b.audio, io.SeekStart); err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return err
}

// save is FLAC.save in place: the new file is written beside path and then
// replaces it, so a failure leaves path as it was.
func (b *flacBlocks) save(path string, padding int) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tagging-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_ = tmp.Close()
	if err := b.writeTo(path, name, padding); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// flacStreamInfo reads STREAMINFO: the bit depth, the sample rate and the
// number of samples.
func flacStreamInfo(path string) (bitDepth int, sampleRate int, samples int64, err error) {
	b, err := readFLACBlocks(path)
	if err != nil {
		return 0, 0, 0, err
	}
	s := b.streamInfo().body
	sampleRate = int(s[10])<<12 | int(s[11])<<4 | int(s[12])>>4
	bitDepth = (int(s[12]&1)<<4 | int(s[13])>>4) + 1
	samples = int64(s[13]&0x0f)<<32 | int64(binary.BigEndian.Uint32(s[14:18]))
	return bitDepth, sampleRate, samples, nil
}

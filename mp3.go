// The length of an MP3 file as mutagen 1.48.1 computes it, which make_m3u
// reads through mutagen.File: mutagen/mp3/__init__.py's MPEGInfo and
// MPEGFrame and mutagen/mp3/_util.py's Xing, LAME and VBRI headers.

package qobuzdl

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strconv"
)

// errMP3NoFrame is mutagen's HeaderNotFoundError.
var errMP3NoFrame = errors.New("can't sync to MPEG frame")

// mpegFrame is the part of mutagen's MPEGFrame the length needs.
type mpegFrame struct {
	offset     int64
	size       int64 // frame_length
	bitrate    int   // bits per second
	sampleRate int
	sketchy    bool // no Xing or VBRI header
	length     float64
	hasLength  bool
}

var mpegBitrates = map[[2]int][]int{
	{10, 1}: {0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448},
	{10, 2}: {0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384},
	{10, 3}: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},
	{20, 1}: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256},
	{20, 2}: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
}

// mpegRates are the sample rates by version times ten: 1, 2 and 2.5.
var mpegRates = map[int][]int{10: {44100, 48000, 32000}, 20: {22050, 24000, 16000}, 25: {11025, 12000, 8000}}

// mp3Length is MPEGInfo.length: the first frame with a Xing or VBRI header
// gives the length from its frame count, else the first of four frames in a
// row gives it from the file size and the bitrate. Syncs are searched in
// the first MiB after any ID3v2 tags.
func mp3Length(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := st.Size()
	start := skipID3(f)
	buf := make([]byte, 1<<20)
	n, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	buf = buf[:n]

	const enoughFrames, minFrames = 4, 2
	maxSyncs := 1500
	var first *mpegFrame
	for i := 0; i+1 < len(buf); i++ {
		if buf[i] != 0xff || buf[i+1]&0xe0 != 0xe0 {
			continue
		}
		maxSyncs--
		if maxSyncs <= 0 {
			break
		}
		var frames []*mpegFrame
		pos := start + int64(i)
		for range enoughFrames {
			fr, ok := readMPEGFrame(f, pos)
			if !ok {
				break
			}
			frames = append(frames, fr)
			if !fr.sketchy {
				break
			}
			pos = fr.offset + fr.size
		}
		if len(frames) >= minFrames && first == nil {
			first = frames[0]
		}
		if len(frames) > 0 && !frames[len(frames)-1].sketchy {
			first = frames[len(frames)-1]
			break
		}
		if len(frames) >= enoughFrames {
			first = frames[0]
			break
		}
	}
	if first == nil {
		return 0, errMP3NoFrame
	}
	if first.hasLength {
		return first.length, nil
	}
	return 8 * float64(size-first.offset) / float64(first.bitrate), nil
}

// skipID3 is mutagen.mp3's skip_id3: past every ID3v2 tag at the start, as
// Windows Media Player stacks them.
func skipID3(r io.ReaderAt) int64 {
	var pos int64
	for {
		var h [10]byte
		if n, _ := r.ReadAt(h[:], pos); n < 10 || string(h[:3]) != "ID3" {
			return pos
		}
		size := int64(syncsafeInt(h[6:10]))
		if size == 0 {
			return pos
		}
		pos += 10 + size
	}
}

// readMPEGFrame is MPEGFrame.__init__ at off; ok is false where mutagen
// raises HeaderNotFoundError.
func readMPEGFrame(r io.ReaderAt, off int64) (*mpegFrame, bool) {
	var h [4]byte
	if n, _ := r.ReadAt(h[:], off); n < 4 {
		return nil, false
	}
	if h[0] != 0xff || h[1]&0xe0 != 0xe0 {
		return nil, false
	}
	version := int(h[1]>>3) & 3
	layer := int(h[1]>>1) & 3
	bitrate := int(h[2] >> 4)
	rate := int(h[2]>>2) & 3
	padding := int(h[2]>>1) & 1
	mode := int(h[3]>>6) & 3
	if version == 1 || layer == 0 || rate == 3 || bitrate == 15 || bitrate == 0 {
		return nil, false
	}
	v := [4]int{25, 0, 20, 10}[version]
	layer = 4 - layer
	table := v
	if v == 25 {
		table = 20
	}
	key := [2]int{table, layer}
	if table == 20 && layer == 3 {
		key = [2]int{20, 2}
	}
	fr := &mpegFrame{offset: off, sketchy: true}
	fr.bitrate = mpegBitrates[key][bitrate] * 1000
	fr.sampleRate = mpegRates[v][rate]

	frameSize, slot := 1152, 1
	switch {
	case layer == 1:
		frameSize, slot = 384, 4
	case v >= 20 && layer == 3:
		frameSize = 576
	}
	fr.size = int64((frameSize/8*fr.bitrate/fr.sampleRate + padding) * slot)
	if layer == 3 {
		fr.parseVBRHeader(r, v, mode, frameSize)
	}
	return fr, true
}

// parseVBRHeader is MPEGFrame._parse_vbr_header: a Xing or Info header, else
// a VBRI one, gives the frame count.
func (fr *mpegFrame) parseVBRHeader(r io.ReaderAt, version, mode, frameSize int) {
	xing := int64(36)
	switch {
	case version == 10 && mode == 3:
		xing = 21
	case version != 10 && mode != 3:
		xing = 21
	case version != 10:
		xing = 13
	}
	if x, ok := readXing(r, fr.offset+xing); ok {
		fr.sketchy = false
		if x.frames >= 0 {
			samples := int64(frameSize) * x.frames
			if x.lame {
				samples -= x.delay + x.padding
			}
			fr.length = float64(max(samples, 0)) / float64(fr.sampleRate)
			fr.hasLength = true
		}
		return
	}
	if frames, ok := readVBRI(r, fr.offset+36); ok {
		fr.sketchy = false
		fr.length = float64(int64(frameSize)*frames) / float64(fr.sampleRate)
		fr.hasLength = true
	}
}

// xingHeader is what the length needs of mutagen's XingHeader.
type xingHeader struct {
	frames         int64 // -1 when unknown
	lame           bool
	delay, padding int64
}

// readXing is XingHeader.__init__; ok is false where it raises.
func readXing(r io.ReaderAt, off int64) (xingHeader, bool) {
	x := xingHeader{frames: -1}
	read := func(n int) ([]byte, bool) {
		b := make([]byte, n)
		got, _ := r.ReadAt(b, off)
		off += int64(got)
		return b, got == n
	}
	head, ok := read(8)
	if !ok || (string(head[:4]) != "Xing" && string(head[:4]) != "Info") {
		return x, false
	}
	flags := binary.BigEndian.Uint32(head[4:])
	for _, field := range []struct {
		flag uint32
		size int
	}{{1, 4}, {2, 4}, {4, 100}, {8, 4}} {
		if flags&field.flag == 0 {
			continue
		}
		b, ok := read(field.size)
		if !ok {
			return x, false
		}
		if field.flag == 1 {
			x.frames = int64(binary.BigEndian.Uint32(b))
		}
	}
	version, ok := read(20)
	if !ok || !lameHasHeader(version) {
		return x, true
	}
	// parse_version seeks back 11 bytes, to where the header starts.
	off -= 11
	b, ok := read(27)
	if !ok || b[0]>>4 != 0 {
		return x, true
	}
	x.lame = true
	x.delay = int64(b[12])<<4 | int64(b[13]>>4)
	x.padding = int64(b[13]&0x0f)<<8 | int64(b[14])
	return x, true
}

// lameHasHeader is LAMEHeader.parse_version's third value, false where it
// raises LAMEError: a LAME version from 3.90 on is followed by the header.
func lameHasHeader(data []byte) bool {
	if !bytes.HasPrefix(data, []byte("LAME")) && !bytes.HasPrefix(data, []byte("L3.99")) {
		return false
	}
	data = bytes.TrimLeft(data, "EMAL")
	if len(data) == 0 {
		return false
	}
	majorText, rest := data[:1], bytes.TrimLeft(data[1:], ".")
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	major, err := strconv.Atoi(string(majorText))
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(string(rest[:i]))
	if err != nil {
		return false
	}
	rest = rest[i:]
	if major < 3 || (major == 3 && minor < 90) {
		return false
	}
	if major == 3 && minor == 90 && len(rest) >= 11 && rest[len(rest)-11] == '(' {
		return false
	}
	return len(rest) >= 11
}

// readVBRI is VBRIHeader.__init__: the frame count, ok false where it raises.
func readVBRI(r io.ReaderAt, off int64) (int64, bool) {
	b := make([]byte, 26)
	if n, _ := r.ReadAt(b, off); n < 26 || !bytes.HasPrefix(b, []byte("VBRI")) {
		return 0, false
	}
	if binary.BigEndian.Uint16(b[4:]) != 1 {
		return 0, false
	}
	frames := int64(binary.BigEndian.Uint32(b[14:]))
	entries := int64(binary.BigEndian.Uint16(b[18:]))
	entrySize := int64(binary.BigEndian.Uint16(b[22:]))
	// The table must be there in full: its last byte is.
	if toc := entries * entrySize; toc > 0 {
		if n, _ := r.ReadAt(make([]byte, 1), off+26+toc-1); n < 1 {
			return 0, false
		}
	}
	if entrySize != 2 && entrySize != 4 {
		return 0, false
	}
	return frames, true
}

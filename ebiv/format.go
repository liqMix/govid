// Package ebiv implements the EBIV video format: a decode-optimized,
// pure-Go codec and container for offline-encoded background animation.
//
// The design is radically asymmetric — encoding happens once with an unlimited
// compute budget, decoding happens inside a game's frame loop and must not
// stall. See .docs/codec-design-plan.md for the full rationale.
//
// The container is a fixed file header, a sequence of self-delimiting frame
// records, a frame index, and a footer pointing at the index. Seeking is O(1)
// through the index; a file whose footer is missing or corrupt (an interrupted
// encode) is still readable by scanning the frame records.
package ebiv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// Format version written by this package. The reader accepts only this exact
// version — the bitstream is not frozen and there is no compatibility story
// until it is. Version 2 was the M1 bitstream (skip/CBP, class-coded MVs and
// escapes, tx-split coefficient contexts, table delta-coding); version 3
// added the golden reference frame (per-macroblock reference select) and
// sign data hiding; version 4 codes coefficient tokens with per-tile
// adaptive CDFs (no shipped tables for those contexts).
const Version = 4

// On-disk sizes, in bytes.
const (
	fileHeaderSize  = 48
	frameRecordSize = 8
	indexEntrySize  = 16
	footerSize      = 20
)

var (
	magicFile   = [4]byte{'E', 'B', 'I', 'V'}
	magicFooter = [4]byte{'E', 'B', 'I', 'X'}
)

// maxFrameSize bounds a frame payload so a corrupt length field cannot drive a
// huge allocation. 256 MiB is far above any legitimate frame (an uncompressed
// 8K 4:2:0 frame is ~50 MiB).
const maxFrameSize = 256 << 20

// Chroma formats. Only 4:2:0 is defined in version 1.
const (
	Chroma420 uint8 = 0
)

// Bit depth codes. Only 8-bit is defined in version 1.
const (
	depth8 uint8 = 0
)

// FrameType distinguishes independently decodable frames from those that
// reference an earlier frame.
type FrameType uint8

const (
	// FrameKey is intra-only: seekable and decodable with no prior state.
	FrameKey FrameType = 0
	// FrameInter predicts from the previously decoded frame.
	FrameInter FrameType = 1
)

func (t FrameType) String() string {
	switch t {
	case FrameKey:
		return "key"
	case FrameInter:
		return "inter"
	}
	return fmt.Sprintf("FrameType(%d)", uint8(t))
}

// CodingMode selects how a frame's pixel data is represented.
type CodingMode uint8

const (
	// CodingRaw stores tightly packed 8-bit planar 4:2:0 samples with no
	// compression. It is the bootstrap path and a lossless intermediate: it
	// exercises the container, the plane geometry, and the frame ring without
	// any codec complexity.
	CodingRaw CodingMode = 0
	// CodingIntra is a compressed, intra-only key frame: directional intra
	// prediction, integer DCT, scalar quantization, and static-table rANS.
	CodingIntra CodingMode = 1
	// CodingInter is a compressed frame predicted from the previous frame:
	// per-macroblock motion compensation with an intra fallback, plus a coded
	// residual.
	CodingInter CodingMode = 2
)

func (m CodingMode) String() string {
	switch m {
	case CodingRaw:
		return "raw"
	case CodingIntra:
		return "intra"
	case CodingInter:
		return "inter"
	}
	return fmt.Sprintf("CodingMode(%d)", uint8(m))
}

// Errors reported by the reader, writer, and codec.
var (
	ErrBadMagic     = errors.New("ebiv: not an EBIV file")
	ErrVersion      = errors.New("ebiv: unsupported format version")
	ErrCorrupt      = errors.New("ebiv: corrupt stream")
	ErrConfig       = errors.New("ebiv: invalid configuration")
	ErrUnsupported  = errors.New("ebiv: unsupported stream feature")
	ErrNoKeyframe   = errors.New("ebiv: inter frame before any key frame")
	ErrDimensions   = errors.New("ebiv: frame dimensions do not match the stream")
	ErrWriterClosed = errors.New("ebiv: writer is closed")
)

// Config describes a stream to be written.
type Config struct {
	Width  int // luma width in pixels, 1..65535
	Height int // luma height in pixels, 1..65535

	// FPSNum/FPSDen give the frame rate as an exact rational (e.g. 30000/1001).
	// Frames are constant-rate; presentation time is derived from frame index.
	FPSNum uint32
	FPSDen uint32

	// TileCols and TileRows describe the tile grid used by coded frames. Zero
	// means one tile. Tiles are entropy-independent so they can be decoded on
	// separate goroutines.
	TileCols int
	TileRows int
}

func (c Config) validate() error {
	if c.Width <= 0 || c.Width > 65535 || c.Height <= 0 || c.Height > 65535 {
		return fmt.Errorf("%w: dimensions %dx%d out of range 1..65535", ErrConfig, c.Width, c.Height)
	}
	if c.FPSNum == 0 || c.FPSDen == 0 {
		return fmt.Errorf("%w: frame rate %d/%d must be non-zero", ErrConfig, c.FPSNum, c.FPSDen)
	}
	if c.TileCols < 0 || c.TileCols > 65535 || c.TileRows < 0 || c.TileRows > 65535 {
		return fmt.Errorf("%w: tile grid %dx%d out of range", ErrConfig, c.TileCols, c.TileRows)
	}
	return nil
}

// FileHeader is the fixed-size header at the start of every EBIV file.
//
// It deliberately carries no frame count: the writer streams to a plain
// io.Writer and cannot revisit the header, so the authoritative count lives in
// the footer alongside the index.
type FileHeader struct {
	Version        uint16
	Width          int
	Height         int
	FPSNum         uint32
	FPSDen         uint32
	ChromaFormat   uint8
	BitDepth       uint8 // code, not bits: depth8 == 0 means 8-bit
	ColorRange     uint8 // 0 = limited range BT.601
	ColorPrimaries uint8 // 0 = BT.601
	TileCols       uint16
	TileRows       uint16
	Flags          uint32
}

func headerFor(c Config) FileHeader {
	return FileHeader{
		Version:      Version,
		Width:        c.Width,
		Height:       c.Height,
		FPSNum:       c.FPSNum,
		FPSDen:       c.FPSDen,
		ChromaFormat: Chroma420,
		BitDepth:     depth8,
		TileCols:     uint16(c.TileCols),
		TileRows:     uint16(c.TileRows),
	}
}

// marshal writes h into b, which must be at least fileHeaderSize bytes.
func (h *FileHeader) marshal(b []byte) {
	b = b[:fileHeaderSize]
	copy(b[0:4], magicFile[:])
	binary.LittleEndian.PutUint16(b[4:6], h.Version)
	binary.LittleEndian.PutUint16(b[6:8], fileHeaderSize)
	binary.LittleEndian.PutUint32(b[8:12], uint32(h.Width))
	binary.LittleEndian.PutUint32(b[12:16], uint32(h.Height))
	binary.LittleEndian.PutUint32(b[16:20], h.FPSNum)
	binary.LittleEndian.PutUint32(b[20:24], h.FPSDen)
	binary.LittleEndian.PutUint32(b[24:28], 0) // reserved
	b[28] = h.ChromaFormat
	b[29] = h.BitDepth
	b[30] = h.ColorRange
	b[31] = h.ColorPrimaries
	binary.LittleEndian.PutUint16(b[32:34], h.TileCols)
	binary.LittleEndian.PutUint16(b[34:36], h.TileRows)
	binary.LittleEndian.PutUint32(b[36:40], h.Flags)
	binary.LittleEndian.PutUint64(b[40:48], 0) // reserved
}

// unmarshal parses a file header from b and validates every field the decoder
// relies on, so no later stage has to re-check them.
func (h *FileHeader) unmarshal(b []byte) error {
	if len(b) < fileHeaderSize {
		return fmt.Errorf("%w: file header truncated", ErrCorrupt)
	}
	if [4]byte(b[0:4]) != magicFile {
		return ErrBadMagic
	}
	h.Version = binary.LittleEndian.Uint16(b[4:6])
	if h.Version != Version {
		return fmt.Errorf("%w: version %d, want %d", ErrVersion, h.Version, Version)
	}
	if size := binary.LittleEndian.Uint16(b[6:8]); size != fileHeaderSize {
		return fmt.Errorf("%w: header size %d, want %d", ErrCorrupt, size, fileHeaderSize)
	}
	w := binary.LittleEndian.Uint32(b[8:12])
	ht := binary.LittleEndian.Uint32(b[12:16])
	if w == 0 || w > 65535 || ht == 0 || ht > 65535 {
		return fmt.Errorf("%w: dimensions %dx%d out of range", ErrCorrupt, w, ht)
	}
	h.Width, h.Height = int(w), int(ht)
	h.FPSNum = binary.LittleEndian.Uint32(b[16:20])
	h.FPSDen = binary.LittleEndian.Uint32(b[20:24])
	if h.FPSNum == 0 || h.FPSDen == 0 {
		return fmt.Errorf("%w: frame rate %d/%d", ErrCorrupt, h.FPSNum, h.FPSDen)
	}
	h.ChromaFormat = b[28]
	h.BitDepth = b[29]
	h.ColorRange = b[30]
	h.ColorPrimaries = b[31]
	if h.ChromaFormat != Chroma420 {
		return fmt.Errorf("%w: chroma format %d, only 4:2:0 is supported", ErrUnsupported, h.ChromaFormat)
	}
	if h.BitDepth != depth8 {
		return fmt.Errorf("%w: bit depth code %d, only 8-bit is supported", ErrUnsupported, h.BitDepth)
	}
	h.TileCols = binary.LittleEndian.Uint16(b[32:34])
	h.TileRows = binary.LittleEndian.Uint16(b[34:36])
	h.Flags = binary.LittleEndian.Uint32(b[36:40])
	return nil
}

// frameTime returns the presentation timestamp of frame i.
func (h *FileHeader) frameTime(i int) time.Duration {
	// Multiply before dividing so the result is exact for rates like 30000/1001.
	return time.Duration(int64(i) * int64(h.FPSDen) * int64(time.Second) / int64(h.FPSNum))
}

// frameIndexAt returns the index of the frame displayed at time t: the largest
// i for which frameTime(i) <= t, clamped to zero.
//
// frameTime truncates, so dividing straight back through lands one frame low
// whenever the frame period is not a whole number of nanoseconds — at 30 fps,
// frameTime(7) is 233333333 ns and 233333333*30/1e9 is 6. The estimate is
// always within one frame, so a single correction step in each direction
// recovers the exact answer.
func (h *FileHeader) frameIndexAt(t time.Duration) int {
	if t <= 0 {
		return 0
	}
	i := int(int64(t) * int64(h.FPSNum) / (int64(h.FPSDen) * int64(time.Second)))
	for h.frameTime(i+1) <= t {
		i++
	}
	for i > 0 && h.frameTime(i) > t {
		i--
	}
	return i
}

// Frame record flags.
const flagKeyframe uint32 = 1 << 0

// IndexEntry locates one frame in the file.
type IndexEntry struct {
	Offset   uint64 // byte offset of the frame record, including its prefix
	Size     uint32 // payload size, excluding the record prefix
	Keyframe bool
}

func (e IndexEntry) marshal(b []byte) {
	binary.LittleEndian.PutUint64(b[0:8], e.Offset)
	binary.LittleEndian.PutUint32(b[8:12], e.Size)
	var flags uint32
	if e.Keyframe {
		flags |= flagKeyframe
	}
	binary.LittleEndian.PutUint32(b[12:16], flags)
}

func (e *IndexEntry) unmarshal(b []byte) {
	e.Offset = binary.LittleEndian.Uint64(b[0:8])
	e.Size = binary.LittleEndian.Uint32(b[8:12])
	e.Keyframe = binary.LittleEndian.Uint32(b[12:16])&flagKeyframe != 0
}

// frameSync opens every frame record. Its only job is to tell a rebuild scan
// where the frame payload ends: without it the scan runs off the last frame
// into the index and happily reads index entries as more frames, since an
// entry's low 32 bits are a plausible length.
const frameSync uint16 = 0xEB17

// putFrameRecord writes the 8-byte prefix that makes a frame self-delimiting
// during a sequential scan.
func putFrameRecord(b []byte, size uint32, keyframe bool) {
	binary.LittleEndian.PutUint16(b[0:2], frameSync)
	var flags uint16
	if keyframe {
		flags |= uint16(flagKeyframe)
	}
	binary.LittleEndian.PutUint16(b[2:4], flags)
	binary.LittleEndian.PutUint32(b[4:8], size)
}

func parseFrameRecord(b []byte) (size uint32, keyframe, ok bool) {
	if binary.LittleEndian.Uint16(b[0:2]) != frameSync {
		return 0, false, false
	}
	keyframe = binary.LittleEndian.Uint16(b[2:4])&uint16(flagKeyframe) != 0
	size = binary.LittleEndian.Uint32(b[4:8])
	return size, keyframe, true
}

// frameHeader is the payload-level header carried inside every packet. It
// duplicates the geometry on key frames so that a Codec is self-describing and
// can be driven by any demuxer, not just this package's reader.
type frameHeader struct {
	Type   FrameType
	Coding CodingMode
	Width  int // key frames only
	Height int // key frames only
}

const (
	frameHeaderBase     = 1 // type/coding byte
	frameHeaderKeyExtra = 5 // width, height, sample description
)

// size returns the encoded length of the header.
func (f frameHeader) size() int {
	if f.Type == FrameKey {
		return frameHeaderBase + frameHeaderKeyExtra
	}
	return frameHeaderBase
}

// appendTo appends the encoded header to dst.
func (f frameHeader) appendTo(dst []byte) []byte {
	dst = append(dst, (uint8(f.Type)&0x03)|((uint8(f.Coding)&0x07)<<2))
	if f.Type != FrameKey {
		return dst
	}
	var geo [frameHeaderKeyExtra]byte
	binary.LittleEndian.PutUint16(geo[0:2], uint16(f.Width))
	binary.LittleEndian.PutUint16(geo[2:4], uint16(f.Height))
	geo[4] = Chroma420<<4 | depth8
	return append(dst, geo[:]...)
}

// parseFrameHeader splits a packet into its header and the payload body.
func parseFrameHeader(b []byte) (frameHeader, []byte, error) {
	var f frameHeader
	if len(b) < frameHeaderBase {
		return f, nil, fmt.Errorf("%w: empty frame payload", ErrCorrupt)
	}
	f.Type = FrameType(b[0] & 0x03)
	f.Coding = CodingMode((b[0] >> 2) & 0x07)
	if f.Type != FrameKey && f.Type != FrameInter {
		return f, nil, fmt.Errorf("%w: frame type %d", ErrCorrupt, uint8(f.Type))
	}
	if f.Type != FrameKey {
		return f, b[frameHeaderBase:], nil
	}
	if len(b) < frameHeaderBase+frameHeaderKeyExtra {
		return f, nil, fmt.Errorf("%w: key frame header truncated", ErrCorrupt)
	}
	f.Width = int(binary.LittleEndian.Uint16(b[1:3]))
	f.Height = int(binary.LittleEndian.Uint16(b[3:5]))
	if f.Width == 0 || f.Height == 0 {
		return f, nil, fmt.Errorf("%w: key frame dimensions %dx%d", ErrCorrupt, f.Width, f.Height)
	}
	desc := b[5]
	if chroma := desc >> 4; chroma != Chroma420 {
		return f, nil, fmt.Errorf("%w: chroma format %d", ErrUnsupported, chroma)
	}
	if depth := desc & 0x0f; depth != depth8 {
		return f, nil, fmt.Errorf("%w: bit depth code %d", ErrUnsupported, depth)
	}
	return f, b[frameHeaderBase+frameHeaderKeyExtra:], nil
}

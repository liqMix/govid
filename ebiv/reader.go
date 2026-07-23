package ebiv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"time"

	govid "github.com/liqmix/govid"
)

// Reader demuxes an EBIV container. It implements govid.Demuxer.
//
// Seeking is O(1): the footer's frame index gives every frame's offset and
// keyframe flag up front. If the footer is missing or corrupt — an encode that
// was interrupted before Close — the index is rebuilt by scanning the frame
// records, which costs one pass but keeps the file usable.
type Reader struct {
	r     io.ReadSeeker
	hdr   FileHeader
	index []IndexEntry

	next   int    // index of the next frame NextPacket will return
	buf    []byte // reused packet buffer
	closed bool
}

// NewDemuxer parses the container metadata and positions the reader at the
// first frame. It does not take ownership of r; the caller still closes it.
func NewDemuxer(r io.ReadSeeker) (*Reader, error) {
	d := &Reader{r: r}

	var hdr [fileHeaderSize]byte
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("ebiv: seek to header: %w", err)
	}
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrBadMagic
		}
		return nil, fmt.Errorf("ebiv: read header: %w", err)
	}
	if err := d.hdr.unmarshal(hdr[:]); err != nil {
		return nil, err
	}

	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("ebiv: measure stream: %w", err)
	}
	if index, err := readIndex(r, size); err == nil {
		d.index = index
	} else if index, err := scanFrames(r, size); err == nil {
		d.index = index
	} else {
		return nil, err
	}
	return d, nil
}

// readIndex loads the frame index using the footer. It returns an error if the
// footer is absent or fails validation, which is the caller's cue to rebuild.
func readIndex(r io.ReadSeeker, size int64) ([]IndexEntry, error) {
	if size < fileHeaderSize+footerSize {
		return nil, fmt.Errorf("%w: file too short to hold a footer", ErrCorrupt)
	}
	var footer [footerSize]byte
	if _, err := r.Seek(size-footerSize, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, footer[:]); err != nil {
		return nil, err
	}
	if [4]byte(footer[16:20]) != magicFooter {
		return nil, fmt.Errorf("%w: missing footer magic", ErrCorrupt)
	}

	offset := binary.LittleEndian.Uint64(footer[0:8])
	count := binary.LittleEndian.Uint32(footer[8:12])
	want := binary.LittleEndian.Uint32(footer[12:16])

	end := int64(offset) + int64(count)*indexEntrySize
	if offset < fileHeaderSize || end != size-footerSize {
		return nil, fmt.Errorf("%w: index bounds %d..%d do not fit a %d-byte file", ErrCorrupt, offset, end, size)
	}

	buf := make([]byte, int(count)*indexEntrySize)
	if _, err := r.Seek(int64(offset), io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	if got := crc32.ChecksumIEEE(buf); got != want {
		return nil, fmt.Errorf("%w: index checksum %08x, want %08x", ErrCorrupt, got, want)
	}

	index := make([]IndexEntry, count)
	for i := range index {
		index[i].unmarshal(buf[i*indexEntrySize:])
		if err := validateEntry(index[i], offset); err != nil {
			return nil, err
		}
	}
	if count > 0 && !index[0].Keyframe {
		return nil, fmt.Errorf("%w: first frame is not a key frame", ErrCorrupt)
	}
	return index, nil
}

func validateEntry(e IndexEntry, payloadEnd uint64) error {
	if e.Offset < fileHeaderSize || e.Size == 0 || e.Size > maxFrameSize {
		return fmt.Errorf("%w: index entry offset %d size %d", ErrCorrupt, e.Offset, e.Size)
	}
	if e.Offset+frameRecordSize+uint64(e.Size) > payloadEnd {
		return fmt.Errorf("%w: index entry at %d runs past the payload", ErrCorrupt, e.Offset)
	}
	return nil
}

// scanFrames rebuilds the index by walking the self-delimiting frame records.
// A trailing partial record ends the scan without failing: an interrupted
// encode should still play back everything it managed to write.
func scanFrames(r io.ReadSeeker, size int64) ([]IndexEntry, error) {
	if _, err := r.Seek(fileHeaderSize, io.SeekStart); err != nil {
		return nil, fmt.Errorf("ebiv: seek to first frame: %w", err)
	}
	var (
		index []IndexEntry
		off   = int64(fileHeaderSize)
		rec   [frameRecordSize]byte
	)
	for off+frameRecordSize <= size {
		if _, err := io.ReadFull(r, rec[:]); err != nil {
			break
		}
		payload, keyframe, ok := parseFrameRecord(rec[:])
		if !ok || payload == 0 || payload > maxFrameSize {
			break
		}
		end := off + frameRecordSize + int64(payload)
		if end > size {
			break
		}
		index = append(index, IndexEntry{Offset: uint64(off), Size: payload, Keyframe: keyframe})
		if _, err := r.Seek(end, io.SeekStart); err != nil {
			return nil, fmt.Errorf("ebiv: scan frames: %w", err)
		}
		off = end
	}
	if len(index) == 0 {
		return nil, fmt.Errorf("%w: no frames found", ErrCorrupt)
	}
	if !index[0].Keyframe {
		return nil, fmt.Errorf("%w: first frame is not a key frame", ErrCorrupt)
	}
	return index, nil
}

// FileHeader returns the stream's parsed header.
func (d *Reader) FileHeader() FileHeader { return d.hdr }

// FrameCount returns the number of frames in the stream.
func (d *Reader) FrameCount() int { return len(d.index) }

// Index returns the frame index. The slice is owned by the reader; do not
// modify it.
func (d *Reader) Index() []IndexEntry { return d.index }

// VideoInfo reports the stream's dimensions and frame rate.
func (d *Reader) VideoInfo() govid.VideoInfo {
	return govid.VideoInfo{
		Width:     d.hdr.Width,
		Height:    d.hdr.Height,
		FrameRate: float64(d.hdr.FPSNum) / float64(d.hdr.FPSDen),
	}
}

// Duration returns the presentation time just past the last frame.
func (d *Reader) Duration() time.Duration { return d.hdr.frameTime(len(d.index)) }

// NextPacket returns the next frame's payload.
//
// The returned Data aliases a buffer the reader reuses, so it is valid only
// until the next call to NextPacket or Seek. Codecs consume a packet before
// requesting another, so this costs no per-frame allocation in steady state.
func (d *Reader) NextPacket() (govid.Packet, error) {
	if d.closed {
		return govid.Packet{}, io.EOF
	}
	if d.next >= len(d.index) {
		return govid.Packet{}, io.EOF
	}
	e := d.index[d.next]

	if cap(d.buf) < int(e.Size) {
		d.buf = make([]byte, e.Size)
	}
	d.buf = d.buf[:e.Size]

	if _, err := d.r.Seek(int64(e.Offset)+frameRecordSize, io.SeekStart); err != nil {
		return govid.Packet{}, fmt.Errorf("ebiv: seek to frame %d: %w", d.next, err)
	}
	if _, err := io.ReadFull(d.r, d.buf); err != nil {
		return govid.Packet{}, fmt.Errorf("ebiv: read frame %d: %w", d.next, err)
	}

	pkt := govid.Packet{
		Data:      d.buf,
		Timestamp: d.hdr.frameTime(d.next),
		Keyframe:  e.Keyframe,
	}
	d.next++
	return pkt, nil
}

// Seek positions the reader at the last key frame at or before t and returns
// that key frame's timestamp. Decoding from there reproduces the requested
// position exactly, since no frame before a key frame can influence it.
func (d *Reader) Seek(t time.Duration) (time.Duration, error) {
	if d.closed {
		return 0, io.EOF
	}
	if len(d.index) == 0 {
		return 0, io.EOF
	}
	target := d.hdr.frameIndexAt(t)
	if target >= len(d.index) {
		target = len(d.index) - 1
	}
	// The walk back to the governing key frame is bounded by the GOP length,
	// which is small by construction.
	target = d.keyframeBefore(target)
	d.next = target
	return d.hdr.frameTime(target), nil
}

// Close marks the reader done. It does not close the underlying stream, which
// the caller still owns.
func (d *Reader) Close() error {
	d.closed = true
	return nil
}

// keyframeBefore reports the index of the last key frame at or before i.
// It exists for tests and tooling that need the seek target without moving the
// read position.
func (d *Reader) keyframeBefore(i int) int {
	if i >= len(d.index) {
		i = len(d.index) - 1
	}
	for i > 0 && !d.index[i].Keyframe {
		i--
	}
	return i
}

package ebiv

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

// Writer serializes an EBIV container to a plain io.Writer.
//
// It streams: the file header goes out immediately, each frame is written as it
// arrives, and the index and footer are emitted by Close. Nothing is ever
// rewritten, so the destination need not be seekable.
type Writer struct {
	w   io.Writer
	hdr FileHeader

	index  []IndexEntry
	off    uint64
	rec    [frameRecordSize]byte
	closed bool
	err    error
}

// NewWriter starts a container with the given configuration and writes the file
// header. The caller must call Close to emit the index; a file without one is
// still readable but seeks cost a linear scan.
func NewWriter(w io.Writer, cfg Config) (*Writer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	wr := &Writer{w: w, hdr: headerFor(cfg)}

	var hdr [fileHeaderSize]byte
	wr.hdr.marshal(hdr[:])
	if _, err := w.Write(hdr[:]); err != nil {
		return nil, fmt.Errorf("ebiv: write file header: %w", err)
	}
	wr.off = fileHeaderSize
	return wr, nil
}

// Header returns the stream's file header.
func (w *Writer) Header() FileHeader { return w.hdr }

// FrameCount returns the number of frames written so far.
func (w *Writer) FrameCount() int { return len(w.index) }

// WriteFrame appends one frame payload. The payload must already carry its
// frame header (see frameHeader.appendTo); Encoder builds it.
//
// The first frame must be a key frame, since nothing precedes it to predict
// from. Errors are sticky: once a write fails, every later call returns the
// same error.
func (w *Writer) WriteFrame(payload []byte, keyframe bool) error {
	if w.err != nil {
		return w.err
	}
	if w.closed {
		return ErrWriterClosed
	}
	if len(payload) == 0 {
		return w.fail(fmt.Errorf("%w: empty frame payload", ErrConfig))
	}
	if len(payload) > maxFrameSize {
		return w.fail(fmt.Errorf("%w: frame payload %d bytes exceeds limit %d", ErrConfig, len(payload), maxFrameSize))
	}
	if len(w.index) == 0 && !keyframe {
		return w.fail(fmt.Errorf("%w: first frame must be a key frame", ErrConfig))
	}

	putFrameRecord(w.rec[:], uint32(len(payload)), keyframe)
	if _, err := w.w.Write(w.rec[:]); err != nil {
		return w.fail(fmt.Errorf("ebiv: write frame record: %w", err))
	}
	if _, err := w.w.Write(payload); err != nil {
		return w.fail(fmt.Errorf("ebiv: write frame payload: %w", err))
	}

	w.index = append(w.index, IndexEntry{
		Offset:   w.off,
		Size:     uint32(len(payload)),
		Keyframe: keyframe,
	})
	w.off += frameRecordSize + uint64(len(payload))
	return nil
}

// Close writes the frame index and footer. It is safe to call more than once;
// later calls return the first result.
func (w *Writer) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err != nil {
		return w.err
	}

	indexOffset := w.off
	buf := make([]byte, len(w.index)*indexEntrySize)
	for i, e := range w.index {
		e.marshal(buf[i*indexEntrySize:])
	}
	if _, err := w.w.Write(buf); err != nil {
		return w.fail(fmt.Errorf("ebiv: write index: %w", err))
	}

	var footer [footerSize]byte
	binary.LittleEndian.PutUint64(footer[0:8], indexOffset)
	binary.LittleEndian.PutUint32(footer[8:12], uint32(len(w.index)))
	binary.LittleEndian.PutUint32(footer[12:16], crc32.ChecksumIEEE(buf))
	copy(footer[16:20], magicFooter[:])
	if _, err := w.w.Write(footer[:]); err != nil {
		return w.fail(fmt.Errorf("ebiv: write footer: %w", err))
	}
	return nil
}

func (w *Writer) fail(err error) error {
	if w.err == nil {
		w.err = err
	}
	return w.err
}

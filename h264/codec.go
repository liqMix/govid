package h264

import (
	"image"

	"github.com/liqmix/govid"
)

// Codec implements govid.Codec for H.264 video.
//
// Streams with B-frames arrive in decode order but must be displayed in
// picture order. The codec keeps a small reorder buffer sized from the SPS
// VUI max_num_reorder_frames (0 for streams without B-frames, so P-only
// streams keep zero-latency behavior). Decode returns nil while the buffer
// fills; Drain returns the tail frames after the last packet.
type Codec struct {
	dec     *Decoder
	pending []pendingFrame
	epoch   int64 // increments at each IDR so output keys stay monotonic
}

type pendingFrame struct {
	frame *govid.Frame
	key   int64 // (epoch << 32) | poc
}

// NewCodec creates a new H.264 codec.
func NewCodec() *Codec {
	return &Codec{dec: NewDecoder()}
}

// reorderDepth is the number of frames the display reorder buffer holds.
func (c *Codec) reorderDepth() int {
	sps := c.dec.activeSPS
	if sps != nil && sps.MaxNumReorderFrames >= 0 {
		return sps.MaxNumReorderFrames
	}
	// No VUI restriction (the spec default is MaxDpbFrames): buffer up to
	// the reference capacity, which bounds any conforming reorder distance.
	// Waiting for the first B slice instead would be wrong: the reference
	// frame ahead of it has already been emitted by then. A stream with no
	// reference frames has no inter pictures at all, so no reordering.
	if sps != nil {
		return int(sps.MaxNumRefFrames)
	}
	return 2
}

// Decode decodes an H.264 packet and returns the next frame in display
// order, or nil while the reorder buffer is filling.
func (c *Codec) Decode(pkt govid.Packet) (*govid.Frame, error) {
	img, err := c.dec.DecodePacket(pkt.Data)
	if err != nil {
		return nil, err
	}
	if img == nil {
		// Packet contained only parameter sets, no frame data.
		return nil, nil
	}

	if c.dec.curIsIDR || c.dec.sawMMCO5 {
		// An MMCO 5 reset renumbers POC from 0 like an IDR does.
		c.epoch++
		c.dec.sawMMCO5 = false
	}
	frame := &govid.Frame{
		YCbCr:     deepCopyYCbCr(img),
		Timestamp: pkt.Timestamp,
		Width:     img.Rect.Dx(),
		Height:    img.Rect.Dy(),
	}
	c.pending = append(c.pending, pendingFrame{
		frame: frame,
		key:   c.epoch<<32 | int64(uint32(c.dec.curPOC)),
	})
	if len(c.pending) <= c.reorderDepth() {
		return nil, nil
	}
	return c.popMin(), nil
}

// Drain returns buffered frames in display order after the last packet has
// been decoded, nil when empty.
func (c *Codec) Drain() *govid.Frame {
	return c.popMin()
}

func (c *Codec) popMin() *govid.Frame {
	if len(c.pending) == 0 {
		return nil
	}
	min := 0
	for i := 1; i < len(c.pending); i++ {
		if c.pending[i].key < c.pending[min].key {
			min = i
		}
	}
	f := c.pending[min].frame
	c.pending = append(c.pending[:min], c.pending[min+1:]...)
	return f
}

// Flush resets the decoder state (e.g., on seek), discarding buffered frames.
func (c *Codec) Flush() {
	c.dec = NewDecoder()
	c.pending = nil
	c.epoch = 0
}

func deepCopyYCbCr(src *image.YCbCr) *image.YCbCr {
	dst := &image.YCbCr{
		Y:              make([]byte, len(src.Y)),
		Cb:             make([]byte, len(src.Cb)),
		Cr:             make([]byte, len(src.Cr)),
		YStride:        src.YStride,
		CStride:        src.CStride,
		SubsampleRatio: src.SubsampleRatio,
		Rect:           src.Rect,
	}
	copy(dst.Y, src.Y)
	copy(dst.Cb, src.Cb)
	copy(dst.Cr, src.Cr)
	return dst
}

// ensure Codec satisfies govid.Codec at compile time.
var _ govid.Codec = (*Codec)(nil)

package vp8

import (
	"bytes"
	"image"

	govid "github.com/liqmix/govid"
)

// Codec implements govid.Codec for VP8 video.
type Codec struct {
	dec *Decoder
}

// NewCodec returns a new VP8 codec.
func NewCodec() *Codec {
	return &Codec{dec: NewDecoder()}
}

// Decode decodes a VP8 packet into a frame.
func (c *Codec) Decode(pkt govid.Packet) (*govid.Frame, error) {
	c.dec.Init(bytes.NewReader(pkt.Data), len(pkt.Data))
	fh, err := c.dec.DecodeFrameHeader()
	if err != nil {
		return nil, err
	}
	img, err := c.dec.DecodeFrame()
	if err != nil {
		return nil, err
	}
	// Deep copy — decoder reuses the buffer.
	ycbcr := deepCopyYCbCr(img)
	return &govid.Frame{
		YCbCr:     ycbcr,
		Timestamp: pkt.Timestamp,
		Width:     fh.Width,
		Height:    fh.Height,
	}, nil
}

// Flush resets the decoder state.
func (c *Codec) Flush() {
	c.dec = NewDecoder()
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

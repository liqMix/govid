package av1

import (
	"image"

	"github.com/liqmix/govid"
)

// Codec implements govid.Codec for AV1 video.
type Codec struct {
	dec *Decoder
}

// NewCodec creates a new AV1 codec.
func NewCodec() *Codec {
	return &Codec{dec: NewDecoder()}
}

// Decode decodes an AV1 packet into a video frame.
func (c *Codec) Decode(pkt govid.Packet) (*govid.Frame, error) {
	img, err := c.dec.DecodePacket(pkt.Data)
	if err != nil {
		return nil, err
	}
	if img == nil {
		return nil, nil
	}

	ycbcr := deepCopyYCbCr(img)
	return &govid.Frame{
		YCbCr:     ycbcr,
		Timestamp: pkt.Timestamp,
		Width:     img.Rect.Dx(),
		Height:    img.Rect.Dy(),
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

// Compile-time check that Codec satisfies govid.Codec.
var _ govid.Codec = (*Codec)(nil)

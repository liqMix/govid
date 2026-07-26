package ebiv

import (
	"fmt"
	"image"

	govid "github.com/liqmix/govid"
)

// defaultRingSize is the plan's triple buffer: one frame being decoded into,
// one on screen, and one in hand.
const defaultRingSize = 3

// Codec decodes EBIV packets. It implements govid.Codec.
//
// Plane buffers come from a fixed ring allocated on the first key frame and
// reused forever after, so steady-state decode does not allocate them. The ring
// bounds how long a decoded frame stays valid: after ring-size further decodes,
// its buffer is overwritten. With NewPlayer that is never a problem (the player
// holds at most three frames). With NewAsyncPlayer(depth) the decoder runs
// ahead, so size the ring with WithFrameRing(depth+2).
type Codec struct {
	ringSize int

	geo   geometry
	ready bool
	ring  []*image.YCbCr
	next  int

	// work is the padded reconstruction buffer coded frames decode into before
	// the visible region is copied to the ring. ref is the previous frame's
	// reconstruction, the reference an inter frame predicts from; golden holds
	// the GOP key frame's reconstruction, the second reference. dec owns the
	// reusable entropy tables and tile workers.
	work   *frameBuf
	ref    *frameBuf
	golden *frameBuf
	dec    *decodeState
}

// Option configures a Codec.
type Option func(*Codec)

// WithFrameRing sets how many plane buffers the decoder cycles through. It must
// be at least 2. See Codec for how to size it against an async player's queue
// depth.
func WithFrameRing(n int) Option {
	return func(c *Codec) {
		if n >= 2 {
			c.ringSize = n
		}
	}
}

// NewCodec returns a codec ready to decode an EBIV stream. Geometry is learned
// from the first key frame, so the codec needs no configuration.
func NewCodec(opts ...Option) *Codec {
	c := &Codec{ringSize: defaultRingSize}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Decode decodes one packet into a frame.
//
// The frame's pixel buffer belongs to the codec's ring and is reused; see
// Codec for the lifetime rules.
func (c *Codec) Decode(pkt govid.Packet) (*govid.Frame, error) {
	fh, body, err := parseFrameHeader(pkt.Data)
	if err != nil {
		return nil, err
	}

	if fh.Type == FrameKey {
		if err := c.configure(fh.Width, fh.Height); err != nil {
			return nil, err
		}
	} else if !c.ready {
		return nil, ErrNoKeyframe
	}

	img := c.ring[c.next]
	c.next++
	if c.next == len(c.ring) {
		c.next = 0
	}

	switch fh.Coding {
	case CodingRaw:
		if err := c.decodeRaw(img, body); err != nil {
			return nil, err
		}
	case CodingIntra, CodingInter:
		if err := c.decodeCoded(img, fh, body); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: coding mode %d", ErrUnsupported, uint8(fh.Coding))
	}

	// The Frame wrapper is the one per-frame allocation the govid.Codec
	// interface imposes; it is ~64 bytes and carries no pixel data.
	return &govid.Frame{
		YCbCr:     img,
		Timestamp: pkt.Timestamp,
		Width:     c.geo.W,
		Height:    c.geo.H,
	}, nil
}

// Flush drops decoder state so decoding can resume at a key frame after a seek.
// The plane ring survives, since its geometry has not changed.
func (c *Codec) Flush() {
	c.next = 0
}

// configure sizes the plane ring for a key frame's geometry. A mid-stream
// resolution change is not supported: it would invalidate every reference
// buffer, and the format's use case (a single offline-encoded clip) never
// produces one.
func (c *Codec) configure(w, h int) error {
	if c.ready {
		if w != c.geo.W || h != c.geo.H {
			return fmt.Errorf("%w: key frame is %dx%d, stream is %dx%d",
				ErrDimensions, w, h, c.geo.W, c.geo.H)
		}
		return nil
	}
	c.geo = geometryFor(w, h)
	c.ring = make([]*image.YCbCr, c.ringSize)
	for i := range c.ring {
		c.ring[i] = c.geo.newImage()
	}
	c.work = newFrameBuf(c.geo)
	c.ref = newFrameBuf(c.geo)
	c.golden = newFrameBuf(c.geo)
	c.dec = newDecodeState()
	c.next = 0
	c.ready = true
	return nil
}

// decodeCoded decodes a compressed frame into the padded work buffer, copies
// the visible region into the ring image, then promotes the work buffer to the
// reference for a following inter frame. A key frame's reconstruction is also
// copied into the golden buffer — the GOP's second reference — so a seek that
// lands on a key rebuilds golden identically to a straight-through decode.
func (c *Codec) decodeCoded(dst *image.YCbCr, fh frameHeader, body []byte) error {
	inter := fh.Coding == CodingInter
	key := fh.Type == FrameKey
	if err := c.dec.decodeCoded(c.work, c.ref, c.golden, body, inter, key); err != nil {
		return err
	}
	if key {
		c.golden.copyFrom(c.work)
	}
	c.work.storeImage(c.geo, dst)
	c.work, c.ref = c.ref, c.work
	return nil
}

// decodeRaw scatters tightly packed planes into the strided ring buffer.
func (c *Codec) decodeRaw(dst *image.YCbCr, body []byte) error {
	g := c.geo
	if want := g.packedSize(); len(body) != want {
		return fmt.Errorf("%w: raw frame is %d bytes, want %d", ErrCorrupt, len(body), want)
	}
	ySize := g.W * g.H
	cSize := g.CW * g.CH

	scatterPlane(dst.Y, dst.YStride, body[:ySize], g.W, g.H)
	scatterPlane(dst.Cb, dst.CStride, body[ySize:ySize+cSize], g.CW, g.CH)
	scatterPlane(dst.Cr, dst.CStride, body[ySize+cSize:], g.CW, g.CH)
	return nil
}

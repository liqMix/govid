package ebiv

import (
	"fmt"
	"image"
	"io"
)

// Encoder writes frames into an EBIV container.
//
// It is an offline tool with no real-time constraint: every expensive decision
// belongs on this side of the format. By default it emits lossless CodingRaw
// key frames; pass WithIntra to compress with the intra codec, and WithGOP to
// interleave motion-compensated inter frames.
type Encoder struct {
	w   *Writer
	geo geometry
	buf []byte

	coding     CodingMode
	qp         int
	gop        int // key-frame interval; 1 means all-intra
	tileCols   int
	tileRows   int
	autoTiles  int  // when >0 and no explicit grid, target this many tiles
	fastEncode bool // skip the second (real-cost RDOQ) encode pass

	ref       *frameBuf  // previous reconstruction, reference for an inter frame
	golden    *frameBuf  // the GOP key frame's reconstruction, the second reference
	sinceKF   int        // inter frames since the last key frame
	prevFreqs [][]uint32 // last shipped frequency vector per context (table deltas)
}

// EncoderOption configures a compressing encoder.
type EncoderOption func(*Encoder)

// WithIntra selects the compressed intra codec at the given quantizer. Lower qp
// means higher quality and larger files; qp is clamped to 0..63. Without WithGOP
// every frame is an independently seekable key frame.
func WithIntra(qp int) EncoderOption {
	return func(e *Encoder) {
		e.coding = CodingIntra
		e.qp = qp
	}
}

// WithGOP sets the key-frame interval for a compressing encoder: every gop-th
// frame is a key frame and the rest are inter frames predicted from their
// predecessor. A value of 1 keeps every frame intra. It has no effect on a raw
// stream. Shorter GOPs seek faster and bound error propagation; longer GOPs
// compress better.
func WithGOP(gop int) EncoderOption {
	return func(e *Encoder) {
		if gop >= 1 {
			e.gop = gop
		}
	}
}

// WithTiles splits each coded frame into a cols×rows grid of independently
// decodable tiles, which both the encoder and decoder spread across goroutines.
// Zero or one in a dimension means no split there. An explicit grid overrides
// WithAutoTiles.
func WithTiles(cols, rows int) EncoderOption {
	return func(e *Encoder) {
		e.tileCols = cols
		e.tileRows = rows
	}
}

// WithAutoTiles sizes the tile grid automatically to about `workers` tiles
// (pass runtime.NumCPU()), so encode and decode both use the whole machine
// without hand-tuning. It is ignored when WithTiles set an explicit grid. Tiles
// cost a little compression at their edges — a few percent of file size at
// equal quality — in exchange for roughly an order of magnitude faster encode
// and decode, the right trade for offline background animation.
func WithAutoTiles(workers int) EncoderOption {
	return func(e *Encoder) {
		e.autoTiles = workers
	}
}

// WithFastEncode skips the second encode pass (the M2 real-cost RDOQ
// re-encode), halving encode time for files that measure ~3.5% larger at
// matched PSNR. The bitstream is identical in format and decodes at the same
// speed; only the encoder's rate-distortion decisions are cheaper. Use it for
// iteration and previews; drop it for final assets if the last few percent
// matter.
func WithFastEncode() EncoderOption {
	return func(e *Encoder) {
		e.fastEncode = true
	}
}

// NewEncoder starts an EBIV stream on w. Close must be called to finish the
// container.
func NewEncoder(w io.Writer, cfg Config, opts ...EncoderOption) (*Encoder, error) {
	cw, err := NewWriter(w, cfg)
	if err != nil {
		return nil, err
	}
	geo := geometryFor(cfg.Width, cfg.Height)
	e := &Encoder{
		w:        cw,
		geo:      geo,
		buf:      make([]byte, 0, frameHeaderBase+frameHeaderKeyExtra+geo.packedSize()),
		coding:   CodingRaw,
		qp:       20,
		gop:      1,
		tileCols: cfg.TileCols,
		tileRows: cfg.TileRows,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e, nil
}

// FrameCount returns the number of frames written so far.
func (e *Encoder) FrameCount() int { return e.w.FrameCount() }

// WriteFrame encodes one frame. The image must be 4:2:0 with exactly the
// dimensions the encoder was configured for; its pixels are copied, so the
// caller may reuse the image immediately.
func (e *Encoder) WriteFrame(img *image.YCbCr) error {
	if img == nil {
		return fmt.Errorf("%w: nil image", ErrDimensions)
	}
	if !e.geo.matches(img) {
		return fmt.Errorf("%w: got %dx%d 4:2:0=%t, want %dx%d",
			ErrDimensions, img.Rect.Dx(), img.Rect.Dy(),
			img.SubsampleRatio == image.YCbCrSubsampleRatio420, e.geo.W, e.geo.H)
	}

	if e.coding == CodingRaw {
		return e.writeRaw(img)
	}
	return e.writeCoded(img)
}

func (e *Encoder) writeRaw(img *image.YCbCr) error {
	hdr := frameHeader{Type: FrameKey, Coding: CodingRaw, Width: e.geo.W, Height: e.geo.H}
	e.buf = hdr.appendTo(e.buf[:0])

	base := len(e.buf)
	e.buf = grow(e.buf, base+e.geo.packedSize())
	e.packPlanes(e.buf[base:], img)

	return e.w.WriteFrame(e.buf, true)
}

func (e *Encoder) writeCoded(img *image.YCbCr) error {
	// Keys land every gop frames: sinceKF counts the inter frames written since
	// the last key, so gop-1 of them fit between keys. (A >= e.gop test here is
	// the off-by-one that made gop=1 alternate I/P instead of all-intra.)
	key := e.ref == nil || e.sinceKF >= e.gop-1
	cols, rows := e.tileGrid()
	var (
		hdr     frameHeader
		payload []byte
		rec     *frameBuf
		freqs   [][]uint32
	)
	if key {
		hdr = frameHeader{Type: FrameKey, Coding: CodingIntra, Width: e.geo.W, Height: e.geo.H}
		payload, rec, freqs = encodeIntraFrame(e.geo, img, e.qp, cols, rows, !e.fastEncode)
		e.sinceKF = 0
		// The key frame's reconstruction is the GOP's golden reference. Frame
		// encoders allocate a fresh rec per frame, so holding the pointer is
		// enough — nothing overwrites it until the next key replaces it.
		e.golden = rec
		// A key frame's tables are self-contained; the delta history restarts
		// here, mirroring the decoder's wipe so a seek can never desync.
		e.prevFreqs = make([][]uint32, numContexts)
	} else {
		hdr = frameHeader{Type: FrameInter, Coding: CodingInter}
		payload, rec, freqs = encodeInterFrame(e.geo, img, e.ref, e.golden, e.qp, cols, rows, e.prevFreqs, !e.fastEncode)
		e.sinceKF++
	}
	e.ref = rec
	// prev keeps the last *shipped* vector per context: a context unused this
	// frame retains its older reference, matching parseTablesInto.
	for c, f := range freqs {
		if f != nil {
			e.prevFreqs[c] = f
		}
	}

	e.buf = hdr.appendTo(e.buf[:0])
	e.buf = append(e.buf, payload...)
	return e.w.WriteFrame(e.buf, key)
}

// tileGrid resolves the tile grid for a coded frame: an explicit WithTiles grid
// wins; otherwise WithAutoTiles sizes one to the machine; otherwise a single
// tile.
func (e *Encoder) tileGrid() (cols, rows int) {
	if e.tileCols > 1 || e.tileRows > 1 {
		return e.tileCols, e.tileRows
	}
	if e.autoTiles > 1 {
		mbCols := (e.geo.W + mbSize - 1) / mbSize
		mbRows := (e.geo.H + mbSize - 1) / mbSize
		return autoTileGrid(mbCols, mbRows, e.autoTiles)
	}
	return e.tileCols, e.tileRows
}

// packPlanes gathers img's strided planes into the tightly packed on-disk
// layout: Y, then Cb, then Cr.
func (e *Encoder) packPlanes(dst []byte, img *image.YCbCr) {
	g := e.geo
	ySize := g.W * g.H
	cSize := g.CW * g.CH

	gatherPlane(dst[:ySize], img.Y, img.YStride, g.W, g.H)
	gatherPlane(dst[ySize:ySize+cSize], img.Cb, img.CStride, g.CW, g.CH)
	gatherPlane(dst[ySize+cSize:], img.Cr, img.CStride, g.CW, g.CH)
}

// Close writes the frame index and footer.
func (e *Encoder) Close() error { return e.w.Close() }

// grow returns b resliced to n bytes, reallocating only if its capacity is too
// small. The encoder's buffer reaches its final size on the first frame and is
// reused from then on.
func grow(b []byte, n int) []byte {
	if cap(b) < n {
		grown := make([]byte, n)
		copy(grown, b)
		return grown
	}
	return b[:n]
}

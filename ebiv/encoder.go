package ebiv

import (
	"encoding/binary"
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
	golden    *frameBuf  // the GOP's golden reference reconstruction
	sinceKF   int        // inter frames since the last key frame
	prevFreqs [][]uint32 // last shipped frequency vector per context (table deltas)
	prevY     []byte     // previous source luma, packed — scene-cut detection

	// pending buffers the current GOP's source frames; its head becomes the
	// next key frame. Buffering is what lets the encoder see the future: the
	// alt-ref golden override is filtered from the frames the GOP will
	// actually contain, and a scene cut can restart the GOP at the cut.
	pending   []*image.YCbCr
	accepted  int // frames accepted by WriteFrame, including still-buffered ones
	altRefWin int // temporal-filter window for the golden override; 0 disables
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

// WithAltRef sets the temporal-filter window for the alt-ref golden override:
// each key frame carries a hidden synthetic reference built by averaging up
// to window source frames wherever they agree with the key (a
// motion-rejecting temporal denoiser). Inter frames may then predict from a
// cleaner image than any single lossy frame — the libvpx alt-ref idea shaped
// to this container.
//
// Off by default (window 0): on the BGA corpus it measured 1–2.5% LARGER at
// matched PSNR, because that content's apparent noise — twinkling lights,
// shimmer — is deliberate animation, not sensor noise; averaging it away
// builds a reference nothing resembles, and the override payload still has to
// be paid for. Enable it only for true film-grain sources, where temporal
// averaging recovers the underlying image.
func WithAltRef(window int) EncoderOption {
	return func(e *Encoder) {
		if window >= 0 {
			e.altRefWin = window
		}
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
		w:         cw,
		geo:       geo,
		buf:       make([]byte, 0, frameHeaderBase+frameHeaderKeyExtra+geo.packedSize()),
		coding:    CodingRaw,
		qp:        20,
		gop:       1,
		tileCols: cfg.TileCols,
		tileRows: cfg.TileRows,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e, nil
}

// FrameCount returns the number of frames accepted so far, including frames
// still buffered in the current GOP (they are written no later than Close).
func (e *Encoder) FrameCount() int { return e.accepted }

// WriteFrame encodes one frame. The image must be 4:2:0 with exactly the
// dimensions the encoder was configured for; its pixels are copied, so the
// caller may reuse the image immediately. Coded frames may be buffered up to
// one GOP before being written; Close flushes the remainder.
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
	if err := e.writeCoded(img); err != nil {
		return err
	}
	e.accepted++
	return nil
}

func (e *Encoder) writeRaw(img *image.YCbCr) error {
	hdr := frameHeader{Type: FrameKey, Coding: CodingRaw, Width: e.geo.W, Height: e.geo.H}
	e.buf = hdr.appendTo(e.buf[:0])

	base := len(e.buf)
	e.buf = grow(e.buf, base+e.geo.packedSize())
	e.packPlanes(e.buf[base:], img)

	return e.w.WriteFrame(e.buf, true)
}

// Scene-cut keyframe placement. A key on a fixed cadence can land just after
// a cut, paying the cut twice: a burst of intra-in-inter macroblocks at the
// cut (measured at ~5% of macroblocks on real MV content), then a full key a
// frame later. Detecting the cut and starting the GOP there converts the
// burst into the key it wanted to be. The scheduled cadence still caps the
// interval, so seek granularity is unchanged.
const (
	// sceneCutAvgSAD is the average per-pixel luma difference above which a
	// frame is treated as starting a new scene. Pans and motion sit well
	// below it; hard cuts sit well above.
	sceneCutAvgSAD = 28
	// sceneCutMinGap is the minimum frames between a key and a cut-forced
	// key, so strobing content cannot spam keys — returning flashes are the
	// golden reference's job, not the keyframe's.
	sceneCutMinGap = 8
)

// sceneCut reports whether img starts a new scene relative to the previous
// source frame, and records img as the new reference for the next call.
func (e *Encoder) sceneCut(img *image.YCbCr) bool {
	w, h := e.geo.W, e.geo.H
	first := e.prevY == nil
	if first {
		e.prevY = make([]byte, w*h)
	}
	var sad int64
	if !first {
		for y := 0; y < h; y++ {
			row := img.Y[y*img.YStride : y*img.YStride+w]
			prev := e.prevY[y*w : y*w+w]
			for x := 0; x < w; x++ {
				d := int32(row[x]) - int32(prev[x])
				sad += int64(abs32(d))
			}
		}
	}
	gatherPlane(e.prevY, img.Y, img.YStride, w, h)
	return !first && sad > int64(w*h)*sceneCutAvgSAD
}

// writeCoded buffers the frame into the current GOP. The GOP flushes when it
// reaches the scheduled length, or early when a scene cut arrives (min-gap
// guarded) so the cut frame starts the next GOP as its key. Buffering is what
// gives the encoder its future: the alt-ref override filters the frames the
// GOP will actually contain.
func (e *Encoder) writeCoded(img *image.YCbCr) error {
	if e.sceneCut(img) && len(e.pending) >= sceneCutMinGap {
		if err := e.flushGOP(); err != nil {
			return err
		}
	}
	e.pending = append(e.pending, cloneImage(img))
	if len(e.pending) >= e.gop {
		return e.flushGOP()
	}
	return nil
}

// flushGOP encodes the buffered GOP: its head as a key frame (with the
// alt-ref golden override when the filter produced one), the rest as inter
// frames.
func (e *Encoder) flushGOP() error {
	if len(e.pending) == 0 {
		return nil
	}
	frames := e.pending
	e.pending = nil
	cols, rows := e.tileGrid()

	altref := e.buildAltRef(frames)
	hdr := frameHeader{
		Type: FrameKey, Coding: CodingIntra,
		Width: e.geo.W, Height: e.geo.H,
		GoldenOverride: altref != nil,
	}
	payload, rec, freqs := encodeIntraFrame(e.geo, frames[0], e.qp, cols, rows, !e.fastEncode)
	// A key frame's tables are self-contained; the delta history restarts
	// here, mirroring the decoder's wipe so a seek can never desync.
	e.prevFreqs = make([][]uint32, numContexts)
	e.mergeFreqs(freqs)
	e.ref = rec
	e.golden = rec
	e.sinceKF = 0

	e.buf = hdr.appendTo(e.buf[:0])
	if altref != nil {
		// The override codes the filtered reference as an inter frame
		// predicted from the key it rides on, chaining the table history the
		// same way the decoder will replay it.
		ovPayload, ovRec, ovFreqs := encodeInterFrame(e.geo, altref, rec, nil, e.qp, cols, rows, e.prevFreqs, !e.fastEncode)
		e.mergeFreqs(ovFreqs)
		e.golden = ovRec
		var lenBuf [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(lenBuf[:], uint64(len(payload)))
		e.buf = append(e.buf, lenBuf[:n]...)
		e.buf = append(e.buf, payload...)
		e.buf = append(e.buf, ovPayload...)
	} else {
		e.buf = append(e.buf, payload...)
	}
	if err := e.w.WriteFrame(e.buf, true); err != nil {
		return err
	}

	for _, f := range frames[1:] {
		hdr := frameHeader{Type: FrameInter, Coding: CodingInter}
		payload, rec, freqs := encodeInterFrame(e.geo, f, e.ref, e.golden, e.qp, cols, rows, e.prevFreqs, !e.fastEncode)
		e.mergeFreqs(freqs)
		e.ref = rec
		e.sinceKF++
		e.buf = hdr.appendTo(e.buf[:0])
		e.buf = append(e.buf, payload...)
		if err := e.w.WriteFrame(e.buf, false); err != nil {
			return err
		}
	}
	return nil
}

// mergeFreqs keeps the last *shipped* vector per context: a context unused in
// a frame retains its older reference, matching parseTablesInto.
func (e *Encoder) mergeFreqs(freqs [][]uint32) {
	for c, f := range freqs {
		if f != nil {
			e.prevFreqs[c] = f
		}
	}
}

// altRefBlockSAD is the per-pixel agreement threshold for the temporal
// filter: a 16×16 block of a window frame joins the average only when it
// matches the key this closely, so motion is rejected and only hold/noise
// variation is averaged away.
const altRefBlockSAD = 6

// buildAltRef builds the golden-override image: the GOP head, temporally
// denoised by averaging every window frame's blocks that agree with it. It
// returns nil when the window is too small or nothing agreed — then the key's
// own reconstruction serves as golden, exactly as before.
func (e *Encoder) buildAltRef(frames []*image.YCbCr) *image.YCbCr {
	n := min(e.altRefWin, len(frames))
	if n < 3 {
		return nil
	}
	g := e.geo
	w, h := g.W, g.H
	cw, ch := g.CW, g.CH
	target := frames[0]

	sumY := make([]uint32, w*h)
	cntY := make([]uint16, w*h)
	sumCb := make([]uint32, cw*ch)
	sumCr := make([]uint32, cw*ch)
	cntC := make([]uint16, cw*ch)

	accPlane := func(sum []uint32, cnt []uint16, src []byte, stride, x0, y0, x1, y1, pw int) {
		for y := y0; y < y1; y++ {
			row := src[y*stride:]
			for x := x0; x < x1; x++ {
				sum[y*pw+x] += uint32(row[x])
				if cnt != nil {
					cnt[y*pw+x]++
				}
			}
		}
	}
	// The key contributes everywhere.
	accPlane(sumY, cntY, target.Y, target.YStride, 0, 0, w, h, w)
	accPlane(sumCb, cntC, target.Cb, target.CStride, 0, 0, cw, ch, cw)
	accPlane(sumCr, nil, target.Cr, target.CStride, 0, 0, cw, ch, cw)

	accepted := 0
	for j := 1; j < n; j++ {
		f := frames[j]
		for by := 0; by < h; by += mbSize {
			bh := min(mbSize, h-by)
			for bx := 0; bx < w; bx += mbSize {
				bw := min(mbSize, w-bx)
				var sad int64
				for y := by; y < by+bh; y++ {
					trow := target.Y[y*target.YStride:]
					frow := f.Y[y*f.YStride:]
					for x := bx; x < bx+bw; x++ {
						sad += int64(abs32(int32(trow[x]) - int32(frow[x])))
					}
				}
				if sad > int64(bw*bh)*altRefBlockSAD {
					continue
				}
				accepted++
				accPlane(sumY, cntY, f.Y, f.YStride, bx, by, bx+bw, by+bh, w)
				cx0, cy0 := bx/2, by/2
				cx1, cy1 := min(cw, (bx+bw+1)/2), min(ch, (by+bh+1)/2)
				accPlane(sumCb, cntC, f.Cb, f.CStride, cx0, cy0, cx1, cy1, cw)
				accPlane(sumCr, nil, f.Cr, f.CStride, cx0, cy0, cx1, cy1, cw)
			}
		}
	}
	if accepted == 0 {
		return nil
	}

	out := &image.YCbCr{
		Y: make([]byte, w*h), Cb: make([]byte, cw*ch), Cr: make([]byte, cw*ch),
		YStride: w, CStride: cw,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, w, h),
	}
	for i := range out.Y {
		c := uint32(cntY[i])
		out.Y[i] = byte((sumY[i] + c/2) / c)
	}
	for i := range out.Cb {
		c := uint32(cntC[i])
		out.Cb[i] = byte((sumCb[i] + c/2) / c)
		out.Cr[i] = byte((sumCr[i] + c/2) / c)
	}
	return out
}

// cloneImage deep-copies a frame so the caller may reuse its buffers while
// the frame waits in the GOP buffer.
func cloneImage(img *image.YCbCr) *image.YCbCr {
	dst := *img
	dst.Y = append([]byte(nil), img.Y...)
	dst.Cb = append([]byte(nil), img.Cb...)
	dst.Cr = append([]byte(nil), img.Cr...)
	return &dst
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

// Close flushes any buffered GOP, then writes the frame index and footer.
func (e *Encoder) Close() error {
	if err := e.flushGOP(); err != nil {
		return err
	}
	return e.w.Close()
}

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

package ebiv

import (
	"image"
	"math"
)

// Intra frame encoder. It is offline and may be slow: it chooses a transform
// size per macroblock by rate-distortion trial and an intra mode per block by
// prediction error, reconstructs exactly as the decoder will, and hands the
// resulting token streams to the entropy layer.
//
// Tiles encode concurrently. Within a tile the macroblock chain is serial —
// each block's intra prediction reads its neighbor's reconstruction — so the
// tile is the unit of parallelism, and everything inside it (motion search, the
// transform-size trials, the DCTs) runs in parallel by virtue of running whole
// tiles at once. Tiles write disjoint macroblock regions and read only their
// own reconstruction plus the read-only reference, so no locking is needed.

// blockScratch holds the per-block working arrays so the hot loops never
// allocate. Each tile worker owns one, so concurrent tiles never share it.
type blockScratch struct {
	pred     [maxLevels]int32
	residual [maxLevels]int32
	coeffs   [maxLevels]int32
	levels   [maxLevels]int32
	recon    [maxLevels]int32
}

// frameEncoder holds the state shared across a frame's tiles. Everything a tile
// mutates is either disjoint per tile (the reconstruction region, the motion
// grid cells) or private to the tile worker (its token stream and scratch).
type frameEncoder struct {
	g       geometry
	src     *frameBuf
	rec     *frameBuf
	ref     *frameBuf // previous reconstruction; nil for an intra frame
	q       quantizer
	grid    tileGrid
	streams []*tileStream
	counts  [][]uint32
	lambda  int64
	lambdaF float64

	// rdoq holds the real token costs from pass 1; when set, codeResidual
	// quantizes rate-distortion-optimally instead of with the dead zone.
	rdoq costModel

	// mv is the reconstructed motion vector per macroblock, used to predict the
	// next macroblock's vector. Each tile writes only its own cells and reads
	// only within its own bounds. mvStride is mbCols.
	mv       []motionVector
	mvStride int
}

// tileEncoder is one tile's worker: a pointer to the shared frame state plus
// private scratch, so tiles run concurrently without sharing mutable memory.
// The scratch token streams let the inter path tokenize each component before
// committing, so the coded-block pattern can precede the tokens it describes.
type tileEncoder struct {
	fe       *frameEncoder
	sc       blockScratch
	lumaToks tileStream
	cbToks   tileStream
	crToks   tileStream
}

// newFrameEncoder builds an encoder over a source image, shared by the intra
// and inter paths. ref is the reference frame for inter coding, or nil.
func newFrameEncoder(g geometry, img *image.YCbCr, ref *frameBuf, qp, tileCols, tileRows int) *frameEncoder {
	src := newFrameBuf(g)
	src.loadImage(g, img)
	rec := newFrameBuf(g)

	e := &frameEncoder{
		g:        g,
		src:      src,
		rec:      rec,
		ref:      ref,
		q:        newQuantizer(qp),
		grid:     newTileGrid(src.mbCols, src.mbRows, tileCols, tileRows),
		mv:       make([]motionVector, src.mbCols*src.mbRows),
		mvStride: src.mbCols,
	}
	qac := int64(e.q.ac)
	e.lambda = qac * qac / 6 // rate weight in the SSE domain
	e.lambdaF = float64(e.lambda)
	e.counts = make([][]uint32, numContexts)
	for c := range e.counts {
		e.counts[c] = make([]uint32, alphabetSizes[c])
	}
	return e
}

// encodeTilesParallel encodes every tile across the shared worker pool. The
// per-tile token streams are independent, so the result is identical regardless
// of the order the tiles finish.
func (e *frameEncoder) encodeTilesParallel(inter bool) {
	e.streams = make([]*tileStream, e.grid.count())
	for i := range e.streams {
		e.streams[i] = &tileStream{}
	}
	sharedPool().run(len(e.streams), func(i int) {
		te := &tileEncoder{fe: e}
		b := e.grid.bounds(i)
		if inter {
			te.encodeInterTile(e.streams[i], b)
		} else {
			te.encodeIntraTile(e.streams[i], b)
		}
	})
}

// finish aggregates counts across tiles, builds the shared tables, entropy-codes
// each tile, and assembles the frame payload. prev is the previous frame's
// shipped frequency vectors (nil for a key frame — tables must be
// self-contained); the returned freqs become the caller's prev for the next
// frame. The bypass context is forced uniform and never shipped.
func (e *frameEncoder) finish(qp int, prev [][]uint32) ([]byte, *frameBuf, [][]uint32) {
	for _, s := range e.streams {
		for _, t := range s.toks {
			e.counts[t.ctx][t.sym]++
		}
	}
	freqs := make([][]uint32, numContexts)
	tables := make([]ransTable, numContexts)
	for c := 0; c < numContexts; c++ {
		if c == ctxBypass {
			continue // fixed uniform, installed below
		}
		freqs[c] = normalizeFreqs(e.counts[c])
		tables[c] = buildTable(freqOrZero(freqs[c], alphabetSizes[c]), false)
	}
	installBypass(tables, false)
	tableBytes := serializeTables(freqs, prev)

	tileBytes := make([][]byte, len(e.streams))
	for i, s := range e.streams {
		tileBytes[i] = ransEncode(s.toks, tables)
	}
	hdr := codedHeader{qp: qp, tileCols: e.grid.cols, tileRows: e.grid.rows}
	return assemblePayload(hdr, tableBytes, tileBytes), e.rec, freqs
}

// encodeIntraFrame codes one image as a key frame and returns the frame
// payload (without the outer frame-type byte), the reconstructed buffer that
// becomes the reference for a following inter frame, and the shipped frequency
// vectors for table delta-coding.
func encodeIntraFrame(g geometry, img *image.YCbCr, qp, tileCols, tileRows int) ([]byte, *frameBuf, [][]uint32) {
	e := newFrameEncoder(g, img, nil, qp, tileCols, tileRows)
	e.encodeTwoPass(false)
	return e.finish(qp, nil)
}

// encodeTwoPass runs the M2 real-cost encode: pass 1 quantizes with the dead
// zone to learn the token statistics, then pass 2 re-encodes with RDOQ pricing
// against those real costs. The second pass reconstructs from scratch, so rec
// ends bit-exact with the decoder.
func (e *frameEncoder) encodeTwoPass(inter bool) {
	e.encodeTilesParallel(inter)
	e.rdoq = e.measureCosts()
	e.resetForPass2()
	e.encodeTilesParallel(inter)
}

// measureCosts aggregates pass-1 token counts and returns the real per-context
// coding cost in bits, −log2(freq/M). An unused or absent symbol is priced high
// so RDOQ never leans on a token the frame does not actually use.
func (e *frameEncoder) measureCosts() costModel {
	counts := make([][]uint32, numContexts)
	for c := range counts {
		counts[c] = make([]uint32, alphabetSizes[c])
	}
	for _, s := range e.streams {
		for _, t := range s.toks {
			counts[t.ctx][t.sym]++
		}
	}
	cost := make(costModel, numContexts)
	for c := 0; c < numContexts; c++ {
		freq := normalizeFreqs(counts[c])
		cost[c] = make([]float64, alphabetSizes[c])
		for s := range cost[c] {
			var f uint32
			if freq != nil {
				f = freq[s]
			}
			if f == 0 {
				cost[c][s] = 20 // ~ log2(M) worst case
			} else {
				cost[c][s] = math.Log2(float64(ransM) / float64(f))
			}
		}
	}
	return cost
}

// resetForPass2 wipes the per-frame mutable state so the second pass encodes
// into a fresh reconstruction, keeping the source, reference, and cost model.
func (e *frameEncoder) resetForPass2() {
	e.rec = newFrameBuf(e.g)
	e.streams = nil
	for c := range e.counts {
		for i := range e.counts[c] {
			e.counts[c][i] = 0
		}
	}
	for i := range e.mv {
		e.mv[i] = motionVector{}
	}
}

// freqOrZero returns freq, or an all-zero vector of the given size when the
// context was unused, so buildTable can mark it unused consistently.
func freqOrZero(freq []uint32, size int) []uint32 {
	if freq != nil {
		return freq
	}
	return make([]uint32, size)
}

func (te *tileEncoder) encodeIntraTile(s *tileStream, b tileBounds) {
	for mby := b.mbY0; mby < b.mbY1; mby++ {
		for mbx := b.mbX0; mbx < b.mbX1; mbx++ {
			te.encodeLumaMB(s, mbx, mby, b)
			te.encodeChromaMB(s, mbx, mby, b)
		}
	}
}

// encodeLumaMB picks the transform size with the lowest rate-distortion cost,
// then commits it: emits its tokens and leaves the reconstruction in place.
func (te *tileEncoder) encodeLumaMB(s *tileStream, mbx, mby int, b tileBounds) {
	bestTx, bestCost := 0, int64(1)<<62
	for txIdx := range lumaTxSizes {
		cost := te.lumaMBCost(mbx, mby, b, txIdx)
		if cost < bestCost {
			bestCost, bestTx = cost, txIdx
		}
	}
	s.put(ctxTxSize, bestTx)
	te.codeLumaMB(s, mbx, mby, b, bestTx)
}

// lumaMBCost reconstructs a macroblock at one transform size and returns
// SSE + lambda*bits, without emitting tokens. The reconstruction it leaves
// behind is overwritten by the committing pass.
func (te *tileEncoder) lumaMBCost(mbx, mby int, b tileBounds, txIdx int) int64 {
	var trial tileStream
	sse := te.codeLumaMB(&trial, mbx, mby, b, txIdx)
	return sse + te.fe.lambda*int64(te.rdBits(trial.toks))
}

// codeLumaMB reconstructs every block of a luma macroblock at the given
// transform size, emitting tokens into s (which may be a throwaway stream for a
// cost trial), and returns the reconstruction SSE against the source.
func (te *tileEncoder) codeLumaMB(s *tileStream, mbx, mby int, b tileBounds, txIdx int) int64 {
	fe := te.fe
	n := lumaTxSizes[txIdx]
	tileLeft := b.mbX0 * mbSize
	tileTop := b.mbY0 * mbSize
	var sse int64
	tileRight := b.mbX1 * mbSize
	mbTop, mbRight := mby*mbSize, (mbx+1)*mbSize
	for by := 0; by < mbSize; by += n {
		for bx := 0; bx < mbSize; bx += n {
			x0 := mbx*mbSize + bx
			y0 := mby*mbSize + by
			avAbove := y0 > tileTop
			avLeft := x0 > tileLeft
			avAR := aboveRightAvailable(x0, y0, n, tileTop, tileRight, mbTop, mbRight)
			mode := te.chooseIntraMode(fe.src.y, fe.rec.y, x0, y0, n, avAbove, avLeft, avAR)
			s.put(ctxLumaMode, mode)
			sse += te.codeBlock(s, planeLuma, txIdx, fe.src.y, fe.rec.y, x0, y0, n, mode, avAbove, avLeft, avAR)
		}
	}
	return sse
}

func (te *tileEncoder) encodeChromaMB(s *tileStream, mbx, mby int, b tileBounds) {
	fe := te.fe
	tileLeft := b.mbX0 * chromaMB
	tileTop := b.mbY0 * chromaMB
	x0 := mbx * chromaMB
	y0 := mby * chromaMB
	avAbove := y0 > tileTop
	avLeft := x0 > tileLeft

	// One mode covers both chroma planes, chosen on their combined prediction
	// error. A macroblock's chroma is a single 8×8 block, so its above-right
	// (the next macroblock's chroma, same row) is never reconstructed.
	best, bestErr := modeDC, int64(1)<<62
	for mode := 0; mode < numIntraModes; mode++ {
		err := te.predErr(fe.src.cb, fe.rec.cb, x0, y0, chromaMB, mode, avAbove, avLeft, false) +
			te.predErr(fe.src.cr, fe.rec.cr, x0, y0, chromaMB, mode, avAbove, avLeft, false)
		if err < bestErr {
			bestErr, best = err, mode
		}
	}
	s.put(ctxChromaMode, best)
	te.codeBlock(s, planeChroma, 0, fe.src.cb, fe.rec.cb, x0, y0, chromaMB, best, avAbove, avLeft, false)
	te.codeBlock(s, planeChroma, 0, fe.src.cr, fe.rec.cr, x0, y0, chromaMB, best, avAbove, avLeft, false)
}

// codeBlock predicts intra, then codes and reconstructs the residual, returning
// its reconstruction SSE.
func (te *tileEncoder) codeBlock(s *tileStream, plane, txIdx int, src, rec planeView, x0, y0, n, mode int, avAbove, avLeft, avAR bool) int64 {
	predictIntra(rec, x0, y0, n, mode, avAbove, avLeft, avAR, te.sc.pred[:])
	return te.codeResidual(s, plane, txIdx, src, rec, x0, y0, n)
}

// codeResidual transforms, quantizes, entropy-codes, and reconstructs a block
// against the prediction already sitting in te.sc.pred, returning its
// reconstruction SSE. Both the intra and inter paths funnel through here.
func (te *tileEncoder) codeResidual(s *tileStream, plane, txIdx int, src, rec planeView, x0, y0, n int) int64 {
	sc := &te.sc
	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			sc.residual[r*n+c] = int32(src.at(x0+c, y0+r)) - sc.pred[r*n+c]
		}
	}
	forwardDCT(sc.residual[:n*n], sc.coeffs[:n*n], n)
	if te.fe.rdoq != nil {
		te.fe.q.quantizeRDOQ(sc.coeffs[:n*n], sc.levels[:n*n], n, plane, txIdx, te.fe.lambdaF, te.fe.rdoq)
	} else {
		te.fe.q.quantize(sc.coeffs[:n*n], sc.levels[:n*n], n) // pass 1: dead zone
	}
	encodeBlock(s, plane, txIdx, sc.levels[:n*n], n)

	// Reconstruct exactly as the decoder will.
	te.fe.q.dequantize(sc.levels[:n*n], sc.coeffs[:n*n], n)
	inverseDCT(sc.coeffs[:n*n], sc.recon[:n*n], n)

	var sse int64
	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			rv := int32(clampByte(sc.pred[r*n+c] + sc.recon[r*n+c]))
			rec.set(x0+c, y0+r, byte(rv))
			d := int64(rv) - int64(src.at(x0+c, y0+r))
			sse += d * d
		}
	}
	return sse
}

// chooseIntraMode picks the prediction mode with the smallest absolute
// prediction error against the source.
func (te *tileEncoder) chooseIntraMode(src, rec planeView, x0, y0, n int, avAbove, avLeft, avAR bool) int {
	best, bestErr := modeDC, int64(1)<<62
	for mode := 0; mode < numIntraModes; mode++ {
		if err := te.predErr(src, rec, x0, y0, n, mode, avAbove, avLeft, avAR); err < bestErr {
			bestErr, best = err, mode
		}
	}
	return best
}

// predErr scores a prediction mode by the SATD of its residual — a Hadamard
// metric that predicts coded cost far better than SAD, so the wider directional
// mode set only wins when it genuinely reduces the residual, not merely the raw
// pixel error.
func (te *tileEncoder) predErr(src, rec planeView, x0, y0, n, mode int, avAbove, avLeft, avAR bool) int64 {
	sc := &te.sc
	predictIntra(rec, x0, y0, n, mode, avAbove, avLeft, avAR, sc.pred[:])
	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			sc.residual[r*n+c] = int32(src.at(x0+c, y0+r)) - sc.pred[r*n+c]
		}
	}
	return satd(sc.residual[:n*n], n)
}

// rdBits prices a token run for a rate-distortion decision: real per-context
// costs from pass 1 when available (pass 2), else the nominal estimate (pass 1).
func (te *tileEncoder) rdBits(toks []entToken) int {
	if te.fe.rdoq != nil {
		var b float64
		for _, t := range toks {
			b += te.fe.rdoq.bits(int(t.ctx), int(t.sym))
		}
		return int(b + 0.5)
	}
	return estimateBits(toks)
}

// estimateBits is a nominal per-token cost used only to order transform-size
// choices; the real cost comes from the entropy tables, but relative ordering
// is enough here.
func estimateBits(toks []entToken) int {
	bits := 0
	for _, t := range toks {
		switch {
		case t.ctx >= ctxTokenBase:
			switch t.sym {
			case tEOB:
				bits += 2
			case tZero:
				bits += 1
			case tEscape:
				bits += 6
			default:
				bits += 4
			}
		case t.ctx == ctxSign || t.ctx == ctxBypass:
			bits++
		case t.ctx == ctxMVClassX || t.ctx == ctxMVClassY || t.ctx == ctxEscClass:
			bits += 3
		case t.ctx == ctxMBMode || t.ctx == ctxCBP:
			bits += 2
		default:
			bits += 3
		}
	}
	return bits
}

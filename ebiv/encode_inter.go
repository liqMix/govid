package ebiv

import "image"

// Inter frame encoder. Each macroblock is coded one of three ways, chosen by
// rate-distortion: skipped (motion-compensated at the predicted MV, no
// residual — one token), inter (explicit MV delta plus a coded-block pattern
// and residual), or intra. All motion-search effort lives here so the decoder
// only ever samples where it is told (§3.4).
//
// Like the intra path, tiles encode concurrently; the per-macroblock methods
// hang off tileEncoder so each tile has private scratch.

// searchStart is the initial diamond-search step in full pixels. It halves each
// time the search stops improving, so the reachable displacement is unbounded
// in principle but converges quickly around a good predictor.
const searchStart = 16

func encodeInterFrame(g geometry, img *image.YCbCr, ref, golden *frameBuf, qp, tileCols, tileRows int, prev [][]uint32, twoPass bool) ([]byte, *frameBuf, [][]uint32) {
	e := newFrameEncoder(g, img, ref, golden, qp, tileCols, tileRows)
	e.encode(true, twoPass)
	return e.finish(qp, prev)
}

func (te *tileEncoder) encodeInterTile(s *tileStream, b tileBounds) {
	for mby := b.mbY0; mby < b.mbY1; mby++ {
		for mbx := b.mbX0; mbx < b.mbX1; mbx++ {
			te.encodeInterMB(s, mbx, mby, b)
		}
	}
}

// encodeInterMB races skip, inter (across partition shapes and both
// references), and intra coding for one macroblock and commits the winner.
// Trials reconstruct into rec as they go; the committing pass re-runs the
// winner so rec ends bit-exact with the decoder.
func (te *tileEncoder) encodeInterMB(s *tileStream, mbx, mby int, b tileBounds) {
	fe := te.fe
	idx := mby*fe.mvStride + mbx
	pred := predictMV(fe.mv, fe.mvStride, mbx, mby, b)

	// Skip candidate: prediction at the predicted MV, zero residual.
	skipSSE := te.mcMBSSE(fe.ref, mbx, mby, pred)
	skipCost := skipSSE + fe.lambda*2

	// Early skip: when the whole-macroblock prediction already sits at the
	// noise floor, no rival can reclaim enough distortion to pay for its extra
	// tokens — commit skip without searching or trialing anything. This is the
	// converged-static case that dominates background animation, and it is
	// what makes encode time scale with motion instead of area.
	if skipSSE <= fe.lambda*8 {
		s.put(ctxMBMode, mbSkip)
		te.reconMC(mbx, mby, pred)
		fe.mv[idx] = pred
		return
	}

	// Inter candidate: pick the partition shape with the lowest RD cost. Each
	// partition searches its own motion vector; partition 0 predicts from the
	// macroblock predictor, the rest from partition 0. Pass 2 reuses pass 1's
	// vectors and gates — the search inputs are identical — and only re-prices.
	cache := &fe.mvSearch[idx]
	pass2 := fe.rdoq != nil
	bestPart := partWhole
	var bestMVs [4]motionVector

	// Whole partition first: its match quality gates everything rarer.
	if !pass2 {
		cache.last[partWhole][0] = te.searchMVRect(fe.ref, mbx, mby, 0, 0, mbSize, mbSize, pred)
	}
	bestMVs[0] = cache.last[partWhole][0]
	var trial tileStream
	sse := te.codeInterMB(&trial, fe.ref, mbx, mby, partWhole, bestMVs[:1], pred)
	bestInterCost := sse + fe.lambda*int64(te.rdBits(trial.toks)+3)

	if !pass2 {
		wholeSAD := te.rectSAD(fe.ref, mbx, mby, 0, 0, mbSize, mbSize, bestMVs[0])
		// Sub-partitions pay extra vectors; they only win when the whole-block
		// match is genuinely poor (avg |error| above ~2/pixel).
		cache.trySubs = wholeSAD > 2*mbSize*mbSize
		// Golden's case is a pose the last frame lost: probe it at zero motion
		// and only search when the probe already beats the last-ref match.
		cache.tryGolden = fe.golden != nil &&
			te.rectSAD(fe.golden, mbx, mby, 0, 0, mbSize, mbSize, motionVector{}) < wholeSAD
		if cache.tryGolden {
			cache.golden = te.searchMVRect(fe.golden, mbx, mby, 0, 0, mbSize, mbSize, motionVector{})
		}
	}

	if cache.trySubs {
		for part := partWhole + 1; part < numPartModes; part++ {
			rects := partRects[part]
			var mvs [4]motionVector
			for i, rc := range rects {
				if pass2 {
					mvs[i] = cache.last[part][i]
					continue
				}
				pp := pred
				if i > 0 {
					pp = mvs[0]
				}
				mvs[i] = te.searchMVRect(fe.ref, mbx, mby, rc[0], rc[1], rc[2], rc[3], pp)
				cache.last[part][i] = mvs[i]
			}
			var trial tileStream
			sse := te.codeInterMB(&trial, fe.ref, mbx, mby, part, mvs[:len(rects)], pred)
			cost := sse + fe.lambda*int64(te.rdBits(trial.toks)+3)
			if cost < bestInterCost {
				bestInterCost = cost
				bestPart = part
				copy(bestMVs[:], mvs[:])
			}
		}
	}

	// Golden candidate: whole-macroblock motion against the GOP's key frame,
	// predicted from zero — the flash-back pattern, content returning to a
	// pose the previous frame no longer holds. Whole-partition only, and only
	// when the zero-MV probe above said golden is in the running.
	goldenCost := int64(1) << 62
	var goldenMV motionVector
	if cache.tryGolden {
		gmv := cache.golden
		var trial tileStream
		sse := te.codeInterMB(&trial, fe.golden, mbx, mby, partWhole, []motionVector{gmv}, motionVector{})
		goldenCost = sse + fe.lambda*int64(te.rdBits(trial.toks)+3)
		goldenMV = gmv
	}

	// Intra candidate — only when inter is struggling. A macroblock whose best
	// inter cost is already below the gate cannot be beaten by intra by more
	// than the gate, and on inter frames intra wins essentially only on
	// occlusions and scene changes, where inter cost is enormous.
	intraCost := int64(1) << 62
	var intraTok tileStream
	if bestInterCost > fe.lambda*32 {
		intraSSE := te.codeLumaMBBest(&intraTok, mbx, mby, b) + te.encodeChromaMBTo(&intraTok, mbx, mby, b)
		intraCost = intraSSE + fe.lambda*int64(te.rdBits(intraTok.toks)+2)
	}

	switch {
	case skipCost <= bestInterCost && skipCost <= intraCost && skipCost <= goldenCost:
		s.put(ctxMBMode, mbSkip)
		te.reconMC(mbx, mby, pred)
		fe.mv[idx] = pred
	case goldenCost < bestInterCost && goldenCost <= intraCost:
		s.put(ctxMBMode, mbInter)
		s.put(ctxRef, refGolden)
		te.codeInterMB(s, fe.golden, mbx, mby, partWhole, []motionVector{goldenMV}, motionVector{})
		fe.mv[idx] = motionVector{} // a golden MB is a zero vector to its neighbors
	case bestInterCost <= intraCost:
		s.put(ctxMBMode, mbInter)
		s.put(ctxRef, refLast)
		te.codeInterMB(s, fe.ref, mbx, mby, bestPart, bestMVs[:len(partRects[bestPart])], pred)
		fe.mv[idx] = bestMVs[0] // the top-left partition represents the macroblock
	default:
		s.put(ctxMBMode, mbIntra)
		te.encodeLumaMB(s, mbx, mby, b)
		te.encodeChromaMB(s, mbx, mby, b)
		fe.mv[idx] = motionVector{}
	}
}

// codeLumaMBBest runs the intra transform-size selection and commits it into s,
// returning the reconstruction SSE.
func (te *tileEncoder) codeLumaMBBest(s *tileStream, mbx, mby int, b tileBounds) int64 {
	bestTx, bestCost := 0, int64(1)<<62
	for txIdx := range lumaTxSizes {
		if cost := te.lumaMBCost(mbx, mby, b, txIdx); cost < bestCost {
			bestCost, bestTx = cost, txIdx
		}
	}
	s.put(ctxTxSize, bestTx)
	return te.codeLumaMB(s, mbx, mby, b, bestTx)
}

// encodeChromaMBTo is encodeChromaMB with an SSE return, for cost trials.
// encodeChromaMB reconstructs into rec, so the SSE is measured from there.
func (te *tileEncoder) encodeChromaMBTo(s *tileStream, mbx, mby int, b tileBounds) int64 {
	fe := te.fe
	te.encodeChromaMB(s, mbx, mby, b)
	x0, y0 := mbx*chromaMB, mby*chromaMB
	return planeSSE(fe.src.cb, fe.rec.cb, x0, y0, chromaMB) +
		planeSSE(fe.src.cr, fe.rec.cr, x0, y0, chromaMB)
}

func planeSSE(src, rec planeView, x0, y0, n int) int64 {
	var sse int64
	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			d := int64(src.at(x0+c, y0+r)) - int64(rec.at(x0+c, y0+r))
			sse += d * d
		}
	}
	return sse
}

// mcMBSSE motion-compensates the whole macroblock at mv from ref and returns
// its SSE against the source without touching rec — the cost of the skip
// candidate.
func (te *tileEncoder) mcMBSSE(ref *frameBuf, mbx, mby int, mv motionVector) int64 {
	fe := te.fe
	var mcY [mbSize * mbSize]int32
	mcLumaMB(ref.y, mbx, mby, mv, mcY[:])
	var sse int64
	x0, y0 := mbx*mbSize, mby*mbSize
	for r := 0; r < mbSize; r++ {
		for c := 0; c < mbSize; c++ {
			d := int64(fe.src.y.at(x0+c, y0+r)) - int64(mcY[r*mbSize+c])
			sse += d * d
		}
	}
	var mcC [chromaMB * chromaMB]int32
	cx, cy := mbx*chromaMB, mby*chromaMB
	mcChromaBlock(ref.cb, cx, cy, mv, mcC[:])
	for r := 0; r < chromaMB; r++ {
		for c := 0; c < chromaMB; c++ {
			d := int64(fe.src.cb.at(cx+c, cy+r)) - int64(mcC[r*chromaMB+c])
			sse += d * d
		}
	}
	mcChromaBlock(ref.cr, cx, cy, mv, mcC[:])
	for r := 0; r < chromaMB; r++ {
		for c := 0; c < chromaMB; c++ {
			d := int64(fe.src.cr.at(cx+c, cy+r)) - int64(mcC[r*chromaMB+c])
			sse += d * d
		}
	}
	return sse
}

// reconMC writes the motion-compensated prediction for the whole macroblock
// into rec — the reconstruction of a skipped or residual-free component.
func (te *tileEncoder) reconMC(mbx, mby int, mv motionVector) {
	fe := te.fe
	var mcY [mbSize * mbSize]int32
	mcLumaMB(fe.ref.y, mbx, mby, mv, mcY[:])
	writeMC(fe.rec.y, mbx*mbSize, mby*mbSize, mbSize, mcY[:], mbSize)
	var mcC [chromaMB * chromaMB]int32
	cx, cy := mbx*chromaMB, mby*chromaMB
	mcChromaBlock(fe.ref.cb, cx, cy, mv, mcC[:])
	writeMC(fe.rec.cb, cx, cy, chromaMB, mcC[:], chromaMB)
	mcChromaBlock(fe.ref.cr, cx, cy, mv, mcC[:])
	writeMC(fe.rec.cr, cx, cy, chromaMB, mcC[:], chromaMB)
}

// writeMC clamps an n×n prediction into a plane. The destination row is sliced
// once so the inner store has no bounds check (§6.2); this is the whole job for
// a skipped macroblock, which is the bulk of inter decode.
func writeMC(rec planeView, x0, y0, n int, mc []int32, mcStride int) {
	for r := 0; r < n; r++ {
		base := (y0+r)*rec.stride + x0
		dst := rec.data[base : base+n : base+n]
		src := mc[r*mcStride : r*mcStride+n : r*mcStride+n]
		for c := 0; c < n; c++ {
			dst[c] = clampByte(src[c])
		}
	}
}

// codeInterMB emits an mbInter macroblock's tokens into s — partition shape,
// per-partition MV deltas, coded-block pattern, and per-component residual —
// reconstructs it, and returns the SSE. Residuals are tokenized to scratch
// first so the CBP can be emitted ahead of them, and all-zero components
// collapse to a clear bit.
func (te *tileEncoder) codeInterMB(s *tileStream, ref *frameBuf, mbx, mby, part int, mvs []motionVector, pred motionVector) int64 {
	fe := te.fe
	rects := partRects[part]
	s.put(ctxPart, part)
	for i := range rects {
		pp := pred
		if i > 0 {
			pp = mvs[0]
		}
		putMVComponent(s, ctxMVClassX, int(mvs[i].x)-int(pp.x))
		putMVComponent(s, ctxMVClassY, int(mvs[i].y)-int(pp.y))
	}

	// Assemble the motion-compensated prediction from the partitions.
	var mcY [mbSize * mbSize]int32
	for i, rc := range rects {
		mcLumaRect(ref.y, mbx, mby, rc[0], rc[1], rc[2], rc[3], mvs[i], mcY[:])
	}
	cx, cy := mbx*chromaMB, mby*chromaMB
	var mcCb, mcCr [chromaMB * chromaMB]int32
	for i, rc := range rects {
		mcChromaRect(ref.cb, cx, cy, rc[0], rc[1], rc[2], rc[3], mvs[i], mcCb[:])
		mcChromaRect(ref.cr, cx, cy, rc[0], rc[1], rc[2], rc[3], mvs[i], mcCr[:])
	}

	// Luma: choose the transform size that codes the residual best, tokenizing
	// into scratch.
	bestTx, bestCost := 0, int64(1)<<62
	for txIdx := range lumaTxSizes {
		var trial tileStream
		sse := te.codeInterLuma(&trial, mbx, mby, mcY[:], txIdx)
		if cost := sse + fe.lambda*int64(te.rdBits(trial.toks)); cost < bestCost {
			bestCost, bestTx = cost, txIdx
		}
	}
	te.lumaToks.toks = te.lumaToks.toks[:0]
	sse := te.codeInterLuma(&te.lumaToks, mbx, mby, mcY[:], bestTx)
	lumaZero := allZeroBlocks(&te.lumaToks)

	te.cbToks.toks = te.cbToks.toks[:0]
	copy(te.sc.pred[:chromaMB*chromaMB], mcCb[:])
	sse += te.codeResidual(&te.cbToks, planeChroma, 0, fe.src.cb, fe.rec.cb, cx, cy, chromaMB)
	cbZero := allZeroBlocks(&te.cbToks)

	te.crToks.toks = te.crToks.toks[:0]
	copy(te.sc.pred[:chromaMB*chromaMB], mcCr[:])
	sse += te.codeResidual(&te.crToks, planeChroma, 0, fe.src.cr, fe.rec.cr, cx, cy, chromaMB)
	crZero := allZeroBlocks(&te.crToks)

	// Assemble: cbp, then only the coded components. A clear luma bit also
	// drops the transform-size token. Reconstruction is already correct: an
	// all-zero residual reconstructed to prediction+0 == the decoder's direct
	// MC write.
	cbp := 0
	if !lumaZero {
		cbp |= cbpLuma
	}
	if !cbZero {
		cbp |= cbpCb
	}
	if !crZero {
		cbp |= cbpCr
	}
	s.put(ctxCBP, cbp)
	if !lumaZero {
		s.put(ctxTxSize, bestTx)
		s.toks = append(s.toks, te.lumaToks.toks...)
	}
	if !cbZero {
		s.toks = append(s.toks, te.cbToks.toks...)
	}
	if !crZero {
		s.toks = append(s.toks, te.crToks.toks...)
	}
	return sse
}

// allZeroBlocks reports whether a scratch stream holds only immediate-EOB
// blocks: every token is tEOB.
func allZeroBlocks(s *tileStream) bool {
	for _, t := range s.toks {
		if t.sym != tEOB || int(t.ctx) < ctxTokenBase {
			return false
		}
	}
	return true
}

// codeInterLuma codes a macroblock's luma residual against its motion-
// compensated prediction at one transform size, returning the SSE. It emits
// only block tokens — the transform-size token is the assembler's job, since
// an all-zero luma drops it.
func (te *tileEncoder) codeInterLuma(s *tileStream, mbx, mby int, mc []int32, txIdx int) int64 {
	fe := te.fe
	n := lumaTxSizes[txIdx]
	var sse int64
	for by := 0; by < mbSize; by += n {
		for bx := 0; bx < mbSize; bx += n {
			for r := 0; r < n; r++ {
				for c := 0; c < n; c++ {
					te.sc.pred[r*n+c] = mc[(by+r)*mbSize+bx+c]
				}
			}
			sse += te.codeResidual(s, planeLuma, txIdx, fe.src.y, fe.rec.y, mbx*mbSize+bx, mby*mbSize+by, n)
		}
	}
	return sse
}

// searchMVRect finds a good motion vector for one partition rectangle by
// diamond search: it seeds at the predictor and the origin, walks a shrinking
// full-pel diamond toward lower luma SAD, then refines to quarter-pel. Diamond
// search visits a small fraction of the points a full search would while
// landing on essentially the same optimum for smooth motion.
func (te *tileEncoder) searchMVRect(ref *frameBuf, mbx, mby, px, py, pw, ph int, pred motionVector) motionVector {
	best := motionVector{}
	bestSAD := te.rectSAD(ref, mbx, mby, px, py, pw, ph, best)
	consider := func(mv motionVector) bool {
		if sad := te.rectSAD(ref, mbx, mby, px, py, pw, ph, mv); sad < bestSAD {
			bestSAD, best = sad, mv
			return true
		}
		return false
	}

	consider(pred)

	// Full-pel diamond. Motion vectors are quarter-pel, so a full pixel is a
	// step of 4.
	for step := searchStart; step >= 1; step >>= 1 {
		for {
			cx, cy := int(best.x), int(best.y)
			moved := consider(motionVector{clampMV(cx + step*4), clampMV(cy)}) ||
				consider(motionVector{clampMV(cx - step*4), clampMV(cy)}) ||
				consider(motionVector{clampMV(cx), clampMV(cy + step*4)}) ||
				consider(motionVector{clampMV(cx), clampMV(cy - step*4)})
			if !moved {
				break
			}
		}
	}

	// Sub-pel refinement: half-pel then quarter-pel around the integer optimum.
	for _, sub := range [2]int{2, 1} {
		bx, by := int(best.x), int(best.y)
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				consider(motionVector{clampMV(bx + dx*sub), clampMV(by + dy*sub)})
			}
		}
	}
	return best
}

func (te *tileEncoder) rectSAD(ref *frameBuf, mbx, mby, px, py, pw, ph int, mv motionVector) int64 {
	fe := te.fe
	// Full-pel interior candidates — the bulk of a diamond search — compare
	// bytes directly, skipping the int32 prediction buffer entirely.
	if mv.x&3 == 0 && mv.y&3 == 0 {
		srcX := mbx*mbSize + px + int(mv.x>>2)
		srcY := mby*mbSize + py + int(mv.y>>2)
		if srcX >= 0 && srcY >= 0 && srcX+pw <= ref.y.w && srcY+ph <= ref.y.h {
			x0, y0 := mbx*mbSize+px, mby*mbSize+py
			var sad int64
			for r := 0; r < ph; r++ {
				sb := (y0+r)*fe.src.y.stride + x0
				a := fe.src.y.data[sb : sb+pw : sb+pw]
				rb := (srcY+r)*ref.y.stride + srcX
				b := ref.y.data[rb : rb+pw : rb+pw]
				for c := 0; c < pw; c++ {
					sad += int64(abs32(int32(a[c]) - int32(b[c])))
				}
			}
			return sad
		}
	}
	var mc [mbSize * mbSize]int32
	mcLumaRect(ref.y, mbx, mby, px, py, pw, ph, mv, mc[:])
	var sad int64
	x0, y0 := mbx*mbSize, mby*mbSize
	for r := 0; r < ph; r++ {
		for c := 0; c < pw; c++ {
			d := int32(fe.src.y.at(x0+px+c, y0+py+r)) - mc[(py+r)*mbSize+px+c]
			sad += int64(abs32(d))
		}
	}
	return sad
}

package ebiv

// Inter frame decoder. It mirrors encodeInterMB exactly: read the macroblock
// mode, then reconstruct a skipped macroblock (predicted MV, no residual), an
// inter macroblock (MV delta, coded-block pattern, residual for the coded
// components), or an intra macroblock.

func (td *tileDecoder) decodeInterMB(mbx, mby int, b tileBounds) {
	mode := td.dec.decode(ctxMBMode)
	idx := mby*td.mvStride + mbx
	switch mode {
	case mbSkip:
		mv := predictMV(td.mv, td.mvStride, mbx, mby, b)
		td.mv[idx] = mv
		td.reconMCMB(mbx, mby, mv)
	case mbIntra:
		td.decodeLumaMB(mbx, mby, b)
		td.decodeChromaMB(mbx, mby, b)
		td.mv[idx] = motionVector{}
	case mbInter:
		refSel := td.dec.decode(ctxRef)
		if refSel < 0 || refSel >= numRefs {
			td.dec.fail(ErrCorrupt)
			return
		}
		// A golden macroblock predicts from a static anchor: its MVs code
		// against zero and it looks like a zero vector to its neighbors, so
		// last-ref prediction never mixes reference spaces.
		ref, pred := td.ref, predictMV(td.mv, td.mvStride, mbx, mby, b)
		if refSel == refGolden {
			ref, pred = td.golden, motionVector{}
		}
		part := td.dec.decode(ctxPart)
		if part < 0 || part >= numPartModes {
			td.dec.fail(ErrCorrupt)
			return
		}
		rects := partRects[part]
		var mvs [4]motionVector
		for i := range rects {
			pp := pred
			if i > 0 {
				pp = mvs[0]
			}
			mvs[i] = motionVector{
				x: clampMV(int(pp.x) + td.dec.getMVComponent(ctxMVClassX)),
				y: clampMV(int(pp.y) + td.dec.getMVComponent(ctxMVClassY)),
			}
		}
		if td.dec.err != nil {
			return
		}
		if refSel == refGolden {
			td.mv[idx] = motionVector{}
		} else {
			td.mv[idx] = mvs[0]
		}
		td.reconInterMB(ref, mbx, mby, part, mvs[:len(rects)])
	default:
		td.dec.fail(ErrCorrupt)
	}
}

// reconMCMB reconstructs a macroblock as pure motion compensation — the whole
// job for a skipped macroblock, and the uncoded components of an inter one.
func (td *tileDecoder) reconMCMB(mbx, mby int, mv motionVector) {
	mcLumaByteMB(td.ref.y, mbx, mby, mv, td.rec.y)
	cx, cy := mbx*chromaMB, mby*chromaMB
	mcChromaByteMB(td.ref.cb, cx, cy, mv, td.rec.cb)
	mcChromaByteMB(td.ref.cr, cx, cy, mv, td.rec.cr)
}

func (td *tileDecoder) reconInterMB(ref *frameBuf, mbx, mby, part int, mvs []motionVector) {
	cbp := td.dec.decode(ctxCBP)
	if cbp < 0 || cbp >= numCBP || td.dec.err != nil {
		td.dec.fail(ErrCorrupt)
		return
	}
	rects := partRects[part]

	// Luma: a coded macroblock needs the int32 prediction as the base for the
	// residual add; an uncoded one goes straight to bytes per partition, skipping
	// the int32 buffer and the separate clamped copy entirely.
	if cbp&cbpLuma != 0 {
		var mcY [mbSize * mbSize]int32
		for i, rc := range rects {
			mcLumaRect(ref.y, mbx, mby, rc[0], rc[1], rc[2], rc[3], mvs[i], mcY[:])
		}
		txIdx := td.dec.decode(ctxTxSize)
		if txIdx < 0 || txIdx >= len(lumaTxSizes) {
			td.dec.fail(ErrCorrupt)
			return
		}
		n := lumaTxSizes[txIdx]
		for by := 0; by < mbSize; by += n {
			for bx := 0; bx < mbSize; bx += n {
				for r := 0; r < n; r++ {
					for c := 0; c < n; c++ {
						td.sc.pred[r*n+c] = mcY[(by+r)*mbSize+bx+c]
					}
				}
				td.reconResidual(planeLuma, txIdx, td.rec.y, mbx*mbSize+bx, mby*mbSize+by, n)
				if td.dec.err != nil {
					return
				}
			}
		}
	} else {
		for i, rc := range rects {
			mcLumaByteRect(ref.y, mbx, mby, rc[0], rc[1], rc[2], rc[3], mvs[i], td.rec.y)
		}
	}

	// Chroma: same split per plane — assemble int32 only for a coded plane.
	cx, cy := mbx*chromaMB, mby*chromaMB
	td.reconInterChroma(mbx, mby, cx, cy, rects, mvs, ref.cb, td.rec.cb, cbp&cbpCb != 0)
	td.reconInterChroma(mbx, mby, cx, cy, rects, mvs, ref.cr, td.rec.cr, cbp&cbpCr != 0)
}

// reconInterChroma reconstructs one chroma plane of an inter macroblock: an
// int32 prediction plus residual when coded, or a direct byte write per
// partition when not.
func (td *tileDecoder) reconInterChroma(mbx, mby, cx, cy int, rects [][4]int, mvs []motionVector, ref, rec planeView, coded bool) {
	if coded {
		var mc [chromaMB * chromaMB]int32
		for i, rc := range rects {
			mcChromaRect(ref, cx, cy, rc[0], rc[1], rc[2], rc[3], mvs[i], mc[:])
		}
		copy(td.sc.pred[:chromaMB*chromaMB], mc[:])
		td.reconResidual(planeChroma, 0, rec, cx, cy, chromaMB)
		return
	}
	for i, rc := range rects {
		mcChromaByteRect(ref, cx, cy, rc[0], rc[1], rc[2], rc[3], mvs[i], rec)
	}
}

// reconResidual decodes one block's residual and adds it to the prediction
// already sitting in td.sc.pred.
func (td *tileDecoder) reconResidual(plane, txIdx int, rec planeView, x0, y0, n int) {
	sc := &td.sc
	for i := 0; i < n*n; i++ {
		sc.levels[i] = 0
	}
	decodeBlock(td.dec, plane, txIdx, sc.levels[:n*n], n)
	if td.dec.err != nil {
		return
	}
	td.q.dequantize(sc.levels[:n*n], sc.coeffs[:n*n], n)
	inverseDCT(sc.coeffs[:n*n], sc.recon[:n*n], n)
	reconstruct(rec, x0, y0, n, sc.pred[:], sc.recon[:])
}

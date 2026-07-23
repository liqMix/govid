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
		pred := predictMV(td.mv, td.mvStride, mbx, mby, b)
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
		td.mv[idx] = mvs[0]
		td.reconInterMB(mbx, mby, part, mvs[:len(rects)])
	default:
		td.dec.fail(ErrCorrupt)
	}
}

// reconMCMB reconstructs a macroblock as pure motion compensation — the whole
// job for a skipped macroblock, and the uncoded components of an inter one.
func (td *tileDecoder) reconMCMB(mbx, mby int, mv motionVector) {
	var mcY [mbSize * mbSize]int32
	mcLumaMB(td.ref.y, mbx, mby, mv, mcY[:])
	writeMC(td.rec.y, mbx*mbSize, mby*mbSize, mbSize, mcY[:], mbSize)
	var mcC [chromaMB * chromaMB]int32
	cx, cy := mbx*chromaMB, mby*chromaMB
	mcChromaBlock(td.ref.cb, cx, cy, mv, mcC[:])
	writeMC(td.rec.cb, cx, cy, chromaMB, mcC[:], chromaMB)
	mcChromaBlock(td.ref.cr, cx, cy, mv, mcC[:])
	writeMC(td.rec.cr, cx, cy, chromaMB, mcC[:], chromaMB)
}

func (td *tileDecoder) reconInterMB(mbx, mby, part int, mvs []motionVector) {
	cbp := td.dec.decode(ctxCBP)
	if cbp < 0 || cbp >= numCBP || td.dec.err != nil {
		td.dec.fail(ErrCorrupt)
		return
	}
	rects := partRects[part]

	// Assemble the motion-compensated prediction from the partitions, then add
	// the residual per transform block when coded, else write the prediction
	// directly.
	var mcY [mbSize * mbSize]int32
	for i, rc := range rects {
		mcLumaRect(td.ref.y, mbx, mby, rc[0], rc[1], rc[2], rc[3], mvs[i], mcY[:])
	}
	if cbp&cbpLuma != 0 {
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
		writeMC(td.rec.y, mbx*mbSize, mby*mbSize, mbSize, mcY[:], mbSize)
	}

	// Chroma: assemble each plane's prediction from the partitions.
	cx, cy := mbx*chromaMB, mby*chromaMB
	var mcCb, mcCr [chromaMB * chromaMB]int32
	for i, rc := range rects {
		mcChromaRect(td.ref.cb, cx, cy, rc[0], rc[1], rc[2], rc[3], mvs[i], mcCb[:])
		mcChromaRect(td.ref.cr, cx, cy, rc[0], rc[1], rc[2], rc[3], mvs[i], mcCr[:])
	}
	if cbp&cbpCb != 0 {
		copy(td.sc.pred[:chromaMB*chromaMB], mcCb[:])
		td.reconResidual(planeChroma, 0, td.rec.cb, cx, cy, chromaMB)
	} else {
		writeMC(td.rec.cb, cx, cy, chromaMB, mcCb[:], chromaMB)
	}
	if cbp&cbpCr != 0 {
		copy(td.sc.pred[:chromaMB*chromaMB], mcCr[:])
		td.reconResidual(planeChroma, 0, td.rec.cr, cx, cy, chromaMB)
	} else {
		writeMC(td.rec.cr, cx, cy, chromaMB, mcCr[:], chromaMB)
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

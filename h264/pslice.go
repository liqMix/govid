package h264

import (
	"fmt"
	"image"
)

// P-slice macroblock types (spec Table 7-10).
const (
	pMBTypeL0_16x16   = 0
	pMBTypeL0_L0_16x8 = 1
	pMBTypeL0_L0_8x16 = 2
	pMBType8x8        = 3
	pMBType8x8ref0    = 4
	pMBTypeIntraStart = 5 // mb_type >= 5 means intra in P-slice
)

// Sub-macroblock types for P_8x8 (spec Table 7-17).
const (
	subMBType8x8 = 0
	subMBType8x4 = 1
	subMBType4x8 = 2
	subMBType4x4 = 3
)

// CBP table for inter macroblocks (spec Table 9-4, inter column).
var cbpTableInter = [48]int{
	0, 16, 1, 2, 4, 8, 32, 3, 5, 10, 12, 15, 47, 7, 11, 13,
	14, 6, 9, 31, 35, 37, 42, 44, 33, 34, 36, 40, 39, 43, 45, 46,
	17, 18, 20, 24, 19, 21, 26, 28, 23, 27, 29, 30, 22, 25, 38, 41,
}

func (d *Decoder) decodePSliceImpl(br *BitReader, sh *sliceHeader) (*image.YCbCr, error) {
	if len(d.refFrames) == 0 {
		return nil, fmt.Errorf("P-slice: no reference frames available")
	}

	totalMBs := d.mbw * d.mbh
	mbIdx := 0

	for mbIdx < totalMBs {
		// Read mb_skip_run.
		skipStart := br.BitsRead()
		skipRun, err := br.ReadUE()
		if err != nil {
			return nil, fmt.Errorf("mb_skip_run: %w", err)
		}
		skipEnd := br.BitsRead()

		// Process skip MBs.
		for i := 0; i < int(skipRun) && mbIdx < totalMBs; i++ {
			mbx := mbIdx % d.mbw
			mby := mbIdx / d.mbw
			d.decodeMBSkip(mbx, mby)
			if DebugMBBits != nil {
				DebugMBBits(mbx, mby, skipStart, skipEnd)
			}
			if DebugPSliceTrace != nil {
				DebugPSliceTrace(mbx, mby, "S", skipStart, skipEnd, skipRun)
			}
			mbIdx++
		}

		if mbIdx >= totalMBs {
			break
		}

		// Non-skip MB.
		mbx := mbIdx % d.mbw
		mby := mbIdx / d.mbw
		mbStart := br.BitsRead()

		mbType, err := br.ReadUE()
		if err != nil {
			return nil, fmt.Errorf("MB(%d,%d) mb_type: %w", mbx, mby, err)
		}

		if int(mbType) >= pMBTypeIntraStart {
			// Intra MB in P-slice.
			intraMBType := int(mbType) - pMBTypeIntraStart
			if err := d.decodeMBIntraWithType(br, mbx, mby, intraMBType); err != nil {
				return nil, fmt.Errorf("MB(%d,%d) intra(mbType=%d): %w", mbx, mby, mbType, err)
			}
			// Mark as intra in mbInfo.
			idx := mby*d.mbw + mbx
			d.mbInfo[idx].isIntra = true
			d.mbInfo[idx].mbType = -2 // intra-in-P
			d.mbInfo[idx].qp = d.qp
			for k := range d.mbInfo[idx].mv {
				d.mbInfo[idx].mv[k] = [2]int16{0, 0}
			}
			for k := range d.mbInfo[idx].refIdx {
				d.mbInfo[idx].refIdx[k] = -1
			}
			d.mbInfo[idx].hasCoef = true
		} else {
			if err := d.decodeMBInter(br, mbx, mby, sh, int(mbType)); err != nil {
				return nil, fmt.Errorf("MB(%d,%d) inter(mbType=%d): %w", mbx, mby, mbType, err)
			}
		}
		if DebugMBBits != nil {
			DebugMBBits(mbx, mby, mbStart, br.BitsRead())
		}
		if DebugPSliceTrace != nil {
			DebugPSliceTrace(mbx, mby, "N", mbStart, br.BitsRead(), mbType)
		}
		mbIdx++
	}

	return d.cropImg(), nil
}

func (d *Decoder) decodeMBSkip(mbx, mby int) {
	ref := d.getRefFrame(0)
	if ref == nil {
		return
	}

	// H.264 spec 8.4.1.1: Skip MV derivation for P/SP slices.
	// If either neighbor A (left) or B (above) is unavailable, or has
	// refIdx=0 and MV=(0,0), the skip MV is forced to (0,0).
	mvp := [2]int16{0, 0}
	aMV, aRef, aAvail := d.getNeighborMVAt(mbx, mby, -1, 0)
	bMV, bRef, bAvail := d.getNeighborMVAt(mbx, mby, 0, -1)
	aZero := !aAvail || (aRef == 0 && aMV[0] == 0 && aMV[1] == 0)
	bZero := !bAvail || (bRef == 0 && bMV[0] == 0 && bMV[1] == 0)
	if !aZero && !bZero {
		mvp = d.predictMVWithSize(mbx, mby, 0, 0, 16, 16, 0)
	}

	// Motion compensation: luma.
	d.motionCompLuma(ref, mbx, mby, mvp, ybrYY, ybrYX, 16, 16)
	// Motion compensation: chroma.
	d.motionCompChroma(ref, mbx, mby, mvp)

	// Copy to output image.
	d.copyMBToImg(mbx, mby)

	// Store MB info.
	idx := mby*d.mbw + mbx
	d.mbInfo[idx].isIntra = false
	d.mbInfo[idx].mbType = -1 // skip
	d.mbInfo[idx].qp = d.qp
	for k := 0; k < 16; k++ {
		d.mbInfo[idx].mv[k] = mvp
	}
	for k := 0; k < 4; k++ {
		d.mbInfo[idx].refIdx[k] = 0
	}
	d.mbInfo[idx].hasCoef = false
	d.mbInfo[idx].decodedMask = 0xFFFF

	// Zero nzCoeff for skip MB.
	for k := range d.nzCoeffCur {
		d.nzCoeffCur[k] = 0
	}
	for k := range d.intraModeCur {
		d.intraModeCur[k] = -1
	}
	d.storeIntraModes(mbx, mby)
	d.storeNZCoeff(mbx, mby)
}

func (d *Decoder) decodeMBInter(br *BitReader, mbx, mby int, sh *sliceHeader, mbType int) error {
	idx := mby*d.mbw + mbx
	info := &d.mbInfo[idx]
	info.isIntra = false
	info.mbType = mbType
	// Spec 6.4.8: track per-4x4-block decoded status for within-MB neighbor
	// availability. Start with no blocks decoded; each partition sets its bits
	// as it stores MVs.
	info.decodedMask = 0

	// Clear workspace.
	for i := range d.coeff {
		d.coeff[i] = 0
	}
	for i := range d.nzCoeffCur {
		d.nzCoeffCur[i] = 0
	}
	for i := range d.intraModeCur {
		d.intraModeCur[i] = -1
	}

	numRefL0 := int(sh.numRefIdxL0Active)

	switch mbType {
	case pMBTypeL0_16x16:
		refIdx := 0
		if numRefL0 > 1 {
			te, err := readTE(br, numRefL0-1)
			if err != nil {
				return fmt.Errorf("ref_idx_l0: %w", err)
			}
			refIdx = int(te)
		}
		mvdX, err := br.ReadSE()
		if err != nil {
			return fmt.Errorf("mvd_l0[0]: %w", err)
		}
		mvdY, err := br.ReadSE()
		if err != nil {
			return fmt.Errorf("mvd_l0[1]: %w", err)
		}

		mvp := d.predictMVWithSize(mbx, mby, 0, 0, 16, 16, refIdx)
		mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}

		ref := d.getRefFrame(refIdx)
		if ref == nil {
			return fmt.Errorf("reference frame %d not found", refIdx)
		}

		d.motionCompLuma(ref, mbx, mby, mv, ybrYY, ybrYX, 16, 16)
		d.motionCompChroma(ref, mbx, mby, mv)

		for k := 0; k < 16; k++ {
			info.mv[k] = mv
		}
		for k := 0; k < 4; k++ {
			info.refIdx[k] = refIdx
		}
		info.decodedMask = 0xFFFF

	case pMBTypeL0_L0_16x8:
		var refs [2]int
		for p := 0; p < 2; p++ {
			if numRefL0 > 1 {
				te, err := readTE(br, numRefL0-1)
				if err != nil {
					return err
				}
				refs[p] = int(te)
			}
		}
		var mvs [2][2]int16
		for p := 0; p < 2; p++ {
			mvdX, err := br.ReadSE()
			if err != nil {
				return err
			}
			mvdY, err := br.ReadSE()
			if err != nil {
				return err
			}
			partY := p * 8
			mvp := d.predictMVWithSize(mbx, mby, 0, partY, 16, 8, refs[p])
			mvs[p] = [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}

			ref := d.getRefFrame(refs[p])
			if ref == nil {
				return fmt.Errorf("reference frame %d not found", refs[p])
			}

			d.motionCompLuma(ref, mbx, mby, mvs[p], ybrYY+partY, ybrYX, 16, 8)
			d.motionCompChromaBlock(ref, mbx, mby, mvs[p], 0, p*4, 8, 4)

			// Store MVs for this partition.
			startBlk := p * 8 // blocks 0-7 or 8-15
			for k := startBlk; k < startBlk+8; k++ {
				info.mv[k] = mvs[p]
				info.decodedMask |= uint16(1) << uint(k)
			}
			info.refIdx[p*2] = refs[p]
			info.refIdx[p*2+1] = refs[p]
		}

	case pMBTypeL0_L0_8x16:
		var refs [2]int
		for p := 0; p < 2; p++ {
			if numRefL0 > 1 {
				te, err := readTE(br, numRefL0-1)
				if err != nil {
					return err
				}
				refs[p] = int(te)
			}
		}
		var mvs [2][2]int16
		for p := 0; p < 2; p++ {
			mvdX, err := br.ReadSE()
			if err != nil {
				return err
			}
			mvdY, err := br.ReadSE()
			if err != nil {
				return err
			}
			partX := p * 8
			mvp := d.predictMVWithSize(mbx, mby, partX, 0, 8, 16, refs[p])
			mvs[p] = [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}

			ref := d.getRefFrame(refs[p])
			if ref == nil {
				return fmt.Errorf("reference frame %d not found", refs[p])
			}

			d.motionCompLuma(ref, mbx, mby, mvs[p], ybrYY, ybrYX+partX, 8, 16)
			d.motionCompChromaBlock(ref, mbx, mby, mvs[p], p*4, 0, 4, 8)

			// Store MVs: left half (blocks 0,1,2,3,8,9,10,11) or right half (4,5,6,7,12,13,14,15)
			// Using 4x4 grid: columns 0-1 for left, 2-3 for right.
			for by := 0; by < 4; by++ {
				for bx := p * 2; bx < p*2+2; bx++ {
					info.mv[by*4+bx] = mvs[p]
					info.decodedMask |= uint16(1) << uint(by*4+bx)
				}
			}
			info.refIdx[p] = refs[p]
			info.refIdx[p+2] = refs[p]
		}

	case pMBType8x8, pMBType8x8ref0:
		if err := d.decodeMB8x8(br, mbx, mby, sh, mbType); err != nil {
			return err
		}
	}

	// Read CBP and residual.
	cbpCode, err := br.ReadUE()
	if err != nil {
		return fmt.Errorf("cbp: %w", err)
	}
	if int(cbpCode) >= len(cbpTableInter) {
		return fmt.Errorf("invalid inter CBP code %d", cbpCode)
	}
	cbp := cbpTableInter[cbpCode]
	cbpLuma := cbp & 15
	cbpChroma := cbp >> 4

	// Read transform_size_8x8_flag for High profile inter MBs.
	use8x8Transform := false
	if d.activePPS.Transform8x8Mode && cbpLuma > 0 {
		noSubMbPartSizeLessThan8x8 := true
		if mbType == pMBType8x8 || mbType == pMBType8x8ref0 {
			// Check if any sub-partition is smaller than 8x8.
			for k := 0; k < 4; k++ {
				if info.subMBType[k] != subMBType8x8 {
					noSubMbPartSizeLessThan8x8 = false
					break
				}
			}
		}
		if noSubMbPartSizeLessThan8x8 {
			t8x8, err := br.ReadBool()
			if err != nil {
				return fmt.Errorf("transform_size_8x8_flag: %w", err)
			}
			if t8x8 {
				// Inter 8x8 path is implemented below under use8x8Transform
				// but shows bit-desync on ~12% of real x264 streams — likely
				// the CAVLC nC context for 8x8 sub-blocks differs from the
				// encoder. Keep the hard-stop in place until a bit-exact
				// fixture is isolated.
				return fmt.Errorf("h264: 8x8 inter transform not supported")
			}
			use8x8Transform = t8x8
		}
	}

	hasCoef := false
	if cbp > 0 {
		qpDelta, err := br.ReadSE()
		if err != nil {
			return fmt.Errorf("mb_qp_delta: %w", err)
		}
		d.qp = (d.qp + int(qpDelta) + 52) % 52

		if use8x8Transform {
			// 8x8 transform path: 4 × 8×8 luma blocks, CBP bit per 8×8 partition.
			// Each 8×8 is decoded via CAVLC as 4 sub-blocks of 16 coefficients
			// (spec §9.2.1.2) using read8x8ResidualCAVLC.
			//
			// nC for each sub-block uses the 4×4 neighbor predictor at the
			// partition's top-left 4×4 scan index — this matches reference
			// decoders (FFmpeg / JM) which compute nC once per 8×8 and reuse
			// for all 4 sub-blocks. Without this, bit consumption diverges
			// from the encoder and subsequent MBs desync.
			var blk [64]int16
			for p := 0; p < 4; p++ {
				if cbpLuma&(1<<uint(p)) == 0 {
					continue
				}
				for i := range blk {
					blk[i] = 0
				}
				nC := d.calcNC(mbx, mby, p*4)
				nz, err := read8x8ResidualCAVLC(br, blk[:], [4]int{nC, nC, nC, nC})
				if err != nil {
					return err
				}
				// Spec-aligned nz count for 8x8 blocks (matches FFmpeg): all 4
				// 4x4 scan positions within the 8x8 are stamped with 16 if any
				// coefficient was non-zero, else 0. This is what neighbor MBs
				// will read when computing their own 4x4 CAVLC nC context.
				mark := 0
				if nz > 0 {
					mark = 16
				}
				for k := 0; k < 4; k++ {
					d.nzCoeffCur[p*4+k] = mark
				}
				if nz > 0 {
					hasCoef = true
					dequant8x8(blk[:], d.qp)
					idct8x8(blk[:])
					partY := (p / 2) * 8
					partX := (p % 2) * 8
					d.addResidual8x8(ybrYY+partY, ybrYX+partX, blk[:])
				}
			}
		} else {
			// Decode luma residual (4×4 transform path).
			for blk := 0; blk < 16; blk++ {
				group8x8 := blkTo8x8[blk]
				if cbpLuma&(1<<uint(group8x8)) != 0 {
					nC := d.calcNC(mbx, mby, blk)
					nz, err := readResidualBlock(br, d.coeff[blk*16:blk*16+16], 16, nC)
					if err != nil {
						return err
					}
					d.nzCoeffCur[blk] = nz
					if DebugBlkLog != nil {
						tmp := make([]int16, 16)
						copy(tmp, d.coeff[blk*16:blk*16+16])
						DebugBlkLog(mbx, mby, blk, nz, tmp, -1, nC)
					}
					if nz > 0 {
						hasCoef = true
						reorderCoeffs(d.coeff[blk*16 : blk*16+16])
						dequant4x4(d.coeff[blk*16:blk*16+16], d.qp)
						idct4x4(d.coeff[blk*16 : blk*16+16])
						pos := blk4x4Pos[blk]
						d.addResidual4x4(ybrYY+pos[0], ybrYX+pos[1], d.coeff[blk*16:blk*16+16])
					}
				}
			}
		}

		// Decode chroma residual.
		if cbpChroma > 0 {
			hasCoef = true
			if err := d.decodeInterChroma(br, mbx, mby, cbpChroma); err != nil {
				return err
			}
		}
	}

	info.hasCoef = hasCoef
	info.qp = d.qp
	d.storeIntraModes(mbx, mby)
	d.storeNZCoeff(mbx, mby)
	d.copyMBToImg(mbx, mby)
	return nil
}

// decodeInterChroma decodes chroma residual for inter MBs (no intra prediction).
func (d *Decoder) decodeInterChroma(br *BitReader, mbx, mby, cbpChroma int) error {
	qpC := chromaQP(d.qp, int(d.activePPS.ChromaQPIndexOffset))

	// Read chroma DC.
	cbDC := make([]int16, 4)
	_, err := readResidualBlock(br, cbDC, 4, -1)
	if err != nil {
		return err
	}
	crDC := make([]int16, 4)
	_, err = readResidualBlock(br, crDC, 4, -1)
	if err != nil {
		return err
	}

	hadamard2x2(cbDC)
	dequantChromaDC(cbDC, qpC)
	hadamard2x2(crDC)
	dequantChromaDC(crDC, qpC)

	// Cb AC.
	for blk := 0; blk < 4; blk++ {
		base := 16*16 + blk*16
		d.coeff[base] = cbDC[blk]

		if cbpChroma >= 2 {
			nC := d.calcNCChroma(mbx, mby, blk)
			nz, err := readResidualBlock(br, d.coeff[base+1:base+16], 15, nC)
			if err != nil {
				return err
			}
			d.nzCoeffCur[16+blk] = nz
			reorderCoeffs(d.coeff[base : base+16])
		}

		if d.coeff[base] != 0 || d.nzCoeffCur[16+blk] > 0 {
			dequant4x4(d.coeff[base:base+16], qpC)
			d.coeff[base] = cbDC[blk]
			idct4x4(d.coeff[base : base+16])
			j4 := blk / 2
			i4 := blk % 2
			d.addResidual4x4(ybrBY+j4*4, ybrBX+i4*4, d.coeff[base:base+16])
		}
	}

	// Cr AC.
	for blk := 0; blk < 4; blk++ {
		base := 16*16 + 4*16 + blk*16
		d.coeff[base] = crDC[blk]

		if cbpChroma >= 2 {
			nC := d.calcNCChroma(mbx, mby, 4+blk)
			nz, err := readResidualBlock(br, d.coeff[base+1:base+16], 15, nC)
			if err != nil {
				return err
			}
			d.nzCoeffCur[20+blk] = nz
			reorderCoeffs(d.coeff[base : base+16])
		}

		if d.coeff[base] != 0 || d.nzCoeffCur[20+blk] > 0 {
			dequant4x4(d.coeff[base:base+16], qpC)
			d.coeff[base] = crDC[blk]
			idct4x4(d.coeff[base : base+16])
			j4 := blk / 2
			i4 := blk % 2
			d.addResidual4x4(ybrRY+j4*4, ybrRX+i4*4, d.coeff[base:base+16])
		}
	}

	return nil
}

func (d *Decoder) decodeMB8x8(br *BitReader, mbx, mby int, sh *sliceHeader, mbType int) error {
	idx := mby*d.mbw + mbx
	info := &d.mbInfo[idx]
	numRefL0 := int(sh.numRefIdxL0Active)

	// Read sub_mb_type for each 8x8 partition.
	var subTypes [4]int
	for p := 0; p < 4; p++ {
		st, err := br.ReadUE()
		if err != nil {
			return fmt.Errorf("sub_mb_type[%d]: %w", p, err)
		}
		subTypes[p] = int(st)
		info.subMBType[p] = int(st)
	}

	// Read ref_idx for each 8x8 partition.
	var refs [4]int
	if mbType != pMBType8x8ref0 {
		for p := 0; p < 4; p++ {
			if numRefL0 > 1 {
				te, err := readTE(br, numRefL0-1)
				if err != nil {
					return err
				}
				refs[p] = int(te)
			}
		}
	}

	// 8x8 partition positions in the MB (pixel coordinates).
	part8x8Pos := [4][2]int{{0, 0}, {8, 0}, {0, 8}, {8, 8}}

	// Read MVDs and perform motion compensation for each sub-partition.
	for p := 0; p < 4; p++ {
		px := part8x8Pos[p][0]
		py := part8x8Pos[p][1]
		info.refIdx[p] = refs[p]

		ref := d.getRefFrame(refs[p])
		if ref == nil {
			return fmt.Errorf("reference frame %d not found", refs[p])
		}

		switch subTypes[p] {
		case subMBType8x8:
			mvdX, err := br.ReadSE()
			if err != nil {
				return err
			}
			mvdY, err := br.ReadSE()
			if err != nil {
				return err
			}
			mvp := d.predictMVWithSize(mbx, mby, px, py, 8, 8, refs[p])
			mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}

			d.motionCompLuma(ref, mbx, mby, mv, ybrYY+py, ybrYX+px, 8, 8)
			d.motionCompChromaBlock(ref, mbx, mby, mv, px/2, py/2, 4, 4)

			// Store MVs for all 4x4 blocks in this 8x8 partition.
			for by := py / 4; by < py/4+2; by++ {
				for bx := px / 4; bx < px/4+2; bx++ {
					info.mv[by*4+bx] = mv
					info.decodedMask |= uint16(1) << uint(by*4+bx)
				}
			}

		case subMBType8x4:
			for sp := 0; sp < 2; sp++ {
				mvdX, err := br.ReadSE()
				if err != nil {
					return err
				}
				mvdY, err := br.ReadSE()
				if err != nil {
					return err
				}
				spy := py + sp*4
				mvp := d.predictMVWithSize(mbx, mby, px, spy, 8, 4, refs[p])
				mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}

				d.motionCompLuma(ref, mbx, mby, mv, ybrYY+spy, ybrYX+px, 8, 4)
				d.motionCompChromaBlock(ref, mbx, mby, mv, px/2, (spy)/2, 4, 2)

				by := spy / 4
				for bx := px / 4; bx < px/4+2; bx++ {
					info.mv[by*4+bx] = mv
					info.decodedMask |= uint16(1) << uint(by*4+bx)
				}
			}

		case subMBType4x8:
			for sp := 0; sp < 2; sp++ {
				mvdX, err := br.ReadSE()
				if err != nil {
					return err
				}
				mvdY, err := br.ReadSE()
				if err != nil {
					return err
				}
				spx := px + sp*4
				mvp := d.predictMVWithSize(mbx, mby, spx, py, 4, 8, refs[p])
				mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}

				d.motionCompLuma(ref, mbx, mby, mv, ybrYY+py, ybrYX+spx, 4, 8)
				d.motionCompChromaBlock(ref, mbx, mby, mv, spx/2, py/2, 2, 4)

				bx := spx / 4
				for by := py / 4; by < py/4+2; by++ {
					info.mv[by*4+bx] = mv
					info.decodedMask |= uint16(1) << uint(by*4+bx)
				}
			}

		case subMBType4x4:
			for sp := 0; sp < 4; sp++ {
				mvdX, err := br.ReadSE()
				if err != nil {
					return err
				}
				mvdY, err := br.ReadSE()
				if err != nil {
					return err
				}
				spx := px + (sp%2)*4
				spy := py + (sp/2)*4
				mvp := d.predictMVWithSize(mbx, mby, spx, spy, 4, 4, refs[p])
				mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}

				d.motionCompLuma(ref, mbx, mby, mv, ybrYY+spy, ybrYX+spx, 4, 4)
				d.motionCompChromaBlock(ref, mbx, mby, mv, spx/2, spy/2, 2, 2)

				bx := spx / 4
				by := spy / 4
				info.mv[by*4+bx] = mv
				info.decodedMask |= uint16(1) << uint(by*4+bx)
			}
		}
	}

	return nil
}

// readTE reads a truncated Exp-Golomb coded value.
// If maxVal == 0, return 0. If maxVal == 1, read 1 bit and invert.
// Otherwise, read UE.
func readTE(br *BitReader, maxVal int) (uint32, error) {
	if maxVal == 0 {
		return 0, nil
	}
	if maxVal == 1 {
		b, err := br.ReadBool()
		if err != nil {
			return 0, err
		}
		if b {
			return 0, nil
		}
		return 1, nil
	}
	return br.ReadUE()
}

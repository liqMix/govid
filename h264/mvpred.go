package h264

// Motion vector prediction per H.264 spec 8.4.1.3.
// Uses median predictor from neighbors A (left), B (above), C (above-right).
// If C is unavailable, D (above-left) is used instead.

// mbInterInfo stores per-MB inter prediction state for MV prediction context.
type mbInterInfo struct {
	isIntra bool
	refIdx  [4]int // per 8x8 partition
	// refPicID identifies the actual reference picture per 8x8 partition.
	// Deblocking bS compares pictures, not reference indices (spec 8.7.2.1) —
	// a modified reference list can map two indices to the same picture.
	refPicID  [4]int
	mv        [16][2]int16 // per 4x4 sub-block, quarter-pixel MVs
	hasCoef   bool         // non-zero residual (for deblocking bS)
	subMBType [4]int       // sub-partition types for P_8x8 MBs
	// transform8x8 marks MBs coded with the 8x8 luma transform. Deblocking
	// skips their internal luma edges 1 and 3 and applies the bS=2 coefficient
	// test at 8x8 granularity (spec 8.7.2.1).
	transform8x8 bool

	// CABAC neighbor-context state (unused by the CAVLC paths).
	// cbpCabac packs: bits 0-3 luma CBP, bits 4-5 chroma CBP value (0-2),
	// bit 6 Cb DC cbf, bit 7 Cr DC cbf, bit 8 luma DC cbf (FFmpeg cbp_table
	// layout). chromaPredMode is 0 for inter/skip MBs. mvdAbs holds per-4x4
	// |mvd| components capped at 70 (raster order, like mv). iNxN / i16OrPCM
	// classify the intra mb_type for the mb_type bin contexts.
	cbpCabac       uint16
	chromaPredMode uint8
	iNxN           bool
	i16OrPCM       bool
	isPCM          bool
	mvdAbs         [16][2]uint8

	// List-1 state for B slices, parallel to mv / refIdx / refPicID /
	// mvdAbs (which serve as list 0). predMask holds per-8x8-partition list
	// usage: bit 0 = uses L0, bit 1 = uses L1 (0 for intra partitions).
	mvL1       [16][2]int16
	refIdxL1   [4]int
	refPicIDL1 [4]int
	mvdAbsL1   [16][2]uint8
	predMask   [4]uint8
	// isDirectMB marks B_Skip / B_Direct_16x16 MBs (CABAC mb_type context);
	// directMask marks per-4x4 cells belonging to direct-predicted
	// partitions (CABAC ref_idx context).
	isDirectMB bool
	directMask uint16
	qp         int // QP used for this macroblock (for deblocking)
	mbType     int // decoded MB type: -1=skip, -2=intra-in-P, 0-4=inter P types
	// decodedMask is a bitmap: bit i set iff 4x4 block i (raster order) has
	// been decoded in the current frame. Required by H.264 spec 6.4.8: within
	// the same MB, neighbors in partitions that have not yet been decoded are
	// considered unavailable for MV prediction. Without this, getNeighborMVAt
	// returns stale mv[]/refIdx[] from the previous frame.
	decodedMask uint16
}

// predictMVWithSize predicts a list-0 MV using the median predictor from
// neighbors A (left), B (above), C (above-right, or D above-left).
func (d *Decoder) predictMVWithSize(mbx, mby, partX, partY, partW, partH, refIdx int) [2]int16 {
	return d.predictMVList(0, mbx, mby, partX, partY, partW, partH, refIdx)
}

// predictMVList is predictMVWithSize parameterized by reference list.
func (d *Decoder) predictMVList(list, mbx, mby, partX, partY, partW, partH, refIdx int) [2]int16 {
	aMV, aRef, aAvail := d.getNeighborMVAtList(list, mbx, mby, partX-1, partY)
	bMV, bRef, bAvail := d.getNeighborMVAtList(list, mbx, mby, partX, partY-1)
	cMV, cRef, cAvail := d.getNeighborMVAtList(list, mbx, mby, partX+partW, partY-1)

	if !cAvail {
		cMV, cRef, cAvail = d.getNeighborMVAtList(list, mbx, mby, partX-1, partY-1) // D
	}

	// Special cases for 16x8 and 8x16.
	if partW == 16 && partH == 8 {
		if partY == 0 && bRef == refIdx {
			return bMV
		}
		if partY != 0 && aRef == refIdx {
			return aMV
		}
	}
	if partW == 8 && partH == 16 {
		if partX == 0 && aRef == refIdx {
			return aMV
		}
		if partX != 0 && cRef == refIdx {
			return cMV
		}
	}

	// H.264 spec 8.4.1.3.1: if B and C are both not available, use A.
	if !bAvail && !cAvail {
		if aAvail {
			return aMV
		}
		return [2]int16{0, 0}
	}

	// H.264 spec 8.4.1.3.1: if exactly one of A, B, C has matching refIdx,
	// use that neighbor's MV instead of the median.
	matchA := aAvail && aRef == refIdx
	matchB := bAvail && bRef == refIdx
	matchC := cAvail && cRef == refIdx
	matchCount := 0
	if matchA {
		matchCount++
	}
	if matchB {
		matchCount++
	}
	if matchC {
		matchCount++
	}
	if matchCount == 1 {
		if matchA {
			return aMV
		}
		if matchB {
			return bMV
		}
		return cMV
	}

	// Default: median of all available (unavailable treated as MV=0).
	return [2]int16{
		median3(aMV[0], bMV[0], cMV[0]),
		median3(aMV[1], bMV[1], cMV[1]),
	}
}

// getNeighborMVAt gets the list-0 MV and ref of the 4x4 block containing
// pixel (px, py) relative to the current MB.
func (d *Decoder) getNeighborMVAt(mbx, mby, px, py int) (mv [2]int16, ref int, avail bool) {
	return d.getNeighborMVAtList(0, mbx, mby, px, py)
}

// getNeighborMVAtList is getNeighborMVAt parameterized by reference list.
// A partition that does not use the requested list reports ref -1 with a
// zero MV (spec 8.4.1.3: treated like an intra neighbor).
func (d *Decoder) getNeighborMVAtList(list, mbx, mby, px, py int) (mv [2]int16, ref int, avail bool) {
	nmbx := mbx
	nmby := mby
	npx := px
	npy := py

	if npx < 0 {
		nmbx--
		npx += 16
	} else if npx >= 16 {
		nmbx++
		npx -= 16
	}
	if npy < 0 {
		nmby--
		npy += 16
	} else if npy >= 16 {
		nmby++
		npy -= 16
	}

	if nmbx < 0 || nmbx >= d.mbw || nmby < 0 || nmby >= d.mbh {
		return [2]int16{0, 0}, -1, false
	}

	nmbIdx := nmby*d.mbw + nmbx
	if nmbIdx >= len(d.mbInfo) {
		return [2]int16{0, 0}, -1, false
	}

	// H.264 spec 6.4.11.7: neighbor is unavailable if it's in an MB that
	// comes after the current MB in raster scan order (not yet decoded).
	curMBIdx := mby*d.mbw + mbx
	if nmbIdx > curMBIdx {
		return [2]int16{0, 0}, -1, false
	}

	info := &d.mbInfo[nmbIdx]
	if info.isIntra {
		// H.264 spec 8.4.1.3.1: intra MBs have MV=0 and refIdx=-1.
		return [2]int16{0, 0}, -1, true
	}

	bx := npx / 4
	by := npy / 4
	if bx > 3 {
		bx = 3
	}
	if by > 3 {
		by = 3
	}
	blk4x4 := by*4 + bx
	// Spec 6.4.8: a within-MB neighbor that lies in a partition not yet decoded
	// is unavailable. Track this via decodedMask; the bit is set when the 4x4
	// block's MV is stored.
	if nmbIdx == curMBIdx && info.decodedMask&(uint16(1)<<uint(blk4x4)) == 0 {
		return [2]int16{0, 0}, -1, false
	}
	part8x8 := (by/2)*2 + bx/2
	if list == 1 {
		if info.predMask[part8x8]&2 == 0 {
			return [2]int16{0, 0}, -1, true
		}
		return info.mvL1[blk4x4], info.refIdxL1[part8x8], true
	}
	if info.predMask[part8x8]&1 == 0 {
		return [2]int16{0, 0}, -1, true
	}
	return info.mv[blk4x4], info.refIdx[part8x8], true
}

func median3(a, b, c int16) int16 {
	if a > b {
		a, b = b, a
	}
	// Now a <= b.
	if b > c {
		b = c
	}
	if a > b {
		return a
	}
	return b
}

package h264

// Motion vector prediction per H.264 spec 8.4.1.3.
// Uses median predictor from neighbors A (left), B (above), C (above-right).
// If C is unavailable, D (above-left) is used instead.

// mbInterInfo stores per-MB inter prediction state for MV prediction context.
type mbInterInfo struct {
	isIntra   bool
	refIdx    [4]int       // per 8x8 partition
	mv        [16][2]int16 // per 4x4 sub-block, quarter-pixel MVs
	hasCoef   bool         // non-zero residual (for deblocking bS)
	subMBType [4]int       // sub-partition types for P_8x8 MBs
	qp        int          // QP used for this macroblock (for deblocking)
	mbType    int          // decoded MB type: -1=skip, -2=intra-in-P, 0-4=inter P types
	// decodedMask is a bitmap: bit i set iff 4x4 block i (raster order) has
	// been decoded in the current frame. Required by H.264 spec 6.4.8: within
	// the same MB, neighbors in partitions that have not yet been decoded are
	// considered unavailable for MV prediction. Without this, getNeighborMVAt
	// returns stale mv[]/refIdx[] from the previous frame.
	decodedMask uint16
}

// predictMVWithSize predicts MV using median predictor from neighbors A, B, C (or D).
func (d *Decoder) predictMVWithSize(mbx, mby, partX, partY, partW, partH, refIdx int) [2]int16 {
	aMV, aRef, aAvail := d.getNeighborMVAt(mbx, mby, partX-1, partY)
	bMV, bRef, bAvail := d.getNeighborMVAt(mbx, mby, partX, partY-1)
	cMV, cRef, cAvail := d.getNeighborMVAt(mbx, mby, partX+partW, partY-1)

	if !cAvail {
		cMV, cRef, cAvail = d.getNeighborMVAt(mbx, mby, partX-1, partY-1) // D
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

// getNeighborMVAt gets MV and ref from the 4x4 block containing pixel (px, py) relative to current MB.
func (d *Decoder) getNeighborMVAt(mbx, mby, px, py int) (mv [2]int16, ref int, avail bool) {
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

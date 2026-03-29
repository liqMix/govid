package vp8

// Inter-frame MV decoding and mode selection per RFC 6386 §16, §17.

// interMB holds per-macroblock inter-frame state.
type interMB struct {
	refFrame uint8      // 0=intra, 1=last, 2=golden, 3=altref
	mv       [2]int16   // row, col in quarter-pixel units
	mode     uint8      // inter mode
	splitMV  [16][2]int16 // per 4x4 sub-block MVs (SPLITMV only)
}

// Inter-frame MB mode constants (§16.2).
const (
	interModeNEARESTMV = iota
	interModeNEARMV
	interModeZEROMV
	interModeNEWMV
	interModeSPLITMV
)

// Sub-MV reference modes (§16.4).
const (
	subMVModeLEFT  = 0
	subMVModeABOVE = 1
	subMVModeZERO  = 2
	subMVModeNEW   = 3
)

// Mode context table from libvpx modecont.c.
// Indexed by count of non-zero MVs among neighbors (0..5).
var modeContexts = [6][4]uint8{
	{7, 1, 1, 143},
	{14, 18, 14, 107},
	{135, 64, 57, 68},
	{60, 56, 128, 65},
	{159, 134, 128, 34},
	{234, 188, 128, 28},
}

// Sub-MV reference probabilities per left/above context (libvpx vp8_sub_mv_ref_prob3).
// Index: (aez << 2) | (lez << 1) | lea, matching libvpx decodemv.c.
var subMVRefProb3 = [8][3]uint8{
	{147, 136, 18},
	{223, 1, 34},
	{106, 145, 1},
	{208, 1, 1},
	{179, 121, 1},
	{223, 1, 34},
	{179, 121, 1},
	{208, 1, 1},
}

// getSubMVRefProb returns sub-MV reference probabilities based on left/above MV context.
// Index per libvpx: (aez << 2) | (lez << 1) | lea.
func getSubMVRefProb(leftMV, aboveMV [2]int16) [3]uint8 {
	var idx int
	leftZero := leftMV[0] == 0 && leftMV[1] == 0
	aboveZero := aboveMV[0] == 0 && aboveMV[1] == 0
	if aboveZero {
		idx += 4
	}
	if leftZero {
		idx += 2
	}
	if leftMV == aboveMV {
		idx += 1
	}
	return subMVRefProb3[idx]
}

// Split MV partition definitions.
// partitions[type][part] = list of 4x4 sub-block indices.
var splitMVPartitions = [4][][]int{
	// Partition 0: 16x8 (top half, bottom half)
	{{0, 1, 2, 3, 4, 5, 6, 7}, {8, 9, 10, 11, 12, 13, 14, 15}},
	// Partition 1: 8x16 (left half, right half)
	{{0, 1, 4, 5, 8, 9, 12, 13}, {2, 3, 6, 7, 10, 11, 14, 15}},
	// Partition 2: 8x8 (quadrants)
	{{0, 1, 4, 5}, {2, 3, 6, 7}, {8, 9, 12, 13}, {10, 11, 14, 15}},
	// Partition 3: 4x4 (individual)
	{{0}, {1}, {2}, {3}, {4}, {5}, {6}, {7}, {8}, {9}, {10}, {11}, {12}, {13}, {14}, {15}},
}

// Default MV component probabilities per RFC 6386 §17.2.
var defaultMVProb = [2][19]uint8{
	{162, 128, 225, 146, 172, 147, 214, 39, 156, 128, 129, 132, 75, 145, 178, 206, 239, 254, 254},
	{164, 128, 204, 170, 119, 235, 140, 230, 228, 128, 130, 130, 74, 148, 180, 203, 236, 254, 254},
}

// MV probability update probabilities per RFC 6386 §17.2.
var mvProbUpdate = [2][19]uint8{
	{237, 246, 253, 253, 254, 254, 254, 254, 254, 254, 254, 254, 254, 254, 250, 250, 252, 254, 254},
	{231, 243, 245, 253, 254, 254, 254, 254, 254, 254, 254, 254, 254, 254, 251, 251, 254, 254, 254},
}

// MV short tree probabilities for values 0-7.
var mvShortTree = [7]int{
	// Tree structure: value decoded MSB first via tree.
	// Index into prob array for short MV component.
	2, 4, 6, // internal nodes
	-0, -1, -2, -3, // Leaves: placeholder, actual values are bit-by-bit.
}

// parseInterFrameHeader reads inter-frame specific header data (§9.7).
// Note: refreshLast is NOT read here — it comes after refresh_entropy_probs.
func (d *Decoder) parseInterFrameHeader() {
	d.refreshGolden = d.fp.readBit(uniformProb)
	d.refreshAltRef = d.fp.readBit(uniformProb)
	if !d.refreshGolden {
		d.copyGolden = uint8(d.fp.readUint(uniformProb, 2))
	} else {
		d.copyGolden = 0
	}
	if !d.refreshAltRef {
		d.copyAltRef = uint8(d.fp.readUint(uniformProb, 2))
	} else {
		d.copyAltRef = 0
	}
	d.refSignBias[2] = d.fp.readBit(uniformProb) // golden
	d.refSignBias[3] = d.fp.readBit(uniformProb) // altref
}

// parseInterFrameProbs reads inter-frame probability tables.
// Called after parseTokenProb and skip prob.
func (d *Decoder) parseInterFrameProbs() {
	d.probIntra = uint8(d.fp.readUint(uniformProb, 8))
	d.probLast = uint8(d.fp.readUint(uniformProb, 8))
	d.probGf = uint8(d.fp.readUint(uniformProb, 8))

	// Y mode probability updates.
	if d.fp.readBit(uniformProb) {
		for i := range d.yModeProb {
			d.yModeProb[i] = uint8(d.fp.readUint(uniformProb, 8))
		}
	}
	// UV mode probability updates.
	if d.fp.readBit(uniformProb) {
		for i := range d.uvModeProb {
			d.uvModeProb[i] = uint8(d.fp.readUint(uniformProb, 8))
		}
	}
	// MV probability updates.
	d.parseMVProb()
}

// parseMVProb reads MV probability updates (§17.2).
func (d *Decoder) parseMVProb() {
	for comp := 0; comp < 2; comp++ {
		for i := 0; i < 19; i++ {
			if d.fp.readBit(mvProbUpdate[comp][i]) {
				x := uint8(d.fp.readUint(uniformProb, 7))
				if x == 0 {
					d.mvProb[comp][i] = 1
				} else {
					d.mvProb[comp][i] = x << 1
				}
			}
		}
	}
}

// readInterMBModeAndMV determines whether the current MB is intra or inter,
// and if inter, reads the motion vector mode and MV.
func (d *Decoder) readInterMBModeAndMV(mbx, mby int) {
	// Determine if this MB uses inter prediction.
	isInter := d.fp.readBit(d.probIntra)
	if !isInter {
		// Intra MB within an inter-frame.
		d.curRefFrame = 0
		d.curMode = 0
		d.curMV = [2]int16{0, 0}
		d.leftInterMB = interMB{refFrame: 0}
		d.upInterMB[mbx] = interMB{refFrame: 0}
		return
	}

	// Determine reference frame.
	if !d.fp.readBit(d.probLast) {
		d.curRefFrame = 1 // last
	} else if !d.fp.readBit(d.probGf) {
		d.curRefFrame = 2 // golden
	} else {
		d.curRefFrame = 3 // altref
	}

	// Find near MVs, bestMV, and counts with merge/swap/SPLITMV applied.
	nearest, near, bestMV, cnt := d.findNearMVs(mbx, mby)
	d.lastCnt = cnt

	// Clamp context indices to [0, 5].
	clampCtx := func(v int) int {
		if v > 5 {
			return 5
		}
		return v
	}

	// Node 0: ZEROMV vs rest, using cnt[CNT_INTRA].
	if !d.fp.readBit(modeContexts[clampCtx(cnt[0])][0]) {
		d.curMode = interModeZEROMV
		d.curMV = [2]int16{0, 0}
	} else {
		// Node 1: NEARESTMV vs rest, using cnt[CNT_NEAREST] (post-swap).
		if !d.fp.readBit(modeContexts[clampCtx(cnt[1])][1]) {
			d.curMode = interModeNEARESTMV
			d.curMV = nearest
		} else {
			// Node 2: NEARMV vs rest, using cnt[CNT_NEAR] (post-swap).
			if !d.fp.readBit(modeContexts[clampCtx(cnt[2])][2]) {
				d.curMode = interModeNEARMV
				d.curMV = near
			} else {
				// Node 3: NEWMV vs SPLITMV, using cnt[CNT_SPLITMV].
				if !d.fp.readBit(modeContexts[clampCtx(cnt[3])][3]) {
					// NEWMV
					d.curMode = interModeNEWMV
					row := d.readMVComponent(0) * 2
					col := d.readMVComponent(1) * 2
					d.curMV = [2]int16{bestMV[0] + row, bestMV[1] + col}
				} else {
					// SPLITMV
					d.curMode = interModeSPLITMV
					d.readSplitMV(mbx, mby, bestMV)
					d.curMV = d.curSubMV[15]
				}
			}
		}
	}

	// Store UNCLAMPED MV in neighbor state (per libvpx: NEWMV and SPLITMV
	// are NOT clamped before storage; NEARESTMV/NEARMV are already clamped
	// by findNearMVs). MC handles out-of-bounds via clampSrc2D.

	// Update neighbor state.
	imb := interMB{
		refFrame: d.curRefFrame,
		mv:       d.curMV,
		mode:     d.curMode,
	}
	if d.curMode == interModeSPLITMV {
		imb.splitMV = d.curSubMV
	}
	d.leftInterMB = imb
	d.upInterMB[mbx] = imb
}

// findNearMVs implements the inline near-MV logic from libvpx read_mb_modes_mv.
// Returns nearest MV, near MV, bestMV, and counts with merge/swap/SPLITMV
// overwrite applied in the same order as libvpx.
func (d *Decoder) findNearMVs(mbx, mby int) (nearest, near, bestMV [2]int16, cnt [4]int) {
	// cnt[0]=CNT_INTRA, cnt[1]=CNT_NEAREST, cnt[2]=CNT_NEAR, cnt[3]=pointer-walk then SPLITMV
	var nearMVs [4][2]int16 // [0]=zero, [1]=nearest, [2]=near, [3]=extra
	nmvIdx := 0             // equivalent to (nmv - near_mvs) in libvpx
	cntIdx := 0             // equivalent to (cntx - cnt) in libvpx

	biasMV := func(mv [2]int16, nbRef uint8) [2]int16 {
		if d.refSignBias[d.curRefFrame] != d.refSignBias[nbRef] {
			return [2]int16{-mv[0], -mv[1]}
		}
		return mv
	}

	// --- Above neighbor ---
	if mby > 0 {
		above := d.upInterMB[mbx]
		if above.refFrame != 0 { // inter
			if above.mv[0] != 0 || above.mv[1] != 0 {
				nmvIdx++
				nearMVs[nmvIdx] = biasMV(above.mv, above.refFrame)
				cntIdx++
			}
			cnt[cntIdx] += 2
		}
	}

	// --- Left neighbor ---
	if mbx > 0 {
		left := d.leftInterMB
		if left.refFrame != 0 { // inter
			if left.mv[0] != 0 || left.mv[1] != 0 {
				mv := biasMV(left.mv, left.refFrame)
				if mv != nearMVs[nmvIdx] {
					nmvIdx++
					if nmvIdx <= 3 {
						nearMVs[nmvIdx] = mv
					}
					cntIdx++
				}
				cnt[cntIdx] += 2
			} else {
				cnt[0] += 2 // zero MV → CNT_INTRA
			}
		}
	}

	// --- Above-left neighbor (per libvpx: above - 1, boundary = INTRA for mbx=0) ---
	if mby > 0 && mbx > 0 {
		diag := d.aboveLeftInterMB
		if diag.refFrame != 0 { // inter
			if diag.mv[0] != 0 || diag.mv[1] != 0 {
				mv := biasMV(diag.mv, diag.refFrame)
				if mv != nearMVs[nmvIdx] {
					nmvIdx++
					if nmvIdx <= 3 {
						nearMVs[nmvIdx] = mv
					}
					cntIdx++
				}
				cnt[cntIdx] += 1
			} else {
				cnt[0] += 1 // zero MV → CNT_INTRA
			}
		}
	}

	// Merge: per libvpx, uses the pointer-arithmetic cnt[3] (third distinct MV
	// found from diagonal) and compares last-found MV vs nearest.
	// Must happen BEFORE cnt[3] is overwritten with SPLITMV mode count.
	if cnt[3] > 0 && nearMVs[nmvIdx] == nearMVs[1] {
		cnt[1]++
	}

	// Overwrite cnt[3] with SPLITMV neighbor mode count.
	cnt[3] = 0
	if mby > 0 && d.upInterMB[mbx].mode == interModeSPLITMV {
		cnt[3] += 2
	}
	if mbx > 0 && d.leftInterMB.mode == interModeSPLITMV {
		cnt[3] += 2
	}
	if mby > 0 && mbx > 0 && d.aboveLeftInterMB.mode == interModeSPLITMV {
		cnt[3]++
	}

	// Swap nearest/near if near count > nearest count (per libvpx).
	nearest = nearMVs[1]
	near = nearMVs[2]
	if cnt[2] > cnt[1] {
		cnt[1], cnt[2] = cnt[2], cnt[1]
		nearest, near = near, nearest
	}

	// Clamp nearest and near MVs (per libvpx vp8_clamp_mv2).
	nearest[0] = clampMVComp(nearest[0], mby, d.mbh)
	nearest[1] = clampMVComp(nearest[1], mbx, d.mbw)
	near[0] = clampMVComp(near[0], mby, d.mbh)
	near[1] = clampMVComp(near[1], mbx, d.mbw)

	// bestMV from post-swap, post-clamp nearest (per libvpx: near_index =
	// CNT_INTRA + (cnt[CNT_NEAREST] >= cnt[CNT_INTRA]), then
	// vp8_clamp_mv2(&near_mvs[near_index]), best_mv = near_mvs[near_index]).
	if cnt[1] >= cnt[0] {
		bestMV = nearest
	}

	return nearest, near, bestMV, cnt
}

// readSplitMV reads SPLITMV sub-block MVs (§16.4).
func (d *Decoder) readSplitMV(mbx, mby int, bestMV [2]int16) {
	// Read partition type. Tree per libvpx vp8_mbsplit_tree:
	// prob=110: 0→type3(4x4), 1→continue
	// prob=111: 0→type2(8x8), 1→continue
	// prob=150: 0→type0(16x8), 1→type1(8x16)
	var partType int
	if !d.fp.readBit(110) {
		partType = 3 // 4x4
	} else if !d.fp.readBit(111) {
		partType = 2 // 8x8
	} else if !d.fp.readBit(150) {
		partType = 0 // 16x8
	} else {
		partType = 1 // 8x16
	}

	parts := splitMVPartitions[partType]

	for _, part := range parts {
		// Get left and above sub-block MVs for probability context.
		leftMV := d.getLeftSubMV(mbx, part[0])
		aboveMV := d.getAboveSubMV(mby, mbx, part[0])
		prob := getSubMVRefProb(leftMV, aboveMV)

		// Read sub-MV mode.
		var subMode int
		if !d.fp.readBit(prob[0]) {
			subMode = subMVModeLEFT
		} else if !d.fp.readBit(prob[1]) {
			subMode = subMVModeABOVE
		} else if !d.fp.readBit(prob[2]) {
			subMode = subMVModeZERO
		} else {
			subMode = subMVModeNEW
		}

		var submv [2]int16
		switch subMode {
		case subMVModeLEFT:
			submv = d.getLeftSubMV(mbx, part[0])
		case subMVModeABOVE:
			submv = d.getAboveSubMV(mby, mbx, part[0])
		case subMVModeZERO:
			submv = [2]int16{0, 0}
		case subMVModeNEW:
			row := d.readMVComponent(0) * 2
			col := d.readMVComponent(1) * 2
			submv = [2]int16{bestMV[0] + row, bestMV[1] + col}
		}

		for _, idx := range part {
			d.curSubMV[idx] = submv
		}
	}
}

// getLeftSubMV returns the MV of the sub-block to the left of the given sub-block.
func (d *Decoder) getLeftSubMV(mbx int, subIdx int) [2]int16 {
	col := subIdx % 4
	if col > 0 {
		return d.curSubMV[subIdx-1]
	}
	// Left edge of MB — use left MB's right-column MV.
	if mbx == 0 {
		return [2]int16{0, 0}
	}
	left := d.leftInterMB
	if left.refFrame == 0 {
		return [2]int16{0, 0}
	}
	if left.mode == interModeSPLITMV {
		// Right column of left MB: sub-blocks 3, 7, 11, 15.
		row := subIdx / 4
		return left.splitMV[row*4+3]
	}
	return left.mv
}

// getAboveSubMV returns the MV of the sub-block above the given sub-block.
func (d *Decoder) getAboveSubMV(mby, mbx int, subIdx int) [2]int16 {
	row := subIdx / 4
	if row > 0 {
		return d.curSubMV[subIdx-4]
	}
	// Top edge of MB — use above MB's bottom-row MV.
	if mby == 0 {
		return [2]int16{0, 0}
	}
	above := d.upInterMB[mbx]
	if above.refFrame == 0 {
		return [2]int16{0, 0}
	}
	if above.mode == interModeSPLITMV {
		// Bottom row of above MB: sub-blocks 12, 13, 14, 15.
		col := subIdx % 4
		return above.splitMV[12+col]
	}
	return above.mv
}

// Probability index constants for MV component decoding (§17.1).
const (
	mvpIsShort = 0  // prob[0]: short vs long
	mvpSign    = 1  // prob[1]: sign
	mvpShort   = 2  // prob[2-8]: short MV tree (7 probs for values 0-7)
	mvpBits    = 9  // prob[9-18]: long MV magnitude bits (10 probs)
)

const mvLongWidth = 10

// readMVComponent reads a single MV component (row or col) from the bitstream (§17.1).
// comp is 0 for row, 1 for col.
func (d *Decoder) readMVComponent(comp int) int16 {
	prob := &d.mvProb[comp]
	var x int16

	if d.fp.readBit(prob[mvpIsShort]) {
		// Long MV (magnitude >= 8).
		// Read bits 0, 1, 2.
		for i := 0; i < 3; i++ {
			if d.fp.readBit(prob[mvpBits+i]) {
				x |= 1 << uint(i)
			}
		}
		// Read bits 9, 8, 7, 6, 5, 4 (high to low, skipping bit 3).
		for i := mvLongWidth - 1; i > 3; i-- {
			if d.fp.readBit(prob[mvpBits+i]) {
				x |= 1 << uint(i)
			}
		}
		// Bit 3: per libvpx, uses || short-circuit:
		//   if (!(x & 0xFFF0) || vp8_read(r, p[MVPbits + 3])) x += 8;
		// No high bits → always set bit 3 (long path magnitude must be ≥8).
		// High bits present → read bit 3 from stream.
		if (x & ^int16(0xF)) == 0 || d.fp.readBit(prob[mvpBits+3]) {
			x |= 8
		}
	} else {
		// Short MV: tree-coded value 0-7 using prob[2]-prob[8].
		x = d.readSmallMV(comp)
	}

	if x == 0 {
		return 0
	}

	// Sign bit.
	if d.fp.readBit(prob[mvpSign]) {
		return -x
	}
	return x
}

// readSmallMV decodes a short MV value (0-7) using a binary tree.
// Uses prob[mvpShort] through prob[mvpShort+6] (i.e., prob[2]-prob[8]).
func (d *Decoder) readSmallMV(comp int) int16 {
	prob := &d.mvProb[comp]
	// Tree: binary tree splitting 0-7 into leaves.
	// Node i uses probability prob[mvpShort + (i>>1)].
	i := 0
	for {
		bit := d.fp.readBit(prob[mvpShort+(i>>1)])
		if bit {
			i = smallMVTree[i+1]
		} else {
			i = smallMVTree[i]
		}
		if i <= 0 {
			return int16(-i)
		}
	}
}

// smallMVTree is the VP8 small MV tree for values 0-7.
// Negative values are leaf nodes (negated result value).
var smallMVTree = [14]int{
	2, 8,     // Node 0: left→Node 2, right→Node 8
	4, 6,     // Node 2: left→Node 4, right→Node 6
	0, -1,    // Node 4: left→0, right→1 (0 encoded as -0 = 0)
	-2, -3,   // Node 6: left→2, right→3
	10, 12,   // Node 8: left→Node 10, right→Node 12
	-4, -5,   // Node 10: left→4, right→5
	-6, -7,   // Node 12: left→6, right→7
}

// clampMVComp clamps a MV component to stay within frame boundaries.
// mb is the macroblock index, mbMax is the frame dimension in macroblocks.
func clampMVComp(mv int16, mb, mbMax int) int16 {
	// Allow 128 pixel overshoot past frame edge (in eighth-pixel units).
	minVal := int16(-((mb * 16) + 128) * 8)
	maxVal := int16(((mbMax-1-mb)*16 + 128) * 8)
	if mv < minVal {
		return minVal
	}
	if mv > maxVal {
		return maxVal
	}
	return mv
}

// parseIntraYModeInter parses the Y mode for an intra MB in an inter-frame.
// Uses the balanced binary tree from libvpx vp8_ymode_tree:
//
//	        [prob0]
//	       /       \
//	     DC       [prob1]
//	             /       \
//	          [prob2]   [prob3]
//	          /   \     /   \
//	         V     H  TM     B
func (d *Decoder) parseIntraYModeInter(mbx int) {
	if !d.fp.readBit(d.yModeProb[0]) {
		d.usePredY16 = true
		d.predY16 = predDC
	} else if !d.fp.readBit(d.yModeProb[1]) {
		if !d.fp.readBit(d.yModeProb[2]) {
			d.usePredY16 = true
			d.predY16 = predVE
		} else {
			d.usePredY16 = true
			d.predY16 = predHE
		}
	} else {
		if !d.fp.readBit(d.yModeProb[3]) {
			d.usePredY16 = true
			d.predY16 = predTM
		} else {
			// B_PRED (4x4) — inter-frame uses flat bmode_prob, not
			// context-dependent kf_bmode_prob.
			d.usePredY16 = false
			d.parsePredModeY4Inter(mbx)
			return
		}
	}
	for i := 0; i < 4; i++ {
		d.upMB[mbx].pred[i] = d.predY16
		d.leftMB.pred[i] = d.predY16
	}
}

// parsePredModeC8Inter parses chroma prediction mode for intra MBs in inter-frames.
func (d *Decoder) parsePredModeC8Inter() {
	if !d.fp.readBit(d.uvModeProb[0]) {
		d.predC8 = predDC
	} else if !d.fp.readBit(d.uvModeProb[1]) {
		d.predC8 = predVE
	} else if !d.fp.readBit(d.uvModeProb[2]) {
		d.predC8 = predHE
	} else {
		d.predC8 = predTM
	}
}


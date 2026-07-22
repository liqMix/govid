package h264

import (
	"fmt"
	"image"
)

// B-slice decoding (spec 7.4.5 Table 7-14, 8.4.1.2). Both entropy coders
// share the partition executor (motion compensation, bi-prediction combine,
// weighted prediction) and the spatial direct derivation; only the syntax
// parsing differs.

// bMBShape describes a decoded B mb_type.
type bMBShape struct {
	direct bool
	parts  int      // 1 (16x16), 2 (16x8 / 8x16), 4 (B_8x8)
	is16x8 bool     // valid when parts == 2
	mask   [2]uint8 // per-partition list usage (bit0 L0, bit1 L1)
}

// bMBTypes maps the CAVLC B mb_type value (0..22) per spec Table 7-14 /
// FFmpeg ff_h264_b_mb_type_info. mb_type >= 23 is intra (mb_type - 23).
var bMBTypes = [23]bMBShape{
	{direct: true},                                 // 0 B_Direct_16x16
	{parts: 1, mask: [2]uint8{1, 0}},               // 1 B_L0_16x16
	{parts: 1, mask: [2]uint8{2, 0}},               // 2 B_L1_16x16
	{parts: 1, mask: [2]uint8{3, 0}},               // 3 B_Bi_16x16
	{parts: 2, is16x8: true, mask: [2]uint8{1, 1}}, // 4 B_L0_L0_16x8
	{parts: 2, mask: [2]uint8{1, 1}},               // 5 B_L0_L0_8x16
	{parts: 2, is16x8: true, mask: [2]uint8{2, 2}}, // 6 B_L1_L1_16x8
	{parts: 2, mask: [2]uint8{2, 2}},               // 7 B_L1_L1_8x16
	{parts: 2, is16x8: true, mask: [2]uint8{1, 2}}, // 8 B_L0_L1_16x8
	{parts: 2, mask: [2]uint8{1, 2}},               // 9 B_L0_L1_8x16
	{parts: 2, is16x8: true, mask: [2]uint8{2, 1}}, // 10 B_L1_L0_16x8
	{parts: 2, mask: [2]uint8{2, 1}},               // 11 B_L1_L0_8x16
	{parts: 2, is16x8: true, mask: [2]uint8{1, 3}}, // 12 B_L0_Bi_16x8
	{parts: 2, mask: [2]uint8{1, 3}},               // 13 B_L0_Bi_8x16
	{parts: 2, is16x8: true, mask: [2]uint8{2, 3}}, // 14 B_L1_Bi_16x8
	{parts: 2, mask: [2]uint8{2, 3}},               // 15 B_L1_Bi_8x16
	{parts: 2, is16x8: true, mask: [2]uint8{3, 1}}, // 16 B_Bi_L0_16x8
	{parts: 2, mask: [2]uint8{3, 1}},               // 17 B_Bi_L0_8x16
	{parts: 2, is16x8: true, mask: [2]uint8{3, 2}}, // 18 B_Bi_L1_16x8
	{parts: 2, mask: [2]uint8{3, 2}},               // 19 B_Bi_L1_8x16
	{parts: 2, is16x8: true, mask: [2]uint8{3, 3}}, // 20 B_Bi_Bi_16x8
	{parts: 2, mask: [2]uint8{3, 3}},               // 21 B_Bi_Bi_8x16
	{parts: 4},                                     // 22 B_8x8
}

// bSubShape describes a B sub_mb_type (spec Table 7-18).
type bSubShape struct {
	direct   bool
	w, h     int // sub-partition size in pixels
	subParts int
	mask     uint8
}

var bSubTypes = [13]bSubShape{
	{direct: true},                     // 0 B_Direct_8x8
	{w: 8, h: 8, subParts: 1, mask: 1}, // 1 B_L0_8x8
	{w: 8, h: 8, subParts: 1, mask: 2}, // 2 B_L1_8x8
	{w: 8, h: 8, subParts: 1, mask: 3}, // 3 B_Bi_8x8
	{w: 8, h: 4, subParts: 2, mask: 1}, // 4 B_L0_8x4
	{w: 4, h: 8, subParts: 2, mask: 1}, // 5 B_L0_4x8
	{w: 8, h: 4, subParts: 2, mask: 2}, // 6 B_L1_8x4
	{w: 4, h: 8, subParts: 2, mask: 2}, // 7 B_L1_4x8
	{w: 8, h: 4, subParts: 2, mask: 3}, // 8 B_Bi_8x4
	{w: 4, h: 8, subParts: 2, mask: 3}, // 9 B_Bi_4x8
	{w: 4, h: 4, subParts: 4, mask: 1}, // 10 B_L0_4x4
	{w: 4, h: 4, subParts: 4, mask: 2}, // 11 B_L1_4x4
	{w: 4, h: 4, subParts: 4, mask: 3}, // 12 B_Bi_4x4
}

// bPart is one motion partition of a B macroblock, ready for execution.
type bPart struct {
	x, y, w, h int // luma geometry within the MB
	mask       uint8
	ref        [2]int
	mv         [2][2]int16
}

// mcBuf holds a saved motion-compensated prediction region for bi-prediction.
type mcBuf struct {
	luma [16 * 16]uint8
	cb   [8 * 8]uint8
	cr   [8 * 8]uint8
}

// --- Implicit weighted bi-prediction (spec 8.4.2.3.1) ----------------------

func clipInt8(v int) int {
	if v < -128 {
		return -128
	}
	if v > 127 {
		return 127
	}
	return v
}

// implicitWeightL0 returns list-0's implicit bipred weight w0 (list 1 gets
// 64-w0), following FFmpeg's implicit_weight_table.
func implicitWeightL0(curPOC, poc0, poc1 int) int {
	w := 32
	td := clipInt8(poc1 - poc0)
	if td != 0 {
		tb := clipInt8(curPOC - poc0)
		abs := td
		if abs < 0 {
			abs = -abs
		}
		tx := (16384 + abs>>1) / td
		dsf := (tb*tx + 32) >> 8
		if dsf >= -64 && dsf <= 128 {
			w = 64 - dsf
		}
	}
	return w
}

// bImplicitUnweighted reports the spec special case where implicit mode
// degenerates to a plain average: single reference in each list with
// poc0 + poc1 == 2*curPOC.
func (d *Decoder) bImplicitUnweighted(sh *sliceHeader) bool {
	if sh.numRefIdxL0Active != 1 || sh.numRefIdxL1Active != 1 {
		return false
	}
	r0 := d.getRefFrameEntry(0, 0)
	r1 := d.getRefFrameEntry(1, 0)
	return r0 != nil && r1 != nil && r0.poc+r1.poc == 2*d.curPOC
}

// --- Partition execution ----------------------------------------------------

// saveMCRegion copies the just-motion-compensated partition region out of the
// ybr workspace so a second list's prediction can be run over it.
func (d *Decoder) saveMCRegion(x, y, w, h int, buf *mcBuf) {
	for j := 0; j < h; j++ {
		copy(buf.luma[j*w:(j+1)*w], d.ybr[ybrYY+y+j][ybrYX+x:ybrYX+x+w])
	}
	cw, ch := w/2, h/2
	cx, cy := x/2, y/2
	for j := 0; j < ch; j++ {
		copy(buf.cb[j*cw:(j+1)*cw], d.ybr[ybrBY+cy+j][ybrBX+cx:ybrBX+cx+cw])
		copy(buf.cr[j*cw:(j+1)*cw], d.ybr[ybrRY+cy+j][ybrRX+cx:ybrRX+cx+cw])
	}
}

// combineBiPred merges the saved list-0 prediction (buf) with the list-1
// prediction currently in ybr, applying default averaging, implicit weights
// (weighted_bipred_idc 2), or explicit weights (idc 1).
func (d *Decoder) combineBiPred(sh *sliceHeader, x, y, w, h, ref0, ref1 int) {
	idc := d.activePPS.WeightedBipredIDC

	combinePlane := func(saved []uint8, rowOff, colOff, pw, ph int, wt [2]int, off [2]int, logWD int, weighted bool) {
		for j := 0; j < ph; j++ {
			row := d.ybr[rowOff+j][colOff : colOff+pw]
			for i := 0; i < pw; i++ {
				a := int(saved[j*pw+i]) // list 0
				b := int(row[i])        // list 1
				var v int
				if weighted {
					v = ((a*wt[0] + b*wt[1] + (1 << uint(logWD))) >> uint(logWD+1)) + (off[0]+off[1]+1)>>1
				} else {
					v = (a + b + 1) >> 1
				}
				row[i] = uint8(clampInt(v, 0, 255))
			}
		}
	}

	cw, ch := w/2, h/2
	cx, cy := x/2, y/2

	switch idc {
	case 2:
		if d.bImplicitUnweighted(sh) {
			break
		}
		p0 := d.getRefFrameEntry(0, ref0)
		p1 := d.getRefFrameEntry(1, ref1)
		if p0 != nil && p1 != nil {
			w0 := 32 // long-term references use the default half weights
			if !p0.longTerm && !p1.longTerm {
				w0 = implicitWeightL0(d.curPOC, p0.poc, p1.poc)
			}
			wt := [2]int{w0, 64 - w0}
			combinePlane(buf0Luma(d), ybrYY+y, ybrYX+x, w, h, wt, [2]int{0, 0}, 5, true)
			combinePlane(buf0Cb(d), ybrBY+cy, ybrBX+cx, cw, ch, wt, [2]int{0, 0}, 5, true)
			combinePlane(buf0Cr(d), ybrRY+cy, ybrRX+cx, cw, ch, wt, [2]int{0, 0}, 5, true)
			return
		}
	case 1:
		if sh.weights != nil && sh.weightsL1 != nil &&
			ref0 < len(sh.weights.luma) && ref1 < len(sh.weightsL1.luma) {
			w0, w1 := sh.weights.entry(ref0), sh.weightsL1.entry(ref1)
			combinePlane(buf0Luma(d), ybrYY+y, ybrYX+x, w, h,
				[2]int{w0.luma.weight, w1.luma.weight},
				[2]int{w0.luma.offset, w1.luma.offset}, sh.weights.lumaLog2Denom, true)
			combinePlane(buf0Cb(d), ybrBY+cy, ybrBX+cx, cw, ch,
				[2]int{w0.cb.weight, w1.cb.weight},
				[2]int{w0.cb.offset, w1.cb.offset}, sh.weights.chromaLog2Denom, true)
			combinePlane(buf0Cr(d), ybrRY+cy, ybrRX+cx, cw, ch,
				[2]int{w0.cr.weight, w1.cr.weight},
				[2]int{w0.cr.offset, w1.cr.offset}, sh.weights.chromaLog2Denom, true)
			return
		}
	}
	combinePlane(buf0Luma(d), ybrYY+y, ybrYX+x, w, h, [2]int{}, [2]int{}, 0, false)
	combinePlane(buf0Cb(d), ybrBY+cy, ybrBX+cx, cw, ch, [2]int{}, [2]int{}, 0, false)
	combinePlane(buf0Cr(d), ybrRY+cy, ybrRX+cx, cw, ch, [2]int{}, [2]int{}, 0, false)
}

// The bipred save buffer lives on the decoder to avoid per-partition allocs.
func buf0Luma(d *Decoder) []uint8 { return d.biBuf.luma[:] }
func buf0Cb(d *Decoder) []uint8   { return d.biBuf.cb[:] }
func buf0Cr(d *Decoder) []uint8   { return d.biBuf.cr[:] }

// weightEntry bundles one reference index's explicit weights per component,
// with defaults filled in (weight = 1<<denom, offset = 0).
type weightEntry struct {
	luma, cb, cr weightOffset
}

func (pw *predWeights) entry(ref int) weightEntry {
	e := weightEntry{
		luma: weightOffset{weight: 1 << uint(pw.lumaLog2Denom)},
		cb:   weightOffset{weight: 1 << uint(pw.chromaLog2Denom)},
		cr:   weightOffset{weight: 1 << uint(pw.chromaLog2Denom)},
	}
	if ref < len(pw.luma) && pw.luma[ref].explicit {
		e.luma = pw.luma[ref]
	}
	if ref < len(pw.chroma) && pw.chroma[ref][0].explicit {
		e.cb = pw.chroma[ref][0]
		e.cr = pw.chroma[ref][1]
	}
	return e
}

// applyWeightsB applies explicit single-list weighted prediction for B slices
// (weighted_bipred_idc 1, only one list used).
func (d *Decoder) applyWeightsB(sh *sliceHeader, list, refIdx, lumaY, lumaX, lw, lh, cOffX, cOffY int) {
	if d.activePPS.WeightedBipredIDC != 1 {
		return
	}
	pw := sh.weights
	if list == 1 {
		pw = sh.weightsL1
	}
	if pw == nil || refIdx < 0 || refIdx >= len(pw.luma) {
		return
	}
	if pw.luma[refIdx].explicit {
		d.applyWeightRegion(lumaY, lumaX, lw, lh, pw.luma[refIdx], pw.lumaLog2Denom)
	}
	if refIdx < len(pw.chroma) && pw.chroma[refIdx][0].explicit {
		d.applyWeightRegion(ybrBY+cOffY, ybrBX+cOffX, lw/2, lh/2, pw.chroma[refIdx][0], pw.chromaLog2Denom)
		d.applyWeightRegion(ybrRY+cOffY, ybrRX+cOffX, lw/2, lh/2, pw.chroma[refIdx][1], pw.chromaLog2Denom)
	}
}

// execBPart runs motion compensation for one B partition: single-list MC with
// optional explicit weighting, or two MC passes combined by combineBiPred.
func (d *Decoder) execBPart(sh *sliceHeader, mbx, mby int, p *bPart) error {
	if DebugBPartExec != nil {
		DebugBPartExec(mbx, mby, p)
	}
	mcOne := func(list int) error {
		rf := d.getRefFrameEntry(list, p.ref[list])
		if rf == nil {
			return fmt.Errorf("reference L%d[%d] not found", list, p.ref[list])
		}
		d.motionCompLuma(rf.img, mbx, mby, p.mv[list], ybrYY+p.y, ybrYX+p.x, p.w, p.h)
		d.motionCompChromaBlock(rf.img, mbx, mby, p.mv[list], p.x/2, p.y/2, p.w/2, p.h/2)
		return nil
	}
	switch p.mask {
	case 1, 2:
		list := int(p.mask - 1)
		if err := mcOne(list); err != nil {
			return err
		}
		d.applyWeightsB(sh, list, p.ref[list], ybrYY+p.y, ybrYX+p.x, p.w, p.h, p.x/2, p.y/2)
	case 3:
		if err := mcOne(0); err != nil {
			return err
		}
		d.saveMCRegion(p.x, p.y, p.w, p.h, &d.biBuf)
		if err := mcOne(1); err != nil {
			return err
		}
		d.combineBiPred(sh, p.x, p.y, p.w, p.h, p.ref[0], p.ref[1])
	default:
		return fmt.Errorf("B partition with empty prediction mask")
	}
	return nil
}

// DebugBPartExec, if non-nil, receives every executed B partition (geometry,
// list mask, refs, MVs) just before motion compensation. Test-only hook.
var DebugBPartExec func(mbx, mby int, p *bPart)

// bSubDecodedMask returns the spec 6.4.8 positional availability mask in
// effect while predicting sub-partition sp of quadrant p of a B_8x8 MB:
// cells of quadrants before p plus earlier sub-partitions within p. Later
// partitions must read as undecoded so the above-right neighbor falls back to
// above-left. (prepareBMB's all-decoded mask is correct for every other B MB
// kind: with 8x8-or-larger partitions no within-MB query ever reaches a later
// partition, but an 8x4/4x8/4x4 second row's above-right neighbor does.)
func bSubDecodedMask(shapes *[4]bSubShape, p, sp int) uint16 {
	var m uint16
	for q := 0; q < p; q++ {
		base := (part8x8Pos[q][1]/4)*4 + part8x8Pos[q][0]/4
		m |= 0x33 << uint(base) // the quadrant's 2x2 cells
	}
	s := shapes[p]
	for k := 0; k < sp; k++ {
		sx := part8x8Pos[p][0] + (k%(8/s.w))*s.w
		sy := part8x8Pos[p][1] + (k/(8/s.w))*s.h
		for by := sy / 4; by < (sy+s.h)/4; by++ {
			for bx := sx / 4; bx < (sx+s.w)/4; bx++ {
				m |= 1 << uint(by*4+bx)
			}
		}
	}
	return m
}

// storeBPart records a partition's motion into mbInfo (both lists).
func (d *Decoder) storeBPart(info *mbInterInfo, p *bPart) {
	for by := p.y / 4; by < (p.y+p.h)/4; by++ {
		for bx := p.x / 4; bx < (p.x+p.w)/4; bx++ {
			k := by*4 + bx
			if p.mask&1 != 0 {
				info.mv[k] = p.mv[0]
			}
			if p.mask&2 != 0 {
				info.mvL1[k] = p.mv[1]
			}
			info.decodedMask |= uint16(1) << uint(k)
		}
	}
	for by := p.y / 8; by < (p.y+p.h+7)/8; by++ {
		for bx := p.x / 8; bx < (p.x+p.w+7)/8; bx++ {
			part := by*2 + bx
			info.predMask[part] = p.mask
			if p.mask&1 != 0 {
				info.refIdx[part] = p.ref[0]
				info.refPicID[part] = d.refPicIDL(0, p.ref[0])
			} else {
				info.refIdx[part] = -1
				info.refPicID[part] = -1
			}
			if p.mask&2 != 0 {
				info.refIdxL1[part] = p.ref[1]
				info.refPicIDL1[part] = d.refPicIDL(1, p.ref[1])
			} else {
				info.refIdxL1[part] = -1
				info.refPicIDL1[part] = -1
			}
		}
	}
}

// --- Spatial direct prediction (spec 8.4.1.2.2) -----------------------------

// bDirectCtx caches the once-per-MB part of the spatial direct derivation.
// temporal marks the MB as temporal direct (spec 8.4.1.2.3), where the whole
// derivation is per-quadrant and the spatial fields are unused.
type bDirectCtx struct {
	ref      [2]int
	mvp      [2][2]int16
	mask     uint8
	temporal bool
}

// deriveDirectMB prepares the direct-mode context for the current MB
// according to the slice's direct_spatial_mv_pred_flag.
func (d *Decoder) deriveDirectMB(sh *sliceHeader, mbx, mby int) bDirectCtx {
	if sh.directSpatialMvPred {
		return d.deriveDirectSpatialMB(mbx, mby)
	}
	return bDirectCtx{temporal: true}
}

func minPositive(a, b int) int {
	if a >= 0 && b >= 0 {
		if a < b {
			return a
		}
		return b
	}
	if a > b {
		return a
	}
	return b
}

// deriveDirectSpatialMB computes the MB-level spatial direct references and
// MV predictors from the 16x16 neighbors.
func (d *Decoder) deriveDirectSpatialMB(mbx, mby int) bDirectCtx {
	var ctx bDirectCtx
	for list := 0; list < 2; list++ {
		_, aRef, _ := d.getNeighborMVAtList(list, mbx, mby, -1, 0)
		_, bRef, _ := d.getNeighborMVAtList(list, mbx, mby, 0, -1)
		cMV, cRef, cAvail := d.getNeighborMVAtList(list, mbx, mby, 16, -1)
		_ = cMV
		if !cAvail {
			_, cRef, _ = d.getNeighborMVAtList(list, mbx, mby, -1, -1)
		}
		ctx.ref[list] = minPositive(aRef, minPositive(bRef, cRef))
	}
	if ctx.ref[0] < 0 && ctx.ref[1] < 0 {
		// directZeroPredictionFlag: both refs 0, zero MVs.
		ctx.ref = [2]int{0, 0}
		ctx.mask = 3
		return ctx
	}
	if ctx.ref[0] >= 0 {
		ctx.mask |= 1
		ctx.mvp[0] = d.predictMVList(0, mbx, mby, 0, 0, 16, 16, ctx.ref[0])
	}
	if ctx.ref[1] >= 0 {
		ctx.mask |= 2
		ctx.mvp[1] = d.predictMVList(1, mbx, mby, 0, 0, 16, 16, ctx.ref[1])
	}
	return ctx
}

// directColZero evaluates colZeroFlag for 8x8 quadrant p of the current MB:
// the co-located block in RefPicList1[0] (corner 4x4 with the
// direct_8x8_inference rule) has refIdx 0 and a near-zero MV.
func (d *Decoder) directColZero(mbx, mby, p int) bool {
	corner := [4]int{0, 3, 12, 15}[p]
	return d.directColZeroAt(mbx, mby, corner)
}

// directColZeroAt evaluates colZeroFlag for the 4x4 block at raster cell.
func (d *Decoder) directColZeroAt(mbx, mby, cell int) bool {
	col := d.getRefFrameEntry(1, 0)
	if col == nil || col.longTerm {
		// colZeroFlag requires RefPicList1[0] to be short-term.
		return false
	}
	idx := (mby*d.mbw+mbx)*16 + cell
	if col.colRef[idx] != 0 {
		return false
	}
	mv := col.colMV[idx]
	return mv[0] >= -1 && mv[0] <= 1 && mv[1] >= -1 && mv[1] <= 1
}

// directPartTemporalAt builds one temporal-direct bPart of the given geometry
// from the co-located 4x4 block at raster cell cellIdx (spec 8.4.1.2.3): the
// co-located block's motion in RefPicList1[0] is scaled by the POC distance
// ratio tb/td; list 0 references the picture the co-located block referenced
// (MapColToList0) and list 1 references RefPicList1[0].
func (d *Decoder) directPartTemporalAt(col *refFrame, mbx, mby, cellIdx, x, y, w, h int) bPart {
	part := bPart{x: x, y: y, w: w, h: h, mask: 3}
	if col == nil {
		return part // defensive: conforming B slices always have L1[0]
	}
	idx := (mby*d.mbw+mbx)*16 + cellIdx
	mvCol := col.colMV[idx]
	refID := col.colRefID[idx]
	if refID < 0 {
		// Intra co-located block: mvCol = 0, refIdxL0 = 0 (spec 8.4.1.2.2).
		return part
	}
	// MapColToList0: lowest current L0 index referring to the same picture.
	refIdxL0 := -1
	refPOC := 0
	refLongTerm := false
	for i, rf := range d.curRefList[0] {
		if rf != nil && rf.id == int(refID) {
			refIdxL0 = i
			refPOC = rf.poc
			refLongTerm = rf.longTerm
			break
		}
	}
	if refIdxL0 < 0 {
		// Referenced picture not in the current L0 (non-conforming stream);
		// fall back to copying the co-located motion at index 0.
		part.mv[0] = mvCol
		return part
	}
	part.ref[0] = refIdxL0
	td := clampInt(col.poc-refPOC, -128, 127)
	if td == 0 || refLongTerm {
		// Long-term reference or same-POC references (spec 8.4.1.2.3):
		// mvL0 = mvCol, mvL1 = 0.
		part.mv[0] = mvCol
		return part
	}
	tb := clampInt(d.curPOC-refPOC, -128, 127)
	absTD := td
	if absTD < 0 {
		absTD = -absTD
	}
	tx := (16384 + absTD/2) / td
	dsf := clampInt((tb*tx+32)>>6, -1024, 1023)
	mv0 := [2]int16{
		int16((dsf*int(mvCol[0]) + 128) >> 8),
		int16((dsf*int(mvCol[1]) + 128) >> 8),
	}
	part.mv[0] = mv0
	part.mv[1] = [2]int16{mv0[0] - mvCol[0], mv0[1] - mvCol[1]}
	return part
}

// directParts appends the direct-mode partitions for 8x8 quadrant p to out.
// Spatial direct and temporal direct with direct_8x8_inference produce one
// 8x8 part (from the quadrant's corner 4x4 for temporal); temporal direct
// WITHOUT the inference flag derives motion independently for each of the
// quadrant's four 4x4 blocks (spec 8.4.1.2.2).
func (d *Decoder) directParts(ctx *bDirectCtx, mbx, mby, p int, out []bPart) []bPart {
	if ctx.temporal {
		col := d.getRefFrameEntry(1, 0)
		bx, by := part8x8Pos[p][0], part8x8Pos[p][1]
		if d.activeSPS == nil || d.activeSPS.Direct8x8Inference {
			corner := [4]int{0, 3, 12, 15}[p]
			return append(out, d.directPartTemporalAt(col, mbx, mby, corner, bx, by, 8, 8))
		}
		base := (by/4)*4 + bx/4
		for _, cell := range [4]int{base, base + 1, base + 4, base + 5} {
			x := (cell % 4) * 4
			y := (cell / 4) * 4
			out = append(out, d.directPartTemporalAt(col, mbx, mby, cell, x, y, 4, 4))
		}
		return out
	}
	if d.activeSPS == nil || d.activeSPS.Direct8x8Inference {
		return append(out, d.directPartSpatial(ctx, part8x8Pos[p][0], part8x8Pos[p][1], 8, 8, d.directColZero(mbx, mby, p)))
	}
	// Spatial direct without the inference flag: the MB-level MV applies, but
	// colZeroFlag is evaluated per 4x4 (spec 8.4.1.2.2). Untested by our
	// fixtures — x264 and the conformance streams here always pair spatial
	// direct with direct_8x8_inference — but mirrors the verified temporal
	// per-4x4 structure.
	bx, by := part8x8Pos[p][0], part8x8Pos[p][1]
	base := (by/4)*4 + bx/4
	for _, cell := range [4]int{base, base + 1, base + 4, base + 5} {
		x := (cell % 4) * 4
		y := (cell / 4) * 4
		out = append(out, d.directPartSpatial(ctx, x, y, 4, 4, d.directColZeroAt(mbx, mby, cell)))
	}
	return out
}

// directPartSpatial builds one spatial-direct bPart of the given geometry.
func (d *Decoder) directPartSpatial(ctx *bDirectCtx, x, y, w, h int, colZero bool) bPart {
	part := bPart{
		x:    x,
		y:    y,
		w:    w,
		h:    h,
		mask: ctx.mask,
		ref:  ctx.ref,
	}
	if ctx.mask&1 != 0 {
		part.mv[0] = ctx.mvp[0]
		if ctx.ref[0] == 0 && colZero {
			part.mv[0] = [2]int16{0, 0}
		}
	}
	if ctx.mask&2 != 0 {
		part.mv[1] = ctx.mvp[1]
		if ctx.ref[1] == 0 && colZero {
			part.mv[1] = [2]int16{0, 0}
		}
	}
	return part
}

// prepareBMB clears per-MB state shared by all B macroblock kinds. B MBs
// mark every 4x4 as decoded up front: unused-list partitions read as
// "available with reference -1" for MV prediction, matching the cache
// model of reference decoders.
func (d *Decoder) prepareBMB(mbx, mby int, mbType int) *mbInterInfo {
	idx := mby*d.mbw + mbx
	info := &d.mbInfo[idx]
	d.curMB = [2]int{mbx, mby}
	d.prepareYBR(mbx, mby)
	info.isIntra = false
	info.mbType = mbType
	info.decodedMask = 0xFFFF
	info.transform8x8 = false
	info.cbpCabac = 0
	info.chromaPredMode = 0
	info.i16OrPCM = false
	info.isPCM = false
	info.isDirectMB = false
	info.directMask = 0
	info.mvdAbs = [16][2]uint8{}
	info.mvdAbsL1 = [16][2]uint8{}
	info.mv = [16][2]int16{}
	info.mvL1 = [16][2]int16{}
	for k := 0; k < 4; k++ {
		info.refIdx[k] = -1
		info.refIdxL1[k] = -1
		info.refPicID[k] = -1
		info.refPicIDL1[k] = -1
		info.predMask[k] = 0
	}
	for i := range d.coeff {
		d.coeff[i] = 0
	}
	for i := range d.nzCoeffCur {
		d.nzCoeffCur[i] = 0
	}
	for i := range d.intraModeCur {
		d.intraModeCur[i] = -1
	}
	return info
}

// decodeMBDirect handles B_Skip and B_Direct_16x16 motion (shared).
func (d *Decoder) decodeMBDirect(sh *sliceHeader, mbx, mby int, skip bool) error {
	mbType := -1
	if !skip {
		mbType = -3 // direct 16x16
	}
	info := d.prepareBMB(mbx, mby, mbType)
	info.isDirectMB = true
	info.directMask = 0xFFFF
	ctx := d.deriveDirectMB(sh, mbx, mby)
	for p := 0; p < 4; p++ {
		var buf [4]bPart
		for _, part := range d.directParts(&ctx, mbx, mby, p, buf[:0]) {
			if err := d.execBPart(sh, mbx, mby, &part); err != nil {
				return err
			}
			d.storeBPart(info, &part)
		}
	}
	info.qp = d.qp
	info.hasCoef = false
	if skip {
		d.storeIntraModes(mbx, mby)
		d.storeNZCoeff(mbx, mby)
		d.copyMBToImg(mbx, mby)
	}
	return nil
}

// --- CAVLC B slice ----------------------------------------------------------

func (d *Decoder) decodeBSliceImpl(br *BitReader, sh *sliceHeader) (*image.YCbCr, error) {
	if len(d.refFrames) == 0 {
		return nil, fmt.Errorf("B-slice: no reference frames available")
	}
	if err := d.buildRefLists(sh); err != nil {
		return nil, fmt.Errorf("B-slice: %w", err)
	}

	totalMBs := d.mbw * d.mbh
	mbIdx := 0
	for mbIdx < totalMBs {
		skipRun, err := br.ReadUE()
		if err != nil {
			return nil, fmt.Errorf("mb_skip_run: %w", err)
		}
		for i := 0; i < int(skipRun) && mbIdx < totalMBs; i++ {
			mbx := mbIdx % d.mbw
			mby := mbIdx / d.mbw
			if err := d.decodeMBDirect(sh, mbx, mby, true); err != nil {
				return nil, fmt.Errorf("MB(%d,%d) B_Skip: %w", mbx, mby, err)
			}
			mbIdx++
		}
		if mbIdx >= totalMBs {
			break
		}

		mbx := mbIdx % d.mbw
		mby := mbIdx / d.mbw
		mbType, err := br.ReadUE()
		if err != nil {
			return nil, fmt.Errorf("MB(%d,%d) mb_type: %w", mbx, mby, err)
		}
		if int(mbType) >= 23 {
			if err := d.decodeMBIntraWithType(br, mbx, mby, int(mbType)-23); err != nil {
				return nil, fmt.Errorf("MB(%d,%d) intra: %w", mbx, mby, err)
			}
			idx := mby*d.mbw + mbx
			info := &d.mbInfo[idx]
			info.isIntra = true
			info.mbType = -2
			info.qp = d.pcmAwareQP(idx)
			info.isDirectMB = false
			info.directMask = 0
			for k := range info.mv {
				info.mv[k] = [2]int16{0, 0}
				info.mvL1[k] = [2]int16{0, 0}
			}
			for k := range info.refIdx {
				info.refIdx[k] = -1
				info.refIdxL1[k] = -1
				info.predMask[k] = 0
			}
			info.hasCoef = true
		} else if err := d.decodeMBInterB(br, mbx, mby, sh, int(mbType)); err != nil {
			return nil, fmt.Errorf("MB(%d,%d) B(mbType=%d): %w", mbx, mby, mbType, err)
		}
		mbIdx++
	}
	return d.cropImg(), nil
}

// decodeMBInterB decodes a non-intra, non-skip B macroblock with CAVLC.
func (d *Decoder) decodeMBInterB(br *BitReader, mbx, mby int, sh *sliceHeader, mbType int) error {
	shape := bMBTypes[mbType]
	info := d.prepareBMB(mbx, mby, mbType)

	numRef := [2]int{int(sh.numRefIdxL0Active), int(sh.numRefIdxL1Active)}
	var parts []bPart
	allSub8x8OrDirect := true

	if shape.direct {
		info.isDirectMB = true
		info.directMask = 0xFFFF
		ctx := d.deriveDirectMB(sh, mbx, mby)
		for p := 0; p < 4; p++ {
			var buf [4]bPart
			for _, part := range d.directParts(&ctx, mbx, mby, p, buf[:0]) {
				if err := d.execBPart(sh, mbx, mby, &part); err != nil {
					return err
				}
				d.storeBPart(info, &part)
				parts = append(parts, part)
			}
		}
	} else if shape.parts <= 2 {
		n := shape.parts
		geo := make([]bPart, n)
		for p := 0; p < n; p++ {
			geo[p] = bPart{w: 16, h: 16, mask: shape.mask[p]}
			if n == 2 {
				if shape.is16x8 {
					geo[p].h = 8
					geo[p].y = p * 8
				} else {
					geo[p].w = 8
					geo[p].x = p * 8
				}
			}
		}
		// ref_idx per list, then mvd per list (spec 7.3.5.1 mb_pred).
		for list := 0; list < 2; list++ {
			for p := 0; p < n; p++ {
				if geo[p].mask&(1<<uint(list)) == 0 {
					continue
				}
				if numRef[list] > 1 {
					te, err := readTE(br, numRef[list]-1)
					if err != nil {
						return err
					}
					geo[p].ref[list] = int(te)
				}
			}
			// Publish refs for the MV prediction context.
			for p := 0; p < n; p++ {
				d.publishBPartRefs(info, &geo[p], list)
			}
		}
		for list := 0; list < 2; list++ {
			for p := 0; p < n; p++ {
				if geo[p].mask&(1<<uint(list)) == 0 {
					continue
				}
				mvdX, err := br.ReadSE()
				if err != nil {
					return err
				}
				mvdY, err := br.ReadSE()
				if err != nil {
					return err
				}
				g := &geo[p]
				mvp := d.predictMVList(list, mbx, mby, g.x, g.y, g.w, g.h, g.ref[list])
				g.mv[list] = [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}
				d.publishBPartMVs(info, g, list)
			}
		}
		for p := 0; p < n; p++ {
			if err := d.execBPart(sh, mbx, mby, &geo[p]); err != nil {
				return err
			}
			d.storeBPart(info, &geo[p])
		}
		parts = geo
	} else {
		// B_8x8: sub_mb_type[4], refs per list, mvds per list per sub-part.
		var subShapes [4]bSubShape
		var subDirect [4]bool
		var dctx bDirectCtx
		haveDirectCtx := false
		for p := 0; p < 4; p++ {
			st, err := br.ReadUE()
			if err != nil {
				return err
			}
			if int(st) >= len(bSubTypes) {
				return fmt.Errorf("invalid B sub_mb_type %d", st)
			}
			subShapes[p] = bSubTypes[st]
			subDirect[p] = subShapes[p].direct
			info.subMBType[p] = int(st)
			if !subShapes[p].direct && (subShapes[p].w != 8 || subShapes[p].h != 8) {
				allSub8x8OrDirect = false
			}
		}
		var refs [4][2]int
		for list := 0; list < 2; list++ {
			for p := 0; p < 4; p++ {
				if subDirect[p] || subShapes[p].mask&(1<<uint(list)) == 0 {
					continue
				}
				if numRef[list] > 1 {
					te, err := readTE(br, numRef[list]-1)
					if err != nil {
						return err
					}
					refs[p][list] = int(te)
				}
			}
			for p := 0; p < 4; p++ {
				if subDirect[p] || subShapes[p].mask&(1<<uint(list)) == 0 {
					continue
				}
				part := (part8x8Pos[p][1]/8)*2 + part8x8Pos[p][0]/8
				if list == 0 {
					info.refIdx[part] = refs[p][0]
					info.predMask[part] |= 1
				} else {
					info.refIdxL1[part] = refs[p][1]
					info.predMask[part] |= 2
				}
			}
		}
		// Direct sub-partitions derive and execute before mvd parsing uses
		// their motion as prediction context.
		for p := 0; p < 4; p++ {
			if !subDirect[p] {
				continue
			}
			if !haveDirectCtx {
				dctx = d.deriveDirectMB(sh, mbx, mby)
				haveDirectCtx = true
			}
			var buf [4]bPart
			for _, part := range d.directParts(&dctx, mbx, mby, p, buf[:0]) {
				d.storeBPart(info, &part)
				for by := part.y / 4; by < (part.y+part.h)/4; by++ {
					for bx := part.x / 4; bx < (part.x+part.w)/4; bx++ {
						info.directMask |= uint16(1) << uint(by*4+bx)
					}
				}
				if err := d.execBPart(sh, mbx, mby, &part); err != nil {
					return err
				}
				parts = append(parts, part)
			}
		}
		for list := 0; list < 2; list++ {
			for p := 0; p < 4; p++ {
				if subDirect[p] || subShapes[p].mask&(1<<uint(list)) == 0 {
					continue
				}
				s := subShapes[p]
				for sp := 0; sp < s.subParts; sp++ {
					sx := part8x8Pos[p][0] + (sp%(8/s.w))*s.w
					sy := part8x8Pos[p][1] + (sp/(8/s.w))*s.h
					mvdX, err := br.ReadSE()
					if err != nil {
						return err
					}
					mvdY, err := br.ReadSE()
					if err != nil {
						return err
					}
					info.decodedMask = bSubDecodedMask(&subShapes, p, sp)
					mvp := d.predictMVList(list, mbx, mby, sx, sy, s.w, s.h, refs[p][list])
					info.decodedMask = 0xFFFF
					mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}
					for by := sy / 4; by < (sy+s.h)/4; by++ {
						for bx := sx / 4; bx < (sx+s.w)/4; bx++ {
							k := by*4 + bx
							if list == 0 {
								info.mv[k] = mv
							} else {
								info.mvL1[k] = mv
							}
						}
					}
					part := bPart{x: sx, y: sy, w: s.w, h: s.h, mask: s.mask,
						ref: refs[p], mv: [2][2]int16{}}
					part.mv[list] = mv
					parts = append(parts, part)
				}
			}
		}
		// Execute non-direct sub-partitions: merge the two per-list passes
		// into per-region MC now that all MVs are known.
		for p := 0; p < 4; p++ {
			if subDirect[p] {
				continue
			}
			s := subShapes[p]
			for sp := 0; sp < s.subParts; sp++ {
				sx := part8x8Pos[p][0] + (sp%(8/s.w))*s.w
				sy := part8x8Pos[p][1] + (sp/(8/s.w))*s.h
				part := bPart{x: sx, y: sy, w: s.w, h: s.h, mask: s.mask, ref: refs[p]}
				for by := sy / 4; by < (sy+s.h)/4; by++ {
					bx := sx / 4
					k := by*4 + bx
					part.mv[0] = info.mv[k]
					part.mv[1] = info.mvL1[k]
					break
				}
				if err := d.execBPart(sh, mbx, mby, &part); err != nil {
					return err
				}
				d.storeBPart(info, &part)
			}
		}
	}
	_ = parts

	// CBP and residual.
	cbpCode, err := br.ReadUE()
	if err != nil {
		return fmt.Errorf("cbp: %w", err)
	}
	if int(cbpCode) >= len(cbpTableInter) {
		return fmt.Errorf("invalid inter CBP code %d", cbpCode)
	}
	cbp := cbpTableInter[cbpCode]
	cbpLuma := cbp & 15

	use8x8Transform := false
	if d.activePPS.Transform8x8Mode && cbpLuma > 0 {
		allowed := allSub8x8OrDirect
		if shape.direct || shape.parts == 4 {
			allowed = allowed && d.activeSPS.Direct8x8Inference
		}
		if allowed {
			t8x8, err := br.ReadBool()
			if err != nil {
				return fmt.Errorf("transform_size_8x8_flag: %w", err)
			}
			use8x8Transform = t8x8
		}
	}
	info.transform8x8 = use8x8Transform

	hasCoef, err := d.decodeInterResidualCAVLC(br, mbx, mby, use8x8Transform, cbp)
	if err != nil {
		return err
	}
	info.hasCoef = hasCoef
	info.qp = d.qp
	d.storeIntraModes(mbx, mby)
	d.storeNZCoeff(mbx, mby)
	d.copyMBToImg(mbx, mby)
	return nil
}

// publishBPartRefs records a partition's reference index for one list into
// mbInfo before MV prediction runs (the prediction context reads it).
func (d *Decoder) publishBPartRefs(info *mbInterInfo, g *bPart, list int) {
	for by := g.y / 8; by < (g.y+g.h+7)/8; by++ {
		for bx := g.x / 8; bx < (g.x+g.w+7)/8; bx++ {
			part := by*2 + bx
			if g.mask&(1<<uint(list)) == 0 {
				continue
			}
			if list == 0 {
				info.refIdx[part] = g.ref[0]
				info.predMask[part] |= 1
			} else {
				info.refIdxL1[part] = g.ref[1]
				info.predMask[part] |= 2
			}
		}
	}
}

// publishBPartMVs records a partition's just-computed MV for one list.
func (d *Decoder) publishBPartMVs(info *mbInterInfo, g *bPart, list int) {
	for by := g.y / 4; by < (g.y+g.h)/4; by++ {
		for bx := g.x / 4; bx < (g.x+g.w)/4; bx++ {
			k := by*4 + bx
			if list == 0 {
				info.mv[k] = g.mv[0]
			} else {
				info.mvL1[k] = g.mv[1]
			}
		}
	}
}

var _ = image.Point{}

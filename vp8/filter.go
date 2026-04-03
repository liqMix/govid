// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vp8

// The filter functions below are direct ports of libvpx's loopfilter_filters.c.
// They use signed-domain arithmetic (XOR 0x80) matching the C code exactly.

// signedCharClamp clamps to [-128, 127]. Port of vp8_signed_char_clamp.
func signedCharClamp(t int) int8 {
	if t < -128 {
		return -128
	}
	if t > 127 {
		return 127
	}
	return int8(t)
}

// vp8FilterMask returns -1 (filter active) or 0 (skip). Port of vp8_filter_mask.
func vp8FilterMask(limit, blimit int, p3, p2, p1, p0, q0, q1, q2, q3 uint8) int8 {
	var mask int8
	mask |= int8(btoi(abs(int(p3)-int(p2)) > limit))
	mask |= int8(btoi(abs(int(p2)-int(p1)) > limit))
	mask |= int8(btoi(abs(int(p1)-int(p0)) > limit))
	mask |= int8(btoi(abs(int(q1)-int(q0)) > limit))
	mask |= int8(btoi(abs(int(q2)-int(q1)) > limit))
	mask |= int8(btoi(abs(int(q3)-int(q2)) > limit))
	mask |= int8(btoi(abs(int(p0)-int(q0))*2+abs(int(p1)-int(q1))/2 > blimit))
	return mask - 1
}

// vp8HevMask returns -1 (high variance) or 0 (low variance). Port of vp8_hevmask.
func vp8HevMask(thresh int, p1, p0, q0, q1 uint8) int8 {
	var hev int8
	if abs(int(p1)-int(p0)) > thresh {
		hev = -1
	}
	if abs(int(q1)-int(q0)) > thresh {
		hev = -1
	}
	return hev
}

// vp8Filter is the sub-block edge filter. Port of vp8_filter from libvpx.
// When hev: 2-pixel filter (p0, q0 only).
// When !hev: 4-pixel filter (p1, p0, q0, q1).
func vp8Filter(mask, hev int8, op1, op0, oq0, oq1 *byte) {
	ps1 := int(int8(*op1) ^ -128) // (signed char)*op1 ^ 0x80
	ps0 := int(int8(*op0) ^ -128)
	qs0 := int(int8(*oq0) ^ -128)
	qs1 := int(int8(*oq1) ^ -128)

	// Add outer taps if high edge variance.
	filterValue := int(signedCharClamp(ps1 - qs1))
	filterValue &= int(hev)

	// Inner taps.
	filterValue = int(signedCharClamp(filterValue + 3*(qs0-ps0)))
	filterValue &= int(mask)

	filter1 := int(signedCharClamp(filterValue + 4))
	filter2 := int(signedCharClamp(filterValue + 3))
	filter1 >>= 3
	filter2 >>= 3

	*oq0 = uint8(signedCharClamp(qs0-filter1) ^ -128)
	*op0 = uint8(signedCharClamp(ps0+filter2) ^ -128)

	// Outer tap adjustments (only when !hev).
	filterValue = filter1
	filterValue += 1
	filterValue >>= 1
	filterValue &= int(^hev)

	*oq1 = uint8(signedCharClamp(qs1-filterValue) ^ -128)
	*op1 = uint8(signedCharClamp(ps1+filterValue) ^ -128)
}

// vp8MBFilter is the MB edge filter. Port of vp8_mbfilter from libvpx.
// When hev: 2-pixel filter (p0, q0 only).
// When !hev: 6-pixel filter (p2, p1, p0, q0, q1, q2).
func vp8MBFilter(mask, hev int8, op2, op1, op0, oq0, oq1, oq2 *byte) {
	ps2 := int(int8(*op2) ^ -128)
	ps1 := int(int8(*op1) ^ -128)
	ps0 := int(int8(*op0) ^ -128)
	qs0 := int(int8(*oq0) ^ -128)
	qs1 := int(int8(*oq1) ^ -128)
	qs2 := int(int8(*oq2) ^ -128)

	// Add outer taps if high edge variance.
	filterValue := int(signedCharClamp(ps1 - qs1))
	filterValue = int(signedCharClamp(filterValue + 3*(qs0-ps0)))
	filterValue &= int(mask)

	filter2 := filterValue
	filter2 &= int(hev)

	// Save bottom 3 bits for rounding (+4 one side, +3 the other).
	filter1 := int(signedCharClamp(filter2 + 4))
	filter2 = int(signedCharClamp(filter2 + 3))
	filter1 >>= 3
	filter2 >>= 3
	qs0 = int(signedCharClamp(qs0 - filter1))
	ps0 = int(signedCharClamp(ps0 + filter2))

	// Only apply wider filter if not high edge variance.
	filterValue &= int(^hev)
	filter2 = filterValue

	// ~3/7th
	u := int(signedCharClamp((63 + filter2*27) >> 7))
	*oq0 = uint8(signedCharClamp(qs0-u) ^ -128)
	*op0 = uint8(signedCharClamp(ps0+u) ^ -128)

	// ~2/7th
	u = int(signedCharClamp((63 + filter2*18) >> 7))
	*oq1 = uint8(signedCharClamp(qs1-u) ^ -128)
	*op1 = uint8(signedCharClamp(ps1+u) ^ -128)

	// ~1/7th
	u = int(signedCharClamp((63 + filter2*9) >> 7))
	*oq2 = uint8(signedCharClamp(qs2-u) ^ -128)
	*op2 = uint8(signedCharClamp(ps2+u) ^ -128)
}

// loopFilterEdge applies the sub-block edge filter along one edge.
// s is the full plane buffer, off is the offset of the edge (first q0 pixel).
// p is the step between rows (1 for vertical edge, stride for horizontal edge).
// iStep is the step between columns (stride for vertical edge, 1 for horizontal edge).
func loopFilterEdge(s []byte, off, p, iStep, blimit, limit, thresh, count int) {
	for i := 0; i < count*8; i++ {
		idx := off + i*iStep
		mask := vp8FilterMask(limit, blimit,
			s[idx-4*p], s[idx-3*p], s[idx-2*p], s[idx-1*p],
			s[idx], s[idx+p], s[idx+2*p], s[idx+3*p])
		hev := vp8HevMask(thresh, s[idx-2*p], s[idx-1*p], s[idx], s[idx+p])
		vp8Filter(mask, hev, &s[idx-2*p], &s[idx-1*p], &s[idx], &s[idx+p])
	}
}

// loopFilterMBEdge applies the MB edge filter along one edge.
func loopFilterMBEdge(s []byte, off, p, iStep, blimit, limit, thresh, count int) {
	for i := 0; i < count*8; i++ {
		idx := off + i*iStep
		mask := vp8FilterMask(limit, blimit,
			s[idx-4*p], s[idx-3*p], s[idx-2*p], s[idx-1*p],
			s[idx], s[idx+p], s[idx+2*p], s[idx+3*p])
		hev := vp8HevMask(thresh, s[idx-2*p], s[idx-1*p], s[idx], s[idx+p])
		vp8MBFilter(mask, hev, &s[idx-3*p], &s[idx-2*p], &s[idx-1*p], &s[idx], &s[idx+p], &s[idx+2*p])
	}
}

// simpleFilter implements the simple filter, as specified in section 15.2.
func (d *Decoder) simpleFilter() {
	ys := d.img.YStride
	for mby := 0; mby < d.mbh; mby++ {
		for mbx := 0; mbx < d.mbw; mbx++ {
			f := d.perMBFilterParams[d.mbw*mby+mbx]
			if f.level == 0 {
				continue
			}
			blim := int(f.level)
			mblim := blim + 4
			il, hl := int(f.ilevel), int(f.hlevel)
			yBase := (mby*ys + mbx) * 16
			if mbx > 0 {
				loopFilterEdge(d.img.Y, yBase, 1, ys, mblim, il, hl, 2)
			}
			if f.inner {
				loopFilterEdge(d.img.Y, yBase+4, 1, ys, blim, il, hl, 2)
				loopFilterEdge(d.img.Y, yBase+8, 1, ys, blim, il, hl, 2)
				loopFilterEdge(d.img.Y, yBase+12, 1, ys, blim, il, hl, 2)
			}
			if mby > 0 {
				loopFilterEdge(d.img.Y, yBase, ys, 1, mblim, il, hl, 2)
			}
			if f.inner {
				loopFilterEdge(d.img.Y, yBase+ys*4, ys, 1, blim, il, hl, 2)
				loopFilterEdge(d.img.Y, yBase+ys*8, ys, 1, blim, il, hl, 2)
				loopFilterEdge(d.img.Y, yBase+ys*12, ys, 1, blim, il, hl, 2)
			}
		}
	}
}

// normalFilter implements the normal filter, as specified in section 15.3.
// Direct port of libvpx's vp8_loop_filter_row_normal per-MB logic.
func (d *Decoder) normalFilter() {
	ys := d.img.YStride
	cs := d.img.CStride
	for mby := 0; mby < d.mbh; mby++ {
		for mbx := 0; mbx < d.mbw; mbx++ {
			f := d.perMBFilterParams[d.mbw*mby+mbx]
			if f.level == 0 {
				continue
			}
			blim := int(f.level)
			mblim := blim + 4
			il := int(f.ilevel)
			hl := int(f.hlevel)

			yBase := (mby*ys + mbx) * 16
			cBase := (mby*cs + mbx) * 8

			// Vertical MB edge (left boundary).
			if mbx > 0 {
				loopFilterMBEdge(d.img.Y, yBase, 1, ys, mblim, il, hl, 2)
				loopFilterMBEdge(d.img.Cb, cBase, 1, cs, mblim, il, hl, 1)
				loopFilterMBEdge(d.img.Cr, cBase, 1, cs, mblim, il, hl, 1)
			}
			// Vertical inner edges.
			if f.inner {
				loopFilterEdge(d.img.Y, yBase+4, 1, ys, blim, il, hl, 2)
				loopFilterEdge(d.img.Y, yBase+8, 1, ys, blim, il, hl, 2)
				loopFilterEdge(d.img.Y, yBase+12, 1, ys, blim, il, hl, 2)
				loopFilterEdge(d.img.Cb, cBase+4, 1, cs, blim, il, hl, 1)
				loopFilterEdge(d.img.Cr, cBase+4, 1, cs, blim, il, hl, 1)
			}
			// Horizontal MB edge (top boundary).
			if mby > 0 {
				loopFilterMBEdge(d.img.Y, yBase, ys, 1, mblim, il, hl, 2)
				loopFilterMBEdge(d.img.Cb, cBase, cs, 1, mblim, il, hl, 1)
				loopFilterMBEdge(d.img.Cr, cBase, cs, 1, mblim, il, hl, 1)
			}
			// Horizontal inner edges.
			if f.inner {
				loopFilterEdge(d.img.Y, yBase+ys*4, ys, 1, blim, il, hl, 2)
				loopFilterEdge(d.img.Y, yBase+ys*8, ys, 1, blim, il, hl, 2)
				loopFilterEdge(d.img.Y, yBase+ys*12, ys, 1, blim, il, hl, 2)
				loopFilterEdge(d.img.Cb, cBase+cs*4, cs, 1, blim, il, hl, 1)
				loopFilterEdge(d.img.Cr, cBase+cs*4, cs, 1, blim, il, hl, 1)
			}
		}
	}
}

// loopFilterInitLUT initializes the HEV threshold lookup table.
// Direct port of libvpx lf_init_lut.
func (d *Decoder) loopFilterInitLUT() {
	for i := 0; i <= 63; i++ {
		if i >= 40 {
			d.filterHevThr[0][i] = 2 // KEY_FRAME
			d.filterHevThr[1][i] = 3 // INTER_FRAME
		} else if i >= 20 {
			d.filterHevThr[0][i] = 1
			d.filterHevThr[1][i] = 2
		} else if i >= 15 {
			d.filterHevThr[0][i] = 1
			d.filterHevThr[1][i] = 1
		} else {
			d.filterHevThr[0][i] = 0
			d.filterHevThr[1][i] = 0
		}
	}
}

// loopFilterUpdateSharpness computes lim/blim/mblim tables for each filter level.
// Direct port of libvpx vp8_loop_filter_update_sharpness.
func (d *Decoder) loopFilterUpdateSharpness(sharpness int) {
	for i := 1; i <= 63; i++ {
		blockInsideLimit := i >> (btoi(sharpness > 0) + btoi(sharpness > 4))
		if sharpness > 0 {
			if blockInsideLimit > (9 - sharpness) {
				blockInsideLimit = 9 - sharpness
			}
		}
		if blockInsideLimit < 1 {
			blockInsideLimit = 1
		}
		d.filterLim[i] = uint8(blockInsideLimit)
		d.filterBlim[i] = uint8(2*i + blockInsideLimit)
		d.filterMblim[i] = uint8(2*(i+2) + blockInsideLimit)
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// loopFilterFrameInit precomputes filter levels for all (segment, ref, mode) combos.
// Direct port of libvpx vp8_loop_filter_frame_init.
func (d *Decoder) loopFilterFrameInit() {
	sharpness := int(d.filterHeader.sharpness)
	if sharpness != int(d.lastSharpness) {
		d.loopFilterUpdateSharpness(sharpness)
		d.lastSharpness = int8(sharpness)
	}

	for seg := 0; seg < nSegment; seg++ {
		lvlSeg := int(d.filterHeader.level)
		if d.segmentHeader.useSegment {
			if !d.segmentHeader.relativeDelta {
				lvlSeg = int(d.segmentHeader.filterStrength[seg])
			} else {
				lvlSeg += int(d.segmentHeader.filterStrength[seg])
			}
			if lvlSeg < 0 {
				lvlSeg = 0
			} else if lvlSeg > 63 {
				lvlSeg = 63
			}
		}

		if !d.filterHeader.useLFDelta {
			// No deltas: all ref/mode combos get the same level.
			for ref := 0; ref < 4; ref++ {
				for mode := 0; mode < 4; mode++ {
					d.filterLvl[seg][ref][mode] = lvlSeg
				}
			}
			continue
		}

		// INTRA_FRAME (ref=0)
		ref := 0
		lvlRef := lvlSeg + int(d.filterHeader.refLFDelta[ref])

		// mode=0: gets mode_lf_deltas[0] (B_PRED in libvpx's mode_lf_lut)
		lvlMode := lvlRef + int(d.filterHeader.modeLFDelta[0])
		if lvlMode < 0 {
			lvlMode = 0
		} else if lvlMode > 63 {
			lvlMode = 63
		}
		d.filterLvl[seg][ref][0] = lvlMode

		// mode=1: NO mode delta (non-B_PRED in libvpx's mode_lf_lut)
		lvlMode = lvlRef
		if lvlMode < 0 {
			lvlMode = 0
		} else if lvlMode > 63 {
			lvlMode = 63
		}
		d.filterLvl[seg][ref][1] = lvlMode

		// Inter frames (ref=1,2,3)
		for ref = 1; ref < 4; ref++ {
			lvlRef = lvlSeg + int(d.filterHeader.refLFDelta[ref])
			for mode := 1; mode < 4; mode++ {
				lvlMode = lvlRef + int(d.filterHeader.modeLFDelta[mode])
				if lvlMode < 0 {
					lvlMode = 0
				} else if lvlMode > 63 {
					lvlMode = 63
				}
				d.filterLvl[seg][ref][mode] = lvlMode
			}
		}
	}
}

// modeLFLut returns the mode_lf_lut index for the current MB.
// Matches libvpx's mode_lf_lut mapping.
func (d *Decoder) modeLFLut() int {
	if d.curRefFrame == 0 {
		// INTRA: B_PRED=0, non-B_PRED=1
		if !d.usePredY16 {
			return 0 // B_PRED
		}
		return 1 // DC/V/H/TM
	}
	// Inter: ZEROMV=1, MV=2, SPLIT=3
	switch d.curMode {
	case interModeZEROMV:
		return 1
	case interModeNEARESTMV, interModeNEARMV, interModeNEWMV:
		return 2
	case interModeSPLITMV:
		return 3
	}
	return 1
}

// lookupFilterParam returns the filter parameters for the current MB using
// the precomputed LUT.
func (d *Decoder) lookupFilterParam() filterParam {
	filtLvl := d.filterLvl[d.segment][d.curRefFrame][d.modeLFLut()]
	if filtLvl <= 0 {
		return filterParam{}
	}
	frameType := 0
	if !d.frameHeader.KeyFrame {
		frameType = 1
	}
	return filterParam{
		level:  d.filterBlim[filtLvl],
		ilevel: d.filterLim[filtLvl],
		hlevel: d.filterHevThr[frameType][filtLvl],
	}
}

// filterParam holds the loop filter parameters for a macroblock.
type filterParam struct {
	// The first three fields are thresholds used by the loop filter to smooth
	// over the edges and interior of a macroblock. level is used by both the
	// simple and normal filters. The inner level and high edge variance level
	// are only used by the normal filter.
	level, ilevel, hlevel uint8
	// inner is whether the inner loop filter cannot be optimized out as a
	// no-op for this particular macroblock.
	inner bool
}

// computeFilterParams and computeInterFilterParam have been replaced by
// the precomputed LUT approach (loopFilterFrameInit + lookupFilterParam),
// which is a direct port of libvpx's vp8_loop_filter_frame_init.

// intSize is either 32 or 64.
const intSize = 32 << (^uint(0) >> 63)

func abs(x int) int {
	// m := -1 if x < 0. m := 0 otherwise.
	m := x >> (intSize - 1)

	// In two's complement representation, the negative number
	// of any number (except the smallest one) can be computed
	// by flipping all the bits and add 1. This is faster than
	// code with a branch.
	// See Hacker's Delight, section 2-4.
	return (x ^ m) - m
}

func clamp15(x int) int {
	if x < -16 {
		return -16
	}
	if x > 15 {
		return 15
	}
	return x
}

func clamp127(x int) int {
	if x < -128 {
		return -128
	}
	if x > 127 {
		return 127
	}
	return x
}

func clamp255(x int) uint8 {
	if x < 0 {
		return 0
	}
	if x > 255 {
		return 255
	}
	return uint8(x)
}

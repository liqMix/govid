package h264

import (
	"fmt"
	"image"
)

// CABAC macroblock-layer decoding (spec 7.3.5, 9.3.3.1). Syntax element
// context derivations are ported against FFmpeg's h264_cabac.c, which is
// bit-exact with the spec; reconstruction reuses the same prediction,
// transform, and motion compensation helpers as the CAVLC paths.

// Residual block categories (spec 9.3.3.1.3 ctxBlockCat).
const (
	catLumaDC   = 0 // Intra16x16DCLevel
	catLumaAC   = 1 // Intra16x16ACLevel
	catLuma4x4  = 2 // LumaLevel4x4
	catChromaDC = 3 // ChromaDCLevel
	catChromaAC = 4 // ChromaACLevel
	catLuma8x8  = 5 // LumaLevel8x8
)

// Context index offsets per category (frame coding). From FFmpeg
// h264_cabac.c; equal to the spec Table 9-11 offsets plus category offsets.
var (
	cabacCBFBase        = [5]int{85, 89, 93, 97, 101}
	cabacSigOffset      = [6]int{105, 120, 134, 149, 152, 402}
	cabacLastOffset     = [6]int{166, 181, 195, 210, 213, 417}
	cabacAbsLevelOffset = [6]int{227, 237, 247, 257, 266, 426}
)

// Significance-map context increments for 8x8 blocks (frame coding),
// spec Table 9-43 / FFmpeg significant_coeff_flag_offset_8x8[0].
var sigCoeffFlagOffset8x8 = [63]uint8{
	0, 1, 2, 3, 4, 5, 5, 4, 4, 3, 3, 4, 4, 4, 5, 5,
	4, 4, 4, 4, 3, 3, 6, 7, 7, 7, 8, 9, 10, 9, 8, 7,
	7, 6, 11, 12, 13, 11, 6, 7, 8, 9, 14, 10, 9, 8, 6, 11,
	12, 13, 11, 6, 9, 14, 10, 9, 11, 12, 13, 11, 14, 10, 12,
}

// last_significant_coeff_flag context increments for 8x8 blocks,
// spec Table 9-43 / FFmpeg ff_h264_last_coeff_flag_offset_8x8.
var lastCoeffFlagOffset8x8 = [63]uint8{
	0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
	3, 3, 3, 3, 3, 3, 3, 3, 4, 4, 4, 4, 4, 4, 4, 4,
	5, 5, 5, 5, 6, 6, 6, 6, 7, 7, 7, 7, 8, 8, 8,
}

// coeff_abs_level_minus1 node-context machinery (FFmpeg h264_cabac.c).
var (
	coeffAbsLevel1Ctx       = [8]uint8{1, 2, 3, 4, 0, 0, 0, 0}
	coeffAbsLevelGt1Ctx     = [8]uint8{5, 5, 5, 5, 6, 7, 8, 9}
	coeffAbsLevelTransition = [2][8]uint8{
		{1, 2, 3, 3, 4, 5, 6, 7},
		{4, 4, 4, 4, 5, 6, 7, 7},
	}
)

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- Neighbor context helpers ---------------------------------------------

// cbpCabacDefault is the packed cbp value assumed for an unavailable
// neighbor MB: luma bits set (condTermFlag 0), chroma 0, DC cbfs set only
// when the current MB is intra (FFmpeg 0x7CF / 0x00F, 4:2:0 subset).
func cbpCabacDefault(curIntra bool) uint16 {
	if curIntra {
		return 0x1CF
	}
	return 0x00F
}

func (d *Decoder) neighborCbpCabac(nx, ny int, curIntra bool) uint16 {
	if nx < 0 || ny < 0 {
		return cbpCabacDefault(curIntra)
	}
	return d.mbInfo[ny*d.mbw+nx].cbpCabac
}

// mbAvail reports whether the MB at (nx, ny) is available as a neighbor
// (within the picture; single slice per frame is assumed).
func (d *Decoder) mbAvail(nx, ny int) bool {
	return nx >= 0 && ny >= 0 && nx < d.mbw && ny < d.mbh
}

// cabacCBFCtx derives the coded_block_flag ctxIdx (spec 9.3.3.1.1.9) for
// categories 0-4. blkIdx: cat 1/2 luma 4x4 scan index; cat 3: 0=Cb 1=Cr;
// cat 4: chroma AC index 0-7 (0-3 Cb, 4-7 Cr).
func (d *Decoder) cabacCBFCtx(cat, blkIdx, mbx, mby int, curIntra bool) int {
	nza, nzb := 0, 0
	switch cat {
	case catLumaDC:
		if d.neighborCbpCabac(mbx-1, mby, curIntra)&0x100 != 0 {
			nza = 1
		}
		if d.neighborCbpCabac(mbx, mby-1, curIntra)&0x100 != 0 {
			nzb = 1
		}
	case catChromaDC:
		bit := uint16(1) << uint(6+blkIdx)
		if d.neighborCbpCabac(mbx-1, mby, curIntra)&bit != 0 {
			nza = 1
		}
		if d.neighborCbpCabac(mbx, mby-1, curIntra)&bit != 0 {
			nzb = 1
		}
	case catLumaAC, catLuma4x4:
		nza = d.cbfNeighborVal(d.getNCLuma(mbx, mby, blkIdx, true), curIntra)
		nzb = d.cbfNeighborVal(d.getNCLuma(mbx, mby, blkIdx, false), curIntra)
	case catChromaAC:
		nza = d.cbfNeighborVal(d.getNCChromaLeft(mbx, mby, blkIdx), curIntra)
		nzb = d.cbfNeighborVal(d.getNCChromaAbove(mbx, mby, blkIdx), curIntra)
	}
	ctx := 0
	if nza > 0 {
		ctx++
	}
	if nzb > 0 {
		ctx += 2
	}
	return cabacCBFBase[cat] + ctx
}

// cbfNeighborVal maps a getNC* result (-1 = unavailable) to the cbf
// condTermFlag source: unavailable counts as coded iff the current MB is
// intra (FFmpeg's 64/0 cache markers).
func (d *Decoder) cbfNeighborVal(nc int, curIntra bool) int {
	if nc < 0 {
		return b2i(curIntra)
	}
	return nc
}

// --- Residual block decoding (spec 7.3.5.3.3 residual_block_cabac) ---------

// cabacResidual decodes one residual block into coeffs, which must be
// pre-zeroed and indexed by scan position (zigzag order; the caller maps to
// raster). For 15-coefficient AC blocks pass the subslice starting at scan
// position 1. Returns the number of non-zero coefficients and updates
// nzCoeffCur / packed DC cbf bits for later neighbor contexts.
func (d *Decoder) cabacResidual(cd *cabacDecoder, cat, blkIdx int, coeffs []int16, maxCoeff, mbx, mby int, curIntra bool) int {
	info := &d.mbInfo[mby*d.mbw+mbx]

	if cat != catLuma8x8 {
		if cd.decodeDecision(d.cabacCBFCtx(cat, blkIdx, mbx, mby, curIntra)) == 0 {
			switch cat {
			case catLumaAC, catLuma4x4:
				d.nzCoeffCur[blkIdx] = 0
			case catChromaAC:
				d.nzCoeffCur[16+blkIdx] = 0
			}
			return 0
		}
	}

	// Significance map.
	var index [64]int
	count := 0
	i := 0
	for ; i < maxCoeff-1; i++ {
		sigInc, lastInc := i, i
		if cat == catLuma8x8 {
			sigInc = int(sigCoeffFlagOffset8x8[i])
			lastInc = int(lastCoeffFlagOffset8x8[i])
		}
		if cd.decodeDecision(cabacSigOffset[cat]+sigInc) == 1 {
			index[count] = i
			count++
			if cd.decodeDecision(cabacLastOffset[cat]+lastInc) == 1 {
				i = maxCoeff
				break
			}
		}
	}
	if i == maxCoeff-1 {
		index[count] = i
		count++
	}

	// Record cbf / non-zero state for neighbor contexts and deblocking.
	switch cat {
	case catLumaDC:
		info.cbpCabac |= 0x100
	case catChromaDC:
		info.cbpCabac |= uint16(1) << uint(6+blkIdx)
	case catLumaAC, catLuma4x4:
		d.nzCoeffCur[blkIdx] = count
	case catChromaAC:
		d.nzCoeffCur[16+blkIdx] = count
	case catLuma8x8:
		for k := 0; k < 4; k++ {
			d.nzCoeffCur[blkIdx*4+k] = count
		}
	}

	// Levels, decoded from the last significant coefficient backwards.
	total := count
	nodeCtx := 0
	for count > 0 {
		count--
		pos := index[count]
		ctx := cabacAbsLevelOffset[cat] + int(coeffAbsLevel1Ctx[nodeCtx])
		if cd.decodeDecision(ctx) == 0 {
			nodeCtx = int(coeffAbsLevelTransition[0][nodeCtx])
			coeffs[pos] = int16(cd.decodeBypassSign(1))
		} else {
			ctx = cabacAbsLevelOffset[cat] + int(coeffAbsLevelGt1Ctx[nodeCtx])
			nodeCtx = int(coeffAbsLevelTransition[1][nodeCtx])
			abs := 2
			for abs < 15 && cd.decodeDecision(ctx) == 1 {
				abs++
			}
			if abs >= 15 {
				// Exp-Golomb suffix (bypass).
				j := 0
				for cd.decodeBypass() == 1 && j < 23 {
					j++
				}
				abs = 1
				for ; j > 0; j-- {
					abs = abs*2 + cd.decodeBypass()
				}
				abs += 14
			}
			coeffs[pos] = int16(cd.decodeBypassSign(abs))
		}
	}
	if DebugCabacResidual != nil {
		DebugCabacResidual(mbx, mby, cat, blkIdx, coeffs)
	}
	return total
}

// --- Syntax element decoders ----------------------------------------------

// decodeCabacIntraMBType decodes an intra mb_type (Table 9-36/9-39).
// ctxBase is 3 in I slices (with neighbor-dependent first bin) and 17 for
// the intra suffix in P slices. Returns the CAVLC mb_type numbering:
// 0 = I_NxN, 1-24 = I_16x16 variants, 25 = I_PCM.
func (d *Decoder) decodeCabacIntraMBType(cd *cabacDecoder, mbx, mby, ctxBase int, intraSlice bool) int {
	base := ctxBase
	if intraSlice {
		inc := 0
		if d.mbAvail(mbx-1, mby) && d.mbInfo[mby*d.mbw+mbx-1].i16OrPCM {
			inc++
		}
		if d.mbAvail(mbx, mby-1) && d.mbInfo[(mby-1)*d.mbw+mbx].i16OrPCM {
			inc++
		}
		if cd.decodeDecision(base+inc) == 0 {
			return 0
		}
		base += 2
	} else {
		if cd.decodeDecision(base) == 0 {
			return 0
		}
	}
	if cd.decodeTerminate() == 1 {
		return 25
	}
	is := b2i(intraSlice)
	mbType := 1
	mbType += 12 * cd.decodeDecision(base+1)
	if cd.decodeDecision(base+2) == 1 {
		mbType += 4 + 4*cd.decodeDecision(base+2+is)
	}
	mbType += 2 * cd.decodeDecision(base+3+is)
	mbType += cd.decodeDecision(base + 3 + 2*is)
	return mbType
}

func (d *Decoder) decodeCabacMBSkip(cd *cabacDecoder, mbx, mby int) bool {
	ctx := 0
	if d.mbAvail(mbx-1, mby) && d.mbInfo[mby*d.mbw+mbx-1].mbType != -1 {
		ctx++
	}
	if d.mbAvail(mbx, mby-1) && d.mbInfo[(mby-1)*d.mbw+mbx].mbType != -1 {
		ctx++
	}
	return cd.decodeDecision(11+ctx) == 1
}

func (cd *cabacDecoder) decodeIntraPredMode(mpm int) int {
	if cd.decodeDecision(68) == 1 {
		return mpm
	}
	mode := cd.decodeDecision(69)
	mode += 2 * cd.decodeDecision(69)
	mode += 4 * cd.decodeDecision(69)
	if mode >= mpm {
		mode++
	}
	return mode
}

func (d *Decoder) decodeCabacChromaPredMode(cd *cabacDecoder, mbx, mby int) int {
	ctx := 0
	if d.mbAvail(mbx-1, mby) && d.mbInfo[mby*d.mbw+mbx-1].chromaPredMode != 0 {
		ctx++
	}
	if d.mbAvail(mbx, mby-1) && d.mbInfo[(mby-1)*d.mbw+mbx].chromaPredMode != 0 {
		ctx++
	}
	if cd.decodeDecision(64+ctx) == 0 {
		return 0
	}
	if cd.decodeDecision(64+3) == 0 {
		return 1
	}
	if cd.decodeDecision(64+3) == 0 {
		return 2
	}
	return 3
}

func (d *Decoder) decodeCabacCBPLuma(cd *cabacDecoder, mbx, mby int, curIntra bool) int {
	cbpA := int(d.neighborCbpCabac(mbx-1, mby, curIntra))
	cbpB := int(d.neighborCbpCabac(mbx, mby-1, curIntra))
	cbp := 0
	ctx := b2i(cbpA&0x02 == 0) + 2*b2i(cbpB&0x04 == 0)
	cbp |= cd.decodeDecision(73 + ctx)
	ctx = b2i(cbp&0x01 == 0) + 2*b2i(cbpB&0x08 == 0)
	cbp |= cd.decodeDecision(73+ctx) << 1
	ctx = b2i(cbpA&0x08 == 0) + 2*b2i(cbp&0x01 == 0)
	cbp |= cd.decodeDecision(73+ctx) << 2
	ctx = b2i(cbp&0x04 == 0) + 2*b2i(cbp&0x02 == 0)
	cbp |= cd.decodeDecision(73+ctx) << 3
	return cbp
}

func (d *Decoder) decodeCabacCBPChroma(cd *cabacDecoder, mbx, mby int, curIntra bool) int {
	cbpA := (int(d.neighborCbpCabac(mbx-1, mby, curIntra)) >> 4) & 3
	cbpB := (int(d.neighborCbpCabac(mbx, mby-1, curIntra)) >> 4) & 3
	ctx := 0
	if cbpA > 0 {
		ctx++
	}
	if cbpB > 0 {
		ctx += 2
	}
	if cd.decodeDecision(77+ctx) == 0 {
		return 0
	}
	ctx = 4
	if cbpA == 2 {
		ctx++
	}
	if cbpB == 2 {
		ctx += 2
	}
	return 1 + cd.decodeDecision(77+ctx)
}

// decodeCabacMBQPDelta decodes mb_qp_delta (spec 9.3.2.7, ctx 60-63).
func (d *Decoder) decodeCabacMBQPDelta(cd *cabacDecoder) int {
	if cd.decodeDecision(60+b2i(d.lastQPDeltaNonZero)) == 0 {
		d.lastQPDeltaNonZero = false
		return 0
	}
	val := 1
	ctx := 62
	for cd.decodeDecision(ctx) == 1 {
		ctx = 63
		val++
		if val > 104 {
			break
		}
	}
	d.lastQPDeltaNonZero = true
	if val&1 == 1 {
		return (val + 1) >> 1
	}
	return -((val + 1) >> 1)
}

// decodeCabacTransformSize decodes transform_size_8x8_flag (ctx 399-401).
func (d *Decoder) decodeCabacTransformSize(cd *cabacDecoder, mbx, mby int) bool {
	inc := 0
	if d.mbAvail(mbx-1, mby) && d.mbInfo[mby*d.mbw+mbx-1].transform8x8 {
		inc++
	}
	if d.mbAvail(mbx, mby-1) && d.mbInfo[(mby-1)*d.mbw+mbx].transform8x8 {
		inc++
	}
	return cd.decodeDecision(399+inc) == 1
}

func (cd *cabacDecoder) decodeCabacPMBType() int {
	if cd.decodeDecision(14) == 0 {
		if cd.decodeDecision(15) == 0 {
			// P_L0_16x16 or P_8x8.
			return 3 * cd.decodeDecision(16)
		}
		// P_L0_8x16 or P_L0_16x8.
		return 2 - cd.decodeDecision(17)
	}
	return -1 // intra suffix follows
}

func (cd *cabacDecoder) decodeCabacPSubMBType() int {
	if cd.decodeDecision(21) == 1 {
		return subMBType8x8
	}
	if cd.decodeDecision(22) == 0 {
		return subMBType8x4
	}
	if cd.decodeDecision(23) == 1 {
		return subMBType4x8
	}
	return subMBType4x4
}

// refNeighborForCtx returns the reference index of the 4x4 cell left of
// (dx=-1) or above (dy=-1) cell n, for the ref_idx context. Cells within the
// current MB read the partially-filled info.refIdx; cells in neighbor MBs
// read their stored per-partition refIdx (-1 for intra, 0 for skip).
// Unavailable neighbors return -1.
func (d *Decoder) refNeighborForCtx(mbx, mby, n, dx, dy int) int {
	cx, cy := n%4+dx, n/4+dy
	nx, ny := mbx, mby
	if cx < 0 {
		nx--
		cx += 4
	}
	if cy < 0 {
		ny--
		cy += 4
	}
	if !d.mbAvail(nx, ny) {
		return -1
	}
	info := &d.mbInfo[ny*d.mbw+nx]
	if nx != mbx || ny != mby {
		if info.isIntra {
			return -1
		}
	}
	part := (cy/2)*2 + cx/2
	return info.refIdx[part]
}

func (d *Decoder) decodeCabacRefIdx(cd *cabacDecoder, mbx, mby, n int) int {
	inc := 0
	if d.refNeighborForCtx(mbx, mby, n, -1, 0) > 0 {
		inc++
	}
	if d.refNeighborForCtx(mbx, mby, n, 0, -1) > 0 {
		inc += 2
	}
	ref := 0
	ctx := inc
	for cd.decodeDecision(54+ctx) == 1 {
		ref++
		ctx = (ctx >> 2) + 4
		if ref >= 32 {
			return -1
		}
	}
	return ref
}

// mvdNeighborAbs returns the stored |mvd| component of the cell left/above
// cell n (0 when unavailable, intra, or skip — those MBs store zeros).
func (d *Decoder) mvdNeighborAbs(mbx, mby, n, dx, dy, comp int) int {
	cx, cy := n%4+dx, n/4+dy
	nx, ny := mbx, mby
	if cx < 0 {
		nx--
		cx += 4
	}
	if cy < 0 {
		ny--
		cy += 4
	}
	if !d.mbAvail(nx, ny) {
		return 0
	}
	return int(d.mbInfo[ny*d.mbw+nx].mvdAbs[cy*4+cx][comp])
}

// decodeCabacMVDComp decodes one mvd component (ctxBase 40 for x, 47 for y).
// Returns the signed mvd and its |mvd| capped at 70 for the context store.
func (cd *cabacDecoder) decodeCabacMVDComp(ctxBase, amvd int) (int, int) {
	inc := 0
	if amvd > 2 {
		inc++
	}
	if amvd > 32 {
		inc++
	}
	if cd.decodeDecision(ctxBase+inc) == 0 {
		return 0, 0
	}
	mvd := 1
	ctx := ctxBase + 3
	for mvd < 9 && cd.decodeDecision(ctx) == 1 {
		if mvd < 4 {
			ctx++
		}
		mvd++
	}
	capped := mvd
	if mvd >= 9 {
		k := 3
		for cd.decodeBypass() == 1 {
			mvd += 1 << uint(k)
			k++
			if k > 24 {
				break
			}
		}
		for k--; k >= 0; k-- {
			mvd += cd.decodeBypass() << uint(k)
		}
		capped = mvd
		if capped > 70 {
			capped = 70
		}
	}
	return cd.decodeBypassSign(mvd), capped
}

// decodeCabacMVD decodes an mvd pair for cell n, storing capped |mvd| values
// into the cells listed in fillCells for later contexts.
func (d *Decoder) decodeCabacMVD(cd *cabacDecoder, mbx, mby, n int) (int, int, [2]uint8) {
	amvdX := d.mvdNeighborAbs(mbx, mby, n, -1, 0, 0) + d.mvdNeighborAbs(mbx, mby, n, 0, -1, 0)
	amvdY := d.mvdNeighborAbs(mbx, mby, n, -1, 0, 1) + d.mvdNeighborAbs(mbx, mby, n, 0, -1, 1)
	mvdX, capX := cd.decodeCabacMVDComp(40, amvdX)
	mvdY, capY := cd.decodeCabacMVDComp(47, amvdY)
	return mvdX, mvdY, [2]uint8{uint8(capX), uint8(capY)}
}

// --- Slice decoding --------------------------------------------------------

func (d *Decoder) newSliceCABAC(br *BitReader, sh *sliceHeader) *cabacDecoder {
	cd := &cabacDecoder{br: br}
	cd.initContexts(sh.sliceType, int(sh.cabacInitIdc), d.qp)
	br.ByteAlign() // cabac_alignment_one_bit
	cd.initEngine()
	d.lastQPDeltaNonZero = false
	return cd
}

func (d *Decoder) decodeISliceCABAC(br *BitReader, sh *sliceHeader) (*image.YCbCr, error) {
	cd := d.newSliceCABAC(br, sh)
	totalMBs := d.mbw * d.mbh
	for mbIdx := 0; mbIdx < totalMBs; mbIdx++ {
		mbx := mbIdx % d.mbw
		mby := mbIdx / d.mbw
		mbType := d.decodeCabacIntraMBType(cd, mbx, mby, 3, true)
		if err := d.decodeMBIntraCABACWithType(cd, mbx, mby, mbType); err != nil {
			return nil, fmt.Errorf("MB(%d,%d): %w", mbx, mby, err)
		}
		if DebugCabacITrace != nil {
			DebugCabacITrace(mbx, mby, mbType, int(d.mbInfo[mby*d.mbw+mbx].cbpCabac), d.qp)
		}
		// Bookkeeping for MV prediction / deblocking, as in decodeISlice.
		idx := mby*d.mbw + mbx
		info := &d.mbInfo[idx]
		info.isIntra = true
		info.mbType = -2
		info.qp = d.pcmAwareQP(idx)
		for k := range info.mv {
			info.mv[k] = [2]int16{0, 0}
		}
		for k := range info.refIdx {
			info.refIdx[k] = -1
			info.refIdxL1[k] = -1
			info.predMask[k] = 0
		}
		if err := cd.checkErr(); err != nil {
			return nil, fmt.Errorf("MB(%d,%d): %w", mbx, mby, err)
		}
		if cd.decodeTerminate() == 1 {
			if mbIdx != totalMBs-1 {
				return nil, fmt.Errorf("early end_of_slice at MB %d/%d", mbIdx, totalMBs)
			}
			break
		}
	}
	d.deblockFrame(sh)
	return d.cropImg(), nil
}

// DebugCabacPTrace, if non-nil, receives (mbx, mby, skip, pType, cbp) for
// every macroblock decoded in a CABAC P slice. pType is -1 for intra MBs,
// -2 for skipped MBs; cbp is the packed cbpCabac after decoding.
var DebugCabacPTrace func(mbx, mby, pType, cbp int)

// DebugCabacITrace, if non-nil, receives (mbx, mby, mbType, cbp, qp) for
// every macroblock decoded in a CABAC I slice (after residual decoding).
var DebugCabacITrace func(mbx, mby, mbType, cbp, qp int)

// DebugCabacResidual, if non-nil, receives every decoded residual block:
// category, block index, and the scan-ordered coefficients.
var DebugCabacResidual func(mbx, mby, cat, blkIdx int, coeffs []int16)

func (d *Decoder) decodePSliceCABAC(br *BitReader, sh *sliceHeader) (*image.YCbCr, error) {
	if len(d.refFrames) == 0 {
		return nil, fmt.Errorf("P-slice: no reference frames available")
	}
	if err := d.buildRefLists(sh); err != nil {
		return nil, fmt.Errorf("P-slice: %w", err)
	}
	cd := d.newSliceCABAC(br, sh)
	totalMBs := d.mbw * d.mbh
	for mbIdx := 0; mbIdx < totalMBs; mbIdx++ {
		mbx := mbIdx % d.mbw
		mby := mbIdx / d.mbw
		if d.decodeCabacMBSkip(cd, mbx, mby) {
			d.decodeMBSkip(mbx, mby, sh)
			d.lastQPDeltaNonZero = false
			if DebugCabacPTrace != nil {
				DebugCabacPTrace(mbx, mby, -2, 0)
			}
		} else {
			pType := cd.decodeCabacPMBType()
			var err error
			if pType < 0 {
				mbType := d.decodeCabacIntraMBType(cd, mbx, mby, 17, false)
				err = d.decodeMBIntraCABACWithType(cd, mbx, mby, mbType)
				if err == nil {
					idx := mby*d.mbw + mbx
					info := &d.mbInfo[idx]
					info.isIntra = true
					info.mbType = -2
					info.qp = d.pcmAwareQP(idx)
					for k := range info.mv {
						info.mv[k] = [2]int16{0, 0}
					}
					for k := range info.refIdx {
						info.refIdx[k] = -1
						info.refIdxL1[k] = -1
						info.predMask[k] = 0
					}
					info.hasCoef = true
				}
			} else {
				err = d.decodeMBInterCABAC(cd, mbx, mby, sh, pType)
			}
			if err != nil {
				return nil, fmt.Errorf("MB(%d,%d): %w", mbx, mby, err)
			}
			if DebugCabacPTrace != nil {
				DebugCabacPTrace(mbx, mby, pType, int(d.mbInfo[mby*d.mbw+mbx].cbpCabac))
			}
		}
		if err := cd.checkErr(); err != nil {
			return nil, fmt.Errorf("MB(%d,%d): %w", mbx, mby, err)
		}
		if cd.decodeTerminate() == 1 {
			if mbIdx != totalMBs-1 {
				return nil, fmt.Errorf("early end_of_slice at MB %d/%d", mbIdx, totalMBs)
			}
			break
		}
	}
	return d.cropImg(), nil
}

// --- Intra macroblock decoding ---------------------------------------------

func (d *Decoder) decodeMBIntraCABACWithType(cd *cabacDecoder, mbx, mby, mbType int) error {
	d.curMB = [2]int{mbx, mby}
	d.prepareYBR(mbx, mby)
	idx := mby*d.mbw + mbx
	info := &d.mbInfo[idx]
	info.transform8x8 = false
	info.cbpCabac = 0
	info.chromaPredMode = 0
	info.i16OrPCM = mbType != 0
	info.isPCM = false
	info.mvdAbs = [16][2]uint8{}
	info.mvdAbsL1 = [16][2]uint8{}
	info.isDirectMB = false
	info.directMask = 0
	for i := range d.coeff {
		d.coeff[i] = 0
	}
	for i := range d.nzCoeffCur {
		d.nzCoeffCur[i] = 0
	}
	for i := range d.intraModeCur {
		d.intraModeCur[i] = -1
	}

	if mbType == mbTypeIPCM {
		return d.decodeMBPCMCABAC(cd, mbx, mby)
	}
	if mbType == mbTypeINxN {
		use8x8 := false
		if d.activePPS != nil && d.activePPS.Transform8x8Mode {
			use8x8 = d.decodeCabacTransformSize(cd, mbx, mby)
		}
		if use8x8 {
			info.transform8x8 = true
			return d.decodeMBI8x8CABAC(cd, mbx, mby)
		}
		return d.decodeMBI4x4CABAC(cd, mbx, mby)
	}
	return d.decodeMBI16x16CABAC(cd, mbx, mby, mbType)
}

// decodeMBPCMCABAC decodes an I_PCM macroblock inside a CABAC slice. Because
// the arithmetic engine here is the spec's bit-serial model (init reads 9
// bits, each renormalization reads one), the BitReader position after the
// I_PCM terminate bin is exactly the conceptual RBSP position: the sample
// data starts at the next byte boundary (pcm_alignment_zero_bit, spec 7.3.5)
// with no pointer adjustment, and the decoding engine is re-initialized after
// the samples (spec 9.3.1.2). Context variables are NOT re-initialized.
func (d *Decoder) decodeMBPCMCABAC(cd *cabacDecoder, mbx, mby int) error {
	if err := cd.checkErr(); err != nil {
		return err
	}
	cd.br.ByteAlign()
	if err := d.readPCMSamples(cd.br); err != nil {
		return err
	}
	d.finishPCM(mbx, mby)
	// I_PCM has no mb_qp_delta; the dqp context resets (FFmpeg
	// last_qscale_diff = 0).
	d.lastQPDeltaNonZero = false
	cd.initEngine()
	return cd.checkErr()
}

func (d *Decoder) decodeMBI4x4CABAC(cd *cabacDecoder, mbx, mby int) error {
	idx := mby*d.mbw + mbx
	info := &d.mbInfo[idx]

	var predModes [16]int
	for blk := 0; blk < 16; blk++ {
		mpm := d.mostProbableMode(mbx, mby, blk)
		mode := cd.decodeIntraPredMode(mpm)
		predModes[blk] = mode
		d.intraModeCur[blk] = mode
	}

	chromaPredMode := d.decodeCabacChromaPredMode(cd, mbx, mby)
	info.chromaPredMode = uint8(chromaPredMode)

	cbpLuma := d.decodeCabacCBPLuma(cd, mbx, mby, true)
	cbpChroma := d.decodeCabacCBPChroma(cd, mbx, mby, true)
	info.cbpCabac |= uint16(cbpLuma) | uint16(cbpChroma)<<4

	if cbpLuma != 0 || cbpChroma != 0 {
		d.qp = (d.qp + d.decodeCabacMBQPDelta(cd) + 52) % 52
	} else {
		d.lastQPDeltaNonZero = false
	}

	// Luma: predict + residual per 4x4 block in scan order.
	for blk := 0; blk < 16; blk++ {
		pos := blk4x4Pos[blk]
		y := ybrYY + pos[0]
		x := ybrYX + pos[1]
		d.predIntra4x4(y, x, predModes[blk], blk)

		if cbpLuma&(1<<uint(blkTo8x8[blk])) != 0 {
			c := d.coeff[blk*16 : blk*16+16]
			nz := d.cabacResidual(cd, catLuma4x4, blk, c, 16, mbx, mby, true)
			if nz > 0 {
				reorderCoeffs(c)
				dequant4x4(c, d.qp, d.wsLuma4(true))
				idct4x4(c)
				d.addResidual4x4(y, x, c)
			}
		}
	}

	if err := d.decodeChromaCABAC(cd, mbx, mby, chromaPredMode, cbpChroma, true, true); err != nil {
		return err
	}
	d.storeIntraModes(mbx, mby)
	d.storeNZCoeff(mbx, mby)
	d.copyMBToImg(mbx, mby)
	return nil
}

func (d *Decoder) decodeMBI8x8CABAC(cd *cabacDecoder, mbx, mby int) error {
	idx := mby*d.mbw + mbx
	info := &d.mbInfo[idx]

	var predModes [4]int
	for p := 0; p < 4; p++ {
		scanIdx := p * 4
		mpm := d.mostProbableMode(mbx, mby, scanIdx)
		mode := cd.decodeIntraPredMode(mpm)
		predModes[p] = mode
		for k := 0; k < 4; k++ {
			d.intraModeCur[p*4+k] = mode
		}
	}

	chromaPredMode := d.decodeCabacChromaPredMode(cd, mbx, mby)
	info.chromaPredMode = uint8(chromaPredMode)

	cbpLuma := d.decodeCabacCBPLuma(cd, mbx, mby, true)
	cbpChroma := d.decodeCabacCBPChroma(cd, mbx, mby, true)
	info.cbpCabac |= uint16(cbpLuma) | uint16(cbpChroma)<<4

	if cbpLuma != 0 || cbpChroma != 0 {
		d.qp = (d.qp + d.decodeCabacMBQPDelta(cd) + 52) % 52
	} else {
		d.lastQPDeltaNonZero = false
	}

	var zz [64]int16
	var blk [64]int16
	for p := 0; p < 4; p++ {
		partY := (p / 2) * 8
		partX := (p % 2) * 8
		y := ybrYY + partY
		x := ybrYX + partX
		d.predIntra8x8Part(y, x, p, predModes[p], mbx, mby)

		if cbpLuma&(1<<uint(p)) != 0 {
			for i := range zz {
				zz[i] = 0
			}
			nz := d.cabacResidual(cd, catLuma8x8, p, zz[:], 64, mbx, mby, true)
			if nz > 0 {
				for i := range blk {
					blk[i] = 0
				}
				for i, v := range zz {
					if v != 0 {
						blk[zigzagToRaster8x8[i]] = v
					}
				}
				dequant8x8(blk[:], d.qp, d.wsLuma8(true))
				idct8x8(blk[:])
				d.addResidual8x8(y, x, blk[:])
			}
		}
	}

	if err := d.decodeChromaCABAC(cd, mbx, mby, chromaPredMode, cbpChroma, true, true); err != nil {
		return err
	}
	d.storeIntraModes(mbx, mby)
	d.storeNZCoeff(mbx, mby)
	d.copyMBToImg(mbx, mby)
	return nil
}

func (d *Decoder) decodeMBI16x16CABAC(cd *cabacDecoder, mbx, mby, mbType int) error {
	idx := mby*d.mbw + mbx
	info := &d.mbInfo[idx]

	predMode, cbpChroma, cbpLuma := i16x16Params(mbType)
	info.cbpCabac |= uint16(cbpLuma) | uint16(cbpChroma)<<4

	chromaPredMode := d.decodeCabacChromaPredMode(cd, mbx, mby)
	info.chromaPredMode = uint8(chromaPredMode)

	d.qp = (d.qp + d.decodeCabacMBQPDelta(cd) + 52) % 52

	predIntra16x16Func[predMode](d, ybrYY, ybrYX)

	// Luma DC (cat 0): decoded in zigzag scan order like the CAVLC path.
	dcCoeffs := make([]int16, 16)
	d.cabacResidual(cd, catLumaDC, 0, dcCoeffs, 16, mbx, mby, true)
	reorderCoeffs(dcCoeffs)
	hadamard4x4(dcCoeffs)
	dequantLumaDC(dcCoeffs, d.qp, d.scalingWS4[0][0])

	for blk := 0; blk < 16; blk++ {
		dcIdx := blkScanToDCIdx[blk]
		c := d.coeff[blk*16 : blk*16+16]
		c[0] = dcCoeffs[dcIdx]

		if cbpLuma != 0 {
			nz := d.cabacResidual(cd, catLumaAC, blk, c[1:16], 15, mbx, mby, true)
			if nz > 0 {
				reorderCoeffs(c)
			}
		}
		if c[0] != 0 || d.nzCoeffCur[blk] > 0 {
			dequant4x4(c, d.qp, d.wsLuma4(true))
			c[0] = dcCoeffs[dcIdx]
			idct4x4(c)
			pos := blk4x4Pos[blk]
			d.addResidual4x4(ybrYY+pos[0], ybrYX+pos[1], c)
		}
	}

	if err := d.decodeChromaCABAC(cd, mbx, mby, chromaPredMode, cbpChroma, true, true); err != nil {
		return err
	}
	d.storeIntraModes(mbx, mby)
	d.storeNZCoeff(mbx, mby)
	d.copyMBToImg(mbx, mby)
	return nil
}

// decodeChromaCABAC parses and reconstructs the chroma residual. When
// predict is true the chroma intra prediction runs first (intra MBs).
func (d *Decoder) decodeChromaCABAC(cd *cabacDecoder, mbx, mby, chromaPredMode, cbpChroma int, curIntra, predict bool) error {
	qpC := chromaQP(d.qp, int(d.activePPS.ChromaQPIndexOffset))

	if predict {
		predIntraChromaFunc[chromaPredMode](d, ybrBY, ybrBX)
		predIntraChromaFunc[chromaPredMode](d, ybrRY, ybrRX)
	}
	if cbpChroma == 0 {
		return nil
	}

	// Chroma DC (cat 3): 2x2 blocks, identity scan.
	cbDC := make([]int16, 4)
	crDC := make([]int16, 4)
	d.cabacResidual(cd, catChromaDC, 0, cbDC, 4, mbx, mby, curIntra)
	d.cabacResidual(cd, catChromaDC, 1, crDC, 4, mbx, mby, curIntra)

	cbW, crW := d.scalingWS4[4][0], d.scalingWS4[5][0]
	if curIntra {
		cbW, crW = d.scalingWS4[1][0], d.scalingWS4[2][0]
	}
	hadamard2x2(cbDC)
	dequantChromaDC(cbDC, qpC, cbW)
	hadamard2x2(crDC)
	dequantChromaDC(crDC, qpC, crW)

	for plane := 0; plane < 2; plane++ {
		dc := cbDC
		yOff, xOff := ybrBY, ybrBX
		if plane == 1 {
			dc = crDC
			yOff, xOff = ybrRY, ybrRX
		}
		for blk := 0; blk < 4; blk++ {
			base := 16*16 + plane*4*16 + blk*16
			c := d.coeff[base : base+16]
			c[0] = dc[blk]

			if cbpChroma == 2 {
				nz := d.cabacResidual(cd, catChromaAC, plane*4+blk, c[1:16], 15, mbx, mby, curIntra)
				if nz > 0 {
					reorderCoeffs(c)
				}
			}
			if c[0] != 0 || d.nzCoeffCur[16+plane*4+blk] > 0 {
				dequant4x4(c, qpC, d.wsChroma4(curIntra, plane == 1))
				c[0] = dc[blk]
				idct4x4(c)
				j4 := blk / 2
				i4 := blk % 2
				d.addResidual4x4(yOff+j4*4, xOff+i4*4, c)
			}
		}
	}
	return nil
}

// --- Inter macroblock decoding ---------------------------------------------

func (d *Decoder) decodeMBInterCABAC(cd *cabacDecoder, mbx, mby int, sh *sliceHeader, mbType int) error {
	idx := mby*d.mbw + mbx
	info := &d.mbInfo[idx]
	d.curMB = [2]int{mbx, mby}
	d.prepareYBR(mbx, mby)
	info.isIntra = false
	info.mbType = mbType
	info.decodedMask = 0
	info.cbpCabac = 0
	info.chromaPredMode = 0
	info.i16OrPCM = false
	info.isPCM = false
	info.transform8x8 = false
	info.mvdAbs = [16][2]uint8{}
	info.isDirectMB = false
	info.directMask = 0
	for k := 0; k < 4; k++ {
		info.predMask[k] = 1
		info.refIdxL1[k] = -1
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

	numRefL0 := int(sh.numRefIdxL0Active)
	allSub8x8 := true

	switch mbType {
	case pMBTypeL0_16x16:
		refIdx := 0
		if numRefL0 > 1 {
			refIdx = d.decodeCabacRefIdx(cd, mbx, mby, 0)
			if refIdx < 0 || refIdx >= numRefL0 {
				return fmt.Errorf("invalid ref_idx %d", refIdx)
			}
		}
		for k := 0; k < 4; k++ {
			info.refIdx[k] = refIdx
		}
		mvdX, mvdY, capd := d.decodeCabacMVD(cd, mbx, mby, 0)
		mvp := d.predictMVWithSize(mbx, mby, 0, 0, 16, 16, refIdx)
		mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}

		ref := d.getRefFrame(refIdx)
		if ref == nil {
			return fmt.Errorf("reference frame %d not found", refIdx)
		}
		d.motionCompLuma(ref, mbx, mby, mv, ybrYY, ybrYX, 16, 16)
		d.motionCompChroma(ref, mbx, mby, mv)
		d.applyWeights(sh, refIdx, ybrYY, ybrYX, 16, 16, 0, 0)

		for k := 0; k < 16; k++ {
			info.mv[k] = mv
			info.mvdAbs[k] = capd
		}
		info.decodedMask = 0xFFFF

	case pMBTypeL0_L0_16x8, pMBTypeL0_L0_8x16:
		is16x8 := mbType == pMBTypeL0_L0_16x8
		var refs [2]int
		for p := 0; p < 2; p++ {
			if numRefL0 > 1 {
				n := p * 8
				if !is16x8 {
					n = p * 2
				}
				r := d.decodeCabacRefIdx(cd, mbx, mby, n)
				if r < 0 || r >= numRefL0 {
					return fmt.Errorf("invalid ref_idx %d", r)
				}
				refs[p] = r
			}
			// Publish this partition's refIdx for the next ref context.
			if is16x8 {
				info.refIdx[p*2] = refs[p]
				info.refIdx[p*2+1] = refs[p]
			} else {
				info.refIdx[p] = refs[p]
				info.refIdx[p+2] = refs[p]
			}
		}
		for p := 0; p < 2; p++ {
			ref := d.getRefFrame(refs[p])
			if ref == nil {
				return fmt.Errorf("reference frame %d not found", refs[p])
			}
			if is16x8 {
				partY := p * 8
				n := p * 8
				mvdX, mvdY, capd := d.decodeCabacMVD(cd, mbx, mby, n)
				mvp := d.predictMVWithSize(mbx, mby, 0, partY, 16, 8, refs[p])
				mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}
				d.motionCompLuma(ref, mbx, mby, mv, ybrYY+partY, ybrYX, 16, 8)
				d.motionCompChromaBlock(ref, mbx, mby, mv, 0, p*4, 8, 4)
				d.applyWeights(sh, refs[p], ybrYY+partY, ybrYX, 16, 8, 0, p*4)
				for k := p * 8; k < p*8+8; k++ {
					info.mv[k] = mv
					info.mvdAbs[k] = capd
					info.decodedMask |= uint16(1) << uint(k)
				}
			} else {
				partX := p * 8
				n := p * 2
				mvdX, mvdY, capd := d.decodeCabacMVD(cd, mbx, mby, n)
				mvp := d.predictMVWithSize(mbx, mby, partX, 0, 8, 16, refs[p])
				mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}
				d.motionCompLuma(ref, mbx, mby, mv, ybrYY, ybrYX+partX, 8, 16)
				d.motionCompChromaBlock(ref, mbx, mby, mv, p*4, 0, 4, 8)
				d.applyWeights(sh, refs[p], ybrYY, ybrYX+partX, 8, 16, p*4, 0)
				for by := 0; by < 4; by++ {
					for bx := p * 2; bx < p*2+2; bx++ {
						k := by*4 + bx
						info.mv[k] = mv
						info.mvdAbs[k] = capd
						info.decodedMask |= uint16(1) << uint(k)
					}
				}
			}
		}

	case pMBType8x8:
		var subTypes [4]int
		for p := 0; p < 4; p++ {
			subTypes[p] = cd.decodeCabacPSubMBType()
			info.subMBType[p] = subTypes[p]
			if subTypes[p] != subMBType8x8 {
				allSub8x8 = false
			}
		}
		var refs [4]int
		for p := 0; p < 4; p++ {
			if numRefL0 > 1 {
				// Top-left raster cell of partition p for the neighbor context.
				n := (part8x8Pos[p][1]/4)*4 + part8x8Pos[p][0]/4
				r := d.decodeCabacRefIdx(cd, mbx, mby, n)
				if r < 0 || r >= numRefL0 {
					return fmt.Errorf("invalid ref_idx %d", r)
				}
				refs[p] = r
			}
			info.refIdx[p] = refs[p]
		}
		for p := 0; p < 4; p++ {
			px := part8x8Pos[p][0]
			py := part8x8Pos[p][1]
			ref := d.getRefFrame(refs[p])
			if ref == nil {
				return fmt.Errorf("reference frame %d not found", refs[p])
			}
			switch subTypes[p] {
			case subMBType8x8:
				n := (py/4)*4 + px/4
				mvdX, mvdY, capd := d.decodeCabacMVD(cd, mbx, mby, n)
				mvp := d.predictMVWithSize(mbx, mby, px, py, 8, 8, refs[p])
				mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}
				d.motionCompLuma(ref, mbx, mby, mv, ybrYY+py, ybrYX+px, 8, 8)
				d.motionCompChromaBlock(ref, mbx, mby, mv, px/2, py/2, 4, 4)
				d.applyWeights(sh, refs[p], ybrYY+py, ybrYX+px, 8, 8, px/2, py/2)
				for by := py / 4; by < py/4+2; by++ {
					for bx := px / 4; bx < px/4+2; bx++ {
						k := by*4 + bx
						info.mv[k] = mv
						info.mvdAbs[k] = capd
						info.decodedMask |= uint16(1) << uint(k)
					}
				}
			case subMBType8x4:
				for sp := 0; sp < 2; sp++ {
					spy := py + sp*4
					n := (spy/4)*4 + px/4
					mvdX, mvdY, capd := d.decodeCabacMVD(cd, mbx, mby, n)
					mvp := d.predictMVWithSize(mbx, mby, px, spy, 8, 4, refs[p])
					mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}
					d.motionCompLuma(ref, mbx, mby, mv, ybrYY+spy, ybrYX+px, 8, 4)
					d.motionCompChromaBlock(ref, mbx, mby, mv, px/2, spy/2, 4, 2)
					d.applyWeights(sh, refs[p], ybrYY+spy, ybrYX+px, 8, 4, px/2, spy/2)
					by := spy / 4
					for bx := px / 4; bx < px/4+2; bx++ {
						k := by*4 + bx
						info.mv[k] = mv
						info.mvdAbs[k] = capd
						info.decodedMask |= uint16(1) << uint(k)
					}
				}
			case subMBType4x8:
				for sp := 0; sp < 2; sp++ {
					spx := px + sp*4
					n := (py/4)*4 + spx/4
					mvdX, mvdY, capd := d.decodeCabacMVD(cd, mbx, mby, n)
					mvp := d.predictMVWithSize(mbx, mby, spx, py, 4, 8, refs[p])
					mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}
					d.motionCompLuma(ref, mbx, mby, mv, ybrYY+py, ybrYX+spx, 4, 8)
					d.motionCompChromaBlock(ref, mbx, mby, mv, spx/2, py/2, 2, 4)
					d.applyWeights(sh, refs[p], ybrYY+py, ybrYX+spx, 4, 8, spx/2, py/2)
					bx := spx / 4
					for by := py / 4; by < py/4+2; by++ {
						k := by*4 + bx
						info.mv[k] = mv
						info.mvdAbs[k] = capd
						info.decodedMask |= uint16(1) << uint(k)
					}
				}
			case subMBType4x4:
				for sp := 0; sp < 4; sp++ {
					spx := px + (sp%2)*4
					spy := py + (sp/2)*4
					n := (spy/4)*4 + spx/4
					mvdX, mvdY, capd := d.decodeCabacMVD(cd, mbx, mby, n)
					mvp := d.predictMVWithSize(mbx, mby, spx, spy, 4, 4, refs[p])
					mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}
					d.motionCompLuma(ref, mbx, mby, mv, ybrYY+spy, ybrYX+spx, 4, 4)
					d.motionCompChromaBlock(ref, mbx, mby, mv, spx/2, spy/2, 2, 2)
					d.applyWeights(sh, refs[p], ybrYY+spy, ybrYX+spx, 4, 4, spx/2, spy/2)
					k := (spy/4)*4 + spx/4
					info.mv[k] = mv
					info.mvdAbs[k] = capd
					info.decodedMask |= uint16(1) << uint(k)
				}
			}
		}
	default:
		return fmt.Errorf("unsupported CABAC P mb_type %d", mbType)
	}

	// CBP.
	cbpLuma := d.decodeCabacCBPLuma(cd, mbx, mby, false)
	cbpChroma := d.decodeCabacCBPChroma(cd, mbx, mby, false)
	info.cbpCabac |= uint16(cbpLuma) | uint16(cbpChroma)<<4

	use8x8Transform := false
	if d.activePPS.Transform8x8Mode && cbpLuma > 0 && allSub8x8 {
		use8x8Transform = d.decodeCabacTransformSize(cd, mbx, mby)
	}
	info.transform8x8 = use8x8Transform

	hasCoef, err := d.decodeInterResidualCABAC(cd, mbx, mby, use8x8Transform, cbpLuma, cbpChroma)
	if err != nil {
		return err
	}

	info.hasCoef = hasCoef
	info.qp = d.qp
	for k := 0; k < 4; k++ {
		info.refPicID[k] = d.refPicID(info.refIdx[k])
		info.refIdxL1[k] = -1
		info.refPicIDL1[k] = -1
		info.predMask[k] = 1
	}
	d.storeIntraModes(mbx, mby)
	d.storeNZCoeff(mbx, mby)
	d.copyMBToImg(mbx, mby)
	return nil
}

// --- B slices ---------------------------------------------------------------

// decodeCabacMBSkipB decodes mb_skip_flag in a B slice (ctx 24-26).
func (d *Decoder) decodeCabacMBSkipB(cd *cabacDecoder, mbx, mby int) bool {
	ctx := 0
	if d.mbAvail(mbx-1, mby) && d.mbInfo[mby*d.mbw+mbx-1].mbType != -1 {
		ctx++
	}
	if d.mbAvail(mbx, mby-1) && d.mbInfo[(mby-1)*d.mbw+mbx].mbType != -1 {
		ctx++
	}
	return cd.decodeDecision(24+ctx) == 1
}

// decodeCabacBMBType decodes a B mb_type (ctx 27-32, FFmpeg tree). Returns
// the CAVLC numbering 0..22, or -100-intraType for intra macroblocks.
func (d *Decoder) decodeCabacBMBType(cd *cabacDecoder, mbx, mby int) int {
	ctx := 0
	if d.mbAvail(mbx-1, mby) {
		n := &d.mbInfo[mby*d.mbw+mbx-1]
		if n.mbType != -1 && !n.isDirectMB {
			ctx++
		}
	}
	if d.mbAvail(mbx, mby-1) {
		n := &d.mbInfo[(mby-1)*d.mbw+mbx]
		if n.mbType != -1 && !n.isDirectMB {
			ctx++
		}
	}
	if cd.decodeDecision(27+ctx) == 0 {
		return 0 // B_Direct_16x16
	}
	if cd.decodeDecision(27+3) == 0 {
		return 1 + cd.decodeDecision(27+5) // B_L0_16x16 / B_L1_16x16
	}
	bits := cd.decodeDecision(27+4) << 3
	bits += cd.decodeDecision(27+5) << 2
	bits += cd.decodeDecision(27+5) << 1
	bits += cd.decodeDecision(27 + 5)
	switch {
	case bits < 8:
		return bits + 3
	case bits == 13:
		return -100 - d.decodeCabacIntraMBType(cd, mbx, mby, 32, false)
	case bits == 14:
		return 11 // B_L1_L0_8x16
	case bits == 15:
		return 22 // B_8x8
	}
	bits = bits<<1 + cd.decodeDecision(27+5)
	return bits - 4
}

func (cd *cabacDecoder) decodeCabacBSubMBType() int {
	if cd.decodeDecision(36) == 0 {
		return 0 // B_Direct_8x8
	}
	if cd.decodeDecision(37) == 0 {
		return 1 + cd.decodeDecision(39) // B_L0_8x8 / B_L1_8x8
	}
	t := 3
	if cd.decodeDecision(38) == 1 {
		if cd.decodeDecision(39) == 1 {
			return 11 + cd.decodeDecision(39) // B_L1_4x4 / B_Bi_4x4
		}
		t += 4
	}
	t += 2 * cd.decodeDecision(39)
	t += cd.decodeDecision(39)
	return t
}

// refNeighborForCtxB is refNeighborForCtx for a given list in a B slice:
// cells inside direct partitions do not count (FFmpeg direct_cache rule).
func (d *Decoder) refNeighborForCtxB(list, mbx, mby, n, dx, dy int) int {
	cx, cy := n%4+dx, n/4+dy
	nx, ny := mbx, mby
	if cx < 0 {
		nx--
		cx += 4
	}
	if cy < 0 {
		ny--
		cy += 4
	}
	if !d.mbAvail(nx, ny) {
		return -1
	}
	info := &d.mbInfo[ny*d.mbw+nx]
	if (nx != mbx || ny != mby) && info.isIntra {
		return -1
	}
	cell := cy*4 + cx
	if info.directMask&(uint16(1)<<uint(cell)) != 0 {
		return -1
	}
	part := (cy/2)*2 + cx/2
	if list == 1 {
		if info.predMask[part]&2 == 0 {
			return -1
		}
		return info.refIdxL1[part]
	}
	if info.predMask[part]&1 == 0 {
		return -1
	}
	return info.refIdx[part]
}

func (d *Decoder) decodeCabacRefIdxB(cd *cabacDecoder, list, mbx, mby, n int) int {
	inc := 0
	if d.refNeighborForCtxB(list, mbx, mby, n, -1, 0) > 0 {
		inc++
	}
	if d.refNeighborForCtxB(list, mbx, mby, n, 0, -1) > 0 {
		inc += 2
	}
	ref := 0
	ctx := inc
	for cd.decodeDecision(54+ctx) == 1 {
		ref++
		ctx = (ctx >> 2) + 4
		if ref >= 32 {
			return -1
		}
	}
	return ref
}

// mvdNeighborAbsB reads a stored |mvd| component for the given list.
func (d *Decoder) mvdNeighborAbsB(list, mbx, mby, n, dx, dy, comp int) int {
	cx, cy := n%4+dx, n/4+dy
	nx, ny := mbx, mby
	if cx < 0 {
		nx--
		cx += 4
	}
	if cy < 0 {
		ny--
		cy += 4
	}
	if !d.mbAvail(nx, ny) {
		return 0
	}
	if list == 1 {
		return int(d.mbInfo[ny*d.mbw+nx].mvdAbsL1[cy*4+cx][comp])
	}
	return int(d.mbInfo[ny*d.mbw+nx].mvdAbs[cy*4+cx][comp])
}

func (d *Decoder) decodeCabacMVDB(cd *cabacDecoder, list, mbx, mby, n int) (int, int, [2]uint8) {
	amvdX := d.mvdNeighborAbsB(list, mbx, mby, n, -1, 0, 0) + d.mvdNeighborAbsB(list, mbx, mby, n, 0, -1, 0)
	amvdY := d.mvdNeighborAbsB(list, mbx, mby, n, -1, 0, 1) + d.mvdNeighborAbsB(list, mbx, mby, n, 0, -1, 1)
	mvdX, capX := cd.decodeCabacMVDComp(40, amvdX)
	mvdY, capY := cd.decodeCabacMVDComp(47, amvdY)
	return mvdX, mvdY, [2]uint8{uint8(capX), uint8(capY)}
}

func (d *Decoder) decodeBSliceCABAC(br *BitReader, sh *sliceHeader) (*image.YCbCr, error) {
	if len(d.refFrames) == 0 {
		return nil, fmt.Errorf("B-slice: no reference frames available")
	}
	if err := d.buildRefLists(sh); err != nil {
		return nil, fmt.Errorf("B-slice: %w", err)
	}
	cd := d.newSliceCABAC(br, sh)
	totalMBs := d.mbw * d.mbh
	for mbIdx := 0; mbIdx < totalMBs; mbIdx++ {
		mbx := mbIdx % d.mbw
		mby := mbIdx / d.mbw
		if d.decodeCabacMBSkipB(cd, mbx, mby) {
			if err := d.decodeMBDirect(sh, mbx, mby, true); err != nil {
				return nil, fmt.Errorf("MB(%d,%d) B_Skip: %w", mbx, mby, err)
			}
			d.lastQPDeltaNonZero = false
			if DebugCabacBTrace != nil {
				DebugCabacBTrace(mbx, mby, -1)
			}
		} else {
			mbType := d.decodeCabacBMBType(cd, mbx, mby)
			if DebugCabacBTrace != nil {
				DebugCabacBTrace(mbx, mby, mbType)
			}
			var err error
			if mbType <= -100 {
				intraType := -(mbType + 100)
				err = d.decodeMBIntraCABACWithType(cd, mbx, mby, intraType)
				if err == nil {
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
				}
			} else {
				err = d.decodeMBInterBCABAC(cd, mbx, mby, sh, mbType)
			}
			if err != nil {
				return nil, fmt.Errorf("MB(%d,%d): %w", mbx, mby, err)
			}
		}
		if err := cd.checkErr(); err != nil {
			return nil, fmt.Errorf("MB(%d,%d): %w", mbx, mby, err)
		}
		if cd.decodeTerminate() == 1 {
			if mbIdx != totalMBs-1 {
				return nil, fmt.Errorf("early end_of_slice at MB %d/%d", mbIdx, totalMBs)
			}
			break
		}
	}
	return d.cropImg(), nil
}

// decodeMBInterBCABAC decodes a non-intra, non-skip B macroblock with CABAC.
func (d *Decoder) decodeMBInterBCABAC(cd *cabacDecoder, mbx, mby int, sh *sliceHeader, mbType int) error {
	shape := bMBTypes[mbType]
	info := d.prepareBMB(mbx, mby, mbType)

	numRef := [2]int{int(sh.numRefIdxL0Active), int(sh.numRefIdxL1Active)}
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
		cellOf := func(g *bPart) int { return (g.y/4)*4 + g.x/4 }
		for list := 0; list < 2; list++ {
			for p := 0; p < n; p++ {
				if geo[p].mask&(1<<uint(list)) == 0 {
					continue
				}
				if numRef[list] > 1 {
					r := d.decodeCabacRefIdxB(cd, list, mbx, mby, cellOf(&geo[p]))
					if r < 0 || r >= numRef[list] {
						return fmt.Errorf("invalid ref_idx_l%d %d", list, r)
					}
					geo[p].ref[list] = r
				}
				d.publishBPartRefs(info, &geo[p], list)
			}
		}
		for list := 0; list < 2; list++ {
			for p := 0; p < n; p++ {
				if geo[p].mask&(1<<uint(list)) == 0 {
					continue
				}
				g := &geo[p]
				mvdX, mvdY, capd := d.decodeCabacMVDB(cd, list, mbx, mby, cellOf(g))
				mvp := d.predictMVList(list, mbx, mby, g.x, g.y, g.w, g.h, g.ref[list])
				g.mv[list] = [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}
				d.publishBPartMVs(info, g, list)
				for by := g.y / 4; by < (g.y+g.h)/4; by++ {
					for bx := g.x / 4; bx < (g.x+g.w)/4; bx++ {
						if list == 0 {
							info.mvdAbs[by*4+bx] = capd
						} else {
							info.mvdAbsL1[by*4+bx] = capd
						}
					}
				}
			}
		}
		for p := 0; p < n; p++ {
			if err := d.execBPart(sh, mbx, mby, &geo[p]); err != nil {
				return err
			}
			d.storeBPart(info, &geo[p])
		}
	} else {
		// B_8x8.
		var subShapes [4]bSubShape
		var subDirect [4]bool
		var dctx bDirectCtx
		haveDirectCtx := false
		for p := 0; p < 4; p++ {
			st := cd.decodeCabacBSubMBType()
			subShapes[p] = bSubTypes[st]
			subDirect[p] = subShapes[p].direct
			info.subMBType[p] = st
			if !subShapes[p].direct && (subShapes[p].w != 8 || subShapes[p].h != 8) {
				allSub8x8OrDirect = false
			}
		}
		// Direct sub-partitions derive their motion (and mark cells) before
		// any ref/mvd contexts consult them.
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
			}
		}
		var refs [4][2]int
		for list := 0; list < 2; list++ {
			for p := 0; p < 4; p++ {
				if subDirect[p] || subShapes[p].mask&(1<<uint(list)) == 0 {
					continue
				}
				if numRef[list] > 1 {
					cell := (part8x8Pos[p][1]/4)*4 + part8x8Pos[p][0]/4
					r := d.decodeCabacRefIdxB(cd, list, mbx, mby, cell)
					if r < 0 || r >= numRef[list] {
						return fmt.Errorf("invalid ref_idx_l%d %d", list, r)
					}
					refs[p][list] = r
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
		for list := 0; list < 2; list++ {
			for p := 0; p < 4; p++ {
				if subDirect[p] || subShapes[p].mask&(1<<uint(list)) == 0 {
					continue
				}
				s := subShapes[p]
				for sp := 0; sp < s.subParts; sp++ {
					sx := part8x8Pos[p][0] + (sp%(8/s.w))*s.w
					sy := part8x8Pos[p][1] + (sp/(8/s.w))*s.h
					cell := (sy/4)*4 + sx/4
					mvdX, mvdY, capd := d.decodeCabacMVDB(cd, list, mbx, mby, cell)
					info.decodedMask = bSubDecodedMask(&subShapes, p, sp)
					mvp := d.predictMVList(list, mbx, mby, sx, sy, s.w, s.h, refs[p][list])
					info.decodedMask = 0xFFFF
					mv := [2]int16{mvp[0] + int16(mvdX), mvp[1] + int16(mvdY)}
					for by := sy / 4; by < (sy+s.h)/4; by++ {
						for bx := sx / 4; bx < (sx+s.w)/4; bx++ {
							k := by*4 + bx
							if list == 0 {
								info.mv[k] = mv
								info.mvdAbs[k] = capd
							} else {
								info.mvL1[k] = mv
								info.mvdAbsL1[k] = capd
							}
						}
					}
				}
			}
		}
		for p := 0; p < 4; p++ {
			if subDirect[p] {
				continue
			}
			s := subShapes[p]
			for sp := 0; sp < s.subParts; sp++ {
				sx := part8x8Pos[p][0] + (sp%(8/s.w))*s.w
				sy := part8x8Pos[p][1] + (sp/(8/s.w))*s.h
				part := bPart{x: sx, y: sy, w: s.w, h: s.h, mask: s.mask, ref: refs[p]}
				k := (sy/4)*4 + sx/4
				part.mv[0] = info.mv[k]
				part.mv[1] = info.mvL1[k]
				if err := d.execBPart(sh, mbx, mby, &part); err != nil {
					return err
				}
				d.storeBPart(info, &part)
			}
		}
	}

	// CBP, transform size, residual.
	cbpLuma := d.decodeCabacCBPLuma(cd, mbx, mby, false)
	cbpChroma := d.decodeCabacCBPChroma(cd, mbx, mby, false)
	info.cbpCabac |= uint16(cbpLuma) | uint16(cbpChroma)<<4

	use8x8Transform := false
	if d.activePPS.Transform8x8Mode && cbpLuma > 0 {
		allowed := allSub8x8OrDirect
		if shape.direct || shape.parts == 4 {
			allowed = allowed && d.activeSPS.Direct8x8Inference
		}
		if allowed {
			use8x8Transform = d.decodeCabacTransformSize(cd, mbx, mby)
		}
	}
	info.transform8x8 = use8x8Transform

	hasCoef, err := d.decodeInterResidualCABAC(cd, mbx, mby, use8x8Transform, cbpLuma, cbpChroma)
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

// decodeInterResidualCABAC decodes the mb_qp_delta and residual of an inter
// (P or B) macroblock via CABAC, reconstructing in place.
func (d *Decoder) decodeInterResidualCABAC(cd *cabacDecoder, mbx, mby int, use8x8Transform bool, cbpLuma, cbpChroma int) (bool, error) {
	hasCoef := false
	if cbpLuma != 0 || cbpChroma != 0 {
		d.qp = (d.qp + d.decodeCabacMBQPDelta(cd) + 52) % 52

		if use8x8Transform {
			var zz [64]int16
			var blk [64]int16
			for p := 0; p < 4; p++ {
				if cbpLuma&(1<<uint(p)) == 0 {
					continue
				}
				for i := range zz {
					zz[i] = 0
				}
				nz := d.cabacResidual(cd, catLuma8x8, p, zz[:], 64, mbx, mby, false)
				if nz > 0 {
					hasCoef = true
					for i := range blk {
						blk[i] = 0
					}
					for i, v := range zz {
						if v != 0 {
							blk[zigzagToRaster8x8[i]] = v
						}
					}
					dequant8x8(blk[:], d.qp, d.wsLuma8(false))
					idct8x8(blk[:])
					partY := (p / 2) * 8
					partX := (p % 2) * 8
					d.addResidual8x8(ybrYY+partY, ybrYX+partX, blk[:])
				}
			}
		} else {
			for blk := 0; blk < 16; blk++ {
				if cbpLuma&(1<<uint(blkTo8x8[blk])) == 0 {
					continue
				}
				c := d.coeff[blk*16 : blk*16+16]
				nz := d.cabacResidual(cd, catLuma4x4, blk, c, 16, mbx, mby, false)
				if nz > 0 {
					hasCoef = true
					reorderCoeffs(c)
					dequant4x4(c, d.qp, d.wsLuma4(false))
					idct4x4(c)
					pos := blk4x4Pos[blk]
					d.addResidual4x4(ybrYY+pos[0], ybrYX+pos[1], c)
				}
			}
		}

		if cbpChroma > 0 {
			hasCoef = true
			if err := d.decodeChromaCABAC(cd, mbx, mby, 0, cbpChroma, false, false); err != nil {
				return false, err
			}
		}
	} else {
		d.lastQPDeltaNonZero = false
	}
	return hasCoef, nil
}

// DebugCabacBTrace, if non-nil, receives (mbx, mby, mbType) for each B MB:
// -1 skip, -2 intra, else the CAVLC B mb_type numbering.
var DebugCabacBTrace func(mbx, mby, mbType int)

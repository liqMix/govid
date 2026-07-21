package h264

import (
	"fmt"
	"image"
)

// Decoder is a stateful H.264 Baseline Profile decoder.
type Decoder struct {
	sps       map[uint32]*SPS
	pps       map[uint32]*PPS
	activeSPS *SPS
	activePPS *PPS

	img   *image.YCbCr
	mbw   int // macroblocks per row
	mbh   int // macroblocks per column
	qp    int // current QP
	curMB [2]int // current macroblock (x, y) for prediction availability

	// Per-MB state for CAVLC context: nzCoeff[mbIdx*24 + blkIdx].
	// blkIdx 0-15: luma (in scan order), 16-19: Cb, 20-23: Cr.
	nzCoeff    []int
	nzCoeffCur [24]int

	// Workspace for reconstruction.
	// Row 0: top border. Rows 1-16: luma. Row 17: chroma border. Rows 18-25: chroma.
	ybr [1 + 16 + 1 + 8][48]uint8

	// Coefficient workspace.
	coeff [16*16 + 2*8*8]int16

	// NAL length prefix size.
	lengthSize int

	// Reference frame storage (Phase 3).
	refFrames []*image.YCbCr

	// Per-MB inter prediction info for MV prediction and deblocking.
	mbInfo []mbInterInfo

	// Per-MB intra 4x4 prediction modes (scan order 0-15), -1 if not I_4x4.
	intraModes   []int
	intraModeCur [16]int

	// disableDeblock skips the deblocking filter (for testing).
	disableDeblock bool
}

// ybr workspace layout constants.
const (
	ybrYX = 16 // luma X offset
	ybrYY = 1  // luma Y offset
	ybrBX = 16 // Cb X offset
	ybrBY = 18 // Cb Y offset
	ybrRX = 32 // Cr X offset
	ybrRY = 18 // Cr Y offset
)

// 4x4 luma block scan order: maps block index (0-15) to (row, col) in pixels.
// H.264 uses a Z-scan within 8x8 blocks pattern:
//
//	 0  1  4  5
//	 2  3  6  7
//	 8  9  12 13
//	10 11 14 15
var blk4x4Pos = [16][2]int{
	{0, 0}, {0, 4}, {4, 0}, {4, 4},
	{0, 8}, {0, 12}, {4, 8}, {4, 12},
	{8, 0}, {8, 4}, {12, 0}, {12, 4},
	{8, 8}, {8, 12}, {12, 8}, {12, 12},
}

// Neighbor lookup: left neighbor block index within same MB, or -1 if at left edge.
var blkLeftIdx = [16]int{
	-1, 0, -1, 2, 1, 4, 3, 6, -1, 8, -1, 10, 9, 12, 11, 14,
}

// For blocks at left edge (blkLeftIdx == -1): block index in left MB.
var blkLeftMBIdx = [16]int{
	5, -1, 7, -1, -1, -1, -1, -1, 13, -1, 15, -1, -1, -1, -1, -1,
}

// Neighbor lookup: above neighbor block index within same MB, or -1 if at top edge.
var blkAboveIdx = [16]int{
	-1, -1, 0, 1, -1, -1, 4, 5, 2, 3, 8, 9, 6, 7, 12, 13,
}

// For blocks at top edge (blkAboveIdx == -1): block index in above MB.
var blkAboveMBIdx = [16]int{
	10, 11, -1, -1, 14, 15, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1,
}

// Maps block scan index (0-15) to DC raster index for I_16x16.
// DC raster index = (row/4)*4 + (col/4) in the 4x4 grid.
var blkScanToDCIdx = [16]int{
	0, 1, 4, 5, 2, 3, 6, 7, 8, 9, 12, 13, 10, 11, 14, 15,
}

// Maps 8x8 block index to luma CBP bit.
// Blocks 0-3 are in 8x8 block 0, blocks 4-7 in 8x8 block 1, etc.
var blkTo8x8 = [16]int{
	0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3,
}

// blk4x4UpperRightUnavail marks scan indices where the above-right 4x4 samples
// are not available for intra 4x4 prediction (spec Section 6.4.12.2 + block 15
// whose upper-right is outside the MB and in an undecoded neighbor).
var blk4x4UpperRightUnavail = [16]bool{
	false, false, false, true,  // scan 3: (4,4) upper-right not yet decoded
	false, false, false, true,  // scan 7: (4,12) upper-right outside MB
	false, false, false, true,  // scan 11: (12,4) upper-right not yet decoded
	false, true, false, true,   // scan 13: (8,12) outside MB; scan 15: (12,12) outside MB
}

// zigzagToRaster maps zigzag scan position to raster index for 4x4 blocks.
var zigzagToRaster = [16]int{0, 1, 4, 8, 5, 2, 3, 6, 9, 12, 13, 10, 7, 11, 14, 15}

// rasterToScan maps raster index to Z-scan (block scan) index for 4x4 blocks.
var rasterToScan = [16]int{0, 1, 4, 5, 2, 3, 6, 7, 8, 9, 12, 13, 10, 11, 14, 15}

// zigzagToRaster8x8 maps 8x8 zigzag scan position (0..63) to raster index
// (row*8 + col). Spec Figure 6-10. Matches FFmpeg's ff_zigzag_direct8x8.
var zigzagToRaster8x8 = [64]int{
	0, 1, 8, 16, 9, 2, 3, 10,
	17, 24, 32, 25, 18, 11, 4, 5,
	12, 19, 26, 33, 40, 48, 41, 34,
	27, 20, 13, 6, 7, 14, 21, 28,
	35, 42, 49, 56, 57, 50, 43, 36,
	29, 22, 15, 23, 30, 37, 44, 51,
	58, 59, 52, 45, 38, 31, 39, 46,
	53, 60, 61, 54, 47, 55, 62, 63,
}

// reorderCoeffs reorders 16 coefficients from zigzag scan order to raster order in-place.
func reorderCoeffs(coeffs []int16) {
	var tmp [16]int16
	copy(tmp[:], coeffs[:16])
	for i := 0; i < 16; i++ {
		coeffs[zigzagToRaster[i]] = tmp[i]
	}
}

// read8x8ResidualCAVLC reads the 64 coefficients of an 8x8 transform block
// via CAVLC. Per FFmpeg's scan8x8_cavlc, the 64 coefficients are read as
// 4 sub-blocks of 16 coefficients each where sub-block s covers the
// contiguous slice zigzag[16*s .. 16*s+15]. Sub-block s position k
// corresponds to zigzag position 16*s + k, mapped to raster via
// zigzagToRaster8x8.
//
// This is NOT the `4*k+s` interleave you'd naïvely assume from reading the
// spec — the CAVLC sub-block scan groups low-frequency coefficients together
// so the context-adaptive coeff_token tables exploit the typical DC-heavy
// low / AC-heavy high frequency separation.
//
// coeffs must be length >= 64 and will be written in raster order.
// Returns the total non-zero count across all sub-blocks.
func read8x8ResidualCAVLC(br *BitReader, coeffs []int16, nCs [4]int) (int, error) {
	for i := 0; i < 64; i++ {
		coeffs[i] = 0
	}
	var subBlock [16]int16
	totalNZ := 0
	for s := 0; s < 4; s++ {
		for i := 0; i < 16; i++ {
			subBlock[i] = 0
		}
		nz, err := readResidualBlock(br, subBlock[:], 16, nCs[s])
		if err != nil {
			return 0, err
		}
		totalNZ += nz
		// Scatter sub-block coefficients into the 8x8 block in raster order.
		// subBlock[k] carries zigzag position 16*s + k.
		for k := 0; k < 16; k++ {
			if subBlock[k] == 0 {
				continue
			}
			scanPos := 16*s + k
			coeffs[zigzagToRaster8x8[scanPos]] = subBlock[k]
		}
	}
	return totalNZ, nil
}

func NewDecoder() *Decoder {
	return &Decoder{
		sps:        make(map[uint32]*SPS),
		pps:        make(map[uint32]*PPS),
		lengthSize: 4,
	}
}

func (d *Decoder) SetLengthSize(n int) {
	d.lengthSize = n
}

// DecodePacket decodes a packet containing one or more NAL units.
func (d *Decoder) DecodePacket(data []byte) (*image.YCbCr, error) {
	nalus, err := ParseNALUnits(data, d.lengthSize)
	if err != nil {
		return nil, fmt.Errorf("parse NAL units: %w", err)
	}

	var result *image.YCbCr
	for _, nalu := range nalus {
		switch nalu.Type {
		case NALSPS:
			sps, err := ParseSPS(nalu.Data)
			if err != nil {
				return nil, fmt.Errorf("parse SPS: %w", err)
			}
			d.sps[sps.ID] = sps
		case NALPPS:
			pps, err := ParsePPS(nalu.Data)
			if err != nil {
				return nil, fmt.Errorf("parse PPS: %w", err)
			}
			d.pps[pps.ID] = pps
		case NALSliceIDR, NALSlice:
			img, err := d.decodeSlice(nalu)
			if err != nil {
				return nil, fmt.Errorf("decode slice: %w", err)
			}
			result = img
		}
	}
	return result, nil
}

func (d *Decoder) decodeSlice(nalu NALUnit) (*image.YCbCr, error) {
	br := NewBitReader(nalu.Data)

	// Peek at PPS ID.
	_, _ = br.ReadUE() // first_mb_in_slice
	_, _ = br.ReadUE() // slice_type
	ppsID, err := br.ReadUE()
	if err != nil {
		return nil, err
	}

	pps, ok := d.pps[ppsID]
	if !ok {
		return nil, fmt.Errorf("PPS %d not found", ppsID)
	}
	sps, ok := d.sps[pps.SPSID]
	if !ok {
		return nil, fmt.Errorf("SPS %d not found", pps.SPSID)
	}
	d.activeSPS = sps
	d.activePPS = pps

	if pps.EntropyCodingModeFlag {
		return nil, fmt.Errorf("h264: CABAC entropy coding not supported (only CAVLC)")
	}

	// Re-parse from beginning.
	br = NewBitReader(nalu.Data)
	sh, err := parseSliceHeader(br, sps, pps, nalu.Type, nalu.RefIDC)
	if err != nil {
		return nil, fmt.Errorf("slice header: %w", err)
	}

	d.mbw = int(sps.PicWidthInMBs)
	d.mbh = int(sps.PicHeightInMapUnits)
	if !sps.FrameMBSOnly {
		d.mbh *= 2
	}
	d.ensureImg()
	d.initDPB()
	d.qp = 26 + int(pps.PicInitQPMinus26) + int(sh.sliceQPDelta)

	if nalu.Type == NALSliceIDR {
		d.resetDPB()
	}

	if sh.sliceType == sliceTypeI || sh.sliceType == sliceTypeSI {
		img, err := d.decodeISlice(br, sh)
		if err != nil {
			return nil, err
		}
		d.storeRefFrame()
		return img, nil
	}
	if sh.sliceType == sliceTypeP {
		img, err := d.decodePSliceImpl(br, sh)
		if err != nil {
			return nil, err
		}
		d.deblockFrame(sh)
		d.storeRefFrame()
		return img, nil
	}
	return nil, fmt.Errorf("unsupported slice type %d", sh.sliceType)
}

func (d *Decoder) ensureImg() {
	w := d.mbw * 16
	h := d.mbh * 16
	if d.img != nil && d.img.Rect.Dx() == w && d.img.Rect.Dy() == h {
		return
	}
	d.img = image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	totalMBs := d.mbw * d.mbh
	d.nzCoeff = make([]int, totalMBs*24)
	d.mbInfo = make([]mbInterInfo, totalMBs)
	d.intraModes = make([]int, totalMBs*16)
	for i := range d.intraModes {
		d.intraModes[i] = -1
	}
}

// DebugMBBits, if non-nil, is called with (mbx, mby, startBit, endBit) for each MB.
var DebugMBBits func(mbx, mby, startBit, endBit int)

// DebugPSliceTrace, if non-nil, is called for every MB in a P-slice with full context.
// branch: "S" = skip (inside skipRun batch), "N" = non-skip.
// rawVal: skipRun UE value for "S", mbType UE value for "N".
var DebugPSliceTrace func(mbx, mby int, branch string, startBit, endBit int, rawVal uint32)

func (d *Decoder) decodeISlice(br *BitReader, sh *sliceHeader) (*image.YCbCr, error) {
	for mby := 0; mby < d.mbh; mby++ {
		for mbx := 0; mbx < d.mbw; mbx++ {
			startBit := br.BitsRead()
			if err := d.decodeMBIntra(br, mbx, mby); err != nil {
				return nil, fmt.Errorf("MB(%d,%d): %w", mbx, mby, err)
			}
			if DebugMBBits != nil {
				DebugMBBits(mbx, mby, startBit, br.BitsRead())
			}
			// Mark as intra in mbInfo for P-slice MV prediction context.
			idx := mby*d.mbw + mbx
			d.mbInfo[idx].isIntra = true
			d.mbInfo[idx].qp = d.qp
			for k := range d.mbInfo[idx].mv {
				d.mbInfo[idx].mv[k] = [2]int16{0, 0}
			}
			for k := range d.mbInfo[idx].refIdx {
				d.mbInfo[idx].refIdx[k] = -1
			}
		}
	}
	d.deblockFrame(sh)
	return d.cropImg(), nil
}

// DebugMBLog, if non-nil, receives debug info for each decoded intra MB.
var DebugMBLog func(mbx, mby, mbType, bitsBeforeMB int)

// DebugBlkLog, if non-nil, is called after reading residual for a 4x4 block.
// coeffs are post-CAVLC (zigzag order, pre-reorder), nz is non-zero count.
var DebugBlkLog func(mbx, mby, blk, nz int, coeffs []int16, predMode int, nC int)

// DebugI4x4Modes, if non-nil, is called with all 16 prediction modes after parsing.
var DebugI4x4Modes func(mbx, mby int, modes [16]int, cbpLuma, cbpChroma int)

func (d *Decoder) decodeMBIntra(br *BitReader, mbx, mby int) error {
	bitsBefore := br.BitsRead()
	mbType, err := br.ReadUE()
	if err != nil {
		return fmt.Errorf("mb_type: %w", err)
	}
	if DebugMBLog != nil {
		DebugMBLog(mbx, mby, int(mbType), bitsBefore)
	}
	return d.decodeMBIntraWithType(br, mbx, mby, int(mbType))
}

func (d *Decoder) decodeMBIntraWithType(br *BitReader, mbx, mby, mbType int) error {
	d.curMB = [2]int{mbx, mby}
	d.prepareYBR(mbx, mby)
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
		return d.decodeMBPCM(br, mbx, mby)
	}
	if mbType == mbTypeINxN {
		if d.activePPS != nil && d.activePPS.Transform8x8Mode {
			t8x8, err := br.ReadBool()
			if err != nil {
				return fmt.Errorf("transform_size_8x8_flag: %w", err)
			}
			if t8x8 {
				// decodeMBI8x8 exists but is disabled pending bit-exactness
				// debug; it shared a bit-desync with the inter 8x8 path that
				// was not resolved this session. Keep the hard-stop until
				// an isolated fixture proves both paths match FFmpeg.
				return fmt.Errorf("h264: 8x8 intra transform not supported")
			}
		}
		return d.decodeMBI4x4(br, mbx, mby)
	}
	return d.decodeMBI16x16(br, mbx, mby, mbType)
}

func (d *Decoder) decodeMBPCM(br *BitReader, mbx, mby int) error {
	br.ByteAlign()
	for j := 0; j < 16; j++ {
		for i := 0; i < 16; i++ {
			v, err := br.ReadBits(8)
			if err != nil {
				return err
			}
			d.ybr[ybrYY+j][ybrYX+i] = uint8(v)
		}
	}
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			v, err := br.ReadBits(8)
			if err != nil {
				return err
			}
			d.ybr[ybrBY+j][ybrBX+i] = uint8(v)
		}
	}
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			v, err := br.ReadBits(8)
			if err != nil {
				return err
			}
			d.ybr[ybrRY+j][ybrRX+i] = uint8(v)
		}
	}
	for i := range d.nzCoeffCur {
		d.nzCoeffCur[i] = 16
	}
	d.storeIntraModes(mbx, mby)
	d.storeNZCoeff(mbx, mby)
	d.copyMBToImg(mbx, mby)
	return nil
}

func (d *Decoder) decodeMBI4x4(br *BitReader, mbx, mby int) error {
	predModes := [16]int{}
	for blk := 0; blk < 16; blk++ {
		prevFlag, err := br.ReadBool()
		if err != nil {
			return err
		}
		if prevFlag {
			predModes[blk] = d.mostProbableMode(mbx, mby, blk)
		} else {
			rem, err := br.ReadBits(3)
			if err != nil {
				return err
			}
			mpm := d.mostProbableMode(mbx, mby, blk)
			if int(rem) < mpm {
				predModes[blk] = int(rem)
			} else {
				predModes[blk] = int(rem) + 1
			}
		}
		d.intraModeCur[blk] = predModes[blk]
	}

	chromaPredMode, err := br.ReadUE()
	if err != nil {
		return err
	}
	if chromaPredMode > 3 {
		return fmt.Errorf("invalid intra_chroma_pred_mode %d", chromaPredMode)
	}

	cbpCode, err := br.ReadUE()
	if err != nil {
		return err
	}
	if int(cbpCode) >= len(cbpTableIntra4x4) {
		return fmt.Errorf("invalid CBP code %d", cbpCode)
	}
	cbp := cbpTableIntra4x4[cbpCode]
	cbpLuma := cbp % 16
	cbpChroma := cbp / 16

	if cbp > 0 {
		qpDelta, err := br.ReadSE()
		if err != nil {
			return err
		}
		d.qp = (d.qp + int(qpDelta) + 52) % 52
	}

	if DebugI4x4Modes != nil {
		DebugI4x4Modes(mbx, mby, predModes, cbpLuma, cbpChroma)
	}

	for blk := 0; blk < 16; blk++ {
		pos := blk4x4Pos[blk]
		y := ybrYY + pos[0]
		x := ybrYX + pos[1]
		// H.264 spec 8.3.1.2.1: when upper-right 4x4 samples are not available,
		// substitute with the rightmost available above sample.
		// Unavailable for scan indices {3, 7, 11, 13, 15}: either the upper-right
		// block hasn't been decoded yet (3, 11) or is outside the MB (7, 13, 15).
		if blk4x4UpperRightUnavail[blk] {
			for k := 4; k < 8; k++ {
				d.ybr[y-1][x+k] = d.ybr[y-1][x+3]
			}
		}
		predIntra4x4Func[predModes[blk]](d, y, x)

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
				DebugBlkLog(mbx, mby, blk, nz, tmp, predModes[blk], nC)
			}
			if nz > 0 {
				reorderCoeffs(d.coeff[blk*16 : blk*16+16])
				dequant4x4(d.coeff[blk*16:blk*16+16], d.qp)
				idct4x4(d.coeff[blk*16 : blk*16+16])
				d.addResidual4x4(y, x, d.coeff[blk*16:blk*16+16])
			}
		}
	}

	if err := d.decodeChroma(br, mbx, mby, int(chromaPredMode), cbpChroma); err != nil {
		return err
	}
	d.storeIntraModes(mbx, mby)
	d.storeNZCoeff(mbx, mby)
	d.copyMBToImg(mbx, mby)
	return nil
}

// decodeMBI8x8 decodes an I_NxN macroblock using the 8x8 transform, spec 8.5.13.
// Called from decodeMBIntraWithType when transform_size_8x8_flag=1 on an I_NxN MB.
//
// Layout: the MB is split into 4 8x8 luma partitions (raster order: top-left,
// top-right, bottom-left, bottom-right). Each has its own Intra_8x8 prediction
// mode read via prev_intra8x8_pred_mode_flag + optional rem_intra8x8_pred_mode.
//
// MPM (spec 8.3.2.1): each partition's top-left 4x4 scan index (0, 4, 8, 12)
// reuses the existing mostProbableMode 4x4 logic — which already handles
// cross-MB neighbor lookups and the spec 8.3.1.1 joint-DC reset fix. The 8x8
// mode is stored at all 4 4x4 scan positions within its partition so neighbor
// queries return the same value regardless of which 4x4 they land on.
//
// Residual: 4 × 64-coeff blocks via read8x8ResidualCAVLC + dequant8x8 + idct8x8.
func (d *Decoder) decodeMBI8x8(br *BitReader, mbx, mby int) error {
	var predModes [4]int
	for p := 0; p < 4; p++ {
		// Top-left 4x4 scan index of partition p is p*4 (scans 0,4,8,12).
		scanIdx := p * 4
		prevFlag, err := br.ReadBool()
		if err != nil {
			return err
		}
		mpm := d.mostProbableMode(mbx, mby, scanIdx)
		if prevFlag {
			predModes[p] = mpm
		} else {
			rem, err := br.ReadBits(3)
			if err != nil {
				return err
			}
			if int(rem) < mpm {
				predModes[p] = int(rem)
			} else {
				predModes[p] = int(rem) + 1
			}
		}
		// Store mode at all 4 4x4 scan indices in this partition so within-MB
		// and cross-MB neighbor lookups return the 8x8 mode consistently.
		for k := 0; k < 4; k++ {
			d.intraModeCur[p*4+k] = predModes[p]
		}
	}

	chromaPredMode, err := br.ReadUE()
	if err != nil {
		return err
	}
	if chromaPredMode > 3 {
		return fmt.Errorf("invalid intra_chroma_pred_mode %d", chromaPredMode)
	}

	cbpCode, err := br.ReadUE()
	if err != nil {
		return err
	}
	if int(cbpCode) >= len(cbpTableIntra4x4) {
		return fmt.Errorf("invalid CBP code %d", cbpCode)
	}
	cbp := cbpTableIntra4x4[cbpCode]
	cbpLuma := cbp % 16
	cbpChroma := cbp / 16

	if cbp > 0 {
		qpDelta, err := br.ReadSE()
		if err != nil {
			return err
		}
		d.qp = (d.qp + int(qpDelta) + 52) % 52
	}

	var blk [64]int16
	for p := 0; p < 4; p++ {
		partY := (p / 2) * 8
		partX := (p % 2) * 8
		y := ybrYY + partY
		x := ybrYX + partX

		// Neighbor availability for this 8x8 partition's intra prediction.
		hasTop := partY > 0 || mby > 0
		hasLeft := partX > 0 || mbx > 0
		hasCorner := (partY > 0 && partX > 0) ||
			(partY > 0 && mbx > 0) ||
			(partX > 0 && mby > 0) ||
			(mbx > 0 && mby > 0)

		// above-right for each partition:
		//  p=0: columns 8..15 of y=-1 come from above MB (if mby > 0).
		//  p=1: columns 16..23 of y=-1 come from above-right MB (mbx+1, mby-1).
		//  p=2: partition 1 is already decoded; its bottom row is our above-right.
		//  p=3: past right edge; right MB not yet decoded.
		aboveRightAvail := false
		switch p {
		case 0:
			aboveRightAvail = mby > 0
		case 1:
			aboveRightAvail = mby > 0 && mbx < d.mbw-1
		case 2:
			aboveRightAvail = true
		case 3:
			aboveRightAvail = false
		}

		samples := d.prepareIntra8x8Samples(y, x, hasTop, hasLeft, hasCorner, aboveRightAvail)
		predIntra8x8Func[predModes[p]](d, y, x, &samples)

		if cbpLuma&(1<<uint(p)) != 0 {
			for i := range blk {
				blk[i] = 0
			}
			// nC at the 8x8 partition's top-left 4x4 scan index, reused for
			// all 4 CAVLC sub-blocks — matches reference decoders (FFmpeg /
			// JM). Using nC=0 here desyncs bit consumption from encoder.
			nC := d.calcNC(mbx, mby, p*4)
			nz, err := read8x8ResidualCAVLC(br, blk[:], [4]int{nC, nC, nC, nC})
			if err != nil {
				return err
			}
			if nz > 0 {
				dequant8x8(blk[:], d.qp)
				idct8x8(blk[:])
				d.addResidual8x8(y, x, blk[:])
			}
			// Spec-aligned nz tracking for 8x8 (matches FFmpeg): all 4 4x4
			// scan positions within the 8x8 get 16 if any nz, else 0.
			mark := 0
			if nz > 0 {
				mark = 16
			}
			for k := 0; k < 4; k++ {
				d.nzCoeffCur[p*4+k] = mark
			}
		}
	}

	if err := d.decodeChroma(br, mbx, mby, int(chromaPredMode), cbpChroma); err != nil {
		return err
	}
	d.storeIntraModes(mbx, mby)
	d.storeNZCoeff(mbx, mby)
	d.copyMBToImg(mbx, mby)
	return nil
}

func (d *Decoder) decodeMBI16x16(br *BitReader, mbx, mby, mbType int) error {
	predMode, cbpChroma, cbpLuma := i16x16Params(mbType)

	chromaPredMode, err := br.ReadUE()
	if err != nil {
		return fmt.Errorf("chromaPredMode: %w", err)
	}
	if chromaPredMode > 3 {
		return fmt.Errorf("invalid intra_chroma_pred_mode %d at bitpos %d", chromaPredMode, br.BitsRead())
	}

	qpDelta, err := br.ReadSE()
	if err != nil {
		return fmt.Errorf("qpDelta: %w", err)
	}
	d.qp = (d.qp + int(qpDelta) + 52) % 52

	predIntra16x16Func[predMode](d, ybrYY, ybrYX)

	// Luma DC: 16 coefficients, Hadamard + dequant.
	dcCoeffs := make([]int16, 16)
	nC := d.calcNC(mbx, mby, 0)
	_, err = readResidualBlock(br, dcCoeffs, 16, nC)
	if err != nil {
		return fmt.Errorf("lumaDC: %w", err)
	}

	reorderCoeffs(dcCoeffs) // CAVLC outputs zigzag order; Hadamard needs raster order
	hadamard4x4(dcCoeffs)
	dequantLumaDC(dcCoeffs, d.qp)

	// Luma AC: for each 4x4 block in scan order, read AC residuals.
	for blk := 0; blk < 16; blk++ {
		dcIdx := blkScanToDCIdx[blk]
		d.coeff[blk*16] = dcCoeffs[dcIdx]

		if cbpLuma != 0 {
			nC := d.calcNC(mbx, mby, blk)
			nz, err := readResidualBlock(br, d.coeff[blk*16+1:blk*16+16], 15, nC)
			if err != nil {
				return err
			}
			d.nzCoeffCur[blk] = nz
			reorderCoeffs(d.coeff[blk*16 : blk*16+16])
		}

		if d.coeff[blk*16] != 0 || d.nzCoeffCur[blk] > 0 {
			dequant4x4(d.coeff[blk*16:blk*16+16], d.qp)
			d.coeff[blk*16] = dcCoeffs[dcIdx] // restore already-dequanted DC
			idct4x4(d.coeff[blk*16 : blk*16+16])
			pos := blk4x4Pos[blk]
			d.addResidual4x4(ybrYY+pos[0], ybrYX+pos[1], d.coeff[blk*16:blk*16+16])
		}
	}

	if err := d.decodeChroma(br, mbx, mby, int(chromaPredMode), cbpChroma); err != nil {
		return err
	}
	d.storeIntraModes(mbx, mby)
	d.storeNZCoeff(mbx, mby)
	d.copyMBToImg(mbx, mby)
	return nil
}

func (d *Decoder) decodeChroma(br *BitReader, mbx, mby, chromaPredMode, cbpChroma int) error {
	qpC := chromaQP(d.qp, int(d.activePPS.ChromaQPIndexOffset))

	predIntraChromaFunc[chromaPredMode](d, ybrBY, ybrBX)
	predIntraChromaFunc[chromaPredMode](d, ybrRY, ybrRX)

	if cbpChroma == 0 {
		return nil
	}

	// Read chroma DC: Cb then Cr.
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

	// Cb AC (4 blocks of 4x4).
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

	// Cr AC (4 blocks of 4x4).
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

func (d *Decoder) addResidual4x4(y, x int, coeffs []int16) {
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			val := int(d.ybr[y+j][x+i]) + int(coeffs[j*4+i])
			if val < 0 {
				val = 0
			} else if val > 255 {
				val = 255
			}
			d.ybr[y+j][x+i] = uint8(val)
		}
	}
}

// addResidual8x8 adds an 8x8 residual (64 coeffs in raster order) to the
// prediction at ybr[y..y+7][x..x+7], clamping to [0, 255].
func (d *Decoder) addResidual8x8(y, x int, coeffs []int16) {
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			val := int(d.ybr[y+j][x+i]) + int(coeffs[j*8+i])
			if val < 0 {
				val = 0
			} else if val > 255 {
				val = 255
			}
			d.ybr[y+j][x+i] = uint8(val)
		}
	}
}

func (d *Decoder) prepareYBR(mbx, mby int) {
	// Default borders to 128.
	for j := range d.ybr {
		d.ybr[j][ybrYX-1] = 128
		d.ybr[j][ybrBX-1] = 128
		d.ybr[j][ybrRX-1] = 128
	}
	for i := 0; i < 48; i++ {
		d.ybr[ybrYY-1][i] = 128
		d.ybr[ybrBY-1][i] = 128
	}

	if mby > 0 {
		imgY := mby*16 - 1
		for i := 0; i < 16; i++ {
			d.ybr[ybrYY-1][ybrYX+i] = d.img.Y[imgY*d.img.YStride+mbx*16+i]
		}
		if mbx < d.mbw-1 {
			for i := 0; i < 8; i++ {
				d.ybr[ybrYY-1][ybrYX+16+i] = d.img.Y[imgY*d.img.YStride+(mbx+1)*16+i]
			}
		} else {
			for i := 0; i < 8; i++ {
				d.ybr[ybrYY-1][ybrYX+16+i] = d.ybr[ybrYY-1][ybrYX+15]
			}
		}
		imgC := mby*8 - 1
		for i := 0; i < 8; i++ {
			d.ybr[ybrBY-1][ybrBX+i] = d.img.Cb[imgC*d.img.CStride+mbx*8+i]
			d.ybr[ybrBY-1][ybrRX+i] = d.img.Cr[imgC*d.img.CStride+mbx*8+i]
		}
	}

	if mbx > 0 {
		for j := 0; j < 16; j++ {
			d.ybr[ybrYY+j][ybrYX-1] = d.img.Y[(mby*16+j)*d.img.YStride+mbx*16-1]
		}
		for j := 0; j < 8; j++ {
			d.ybr[ybrBY+j][ybrBX-1] = d.img.Cb[(mby*8+j)*d.img.CStride+mbx*8-1]
			d.ybr[ybrRY+j][ybrRX-1] = d.img.Cr[(mby*8+j)*d.img.CStride+mbx*8-1]
		}
	}

	if mbx > 0 && mby > 0 {
		d.ybr[ybrYY-1][ybrYX-1] = d.img.Y[(mby*16-1)*d.img.YStride+mbx*16-1]
		d.ybr[ybrBY-1][ybrBX-1] = d.img.Cb[(mby*8-1)*d.img.CStride+mbx*8-1]
		d.ybr[ybrBY-1][ybrRX-1] = d.img.Cr[(mby*8-1)*d.img.CStride+mbx*8-1]
	}
}

func (d *Decoder) copyMBToImg(mbx, mby int) {
	for j := 0; j < 16; j++ {
		for i := 0; i < 16; i++ {
			d.img.Y[(mby*16+j)*d.img.YStride+mbx*16+i] = d.ybr[ybrYY+j][ybrYX+i]
		}
	}
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			d.img.Cb[(mby*8+j)*d.img.CStride+mbx*8+i] = d.ybr[ybrBY+j][ybrBX+i]
			d.img.Cr[(mby*8+j)*d.img.CStride+mbx*8+i] = d.ybr[ybrRY+j][ybrRX+i]
		}
	}
}

func (d *Decoder) cropImg() *image.YCbCr {
	if d.activeSPS == nil {
		return d.img
	}
	w := d.activeSPS.Width
	h := d.activeSPS.Height
	if w == d.img.Rect.Dx() && h == d.img.Rect.Dy() {
		return d.img
	}
	return d.img.SubImage(image.Rect(0, 0, w, h)).(*image.YCbCr)
}

// calcNC computes CAVLC nC for luma block blkIdx (scan order 0-15).
func (d *Decoder) calcNC(mbx, mby, blkIdx int) int {
	nA := d.getNCLuma(mbx, mby, blkIdx, true)
	nB := d.getNCLuma(mbx, mby, blkIdx, false)
	if nA >= 0 && nB >= 0 {
		return (nA + nB + 1) >> 1
	}
	if nA >= 0 {
		return nA
	}
	if nB >= 0 {
		return nB
	}
	return 0
}

// getNCLuma gets nC from left (isLeft=true) or above (isLeft=false) neighbor.
func (d *Decoder) getNCLuma(mbx, mby, blkIdx int, isLeft bool) int {
	var neighborIdx int
	var neighborMBIdx int

	if isLeft {
		neighborIdx = blkLeftIdx[blkIdx]
		if neighborIdx >= 0 {
			return d.nzCoeffCur[neighborIdx]
		}
		if mbx == 0 {
			return -1
		}
		neighborMBIdx = mby*d.mbw + (mbx - 1)
		neighborIdx = blkLeftMBIdx[blkIdx]
	} else {
		neighborIdx = blkAboveIdx[blkIdx]
		if neighborIdx >= 0 {
			return d.nzCoeffCur[neighborIdx]
		}
		if mby == 0 {
			return -1
		}
		neighborMBIdx = (mby-1)*d.mbw + mbx
		neighborIdx = blkAboveMBIdx[blkIdx]
	}

	if neighborIdx < 0 {
		return -1
	}
	return d.nzCoeff[neighborMBIdx*24+neighborIdx]
}

func (d *Decoder) calcNCChroma(mbx, mby, blkIdx int) int {
	nA := d.getNCChromaLeft(mbx, mby, blkIdx)
	nB := d.getNCChromaAbove(mbx, mby, blkIdx)
	if nA >= 0 && nB >= 0 {
		return (nA + nB + 1) >> 1
	}
	if nA >= 0 {
		return nA
	}
	if nB >= 0 {
		return nB
	}
	return 0
}

func (d *Decoder) getNCChromaLeft(mbx, mby, blkIdx int) int {
	// blkIdx 0-3: Cb, 4-7: Cr. Within each plane: raster 2x2.
	bx := blkIdx % 2
	if bx > 0 {
		return d.nzCoeffCur[16+blkIdx-1]
	}
	if mbx == 0 {
		return -1
	}
	// Left neighbor: right column of left MB's chroma.
	prevMBIdx := mby*d.mbw + (mbx - 1)
	leftBlk := blkIdx + 1 // right column in 2x2
	return d.nzCoeff[prevMBIdx*24+16+leftBlk]
}

func (d *Decoder) getNCChromaAbove(mbx, mby, blkIdx int) int {
	by := (blkIdx % 4) / 2
	if by > 0 {
		return d.nzCoeffCur[16+blkIdx-2]
	}
	if mby == 0 {
		return -1
	}
	// Above neighbor: bottom row of above MB's chroma.
	aboveMBIdx := (mby-1)*d.mbw + mbx
	aboveBlk := blkIdx + 2 // bottom row in 2x2
	return d.nzCoeff[aboveMBIdx*24+16+aboveBlk]
}

func (d *Decoder) storeNZCoeff(mbx, mby int) {
	mbIdx := mby*d.mbw + mbx
	copy(d.nzCoeff[mbIdx*24:mbIdx*24+24], d.nzCoeffCur[:])
}

func (d *Decoder) storeIntraModes(mbx, mby int) {
	mbIdx := mby*d.mbw + mbx
	copy(d.intraModes[mbIdx*16:mbIdx*16+16], d.intraModeCur[:])
}

func (d *Decoder) mostProbableMode(mbx, mby, blk int) int {
	// Spec 8.3.1.1: when mbAddrA or mbAddrB is unavailable, both
	// intraMxMPredModeA and intraMxMPredModeB are jointly set to Intra_4x4_DC.
	// "Unavailable" means the neighbor MB lies outside the picture / slice.
	// Within-MB neighbors (blkLeftIdx/blkAboveIdx >= 0) are always available.
	leftMBUnavail := blkLeftIdx[blk] < 0 && mbx == 0
	aboveMBUnavail := blkAboveIdx[blk] < 0 && mby == 0
	if leftMBUnavail || aboveMBUnavail {
		return intra4x4DC
	}
	modeA := d.getIntraModeNeighbor(mbx, mby, blk, true)
	modeB := d.getIntraModeNeighbor(mbx, mby, blk, false)
	if modeA < modeB {
		return modeA
	}
	return modeB
}

func (d *Decoder) getIntraModeNeighbor(mbx, mby, blkIdx int, isLeft bool) int {
	if isLeft {
		neighborIdx := blkLeftIdx[blkIdx]
		if neighborIdx >= 0 {
			mode := d.intraModeCur[neighborIdx]
			if mode < 0 {
				return intra4x4DC
			}
			return mode
		}
		if mbx == 0 {
			return intra4x4DC
		}
		neighborMBIdx := mby*d.mbw + (mbx - 1)
		neighborBlk := blkLeftMBIdx[blkIdx]
		if neighborBlk < 0 {
			return intra4x4DC
		}
		mode := d.intraModes[neighborMBIdx*16+neighborBlk]
		if mode < 0 {
			return intra4x4DC
		}
		return mode
	}
	neighborIdx := blkAboveIdx[blkIdx]
	if neighborIdx >= 0 {
		mode := d.intraModeCur[neighborIdx]
		if mode < 0 {
			return intra4x4DC
		}
		return mode
	}
	if mby == 0 {
		return intra4x4DC
	}
	neighborMBIdx := (mby-1)*d.mbw + mbx
	neighborBlk := blkAboveMBIdx[blkIdx]
	if neighborBlk < 0 {
		return intra4x4DC
	}
	mode := d.intraModes[neighborMBIdx*16+neighborBlk]
	if mode < 0 {
		return intra4x4DC
	}
	return mode
}

// cbpTableIntra4x4: maps CBP code number → CBP value (spec table 9-4).
var cbpTableIntra4x4 = [48]int{
	47, 31, 15, 0, 23, 27, 29, 30, 7, 11, 13, 14, 39, 43, 45, 46,
	16, 3, 5, 10, 12, 19, 21, 26, 28, 35, 37, 42, 44, 1, 2, 4,
	8, 17, 18, 20, 24, 6, 9, 22, 25, 32, 33, 34, 36, 40, 38, 41,
}

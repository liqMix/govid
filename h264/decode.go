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
	mbw   int    // macroblocks per row
	mbh   int    // macroblocks per column
	qp    int    // current QP
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

	// Reference frame storage: short-term DPB entries plus the per-slice
	// reference picture lists (spec 8.2.4). curRefList[0] is L0, [1] is L1.
	refFrames  []*refFrame
	nextRefID  int
	curRefList [2][]*refFrame

	// Picture order count state (spec 8.2.1). curPOC is the POC of the
	// picture currently being decoded; the prev* fields carry state between
	// pictures for POC types 0 and 2.
	curPOC             int
	prevPOCMsb         int
	prevPOCLsb         int
	prevFrameNumOffset int
	prevFrameNum       int

	// curIsIDR / curIsB / maxReorder feed the Codec's display reordering.
	// sawMMCO5 is set when an MMCO op 5 reset renumbered the current picture
	// (the Codec treats it like an IDR for output-order keys).
	curIsIDR bool
	sawB     bool
	sawMMCO5 bool

	// biBuf holds the saved list-0 prediction during B bi-prediction.
	biBuf mcBuf

	// Per-MB inter prediction info for MV prediction and deblocking.
	mbInfo []mbInterInfo

	// Per-MB intra 4x4 prediction modes (scan order 0-15), -1 if not I_4x4.
	intraModes   []int
	intraModeCur [16]int

	// disableDeblock skips the deblocking filter (for testing).
	disableDeblock bool

	// lastQPDeltaNonZero tracks whether the previously decoded MB in the
	// current slice had a non-zero mb_qp_delta (CABAC dqp context).
	lastQPDeltaNonZero bool

	// Active weightScale matrices (raster order) for inverse quantization,
	// derived from the bound SPS/PPS pair by updateScalingMatrices. Lists:
	// 4x4 = Intra Y/Cb/Cr, Inter Y/Cb/Cr; 8x8 = Intra Y, Inter Y.
	scalingWS4 [6][16]int
	scalingWS8 [2][64]int
	scalingSPS *SPS
	scalingPPS *PPS

	// Multi-slice picture state: mbSlice[i] records which slice (sequence
	// number curSlice) decoded MB i, so neighbor availability can exclude
	// MBs of other slices (spec 6.4.9); picMBsDone counts the current
	// picture's decoded MBs so output waits for the last slice.
	mbSlice    []int32
	curSlice   int32
	picMBsDone int
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
	false, false, false, true, // scan 3: (4,4) upper-right not yet decoded
	false, false, false, true, // scan 7: (4,12) upper-right outside MB
	false, false, false, true, // scan 11: (12,4) upper-right not yet decoded
	false, true, false, true, // scan 13: (8,12) outside MB; scan 15: (12,12) outside MB
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

// read8x8ResidualCAVLC reads the 64 coefficients of the 8x8 transform block
// for luma 8x8 partition p (0-3) via CAVLC. Per spec 7.3.5.3.2, the 8x8 block
// is coded as four ordinary 4x4 residual blocks at scan indices p*4+0..p*4+3,
// each with its own neighbor-derived nC, and the coefficients interleave into
// the 8x8 zigzag scan: level8x8[4*i + i4x4] = level4x4[i4x4][i]. The actual
// per-sub-block TotalCoeff is stored in nzCoeffCur immediately, because spec
// 9.2.1 treats these sub-blocks as normal 4x4 blocks when later blocks (in
// this MB or a neighbor) derive their nC.
//
// coeffs must be length >= 64 and is written in raster order.
// Returns the total non-zero count across all sub-blocks.
func (d *Decoder) read8x8ResidualCAVLC(br *BitReader, coeffs []int16, mbx, mby, p int) (int, error) {
	for i := 0; i < 64; i++ {
		coeffs[i] = 0
	}
	var subBlock [16]int16
	totalNZ := 0
	for s := 0; s < 4; s++ {
		for i := range subBlock {
			subBlock[i] = 0
		}
		blkIdx := p*4 + s
		nC := d.calcNC(mbx, mby, blkIdx)
		nz, err := readResidualBlock(br, subBlock[:], 16, nC)
		if err != nil {
			return 0, err
		}
		d.nzCoeffCur[blkIdx] = nz
		totalNZ += nz
		for k := 0; k < 16; k++ {
			if subBlock[k] == 0 {
				continue
			}
			coeffs[zigzagToRaster8x8[4*k+s]] = subBlock[k]
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
	if d.scalingSPS != sps || d.scalingPPS != pps {
		d.updateScalingMatrices(sps, pps)
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

	// A slice with first_mb_in_slice == 0 starts a new picture; later slices
	// of the same picture only add their macroblocks. Per-picture state
	// (IDR handling, POC) is derived once, from the first slice.
	if sh.firstMB == 0 {
		d.curIsIDR = nalu.Type == NALSliceIDR
		if d.curIsIDR {
			d.resetDPB()
		}
		if err := d.computePOC(sh, nalu); err != nil {
			return nil, err
		}
		d.picMBsDone = 0
	}
	d.curSlice++

	cabac := pps.EntropyCodingModeFlag

	var n int
	switch sh.sliceType {
	case sliceTypeI, sliceTypeSI:
		if cabac {
			n, err = d.decodeISliceCABAC(br, sh)
		} else {
			n, err = d.decodeISlice(br, sh)
		}
	case sliceTypeP:
		if cabac {
			n, err = d.decodePSliceCABAC(br, sh)
		} else {
			n, err = d.decodePSliceImpl(br, sh)
		}
	case sliceTypeB:
		d.sawB = true
		if cabac {
			n, err = d.decodeBSliceCABAC(br, sh)
		} else {
			n, err = d.decodeBSliceImpl(br, sh)
		}
	default:
		return nil, fmt.Errorf("unsupported slice type %d", sh.sliceType)
	}
	if err != nil {
		return nil, err
	}
	d.picMBsDone += n
	if d.picMBsDone < d.mbw*d.mbh {
		// More slices of this picture follow in later NAL units.
		return nil, nil
	}

	// Picture complete: deblock (crossing slice boundaries; the deblock
	// parameters of the last slice apply — encoders keep them uniform
	// across a picture's slices), store, and emit.
	d.deblockFrame(sh)
	if nalu.RefIDC != 0 {
		if err := d.storeRefFrame(sh); err != nil {
			return nil, err
		}
	}
	return d.cropImg(), nil
}

// computePOC derives the picture order count of the current picture
// (spec 8.2.1) for pic_order_cnt_type 0 and 2.
func (d *Decoder) computePOC(sh *sliceHeader, nalu NALUnit) error {
	sps := d.activeSPS
	switch sps.PicOrderCntType {
	case 0:
		if d.curIsIDR {
			d.prevPOCMsb = 0
			d.prevPOCLsb = 0
		}
		maxLsb := 1 << uint(sps.Log2MaxPicOrderCntLsb)
		lsb := int(sh.picOrderCntLsb)
		msb := d.prevPOCMsb
		if lsb < d.prevPOCLsb && d.prevPOCLsb-lsb >= maxLsb/2 {
			msb = d.prevPOCMsb + maxLsb
		} else if lsb > d.prevPOCLsb && lsb-d.prevPOCLsb > maxLsb/2 {
			msb = d.prevPOCMsb - maxLsb
		}
		d.curPOC = msb + lsb
		if nalu.RefIDC != 0 {
			d.prevPOCMsb = msb
			d.prevPOCLsb = lsb
		}
	case 2:
		maxFrameNum := 1 << uint(sps.Log2MaxFrameNum)
		fn := int(sh.frameNum)
		var offset int
		switch {
		case d.curIsIDR:
			offset = 0
		case d.prevFrameNum > fn:
			offset = d.prevFrameNumOffset + maxFrameNum
		default:
			offset = d.prevFrameNumOffset
		}
		poc := 2 * (offset + fn)
		if nalu.RefIDC == 0 {
			poc--
		}
		d.curPOC = poc
		d.prevFrameNumOffset = offset
		d.prevFrameNum = fn
	default:
		return fmt.Errorf("pic_order_cnt_type %d not supported", sps.PicOrderCntType)
	}
	return nil
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
	d.mbSlice = make([]int32, totalMBs)
	for i := range d.mbSlice {
		d.mbSlice[i] = -1
	}
}

// mbAvailable reports whether the MB at (nx, ny) is available as a neighbor
// of the MB currently being decoded: inside the picture AND decoded by the
// current slice (spec 6.4.9 — MBs of other slices are not available for
// prediction or context derivation; only the deblocking filter crosses
// slice boundaries).
func (d *Decoder) mbAvailable(nx, ny int) bool {
	if nx < 0 || ny < 0 || nx >= d.mbw || ny >= d.mbh {
		return false
	}
	return d.mbSlice[ny*d.mbw+nx] == d.curSlice
}

// DebugMBBits, if non-nil, is called with (mbx, mby, startBit, endBit) for each MB.
var DebugMBBits func(mbx, mby, startBit, endBit int)

// DebugPSliceTrace, if non-nil, is called for every MB in a P-slice with full context.
// branch: "S" = skip (inside skipRun batch), "N" = non-skip.
// rawVal: skipRun UE value for "S", mbType UE value for "N".
var DebugPSliceTrace func(mbx, mby int, branch string, startBit, endBit int, rawVal uint32)

func (d *Decoder) decodeISlice(br *BitReader, sh *sliceHeader) (int, error) {
	totalMBs := d.mbw * d.mbh
	mbIdx := int(sh.firstMB)
	for mbIdx < totalMBs {
		mbx := mbIdx % d.mbw
		mby := mbIdx / d.mbw
		d.mbSlice[mbIdx] = d.curSlice
		startBit := br.BitsRead()
		if err := d.decodeMBIntra(br, mbx, mby); err != nil {
			return 0, fmt.Errorf("MB(%d,%d): %w", mbx, mby, err)
		}
		if DebugMBBits != nil {
			DebugMBBits(mbx, mby, startBit, br.BitsRead())
		}
		// Mark as intra in mbInfo for P-slice MV prediction context.
		idx := mbIdx
		d.mbInfo[idx].isIntra = true
		d.mbInfo[idx].qp = d.pcmAwareQP(idx)
		for k := range d.mbInfo[idx].mv {
			d.mbInfo[idx].mv[k] = [2]int16{0, 0}
		}
		for k := range d.mbInfo[idx].refIdx {
			d.mbInfo[idx].refIdx[k] = -1
			d.mbInfo[idx].refIdxL1[k] = -1
			d.mbInfo[idx].predMask[k] = 0
		}
		mbIdx++
		if !br.MoreRBSPData() {
			break // end of this slice's data
		}
	}
	return mbIdx - int(sh.firstMB), nil
}

// DebugMBLog, if non-nil, receives debug info for each decoded intra MB.
var DebugMBLog func(mbx, mby, mbType, bitsBeforeMB int)

// DebugBlkLog, if non-nil, is called after reading residual for a 4x4 block.
// coeffs are post-CAVLC (zigzag order, pre-reorder), nz is non-zero count.
var DebugBlkLog func(mbx, mby, blk, nz int, coeffs []int16, predMode int, nC int)

// DebugI4x4Modes, if non-nil, is called with all 16 prediction modes after parsing.
var DebugI4x4Modes func(mbx, mby int, modes [16]int, cbpLuma, cbpChroma int)

// DebugI8x8Modes, if non-nil, is called with the 4 partition prediction modes
// of each I_NxN MB decoded with transform_size_8x8_flag=1.
var DebugI8x8Modes func(mbx, mby int, modes [4]int, cbpLuma, cbpChroma int)

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
	// mbInfo persists across frames; reset the transform flag so a stale value
	// from the previous frame's co-located MB cannot leak into deblocking.
	d.mbInfo[mby*d.mbw+mbx].transform8x8 = false
	for i := range d.coeff {
		d.coeff[i] = 0
	}
	for i := range d.nzCoeffCur {
		d.nzCoeffCur[i] = 0
	}
	for i := range d.intraModeCur {
		d.intraModeCur[i] = -1
	}

	d.mbInfo[mby*d.mbw+mbx].isPCM = false
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
				d.mbInfo[mby*d.mbw+mbx].transform8x8 = true
				return d.decodeMBI8x8(br, mbx, mby)
			}
		}
		return d.decodeMBI4x4(br, mbx, mby)
	}
	return d.decodeMBI16x16(br, mbx, mby, mbType)
}

func (d *Decoder) decodeMBPCM(br *BitReader, mbx, mby int) error {
	br.ByteAlign()
	if err := d.readPCMSamples(br); err != nil {
		return err
	}
	d.finishPCM(mbx, mby)
	return nil
}

// readPCMSamples reads the raw pcm_sample_luma / pcm_sample_chroma bytes into
// the reconstruction workspace. br must already be byte-aligned.
func (d *Decoder) readPCMSamples(br *BitReader) error {
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
	return nil
}

// pcmAwareQP returns the QP to store for deblocking of the MB at idx: 0 for
// I_PCM macroblocks (spec 8.7), else the current decoder QP.
func (d *Decoder) pcmAwareQP(idx int) int {
	if d.mbInfo[idx].isPCM {
		return 0
	}
	return d.qp
}

// finishPCM records the per-MB side state shared by CAVLC and CABAC I_PCM:
// every block counts as fully coded for neighbor contexts (TotalCoeff 16,
// FFmpeg cbp_table 0x1EF), and the MB is marked so deblocking uses QP 0
// (spec 8.7: QPY of an I_PCM macroblock is 0).
func (d *Decoder) finishPCM(mbx, mby int) {
	for i := range d.nzCoeffCur {
		d.nzCoeffCur[i] = 16
	}
	info := &d.mbInfo[mby*d.mbw+mbx]
	info.isPCM = true
	info.cbpCabac = 0x1EF
	d.storeIntraModes(mbx, mby)
	d.storeNZCoeff(mbx, mby)
	d.copyMBToImg(mbx, mby)
}

// predIntra4x4 runs Intra_4x4 prediction for the block at ybr (y, x),
// applying the spec 8.3.1.2.1 upper-right sample substitution first: when the
// upper-right 4x4 samples are not available (scan indices {3, 7, 11, 13, 15}
// — either not yet decoded or outside the MB), replicate the rightmost
// available above sample.
func (d *Decoder) predIntra4x4(y, x, mode, blk int) {
	if blk4x4UpperRightUnavail[blk] {
		for k := 4; k < 8; k++ {
			d.ybr[y-1][x+k] = d.ybr[y-1][x+3]
		}
	}
	predIntra4x4Func[mode](d, y, x)
}

// predIntra8x8Part runs Intra_8x8 prediction for partition p (Z order) at
// ybr (y, x), deriving neighbor availability per spec 6.4.9/8.3.2.2.
func (d *Decoder) predIntra8x8Part(y, x, p, mode, mbx, mby int) {
	partY := (p / 2) * 8
	partX := (p % 2) * 8
	topMB := d.mbAvailable(mbx, mby-1)
	leftMB := d.mbAvailable(mbx-1, mby)
	hasTop := partY > 0 || topMB
	hasLeft := partX > 0 || leftMB
	hasCorner := (partY > 0 && partX > 0) ||
		(partY > 0 && leftMB) ||
		(partX > 0 && topMB) ||
		(leftMB && topMB && d.mbAvailable(mbx-1, mby-1))

	// above-right for each partition:
	//  p=0: columns 8..15 of y=-1 come from above MB (if mby > 0).
	//  p=1: columns 16..23 of y=-1 come from above-right MB (mbx+1, mby-1).
	//  p=2: partition 1 is already decoded; its bottom row is our above-right.
	//  p=3: past right edge; right MB not yet decoded.
	aboveRightAvail := false
	switch p {
	case 0:
		aboveRightAvail = topMB
	case 1:
		aboveRightAvail = d.mbAvailable(mbx+1, mby-1)
	case 2:
		aboveRightAvail = true
	}

	samples := d.prepareIntra8x8Samples(y, x, hasTop, hasLeft, hasCorner, aboveRightAvail)
	predIntra8x8Func[mode](d, y, x, &samples)
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
		d.predIntra4x4(y, x, predModes[blk], blk)

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
				dequant4x4(d.coeff[blk*16:blk*16+16], d.qp, d.wsLuma4(true))
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

	if DebugI8x8Modes != nil {
		DebugI8x8Modes(mbx, mby, predModes, cbpLuma, cbpChroma)
	}

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
		d.predIntra8x8Part(y, x, p, predModes[p], mbx, mby)

		if cbpLuma&(1<<uint(p)) != 0 {
			nz, err := d.read8x8ResidualCAVLC(br, blk[:], mbx, mby, p)
			if err != nil {
				return err
			}
			if nz > 0 {
				dequant8x8(blk[:], d.qp, d.wsLuma8(true))
				idct8x8(blk[:])
				d.addResidual8x8(y, x, blk[:])
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
	dequantLumaDC(dcCoeffs, d.qp, d.scalingWS4[0][0])

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
			dequant4x4(d.coeff[blk*16:blk*16+16], d.qp, d.wsLuma4(true))
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
	dequantChromaDC(cbDC, qpC, d.scalingWS4[1][0])
	hadamard2x2(crDC)
	dequantChromaDC(crDC, qpC, d.scalingWS4[2][0])

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
			dequant4x4(d.coeff[base:base+16], qpC, d.wsChroma4(true, false))
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
			dequant4x4(d.coeff[base:base+16], qpC, d.wsChroma4(true, true))
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

	if d.mbAvailable(mbx, mby-1) {
		imgY := mby*16 - 1
		for i := 0; i < 16; i++ {
			d.ybr[ybrYY-1][ybrYX+i] = d.img.Y[imgY*d.img.YStride+mbx*16+i]
		}
		if d.mbAvailable(mbx+1, mby-1) {
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

	if d.mbAvailable(mbx-1, mby) {
		for j := 0; j < 16; j++ {
			d.ybr[ybrYY+j][ybrYX-1] = d.img.Y[(mby*16+j)*d.img.YStride+mbx*16-1]
		}
		for j := 0; j < 8; j++ {
			d.ybr[ybrBY+j][ybrBX-1] = d.img.Cb[(mby*8+j)*d.img.CStride+mbx*8-1]
			d.ybr[ybrRY+j][ybrRX-1] = d.img.Cr[(mby*8+j)*d.img.CStride+mbx*8-1]
		}
	}

	if d.mbAvailable(mbx-1, mby-1) {
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
		if !d.mbAvailable(mbx-1, mby) {
			return -1
		}
		neighborMBIdx = mby*d.mbw + (mbx - 1)
		neighborIdx = blkLeftMBIdx[blkIdx]
	} else {
		neighborIdx = blkAboveIdx[blkIdx]
		if neighborIdx >= 0 {
			return d.nzCoeffCur[neighborIdx]
		}
		if !d.mbAvailable(mbx, mby-1) {
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
	if !d.mbAvailable(mbx-1, mby) {
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
	if !d.mbAvailable(mbx, mby-1) {
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
	leftMBUnavail := blkLeftIdx[blk] < 0 && !d.mbAvailable(mbx-1, mby)
	aboveMBUnavail := blkAboveIdx[blk] < 0 && !d.mbAvailable(mbx, mby-1)
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
		if !d.mbAvailable(mbx-1, mby) {
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
	if !d.mbAvailable(mbx, mby-1) {
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

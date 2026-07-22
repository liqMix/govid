package h264

import "fmt"

// Slice types from the spec.
const (
	sliceTypeP  = 0
	sliceTypeB  = 1
	sliceTypeI  = 2
	sliceTypeSP = 3
	sliceTypeSI = 4
)

// MB type constants for I-slices.
const (
	mbTypeINxN   = 0  // I_NxN (I_4x4 or I_8x8)
	mbTypeI16x16 = 1  // I_16x16_0_0_0 through I_16x16_3_2_1 (1..24)
	mbTypeIPCM   = 25 // I_PCM
)

// sliceHeader holds parsed slice header data.
type sliceHeader struct {
	sliceType               uint32
	ppsID                   uint32
	frameNum                uint32
	idrPicID                uint32
	picOrderCntLsb          uint32
	deltaPicOrderCntBottom  int32
	deltaPicOrderCnt        [2]int32
	redundantPicCnt         uint32
	directSpatialMvPred     bool
	numRefIdxL0Active       uint32
	numRefIdxL1Active       uint32
	sliceQPDelta            int32
	disableDeblockingFilter int32
	sliceAlphaC0Offset      int32
	sliceBetaOffset         int32
	refPicListModL0         []refListModOp
	refPicListModL1         []refListModOp
	weights                 *predWeights
	weightsL1               *predWeights
	cabacInitIdc            uint32
	mmco                    []mmcoOp
}

// mmcoOp is one memory_management_control_operation (spec 7.4.3.3).
type mmcoOp struct {
	op   uint32
	arg1 uint32
	arg2 uint32
}

// predWeights holds the explicit weighted prediction parameters from
// pred_weight_table (spec 7.3.3.2), one entry per L0 reference index.
type predWeights struct {
	lumaLog2Denom   int
	chromaLog2Denom int
	luma            []weightOffset
	chroma          [][2]weightOffset // [refIdx][0]=Cb, [1]=Cr
}

// weightOffset is one weight/offset pair. explicit is false when the stream
// left the entry at its default, which the weighting formula maps to identity.
type weightOffset struct {
	weight   int
	offset   int
	explicit bool
}

// refListModOp is one ref_pic_list_modification operation (spec 7.4.3.1).
// idc 0/1 reorder a short-term picture by abs_diff_pic_num_minus1 (val);
// idc 2 selects a long-term picture by long_term_pic_num (unsupported).
type refListModOp struct {
	idc uint32
	val uint32
}

// parseSliceHeader parses a slice header from RBSP data.
func parseSliceHeader(br *BitReader, sps *SPS, pps *PPS, nalType uint8, nalRefIDC uint8) (*sliceHeader, error) {
	sh := &sliceHeader{}
	var err error

	// first_mb_in_slice
	_, err = br.ReadUE()
	if err != nil {
		return nil, fmt.Errorf("slice: first_mb_in_slice: %w", err)
	}

	st, err := br.ReadUE()
	if err != nil {
		return nil, fmt.Errorf("slice: slice_type: %w", err)
	}
	// Map 5-9 to 0-4.
	if st >= 5 {
		st -= 5
	}
	sh.sliceType = st

	sh.ppsID, err = br.ReadUE()
	if err != nil {
		return nil, fmt.Errorf("slice: pic_parameter_set_id: %w", err)
	}

	if sps.SeparateColourPlane {
		_, err = br.ReadBits(2) // colour_plane_id
		if err != nil {
			return nil, err
		}
	}

	sh.frameNum, err = br.ReadBits(uint(sps.Log2MaxFrameNum))
	if err != nil {
		return nil, fmt.Errorf("slice: frame_num: %w", err)
	}

	if !sps.FrameMBSOnly {
		// field_pic_flag, bottom_field_flag — skip for Baseline (always frame).
		_, err = br.ReadBool()
		if err != nil {
			return nil, err
		}
	}

	if nalType == NALSliceIDR {
		sh.idrPicID, err = br.ReadUE()
		if err != nil {
			return nil, fmt.Errorf("slice: idr_pic_id: %w", err)
		}
	}

	if sps.PicOrderCntType == 0 {
		sh.picOrderCntLsb, err = br.ReadBits(uint(sps.Log2MaxPicOrderCntLsb))
		if err != nil {
			return nil, err
		}
		if pps.BottomFieldPicOrderInFrame && !false /* field_pic_flag */ {
			sh.deltaPicOrderCntBottom, err = br.ReadSE()
			if err != nil {
				return nil, err
			}
		}
	}
	if sps.PicOrderCntType == 1 && !sps.DeltaPicOrderAlwaysZero {
		sh.deltaPicOrderCnt[0], err = br.ReadSE()
		if err != nil {
			return nil, err
		}
		if pps.BottomFieldPicOrderInFrame {
			sh.deltaPicOrderCnt[1], err = br.ReadSE()
			if err != nil {
				return nil, err
			}
		}
	}

	if pps.RedundantPicCntPresent {
		sh.redundantPicCnt, err = br.ReadUE()
		if err != nil {
			return nil, err
		}
	}

	if sh.sliceType == sliceTypeB {
		sh.directSpatialMvPred, err = br.ReadBool()
		if err != nil {
			return nil, fmt.Errorf("slice: direct_spatial_mv_pred_flag: %w", err)
		}
	}

	// P/B-slice reference count overrides.
	if sh.sliceType == sliceTypeP || sh.sliceType == sliceTypeSP || sh.sliceType == sliceTypeB {
		numRefOverride, err := br.ReadBool()
		if err != nil {
			return nil, err
		}
		sh.numRefIdxL0Active = pps.NumRefIdxL0DefaultActive
		sh.numRefIdxL1Active = pps.NumRefIdxL1DefaultActive
		if numRefOverride {
			l0, err := br.ReadUE()
			if err != nil {
				return nil, err
			}
			sh.numRefIdxL0Active = l0 + 1
			if sh.sliceType == sliceTypeB {
				l1, err := br.ReadUE()
				if err != nil {
					return nil, err
				}
				sh.numRefIdxL1Active = l1 + 1
			}
		}
	}

	// ref_pic_list_modification
	if sh.sliceType != sliceTypeI && sh.sliceType != sliceTypeSI {
		sh.refPicListModL0, err = parseRefPicListModification(br)
		if err != nil {
			return nil, fmt.Errorf("ref_pic_list_modification l0: %w", err)
		}
	}
	if sh.sliceType == sliceTypeB {
		sh.refPicListModL1, err = parseRefPicListModification(br)
		if err != nil {
			return nil, fmt.Errorf("ref_pic_list_modification l1: %w", err)
		}
	}

	// pred_weight_table: P/SP with weighted prediction, or B with explicit
	// weighted biprediction (weighted_bipred_idc == 1). The denominators
	// appear once; the l1 entry loop follows the l0 loop for B slices.
	if ((sh.sliceType == sliceTypeP || sh.sliceType == sliceTypeSP) && pps.WeightedPredFlag) ||
		(sh.sliceType == sliceTypeB && pps.WeightedBipredIDC == 1) {
		sh.weights, err = parsePredWeightTable(br, sh.numRefIdxL0Active, sps.ChromaFormatIDC, nil)
		if err != nil {
			return nil, fmt.Errorf("pred_weight_table: %w", err)
		}
		if sh.sliceType == sliceTypeB {
			sh.weightsL1, err = parsePredWeightTable(br, sh.numRefIdxL1Active, sps.ChromaFormatIDC, sh.weights)
			if err != nil {
				return nil, fmt.Errorf("pred_weight_table l1: %w", err)
			}
		}
	}

	// dec_ref_pic_marking
	if nalType == NALSliceIDR || nalRefIDC > 0 {
		sh.mmco, err = parseDecRefPicMarking(br, nalType)
		if err != nil {
			return nil, err
		}
	}

	if pps.EntropyCodingModeFlag && sh.sliceType != sliceTypeI && sh.sliceType != sliceTypeSI {
		sh.cabacInitIdc, err = br.ReadUE()
		if err != nil {
			return nil, fmt.Errorf("slice: cabac_init_idc: %w", err)
		}
		if sh.cabacInitIdc > 2 {
			return nil, fmt.Errorf("invalid cabac_init_idc %d", sh.cabacInitIdc)
		}
	}

	sh.sliceQPDelta, err = br.ReadSE()
	if err != nil {
		return nil, fmt.Errorf("slice: slice_qp_delta: %w", err)
	}

	if sh.sliceType == sliceTypeSP || sh.sliceType == sliceTypeSI {
		if sh.sliceType == sliceTypeSP {
			_, _ = br.ReadBool() // sp_for_switch_flag
		}
		_, _ = br.ReadSE() // slice_qs_delta
	}

	if pps.DeblockingFilterControlPresent {
		dfMode, err := br.ReadUE()
		if err != nil {
			return nil, err
		}
		sh.disableDeblockingFilter = int32(dfMode)
		if sh.disableDeblockingFilter != 1 {
			sh.sliceAlphaC0Offset, err = br.ReadSE()
			if err != nil {
				return nil, err
			}
			sh.sliceAlphaC0Offset *= 2
			sh.sliceBetaOffset, err = br.ReadSE()
			if err != nil {
				return nil, err
			}
			sh.sliceBetaOffset *= 2
		}
	}

	return sh, nil
}

func parseRefPicListModification(br *BitReader) ([]refListModOp, error) {
	flag, err := br.ReadBool()
	if err != nil {
		return nil, err
	}
	if !flag {
		return nil, nil
	}
	var ops []refListModOp
	for {
		idc, err := br.ReadUE()
		if err != nil {
			return nil, err
		}
		if idc == 3 {
			return ops, nil
		}
		if idc > 3 {
			return nil, fmt.Errorf("invalid modification_of_pic_nums_idc %d", idc)
		}
		val, err := br.ReadUE()
		if err != nil {
			return nil, err
		}
		ops = append(ops, refListModOp{idc: idc, val: val})
	}
}

func parseDecRefPicMarking(br *BitReader, nalType uint8) ([]mmcoOp, error) {
	if nalType == NALSliceIDR {
		if _, err := br.ReadBool(); err != nil { // no_output_of_prior_pics_flag
			return nil, err
		}
		_, err := br.ReadBool() // long_term_reference_flag
		return nil, err
	}
	flag, err := br.ReadBool() // adaptive_ref_pic_marking_mode_flag
	if err != nil {
		return nil, err
	}
	if !flag {
		return nil, nil
	}
	var ops []mmcoOp
	for {
		op, err := br.ReadUE()
		if err != nil {
			return nil, err
		}
		if op == 0 {
			return ops, nil
		}
		m := mmcoOp{op: op}
		switch op {
		case 1, 2, 4, 6:
			m.arg1, err = br.ReadUE()
		case 3:
			m.arg1, err = br.ReadUE()
			if err != nil {
				return nil, err
			}
			m.arg2, err = br.ReadUE()
		case 5:
			// No parameters.
		default:
			return nil, fmt.Errorf("unknown MMCO operation %d", op)
		}
		if err != nil {
			return nil, err
		}
		ops = append(ops, m)
	}
}

// parsePredWeightTable parses one list's entries of the pred_weight_table()
// syntax structure (spec 7.3.3.2). When cont is nil the shared denominators
// are read first; for the l1 continuation of a B slice pass the l0 table so
// its denominators are reused.
func parsePredWeightTable(br *BitReader, numRefIdxActive uint32, chromaFormatIDC uint32, cont *predWeights) (*predWeights, error) {
	pw := &predWeights{
		luma:   make([]weightOffset, numRefIdxActive),
		chroma: make([][2]weightOffset, numRefIdxActive),
	}

	if cont != nil {
		pw.lumaLog2Denom = cont.lumaLog2Denom
		pw.chromaLog2Denom = cont.chromaLog2Denom
	} else {
		lumaDenom, err := br.ReadUE()
		if err != nil {
			return nil, err
		}
		pw.lumaLog2Denom = int(lumaDenom)
		if chromaFormatIDC != 0 {
			chromaDenom, err := br.ReadUE()
			if err != nil {
				return nil, err
			}
			pw.chromaLog2Denom = int(chromaDenom)
		}
	}

	for i := uint32(0); i < numRefIdxActive; i++ {
		lumaFlag, err := br.ReadBool()
		if err != nil {
			return nil, err
		}
		if lumaFlag {
			w, err := br.ReadSE()
			if err != nil {
				return nil, err
			}
			o, err := br.ReadSE()
			if err != nil {
				return nil, err
			}
			pw.luma[i] = weightOffset{weight: int(w), offset: int(o), explicit: true}
		}
		if chromaFormatIDC != 0 {
			chromaFlag, err := br.ReadBool()
			if err != nil {
				return nil, err
			}
			if chromaFlag {
				for j := 0; j < 2; j++ {
					w, err := br.ReadSE()
					if err != nil {
						return nil, err
					}
					o, err := br.ReadSE()
					if err != nil {
						return nil, err
					}
					pw.chroma[i][j] = weightOffset{weight: int(w), offset: int(o), explicit: true}
				}
			}
		}
	}
	return pw, nil
}

// i16x16 decodes mb_type for I_16x16 macroblocks.
// mb_type 1..24 encodes: mode (0-3), cbp_chroma (0-2), cbp_luma (0 or 15).
func i16x16Params(mbType int) (predMode, cbpChroma, cbpLuma int) {
	mbType-- // 1-based → 0-based
	predMode = mbType % 4
	cbpChroma = (mbType / 4) % 3
	cbpLuma = 0
	if mbType >= 12 {
		cbpLuma = 15
	}
	return
}

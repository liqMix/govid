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
	sliceType            uint32
	ppsID                uint32
	frameNum             uint32
	idrPicID             uint32
	picOrderCntLsb       uint32
	deltaPicOrderCntBottom int32
	deltaPicOrderCnt     [2]int32
	redundantPicCnt      uint32
	directSpatialMvPred  bool
	numRefIdxL0Active    uint32
	numRefIdxL1Active    uint32
	sliceQPDelta         int32
	disableDeblockingFilter int32
	sliceAlphaC0Offset   int32
	sliceBetaOffset      int32
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

	// P-slice specific.
	if sh.sliceType == sliceTypeP || sh.sliceType == sliceTypeSP {
		numRefOverride, err := br.ReadBool()
		if err != nil {
			return nil, err
		}
		sh.numRefIdxL0Active = pps.NumRefIdxL0DefaultActive
		if numRefOverride {
			l0, err := br.ReadUE()
			if err != nil {
				return nil, err
			}
			sh.numRefIdxL0Active = l0 + 1
		}
	}

	// ref_pic_list_modification
	if sh.sliceType != sliceTypeI && sh.sliceType != sliceTypeSI {
		if err := skipRefPicListModification(br); err != nil {
			return nil, err
		}
	}

	// pred_weight_table (only for P/SP slices with weighted prediction).
	if (sh.sliceType == sliceTypeP || sh.sliceType == sliceTypeSP) && pps.WeightedPredFlag {
		if err := skipPredWeightTable(br, sh.numRefIdxL0Active, sps.ChromaFormatIDC); err != nil {
			return nil, fmt.Errorf("pred_weight_table: %w", err)
		}
	}

	// dec_ref_pic_marking
	if nalType == NALSliceIDR || nalRefIDC > 0 {
		if err := skipDecRefPicMarking(br, nalType); err != nil {
			return nil, err
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

func skipRefPicListModification(br *BitReader) error {
	flag, err := br.ReadBool()
	if err != nil {
		return err
	}
	if flag {
		for {
			op, err := br.ReadUE()
			if err != nil {
				return err
			}
			if op == 3 {
				break
			}
			_, err = br.ReadUE() // abs_diff
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func skipDecRefPicMarking(br *BitReader, nalType uint8) error {
	if nalType == NALSliceIDR {
		_, err := br.ReadBool() // no_output_of_prior_pics_flag
		if err != nil {
			return err
		}
		_, err = br.ReadBool() // long_term_reference_flag
		return err
	}
	flag, err := br.ReadBool() // adaptive_ref_pic_marking_mode_flag
	if err != nil {
		return err
	}
	if flag {
		for {
			op, err := br.ReadUE()
			if err != nil {
				return err
			}
			if op == 0 {
				break
			}
			switch op {
			case 1, 2, 4, 6:
				_, err = br.ReadUE()
			case 3:
				_, err = br.ReadUE()
				if err != nil {
					return err
				}
				_, err = br.ReadUE()
			case 5:
				// No parameters.
			default:
				return fmt.Errorf("unknown MMCO operation %d", op)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// skipPredWeightTable skips the pred_weight_table() syntax structure.
func skipPredWeightTable(br *BitReader, numRefIdxL0Active uint32, chromaFormatIDC uint32) error {
	// luma_log2_weight_denom
	_, err := br.ReadUE()
	if err != nil {
		return err
	}
	// chroma_log2_weight_denom
	if chromaFormatIDC != 0 {
		_, err = br.ReadUE()
		if err != nil {
			return err
		}
	}
	for i := uint32(0); i < numRefIdxL0Active; i++ {
		lumaFlag, err := br.ReadBool()
		if err != nil {
			return err
		}
		if lumaFlag {
			_, err = br.ReadSE() // luma_weight
			if err != nil {
				return err
			}
			_, err = br.ReadSE() // luma_offset
			if err != nil {
				return err
			}
		}
		if chromaFormatIDC != 0 {
			chromaFlag, err := br.ReadBool()
			if err != nil {
				return err
			}
			if chromaFlag {
				for j := 0; j < 2; j++ {
					_, err = br.ReadSE() // chroma_weight
					if err != nil {
						return err
					}
					_, err = br.ReadSE() // chroma_offset
					if err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
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

package h264

import "fmt"

// SPS represents a Sequence Parameter Set.
type SPS struct {
	ProfileIDC                  uint8
	ConstraintFlags             uint8
	LevelIDC                    uint8
	ID                          uint32
	ChromaFormatIDC             uint32
	SeparateColourPlane         bool
	BitDepthLuma                uint32
	BitDepthChroma              uint32
	QpprimeYZeroTransformBypass bool
	SeqScalingMatrixPresent     bool
	ScalingLists                [12]scalingListEntry
	Log2MaxFrameNum             uint32
	PicOrderCntType             uint32
	Log2MaxPicOrderCntLsb       uint32
	DeltaPicOrderAlwaysZero     bool
	OffsetForNonRefPic          int32
	OffsetForTopToBottom        int32
	NumRefFramesInPOCCycle      uint32
	OffsetForRefFrame           []int32
	MaxNumRefFrames             uint32
	GapsInFrameNumAllowed       bool
	PicWidthInMBs               uint32
	PicHeightInMapUnits         uint32
	FrameMBSOnly                bool
	MBAdaptiveFrameField        bool
	Direct8x8Inference          bool
	FrameCropping               bool
	CropLeft                    uint32
	CropRight                   uint32
	CropTop                     uint32
	CropBottom                  uint32
	VUIPresent                  bool
	// MaxNumReorderFrames from the VUI bitstream restriction, or -1 when the
	// stream does not declare it. Sizes the display reorder buffer for
	// B-frame streams.
	MaxNumReorderFrames int

	// Derived dimensions.
	Width  int
	Height int
}

// ParseSPS parses an SPS from RBSP data (after NAL header removal).
func ParseSPS(rbsp []byte) (*SPS, error) {
	br := NewBitReader(rbsp)
	s := &SPS{}

	var err error
	v, err := br.ReadBits(8)
	if err != nil {
		return nil, fmt.Errorf("sps: profile_idc: %w", err)
	}
	s.ProfileIDC = uint8(v)

	v, err = br.ReadBits(8)
	if err != nil {
		return nil, fmt.Errorf("sps: constraint_flags: %w", err)
	}
	s.ConstraintFlags = uint8(v)

	v, err = br.ReadBits(8)
	if err != nil {
		return nil, fmt.Errorf("sps: level_idc: %w", err)
	}
	s.LevelIDC = uint8(v)

	s.ID, err = br.ReadUE()
	if err != nil {
		return nil, fmt.Errorf("sps: seq_parameter_set_id: %w", err)
	}

	// High profiles have additional chroma/scaling info.
	if s.ProfileIDC == 100 || s.ProfileIDC == 110 || s.ProfileIDC == 122 ||
		s.ProfileIDC == 244 || s.ProfileIDC == 44 || s.ProfileIDC == 83 ||
		s.ProfileIDC == 86 || s.ProfileIDC == 118 || s.ProfileIDC == 128 ||
		s.ProfileIDC == 138 || s.ProfileIDC == 139 || s.ProfileIDC == 134 || s.ProfileIDC == 135 {
		s.ChromaFormatIDC, err = br.ReadUE()
		if err != nil {
			return nil, fmt.Errorf("sps: chroma_format_idc: %w", err)
		}
		if s.ChromaFormatIDC == 3 {
			s.SeparateColourPlane, err = br.ReadBool()
			if err != nil {
				return nil, err
			}
		}
		bdl, err := br.ReadUE()
		if err != nil {
			return nil, fmt.Errorf("sps: bit_depth_luma: %w", err)
		}
		s.BitDepthLuma = bdl + 8

		bdc, err := br.ReadUE()
		if err != nil {
			return nil, fmt.Errorf("sps: bit_depth_chroma: %w", err)
		}
		s.BitDepthChroma = bdc + 8

		s.QpprimeYZeroTransformBypass, err = br.ReadBool()
		if err != nil {
			return nil, err
		}

		s.SeqScalingMatrixPresent, err = br.ReadBool()
		if err != nil {
			return nil, err
		}
		if s.SeqScalingMatrixPresent {
			nScalingList := 8
			if s.ChromaFormatIDC == 3 {
				nScalingList = 12
			}
			for i := 0; i < nScalingList; i++ {
				present, err := br.ReadBool()
				if err != nil {
					return nil, err
				}
				if present {
					size := 16
					if i >= 6 {
						size = 64
					}
					if err := parseScalingList(br, &s.ScalingLists[i], size); err != nil {
						return nil, err
					}
				}
			}
		}
	} else {
		s.ChromaFormatIDC = 1
		s.BitDepthLuma = 8
		s.BitDepthChroma = 8
	}

	lmf, err := br.ReadUE()
	if err != nil {
		return nil, fmt.Errorf("sps: log2_max_frame_num: %w", err)
	}
	s.Log2MaxFrameNum = lmf + 4

	s.PicOrderCntType, err = br.ReadUE()
	if err != nil {
		return nil, fmt.Errorf("sps: pic_order_cnt_type: %w", err)
	}

	switch s.PicOrderCntType {
	case 0:
		lmpocl, err := br.ReadUE()
		if err != nil {
			return nil, fmt.Errorf("sps: log2_max_pic_order_cnt_lsb: %w", err)
		}
		s.Log2MaxPicOrderCntLsb = lmpocl + 4
	case 1:
		s.DeltaPicOrderAlwaysZero, err = br.ReadBool()
		if err != nil {
			return nil, err
		}
		s.OffsetForNonRefPic, err = br.ReadSE()
		if err != nil {
			return nil, err
		}
		s.OffsetForTopToBottom, err = br.ReadSE()
		if err != nil {
			return nil, err
		}
		s.NumRefFramesInPOCCycle, err = br.ReadUE()
		if err != nil {
			return nil, err
		}
		s.OffsetForRefFrame = make([]int32, s.NumRefFramesInPOCCycle)
		for i := range s.OffsetForRefFrame {
			s.OffsetForRefFrame[i], err = br.ReadSE()
			if err != nil {
				return nil, err
			}
		}
	case 2:
		// No additional data.
	}

	s.MaxNumRefFrames, err = br.ReadUE()
	if err != nil {
		return nil, fmt.Errorf("sps: max_num_ref_frames: %w", err)
	}

	s.GapsInFrameNumAllowed, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	s.PicWidthInMBs, err = br.ReadUE()
	if err != nil {
		return nil, fmt.Errorf("sps: pic_width_in_mbs: %w", err)
	}
	s.PicWidthInMBs++

	s.PicHeightInMapUnits, err = br.ReadUE()
	if err != nil {
		return nil, fmt.Errorf("sps: pic_height_in_map_units: %w", err)
	}
	s.PicHeightInMapUnits++

	s.FrameMBSOnly, err = br.ReadBool()
	if err != nil {
		return nil, err
	}
	if !s.FrameMBSOnly {
		s.MBAdaptiveFrameField, err = br.ReadBool()
		if err != nil {
			return nil, err
		}
	}

	s.Direct8x8Inference, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	s.FrameCropping, err = br.ReadBool()
	if err != nil {
		return nil, err
	}
	if s.FrameCropping {
		s.CropLeft, err = br.ReadUE()
		if err != nil {
			return nil, err
		}
		s.CropRight, err = br.ReadUE()
		if err != nil {
			return nil, err
		}
		s.CropTop, err = br.ReadUE()
		if err != nil {
			return nil, err
		}
		s.CropBottom, err = br.ReadUE()
		if err != nil {
			return nil, err
		}
	}

	s.VUIPresent, err = br.ReadBool()
	if err != nil {
		return nil, err
	}
	s.MaxNumReorderFrames = -1 // unknown until VUI bitstream restriction says
	if s.VUIPresent {
		// Parse VUI far enough to reach max_num_reorder_frames (needed to
		// size the display reorder buffer for B-frame streams). Errors here
		// are non-fatal: leave MaxNumReorderFrames at -1.
		if v, err := parseVUIReorderFrames(br); err == nil {
			s.MaxNumReorderFrames = v
		}
	}

	// Derive pixel dimensions.
	cropUnitX := uint32(1)
	cropUnitY := uint32(2 - boolToUint32(s.FrameMBSOnly))
	if s.ChromaFormatIDC == 1 {
		cropUnitX = 2
		cropUnitY = 2 * (2 - boolToUint32(s.FrameMBSOnly))
	} else if s.ChromaFormatIDC == 2 {
		cropUnitX = 2
		cropUnitY = 2 - boolToUint32(s.FrameMBSOnly)
	}

	s.Width = int(s.PicWidthInMBs*16 - cropUnitX*(s.CropLeft+s.CropRight))
	picHeight := s.PicHeightInMapUnits * 16
	if !s.FrameMBSOnly {
		picHeight *= 2
	}
	s.Height = int(picHeight - cropUnitY*(s.CropTop+s.CropBottom))

	return s, nil
}

// parseVUIReorderFrames walks vui_parameters (spec E.1.1) up to
// max_num_reorder_frames in the bitstream_restriction section. Returns -1 if
// the restriction section is absent.
func parseVUIReorderFrames(br *BitReader) (int, error) {
	if flag, err := br.ReadBool(); err != nil {
		return -1, err
	} else if flag { // aspect_ratio_info_present_flag
		idc, err := br.ReadBits(8)
		if err != nil {
			return -1, err
		}
		if idc == 255 { // Extended_SAR
			if _, err := br.ReadBits(32); err != nil {
				return -1, err
			}
		}
	}
	if flag, err := br.ReadBool(); err != nil {
		return -1, err
	} else if flag { // overscan_info_present_flag
		if _, err := br.ReadBool(); err != nil {
			return -1, err
		}
	}
	if flag, err := br.ReadBool(); err != nil {
		return -1, err
	} else if flag { // video_signal_type_present_flag
		if _, err := br.ReadBits(4); err != nil { // format(3) + full_range(1)
			return -1, err
		}
		if cd, err := br.ReadBool(); err != nil {
			return -1, err
		} else if cd { // colour_description_present_flag
			if _, err := br.ReadBits(24); err != nil {
				return -1, err
			}
		}
	}
	if flag, err := br.ReadBool(); err != nil {
		return -1, err
	} else if flag { // chroma_loc_info_present_flag
		if _, err := br.ReadUE(); err != nil {
			return -1, err
		}
		if _, err := br.ReadUE(); err != nil {
			return -1, err
		}
	}
	if flag, err := br.ReadBool(); err != nil {
		return -1, err
	} else if flag { // timing_info_present_flag
		if _, err := br.ReadBits(32); err != nil {
			return -1, err
		}
		if _, err := br.ReadBits(32); err != nil {
			return -1, err
		}
		if _, err := br.ReadBool(); err != nil {
			return -1, err
		}
	}
	skipHRD := func() error {
		cpbCnt, err := br.ReadUE()
		if err != nil {
			return err
		}
		if _, err := br.ReadBits(8); err != nil { // bit_rate_scale + cpb_size_scale
			return err
		}
		for i := uint32(0); i <= cpbCnt; i++ {
			if _, err := br.ReadUE(); err != nil {
				return err
			}
			if _, err := br.ReadUE(); err != nil {
				return err
			}
			if _, err := br.ReadBool(); err != nil {
				return err
			}
		}
		_, err = br.ReadBits(20) // 4 x 5-bit delay lengths
		return err
	}
	nalHRD, err := br.ReadBool()
	if err != nil {
		return -1, err
	}
	if nalHRD {
		if err := skipHRD(); err != nil {
			return -1, err
		}
	}
	vclHRD, err := br.ReadBool()
	if err != nil {
		return -1, err
	}
	if vclHRD {
		if err := skipHRD(); err != nil {
			return -1, err
		}
	}
	if nalHRD || vclHRD {
		if _, err := br.ReadBool(); err != nil { // low_delay_hrd_flag
			return -1, err
		}
	}
	if _, err := br.ReadBool(); err != nil { // pic_struct_present_flag
		return -1, err
	}
	restriction, err := br.ReadBool()
	if err != nil || !restriction {
		return -1, err
	}
	if _, err := br.ReadBool(); err != nil { // motion_vectors_over_pic_boundaries
		return -1, err
	}
	for i := 0; i < 4; i++ { // max_bytes/bits denom, log2 mv lengths
		if _, err := br.ReadUE(); err != nil {
			return -1, err
		}
	}
	reorder, err := br.ReadUE()
	if err != nil {
		return -1, err
	}
	return int(reorder), nil
}

func boolToUint32(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

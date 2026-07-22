package h264

import "fmt"

// PPS represents a Picture Parameter Set.
type PPS struct {
	ID                             uint32
	SPSID                          uint32
	EntropyCodingModeFlag          bool // false = CAVLC, true = CABAC
	BottomFieldPicOrderInFrame     bool
	NumSliceGroupsMinus1           uint32
	NumRefIdxL0DefaultActive       uint32
	NumRefIdxL1DefaultActive       uint32
	WeightedPredFlag               bool
	WeightedBipredIDC              uint32
	PicInitQPMinus26               int32
	PicInitQSMinus26               int32
	ChromaQPIndexOffset            int32
	DeblockingFilterControlPresent bool
	ConstrainedIntraPred           bool
	RedundantPicCntPresent         bool
	Transform8x8Mode               bool
	PicScalingMatrixPresent        bool
	SecondChromaQPIndexOffset      int32
}

// ParsePPS parses a PPS from RBSP data (after NAL header removal).
func ParsePPS(rbsp []byte) (*PPS, error) {
	br := NewBitReader(rbsp)
	p := &PPS{}
	var err error

	p.ID, err = br.ReadUE()
	if err != nil {
		return nil, fmt.Errorf("pps: pic_parameter_set_id: %w", err)
	}

	p.SPSID, err = br.ReadUE()
	if err != nil {
		return nil, fmt.Errorf("pps: seq_parameter_set_id: %w", err)
	}

	p.EntropyCodingModeFlag, err = br.ReadBool()
	if err != nil {
		return nil, fmt.Errorf("pps: entropy_coding_mode_flag: %w", err)
	}

	p.BottomFieldPicOrderInFrame, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	p.NumSliceGroupsMinus1, err = br.ReadUE()
	if err != nil {
		return nil, err
	}
	if p.NumSliceGroupsMinus1 > 0 {
		return nil, fmt.Errorf("pps: slice groups not supported (num_slice_groups=%d)", p.NumSliceGroupsMinus1+1)
	}

	l0, err := br.ReadUE()
	if err != nil {
		return nil, err
	}
	p.NumRefIdxL0DefaultActive = l0 + 1

	l1, err := br.ReadUE()
	if err != nil {
		return nil, err
	}
	p.NumRefIdxL1DefaultActive = l1 + 1

	p.WeightedPredFlag, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	v, err := br.ReadBits(2)
	if err != nil {
		return nil, err
	}
	p.WeightedBipredIDC = v

	p.PicInitQPMinus26, err = br.ReadSE()
	if err != nil {
		return nil, err
	}

	p.PicInitQSMinus26, err = br.ReadSE()
	if err != nil {
		return nil, err
	}

	p.ChromaQPIndexOffset, err = br.ReadSE()
	if err != nil {
		return nil, err
	}

	p.DeblockingFilterControlPresent, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	p.ConstrainedIntraPred, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	p.RedundantPicCntPresent, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	// Optional fields: transform_8x8_mode, scaling matrix, second chroma QP offset.
	if br.MoreRBSPData() {
		p.Transform8x8Mode, err = br.ReadBool()
		if err != nil {
			return nil, err
		}

		p.PicScalingMatrixPresent, err = br.ReadBool()
		if err != nil {
			return nil, err
		}
		if p.PicScalingMatrixPresent {
			nScalingList := 6
			if p.Transform8x8Mode {
				nScalingList = 10
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
					if err := skipScalingList(br, size); err != nil {
						return nil, err
					}
				}
			}
		}

		p.SecondChromaQPIndexOffset, err = br.ReadSE()
		if err != nil {
			return nil, err
		}
	} else {
		p.SecondChromaQPIndexOffset = p.ChromaQPIndexOffset
	}

	return p, nil
}

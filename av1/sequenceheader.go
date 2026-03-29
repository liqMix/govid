package av1

import "fmt"

// Color primaries constants (AV1 spec 6.4.2).
const (
	cpBT709      = 1
	cpUnspecified = 2
)

// Transfer characteristics constants.
const (
	tcSRGB        = 13
	tcUnspecified = 2
)

// Matrix coefficients constants.
const (
	mcIdentity    = 0
	mcUnspecified = 2
)

// Screen content tools constants.
const (
	selectScreenContentTools = 2
	selectIntegerMV          = 2
)

// ColorConfig holds color format information from the sequence header.
type ColorConfig struct {
	BitDepth               int
	MonoChrome             bool
	NumPlanes              int
	ColorPrimaries         int
	TransferCharacteristics int
	MatrixCoefficients     int
	ColorRange             bool // false = studio range, true = full range
	SubsamplingX           int
	SubsamplingY           int
	ChromaSamplePosition   int
	SeparateUVDelta        bool
}

// SequenceHeader contains the parsed AV1 sequence header OBU.
// AV1 spec 5.5.
type SequenceHeader struct {
	Profile                   int
	StillPicture              bool
	ReducedStillPictureHeader bool

	TimingInfoPresent       bool
	DecoderModelInfoPresent bool

	OperatingPointsCntMinus1 int
	OperatingPointIDC        [32]uint16
	SeqLevelIdx              [32]int
	SeqTier                  [32]int

	MaxFrameWidthMinus1  uint32
	MaxFrameHeightMinus1 uint32
	MaxFrameWidth        int
	MaxFrameHeight       int

	FrameIDNumbersPresent         bool
	DeltaFrameIDLengthMinus2      int
	AdditionalFrameIDLengthMinus1 int

	Use128x128Superblock bool
	SuperblockSize       int // 64 or 128

	EnableFilterIntra        bool
	EnableIntraEdgeFilter    bool
	EnableInterIntraCompound bool
	EnableMaskedCompound     bool
	EnableWarpedMotion       bool
	EnableDualFilter         bool
	EnableOrderHint          bool
	EnableJNTComp            bool
	EnableRefFrameMVS        bool
	EnableSuperRes           bool
	EnableCDEF               bool
	EnableRestoration        bool

	SeqForceScreenContentTools int
	SeqForceIntegerMV          int

	OrderHintBits int

	Color ColorConfig

	FilmGrainParamsPresent bool

	// Decoder model info (needed to skip fields during frame header parsing).
	bufferDelayLengthMinus1 int
}

// ParseSequenceHeader parses a sequence header OBU payload.
func ParseSequenceHeader(data []byte) (*SequenceHeader, error) {
	br := NewBitReader(data)
	sh := &SequenceHeader{}

	v, err := br.ReadBits(3)
	if err != nil {
		return nil, fmt.Errorf("seq_profile: %w", err)
	}
	sh.Profile = int(v)

	sh.StillPicture, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	sh.ReducedStillPictureHeader, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	if sh.ReducedStillPictureHeader {
		sh.OperatingPointsCntMinus1 = 0
		sh.OperatingPointIDC[0] = 0
		v, err = br.ReadBits(5)
		if err != nil {
			return nil, err
		}
		sh.SeqLevelIdx[0] = int(v)
		sh.SeqTier[0] = 0
		sh.TimingInfoPresent = false
		sh.DecoderModelInfoPresent = false
	} else {
		sh.TimingInfoPresent, err = br.ReadBool()
		if err != nil {
			return nil, err
		}
		if sh.TimingInfoPresent {
			if err := skipTimingInfo(br); err != nil {
				return nil, fmt.Errorf("timing_info: %w", err)
			}
			sh.DecoderModelInfoPresent, err = br.ReadBool()
			if err != nil {
				return nil, err
			}
			if sh.DecoderModelInfoPresent {
				bdl, err := parseDecoderModelInfo(br)
				if err != nil {
					return nil, fmt.Errorf("decoder_model_info: %w", err)
				}
				sh.bufferDelayLengthMinus1 = bdl
			}
		}

		// initial_display_delay_present_flag
		initialDisplayDelayPresent, err := br.ReadBool()
		if err != nil {
			return nil, err
		}

		v, err = br.ReadBits(5)
		if err != nil {
			return nil, err
		}
		sh.OperatingPointsCntMinus1 = int(v)

		for i := 0; i <= sh.OperatingPointsCntMinus1; i++ {
			v, err = br.ReadBits(12)
			if err != nil {
				return nil, err
			}
			sh.OperatingPointIDC[i] = uint16(v)

			v, err = br.ReadBits(5)
			if err != nil {
				return nil, err
			}
			sh.SeqLevelIdx[i] = int(v)

			if sh.SeqLevelIdx[i] > 7 {
				v, err = br.ReadBits(1)
				if err != nil {
					return nil, err
				}
				sh.SeqTier[i] = int(v)
			}

			if sh.DecoderModelInfoPresent {
				present, err := br.ReadBool()
				if err != nil {
					return nil, err
				}
				if present {
					if err := skipOperatingParametersInfo(br, sh.bufferDelayLengthMinus1); err != nil {
						return nil, fmt.Errorf("operating_parameters_info: %w", err)
					}
				}
			}

			if initialDisplayDelayPresent {
				present, err := br.ReadBool()
				if err != nil {
					return nil, err
				}
				if present {
					_, err = br.ReadBits(4) // initial_display_delay_minus_1
					if err != nil {
						return nil, err
					}
				}
			}
		}
	}

	// frame_width_bits_minus_1, frame_height_bits_minus_1
	fwBits, err := br.ReadBits(4)
	if err != nil {
		return nil, err
	}
	fhBits, err := br.ReadBits(4)
	if err != nil {
		return nil, err
	}

	sh.MaxFrameWidthMinus1, err = br.ReadBits(uint(fwBits + 1))
	if err != nil {
		return nil, err
	}
	sh.MaxFrameHeightMinus1, err = br.ReadBits(uint(fhBits + 1))
	if err != nil {
		return nil, err
	}
	sh.MaxFrameWidth = int(sh.MaxFrameWidthMinus1) + 1
	sh.MaxFrameHeight = int(sh.MaxFrameHeightMinus1) + 1

	if !sh.ReducedStillPictureHeader {
		sh.FrameIDNumbersPresent, err = br.ReadBool()
		if err != nil {
			return nil, err
		}
	}
	if sh.FrameIDNumbersPresent {
		v, err = br.ReadBits(4)
		if err != nil {
			return nil, err
		}
		sh.DeltaFrameIDLengthMinus2 = int(v)

		v, err = br.ReadBits(3)
		if err != nil {
			return nil, err
		}
		sh.AdditionalFrameIDLengthMinus1 = int(v)
	}

	// use_128x128_superblock
	sb, err := br.ReadBool()
	if err != nil {
		return nil, err
	}
	sh.Use128x128Superblock = sb
	if sb {
		sh.SuperblockSize = 128
	} else {
		sh.SuperblockSize = 64
	}

	sh.EnableFilterIntra, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	sh.EnableIntraEdgeFilter, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	if sh.ReducedStillPictureHeader {
		sh.EnableInterIntraCompound = false
		sh.EnableMaskedCompound = false
		sh.EnableWarpedMotion = false
		sh.EnableDualFilter = false
		sh.EnableOrderHint = false
		sh.EnableJNTComp = false
		sh.EnableRefFrameMVS = false
		sh.SeqForceScreenContentTools = selectScreenContentTools
		sh.SeqForceIntegerMV = selectIntegerMV
		sh.OrderHintBits = 0
	} else {
		sh.EnableInterIntraCompound, err = br.ReadBool()
		if err != nil {
			return nil, err
		}
		sh.EnableMaskedCompound, err = br.ReadBool()
		if err != nil {
			return nil, err
		}
		sh.EnableWarpedMotion, err = br.ReadBool()
		if err != nil {
			return nil, err
		}
		sh.EnableDualFilter, err = br.ReadBool()
		if err != nil {
			return nil, err
		}
		sh.EnableOrderHint, err = br.ReadBool()
		if err != nil {
			return nil, err
		}

		if sh.EnableOrderHint {
			sh.EnableJNTComp, err = br.ReadBool()
			if err != nil {
				return nil, err
			}
			sh.EnableRefFrameMVS, err = br.ReadBool()
			if err != nil {
				return nil, err
			}
		}

		// seq_choose_screen_content_tools
		choose, err := br.ReadBool()
		if err != nil {
			return nil, err
		}
		if choose {
			sh.SeqForceScreenContentTools = selectScreenContentTools
		} else {
			v, err := br.ReadBits(1)
			if err != nil {
				return nil, err
			}
			sh.SeqForceScreenContentTools = int(v)
		}

		if sh.SeqForceScreenContentTools > 0 {
			// seq_choose_integer_mv
			choose, err := br.ReadBool()
			if err != nil {
				return nil, err
			}
			if choose {
				sh.SeqForceIntegerMV = selectIntegerMV
			} else {
				v, err := br.ReadBits(1)
				if err != nil {
					return nil, err
				}
				sh.SeqForceIntegerMV = int(v)
			}
		} else {
			sh.SeqForceIntegerMV = selectIntegerMV
		}

		if sh.EnableOrderHint {
			v, err := br.ReadBits(3)
			if err != nil {
				return nil, err
			}
			sh.OrderHintBits = int(v) + 1
		} else {
			sh.OrderHintBits = 0
		}
	}

	sh.EnableSuperRes, err = br.ReadBool()
	if err != nil {
		return nil, err
	}
	sh.EnableCDEF, err = br.ReadBool()
	if err != nil {
		return nil, err
	}
	sh.EnableRestoration, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	if err := parseColorConfig(br, sh); err != nil {
		return nil, fmt.Errorf("color_config: %w", err)
	}

	sh.FilmGrainParamsPresent, err = br.ReadBool()
	if err != nil {
		return nil, err
	}

	return sh, nil
}

// skipTimingInfo skips the timing_info() syntax element.
// AV1 spec 5.5.3.
func skipTimingInfo(br *BitReader) error {
	_, err := br.ReadBits(32) // num_units_in_display_tick
	if err != nil {
		return err
	}
	_, err = br.ReadBits(32) // time_scale
	if err != nil {
		return err
	}
	equalPictureInterval, err := br.ReadBool()
	if err != nil {
		return err
	}
	if equalPictureInterval {
		_, err = br.ReadUVLC() // num_ticks_per_picture_minus_1
		if err != nil {
			return err
		}
	}
	return nil
}

// parseDecoderModelInfo parses decoder_model_info() and returns buffer_delay_length_minus_1.
// AV1 spec 5.5.4.
func parseDecoderModelInfo(br *BitReader) (int, error) {
	v, err := br.ReadBits(5) // buffer_delay_length_minus_1
	if err != nil {
		return 0, err
	}
	bufferDelayLengthMinus1 := int(v)

	_, err = br.ReadBits(32) // num_units_in_decoding_tick
	if err != nil {
		return 0, err
	}
	_, err = br.ReadBits(5) // buffer_removal_time_length_minus_1
	if err != nil {
		return 0, err
	}
	_, err = br.ReadBits(5) // frame_presentation_time_length_minus_1
	if err != nil {
		return 0, err
	}
	return bufferDelayLengthMinus1, nil
}

// skipOperatingParametersInfo skips operating_parameters_info().
// AV1 spec 5.5.5.
func skipOperatingParametersInfo(br *BitReader, bufferDelayLengthMinus1 int) error {
	n := uint(bufferDelayLengthMinus1 + 1)
	_, err := br.ReadBits(n) // decoder_buffer_delay
	if err != nil {
		return err
	}
	_, err = br.ReadBits(n) // encoder_buffer_delay
	if err != nil {
		return err
	}
	_, err = br.ReadBits(1) // low_delay_mode_flag
	return err
}

// parseColorConfig parses the color_config() syntax element.
// AV1 spec 5.5.2.
func parseColorConfig(br *BitReader, sh *SequenceHeader) error {
	cc := &sh.Color

	highBitDepth, err := br.ReadBool()
	if err != nil {
		return err
	}

	if sh.Profile == 2 && highBitDepth {
		twelveBit, err := br.ReadBool()
		if err != nil {
			return err
		}
		if twelveBit {
			cc.BitDepth = 12
		} else {
			cc.BitDepth = 10
		}
	} else if highBitDepth {
		cc.BitDepth = 10
	} else {
		cc.BitDepth = 8
	}

	if sh.Profile == 1 {
		cc.MonoChrome = false
	} else {
		cc.MonoChrome, err = br.ReadBool()
		if err != nil {
			return err
		}
	}

	if cc.MonoChrome {
		cc.NumPlanes = 1
	} else {
		cc.NumPlanes = 3
	}

	colorDescPresent, err := br.ReadBool()
	if err != nil {
		return err
	}
	if colorDescPresent {
		v, err := br.ReadBits(8)
		if err != nil {
			return err
		}
		cc.ColorPrimaries = int(v)

		v, err = br.ReadBits(8)
		if err != nil {
			return err
		}
		cc.TransferCharacteristics = int(v)

		v, err = br.ReadBits(8)
		if err != nil {
			return err
		}
		cc.MatrixCoefficients = int(v)
	} else {
		cc.ColorPrimaries = cpUnspecified
		cc.TransferCharacteristics = tcUnspecified
		cc.MatrixCoefficients = mcUnspecified
	}

	if cc.MonoChrome {
		cc.ColorRange, err = br.ReadBool()
		if err != nil {
			return err
		}
		cc.SubsamplingX = 1
		cc.SubsamplingY = 1
		cc.ChromaSamplePosition = 0
		cc.SeparateUVDelta = false
		return nil
	}

	if cc.ColorPrimaries == cpBT709 &&
		cc.TransferCharacteristics == tcSRGB &&
		cc.MatrixCoefficients == mcIdentity {
		cc.ColorRange = true
		cc.SubsamplingX = 0
		cc.SubsamplingY = 0
	} else {
		cc.ColorRange, err = br.ReadBool()
		if err != nil {
			return err
		}

		if sh.Profile == 0 {
			cc.SubsamplingX = 1
			cc.SubsamplingY = 1
		} else if sh.Profile == 1 {
			cc.SubsamplingX = 0
			cc.SubsamplingY = 0
		} else {
			if cc.BitDepth == 12 {
				v, err := br.ReadBits(1)
				if err != nil {
					return err
				}
				cc.SubsamplingX = int(v)
				if cc.SubsamplingX == 1 {
					v, err = br.ReadBits(1)
					if err != nil {
						return err
					}
					cc.SubsamplingY = int(v)
				}
			} else {
				cc.SubsamplingX = 1
				cc.SubsamplingY = 0
			}
		}

		if cc.SubsamplingX == 1 && cc.SubsamplingY == 1 {
			v, err := br.ReadBits(2)
			if err != nil {
				return err
			}
			cc.ChromaSamplePosition = int(v)
		}
	}

	cc.SeparateUVDelta, err = br.ReadBool()
	if err != nil {
		return err
	}

	return nil
}

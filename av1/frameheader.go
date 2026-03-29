package av1

import "fmt"

// Frame type constants (AV1 spec 6.8.2).
const (
	FrameTypeKeyFrame    = 0
	FrameTypeInterFrame  = 1
	FrameTypeIntraOnly   = 2
	FrameTypeSwitchFrame = 3
)

// Number of reference frame slots.
const numRefFrames = 8

// Number of reference frames each inter frame can use.
const refsPerFrame = 7

// Interpolation filter constants.
const (
	interpFilterEightTapRegular = 0
	interpFilterEightTapSmooth  = 1
	interpFilterEightTapSharp   = 2
	interpFilterBilinear        = 3
	interpFilterSwitchable      = 4
)

// TX mode constants.
const (
	txModeOnly4x4 = 0
	txModeLargest = 1
	txModeSelect  = 2
)

// Maximum number of segments.
const maxSegments = 8

// Segment feature constants.
const segLvlMax = 8

// Restoration type constants.
const (
	restoreNone    = 0
	restoreWiener  = 1
	restoreSgrproj = 2
	restoreSwitchable = 3
)

// TileInfo contains tile configuration.
type TileInfo struct {
	UniformTileSpacing bool
	TileCols           int
	TileRows           int
	TileColsLog2       int
	TileRowsLog2       int
	TileWidthSB        []int // width in superblocks for each column
	TileHeightSB       []int // height in superblocks for each row
	ContextUpdateTileID int
	TileSizeBytes       int
}

// QuantizationParams contains frame quantization parameters.
type QuantizationParams struct {
	BaseQIdx   int
	DeltaQYDc  int
	DeltaQUDc  int
	DeltaQUAc  int
	DeltaQVDc  int
	DeltaQVAc  int
	UsingQMatrix bool
	QMY        int
	QMU        int
	QMV        int
}

// SegmentationParams contains segmentation parameters.
type SegmentationParams struct {
	Enabled        bool
	UpdateMap      bool
	TemporalUpdate bool
	UpdateData     bool
	FeatureEnabled [maxSegments][segLvlMax]bool
	FeatureData    [maxSegments][segLvlMax]int
}

// LoopFilterParams contains loop filter parameters.
type LoopFilterParams struct {
	FilterLevel    [4]int // [0]=Y vertical, [1]=Y horizontal, [2]=U, [3]=V
	SharpnessLevel int
	DeltaEnabled   bool
	DeltaUpdate    bool
	RefDeltas      [numRefFrames]int
	ModeDeltas     [2]int
}

// CDEFParams contains CDEF parameters.
type CDEFParams struct {
	DampingMinus3 int
	Bits          int
	YPriStrength  []int
	YSecStrength  []int
	UVPriStrength []int
	UVSecStrength []int
}

// LRParams contains loop restoration parameters per plane.
type LRParams struct {
	Type     [3]int // restoration type per plane
	UnitSize [3]int // restoration unit size per plane
}

// GlobalMotionParams contains global motion parameters.
type GlobalMotionParams struct {
	Type [numRefFrames]int // motion type per reference frame
	// Warp parameters stored for future phases.
}

// FrameHeader contains the parsed AV1 frame header.
// AV1 spec 5.9.
type FrameHeader struct {
	ShowExistingFrame    bool
	FrameToShowMapIdx    int
	FrameType            int
	FrameIsIntra         bool
	ShowFrame            bool
	ShowableFrame        bool
	ErrorResilientMode   bool
	DisableCDFUpdate     bool
	AllowScreenContentTools bool
	ForceIntegerMV       bool
	CurrentFrameID       uint32
	FrameSizeOverrideFlag bool
	OrderHint            int

	FrameWidth           int
	FrameHeight          int
	RenderWidth          int
	RenderHeight         int
	UpscaledWidth        int

	AllowIntraBC         bool
	AllowHighPrecisionMV bool
	InterpFilter         int
	IsMotionModeSwitchable bool
	UseRefFrameMVS       bool

	RefFrameIdx          [refsPerFrame]int
	RefOrderHint         [numRefFrames]int
	RefFrameSignBias     [numRefFrames]bool

	RefreshFrameFlags    uint8

	Tile                 TileInfo
	Quant                QuantizationParams
	Segmentation         SegmentationParams
	LoopFilter           LoopFilterParams
	CDEF                 CDEFParams
	LR                   LRParams
	TXMode               int
	ReferenceSelect      bool
	SkipModePresent      bool
	AllowWarpedMotion    bool
	ReducedTXSet         bool
	GlobalMotion         GlobalMotionParams

	// Derived values.
	MIRows       int
	MICols       int
	SBRows       int
	SBCols       int
	CodedLossless bool
	AllLossless   bool

	// DeltaQ/DeltaLF params.
	DeltaQPresent bool
	DeltaQRes     int
	DeltaLFPresent bool
	DeltaLFRes     int
	DeltaLFMulti   bool

	// Primary reference frame index.
	PrimaryRefFrame int
}

// ParseFrameHeader parses the uncompressed header from a frame header or frame OBU.
// AV1 spec 5.9.2 (uncompressed_header).
func ParseFrameHeader(data []byte, sh *SequenceHeader) (*FrameHeader, int, error) {
	br := NewBitReader(data)
	fh := &FrameHeader{}

	allFrames := uint8((1 << numRefFrames) - 1)

	if sh.ReducedStillPictureHeader {
		fh.ShowExistingFrame = false
		fh.FrameType = FrameTypeKeyFrame
		fh.FrameIsIntra = true
		fh.ShowFrame = true
		fh.ShowableFrame = false
	} else {
		var err error
		fh.ShowExistingFrame, err = br.ReadBool()
		if err != nil {
			return nil, 0, fmt.Errorf("show_existing_frame: %w", err)
		}

		if fh.ShowExistingFrame {
			v, err := br.ReadBits(3)
			if err != nil {
				return nil, 0, err
			}
			fh.FrameToShowMapIdx = int(v)
			// For show_existing_frame, we stop parsing here.
			// The decoder uses the buffered reference frame.
			bitsRead := br.BitsRead()
			return fh, bitsRead, nil
		}

		v, err := br.ReadBits(2)
		if err != nil {
			return nil, 0, err
		}
		fh.FrameType = int(v)
		fh.FrameIsIntra = fh.FrameType == FrameTypeKeyFrame || fh.FrameType == FrameTypeIntraOnly

		fh.ShowFrame, err = br.ReadBool()
		if err != nil {
			return nil, 0, err
		}

		if fh.ShowFrame {
			fh.ShowableFrame = fh.FrameType != FrameTypeKeyFrame
		} else {
			fh.ShowableFrame, err = br.ReadBool()
			if err != nil {
				return nil, 0, err
			}
		}

		if fh.FrameType == FrameTypeSwitchFrame || (fh.FrameType == FrameTypeKeyFrame && fh.ShowFrame) {
			fh.ErrorResilientMode = true
		} else {
			fh.ErrorResilientMode, err = br.ReadBool()
			if err != nil {
				return nil, 0, err
			}
		}
	}

	var err error

	fh.DisableCDFUpdate, err = br.ReadBool()
	if err != nil {
		return nil, 0, fmt.Errorf("disable_cdf_update: %w", err)
	}

	if sh.SeqForceScreenContentTools == selectScreenContentTools {
		fh.AllowScreenContentTools, err = br.ReadBool()
		if err != nil {
			return nil, 0, err
		}
	} else {
		fh.AllowScreenContentTools = sh.SeqForceScreenContentTools != 0
	}

	if fh.AllowScreenContentTools {
		if sh.SeqForceIntegerMV == selectIntegerMV {
			fh.ForceIntegerMV, err = br.ReadBool()
			if err != nil {
				return nil, 0, err
			}
		} else {
			fh.ForceIntegerMV = sh.SeqForceIntegerMV != 0
		}
	}

	if sh.FrameIDNumbersPresent {
		idLen := uint(sh.AdditionalFrameIDLengthMinus1 + sh.DeltaFrameIDLengthMinus2 + 3)
		v, err := br.ReadBits(idLen)
		if err != nil {
			return nil, 0, err
		}
		fh.CurrentFrameID = v
	}

	if fh.FrameType == FrameTypeSwitchFrame {
		fh.FrameSizeOverrideFlag = true
	} else if sh.ReducedStillPictureHeader {
		fh.FrameSizeOverrideFlag = false
	} else {
		fh.FrameSizeOverrideFlag, err = br.ReadBool()
		if err != nil {
			return nil, 0, err
		}
	}

	if sh.EnableOrderHint {
		v, err := br.ReadBits(uint(sh.OrderHintBits))
		if err != nil {
			return nil, 0, err
		}
		fh.OrderHint = int(v)
	}

	if fh.FrameIsIntra || fh.ErrorResilientMode {
		fh.PrimaryRefFrame = 7 // PRIMARY_REF_NONE
	} else {
		v, err := br.ReadBits(3)
		if err != nil {
			return nil, 0, err
		}
		fh.PrimaryRefFrame = int(v)
	}

	if sh.DecoderModelInfoPresent {
		// Skip decoder model info per-frame.
		bufRemovalTimePresent, err := br.ReadBool()
		if err != nil {
			return nil, 0, err
		}
		if bufRemovalTimePresent {
			for i := 0; i <= sh.OperatingPointsCntMinus1; i++ {
				if sh.OperatingPointIDC[i] != 0 {
					// Check temporal and spatial matching (simplified).
					n := uint(sh.bufferDelayLengthMinus1 + 1)
					_, err = br.ReadBits(n) // buffer_removal_time
					if err != nil {
						return nil, 0, err
					}
				}
			}
		}
	}

	if fh.FrameType == FrameTypeSwitchFrame || (fh.FrameType == FrameTypeKeyFrame && fh.ShowFrame) {
		fh.RefreshFrameFlags = allFrames
	} else {
		v, err := br.ReadBits(8)
		if err != nil {
			return nil, 0, err
		}
		fh.RefreshFrameFlags = uint8(v)
	}

	if (!fh.FrameIsIntra || fh.RefreshFrameFlags != allFrames) &&
		fh.ErrorResilientMode && sh.EnableOrderHint {
		for i := 0; i < numRefFrames; i++ {
			v, err := br.ReadBits(uint(sh.OrderHintBits))
			if err != nil {
				return nil, 0, err
			}
			fh.RefOrderHint[i] = int(v)
		}
	}

	// Frame size.
	if fh.FrameIsIntra {
		if err := parseFrameSize(br, sh, fh); err != nil {
			return nil, 0, fmt.Errorf("frame_size: %w", err)
		}
		if err := parseRenderSize(br, fh); err != nil {
			return nil, 0, fmt.Errorf("render_size: %w", err)
		}
		if fh.AllowScreenContentTools && fh.UpscaledWidth == fh.FrameWidth {
			fh.AllowIntraBC, err = br.ReadBool()
			if err != nil {
				return nil, 0, err
			}
		}
	} else {
		// Inter frame: read reference frame assignments.
		if !sh.EnableOrderHint {
			_, err = br.ReadBool() // frame_refs_short_signaling
			if err != nil {
				return nil, 0, err
			}
		} else {
			frameRefsShortSignaling, err := br.ReadBool()
			if err != nil {
				return nil, 0, err
			}
			if frameRefsShortSignaling {
				_, err = br.ReadBits(3) // last_frame_idx
				if err != nil {
					return nil, 0, err
				}
				_, err = br.ReadBits(3) // gold_frame_idx
				if err != nil {
					return nil, 0, err
				}
				// set_frame_refs() — computed, no bits read
			}
		}
		for i := 0; i < refsPerFrame; i++ {
			v, err := br.ReadBits(3)
			if err != nil {
				return nil, 0, err
			}
			fh.RefFrameIdx[i] = int(v)
			if sh.FrameIDNumbersPresent {
				deltaLen := uint(sh.DeltaFrameIDLengthMinus2 + 2)
				_, err = br.ReadBits(deltaLen) // delta_frame_id_minus_1
				if err != nil {
					return nil, 0, err
				}
			}
		}

		if fh.FrameSizeOverrideFlag && !fh.ErrorResilientMode {
			// frame_size_with_refs()
			foundRef := false
			for i := 0; i < refsPerFrame; i++ {
				found, err := br.ReadBool()
				if err != nil {
					return nil, 0, err
				}
				if found {
					foundRef = true
					break
				}
			}
			if !foundRef {
				if err := parseFrameSize(br, sh, fh); err != nil {
					return nil, 0, fmt.Errorf("frame_size: %w", err)
				}
				if err := parseRenderSize(br, fh); err != nil {
					return nil, 0, fmt.Errorf("render_size: %w", err)
				}
			} else {
				if sh.EnableSuperRes {
					if err := parseSuperResParams(br, sh, fh); err != nil {
						return nil, 0, err
					}
				} else {
					fh.UpscaledWidth = fh.FrameWidth
				}
			}
		} else {
			if err := parseFrameSize(br, sh, fh); err != nil {
				return nil, 0, fmt.Errorf("frame_size: %w", err)
			}
			if err := parseRenderSize(br, fh); err != nil {
				return nil, 0, fmt.Errorf("render_size: %w", err)
			}
		}

		if !fh.ForceIntegerMV {
			fh.AllowHighPrecisionMV, err = br.ReadBool()
			if err != nil {
				return nil, 0, err
			}
		}

		// read_interpolation_filter()
		isFilterSwitchable, err := br.ReadBool()
		if err != nil {
			return nil, 0, err
		}
		if isFilterSwitchable {
			fh.InterpFilter = interpFilterSwitchable
		} else {
			v, err := br.ReadBits(2)
			if err != nil {
				return nil, 0, err
			}
			fh.InterpFilter = int(v)
		}

		fh.IsMotionModeSwitchable, err = br.ReadBool()
		if err != nil {
			return nil, 0, err
		}

		if fh.ErrorResilientMode || !sh.EnableRefFrameMVS {
			fh.UseRefFrameMVS = false
		} else {
			fh.UseRefFrameMVS, err = br.ReadBool()
			if err != nil {
				return nil, 0, err
			}
		}
	}

	// Compute derived dimensions.
	computeImageSize(sh, fh)

	// disable_frame_end_update_cdf (AV1 spec 5.9.2)
	// In dav1d this is called "refresh_context".
	if !sh.ReducedStillPictureHeader && !fh.DisableCDFUpdate {
		_, err = br.ReadBool() // disable_frame_end_update_cdf
		if err != nil {
			return nil, 0, fmt.Errorf("disable_frame_end_update_cdf: %w", err)
		}
	}

	// tile_info()
	if err := parseTileInfo(br, sh, fh); err != nil {
		return nil, 0, fmt.Errorf("tile_info: %w", err)
	}

	// quantization_params()
	if err := parseQuantizationParams(br, sh, fh); err != nil {
		return nil, 0, fmt.Errorf("quantization_params: %w", err)
	}

	// segmentation_params()
	if err := parseSegmentationParams(br, fh); err != nil {
		return nil, 0, fmt.Errorf("segmentation_params: %w", err)
	}

	// Compute CodedLossless and AllLossless.
	computeLossless(sh, fh)

	// delta_q_params()
	if err := parseDeltaQParams(br, fh); err != nil {
		return nil, 0, fmt.Errorf("delta_q_params: %w", err)
	}

	// delta_lf_params()
	if err := parseDeltaLFParams(br, fh); err != nil {
		return nil, 0, fmt.Errorf("delta_lf_params: %w", err)
	}

	// loop_filter_params()
	if err := parseLoopFilterParams(br, sh, fh); err != nil {
		return nil, 0, fmt.Errorf("loop_filter_params: %w", err)
	}

	// cdef_params()
	if err := parseCDEFParams(br, sh, fh); err != nil {
		return nil, 0, fmt.Errorf("cdef_params: %w", err)
	}

	// lr_params()
	if err := parseLRParams(br, sh, fh); err != nil {
		return nil, 0, fmt.Errorf("lr_params: %w", err)
	}

	// read_tx_mode()
	if fh.CodedLossless {
		fh.TXMode = txModeOnly4x4
	} else {
		useTXSelect, err := br.ReadBool()
		if err != nil {
			return nil, 0, fmt.Errorf("tx_mode: %w", err)
		}
		if useTXSelect {
			fh.TXMode = txModeSelect
		} else {
			fh.TXMode = txModeLargest
		}
	}

	// frame_reference_mode()
	if fh.FrameIsIntra {
		fh.ReferenceSelect = false
	} else {
		fh.ReferenceSelect, err = br.ReadBool()
		if err != nil {
			return nil, 0, err
		}
	}

	// skip_mode_params()
	if err := parseSkipModeParams(br, sh, fh); err != nil {
		return nil, 0, fmt.Errorf("skip_mode_params: %w", err)
	}

	if fh.FrameIsIntra || fh.ErrorResilientMode || !sh.EnableWarpedMotion {
		fh.AllowWarpedMotion = false
	} else {
		fh.AllowWarpedMotion, err = br.ReadBool()
		if err != nil {
			return nil, 0, err
		}
	}

	fh.ReducedTXSet, err = br.ReadBool()
	if err != nil {
		return nil, 0, fmt.Errorf("reduced_tx_set: %w", err)
	}

	// global_motion_params()
	if err := parseGlobalMotionParams(br, fh); err != nil {
		return nil, 0, fmt.Errorf("global_motion_params: %w", err)
	}

	// film_grain_params() — skip for now, only present if seq header flag set.
	if sh.FilmGrainParamsPresent && (fh.ShowFrame || fh.ShowableFrame) {
		if err := skipFilmGrainParams(br, sh, fh); err != nil {
			return nil, 0, fmt.Errorf("film_grain_params: %w", err)
		}
	}

	bitsRead := br.BitsRead()
	return fh, bitsRead, nil
}

// parseFrameSize parses frame_size() (AV1 spec 5.9.5).
func parseFrameSize(br *BitReader, sh *SequenceHeader, fh *FrameHeader) error {
	if fh.FrameSizeOverrideFlag {
		fwBits := uint(FloorLog2(uint32(sh.MaxFrameWidthMinus1)) + 1)
		fhBits := uint(FloorLog2(uint32(sh.MaxFrameHeightMinus1)) + 1)

		v, err := br.ReadBits(fwBits)
		if err != nil {
			return err
		}
		fh.FrameWidth = int(v) + 1

		v, err = br.ReadBits(fhBits)
		if err != nil {
			return err
		}
		fh.FrameHeight = int(v) + 1
	} else {
		fh.FrameWidth = sh.MaxFrameWidth
		fh.FrameHeight = sh.MaxFrameHeight
	}

	if err := parseSuperResParams(br, sh, fh); err != nil {
		return err
	}

	return nil
}

// parseSuperResParams parses superres_params() (AV1 spec 5.9.8).
func parseSuperResParams(br *BitReader, sh *SequenceHeader, fh *FrameHeader) error {
	if sh.EnableSuperRes {
		useSuperRes, err := br.ReadBool()
		if err != nil {
			return err
		}
		if useSuperRes {
			v, err := br.ReadBits(3) // coded_denom
			if err != nil {
				return err
			}
			superResDenom := int(v) + 9 // SUPERRES_DENOM_MIN = 9
			fh.UpscaledWidth = fh.FrameWidth
			fh.FrameWidth = (fh.UpscaledWidth*8 + superResDenom/2) / superResDenom
			return nil
		}
	}
	fh.UpscaledWidth = fh.FrameWidth
	return nil
}

// parseRenderSize parses render_size() (AV1 spec 5.9.6).
func parseRenderSize(br *BitReader, fh *FrameHeader) error {
	renderAndFrameSizeDifferent, err := br.ReadBool()
	if err != nil {
		return err
	}
	if renderAndFrameSizeDifferent {
		v, err := br.ReadBits(16)
		if err != nil {
			return err
		}
		fh.RenderWidth = int(v) + 1
		v, err = br.ReadBits(16)
		if err != nil {
			return err
		}
		fh.RenderHeight = int(v) + 1
	} else {
		fh.RenderWidth = fh.UpscaledWidth
		fh.RenderHeight = fh.FrameHeight
	}
	return nil
}

// computeImageSize computes derived image dimensions (AV1 spec 5.9.9).
func computeImageSize(sh *SequenceHeader, fh *FrameHeader) {
	fh.MICols = 2 * ((fh.FrameWidth + 7) >> 3)
	fh.MIRows = 2 * ((fh.FrameHeight + 7) >> 3)

	sbSize := sh.SuperblockSize
	fh.SBCols = (fh.MICols + (sbSize/4 - 1)) / (sbSize / 4)
	fh.SBRows = (fh.MIRows + (sbSize/4 - 1)) / (sbSize / 4)
}

// parseTileInfo parses tile_info() (AV1 spec 5.9.15).
func parseTileInfo(br *BitReader, sh *SequenceHeader, fh *FrameHeader) error {
	sbCols := uint32(fh.SBCols)
	sbRows := uint32(fh.SBRows)
	sbSize := uint32(sh.SuperblockSize)
	miSize := uint32(4) // MI size is always 4
	sbShift := uint(FloorLog2(sbSize)) - uint(FloorLog2(miSize))

	maxTileWidthSB := uint32(4096) / sbSize // MAX_TILE_WIDTH = 4096
	maxTileAreaSB := uint32(4096*2304) / (sbSize * sbSize)

	minLog2TileCols := TileLog2(maxTileWidthSB, sbCols)
	maxLog2TileCols := CeilLog2(sbCols)
	maxLog2TileRows := CeilLog2(sbRows)
	minLog2Tiles := max(TileLog2(maxTileAreaSB, sbCols*sbRows), uint(minLog2TileCols))

	var err error
	fh.Tile.UniformTileSpacing, err = br.ReadBool()
	if err != nil {
		return err
	}

	if fh.Tile.UniformTileSpacing {
		tileColsLog2 := minLog2TileCols
		for tileColsLog2 < maxLog2TileCols {
			inc, err := br.ReadBool()
			if err != nil {
				return err
			}
			if inc {
				tileColsLog2++
			} else {
				break
			}
		}

		tileWidthSB := (sbCols + (1 << tileColsLog2) - 1) >> tileColsLog2
		i := 0
		for startSB := uint32(0); startSB < sbCols; startSB += tileWidthSB {
			w := tileWidthSB
			if startSB+w > sbCols {
				w = sbCols - startSB
			}
			fh.Tile.TileWidthSB = append(fh.Tile.TileWidthSB, int(w))
			i++
		}
		fh.Tile.TileCols = i
		fh.Tile.TileColsLog2 = int(tileColsLog2)

		var minLog2TileRows uint
		if minLog2Tiles > tileColsLog2 {
			minLog2TileRows = minLog2Tiles - tileColsLog2
		}

		tileRowsLog2 := minLog2TileRows
		for tileRowsLog2 < maxLog2TileRows {
			inc, err := br.ReadBool()
			if err != nil {
				return err
			}
			if inc {
				tileRowsLog2++
			} else {
				break
			}
		}

		tileHeightSB := (sbRows + (1 << tileRowsLog2) - 1) >> tileRowsLog2
		i = 0
		for startSB := uint32(0); startSB < sbRows; startSB += tileHeightSB {
			h := tileHeightSB
			if startSB+h > sbRows {
				h = sbRows - startSB
			}
			fh.Tile.TileHeightSB = append(fh.Tile.TileHeightSB, int(h))
			i++
		}
		fh.Tile.TileRows = i
		fh.Tile.TileRowsLog2 = int(tileRowsLog2)
	} else {
		// Non-uniform tile spacing: widths coded explicitly.
		var widestTileSB uint32
		startSB := uint32(0)
		for startSB < sbCols {
			maxWidth := min32(sbCols-startSB, maxTileWidthSB)
			w, err := br.ReadNS(maxWidth)
			if err != nil {
				return err
			}
			w++ // width_in_sbs_minus_1
			fh.Tile.TileWidthSB = append(fh.Tile.TileWidthSB, int(w))
			if w > widestTileSB {
				widestTileSB = w
			}
			startSB += w
		}
		fh.Tile.TileCols = len(fh.Tile.TileWidthSB)
		fh.Tile.TileColsLog2 = int(CeilLog2(uint32(fh.Tile.TileCols)))

		if minLog2Tiles > 0 {
			maxTileAreaSB = sbRows * sbCols / (uint32(1) << minLog2Tiles)
		} else {
			maxTileAreaSB = sbRows * sbCols
		}
		maxTileHeightSB := max32(maxTileAreaSB/widestTileSB, 1)

		startSB = 0
		for startSB < sbRows {
			maxHeight := min32(sbRows-startSB, maxTileHeightSB)
			h, err := br.ReadNS(maxHeight)
			if err != nil {
				return err
			}
			h++ // height_in_sbs_minus_1
			fh.Tile.TileHeightSB = append(fh.Tile.TileHeightSB, int(h))
			startSB += h
		}
		fh.Tile.TileRows = len(fh.Tile.TileHeightSB)
		fh.Tile.TileRowsLog2 = int(CeilLog2(uint32(fh.Tile.TileRows)))
	}

	_ = sbShift // used for MI-level tile boundaries in later phases

	if fh.Tile.TileColsLog2 > 0 || fh.Tile.TileRowsLog2 > 0 {
		v, err := br.ReadBits(uint(fh.Tile.TileColsLog2 + fh.Tile.TileRowsLog2))
		if err != nil {
			return err
		}
		fh.Tile.ContextUpdateTileID = int(v)

		v, err = br.ReadBits(2)
		if err != nil {
			return err
		}
		fh.Tile.TileSizeBytes = int(v) + 1
	} else {
		fh.Tile.ContextUpdateTileID = 0
		fh.Tile.TileSizeBytes = 0
	}

	return nil
}

// parseQuantizationParams parses quantization_params() (AV1 spec 5.9.12).
func parseQuantizationParams(br *BitReader, sh *SequenceHeader, fh *FrameHeader) error {
	v, err := br.ReadBits(8)
	if err != nil {
		return err
	}
	fh.Quant.BaseQIdx = int(v)

	dq, err := br.ReadDeltaQ()
	if err != nil {
		return err
	}
	fh.Quant.DeltaQYDc = int(dq)

	if sh.Color.NumPlanes > 1 {
		if sh.Color.SeparateUVDelta {
			dq, err = br.ReadDeltaQ()
			if err != nil {
				return err
			}
			fh.Quant.DeltaQUDc = int(dq)

			dq, err = br.ReadDeltaQ()
			if err != nil {
				return err
			}
			fh.Quant.DeltaQUAc = int(dq)

			dq, err = br.ReadDeltaQ()
			if err != nil {
				return err
			}
			fh.Quant.DeltaQVDc = int(dq)

			dq, err = br.ReadDeltaQ()
			if err != nil {
				return err
			}
			fh.Quant.DeltaQVAc = int(dq)
		} else {
			dq, err = br.ReadDeltaQ()
			if err != nil {
				return err
			}
			fh.Quant.DeltaQUDc = int(dq)
			fh.Quant.DeltaQVDc = fh.Quant.DeltaQUDc

			dq, err = br.ReadDeltaQ()
			if err != nil {
				return err
			}
			fh.Quant.DeltaQUAc = int(dq)
			fh.Quant.DeltaQVAc = fh.Quant.DeltaQUAc
		}
	}

	fh.Quant.UsingQMatrix, err = br.ReadBool()
	if err != nil {
		return err
	}
	if fh.Quant.UsingQMatrix {
		v, err := br.ReadBits(4)
		if err != nil {
			return err
		}
		fh.Quant.QMY = int(v)

		v, err = br.ReadBits(4)
		if err != nil {
			return err
		}
		fh.Quant.QMU = int(v)

		if sh.Color.SeparateUVDelta {
			v, err = br.ReadBits(4)
			if err != nil {
				return err
			}
			fh.Quant.QMV = int(v)
		} else {
			fh.Quant.QMV = fh.Quant.QMU
		}
	}

	return nil
}

// Segmentation feature constants from spec table.
var segmentationFeatureBits = [segLvlMax]int{8, 6, 6, 6, 6, 3, 0, 0}
var segmentationFeatureSigned = [segLvlMax]bool{true, true, true, true, true, false, false, false}
var segmentationFeatureMax = [segLvlMax]int{255, 63, 63, 63, 63, 7, 0, 0}

// parseSegmentationParams parses segmentation_params() (AV1 spec 5.9.14).
func parseSegmentationParams(br *BitReader, fh *FrameHeader) error {
	var err error
	fh.Segmentation.Enabled, err = br.ReadBool()
	if err != nil {
		return err
	}
	if !fh.Segmentation.Enabled {
		return nil
	}

	if fh.PrimaryRefFrame == 7 { // PRIMARY_REF_NONE
		fh.Segmentation.UpdateMap = true
		fh.Segmentation.TemporalUpdate = false
		fh.Segmentation.UpdateData = true
	} else {
		fh.Segmentation.UpdateMap, err = br.ReadBool()
		if err != nil {
			return err
		}
		if fh.Segmentation.UpdateMap {
			fh.Segmentation.TemporalUpdate, err = br.ReadBool()
			if err != nil {
				return err
			}
		}
		fh.Segmentation.UpdateData, err = br.ReadBool()
		if err != nil {
			return err
		}
	}

	if fh.Segmentation.UpdateData {
		for i := 0; i < maxSegments; i++ {
			for j := 0; j < segLvlMax; j++ {
				fh.Segmentation.FeatureEnabled[i][j], err = br.ReadBool()
				if err != nil {
					return err
				}
				if fh.Segmentation.FeatureEnabled[i][j] {
					bitsToRead := segmentationFeatureBits[j]
					if bitsToRead > 0 {
						v, err := br.ReadBits(uint(bitsToRead))
						if err != nil {
							return err
						}
						val := int(v)
						if segmentationFeatureSigned[j] {
							sign, err := br.ReadBool()
							if err != nil {
								return err
							}
							if sign {
								val = -val
							}
						}
						fh.Segmentation.FeatureData[i][j] = clampInt(val, -segmentationFeatureMax[j], segmentationFeatureMax[j])
					}
				}
			}
		}
	}

	return nil
}

// computeLossless determines if the frame is coded lossless.
func computeLossless(sh *SequenceHeader, fh *FrameHeader) {
	fh.CodedLossless = true
	for segID := 0; segID < maxSegments; segID++ {
		qIndex := fh.getQIndex(segID)
		lossless := qIndex == 0 &&
			fh.Quant.DeltaQYDc == 0 &&
			fh.Quant.DeltaQUDc == 0 &&
			fh.Quant.DeltaQUAc == 0 &&
			fh.Quant.DeltaQVDc == 0 &&
			fh.Quant.DeltaQVAc == 0
		if !lossless {
			fh.CodedLossless = false
		}
	}
	_ = sh // needed in full implementation for segment feature checks
	fh.AllLossless = fh.CodedLossless && fh.FrameWidth == fh.UpscaledWidth
}

// getQIndex returns the quantizer index for a given segment, accounting for segmentation.
func (fh *FrameHeader) getQIndex(segID int) int {
	if fh.Segmentation.Enabled && fh.Segmentation.FeatureEnabled[segID][0] {
		return clampInt(fh.Quant.BaseQIdx+fh.Segmentation.FeatureData[segID][0], 0, 255)
	}
	return fh.Quant.BaseQIdx
}

// parseDeltaQParams parses delta_q_params() (AV1 spec 5.9.17).
func parseDeltaQParams(br *BitReader, fh *FrameHeader) error {
	fh.DeltaQRes = 0
	fh.DeltaQPresent = false
	if fh.Quant.BaseQIdx > 0 {
		var err error
		fh.DeltaQPresent, err = br.ReadBool()
		if err != nil {
			return err
		}
	}
	if fh.DeltaQPresent {
		v, err := br.ReadBits(2)
		if err != nil {
			return err
		}
		fh.DeltaQRes = int(v)
	}
	return nil
}

// parseDeltaLFParams parses delta_lf_params() (AV1 spec 5.9.18).
func parseDeltaLFParams(br *BitReader, fh *FrameHeader) error {
	fh.DeltaLFPresent = false
	fh.DeltaLFRes = 0
	fh.DeltaLFMulti = false
	if fh.DeltaQPresent {
		if !fh.AllowIntraBC {
			var err error
			fh.DeltaLFPresent, err = br.ReadBool()
			if err != nil {
				return err
			}
		}
		if fh.DeltaLFPresent {
			v, err := br.ReadBits(2)
			if err != nil {
				return err
			}
			fh.DeltaLFRes = int(v)

			fh.DeltaLFMulti, err = br.ReadBool()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Default reference deltas (AV1 spec 5.9.11).
var defaultRefDeltas = [numRefFrames]int{1, 0, 0, 0, 0, -1, -1, -1}

// parseLoopFilterParams parses loop_filter_params() (AV1 spec 5.9.11).
func parseLoopFilterParams(br *BitReader, sh *SequenceHeader, fh *FrameHeader) error {
	if fh.CodedLossless || fh.AllowIntraBC {
		fh.LoopFilter.FilterLevel = [4]int{0, 0, 0, 0}
		fh.LoopFilter.SharpnessLevel = 0
		fh.LoopFilter.DeltaEnabled = false
		fh.LoopFilter.RefDeltas = defaultRefDeltas
		return nil
	}

	v, err := br.ReadBits(6)
	if err != nil {
		return err
	}
	fh.LoopFilter.FilterLevel[0] = int(v)

	v, err = br.ReadBits(6)
	if err != nil {
		return err
	}
	fh.LoopFilter.FilterLevel[1] = int(v)

	if sh.Color.NumPlanes > 1 {
		if fh.LoopFilter.FilterLevel[0] != 0 || fh.LoopFilter.FilterLevel[1] != 0 {
			v, err = br.ReadBits(6)
			if err != nil {
				return err
			}
			fh.LoopFilter.FilterLevel[2] = int(v)

			v, err = br.ReadBits(6)
			if err != nil {
				return err
			}
			fh.LoopFilter.FilterLevel[3] = int(v)
		}
	}

	v, err = br.ReadBits(3)
	if err != nil {
		return err
	}
	fh.LoopFilter.SharpnessLevel = int(v)

	fh.LoopFilter.RefDeltas = defaultRefDeltas

	fh.LoopFilter.DeltaEnabled, err = br.ReadBool()
	if err != nil {
		return err
	}
	if fh.LoopFilter.DeltaEnabled {
		fh.LoopFilter.DeltaUpdate, err = br.ReadBool()
		if err != nil {
			return err
		}
		if fh.LoopFilter.DeltaUpdate {
			for i := 0; i < numRefFrames; i++ {
				updateRefDelta, err := br.ReadBool()
				if err != nil {
					return err
				}
				if updateRefDelta {
					dq, err := br.ReadSU(7)
					if err != nil {
						return err
					}
					fh.LoopFilter.RefDeltas[i] = int(dq)
				}
			}
			for i := 0; i < 2; i++ {
				updateModeDelta, err := br.ReadBool()
				if err != nil {
					return err
				}
				if updateModeDelta {
					dq, err := br.ReadSU(7)
					if err != nil {
						return err
					}
					fh.LoopFilter.ModeDeltas[i] = int(dq)
				}
			}
		}
	}

	return nil
}

// parseCDEFParams parses cdef_params() (AV1 spec 5.9.19).
func parseCDEFParams(br *BitReader, sh *SequenceHeader, fh *FrameHeader) error {
	if fh.CodedLossless || fh.AllowIntraBC || !sh.EnableCDEF {
		fh.CDEF.Bits = 0
		fh.CDEF.YPriStrength = []int{0}
		fh.CDEF.YSecStrength = []int{0}
		fh.CDEF.UVPriStrength = []int{0}
		fh.CDEF.UVSecStrength = []int{0}
		return nil
	}

	v, err := br.ReadBits(2)
	if err != nil {
		return err
	}
	fh.CDEF.DampingMinus3 = int(v)

	v, err = br.ReadBits(2)
	if err != nil {
		return err
	}
	fh.CDEF.Bits = int(v)

	n := 1 << fh.CDEF.Bits
	fh.CDEF.YPriStrength = make([]int, n)
	fh.CDEF.YSecStrength = make([]int, n)
	fh.CDEF.UVPriStrength = make([]int, n)
	fh.CDEF.UVSecStrength = make([]int, n)

	for i := 0; i < n; i++ {
		v, err = br.ReadBits(4)
		if err != nil {
			return err
		}
		fh.CDEF.YPriStrength[i] = int(v)

		v, err = br.ReadBits(2)
		if err != nil {
			return err
		}
		fh.CDEF.YSecStrength[i] = int(v)
		if fh.CDEF.YSecStrength[i] == 3 {
			fh.CDEF.YSecStrength[i] = 4
		}

		if sh.Color.NumPlanes > 1 {
			v, err = br.ReadBits(4)
			if err != nil {
				return err
			}
			fh.CDEF.UVPriStrength[i] = int(v)

			v, err = br.ReadBits(2)
			if err != nil {
				return err
			}
			fh.CDEF.UVSecStrength[i] = int(v)
			if fh.CDEF.UVSecStrength[i] == 3 {
				fh.CDEF.UVSecStrength[i] = 4
			}
		}
	}

	return nil
}

// parseLRParams parses lr_params() (AV1 spec 5.9.20).
func parseLRParams(br *BitReader, sh *SequenceHeader, fh *FrameHeader) error {
	if fh.AllLossless || fh.AllowIntraBC || !sh.EnableRestoration {
		fh.LR.Type = [3]int{restoreNone, restoreNone, restoreNone}
		return nil
	}

	allNone := true
	usesChromaLR := false

	for i := 0; i < sh.Color.NumPlanes; i++ {
		v, err := br.ReadBits(2)
		if err != nil {
			return err
		}
		lrType := int(v)
		// Remap: 0=NONE, 1=SWITCHABLE, 2=WIENER, 3=SGRPROJ
		remap := [4]int{restoreNone, restoreSwitchable, restoreWiener, restoreSgrproj}
		fh.LR.Type[i] = remap[lrType]

		if fh.LR.Type[i] != restoreNone {
			allNone = false
			if i > 0 {
				usesChromaLR = true
			}
		}
	}

	if allNone {
		return nil
	}

	if sh.Use128x128Superblock {
		fh.LR.UnitSize[0] = 256
	} else {
		fh.LR.UnitSize[0] = 128
	}

	if fh.LR.Type[0] != restoreNone {
		lrUnitShift, err := br.ReadBool()
		if err != nil {
			return err
		}
		if lrUnitShift {
			fh.LR.UnitSize[0] <<= 1
			if !sh.Use128x128Superblock {
				lrUnitExtra, err := br.ReadBool()
				if err != nil {
					return err
				}
				if lrUnitExtra {
					fh.LR.UnitSize[0] <<= 1
				}
			}
		}
	}

	fh.LR.UnitSize[1] = fh.LR.UnitSize[0]
	fh.LR.UnitSize[2] = fh.LR.UnitSize[0]

	if sh.Color.SubsamplingX != 0 && sh.Color.SubsamplingY != 0 && usesChromaLR {
		lrUVShift, err := br.ReadBool()
		if err != nil {
			return err
		}
		if lrUVShift {
			// No shift: chroma LR unit = luma LR unit
		} else {
			fh.LR.UnitSize[1] >>= 1
			fh.LR.UnitSize[2] >>= 1
		}
	}

	return nil
}

// parseSkipModeParams parses skip_mode_params() (AV1 spec 5.9.22).
func parseSkipModeParams(br *BitReader, sh *SequenceHeader, fh *FrameHeader) error {
	if fh.FrameIsIntra || !fh.ReferenceSelect || !sh.EnableOrderHint {
		fh.SkipModePresent = false
		return nil
	}
	// For inter frames, skip mode availability depends on reference order hints.
	// For Phase 1, we just read the flag.
	var err error
	fh.SkipModePresent, err = br.ReadBool()
	if err != nil {
		return err
	}
	return nil
}

// parseGlobalMotionParams parses global_motion_params() (AV1 spec 5.9.24).
func parseGlobalMotionParams(br *BitReader, fh *FrameHeader) error {
	if fh.FrameIsIntra {
		return nil
	}

	for ref := 1; ref < numRefFrames; ref++ {
		isGlobal, err := br.ReadBool()
		if err != nil {
			return err
		}
		if !isGlobal {
			fh.GlobalMotion.Type[ref] = 0 // IDENTITY
			continue
		}
		isROTZoom, err := br.ReadBool()
		if err != nil {
			return err
		}
		if isROTZoom {
			fh.GlobalMotion.Type[ref] = 2 // ROTZOOM
		} else {
			isTrans, err := br.ReadBool()
			if err != nil {
				return err
			}
			if isTrans {
				fh.GlobalMotion.Type[ref] = 1 // TRANSLATION
			} else {
				fh.GlobalMotion.Type[ref] = 3 // AFFINE
			}
		}

		gmType := fh.GlobalMotion.Type[ref]
		if gmType >= 2 { // ROTZOOM or AFFINE
			// Read rotation/zoom parameters.
			if _, err := readSignedSubexpWithRef(br, 12); err != nil {
				return err
			}
			if _, err := readSignedSubexpWithRef(br, 12); err != nil {
				return err
			}
		}
		if gmType == 3 { // AFFINE
			if _, err := readSignedSubexpWithRef(br, 12); err != nil {
				return err
			}
			if _, err := readSignedSubexpWithRef(br, 12); err != nil {
				return err
			}
		}
		if gmType >= 1 { // TRANSLATION, ROTZOOM, AFFINE
			transBits := 9
			if fh.AllowHighPrecisionMV {
				transBits = 12
			}
			if _, err := readSignedSubexpWithRef(br, transBits); err != nil {
				return err
			}
			if _, err := readSignedSubexpWithRef(br, transBits); err != nil {
				return err
			}
		}
	}

	return nil
}

// readSignedSubexpWithRef reads a signed subexp coded value for global motion.
// Simplified: reads the actual decode_signed_subexp_with_ref() per spec 5.9.26.
func readSignedSubexpWithRef(br *BitReader, allowBits int) (int, error) {
	// decode_subexp()
	i := 0
	mk := 0
	k := 3 // initial sub-exp parameter
	for {
		b := 0
		if i != 0 {
			b = k + i - 1
		} else {
			b = k
		}
		maxCode := 1 << uint(allowBits)
		a := 1 << uint(b)
		if maxCode-mk <= 3*a {
			// Read NS coded value.
			v, err := br.ReadNS(uint32(maxCode - mk))
			if err != nil {
				return 0, err
			}
			_ = v
			break
		} else {
			subexpMore, err := br.ReadBool()
			if err != nil {
				return 0, err
			}
			if subexpMore {
				i++
				mk += a
			} else {
				v, err := br.ReadBits(uint(b))
				if err != nil {
					return 0, err
				}
				_ = v
				break
			}
		}
	}

	// sign bit
	_, err := br.ReadBool()
	if err != nil {
		return 0, err
	}

	return 0, nil // value not needed for Phase 1
}

// skipFilmGrainParams skips film_grain_params() (AV1 spec 5.9.30).
func skipFilmGrainParams(br *BitReader, sh *SequenceHeader, fh *FrameHeader) error {
	if !sh.FilmGrainParamsPresent || (!fh.ShowFrame && !fh.ShowableFrame) {
		return nil
	}

	applyGrain, err := br.ReadBool()
	if err != nil {
		return err
	}
	if !applyGrain {
		return nil
	}

	_, err = br.ReadBits(16) // grain_seed
	if err != nil {
		return err
	}

	if fh.FrameType == FrameTypeInterFrame {
		updateGrain, err := br.ReadBool()
		if err != nil {
			return err
		}
		if !updateGrain {
			_, err = br.ReadBits(3) // film_grain_params_ref_idx
			return err
		}
	}

	numYPoints, err := br.ReadBits(4)
	if err != nil {
		return err
	}
	for i := 0; i < int(numYPoints); i++ {
		_, err = br.ReadBits(8) // point_y_value
		if err != nil {
			return err
		}
		_, err = br.ReadBits(8) // point_y_scaling
		if err != nil {
			return err
		}
	}

	chromaScalingFromLuma := false
	if sh.Color.NumPlanes > 1 {
		chromaScalingFromLuma, err = br.ReadBool()
		if err != nil {
			return err
		}
	}

	var numCbPoints, numCrPoints uint32
	if sh.Color.MonoChrome || chromaScalingFromLuma ||
		(sh.Color.SubsamplingX == 1 && sh.Color.SubsamplingY == 1 && numYPoints == 0) {
		// No chroma points.
	} else {
		numCbPoints, err = br.ReadBits(4)
		if err != nil {
			return err
		}
		for i := 0; i < int(numCbPoints); i++ {
			_, err = br.ReadBits(16) // point_cb_value + scaling
			if err != nil {
				return err
			}
		}
		numCrPoints, err = br.ReadBits(4)
		if err != nil {
			return err
		}
		for i := 0; i < int(numCrPoints); i++ {
			_, err = br.ReadBits(16) // point_cr_value + scaling
			if err != nil {
				return err
			}
		}
	}

	_, err = br.ReadBits(2) // grain_scaling_minus_8
	if err != nil {
		return err
	}

	arCoeffLag, err := br.ReadBits(2)
	if err != nil {
		return err
	}

	numPosLuma := 2 * arCoeffLag * (arCoeffLag + 1)
	numPosChroma := numPosLuma
	if numYPoints > 0 {
		numPosChroma = numPosLuma + 1
	}

	for i := 0; i < int(numPosLuma); i++ {
		_, err = br.ReadBits(8) // ar_coeffs_y_plus_128
		if err != nil {
			return err
		}
	}

	if numCbPoints > 0 || chromaScalingFromLuma {
		for i := 0; i < int(numPosChroma); i++ {
			_, err = br.ReadBits(8) // ar_coeffs_cb_plus_128
			if err != nil {
				return err
			}
		}
	}
	if numCrPoints > 0 || chromaScalingFromLuma {
		for i := 0; i < int(numPosChroma); i++ {
			_, err = br.ReadBits(8) // ar_coeffs_cr_plus_128
			if err != nil {
				return err
			}
		}
	}

	_, err = br.ReadBits(2) // ar_coeff_shift_minus_6
	if err != nil {
		return err
	}
	_, err = br.ReadBits(2) // grain_scale_shift
	if err != nil {
		return err
	}

	if numCbPoints > 0 {
		_, err = br.ReadBits(8) // cb_mult
		if err != nil {
			return err
		}
		_, err = br.ReadBits(8) // cb_luma_mult
		if err != nil {
			return err
		}
		_, err = br.ReadBits(9) // cb_offset
		if err != nil {
			return err
		}
	}
	if numCrPoints > 0 {
		_, err = br.ReadBits(8) // cr_mult
		if err != nil {
			return err
		}
		_, err = br.ReadBits(8) // cr_luma_mult
		if err != nil {
			return err
		}
		_, err = br.ReadBits(9) // cr_offset
		if err != nil {
			return err
		}
	}

	_, err = br.ReadBool() // overlap_flag
	if err != nil {
		return err
	}
	_, err = br.ReadBits(2) // clip_to_restricted_range
	if err != nil {
		return err
	}

	return nil
}

// Helper functions.

func clampInt(val, low, high int) int {
	if val < low {
		return low
	}
	if val > high {
		return high
	}
	return val
}

func min32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

func max32(a, b uint32) uint32 {
	if a > b {
		return a
	}
	return b
}

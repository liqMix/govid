package av1

// CDFTables holds all CDF arrays used by the AV1 entropy decoder.
// Each CDF array has nsymbs+1 entries: nsymbs cumulative probabilities
// followed by an adaptation counter.
type CDFTables struct {
	// Partition type CDFs, indexed by [ctx].
	// ctx depends on block size and neighbor availability.
	Partition [20][]uint16

	// Intra mode CDFs.
	KFIntraMode  [5][5][]uint16 // Keyframe Y mode, indexed by [above_mode_ctx][left_mode_ctx]
	IntraMode    [4][]uint16    // Inter-frame Y mode, indexed by [size_ctx]
	IntraModeUV    [14][]uint16 // UV mode without CfL (13 symbols), indexed by [y_mode]
	IntraModeUVCfL [14][]uint16 // UV mode with CfL (14 symbols), indexed by [y_mode]
	AngleDelta     [8][]uint16  // angle delta, indexed by [mode-V_PRED]
	CfLSign        []uint16     // CfL sign combination (8 symbols)
	CfLAlpha       [6][]uint16  // CfL alpha magnitude (16 symbols per ctx)

	// TX size CDFs.
	TXSize [6][3][]uint16 // [tx_depth][ctx]

	// TX type CDFs.
	TXType [4][16][]uint16 // [txSizeSquare][intraDir]

	// Skip flag CDF.
	Skip [3][]uint16 // [ctx]

	// Segment ID CDFs.
	SegmentID [3][]uint16 // spatial prediction ctx

	// Coefficient CDFs.
	TXBSkip         [5][13][]uint16 // [txSzCtx][skipCtx] — all_zero flag per TXB
	EOBMulti16      [2][]uint16      // [plane_type]
	EOBMulti32      [2][]uint16
	EOBMulti64      [2][]uint16
	EOBMulti128     [2][]uint16
	EOBMulti256     [2][]uint16
	EOBMulti512     [2][]uint16
	EOBMulti1024    [2][]uint16
	EOBExtra        [9][2][]uint16 // [eob_multi_ctx][plane_type]
	CoeffBaseEOB    [3][2][3][]uint16 // [txSizeCtx][planeType][eobCtx]
	CoeffBase       [3][2][41][]uint16 // [txSizeCtx][planeType][ctx]
	CoeffBaseRange  [3][2][21][]uint16 // [txSizeCtx][planeType][ctx]
	DCSign          [2][3][]uint16 // [planeType][ctx]

	// Inter mode CDFs (Phase 5).
	InterMode    [8][]uint16 // [ctx]
	IsInter      [4][]uint16 // [ctx]
	CompMode     [5][]uint16 // [ctx]
	CompRef      [5][3][]uint16 // [ctx][type]
	MVJoint      []uint16
	MVSign       [2][]uint16 // [comp]
	MVClass      [2][]uint16 // [comp]
	MVBit        [2][10][]uint16 // [comp][bit]
	MVFracHP     [2][]uint16 // [comp]
	MVFracLP     [2][]uint16 // [comp]
	NewMVContext [6][]uint16
	ZeroMVContext [2][]uint16
	RefMVContext [6][]uint16
	DRLMode      [3][]uint16

	// Filter intra mode CDF.
	FilterIntraMode []uint16
	UseFilterIntra  [22][]uint16

	// Delta Q/LF CDFs.
	DeltaQ  []uint16
	DeltaLF [5][]uint16

	// Restoration type CDF.
	RestorationType [2][]uint16 // [plane > 0]

	// CDEF CDFs (not entropy-coded, but stored here for uniformity).

	// Palette CDFs.
	PaletteY     [7][3][]uint16 // has_palette_y [bsCtx][ctx]
	PaletteUV    [2][]uint16    // has_palette_uv [ctx]
	PaletteSizeY     [7][]uint16    // palette_size_y [bsCtx] — 7 symbols
	PaletteSizeUV    [7][]uint16   // palette_size_uv [bsCtx] — 7 symbols
	PaletteColorIdx  [7][5][]uint16 // palette_color_idx [palSize-2][ctx] — palSize symbols
}

// NewCDFTables creates a fresh set of default CDF tables.
// baseQIdx selects Q-specific coefficient CDFs (0-255). Pass -1 for universal defaults.
func NewCDFTables(baseQIdx ...int) *CDFTables {
	cdf := &CDFTables{}
	q := -1
	if len(baseQIdx) > 0 {
		q = baseQIdx[0]
	}
	initDefaultCDFs(cdf, q)
	return cdf
}

// SaveCDFs returns a deep copy of the CDF tables for frame-end snapshot.
func (c *CDFTables) SaveCDFs() *CDFTables {
	dst := &CDFTables{}
	*dst = *c
	// Deep copy all slices.
	copyPartition(dst, c)
	return dst
}

// RestoreCDFs copies saved CDF state back.
func (c *CDFTables) RestoreCDFs(src *CDFTables) {
	*c = *src
}

func copyPartition(dst, src *CDFTables) {
	for i := range src.Partition {
		if src.Partition[i] != nil {
			dst.Partition[i] = make([]uint16, len(src.Partition[i]))
			copy(dst.Partition[i], src.Partition[i])
		}
	}
	for i := range src.KFIntraMode {
		for j := range src.KFIntraMode[i] {
			if src.KFIntraMode[i][j] != nil {
				dst.KFIntraMode[i][j] = make([]uint16, len(src.KFIntraMode[i][j]))
				copy(dst.KFIntraMode[i][j], src.KFIntraMode[i][j])
			}
		}
	}
	for i := range src.IntraMode {
		if src.IntraMode[i] != nil {
			dst.IntraMode[i] = make([]uint16, len(src.IntraMode[i]))
			copy(dst.IntraMode[i], src.IntraMode[i])
		}
	}
	for i := range src.Skip {
		if src.Skip[i] != nil {
			dst.Skip[i] = make([]uint16, len(src.Skip[i]))
			copy(dst.Skip[i], src.Skip[i])
		}
	}
}

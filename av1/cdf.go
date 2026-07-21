package av1

// CDFTables holds all CDF arrays used by the AV1 entropy decoder.
// Each CDF array has nsymbs entries: nsymbs-1 cumulative probabilities
// followed by an adaptation counter.
type CDFTables struct {
	// Partition type CDFs: [block_level][ctx].
	// BL0=128x128 (nsymbs=8), BL1-3 (nsymbs=10), BL4=8x8 (nsymbs=4).
	Partition [5][4][]uint16

	// Intra mode CDFs.
	KFIntraMode    [5][5][]uint16 // Keyframe Y mode, [above_mode_ctx][left_mode_ctx], nsymbs=13
	IntraMode      [4][]uint16    // Inter-frame Y mode, [size_ctx], nsymbs=13
	IntraModeUV    [14][]uint16   // UV mode without CfL (13 symbols), [y_mode]
	IntraModeUVCfL [14][]uint16   // UV mode with CfL (14 symbols), [y_mode]
	AngleDelta     [8][]uint16    // angle delta, [mode-V_PRED], nsymbs=7
	CfLSign        []uint16       // CfL sign combination (8 symbols)
	CfLAlpha       [6][]uint16    // CfL alpha magnitude (16 symbols per ctx)

	// TX partition CDFs — dav1d txpart[7][3], nsymbs=2.
	TXSize [7][3][]uint16

	// TX type CDFs matching dav1d:
	// IntraTXType1: non-reduced set for TX4x4/TX8x8, nsymbs=7.
	IntraTXType1 [2][NumIntraModes][]uint16
	// IntraTXType2: reduced set or TX16x16, nsymbs=5.
	IntraTXType2 [3][NumIntraModes][]uint16

	// Skip flag CDF, nsymbs=2.
	Skip [3][]uint16

	// Segment ID CDFs, nsymbs=8.
	SegmentID [3][]uint16

	// Coefficient CDFs.
	TXBSkip        [5][13][]uint16    // [txSzCtx][skipCtx], nsymbs=2
	EOBMulti16     [2][]uint16        // [plane_type], nsymbs=5
	EOBMulti32     [2][]uint16        // nsymbs=6
	EOBMulti64     [2][]uint16        // nsymbs=7
	EOBMulti128    [2][]uint16        // nsymbs=8
	EOBMulti256    [2][]uint16        // nsymbs=9
	EOBMulti512    [2][]uint16        // nsymbs=10
	EOBMulti1024   [2][]uint16        // nsymbs=11
	EOBExtra       [5][2][9][]uint16  // [txSizeCtx][plane_type][eob_bit_ctx], nsymbs=2
	CoeffBaseEOB   [5][2][4][]uint16  // [txSizeCtx][planeType][eobCtx], nsymbs=3
	CoeffBase      [5][2][41][]uint16 // [txSizeCtx][planeType][ctx], nsymbs=4
	CoeffBaseRange [4][2][21][]uint16 // [brTxCtx][planeType][ctx], nsymbs=4
	DCSign         [2][3][]uint16     // [planeType][ctx], nsymbs=2

	// Inter mode CDFs.
	InterMode     [8][]uint16
	IsInter       [4][]uint16
	CompMode      [5][]uint16
	CompRef       [5][3][]uint16
	MVJoint       []uint16
	MVSign        [2][]uint16
	MVClass       [2][]uint16
	MVBit         [2][10][]uint16
	MVFracHP      [2][]uint16
	MVFracLP      [2][]uint16
	NewMVContext  [6][]uint16
	ZeroMVContext [2][]uint16
	RefMVContext  [6][]uint16
	DRLMode       [3][]uint16

	// Filter intra mode CDF.
	FilterIntraMode []uint16
	UseFilterIntra  [22][]uint16

	// Delta Q/LF CDFs.
	DeltaQ  []uint16
	DeltaLF [5][]uint16

	// Restoration type CDF.
	RestorationType [2][]uint16

	// Palette CDFs.
	PaletteY        [7][3][]uint16
	PaletteUV       [2][]uint16
	PaletteSizeY    [7][]uint16
	PaletteSizeUV   [7][]uint16
	PaletteColorIdx [7][5][]uint16
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

// DeepCopy returns a deep copy of the CDF tables.
func (c *CDFTables) DeepCopy() *CDFTables {
	dst := &CDFTables{}
	*dst = *c
	deepCopyAllSlices(dst, c)
	return dst
}

// SaveCDFs returns a deep copy of the CDF tables for frame-end snapshot.
func (c *CDFTables) SaveCDFs() *CDFTables {
	return c.DeepCopy()
}

// RestoreCDFs copies saved CDF state back.
func (c *CDFTables) RestoreCDFs(src *CDFTables) {
	*c = *src
}

func deepCopyAllSlices(dst, src *CDFTables) {
	copySlice := func(s []uint16) []uint16 {
		if s == nil {
			return nil
		}
		d := make([]uint16, len(s))
		copy(d, s)
		return d
	}

	for bl := range src.Partition {
		for ctx := range src.Partition[bl] {
			dst.Partition[bl][ctx] = copySlice(src.Partition[bl][ctx])
		}
	}
	for i := range src.KFIntraMode {
		for j := range src.KFIntraMode[i] {
			dst.KFIntraMode[i][j] = copySlice(src.KFIntraMode[i][j])
		}
	}
	for i := range src.IntraMode {
		dst.IntraMode[i] = copySlice(src.IntraMode[i])
	}
	for i := range src.IntraModeUV {
		dst.IntraModeUV[i] = copySlice(src.IntraModeUV[i])
	}
	for i := range src.IntraModeUVCfL {
		dst.IntraModeUVCfL[i] = copySlice(src.IntraModeUVCfL[i])
	}
	for i := range src.AngleDelta {
		dst.AngleDelta[i] = copySlice(src.AngleDelta[i])
	}
	dst.CfLSign = copySlice(src.CfLSign)
	for i := range src.CfLAlpha {
		dst.CfLAlpha[i] = copySlice(src.CfLAlpha[i])
	}
	for i := range src.TXSize {
		for j := range src.TXSize[i] {
			dst.TXSize[i][j] = copySlice(src.TXSize[i][j])
		}
	}
	for i := range src.IntraTXType1 {
		for j := range src.IntraTXType1[i] {
			dst.IntraTXType1[i][j] = copySlice(src.IntraTXType1[i][j])
		}
	}
	for i := range src.IntraTXType2 {
		for j := range src.IntraTXType2[i] {
			dst.IntraTXType2[i][j] = copySlice(src.IntraTXType2[i][j])
		}
	}
	for i := range src.Skip {
		dst.Skip[i] = copySlice(src.Skip[i])
	}
	for i := range src.SegmentID {
		dst.SegmentID[i] = copySlice(src.SegmentID[i])
	}
	// Coeff CDFs
	for a := range src.TXBSkip {
		for b := range src.TXBSkip[a] {
			dst.TXBSkip[a][b] = copySlice(src.TXBSkip[a][b])
		}
	}
	for i := range src.EOBMulti16 {
		dst.EOBMulti16[i] = copySlice(src.EOBMulti16[i])
	}
	for i := range src.EOBMulti32 {
		dst.EOBMulti32[i] = copySlice(src.EOBMulti32[i])
	}
	for i := range src.EOBMulti64 {
		dst.EOBMulti64[i] = copySlice(src.EOBMulti64[i])
	}
	for i := range src.EOBMulti128 {
		dst.EOBMulti128[i] = copySlice(src.EOBMulti128[i])
	}
	for i := range src.EOBMulti256 {
		dst.EOBMulti256[i] = copySlice(src.EOBMulti256[i])
	}
	for i := range src.EOBMulti512 {
		dst.EOBMulti512[i] = copySlice(src.EOBMulti512[i])
	}
	for i := range src.EOBMulti1024 {
		dst.EOBMulti1024[i] = copySlice(src.EOBMulti1024[i])
	}
	for a := range src.EOBExtra {
		for b := range src.EOBExtra[a] {
			for c := range src.EOBExtra[a][b] {
				dst.EOBExtra[a][b][c] = copySlice(src.EOBExtra[a][b][c])
			}
		}
	}
	for a := range src.CoeffBaseEOB {
		for b := range src.CoeffBaseEOB[a] {
			for c := range src.CoeffBaseEOB[a][b] {
				dst.CoeffBaseEOB[a][b][c] = copySlice(src.CoeffBaseEOB[a][b][c])
			}
		}
	}
	for a := range src.CoeffBase {
		for b := range src.CoeffBase[a] {
			for c := range src.CoeffBase[a][b] {
				dst.CoeffBase[a][b][c] = copySlice(src.CoeffBase[a][b][c])
			}
		}
	}
	for a := range src.CoeffBaseRange {
		for b := range src.CoeffBaseRange[a] {
			for c := range src.CoeffBaseRange[a][b] {
				dst.CoeffBaseRange[a][b][c] = copySlice(src.CoeffBaseRange[a][b][c])
			}
		}
	}
	for a := range src.DCSign {
		for b := range src.DCSign[a] {
			dst.DCSign[a][b] = copySlice(src.DCSign[a][b])
		}
	}
	dst.FilterIntraMode = copySlice(src.FilterIntraMode)
	for i := range src.UseFilterIntra {
		dst.UseFilterIntra[i] = copySlice(src.UseFilterIntra[i])
	}
	dst.DeltaQ = copySlice(src.DeltaQ)
	for i := range src.DeltaLF {
		dst.DeltaLF[i] = copySlice(src.DeltaLF[i])
	}
	for i := range src.PaletteY {
		for j := range src.PaletteY[i] {
			dst.PaletteY[i][j] = copySlice(src.PaletteY[i][j])
		}
	}
	for i := range src.PaletteUV {
		dst.PaletteUV[i] = copySlice(src.PaletteUV[i])
	}
	for i := range src.PaletteSizeY {
		dst.PaletteSizeY[i] = copySlice(src.PaletteSizeY[i])
	}
	for i := range src.PaletteSizeUV {
		dst.PaletteSizeUV[i] = copySlice(src.PaletteSizeUV[i])
	}
	for i := range src.PaletteColorIdx {
		for j := range src.PaletteColorIdx[i] {
			dst.PaletteColorIdx[i][j] = copySlice(src.PaletteColorIdx[i][j])
		}
	}
}

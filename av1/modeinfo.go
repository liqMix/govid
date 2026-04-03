package av1

// blockInfo holds per-block syntax elements decoded from the bitstream.
type blockInfo struct {
	yMode           int
	uvMode          int
	angleDeltaY     int
	angleDeltaUV    int
	bSize           int
	txSize          int
	txType          int
	skip            bool
	segmentID       int
	cflAlphaU       int
	cflAlphaV       int
	cflSignU        int
	cflSignV        int
	useFilterIntra  bool
	filterIntraMode int
	paletteSizeY    int // 0 = no palette, 2-8 = palette size
	paletteSizeUV   int
	paletteColorsY  []uint8
	paletteColorsUV []uint8
}

// isDirectional returns true if mode is a directional intra mode (V, H, D45..D67).
func isDirectional(mode int) bool {
	return mode >= IntraVertical && mode <= IntraD67
}

// decodeModeInfo reads per-block intra mode information from the bitstream.
// Follows AV1 spec intra_frame_mode_info() reading order.
func (td *tileDecoder) decodeModeInfo(miRow, miCol, bSize int) (*blockInfo, error) {
	bi := &blockInfo{bSize: bSize}
	fh := td.dec.frameHdr
	sh := td.dec.seqHdr

	// 1. Segment ID (if Segmentation.Enabled && UpdateMap).
	if fh.Segmentation.Enabled && fh.Segmentation.UpdateMap {
		ctx := td.getSegmentIDCtx(miRow, miCol)
		seg, err := td.sc.ReadSymbol(td.cdf.SegmentID[ctx], maxSegments)
		if err != nil {
			return nil, err
		}
		bi.segmentID = seg
	}

	// 2. Skip flag.
	skipCtx := td.getSkipCtx(miRow, miCol)
	skipSym, err := td.sc.ReadSymbol(td.cdf.Skip[skipCtx], 2)
	if err != nil {
		return nil, err
	}
	bi.skip = skipSym == 1

	// 3. CDEF block index — once per 64x64 region, first non-skip block.
	if err := td.readCDEF(miRow, miCol, bi.skip); err != nil {
		return nil, err
	}

	// 4. Delta Q/LF.
	if err := td.readDeltaQLF(bSize, bi.skip); err != nil {
		return nil, err
	}

	// 5. Y intra mode — keyframes use above/left mode context per spec.
	var yMode int
	if fh.FrameType == FrameTypeKeyFrame || fh.FrameType == FrameTypeIntraOnly {
		aboveCtx := td.getKFYModeCtx(miRow, miCol, true)
		leftCtx := td.getKFYModeCtx(miRow, miCol, false)
		yMode, err = td.sc.ReadSymbol(td.cdf.KFIntraMode[aboveCtx][leftCtx], NumIntraModes)
	} else {
		sizeCtx := bsizeToIntraModeCtx(bSize)
		yMode, err = td.sc.ReadSymbol(td.cdf.IntraMode[sizeCtx], NumIntraModes)
	}
	if err != nil {
		return nil, err
	}
	bi.yMode = yMode

	// 6. Angle delta for Y directional modes.
	if isDirectional(yMode) {
		deltaIdx := yMode - IntraVertical
		if deltaIdx < 0 {
			deltaIdx = 0
		}
		if deltaIdx >= 8 {
			deltaIdx = 7
		}
		sym, err := td.sc.ReadSymbol(td.cdf.AngleDelta[deltaIdx], 7)
		if err != nil {
			return nil, err
		}
		bi.angleDeltaY = sym - 3
	}

	// 7. UV mode: 14 symbols if CfL allowed, 13 otherwise.
	uvCtx := yMode
	if uvCtx >= 14 {
		uvCtx = 13
	}
	hasChroma := sh.Color.NumPlanes > 1
	cflAllowed := hasChroma && td.isCfLAllowed(bSize)
	if cflAllowed {
		uvMode, err := td.sc.ReadSymbol(td.cdf.IntraModeUVCfL[uvCtx], NumIntraModes+1)
		if err != nil {
			return nil, err
		}
		bi.uvMode = uvMode
	} else if hasChroma {
		uvMode, err := td.sc.ReadSymbol(td.cdf.IntraModeUV[uvCtx], NumIntraModes)
		if err != nil {
			return nil, err
		}
		bi.uvMode = uvMode
	}

	// 8. CfL alpha values if UV mode is CfL.
	if bi.uvMode == UV_CFL_PRED {
		if err := td.readCfLAlphas(bi); err != nil {
			return nil, err
		}
	}

	// 9. Angle delta for UV directional modes (skip if CfL).
	if bi.uvMode != UV_CFL_PRED && isDirectional(bi.uvMode) {
		deltaIdx := bi.uvMode - IntraVertical
		if deltaIdx < 0 {
			deltaIdx = 0
		}
		if deltaIdx >= 8 {
			deltaIdx = 7
		}
		sym, err := td.sc.ReadSymbol(td.cdf.AngleDelta[deltaIdx], 7)
		if err != nil {
			return nil, err
		}
		bi.angleDeltaUV = sym - 3
	}

	// 10. Palette mode info (when AllowScreenContentTools).
	if err := td.readPaletteModeInfo(bSize, bi); err != nil {
		return nil, err
	}

	// 11. Filter intra mode info.
	if err := td.readFilterIntraModeInfo(bSize, bi); err != nil {
		return nil, err
	}

	// 12. TX size: skip blocks use largest, non-skip reads depth.
	if !bi.skip {
		bi.txSize, err = td.getTXSize(bSize)
		if err != nil {
			return nil, err
		}
	} else {
		bi.txSize = TXSizeForBlockSize[bSize]
	}

	// 13. TX type (once per block).
	bi.txType, err = td.readTXType(bi.yMode, bi.txSize, bi.skip)
	if err != nil {
		return nil, err
	}

	// Update above/left mode context.
	bw4 := MISizeWide[bSize]
	bh4 := MISizeTall[bSize]
	for i := miCol; i < miCol+bw4 && i < td.miColEnd; i++ {
		idx := i - td.miColStart
		if idx >= 0 && idx < len(td.aboveModeCtx) {
			td.aboveModeCtx[idx] = yMode
			td.aboveSkipCtx[idx] = bi.skip
		}
	}
	for i := miRow; i < miRow+bh4 && i < td.miRowEnd; i++ {
		idx := i - td.miRowStart
		if idx >= 0 && idx < len(td.leftModeCtx) {
			td.leftModeCtx[idx] = yMode
			td.leftSkipCtx[idx] = bi.skip
		}
	}

	return bi, nil
}

// readCDEF reads CDEF index for the 64x64 region containing this block.
// Per spec, it reads once per 64x64 region (first non-skip block).
func (td *tileDecoder) readCDEF(miRow, miCol int, skip bool) error {
	fh, sh := td.dec.frameHdr, td.dec.seqHdr
	if skip || fh.CodedLossless || !sh.EnableCDEF || fh.AllowIntraBC || fh.CDEF.Bits == 0 {
		return nil
	}
	// Align to 64x64 boundary (16 MI units).
	r := miRow & ^15
	c := miCol & ^15
	key := [2]int{r, c}
	if td.cdefRead[key] {
		return nil
	}
	td.cdefRead[key] = true
	_, err := td.sc.ReadLiteral(fh.CDEF.Bits)
	return err
}

// readDeltaQLF reads delta Q and delta LF values from the bitstream.
func (td *tileDecoder) readDeltaQLF(bSize int, skip bool) error {
	fh := td.dec.frameHdr
	if !fh.DeltaQPresent || !td.readDeltas {
		return nil
	}

	sbSize := Block64x64
	if td.dec.seqHdr.Use128x128Superblock {
		sbSize = Block128x128
	}
	if bSize == sbSize && skip {
		return nil
	}

	// Delta Q: per dav1d, 4-symbol CDF then exponential extension if sym==3.
	deltaQAbs, err := td.readDeltaAbsValue(td.cdf.DeltaQ)
	if err != nil {
		return err
	}
	deltaQSign := 0
	if deltaQAbs > 0 {
		if td.sc.ReadBoolEqui() {
			deltaQSign = -1
		} else {
			deltaQSign = 1
		}
	}
	// Apply delta Q to current quantizer index.
	if deltaQAbs > 0 {
		deltaQ := deltaQSign * deltaQAbs
		td.currentQIdx += deltaQ
		if td.currentQIdx < 1 {
			td.currentQIdx = 1
		}
		if td.currentQIdx > 255 {
			td.currentQIdx = 255
		}
	}

	// Delta LF (if present).
	if fh.DeltaLFPresent {
		lfCount := 1
		if fh.DeltaLFMulti {
			lfCount = 4
		}
		for i := 0; i < lfCount; i++ {
			idx := i
			if idx >= 5 {
				idx = 4
			}
			dlAbs, err := td.sc.ReadSymbol(td.cdf.DeltaLF[idx], 4)
			if err != nil {
				return err
			}
			dlAbsVal := int(dlAbs)
			if dlAbs == 3 {
				n, err := td.sc.ReadLiteral(3)
				if err != nil {
					return err
				}
				nBits := int(n) + 1
				extra, err := td.sc.ReadLiteral(nBits)
				if err != nil {
					return err
				}
				dlAbsVal = int(extra) + 1 + (1 << nBits)
			}
			if dlAbsVal > 0 {
				td.sc.ReadBoolEqui() // sign bit (unused)
			}
		}
	}

	td.readDeltas = false
	return nil
}

// readDeltaAbsValue reads a delta absolute value using CDF + exponential extension.
// Per dav1d: sym ∈ {0,1,2}; if sym==3 (rare): n_bits=1+bools(3), val=bools(n_bits)+1+(1<<n_bits).
func (td *tileDecoder) readDeltaAbsValue(cdf []uint16) (int, error) {
	sym, err := td.sc.ReadSymbol(cdf, 4)
	if err != nil {
		return 0, err
	}
	if sym < 3 {
		return int(sym), nil
	}
	n, err := td.sc.ReadLiteral(3)
	if err != nil {
		return 3, err
	}
	nBits := int(n) + 1
	extra, err := td.sc.ReadLiteral(nBits)
	if err != nil {
		return 3, err
	}
	return int(extra) + 1 + (1 << nBits), nil
}

// readCfLAlphas reads CfL sign and alpha magnitudes.
func (td *tileDecoder) readCfLAlphas(bi *blockInfo) error {
	signs, err := td.sc.ReadSymbol(td.cdf.CfLSign, 8)
	if err != nil {
		return err
	}
	// Per libaom: signU = joint_sign / 3, signV = joint_sign % 3
	signU := signs / 3
	signV := signs % 3
	bi.cflSignU = signU
	bi.cflSignV = signV
	if signU != 0 {
		ctx := signV*2 + (signU - 1) // context 0-5
		alpha, err := td.sc.ReadSymbol(td.cdf.CfLAlpha[ctx], 16)
		if err != nil {
			return err
		}
		bi.cflAlphaU = alpha + 1
	}
	if signV != 0 {
		ctx := signU*2 + (signV - 1) // context 0-5
		alpha, err := td.sc.ReadSymbol(td.cdf.CfLAlpha[ctx], 16)
		if err != nil {
			return err
		}
		bi.cflAlphaV = alpha + 1
	}
	return nil
}

// readPaletteModeInfo reads palette flags from the bitstream.
// Per libaom: palette Y flag only read when yMode == DC_PRED,
// palette UV flag only read when uvMode == DC_PRED.
func (td *tileDecoder) readPaletteModeInfo(bSize int, bi *blockInfo) error {
	fh := td.dec.frameHdr
	sh := td.dec.seqHdr
	if !fh.AllowScreenContentTools {
		return nil
	}
	if !blockAllowsPalette(bSize) {
		return nil
	}

	// Read has_palette_y (only when Y mode is DC_PRED).
	if bi.yMode == IntraDC {
		bsCtx := bsizeToPaletteCtx(bSize)
		hasPaletteY, err := td.sc.ReadSymbol(td.cdf.PaletteY[bsCtx][0], 2)
		if err != nil {
			return err
		}
		if hasPaletteY == 1 {
			if err := td.readPaletteColors(bi, 0); err != nil {
				return err
			}
		}
	}

	// Read has_palette_uv (only when UV mode is DC_PRED and chroma present).
	hasChroma := sh.Color.NumPlanes > 1
	if hasChroma && bi.uvMode == IntraDC {
		hasPaletteUV, err := td.sc.ReadSymbol(td.cdf.PaletteUV[0], 2)
		if err != nil {
			return err
		}
		if hasPaletteUV == 1 {
			if err := td.readPaletteColors(bi, 1); err != nil {
				return err
			}
		}
	}

	return nil
}

// readPaletteColors reads the palette size and color entries from the bitstream.
// plane: 0=Y, 1=UV.
func (td *tileDecoder) readPaletteColors(bi *blockInfo, plane int) error {
	bitDepth := td.dec.seqHdr.Color.BitDepth
	bsCtx := bsizeToPaletteCtx(bi.bSize)

	// Read palette_size_minus_2 from CDF (7 symbols: sizes 2-8).
	var sizeCDF []uint16
	if plane == 0 {
		sizeCDF = td.cdf.PaletteSizeY[bsCtx]
	} else {
		sizeCDF = td.cdf.PaletteSizeUV[bsCtx]
	}
	sizeMinus2, err := td.sc.ReadSymbol(sizeCDF, 7)
	if err != nil {
		return err
	}
	palSize := sizeMinus2 + 2

	// Read palette colors with delta encoding per dav1d.
	// For the first palette block (no cache), all colors are "new."
	colors := make([]uint8, palSize)
	// First color: raw literal.
	val, err := td.sc.ReadLiteral(bitDepth)
	if err != nil {
		return err
	}
	colors[0] = uint8(val)
	prev := int(val)
	if palSize > 1 {
		// Read bits parameter: bpc - 3 + read(2).
		bitsVal, err := td.sc.ReadLiteral(2)
		if err != nil {
			return err
		}
		bits := bitDepth - 3 + int(bitsVal)
		maxVal := (1 << bitDepth) - 1
		isLuma := plane == 0
		for i := 1; i < palSize; i++ {
			delta, err := td.sc.ReadLiteral(bits)
			if err != nil {
				return err
			}
			d := int(delta)
			if isLuma {
				d++ // luma: delta + 1 (per spec: +!pl)
			}
			prev = prev + d
			if prev > maxVal {
				prev = maxVal
			}
			colors[i] = uint8(prev)
			if isLuma {
				remaining := maxVal - prev - 1
				if remaining < 0 {
					// Fill remaining colors with max.
					for j := i + 1; j < palSize; j++ {
						colors[j] = uint8(maxVal)
					}
					break
				}
				newBits := 1
				for v := remaining; v > 1; v >>= 1 {
					newBits++
				}
				if newBits < bits {
					bits = newBits
				}
			}
		}
	}

	if plane == 0 {
		bi.paletteSizeY = palSize
		bi.paletteColorsY = colors
	} else {
		bi.paletteSizeUV = palSize
		bi.paletteColorsUV = colors
	}
	return nil
}

// readFilterIntraModeInfo reads filter intra mode info per the AV1 spec.
func (td *tileDecoder) readFilterIntraModeInfo(bSize int, bi *blockInfo) error {
	sh := td.dec.seqHdr
	if !sh.EnableFilterIntra {
		return nil
	}
	// Spec conditions: YMode == DC_PRED, PaletteSizeY == 0, Max(w, h) <= 32.
	if bi.yMode != IntraDC || bi.paletteSizeY > 0 {
		return nil
	}
	w := BlockWidth[bSize]
	h := BlockHeight[bSize]
	maxDim := w
	if h > maxDim {
		maxDim = h
	}
	if maxDim > 32 {
		return nil
	}

	useSym, err := td.sc.ReadSymbol(td.cdf.UseFilterIntra[bSize], 2)
	if err != nil {
		return err
	}
	bi.useFilterIntra = useSym == 1
	if bi.useFilterIntra {
		fiMode, err := td.sc.ReadSymbol(td.cdf.FilterIntraMode, 5)
		if err != nil {
			return err
		}
		bi.filterIntraMode = fiMode
	}
	return nil
}

// isCfLAllowed returns true if Chroma-from-Luma is allowed for this block.
func (td *tileDecoder) isCfLAllowed(bSize int) bool {
	sh := td.dec.seqHdr
	if sh.Color.SubsamplingX != 1 || sh.Color.SubsamplingY != 1 || sh.Color.MonoChrome {
		return false
	}
	return BlockWidth[bSize] <= 32 && BlockHeight[bSize] <= 32
}

// blockAllowsPalette returns true if palette mode is allowed for this block size.
func blockAllowsPalette(bSize int) bool {
	w := BlockWidth[bSize]
	h := BlockHeight[bSize]
	return w >= 8 && h >= 8 && w <= 64 && h <= 64
}

// bsizeToPaletteCtx maps block size to the palette CDF bsize_ctx (0-6).
func bsizeToPaletteCtx(bSize int) int {
	w := BlockWidth[bSize]
	h := BlockHeight[bSize]
	area := w * h
	ctx := 0
	for a := 128; a <= area && ctx < 6; a *= 2 {
		ctx++
	}
	return ctx
}

// getSegmentIDCtx computes the context for segment ID CDF.
func (td *tileDecoder) getSegmentIDCtx(miRow, miCol int) int {
	prevAbove := 0
	prevLeft := 0
	if miRow > td.miRowStart {
		prevAbove = 1
	}
	if miCol > td.miColStart {
		prevLeft = 1
	}
	return prevAbove + prevLeft
}

// getSkipCtx computes the context for the skip flag CDF.
func (td *tileDecoder) getSkipCtx(miRow, miCol int) int {
	ctx := 0
	if miRow > td.miRowStart {
		aboveIdx := miCol - td.miColStart
		if aboveIdx >= 0 && aboveIdx < len(td.aboveSkipCtx) && td.aboveSkipCtx[aboveIdx] {
			ctx++
		}
	}
	if miCol > td.miColStart {
		leftIdx := miRow - td.miRowStart
		if leftIdx >= 0 && leftIdx < len(td.leftSkipCtx) && td.leftSkipCtx[leftIdx] {
			ctx++
		}
	}
	return ctx
}

// getTXSize determines the transform size for the current block.
func (td *tileDecoder) getTXSize(bSize int) (int, error) {
	txMode := td.dec.frameHdr.TXMode
	maxTx := TXSizeForBlockSize[bSize]

	switch txMode {
	case txModeOnly4x4:
		return TX4x4, nil
	case txModeLargest:
		return maxTx, nil
	case txModeSelect:
		maxDepth := maxTx
		if maxDepth > 2 {
			maxDepth = 2
		}
		if maxDepth == 0 {
			return TX4x4, nil
		}
		// Sequential binary split decisions per depth level.
		txDepth := 0
		for d := 0; d < maxDepth; d++ {
			split, err := td.sc.ReadSymbol(td.cdf.TXSize[maxTx][d], 2)
			if err != nil {
				return TX4x4, err
			}
			if split == 0 {
				break
			}
			txDepth++
		}
		result := maxTx - txDepth
		if result < TX4x4 {
			result = TX4x4
		}
		return result, nil
	default:
		return maxTx, nil
	}
}

// dav1d tx_types_per_set mapping tables:
// Intra2 (reduced/TX16x16): 5 symbols → TX types
var txTypesIntra2 = [5]int{
	TxTypeIDENTITY_IDENTITY, // 0: IDTX
	TxTypeDCT_DCT,           // 1: DCT_DCT
	TxTypeADST_ADST,         // 2: ADST_ADST
	TxTypeADST_DCT,          // 3: ADST_DCT
	TxTypeDCT_ADST,          // 4: DCT_ADST
}

// Intra1 (non-reduced, TX4x4/TX8x8): 7 symbols → TX types
var txTypesIntra1 = [7]int{
	TxTypeIDENTITY_IDENTITY, // 0: IDTX
	TxTypeDCT_DCT,           // 1: DCT_DCT
	TxTypeIDENTITY_DCT,      // 2: V_DCT
	TxTypeDCT_IDENTITY,      // 3: H_DCT
	TxTypeADST_ADST,         // 4: ADST_ADST
	TxTypeADST_DCT,          // 5: ADST_DCT
	TxTypeDCT_ADST,          // 6: DCT_ADST
}

// readTXType reads or derives the TX type for an intra block.
// Matches dav1d decode.c: derives for TX32x32+, reads intra2 for TX16x16/reduced,
// reads intra1 for TX4x4/TX8x8 non-reduced.
func (td *tileDecoder) readTXType(yMode, txSize int, skip bool) (int, error) {
	fh := td.dec.frameHdr

	if skip || fh.CodedLossless {
		return TxTypeDCT_DCT, nil
	}

	// For intra: max+1 >= TX_64X64 means txSize >= TX32x32 → derive DCT_DCT
	if txSize >= TX32x32 {
		return TxTypeDCT_DCT, nil
	}

	modeCtx := yMode
	if modeCtx >= NumIntraModes {
		modeCtx = 0
	}

	if fh.ReducedTXSet || txSize == TX16x16 {
		// Intra2: reduced set (4 symbols = nsymbs 5)
		minCtx := txSize
		if minCtx > 2 {
			minCtx = 2
		}
		cdf := td.cdf.IntraTXType2[minCtx][modeCtx]
		if cdf == nil {
			return TxTypeDCT_DCT, nil
		}
		sym, err := td.sc.ReadSymbol(cdf, 5)
		if err != nil {
			return TxTypeDCT_DCT, err
		}
		if sym < len(txTypesIntra2) {
			return txTypesIntra2[sym], nil
		}
		return TxTypeDCT_DCT, nil
	}

	// Intra1: non-reduced, TX4x4/TX8x8 (6 symbols = nsymbs 7)
	minCtx := txSize
	if minCtx > 1 {
		minCtx = 1
	}
	cdf := td.cdf.IntraTXType1[minCtx][modeCtx]
	if cdf == nil {
		return TxTypeDCT_DCT, nil
	}
	sym, err := td.sc.ReadSymbol(cdf, 7)
	if err != nil {
		return TxTypeDCT_DCT, err
	}
	if sym < len(txTypesIntra1) {
		return txTypesIntra1[sym], nil
	}
	return TxTypeDCT_DCT, nil
}

// intraModeToTxType derives the TX type from the intra prediction mode.
// Per AV1 spec Table 7-10. For TX sizes > 16x16, ADST falls back to DCT.
func intraModeToTxType(mode, txSize int) int {
	// AV1 spec: ADST/FLIPADST not available for TX sizes > 16x16.
	if txSize > TX16x16 {
		return TxTypeDCT_DCT
	}

	switch mode {
	case IntraDC:
		return TxTypeDCT_DCT
	case IntraVertical:
		return TxTypeADST_DCT
	case IntraHorizontal:
		return TxTypeDCT_ADST
	case IntraD45:
		return TxTypeDCT_DCT
	case IntraD135:
		return TxTypeADST_ADST
	case IntraD113:
		return TxTypeADST_DCT
	case IntraD157:
		return TxTypeDCT_ADST
	case IntraD203:
		return TxTypeDCT_ADST
	case IntraD67:
		return TxTypeADST_DCT
	case IntraSmooth:
		return TxTypeADST_ADST
	case IntraSmoothV:
		return TxTypeADST_DCT
	case IntraSmoothH:
		return TxTypeDCT_ADST
	case IntraPaeth:
		return TxTypeADST_ADST
	default:
		return TxTypeDCT_DCT
	}
}

// kfYModeCtxMap maps intra mode to KF Y mode context (0-4).
// Modes 0-2 → ctx 0-2, modes 3-8 → ctx 3, modes 9-12 → ctx 4.
var kfYModeCtxMap = [NumIntraModes]int{0, 1, 2, 3, 3, 3, 3, 3, 3, 4, 4, 4, 4}

// getKFYModeCtx returns the keyframe Y mode context from neighbor modes.
func (td *tileDecoder) getKFYModeCtx(miRow, miCol int, isAbove bool) int {
	if isAbove {
		if miRow > td.miRowStart {
			idx := miCol - td.miColStart
			if idx >= 0 && idx < len(td.aboveModeCtx) {
				mode := td.aboveModeCtx[idx]
				if mode >= 0 && mode < NumIntraModes {
					return kfYModeCtxMap[mode]
				}
			}
		}
	} else {
		if miCol > td.miColStart {
			idx := miRow - td.miRowStart
			if idx >= 0 && idx < len(td.leftModeCtx) {
				mode := td.leftModeCtx[idx]
				if mode >= 0 && mode < NumIntraModes {
					return kfYModeCtxMap[mode]
				}
			}
		}
	}
	return 0 // DC_PRED context when unavailable
}

// bsizeToIntraModeCtx maps block size to the intra mode CDF context index.
func bsizeToIntraModeCtx(bSize int) int {
	w := BlockWidth[bSize]
	h := BlockHeight[bSize]
	sz := w * h
	switch {
	case sz <= 16:
		return 0
	case sz <= 64:
		return 1
	case sz <= 256:
		return 2
	default:
		return 3
	}
}

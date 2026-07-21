package av1

// blockInfo holds per-block syntax elements decoded from the bitstream.
type blockInfo struct {
	yMode           int
	uvMode          int
	angleDeltaY     int
	angleDeltaUV    int
	bSize           int
	txSize          int
	skip            bool
	segmentID       int
	cflAlphaU       int
	cflAlphaV       int
	cflSignU        int
	cflSignV        int
	useFilterIntra  bool
	filterIntraMode int
	paletteSizeY    int
	paletteSizeUV   int
	paletteColorsY  []uint8
	paletteColorsUV []uint8
}

// isDirectional returns true if mode is a directional intra mode (V, H, D45..D67).
func isDirectional(mode int) bool {
	return mode >= IntraVertical && mode <= IntraD67
}

// decodeModeInfo reads per-block intra mode information from the bitstream.
// TX type is NOT read here — it moves to coeffs.go (per TXB).
func (td *tileDecoder) decodeModeInfo(miRow, miCol, bSize int) (*blockInfo, error) {
	bi := &blockInfo{bSize: bSize}
	fh := td.dec.frameHdr
	sh := td.dec.seqHdr

	// 1. Segment ID.
	if fh.Segmentation.Enabled && fh.Segmentation.UpdateMap {
		ctx := td.getSegmentIDCtx(miRow, miCol)
		bi.segmentID = td.sc.ReadSymbol(td.cdf.SegmentID[ctx], maxSegments)
	}

	// 2. Skip flag.
	skipCtx := td.getSkipCtx(miRow, miCol)
	bi.skip = td.sc.ReadSymbol(td.cdf.Skip[skipCtx], 2) == 1

	// 3. CDEF block index.
	td.readCDEF(miRow, miCol, bi.skip)

	// 4. Delta Q/LF.
	td.readDeltaQLF(bSize, bi.skip)

	// 5. Y intra mode.
	var yMode int
	if fh.FrameType == FrameTypeKeyFrame || fh.FrameType == FrameTypeIntraOnly {
		aboveCtx := td.getKFYModeCtx(miRow, miCol, true)
		leftCtx := td.getKFYModeCtx(miRow, miCol, false)
		yMode = td.sc.ReadSymbol(td.cdf.KFIntraMode[aboveCtx][leftCtx], NumIntraModes)
	} else {
		sizeCtx := bsizeToIntraModeCtx(bSize)
		yMode = td.sc.ReadSymbol(td.cdf.IntraMode[sizeCtx], NumIntraModes)
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
		sym := td.sc.ReadSymbol(td.cdf.AngleDelta[deltaIdx], 7)
		bi.angleDeltaY = sym - 3
	}

	// 7. UV mode.
	uvCtx := yMode
	if uvCtx >= 14 {
		uvCtx = 13
	}
	hasChroma := sh.Color.NumPlanes > 1
	cflAllowed := hasChroma && td.isCfLAllowed(bSize)
	if cflAllowed {
		bi.uvMode = td.sc.ReadSymbol(td.cdf.IntraModeUVCfL[uvCtx], NumIntraModes+1)
	} else if hasChroma {
		bi.uvMode = td.sc.ReadSymbol(td.cdf.IntraModeUV[uvCtx], NumIntraModes)
	}

	// 8. CfL alpha values.
	if bi.uvMode == UV_CFL_PRED {
		td.readCfLAlphas(bi)
	}

	// 9. Angle delta for UV directional modes.
	if bi.uvMode != UV_CFL_PRED && isDirectional(bi.uvMode) {
		deltaIdx := bi.uvMode - IntraVertical
		if deltaIdx < 0 {
			deltaIdx = 0
		}
		if deltaIdx >= 8 {
			deltaIdx = 7
		}
		sym := td.sc.ReadSymbol(td.cdf.AngleDelta[deltaIdx], 7)
		bi.angleDeltaUV = sym - 3
	}

	// 10. Palette mode info.
	td.readPaletteModeInfo(bSize, bi)

	// 11. Filter intra mode info.
	td.readFilterIntraModeInfo(bSize, bi)

	// 12. TX size.
	if !bi.skip {
		bi.txSize = td.getTXSize(bSize)
	} else {
		bi.txSize = TXSizeForBlockSize[bSize]
	}

	// TX type is read per-TXB in decodeCoeffs (correct per dav1d/spec).

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
func (td *tileDecoder) readCDEF(miRow, miCol int, skip bool) {
	fh, sh := td.dec.frameHdr, td.dec.seqHdr
	if skip || fh.CodedLossless || !sh.EnableCDEF || fh.AllowIntraBC || fh.CDEF.Bits == 0 {
		return
	}
	r := miRow & ^15
	c := miCol & ^15
	key := [2]int{r, c}
	if td.cdefRead[key] {
		return
	}
	td.cdefRead[key] = true
	td.sc.ReadLiteral(fh.CDEF.Bits)
}

// readDeltaQLF reads delta Q and delta LF values.
func (td *tileDecoder) readDeltaQLF(bSize int, skip bool) {
	fh := td.dec.frameHdr
	if !fh.DeltaQPresent || !td.readDeltas {
		return
	}

	sbSize := Block64x64
	if td.dec.seqHdr.Use128x128Superblock {
		sbSize = Block128x128
	}
	if bSize == sbSize && skip {
		return
	}

	// Delta Q.
	deltaQAbs := td.readDeltaAbsValue(td.cdf.DeltaQ)
	deltaQSign := 0
	if deltaQAbs > 0 {
		if td.sc.ReadBoolEqui() {
			deltaQSign = -1
		} else {
			deltaQSign = 1
		}
	}
	if deltaQAbs > 0 {
		td.currentQIdx += deltaQSign * deltaQAbs
		if td.currentQIdx < 1 {
			td.currentQIdx = 1
		}
		if td.currentQIdx > 255 {
			td.currentQIdx = 255
		}
	}

	// Delta LF.
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
			dlAbs := td.sc.ReadSymbol(td.cdf.DeltaLF[idx], 4)
			dlAbsVal := int(dlAbs)
			if dlAbs == 3 {
				n := td.sc.ReadLiteral(3)
				nBits := int(n) + 1
				extra := td.sc.ReadLiteral(nBits)
				dlAbsVal = int(extra) + 1 + (1 << nBits)
			}
			if dlAbsVal > 0 {
				td.sc.ReadBoolEqui()
			}
		}
	}

	td.readDeltas = false
}

// readDeltaAbsValue reads a delta absolute value using CDF + exponential extension.
func (td *tileDecoder) readDeltaAbsValue(cdf []uint16) int {
	sym := td.sc.ReadSymbol(cdf, 4)
	if sym < 3 {
		return sym
	}
	n := td.sc.ReadLiteral(3)
	nBits := int(n) + 1
	extra := td.sc.ReadLiteral(nBits)
	return int(extra) + 1 + (1 << nBits)
}

// readCfLAlphas reads CfL sign and alpha magnitudes.
func (td *tileDecoder) readCfLAlphas(bi *blockInfo) {
	signs := td.sc.ReadSymbol(td.cdf.CfLSign, 8)
	signU := signs / 3
	signV := signs % 3
	bi.cflSignU = signU
	bi.cflSignV = signV
	if signU != 0 {
		ctx := signV*2 + (signU - 1)
		bi.cflAlphaU = td.sc.ReadSymbol(td.cdf.CfLAlpha[ctx], 16) + 1
	}
	if signV != 0 {
		ctx := signU*2 + (signV - 1)
		bi.cflAlphaV = td.sc.ReadSymbol(td.cdf.CfLAlpha[ctx], 16) + 1
	}
}

// readPaletteModeInfo reads palette flags from the bitstream.
func (td *tileDecoder) readPaletteModeInfo(bSize int, bi *blockInfo) {
	fh := td.dec.frameHdr
	sh := td.dec.seqHdr
	if !fh.AllowScreenContentTools || !blockAllowsPalette(bSize) {
		return
	}

	if bi.yMode == IntraDC {
		bsCtx := bsizeToPaletteCtx(bSize)
		if td.sc.ReadSymbol(td.cdf.PaletteY[bsCtx][0], 2) == 1 {
			td.readPaletteColors(bi, 0)
		}
	}

	hasChroma := sh.Color.NumPlanes > 1
	if hasChroma && bi.uvMode == IntraDC {
		if td.sc.ReadSymbol(td.cdf.PaletteUV[0], 2) == 1 {
			td.readPaletteColors(bi, 1)
		}
	}
}

// readPaletteColors reads the palette size and color entries.
func (td *tileDecoder) readPaletteColors(bi *blockInfo, plane int) {
	bitDepth := td.dec.seqHdr.Color.BitDepth
	bsCtx := bsizeToPaletteCtx(bi.bSize)

	var sizeCDF []uint16
	if plane == 0 {
		sizeCDF = td.cdf.PaletteSizeY[bsCtx]
	} else {
		sizeCDF = td.cdf.PaletteSizeUV[bsCtx]
	}
	palSize := td.sc.ReadSymbol(sizeCDF, 7) + 2

	colors := make([]uint8, palSize)
	val := td.sc.ReadLiteral(bitDepth)
	colors[0] = uint8(val)
	prev := int(val)
	if palSize > 1 {
		bitsVal := td.sc.ReadLiteral(2)
		bits := bitDepth - 3 + int(bitsVal)
		maxVal := (1 << bitDepth) - 1
		isLuma := plane == 0
		for i := 1; i < palSize; i++ {
			delta := td.sc.ReadLiteral(bits)
			d := int(delta)
			if isLuma {
				d++
			}
			prev = prev + d
			if prev > maxVal {
				prev = maxVal
			}
			colors[i] = uint8(prev)
			if isLuma {
				remaining := maxVal - prev - 1
				if remaining < 0 {
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
}

// readFilterIntraModeInfo reads filter intra mode info.
func (td *tileDecoder) readFilterIntraModeInfo(bSize int, bi *blockInfo) {
	sh := td.dec.seqHdr
	if !sh.EnableFilterIntra || bi.yMode != IntraDC || bi.paletteSizeY > 0 {
		return
	}
	w := BlockWidth[bSize]
	h := BlockHeight[bSize]
	maxDim := w
	if h > maxDim {
		maxDim = h
	}
	if maxDim > 32 {
		return
	}

	bi.useFilterIntra = td.sc.ReadSymbol(td.cdf.UseFilterIntra[bSize], 2) == 1
	if bi.useFilterIntra {
		bi.filterIntraMode = td.sc.ReadSymbol(td.cdf.FilterIntraMode, 5)
	}
}

// isCfLAllowed returns true if Chroma-from-Luma is allowed for this block.
func (td *tileDecoder) isCfLAllowed(bSize int) bool {
	sh := td.dec.seqHdr
	if sh.Color.SubsamplingX != 1 || sh.Color.SubsamplingY != 1 || sh.Color.MonoChrome {
		return false
	}
	return BlockWidth[bSize] <= 32 && BlockHeight[bSize] <= 32
}

// blockAllowsPalette returns true if palette mode is allowed.
func blockAllowsPalette(bSize int) bool {
	w := BlockWidth[bSize]
	h := BlockHeight[bSize]
	return w >= 8 && h >= 8 && w <= 64 && h <= 64
}

// bsizeToPaletteCtx maps block size to palette CDF context (0-6).
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

// txPartIdx computes the flat index into TXSize (txpart) CDF array.
func txPartIdx(maxTx, depth int) int {
	if maxTx == 1 {
		return 0
	}
	return 2*(maxTx-2) + 1 + depth
}

// getTXSize determines the transform size for the current block.
// Reads TX partition decisions with above/left neighbor context.
func (td *tileDecoder) getTXSize(bSize int) int {
	txMode := td.dec.frameHdr.TXMode
	maxTx := TXSizeForBlockSize[bSize]

	switch txMode {
	case txModeOnly4x4:
		return TX4x4
	case txModeLargest:
		return maxTx
	case txModeSelect:
		maxDepth := maxTx
		if maxDepth > 2 {
			maxDepth = 2
		}
		if maxDepth == 0 {
			return TX4x4
		}
		txDepth := 0
		for d := 0; d < maxDepth; d++ {
			idx := txPartIdx(maxTx, d)
			if idx > 6 {
				idx = 6
			}
			// TX partition context from above/left neighbors.
			txCtx := 0 // default context
			split := td.sc.ReadSymbol(td.cdf.TXSize[idx][txCtx], 2)
			if split == 0 {
				break
			}
			txDepth++
		}
		result := maxTx - txDepth
		if result < TX4x4 {
			result = TX4x4
		}
		return result
	default:
		return maxTx
	}
}

// dav1d tx_types_per_set mapping tables.
var txTypesIntra2 = [5]int{
	TxTypeIDENTITY_IDENTITY,
	TxTypeDCT_DCT,
	TxTypeADST_ADST,
	TxTypeADST_DCT,
	TxTypeDCT_ADST,
}

var txTypesIntra1 = [7]int{
	TxTypeIDENTITY_IDENTITY,
	TxTypeDCT_DCT,
	TxTypeIDENTITY_DCT,
	TxTypeDCT_IDENTITY,
	TxTypeADST_ADST,
	TxTypeADST_DCT,
	TxTypeDCT_ADST,
}

// readTXType reads or derives the TX type for an intra block.
// Called from coeffs.go per-TXB, not per-block.
func (td *tileDecoder) readTXType(yMode, txSize int, skip bool) int {
	fh := td.dec.frameHdr

	if skip || fh.CodedLossless {
		return TxTypeDCT_DCT
	}

	if txSize >= TX32x32 {
		return TxTypeDCT_DCT
	}

	modeCtx := yMode
	if modeCtx >= NumIntraModes {
		modeCtx = 0
	}

	if fh.ReducedTXSet || txSize == TX16x16 {
		minCtx := txSize
		if minCtx > 2 {
			minCtx = 2
		}
		cdf := td.cdf.IntraTXType2[minCtx][modeCtx]
		if cdf == nil {
			return TxTypeDCT_DCT
		}
		sym := td.sc.ReadSymbol(cdf, 5)
		if sym < len(txTypesIntra2) {
			return txTypesIntra2[sym]
		}
		return TxTypeDCT_DCT
	}

	minCtx := txSize
	if minCtx > 1 {
		minCtx = 1
	}
	cdf := td.cdf.IntraTXType1[minCtx][modeCtx]
	if cdf == nil {
		return TxTypeDCT_DCT
	}
	sym := td.sc.ReadSymbol(cdf, 7)
	if sym < len(txTypesIntra1) {
		return txTypesIntra1[sym]
	}
	return TxTypeDCT_DCT
}

// intraModeToTxType derives the TX type from the intra prediction mode.
func intraModeToTxType(mode, txSize int) int {
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
	return 0
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

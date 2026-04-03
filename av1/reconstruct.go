package av1

// reconstruct.go

// decodeBlock decodes a single coding block and reconstructs pixels.
func (td *tileDecoder) decodeBlock(miRow, miCol, bSize int) error {
	if miRow >= td.miRowEnd || miCol >= td.miColEnd {
		return nil
	}
	if bSize < 0 || bSize >= NumBlockSizes {
		return nil
	}

	td.blockCount++

	// Decode mode info (intra mode, TX info, skip).
	bi, err := td.decodeModeInfo(miRow, miCol, bSize)
	if err != nil {
		return err
	}

	// Capture first block diagnostics.
	if td.blockCount == 1 && td.dec.diagFirstBlock == nil {
		td.dec.diagFirstBlock = bi
	}

	bw := BlockWidth[bSize]
	bh := BlockHeight[bSize]
	txSize := bi.txSize
	txW := TXWidth[txSize]
	txH := TXHeight[txSize]

	// Pixel coordinates of block origin.
	pixX := miCol * 4
	pixY := miRow * 4

	// Handle palette mode: read palette indices from bitstream and fill pixels.
	// Per spec: skip=true means no palette tokens AND no coefficients.
	if bi.paletteSizeY > 0 {
		td.decodePaletteTokens(0, pixX, pixY, bw, bh, bi.paletteSizeY, bi.paletteColorsY)
	} else {
		// Reconstruct Y plane (plane 0) with prediction + coefficients.
		for ty := 0; ty < bh; ty += txH {
			for tx := 0; tx < bw; tx += txW {
				py := pixY + ty
				px := pixX + tx
				if err := td.reconstructTXB(0, px, py, txSize, bi.yMode, bi); err != nil {
					return err
				}
			}
		}
	}

	// Reconstruct chroma planes (Cb=1, Cr=2) with 4:2:0 subsampling.
	chromaTXSize := txSize
	if chromaTXSize > TX4x4 {
		chromaTXSize--
	}
	chromaTXW := TXWidth[chromaTXSize]
	chromaTXH := TXHeight[chromaTXSize]
	chromaBW := bw / 2
	chromaBH := bh / 2
	if chromaBW < 4 {
		chromaBW = 4
	}
	if chromaBH < 4 {
		chromaBH = 4
	}

	chromaMode := bi.uvMode
	if chromaMode == UV_CFL_PRED {
		chromaMode = IntraDC
	}

	if bi.paletteSizeUV > 0 {
		td.decodePaletteTokens(1, pixX/2, pixY/2, chromaBW, chromaBH, bi.paletteSizeUV, bi.paletteColorsUV)
		td.decodePaletteTokens(2, pixX/2, pixY/2, chromaBW, chromaBH, bi.paletteSizeUV, bi.paletteColorsUV)
	} else {
		for plane := 1; plane <= 2; plane++ {
			for ty := 0; ty < chromaBH; ty += chromaTXH {
				for tx := 0; tx < chromaBW; tx += chromaTXW {
					py := pixY/2 + ty
					px := pixX/2 + tx
					if err := td.reconstructTXB(plane, px, py, chromaTXSize, chromaMode, bi); err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}

// decodePaletteTokens reads palette color indices from the bitstream using
// diagonal wave-front scan with color_order reordering per the AV1 spec.
func (td *tileDecoder) decodePaletteTokens(plane, pixX, pixY, blockW, blockH, palSize int, palColors []uint8) {
	d := td.dec
	img := d.img

	var planeW, planeH, stride int
	var planeBuf []uint8
	switch plane {
	case 0:
		planeW, planeH, stride, planeBuf = img.Rect.Dx(), img.Rect.Dy(), img.YStride, img.Y
	case 1:
		planeW, planeH, stride, planeBuf = img.Rect.Dx()/2, img.Rect.Dy()/2, img.CStride, img.Cb
	case 2:
		planeW, planeH, stride, planeBuf = img.Rect.Dx()/2, img.Rect.Dy()/2, img.CStride, img.Cr
	}

	effW := blockW
	effH := blockH
	if pixX+effW > planeW {
		effW = planeW - pixX
	}
	if pixY+effH > planeH {
		effH = planeH - pixY
	}
	if effW <= 0 || effH <= 0 {
		return
	}

	// Palette index map (blockW x blockH).
	palIdx := make([]uint8, blockW*blockH)

	// First pixel: read uniform(palSize) per spec.
	// Use a uniform CDF with palSize symbols for exact bit consumption match.
	if palSize > 1 {
		uniformCDF := make([]uint16, palSize)
		for i := 0; i < palSize-1; i++ {
			uniformCDF[i] = 32768 - uint16((i+1)*32768/palSize)
		}
		v, err := td.sc.ReadSymbol(uniformCDF, palSize)
		if err == nil && v < palSize {
			palIdx[0] = uint8(v)
		}
	}

	// Palette color index CDF: palSize-1 symbols (color_order puts most likely first).
	palSizeIdx := palSize - 2
	if palSizeIdx < 0 {
		palSizeIdx = 0
	}
	if palSizeIdx > 6 {
		palSizeIdx = 6
	}
	nSyms := palSize - 1
	if nSyms < 1 {
		nSyms = 1
	}
	// Fresh CDF copies per context for adaptive palette index reading.
	var localCDFs [5][]uint16
	for ctx := 0; ctx < 5; ctx++ {
		src := td.cdf.PaletteColorIdx[palSizeIdx][ctx]
		cdf := make([]uint16, nSyms)
		for i := 0; i < nSyms-1 && i < len(src); i++ {
			cdf[i] = src[i]
		}
		localCDFs[ctx] = cdf
	}

	// Diagonal wave-front scan per spec with adaptive CDF reading.
	for diag := 1; diag < blockW+blockH-1; diag++ {
		jStart := diag
		if jStart >= blockW {
			jStart = blockW - 1
		}
		jEnd := diag - blockH + 1
		if jEnd < 0 {
			jEnd = 0
		}
		for j := jStart; j >= jEnd; j-- {
			i := diag - j
			var order [8]uint8
			ctx := td.palOrderCtx(palIdx, blockW, i, j, &order, palSize)

			colorIdx, err := td.sc.ReadSymbol(localCDFs[ctx], nSyms)
			if err != nil {
				return
			}
			if colorIdx >= palSize {
				colorIdx = palSize - 1
			}
			palIdx[i*blockW+j] = order[colorIdx]
		}
	}

	// Write palette colors to plane buffer.
	for r := 0; r < effH; r++ {
		for c := 0; c < effW; c++ {
			idx := int(palIdx[r*blockW+c])
			color := uint8(128)
			if idx < len(palColors) {
				color = palColors[idx]
			}
			planeBuf[(pixY+r)*stride+(pixX+c)] = color
		}
	}
}

// palOrderCtx builds the color_order array and returns the CDF context
// for a palette pixel at position (i, j), based on its neighbors.
func (td *tileDecoder) palOrderCtx(palIdx []uint8, stride, i, j int, order *[8]uint8, palSize int) int {
	haveTop := i > 0
	haveLeft := j > 0

	var mask uint32
	oIdx := 0
	ctx := 0

	add := func(v uint8) {
		order[oIdx] = v
		oIdx++
		mask |= 1 << v
	}

	if !haveLeft && !haveTop {
		ctx = 0
		// shouldn't happen (first pixel is handled separately)
	} else if !haveLeft {
		ctx = 0
		add(palIdx[(i-1)*stride+j])
	} else if !haveTop {
		ctx = 0
		add(palIdx[i*stride+(j-1)])
	} else {
		l := palIdx[i*stride+(j-1)]
		t := palIdx[(i-1)*stride+j]
		tl := palIdx[(i-1)*stride+(j-1)]
		sameAll := t == l && t == tl
		sameTL := t == l
		sameTTL := t == tl
		sameLTL := l == tl

		if sameAll {
			ctx = 4
			add(t)
		} else if sameTL {
			ctx = 3
			add(t)
			add(tl)
		} else if sameTTL || sameLTL {
			ctx = 2
			add(tl)
			if sameTTL {
				add(l)
			} else {
				add(t)
			}
		} else {
			ctx = 1
			if t < l {
				add(t)
				add(l)
			} else {
				add(l)
				add(t)
			}
			add(tl)
		}
	}

	// Fill remaining order entries with colors not yet in order.
	for bit := 0; bit < palSize && oIdx < 8; bit++ {
		if mask&(1<<bit) == 0 {
			order[oIdx] = uint8(bit)
			oIdx++
		}
	}

	return ctx
}

// reconstructTXB reconstructs a single transform block.
func (td *tileDecoder) reconstructTXB(plane, pixX, pixY, txSize, mode int, bi *blockInfo) error {
	txW := TXWidth[txSize]
	txH := TXHeight[txSize]
	d := td.dec
	img := d.img

	// Determine plane dimensions and data slice.
	var planeW, planeH, stride int
	var planeBuf []uint8

	switch plane {
	case 0:
		planeW = img.Rect.Dx()
		planeH = img.Rect.Dy()
		stride = img.YStride
		planeBuf = img.Y
	case 1:
		planeW = img.Rect.Dx() / 2
		planeH = img.Rect.Dy() / 2
		stride = img.CStride
		planeBuf = img.Cb
	case 2:
		planeW = img.Rect.Dx() / 2
		planeH = img.Rect.Dy() / 2
		stride = img.CStride
		planeBuf = img.Cr
	}

	// Bounds check: ensure the block fits in the plane.
	if pixX >= planeW || pixY >= planeH {
		return nil
	}
	effW := txW
	effH := txH
	if pixX+effW > planeW {
		effW = planeW - pixX
	}
	if pixY+effH > planeH {
		effH = planeH - pixY
	}

	// Gather reference samples for intra prediction.
	above := make([]uint8, txW)
	left := make([]uint8, txH)
	var topLeft uint8 = 128
	haveAbove := pixY > 0
	haveLeft := pixX > 0

	if haveAbove {
		for i := 0; i < txW; i++ {
			x := pixX + i
			if x < planeW {
				above[i] = planeBuf[(pixY-1)*stride+x]
			} else {
				above[i] = above[0]
			}
		}
	} else {
		for i := range above {
			above[i] = 128
		}
	}

	if haveLeft {
		for i := 0; i < txH; i++ {
			y := pixY + i
			if y < planeH {
				left[i] = planeBuf[y*stride+(pixX-1)]
			} else {
				left[i] = left[0]
			}
		}
	} else {
		for i := range left {
			left[i] = 128
		}
	}

	if haveAbove && haveLeft {
		topLeft = planeBuf[(pixY-1)*stride+(pixX-1)]
	} else if haveAbove {
		topLeft = above[0]
	} else if haveLeft {
		topLeft = left[0]
	}

	// Intra predict.
	predBuf := make([]uint8, txW*txH)
	PredictIntra(predBuf, txW, mode, txW, txH, above, left, topLeft, haveAbove, haveLeft)

	if bi.skip {
		// Skip: write prediction directly to frame, no residual.
		for r := 0; r < effH; r++ {
			for c := 0; c < effW; c++ {
				planeBuf[(pixY+r)*stride+(pixX+c)] = predBuf[r*txW+c]
			}
		}
		td.updateNzCtx(plane, pixX, pixY, txW, txH, false)
		td.updateDCSignCtx(plane, pixX, pixY, txW, txH, 0)
		if plane == 0 {
			td.skipCount++
		}
		return nil
	}

	// Decode coefficients (TX type read once per block in decodeModeInfo).
	td.txbCount++
	coeffs, err := td.decodeCoeffs(plane, txSize, bi.txType, bi.bSize, pixX, pixY)
	if err != nil {
		return err
	}

	// Check if all zero.
	allZero := true
	for _, c := range coeffs {
		if c != 0 {
			allZero = false
			break
		}
	}

	// Update DC sign context: 0=zero, 1=positive, 2=negative.
	dcSign := 0
	if len(coeffs) > 0 && coeffs[0] > 0 {
		dcSign = 1
	} else if len(coeffs) > 0 && coeffs[0] < 0 {
		dcSign = 2
	}
	td.updateDCSignCtx(plane, pixX, pixY, txW, txH, dcSign)

	if !allZero {
		td.txbNzCount++
		// Dequantize using current Q index (BaseQIdx + delta Q).
		fh := d.frameHdr
		qIdx := td.currentQIdx

		var dcQ, acQ int
		switch plane {
		case 0:
			dcQ = GetDCQuant(qIdx + fh.Quant.DeltaQYDc)
			acQ = GetACQuant(qIdx)
		case 1:
			dcQ = GetDCQuant(qIdx + fh.Quant.DeltaQUDc)
			acQ = GetACQuant(qIdx + fh.Quant.DeltaQUAc)
		case 2:
			dcQ = GetDCQuant(qIdx + fh.Quant.DeltaQVDc)
			acQ = GetACQuant(qIdx + fh.Quant.DeltaQVAc)
		}

		// Capture first TXB diagnostics (before dequant).
		if d.diagFirstTXBCoeffs == nil && plane == 0 {
			snap := make([]int32, len(coeffs))
			copy(snap, coeffs)
			d.diagFirstTXBCoeffs = snap
			d.diagFirstTXBDCQ = dcQ
			d.diagFirstTXBACQ = acQ
		}

		DequantCoeffs(coeffs, dcQ, acQ)

		// Inverse transform.
		InverseTransform2D(coeffs, txW, txH, bi.txType)
	}

	// Add residual to prediction and write to frame.
	for r := 0; r < effH; r++ {
		for c := 0; c < effW; c++ {
			val := int(predBuf[r*txW+c]) + int(coeffs[r*txW+c])
			if val < 0 {
				val = 0
			}
			if val > 255 {
				val = 255
			}
			planeBuf[(pixY+r)*stride+(pixX+c)] = uint8(val)
		}
	}

	td.updateNzCtx(plane, pixX, pixY, txW, txH, !allZero)
	return nil
}

// applyCfL adds the scaled luma AC component to the chroma DC prediction.
// Per AV1 spec Section 7.11.5: CfL prediction.
// pixX, pixY are chroma pixel coordinates. The corresponding luma is at 2*pixX, 2*pixY.
func (td *tileDecoder) applyCfL(predBuf []uint8, txW, txH, pixX, pixY, alpha, sign int) {
	img := td.dec.img
	lumaStride := img.YStride
	lumaW := img.Rect.Dx()
	lumaH := img.Rect.Dy()

	// Compute average luma value over the block (for DC subtraction).
	var lumaSum int
	count := 0
	for r := 0; r < txH; r++ {
		for c := 0; c < txW; c++ {
			ly := pixY*2 + r*2
			lx := pixX*2 + c*2
			if ly < lumaH && lx < lumaW {
				// Average of 2x2 luma block.
				var s int
				n := 0
				for dr := 0; dr < 2; dr++ {
					for dc := 0; dc < 2; dc++ {
						ry := ly + dr
						rx := lx + dc
						if ry < lumaH && rx < lumaW {
							s += int(img.Y[ry*lumaStride+rx])
							n++
						}
					}
				}
				if n > 0 {
					lumaSum += s / n
					count++
				}
			}
		}
	}
	if count == 0 {
		return
	}
	lumaDC := lumaSum / count

	// Apply CfL: for each chroma pixel, add alpha * (luma_subsampled - lumaDC).
	// sign: 1 = positive alpha, 2 = negative alpha.
	signMul := 1
	if sign == 2 {
		signMul = -1
	}

	for r := 0; r < txH; r++ {
		for c := 0; c < txW; c++ {
			ly := pixY*2 + r*2
			lx := pixX*2 + c*2
			if ly >= lumaH || lx >= lumaW {
				continue
			}
			// Subsample luma: average of 2x2 block.
			var s int
			n := 0
			for dr := 0; dr < 2; dr++ {
				for dc := 0; dc < 2; dc++ {
					ry := ly + dr
					rx := lx + dc
					if ry < lumaH && rx < lumaW {
						s += int(img.Y[ry*lumaStride+rx])
						n++
					}
				}
			}
			lumaVal := s / n
			ac := lumaVal - lumaDC

			// Scale: alpha ranges 1-16, scale is alpha/16 approximately.
			// Per spec: scaled = clip(pred + ((alpha * ac + 32) >> 6), 0, 255)
			// But alpha from CDF is 1-16, and the scaling in the spec uses a different convention.
			// Simplified: use alpha directly as the scaling factor.
			adj := (signMul * alpha * ac + 32) >> 6
			val := int(predBuf[r*txW+c]) + adj
			if val < 0 {
				val = 0
			}
			if val > 255 {
				val = 255
			}
			predBuf[r*txW+c] = uint8(val)
		}
	}
}

// getNzCtx returns whether the above and left neighbors have nonzero coefficients.
func (td *tileDecoder) getNzCtx(plane, pixX, pixY, txW, txH int) (aboveNz, leftNz bool) {
	var miX, miY int
	if plane == 0 {
		miX = pixX / 4
		miY = pixY / 4
	} else {
		miX = pixX / 4 // chroma MI is half of luma MI, but stored at same MI units
		miY = pixY / 4
	}

	// Check above.
	aboveIdx := miX - td.miColStart
	if plane > 0 {
		aboveIdx = pixX / 4
	}
	if aboveIdx >= 0 && aboveIdx < len(td.aboveNzCtx[plane]) {
		aboveNz = td.aboveNzCtx[plane][aboveIdx]
	}

	// Check left.
	leftIdx := miY - td.miRowStart
	if plane > 0 {
		leftIdx = pixY / 4
	}
	if leftIdx >= 0 && leftIdx < len(td.leftNzCtx[plane]) {
		leftNz = td.leftNzCtx[plane][leftIdx]
	}

	return
}

// updateNzCtx updates the nonzero coefficient context.
func (td *tileDecoder) updateNzCtx(plane, pixX, pixY, txW, txH int, hasNz bool) {
	// Update above context.
	startX := pixX / 4
	if plane > 0 {
		startX = pixX / 4
	}
	nMI := txW / 4
	if nMI < 1 {
		nMI = 1
	}
	for i := startX; i < startX+nMI; i++ {
		idx := i
		if plane == 0 {
			idx = i - td.miColStart
		}
		if idx >= 0 && idx < len(td.aboveNzCtx[plane]) {
			td.aboveNzCtx[plane][idx] = hasNz
		}
	}

	// Update left context.
	startY := pixY / 4
	if plane > 0 {
		startY = pixY / 4
	}
	nMIH := txH / 4
	if nMIH < 1 {
		nMIH = 1
	}
	for i := startY; i < startY+nMIH; i++ {
		idx := i
		if plane == 0 {
			idx = i - td.miRowStart
		}
		if idx >= 0 && idx < len(td.leftNzCtx[plane]) {
			td.leftNzCtx[plane][idx] = hasNz
		}
	}
}

// maxTXSizeForDimensions returns the largest square TX size index
// that fits within the given pixel dimensions.
func maxTXSizeForDimensions(w, h int) int {
	minDim := w
	if h < minDim {
		minDim = h
	}
	switch {
	case minDim >= 64:
		return TX64x64
	case minDim >= 32:
		return TX32x32
	case minDim >= 16:
		return TX16x16
	case minDim >= 8:
		return TX8x8
	default:
		return TX4x4
	}
}

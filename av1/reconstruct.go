package av1

// decodeBlock decodes a single coding block and reconstructs pixels.
func (td *tileDecoder) decodeBlock(miRow, miCol, bSize int) error {
	if miRow >= td.miRowEnd || miCol >= td.miColEnd {
		return nil
	}
	if bSize < 0 || bSize >= NumBlockSizes {
		return nil
	}

	td.blockCount++

	bi, err := td.decodeModeInfo(miRow, miCol, bSize)
	if err != nil {
		return err
	}

	if td.blockCount == 1 && td.dec.diagFirstBlock == nil {
		td.dec.diagFirstBlock = bi
	}

	bw := BlockWidth[bSize]
	bh := BlockHeight[bSize]
	txSize := bi.txSize
	txW := TXWidth[txSize]
	txH := TXHeight[txSize]

	pixX := miCol * 4
	pixY := miRow * 4

	// Handle palette mode.
	if bi.paletteSizeY > 0 {
		td.decodePaletteTokens(0, pixX, pixY, bw, bh, bi.paletteSizeY, bi.paletteColorsY)
	} else {
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

	// Chroma planes with 4:2:0 subsampling.
	chromaTXSize := getChromaTXSize(txSize, bSize)
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

// getChromaTXSize maps luma TX size to chroma TX size for 4:2:0.
func getChromaTXSize(lumaTXSize, bSize int) int {
	chromaW := BlockWidth[bSize] / 2
	chromaH := BlockHeight[bSize] / 2
	if chromaW < 4 {
		chromaW = 4
	}
	if chromaH < 4 {
		chromaH = 4
	}
	minDim := chromaW
	if chromaH < minDim {
		minDim = chromaH
	}

	maxChromaTX := TX4x4
	switch {
	case minDim >= 64:
		maxChromaTX = TX64x64
	case minDim >= 32:
		maxChromaTX = TX32x32
	case minDim >= 16:
		maxChromaTX = TX16x16
	case minDim >= 8:
		maxChromaTX = TX8x8
	}

	if lumaTXSize <= maxChromaTX {
		return lumaTXSize
	}
	return maxChromaTX
}

// decodePaletteTokens reads palette color indices and fills pixels.
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

	palIdx := make([]uint8, blockW*blockH)

	if palSize > 1 {
		uniformCDF := make([]uint16, palSize)
		for i := 0; i < palSize-1; i++ {
			uniformCDF[i] = 32768 - uint16((i+1)*32768/palSize)
		}
		v := td.sc.ReadSymbol(uniformCDF, palSize)
		if v < palSize {
			palIdx[0] = uint8(v)
		}
	}

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

	var localCDFs [5][]uint16
	for ctx := 0; ctx < 5; ctx++ {
		src := td.cdf.PaletteColorIdx[palSizeIdx][ctx]
		cdf := make([]uint16, nSyms)
		for i := 0; i < nSyms-1 && i < len(src); i++ {
			cdf[i] = src[i]
		}
		localCDFs[ctx] = cdf
	}

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

			colorIdx := td.sc.ReadSymbol(localCDFs[ctx], nSyms)
			if colorIdx >= palSize {
				colorIdx = palSize - 1
			}
			palIdx[i*blockW+j] = order[colorIdx]
		}
	}

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

// palOrderCtx builds the color_order array and returns the CDF context.
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

	// Gather reference samples (extended for directional prediction).
	above := make([]uint8, txW*2)
	left := make([]uint8, txH*2)
	var topLeft uint8 = 128
	haveAbove := pixY > 0
	haveLeft := pixX > 0

	if haveAbove {
		for i := 0; i < txW*2; i++ {
			x := pixX + i
			if x < planeW {
				above[i] = planeBuf[(pixY-1)*stride+x]
			} else if i > 0 {
				above[i] = above[i-1]
			} else {
				above[i] = 128
			}
		}
	} else {
		for i := range above {
			above[i] = 128
		}
	}

	if haveLeft {
		for i := 0; i < txH*2; i++ {
			y := pixY + i
			if y < planeH {
				left[i] = planeBuf[y*stride+(pixX-1)]
			} else if i > 0 {
				left[i] = left[i-1]
			} else {
				left[i] = 128
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

	predBuf := make([]uint8, txW*txH)
	angleDelta := 0
	if plane == 0 {
		angleDelta = bi.angleDeltaY
	} else {
		angleDelta = bi.angleDeltaUV
	}
	PredictIntra(predBuf, txW, mode, txW, txH, above, left, topLeft, haveAbove, haveLeft, angleDelta)

	if bi.skip {
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

	// Decode coefficients — TX type is returned per TXB.
	td.txbCount++
	coeffs, txType := td.decodeCoeffs(plane, txSize, bi.bSize, bi, pixX, pixY)

	allZero := true
	for _, c := range coeffs {
		if c != 0 {
			allZero = false
			break
		}
	}

	dcSign := 0
	if len(coeffs) > 0 && coeffs[0] > 0 {
		dcSign = 1
	} else if len(coeffs) > 0 && coeffs[0] < 0 {
		dcSign = 2
	}
	td.updateDCSignCtx(plane, pixX, pixY, txW, txH, dcSign)

	if !allZero {
		td.txbNzCount++
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

		if d.diagFirstTXBCoeffs == nil && plane == 0 {
			snap := make([]int32, len(coeffs))
			copy(snap, coeffs)
			d.diagFirstTXBCoeffs = snap
			d.diagFirstTXBDCQ = dcQ
			d.diagFirstTXBACQ = acQ
		}

		DequantCoeffs(coeffs, dcQ, acQ)
		InverseTransform2D(coeffs, txW, txH, txType)
	}

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
func (td *tileDecoder) applyCfL(predBuf []uint8, txW, txH, pixX, pixY, alpha, sign int) {
	img := td.dec.img
	lumaStride := img.YStride
	lumaW := img.Rect.Dx()
	lumaH := img.Rect.Dy()

	var lumaSum int
	count := 0
	for r := 0; r < txH; r++ {
		for c := 0; c < txW; c++ {
			ly := pixY*2 + r*2
			lx := pixX*2 + c*2
			if ly < lumaH && lx < lumaW {
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

// getNzCtx returns whether above/left neighbors have nonzero coefficients.
func (td *tileDecoder) getNzCtx(plane, pixX, pixY, txW, txH int) (aboveNz, leftNz bool) {
	aboveIdx := pixX/4 - td.miColStart
	if plane > 0 {
		aboveIdx = pixX / 4
	}
	if aboveIdx >= 0 && aboveIdx < len(td.aboveNzCtx[plane]) {
		aboveNz = td.aboveNzCtx[plane][aboveIdx]
	}

	leftIdx := pixY/4 - td.miRowStart
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
	startX := pixX / 4
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

	startY := pixY / 4
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

// maxTXSizeForDimensions returns the largest square TX size that fits.
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

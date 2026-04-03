package av1

// decodeCoeffs reads the transform coefficients for one transform block.
// Returns coefficients in raster order (ready for inverse transform).
func (td *tileDecoder) decodeCoeffs(plane, txSize, txType, bSize, pixX, pixY int) ([]int32, error) {
	txW := TXWidth[txSize]
	txH := TXHeight[txSize]
	// For TX_64X64, coefficient scanning is limited to 32x32 (1024 coeffs).
	coeffW := txW
	coeffH := txH
	if coeffW > 32 {
		coeffW = 32
	}
	if coeffH > 32 {
		coeffH = 32
	}
	n := coeffW * coeffH

	// TX size context: 0 for 4x4, 1 for 8x8/16x16, 2 for 32x32+.
	txSizeCtx := txSizeToCtx(txSize)

	// Plane type: 0=luma, 1=chroma.
	planeType := 0
	if plane > 0 {
		planeType = 1
	}

	// TXB skip (all_zero) check.
	txSzCtx := txSizeToSqrCtx(txSize)
	txbSkipCtx := td.getTXBSkipCtx(plane, bSize, txSize, pixX, pixY)
	if txbSkipCtx >= 13 {
		txbSkipCtx = 12
	}
	allZero, err := td.sc.ReadSymbol(td.cdf.TXBSkip[txSzCtx][txbSkipCtx], 2)
	if err != nil {
		return nil, err
	}
	if allZero == 1 {
		return make([]int32, txW*txH), nil
	}

	// Decode EOB (end of block) position.
	eob, err := td.decodeEOB(txSize, planeType, n)
	if err != nil {
		return nil, err
	}
	// Capture EOB diagnostics for first TXB.
	if td.dec.diagFirstTXBCoeffs == nil && plane == 0 {
		td.dec.diagFirstTXBEOB = eob
		td.dec.diagFirstTXBN = n
	}

	scan := GetScanOrder(txSize, txType)
	levels := make([]int, n)

	// Use coeffW for context computation (TX64x64 uses 32x32 coeff grid).
	cW := coeffW

	// Decode coefficient levels in scan order matching dav1d's structure:
	// base_tok + inline hi_tok (range extension) in a single backward pass.
	brTxCtx := txSizeCtx
	if brTxCtx > 3 {
		brTxCtx = 3 // dav1d: imin(t_dim->ctx, 3)
	}

	// At EOB position: use CoeffBaseEOB CDF.
	eobCtx := clampCoeff(getCoeffBaseEOBCtx(eob, coeffW, coeffH), 0, 3)
	baseEOB, err := td.sc.ReadSymbol(td.cdf.CoeffBaseEOB[txSizeCtx][planeType][eobCtx], 3)
	if err != nil {
		return nil, err
	}
	levels[scan[eob]] = baseEOB + 1

	// Inline range extension at EOB if level >= 3.
	// Per dav1d: EOB hi_tok ctx is position-only: (x|y)>1 ? 14 : 7.
	if baseEOB+1 >= 3 {
		pos := scan[eob]
		row := pos / cW
		col := pos % cW
		brCtx := 7
		if (row | col) > 1 {
			brCtx = 14
		}
		for k := 0; k < 4; k++ {
			sym, err := td.sc.ReadSymbol(td.cdf.CoeffBaseRange[brTxCtx][planeType][brCtx], 4)
			if err != nil {
				return nil, err
			}
			levels[scan[eob]] += sym
			if sym < 3 {
				break
			}
		}
	}

	// Backward scan from eob-1 to 0: base_tok + inline hi_tok.
	for c := eob - 1; c >= 0; c-- {
		pos := scan[c]

		// DC (scan position 0) always uses ctx=0 for TX_CLASS_2D (per dav1d).
		var ctx int
		if c == 0 {
			ctx = 0
		} else {
			ctx = getCoeffBaseCtx(scan, levels, c, cW, coeffH)
			if ctx >= 41 {
				ctx = 40
			}
		}

		base, err := td.sc.ReadSymbol(td.cdf.CoeffBase[txSizeCtx][planeType][ctx], 4)
		if err != nil {
			return nil, err
		}
		levels[pos] = base

		// Inline range extension if base >= 3 (matching dav1d's hi_tok).
		if base >= 3 {
			var brCtx int
			if c == 0 {
				// DC hi_tok: magnitude-only, no position offset (per dav1d).
				mag := 0
				row := pos / cW
				col := pos % cW
				if col+1 < cW {
					mag += minInt(levels[row*cW+col+1], 15)
				}
				if row+1 < coeffH {
					mag += minInt(levels[(row+1)*cW+col], 15)
				}
				if row+1 < coeffH && col+1 < cW {
					mag += minInt(levels[(row+1)*cW+col+1], 15)
				}
				brCtx = minInt((mag+1)>>1, 6)
			} else {
				row := pos / cW
				col := pos % cW
				// AC hi_tok: per dav1d, y |= x before check.
				// For TX_CLASS_2D: ctx = (y > 1 ? 14 : 7) + mag_ctx
				// where y has been OR'd with x.
				yOrX := row | col
				mag := 0
				if col+1 < cW {
					mag += minInt(levels[row*cW+col+1], 15)
				}
				if row+1 < coeffH {
					mag += minInt(levels[(row+1)*cW+col], 15)
				}
				if row+1 < coeffH && col+1 < cW {
					mag += minInt(levels[(row+1)*cW+col+1], 15)
				}
				magCtx := minInt((mag+1)>>1, 6)
				if yOrX > 1 {
					brCtx = 14 + magCtx
				} else {
					brCtx = 7 + magCtx
				}
			}
			if brCtx > 20 {
				brCtx = 20
			}
			for k := 0; k < 4; k++ {
				sym, err := td.sc.ReadSymbol(td.cdf.CoeffBaseRange[brTxCtx][planeType][brCtx], 4)
				if err != nil {
					return nil, err
				}
				levels[pos] += sym
				if sym < 3 {
					break
				}
			}
		}
	}

	// Remaining bits (Golomb-coded) for levels with base_range maxed out.
	for c := 0; c <= eob; c++ {
		pos := scan[c]
		if levels[pos] >= 15 { // 3 + 4*3 = 15 is the max from base+range
			remainder, err := td.readGolomb()
			if err != nil {
				return nil, err
			}
			levels[pos] += int(remainder)
		}
	}

	// Signs. Output array is full txW*txH for inverse transform.
	fullN := txW * txH
	coeffs := make([]int32, fullN)

	// remapPos converts a position from the coeffW-wide scan grid to the
	// txW-wide output grid. Only differs for TX_64x64 where coeffW=32 < txW=64.
	remapPos := func(pos int) int {
		if coeffW == txW {
			return pos
		}
		row := pos / coeffW
		col := pos % coeffW
		return row*txW + col
	}

	// DC sign.
	if levels[0] > 0 {
		dcCtx := td.getDCSignCtx(plane, pixX, pixY, txW, txH)
		sign, err := td.sc.ReadSymbol(td.cdf.DCSign[planeType][dcCtx], 2)
		if err != nil {
			return nil, err
		}
		if sign == 1 {
			coeffs[0] = -int32(levels[0])
		} else {
			coeffs[0] = int32(levels[0])
		}
	}

	// AC signs.
	for c := 1; c <= eob; c++ {
		pos := scan[c]
		if levels[pos] > 0 {
			sign := td.sc.ReadBoolEqui()
			outPos := remapPos(pos)
			if sign {
				coeffs[outPos] = -int32(levels[pos])
			} else {
				coeffs[outPos] = int32(levels[pos])
			}
		}
	}

	return coeffs, nil
}

// decodeEOB reads the end-of-block position.
func (td *tileDecoder) decodeEOB(txSize, planeType, n int) (int, error) {
	var eobCDF []uint16
	var nsymbs int

	switch {
	case n <= 16:
		eobCDF = td.cdf.EOBMulti16[planeType]
		nsymbs = 5
	case n <= 32:
		eobCDF = td.cdf.EOBMulti32[planeType]
		nsymbs = 6
	case n <= 64:
		eobCDF = td.cdf.EOBMulti64[planeType]
		nsymbs = 7
	case n <= 128:
		eobCDF = td.cdf.EOBMulti128[planeType]
		nsymbs = 8
	case n <= 256:
		eobCDF = td.cdf.EOBMulti256[planeType]
		nsymbs = 9
	case n <= 512:
		eobCDF = td.cdf.EOBMulti512[planeType]
		nsymbs = 10
	default:
		eobCDF = td.cdf.EOBMulti1024[planeType]
		nsymbs = 11
	}

	cat, err := td.sc.ReadSymbol(eobCDF, nsymbs)
	if err != nil {
		return 0, err
	}

	// Convert category to EOB position using EOBExtra CDF for MSB.
	eob := td.eobCategoryToPos(cat, planeType)
	if eob >= n {
		eob = n - 1
	}
	return eob, nil
}

// eobCategoryToPos converts an EOB category symbol to an actual position.
// Per AV1 spec: for cat >= 2, the MSB of the extra bits uses the EOBExtra CDF,
// and remaining bits are literal (equi-probability).
func (td *tileDecoder) eobCategoryToPos(cat, planeType int) int {
	if cat == 0 {
		return 0
	}
	if cat == 1 {
		return 1
	}
	extraBits := cat - 1
	base := 1 << extraBits

	// MSB from EOBExtra CDF (2-symbol adaptive).
	eobCtx := cat - 2
	if eobCtx >= 9 {
		eobCtx = 8
	}
	msb, err := td.sc.ReadSymbol(td.cdf.EOBExtra[eobCtx][planeType], 2)
	if err != nil {
		return base
	}

	var extra int
	if extraBits > 1 {
		// Remaining bits are literal (equi-probability).
		rem, err := td.sc.ReadLiteral(extraBits - 1)
		if err != nil {
			return base
		}
		extra = (msb << (extraBits - 1)) | int(rem)
	} else {
		extra = msb
	}
	return base + extra
}

// readGolomb reads a Golomb-coded remainder value.
func (td *tileDecoder) readGolomb() (uint32, error) {
	// Count leading zeros.
	var length int
	for {
		bit := td.sc.ReadBoolEqui()
		if !bit {
			break
		}
		length++
		if length > 20 {
			return 0, nil // Safety limit.
		}
	}
	if length == 0 {
		return 0, nil
	}
	val, err := td.sc.ReadLiteral(length)
	if err != nil {
		return 0, err
	}
	return (1 << length) - 1 + val, nil
}

// txSizeToSqrCtx maps TX size to the 5-value square TX size context for TXBSkip.
func txSizeToSqrCtx(txSize int) int {
	if txSize >= 0 && txSize < len(txSzSqrMap) {
		return txSzSqrMap[txSize]
	}
	return 0
}

// txSizeToCtx maps TX size to the 5-value context for coefficient CDFs.
// TX4x4→0, TX8x8→1, TX16x16→2, TX32x32→3, TX64x64→4.
// Matches dav1d's t_dim->ctx which is the identity mapping.
func txSizeToCtx(txSize int) int {
	return txSize // identity: TX4x4=0, TX8x8=1, TX16x16=2, TX32x32=3, TX64x64=4
}

// getTXBSkipCtx computes the context for the TXB skip (all_zero) CDF.
func (td *tileDecoder) getTXBSkipCtx(plane, bSize, txSize, pixX, pixY int) int {
	txMIW := TXWidth[txSize] / 4
	txMIH := TXHeight[txSize] / 4
	if txMIW < 1 {
		txMIW = 1
	}
	if txMIH < 1 {
		txMIH = 1
	}

	// Sum above NZ values across TX block width.
	ca := 0
	aboveBase := pixX / 4
	if plane == 0 {
		aboveBase -= td.miColStart
	}
	for i := aboveBase; i < aboveBase+txMIW; i++ {
		if i >= 0 && i < len(td.aboveNzCtx[plane]) && td.aboveNzCtx[plane][i] {
			ca++
		}
	}

	// Sum left NZ values across TX block height.
	cl := 0
	leftBase := pixY / 4
	if plane == 0 {
		leftBase -= td.miRowStart
	}
	for i := leftBase; i < leftBase+txMIH; i++ {
		if i >= 0 && i < len(td.leftNzCtx[plane]) && td.leftNzCtx[plane][i] {
			cl++
		}
	}

	if plane > 0 {
		// Chroma
		bw := MISizeWide[bSize] / 2
		bh := MISizeTall[bSize] / 2
		if bw < 1 {
			bw = 1
		}
		if bh < 1 {
			bh = 1
		}
		notOneBlk := 0
		if bw > txMIW || bh > txMIH {
			notOneBlk = 1
		}
		if notOneBlk != 0 {
			sum := ca + cl
			if sum > 2 {
				sum = 2
			}
			return 7 + notOneBlk + sum // 8, 9, 10
		}
		return minInt(ca+cl, 2) + 10 // 10, 11, 12
	}

	// Luma: use multi-neighbor sum with dav1d formula
	sum := ca + cl
	if sum > 4 {
		sum = 4
	}
	notOneBlk := 0
	bw := MISizeWide[bSize]
	bh := MISizeTall[bSize]
	if bw > txMIW || bh > txMIH {
		notOneBlk = 1
	}
	return sum + notOneBlk*5 // 0-9
}

// getDCSignCtx computes the context for the DC sign CDF.
// Per dav1d get_dc_sign_ctx: sum above and left DC signs,
// return 0 (balanced), 1 (net negative), 2 (net positive).
func (td *tileDecoder) getDCSignCtx(plane, pixX, pixY, txW, txH int) int {
	sum := 0

	// Above contribution.
	startX := pixX / 4
	if plane == 0 {
		startX -= td.miColStart
	}
	nMI := txW / 4
	if nMI < 1 {
		nMI = 1
	}
	for i := startX; i < startX+nMI; i++ {
		if i >= 0 && i < len(td.aboveDCSign[plane]) {
			switch td.aboveDCSign[plane][i] {
			case 1:
				sum++
			case 2:
				sum--
			}
		}
	}

	// Left contribution.
	startY := pixY / 4
	if plane == 0 {
		startY -= td.miRowStart
	}
	nMIH := txH / 4
	if nMIH < 1 {
		nMIH = 1
	}
	for i := startY; i < startY+nMIH; i++ {
		if i >= 0 && i < len(td.leftDCSign[plane]) {
			switch td.leftDCSign[plane][i] {
			case 1:
				sum++
			case 2:
				sum--
			}
		}
	}

	if sum > 0 {
		return 2
	}
	if sum < 0 {
		return 1
	}
	return 0
}

// updateDCSignCtx updates the DC sign context arrays after decoding a transform block.
// sign: 0=zero, 1=positive, 2=negative.
func (td *tileDecoder) updateDCSignCtx(plane, pixX, pixY, txW, txH, sign int) {
	// Update above context.
	startX := pixX / 4
	if plane == 0 {
		startX -= td.miColStart
	}
	nMI := txW / 4
	if nMI < 1 {
		nMI = 1
	}
	for i := startX; i < startX+nMI; i++ {
		if i >= 0 && i < len(td.aboveDCSign[plane]) {
			td.aboveDCSign[plane][i] = sign
		}
	}

	// Update left context.
	startY := pixY / 4
	if plane == 0 {
		startY -= td.miRowStart
	}
	nMIH := txH / 4
	if nMIH < 1 {
		nMIH = 1
	}
	for i := startY; i < startY+nMIH; i++ {
		if i >= 0 && i < len(td.leftDCSign[plane]) {
			td.leftDCSign[plane][i] = sign
		}
	}
}

// getCoeffBaseEOBCtx computes the context for CoeffBaseEOB using scan index thresholds.
func getCoeffBaseEOBCtx(eob, coeffW, coeffH int) int {
	if eob == 0 {
		return 0
	}
	n := coeffW * coeffH
	if eob <= n/8 {
		return 1
	}
	if eob <= n/4 {
		return 2
	}
	return 3
}

// nzMapCtxOffset2D provides the position-based context offset for CoeffBase.
var nzMapCtxOffset2D = [5][5]int{
	{0, 1, 6, 6, 21}, {1, 6, 6, 21, 21},
	{6, 6, 21, 21, 21}, {6, 21, 21, 21, 21},
	{21, 21, 21, 21, 21},
}

// getCoeffBaseCtx computes the context for CoeffBase using the 5x5 offset table approach.
// txW and txH are the coefficient grid dimensions (capped at 32 for TX_64x64).
func getCoeffBaseCtx(scan []int, levels []int, c, txW, txH int) int {
	pos := scan[c]
	row := pos / txW
	col := pos % txW

	// Sum magnitudes of up to 5 neighbors, each capped at 3.
	mag := 0
	if col+1 < txW {
		mag += minInt(levels[row*txW+col+1], 3)
	}
	if row+1 < txH {
		mag += minInt(levels[(row+1)*txW+col], 3)
	}
	if row+1 < txH && col+1 < txW {
		mag += minInt(levels[(row+1)*txW+col+1], 3)
	}
	if col+2 < txW {
		mag += minInt(levels[row*txW+col+2], 3)
	}
	if row+2 < txH {
		mag += minInt(levels[(row+2)*txW+col], 3)
	}

	ctx := minInt((mag+1)>>1, 4)
	r := row
	if r > 4 {
		r = 4
	}
	cc := col
	if cc > 4 {
		cc = 4
	}
	return nzMapCtxOffset2D[r][cc] + ctx
}

// getCoeffBRCtx computes the context for CoeffBaseRange.
// txW and txH are the coefficient grid dimensions (capped at 32 for TX_64x64).
func getCoeffBRCtx(scan []int, levels []int, c, txW, txH int) int {
	pos := scan[c]
	row := pos / txW
	col := pos % txW

	mag := 0
	if col+1 < txW {
		mag += minInt(levels[row*txW+col+1], 15)
	}
	if row+1 < txH {
		mag += minInt(levels[(row+1)*txW+col], 15)
	}
	if row+1 < txH && col+1 < txW {
		mag += minInt(levels[(row+1)*txW+col+1], 15)
	}
	mag = minInt((mag+1)>>1, 6)

	if row == 0 && col == 0 {
		return mag
	}
	if row < 2 && col < 2 {
		return mag + 7
	}
	return mag + 14
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// clampCoeff clamps v to [lo, hi].
func clampCoeff(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

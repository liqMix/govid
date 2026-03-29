package av1

// decodeCoeffs reads the transform coefficients for one transform block.
// Returns coefficients in raster order (ready for inverse transform).
func (td *tileDecoder) decodeCoeffs(plane, txSize, txType, bSize, pixX, pixY int, aboveNz, leftNz bool) ([]int32, error) {
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
	txbSkipCtx := td.getTXBSkipCtx(plane, bSize, txSize, aboveNz, leftNz)
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
	scan := GetScanOrder(txSize, txType)
	levels := make([]int, n)

	// Use coeffW for context computation (TX64x64 uses 32x32 coeff grid).
	cW := coeffW

	// Decode coefficient levels in scan order.
	// At EOB position: use CoeffBaseEOB CDF.
	eobCtx := clampCoeff(getCoeffBaseEOBCtx(scan, levels, eob, cW), 0, 2)
	baseEOB, err := td.sc.ReadSymbol(td.cdf.CoeffBaseEOB[txSizeCtx][planeType][eobCtx], 3)
	if err != nil {
		return nil, err
	}
	levels[scan[eob]] = baseEOB + 1 // at EOB, level is at least 1

	// Scan from eob-1 down to 0.
	for c := eob - 1; c >= 0; c-- {
		pos := scan[c]
		ctx := getCoeffBaseCtx(scan, levels, c, cW, coeffH)
		if ctx >= 41 {
			ctx = 40
		}
		base, err := td.sc.ReadSymbol(td.cdf.CoeffBase[txSizeCtx][planeType][ctx], 4)
		if err != nil {
			return nil, err
		}
		levels[pos] = base // 0 means zero coeff
	}

	// Base range extension for levels >= 3.
	for c := 0; c <= eob; c++ {
		pos := scan[c]
		if levels[pos] >= 3 {
			brCtx := getCoeffBRCtx(scan, levels, c, cW, coeffH)
			if brCtx >= 21 {
				brCtx = 20
			}
			for k := 0; k < 4; k++ {
				sym, err := td.sc.ReadSymbol(td.cdf.CoeffBaseRange[txSizeCtx][planeType][brCtx], 4)
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
			sign, err := td.sc.ReadBool(128)
			if err != nil {
				return nil, err
			}
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

	// Convert category to EOB position.
	eob := eobCategoryToPos(cat, td)
	if eob >= n {
		eob = n - 1
	}
	return eob, nil
}

// eobCategoryToPos converts an EOB category symbol to an actual position.
// Category k means EOB is in range [2^(k-1), 2^k - 1] for k >= 2.
func eobCategoryToPos(cat int, td *tileDecoder) int {
	if cat == 0 {
		return 0
	}
	if cat == 1 {
		return 1
	}
	// For category k (k >= 2), read (k-1) extra bits.
	extraBits := cat - 1
	base := 1 << extraBits
	extra, err := td.sc.ReadLiteral(extraBits)
	if err != nil {
		return base
	}
	return base + int(extra)
}

// readGolomb reads a Golomb-coded remainder value.
func (td *tileDecoder) readGolomb() (uint32, error) {
	// Count leading zeros.
	var length int
	for {
		bit, err := td.sc.ReadBool(128)
		if err != nil {
			return 0, err
		}
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

// txSizeToCtx maps TX size to the 3-value context for coefficient CDFs.
func txSizeToCtx(txSize int) int {
	switch txSize {
	case TX4x4:
		return 0
	case TX8x8, TX16x16:
		return 1
	default:
		return 2
	}
}

// getTXBSkipCtx computes the context for the TXB skip (all_zero) CDF.
// Per dav1d get_skip_ctx: luma ctx = ca + cl + not_one_blk * 3 (0-5),
// chroma ctx = 7 + not_one_blk + min(ca+cl, 2) (7-12 when multi, 10-12 when single).
func (td *tileDecoder) getTXBSkipCtx(plane, bSize, txSize int, aboveNz, leftNz bool) int {
	ca := 0
	if aboveNz {
		ca = 1
	}
	cl := 0
	if leftNz {
		cl = 1
	}

	txMIW := TXWidth[txSize] / 4
	txMIH := TXHeight[txSize] / 4
	if txMIW < 1 {
		txMIW = 1
	}
	if txMIH < 1 {
		txMIH = 1
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
		return ca + cl + 10 // 10, 11, 12
	}

	// Luma
	bw := MISizeWide[bSize]
	bh := MISizeTall[bSize]
	notOneBlk := 0
	if bw > txMIW || bh > txMIH {
		notOneBlk = 1
	}
	return ca + cl + notOneBlk*3 // 0-5
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

// getCoeffBaseEOBCtx computes the context for CoeffBaseEOB.
func getCoeffBaseEOBCtx(scan []int, levels []int, eob, txW int) int {
	if eob == 0 {
		return 0
	}
	pos := scan[eob]
	row := pos / txW
	col := pos % txW
	if row == 0 && col == 0 {
		return 0
	}
	if row+col < 2 {
		return 1
	}
	return 2
}

// getCoeffBaseCtx computes the context for CoeffBase.
// txW and txH are the coefficient grid dimensions (capped at 32 for TX_64x64).
func getCoeffBaseCtx(scan []int, levels []int, c, txW, txH int) int {
	pos := scan[c]
	row := pos / txW
	col := pos % txW

	// Sum magnitudes of up to 5 neighbors (right, below, diagonal).
	mag := 0
	neighbors := [][2]int{{0, 1}, {1, 0}, {1, 1}, {0, 2}, {2, 0}}
	for _, nb := range neighbors {
		nr := row + nb[0]
		nc := col + nb[1]
		if nr < txH && nc < txW {
			mag += minInt(levels[nr*txW+nc], 3)
		}
	}
	mag = minInt(mag, 12)

	// Context offset based on position.
	var posCtx int
	if row == 0 && col == 0 {
		posCtx = 0
	} else if row+col < 2 {
		posCtx = 1
	} else if row+col < 4 {
		posCtx = 2
	} else {
		posCtx = 3
	}

	return posCtx*10 + minInt(mag, 9) + 1
}

// getCoeffBRCtx computes the context for CoeffBaseRange.
// txW and txH are the coefficient grid dimensions (capped at 32 for TX_64x64).
func getCoeffBRCtx(scan []int, levels []int, c, txW, txH int) int {
	pos := scan[c]
	row := pos / txW
	col := pos % txW

	mag := 0
	neighbors := [][2]int{{0, 1}, {1, 0}, {1, 1}}
	for _, nb := range neighbors {
		nr := row + nb[0]
		nc := col + nb[1]
		if nr < txH && nc < txW {
			mag += minInt(levels[nr*txW+nc], 6)
		}
	}
	mag = minInt(mag, 18)

	var posCtx int
	if row == 0 && col == 0 {
		posCtx = 0
	} else if row+col < 2 {
		posCtx = 7
	} else {
		posCtx = 14
	}

	return posCtx + minInt(mag/2, 6)
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

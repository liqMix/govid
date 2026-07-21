package av1

// dav1d lo_ctx_offsets[3][5][5] for base_tok context computation.
// [0]=square, [1]=wide (w>h), [2]=tall (w<h).
var loCtxOffsets = [3][5][5]int{
	{ // square
		{0, 1, 6, 6, 21},
		{1, 6, 6, 21, 21},
		{6, 6, 21, 21, 21},
		{6, 21, 21, 21, 21},
		{21, 21, 21, 21, 21},
	},
	{ // wide (w > h)
		{0, 16, 6, 6, 21},
		{16, 16, 6, 21, 21},
		{16, 16, 21, 21, 21},
		{16, 16, 21, 21, 21},
		{16, 16, 21, 21, 21},
	},
	{ // tall (w < h)
		{0, 11, 11, 11, 11},
		{11, 11, 11, 11, 11},
		{6, 6, 21, 21, 21},
		{6, 21, 21, 21, 21},
		{21, 21, 21, 21, 21},
	},
}

// decodeCoeffs reads the transform coefficients for one transform block.
// Returns coefficients in raster order and the TX type.
func (td *tileDecoder) decodeCoeffs(plane, txSize, bSize int, bi *blockInfo, pixX, pixY int) ([]int32, int) {
	txW := TXWidth[txSize]
	txH := TXHeight[txSize]
	coeffW := txW
	coeffH := txH
	if coeffW > 32 {
		coeffW = 32
	}
	if coeffH > 32 {
		coeffH = 32
	}
	n := coeffW * coeffH

	txSizeCtx := txSizeToCtx(txSize)
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
	allZero := td.sc.ReadSymbol(td.cdf.TXBSkip[txSzCtx][txbSkipCtx], 2)
	if allZero == 1 {
		return make([]int32, txW*txH), TxTypeDCT_DCT
	}

	// TX type — read per-TXB, after TXB skip (correct per dav1d/spec).
	txType := td.readTXType(bi.yMode, txSize, bi.skip)

	// Decode EOB position.
	eob := td.decodeEOB(txSize, txSizeCtx, planeType, n)
	if td.dec.diagFirstTXBCoeffs == nil && plane == 0 {
		td.dec.diagFirstTXBEOB = eob
		td.dec.diagFirstTXBN = n
	}

	scan := GetScanOrder(txSize, txType)
	levels := make([]int, n)
	cW := coeffW

	brTxCtx := txSizeCtx
	if brTxCtx > 3 {
		brTxCtx = 3
	}

	// At EOB position: use CoeffBaseEOB CDF.
	eobCtx := clampCoeff(getCoeffBaseEOBCtx(eob, coeffW, coeffH), 0, 3)
	baseEOB := td.sc.ReadSymbol(td.cdf.CoeffBaseEOB[txSizeCtx][planeType][eobCtx], 3)
	levels[scan[eob]] = baseEOB + 1

	// Inline range extension at EOB if level >= 3.
	if baseEOB+1 >= 3 {
		pos := scan[eob]
		row := pos / cW
		brCtx := 7
		if row > 0 {
			brCtx = 14
		}
		for k := 0; k < 4; k++ {
			sym := td.sc.ReadSymbol(td.cdf.CoeffBaseRange[brTxCtx][planeType][brCtx], 4)
			levels[scan[eob]] += sym
			if sym < 3 {
				break
			}
		}
	}

	// Backward scan from eob-1 to 0.
	for c := eob - 1; c >= 0; c-- {
		pos := scan[c]

		var ctx int
		if c == 0 {
			ctx = 0
		} else {
			ctx = td.getCoeffBaseCtxLo(levels, c, cW, coeffH, scan)
			if ctx >= 41 {
				ctx = 40
			}
		}

		base := td.sc.ReadSymbol(td.cdf.CoeffBase[txSizeCtx][planeType][ctx], 4)
		levels[pos] = base

		// Inline range extension if base >= 3.
		if base >= 3 {
			pos := scan[c]
			row := pos / cW
			col := pos % cW

			// BR context: sum 3 neighbors, each capped at 63.
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

			// Per dav1d: DC (row==0 && col==0) uses base 0, row==0 uses base 7, else base 14.
			var brCtx int
			if row == 0 && col == 0 {
				brCtx = magCtx
			} else if row > 0 {
				brCtx = 14 + magCtx
			} else {
				brCtx = 7 + magCtx
			}
			if brCtx > 20 {
				brCtx = 20
			}
			for k := 0; k < 4; k++ {
				sym := td.sc.ReadSymbol(td.cdf.CoeffBaseRange[brTxCtx][planeType][brCtx], 4)
				levels[pos] += sym
				if sym < 3 {
					break
				}
			}
		}
	}

	// Golomb-coded remainders for levels >= 15.
	for c := 0; c <= eob; c++ {
		pos := scan[c]
		if levels[pos] >= 15 {
			remainder := td.readGolomb()
			levels[pos] += int(remainder)
		}
	}

	// Signs.
	fullN := txW * txH
	coeffs := make([]int32, fullN)

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
		sign := td.sc.ReadSymbol(td.cdf.DCSign[planeType][dcCtx], 2)
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

	return coeffs, txType
}

// getCoeffBaseCtxLo computes the base_tok context using dav1d lo_ctx_offsets.
func (td *tileDecoder) getCoeffBaseCtxLo(levels []int, c, txW, txH int, scan []int) int {
	pos := scan[c]
	row := pos / txW
	col := pos % txW

	// Sum magnitudes of 5 neighbors, each capped at 3.
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

	// Use lo_ctx_offsets for position-based context.
	// Determine TX shape: 0=square, 1=wide, 2=tall.
	var shape int
	if txW == txH {
		shape = 0
	} else if txW > txH {
		shape = 1
	} else {
		shape = 2
	}

	r := row
	if r > 4 {
		r = 4
	}
	cc := col
	if cc > 4 {
		cc = 4
	}
	return loCtxOffsets[shape][r][cc] + ctx
}

// decodeEOB reads the end-of-block position.
func (td *tileDecoder) decodeEOB(txSize, txSizeCtx, planeType, n int) int {
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

	cat := td.sc.ReadSymbol(eobCDF, nsymbs)
	eob := td.eobCatToPos(cat, txSizeCtx, planeType)
	if eob >= n {
		eob = n - 1
	}
	return eob
}

// eobCatToPos converts an EOB category symbol to an actual position.
func (td *tileDecoder) eobCatToPos(cat, txSizeCtx, planeType int) int {
	if cat == 0 {
		return 0
	}
	if cat == 1 {
		return 1
	}
	extraBits := cat - 1
	base := 1 << extraBits

	eobCtx := cat - 2
	if eobCtx >= 9 {
		eobCtx = 8
	}
	if txSizeCtx > 4 {
		txSizeCtx = 4
	}
	msb := td.sc.ReadSymbol(td.cdf.EOBExtra[txSizeCtx][planeType][eobCtx], 2)

	var extra int
	if extraBits > 1 {
		rem := td.sc.ReadLiteral(extraBits - 1)
		extra = (msb << (extraBits - 1)) | int(rem)
	} else {
		extra = msb
	}
	return base + extra
}

// readGolomb reads a Golomb-coded remainder value.
func (td *tileDecoder) readGolomb() uint32 {
	var length int
	for {
		bit := td.sc.ReadBoolEqui()
		if !bit {
			break
		}
		length++
		if length > 20 {
			return 0
		}
	}
	if length == 0 {
		return 0
	}
	val := td.sc.ReadLiteral(length)
	return (1 << length) - 1 + val
}

// txSizeToSqrCtx maps TX size to 5-value square TX size context.
func txSizeToSqrCtx(txSize int) int {
	if txSize >= 0 && txSize < len(txSzSqrMap) {
		return txSzSqrMap[txSize]
	}
	return 0
}

// txSizeToCtx maps TX size to the 5-value context for coefficient CDFs.
func txSizeToCtx(txSize int) int {
	return txSize
}

// getTXBSkipCtx computes the context for TXB skip (all_zero) CDF.
func (td *tileDecoder) getTXBSkipCtx(plane, bSize, txSize, pixX, pixY int) int {
	txMIW := TXWidth[txSize] / 4
	txMIH := TXHeight[txSize] / 4
	if txMIW < 1 {
		txMIW = 1
	}
	if txMIH < 1 {
		txMIH = 1
	}

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
			return 7 + notOneBlk + sum
		}
		return minInt(ca+cl, 2) + 10
	}

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
	return sum + notOneBlk*5
}

// getDCSignCtx computes the context for DC sign CDF.
func (td *tileDecoder) getDCSignCtx(plane, pixX, pixY, txW, txH int) int {
	sum := 0

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

// updateDCSignCtx updates the DC sign context arrays.
func (td *tileDecoder) updateDCSignCtx(plane, pixX, pixY, txW, txH, sign int) {
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
func getCoeffBaseEOBCtx(eob, coeffW, coeffH int) int {
	if eob == 0 {
		return 0
	}
	n := coeffW * coeffH
	ctx := 1
	if eob > n/4 {
		ctx++
	}
	if eob > n/2 {
		ctx++
	}
	return ctx
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clampCoeff(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

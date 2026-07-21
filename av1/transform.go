package av1

// Inverse transform implementation for AV1.
// AV1 spec Section 7.12.

// InverseTransform2D performs the 2D inverse transform on a coefficient block.
// Applies column transform, then row transform, with intermediate rounding.
// txW and txH are the transform width and height in pixels.
func InverseTransform2D(coeffs []int32, txW, txH int, txType int) {
	if txW == 0 || txH == 0 {
		return
	}

	rowTx, colTx := txTypeToFunctions(txType)

	// Intermediate shift after row transform per AV1 spec Table 7-2.
	// Depends on txW (row transform size).
	rowShift := 2
	if txW <= 4 {
		rowShift = 0
	} else if txW <= 8 {
		rowShift = 1
	}

	// Work buffer.
	buf := make([]int32, txW*txH)
	copy(buf, coeffs[:txW*txH])

	// Step 1: Row transform — for each row, apply rowTx.
	row := make([]int32, txW)
	for r := 0; r < txH; r++ {
		copy(row, buf[r*txW:(r+1)*txW])
		rowTx(row, txW)
		for c := 0; c < txW; c++ {
			buf[r*txW+c] = roundShift(row[c], rowShift)
		}
	}

	// Step 2: Column transform — for each column, apply colTx.
	col := make([]int32, txH)
	for c := 0; c < txW; c++ {
		for r := 0; r < txH; r++ {
			col[r] = buf[r*txW+c]
		}
		colTx(col, txH)
		for r := 0; r < txH; r++ {
			coeffs[r*txW+c] = roundShift(col[r], 4)
		}
	}
}

// txTypeToFunctions returns the row and column transform functions for a TX type.
func txTypeToFunctions(txType int) (row, col func([]int32, int)) {
	switch txType {
	case TxTypeDCT_DCT:
		return inverseDCT, inverseDCT
	case TxTypeADST_DCT:
		return inverseDCT, inverseADST
	case TxTypeDCT_ADST:
		return inverseADST, inverseDCT
	case TxTypeADST_ADST:
		return inverseADST, inverseADST
	case TxTypeFLIPADST_DCT:
		return inverseDCT, inverseFlipADST
	case TxTypeDCT_FLIPADST:
		return inverseFlipADST, inverseDCT
	case TxTypeFLIPADST_FLIPADST:
		return inverseFlipADST, inverseFlipADST
	case TxTypeADST_FLIPADST:
		return inverseFlipADST, inverseADST
	case TxTypeFLIPADST_ADST:
		return inverseADST, inverseFlipADST
	case TxTypeIDENTITY_IDENTITY:
		return inverseIdentity, inverseIdentity
	case TxTypeIDENTITY_DCT:
		return inverseDCT, inverseIdentity
	case TxTypeDCT_IDENTITY:
		return inverseIdentity, inverseDCT
	case TxTypeIDENTITY_ADST:
		return inverseADST, inverseIdentity
	case TxTypeADST_IDENTITY:
		return inverseIdentity, inverseADST
	case TxTypeIDENTITY_FLIPADST:
		return inverseFlipADST, inverseIdentity
	case TxTypeFLIPADST_IDENTITY:
		return inverseIdentity, inverseFlipADST
	default:
		return inverseDCT, inverseDCT
	}
}

// inverseDCT performs an in-place inverse DCT on the first n elements.
func inverseDCT(data []int32, n int) {
	switch n {
	case 4:
		idct4(data)
	case 8:
		idct8(data)
	case 16:
		idct16(data)
	case 32:
		idct32(data)
	case 64:
		idct64(data)
	}
}

// inverseADST performs an in-place inverse ADST on the first n elements.
func inverseADST(data []int32, n int) {
	switch n {
	case 4:
		iadst4(data)
	case 8:
		iadst8(data)
	case 16:
		iadst16(data)
	default:
		// ADST not defined for 32/64; fall back to DCT.
		inverseDCT(data, n)
	}
}

// inverseFlipADST performs an in-place inverse flip-ADST.
func inverseFlipADST(data []int32, n int) {
	inverseADST(data, n)
	// Reverse the output.
	for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
		data[i], data[j] = data[j], data[i]
	}
}

// inverseIdentity performs the identity transform (scaled copy).
func inverseIdentity(data []int32, n int) {
	var shift int
	switch n {
	case 4:
		shift = 0
		for i := 0; i < n; i++ {
			data[i] = roundShift(data[i]*int32(5793), 12+shift)
		}
	case 8:
		for i := 0; i < n; i++ {
			data[i] = data[i] * 2
		}
	case 16:
		shift = 1
		for i := 0; i < n; i++ {
			data[i] = roundShift(data[i]*int32(5793), 12-shift)
		}
	case 32:
		for i := 0; i < n; i++ {
			data[i] = data[i] * 4
		}
	default:
		for i := 0; i < n; i++ {
			data[i] = data[i] * 4
		}
	}
}

// Cosine constants for transforms, scaled by 2^12.
// cospi[k] = round(cos(k*pi/128) * 4096)
const (
	cospi1  = 4096
	cospi2  = 4091
	cospi3  = 4085
	cospi4  = 4076
	cospi5  = 4065
	cospi6  = 4052
	cospi7  = 4036
	cospi8  = 4017
	cospi9  = 3996
	cospi10 = 3973
	cospi11 = 3948
	cospi12 = 3920
	cospi13 = 3889
	cospi14 = 3857
	cospi15 = 3822
	cospi16 = 3784
	cospi17 = 3745
	cospi18 = 3703
	cospi19 = 3659
	cospi20 = 3612
	cospi21 = 3564
	cospi22 = 3513
	cospi23 = 3461
	cospi24 = 3406
	cospi25 = 3349
	cospi26 = 3290
	cospi27 = 3229
	cospi28 = 3166
	cospi29 = 3102
	cospi30 = 3035
	cospi31 = 2967
	cospi32 = 2896
	cospi33 = 2822
	cospi34 = 2751
	cospi35 = 2675
	cospi36 = 2598
	cospi37 = 2520
	cospi38 = 2440
	cospi39 = 2359
	cospi40 = 2276
	cospi41 = 2191
	cospi42 = 2106
	cospi43 = 2019
	cospi44 = 1931
	cospi45 = 1842
	cospi46 = 1751
	cospi47 = 1660
	cospi48 = 1567
	cospi49 = 1474
	cospi50 = 1380
	cospi51 = 1285
	cospi52 = 1189
	cospi53 = 1092
	cospi54 = 995
	cospi55 = 897
	cospi56 = 799
	cospi57 = 700
	cospi58 = 601
	cospi59 = 501
	cospi60 = 401
	cospi61 = 301
	cospi62 = 201
	cospi63 = 101
)

// roundShift performs (value + (1 << (shift-1))) >> shift for positive shifts.
func roundShift(value int32, shift int) int32 {
	if shift <= 0 {
		return value
	}
	return (value + (1 << (shift - 1))) >> shift
}

// idct4 performs a 4-point inverse DCT.
func idct4(data []int32) {
	s0 := data[0]
	s1 := data[1]
	s2 := data[2]
	s3 := data[3]

	// Stage 1.
	a0 := roundShift(s0*cospi32+s2*cospi32, 12)
	a1 := roundShift(s0*cospi32-s2*cospi32, 12)
	a2 := roundShift(s1*cospi48-s3*cospi16, 12)
	a3 := roundShift(s1*cospi16+s3*cospi48, 12)

	// Stage 2.
	data[0] = a0 + a3
	data[1] = a1 + a2
	data[2] = a1 - a2
	data[3] = a0 - a3
}

// idct8 performs an 8-point inverse DCT per AV1 spec / libaom.
func idct8(data []int32) {
	// Even part: 4-point IDCT on indices {0,2,4,6}.
	even := [4]int32{data[0], data[2], data[4], data[6]}
	idct4(even[:])

	// Odd part: indices {1,3,5,7}.
	s1 := data[1]
	s3 := data[3]
	s5 := data[5]
	s7 := data[7]

	// Stage 2: rotations (cospi values from dav1d cospi[] table).
	t4 := roundShift(s1*cospi56-s7*cospi8, 12)
	t7 := roundShift(s1*cospi8+s7*cospi56, 12)
	t5 := roundShift(s3*cospi24-s5*cospi40, 12)
	t6 := roundShift(s3*cospi40+s5*cospi24, 12)

	// Stage 3: butterfly on odd part.
	u4 := t4 + t5
	u5 := t4 - t5
	u6 := t7 - t6
	u7 := t7 + t6

	// Stage 4: cos(pi/4) rotation on (u6, u5).
	v5 := roundShift((u6-u5)*cospi32, 12)
	v6 := roundShift((u6+u5)*cospi32, 12)

	// Stage 5: final butterfly.
	data[0] = even[0] + u7
	data[1] = even[1] + v6
	data[2] = even[2] + v5
	data[3] = even[3] + u4
	data[4] = even[3] - u4
	data[5] = even[2] - v5
	data[6] = even[1] - v6
	data[7] = even[0] - u7
}

// halfBtf computes the butterfly rotation: (w0*in0 + w1*in1) >> 12.
func halfBtf(w0, in0, w1, in1 int32) int32 {
	return int32(roundShift64(int64(w0)*int64(in0)+int64(w1)*int64(in1), 12))
}

// idct16 performs a 16-point inverse DCT using butterfly decomposition.
// Matches libaom's av1_idct16 stage-by-stage structure.
func idct16(data []int32) {
	// Stage 1: input reorder
	s := [16]int32{
		data[0], data[8], data[4], data[12],
		data[2], data[10], data[6], data[14],
		data[1], data[9], data[5], data[13],
		data[3], data[11], data[7], data[15],
	}

	// Stage 2: rotations on odd part (indices 8-15)
	t8 := halfBtf(cospi60, s[8], -cospi4, s[15])
	t15 := halfBtf(cospi4, s[8], cospi60, s[15])
	t9 := halfBtf(cospi28, s[9], -cospi36, s[14])
	t14 := halfBtf(cospi36, s[9], cospi28, s[14])
	t10 := halfBtf(cospi44, s[10], -cospi20, s[13])
	t13 := halfBtf(cospi20, s[10], cospi44, s[13])
	t11 := halfBtf(cospi12, s[11], -cospi52, s[12])
	t12 := halfBtf(cospi52, s[11], cospi12, s[12])

	// Stage 3: 8-point IDCT on even part + butterflies on odd
	even := [8]int32{s[0], s[1], s[2], s[3], s[4], s[5], s[6], s[7]}
	idct8(even[:])

	u8 := t8 + t9
	u9 := t8 - t9
	u10 := -t10 + t11
	u11 := t10 + t11
	u12 := t12 + t13
	u13 := t12 - t13
	u14 := -t14 + t15
	u15 := t14 + t15

	// Stage 4: cospi[32] rotations
	v9 := halfBtf(cospi32, u9, -cospi32, u14)
	v14 := halfBtf(cospi32, u9, cospi32, u14)
	v10 := halfBtf(-cospi32, u10, cospi32, u13)
	v13 := halfBtf(cospi32, u10, cospi32, u13)

	// Stage 5: butterflies
	w8 := u8 + u11
	w9 := v9 + v10
	w10 := v9 - v10
	w11 := u8 - u11
	w12 := -u12 + u15
	w13 := -v13 + v14
	w14 := v13 + v14
	w15 := u12 + u15

	// Stage 6: cospi[32] rotations
	x10 := halfBtf(cospi32, w10, -cospi32, w13)
	x13 := halfBtf(cospi32, w10, cospi32, w13)
	x11 := halfBtf(cospi32, w11, -cospi32, w12)
	x12 := halfBtf(cospi32, w11, cospi32, w12)

	// Stage 7: final butterfly with even part
	data[0] = even[0] + w15
	data[1] = even[1] + w14
	data[2] = even[2] + x13
	data[3] = even[3] + x12
	data[4] = even[4] + x11
	data[5] = even[5] + x10
	data[6] = even[6] + w9
	data[7] = even[7] + w8
	data[8] = even[7] - w8
	data[9] = even[6] - w9
	data[10] = even[5] - x10
	data[11] = even[4] - x11
	data[12] = even[3] - x12
	data[13] = even[2] - x13
	data[14] = even[1] - w14
	data[15] = even[0] - w15
}

// idct32 performs a 32-point inverse DCT using butterfly decomposition.
// Matches libaom's av1_idct32 structure.
func idct32(data []int32) {
	// Stage 1: input reorder — even indices go to even part, odd to odd
	var even [16]int32
	for i := 0; i < 16; i++ {
		even[i] = data[i*2]
	}
	idct16(even[:])

	// Odd part: data[1], data[3], ..., data[31] after input reorder
	// Input reorder for odd part maps to specific positions
	in := [16]int32{
		data[1], data[17], data[9], data[25],
		data[5], data[21], data[13], data[29],
		data[3], data[19], data[11], data[27],
		data[7], data[23], data[15], data[31],
	}

	// Stage 2: 8 rotations on odd part
	t0 := halfBtf(cospi62, in[0], -cospi2, in[15])
	t15 := halfBtf(cospi2, in[0], cospi62, in[15])
	t1 := halfBtf(cospi30, in[1], -cospi34, in[14])
	t14 := halfBtf(cospi34, in[1], cospi30, in[14])
	t2 := halfBtf(cospi46, in[2], -cospi18, in[13])
	t13 := halfBtf(cospi18, in[2], cospi46, in[13])
	t3 := halfBtf(cospi14, in[3], -cospi50, in[12])
	t12 := halfBtf(cospi50, in[3], cospi14, in[12])
	t4 := halfBtf(cospi54, in[4], -cospi10, in[11])
	t11 := halfBtf(cospi10, in[4], cospi54, in[11])
	t5 := halfBtf(cospi22, in[5], -cospi42, in[10])
	t10 := halfBtf(cospi42, in[5], cospi22, in[10])
	t6 := halfBtf(cospi38, in[6], -cospi26, in[9])
	t9 := halfBtf(cospi26, in[6], cospi38, in[9])
	t7 := halfBtf(cospi6, in[7], -cospi58, in[8])
	t8 := halfBtf(cospi58, in[7], cospi6, in[8])

	// Stage 3: butterflies
	u0 := t0 + t1
	u1 := t0 - t1
	u2 := -t2 + t3
	u3 := t2 + t3
	u4 := t4 + t5
	u5 := t4 - t5
	u6 := -t6 + t7
	u7 := t6 + t7
	u8 := t8 + t9
	u9 := t8 - t9
	u10 := -t10 + t11
	u11 := t10 + t11
	u12 := t12 + t13
	u13 := t12 - t13
	u14 := -t14 + t15
	u15 := t14 + t15

	// Stage 4: rotations
	v1 := halfBtf(cospi56, u1, -cospi8, u14)
	v14 := halfBtf(cospi8, u1, cospi56, u14)
	v2 := halfBtf(-cospi40, u2, cospi24, u13)
	v13 := halfBtf(cospi24, u2, cospi40, u13)
	v5 := halfBtf(cospi40, u5, -cospi24, u10)
	v10 := halfBtf(cospi24, u5, cospi40, u10)
	v6 := halfBtf(-cospi56, u6, cospi8, u9)
	v9 := halfBtf(cospi8, u6, cospi56, u9)

	// Stage 5: butterflies
	w0 := u0 + u3
	w1 := v1 + v2
	w2 := v1 - v2
	w3 := u0 - u3
	w4 := -u4 + u7
	w5 := -v5 + v6
	w6 := v5 + v6
	w7 := u4 + u7
	w8 := u8 + u11
	w9 := v9 + v10
	w10 := v9 - v10
	w11 := u8 - u11
	w12 := -u12 + u15
	w13 := -v13 + v14
	w14 := v13 + v14
	w15 := u12 + u15

	// Stage 6: cospi[32] rotations
	x2 := halfBtf(cospi32, w2, -cospi32, w13)
	x13 := halfBtf(cospi32, w2, cospi32, w13)
	x3 := halfBtf(cospi32, w3, -cospi32, w12)
	x12 := halfBtf(cospi32, w3, cospi32, w12)
	x4 := halfBtf(-cospi32, w4, cospi32, w11)
	x11 := halfBtf(cospi32, w4, cospi32, w11)
	x5 := halfBtf(-cospi32, w5, cospi32, w10)
	x10 := halfBtf(cospi32, w5, cospi32, w10)

	// Stage 7: butterflies
	y0 := w0 + w7
	y1 := w1 + w6
	y2 := x2 + x5
	y3 := x3 + x4
	y4 := x3 - x4
	y5 := x2 - x5
	y6 := w1 - w6
	y7 := w0 - w7
	y8 := -w8 + w15
	y9 := -w9 + w14
	y10 := -x10 + x13
	y11 := -x11 + x12
	y12 := x11 + x12
	y13 := x10 + x13
	y14 := w9 + w14
	y15 := w8 + w15

	// Stage 8: cospi[32] rotations
	z4 := halfBtf(cospi32, y4, -cospi32, y11)
	z11 := halfBtf(cospi32, y4, cospi32, y11)
	z5 := halfBtf(cospi32, y5, -cospi32, y10)
	z10 := halfBtf(cospi32, y5, cospi32, y10)
	z6 := halfBtf(cospi32, y6, -cospi32, y9)
	z9 := halfBtf(cospi32, y6, cospi32, y9)
	z7 := halfBtf(cospi32, y7, -cospi32, y8)
	z8 := halfBtf(cospi32, y7, cospi32, y8)

	// Stage 9: final butterfly with even part
	data[0] = even[0] + y15
	data[1] = even[1] + y14
	data[2] = even[2] + y13
	data[3] = even[3] + y12
	data[4] = even[4] + z11
	data[5] = even[5] + z10
	data[6] = even[6] + z9
	data[7] = even[7] + z8
	data[8] = even[8] + z7
	data[9] = even[9] + z6
	data[10] = even[10] + z5
	data[11] = even[11] + z4
	data[12] = even[12] + y3
	data[13] = even[13] + y2
	data[14] = even[14] + y1
	data[15] = even[15] + y0
	data[16] = even[15] - y0
	data[17] = even[14] - y1
	data[18] = even[13] - y2
	data[19] = even[12] - y3
	data[20] = even[11] - z4
	data[21] = even[10] - z5
	data[22] = even[9] - z6
	data[23] = even[8] - z7
	data[24] = even[7] - z8
	data[25] = even[6] - z9
	data[26] = even[5] - z10
	data[27] = even[4] - z11
	data[28] = even[3] - y12
	data[29] = even[2] - y13
	data[30] = even[1] - y14
	data[31] = even[0] - y15
}

// idct64 performs a 64-point inverse DCT using butterfly decomposition.
// Uses even/odd split: even part → idct32, odd part → butterfly stages.
// AV1 limits TX_64x64 coefficients to the first 32x32, so only the first 32
// inputs can be non-zero.
//
// The odd part follows the libaom/dav1d butterfly structure with stages
// mirroring the idct32 odd-part decomposition.
func idct64(data []int32) {
	// Even part: 32-point IDCT on even-indexed inputs.
	var even [32]int32
	for i := 0; i < 32; i++ {
		even[i] = data[i*2]
	}
	idct32(even[:])

	// Odd part: odd-indexed inputs in natural frequency order.
	// bf[k] = data[2*k+1].
	var bf [32]int32
	for i := 0; i < 32; i++ {
		bf[i] = data[2*i+1]
	}

	// Stage 1: 16 rotation pairs (Level 0).
	// For pair (bf[j], bf[31-j]):
	//   cospi_high = cospi[63-2j], cospi_low = cospi[2j+1]
	{
		type rot struct{ lo, hi, ch, cl int32 }
		rots := [16]rot{
			{0, 31, cospi63, cospi1},
			{1, 30, cospi61, cospi3},
			{2, 29, cospi59, cospi5},
			{3, 28, cospi57, cospi7},
			{4, 27, cospi55, cospi9},
			{5, 26, cospi53, cospi11},
			{6, 25, cospi51, cospi13},
			{7, 24, cospi49, cospi15},
			{8, 23, cospi47, cospi17},
			{9, 22, cospi45, cospi19},
			{10, 21, cospi43, cospi21},
			{11, 20, cospi41, cospi23},
			{12, 19, cospi39, cospi25},
			{13, 18, cospi37, cospi27},
			{14, 17, cospi35, cospi29},
			{15, 16, cospi33, cospi31},
		}
		var tmp [32]int32
		for _, r := range rots {
			tmp[r.lo] = halfBtf(r.ch, bf[r.lo], -r.cl, bf[r.hi])
			tmp[r.hi] = halfBtf(r.cl, bf[r.lo], r.ch, bf[r.hi])
		}
		bf = tmp
	}

	// Stage 2: butterflies — 16 add/subtract pairs at distance 1.
	{
		var tmp [32]int32
		for i := 0; i < 32; i += 4 {
			tmp[i] = bf[i] + bf[i+1]
			tmp[i+1] = bf[i] - bf[i+1]
			tmp[i+2] = -bf[i+2] + bf[i+3]
			tmp[i+3] = bf[i+2] + bf[i+3]
		}
		bf = tmp
	}

	// Stage 3: 8 rotation pairs (Level 1).
	// Cospi values match idct16 odd-part Level 0: cospi[60/4, 28/36, 44/20, 12/52].
	// Pairs mirror: (0,7)↔(60,4), (1,6)↔(28,36), (2,5)↔(44,20), (3,4)↔(12,52).
	{
		a, b := halfBtf(cospi60, bf[1], -cospi4, bf[30]), halfBtf(cospi4, bf[1], cospi60, bf[30])
		bf[1], bf[30] = a, b
		a, b = halfBtf(-cospi28, bf[2], cospi36, bf[29]), halfBtf(cospi36, bf[2], cospi28, bf[29])
		bf[2], bf[29] = a, b
		a, b = halfBtf(cospi44, bf[5], -cospi20, bf[26]), halfBtf(cospi20, bf[5], cospi44, bf[26])
		bf[5], bf[26] = a, b
		a, b = halfBtf(-cospi12, bf[6], cospi52, bf[25]), halfBtf(cospi52, bf[6], cospi12, bf[25])
		bf[6], bf[25] = a, b
		a, b = halfBtf(cospi52, bf[9], -cospi12, bf[22]), halfBtf(cospi12, bf[9], cospi52, bf[22])
		bf[9], bf[22] = a, b
		a, b = halfBtf(-cospi44, bf[10], cospi20, bf[21]), halfBtf(cospi20, bf[10], cospi44, bf[21])
		bf[10], bf[21] = a, b
		a, b = halfBtf(cospi36, bf[13], -cospi28, bf[18]), halfBtf(cospi28, bf[13], cospi36, bf[18])
		bf[13], bf[18] = a, b
		a, b = halfBtf(-cospi60, bf[14], cospi4, bf[17]), halfBtf(cospi4, bf[14], cospi60, bf[17])
		bf[14], bf[17] = a, b
	}

	// Stage 4: butterflies — distance-3 pairing within groups of 8.
	{
		var tmp [32]int32
		for g := 0; g < 4; g++ {
			i := g * 8
			// Pass-through elements: bf[i+0], bf[i+3], bf[i+4], bf[i+7]
			// Rotated elements: bf[i+1], bf[i+2], bf[i+5], bf[i+6]
			tmp[i+0] = bf[i+0] + bf[i+3]
			tmp[i+1] = bf[i+1] + bf[i+2]
			tmp[i+2] = bf[i+1] - bf[i+2]
			tmp[i+3] = bf[i+0] - bf[i+3]
			tmp[i+4] = -bf[i+4] + bf[i+7]
			tmp[i+5] = -bf[i+5] + bf[i+6]
			tmp[i+6] = bf[i+5] + bf[i+6]
			tmp[i+7] = bf[i+4] + bf[i+7]
		}
		bf = tmp
	}

	// Stage 5: 8 rotation pairs (Level 2).
	// Cospi values match idct8 odd-part Level 0: cospi[56/8, 40/24].
	// 4 pairs use (56,8), 4 pairs use (40,24) with alternating signs.
	{
		a, b := halfBtf(cospi56, bf[2], -cospi8, bf[29]), halfBtf(cospi8, bf[2], cospi56, bf[29])
		bf[2], bf[29] = a, b
		a, b = halfBtf(-cospi40, bf[3], cospi24, bf[28]), halfBtf(cospi24, bf[3], cospi40, bf[28])
		bf[3], bf[28] = a, b
		a, b = halfBtf(cospi40, bf[4], -cospi24, bf[27]), halfBtf(cospi24, bf[4], cospi40, bf[27])
		bf[4], bf[27] = a, b
		a, b = halfBtf(-cospi56, bf[5], cospi8, bf[26]), halfBtf(cospi8, bf[5], cospi56, bf[26])
		bf[5], bf[26] = a, b
		a, b = halfBtf(cospi24, bf[10], -cospi40, bf[21]), halfBtf(cospi40, bf[10], cospi24, bf[21])
		bf[10], bf[21] = a, b
		a, b = halfBtf(-cospi8, bf[11], cospi56, bf[20]), halfBtf(cospi56, bf[11], cospi8, bf[20])
		bf[11], bf[20] = a, b
		a, b = halfBtf(cospi8, bf[12], -cospi56, bf[19]), halfBtf(cospi56, bf[12], cospi8, bf[19])
		bf[12], bf[19] = a, b
		a, b = halfBtf(-cospi24, bf[13], cospi40, bf[18]), halfBtf(cospi40, bf[13], cospi24, bf[18])
		bf[13], bf[18] = a, b
	}

	// Stage 6: butterflies — distance-7 pairing within groups of 16.
	{
		var tmp [32]int32
		for g := 0; g < 2; g++ {
			i := g * 16
			tmp[i+0] = bf[i+0] + bf[i+7]
			tmp[i+1] = bf[i+1] + bf[i+6]
			tmp[i+2] = bf[i+2] + bf[i+5]
			tmp[i+3] = bf[i+3] + bf[i+4]
			tmp[i+4] = bf[i+3] - bf[i+4]
			tmp[i+5] = bf[i+2] - bf[i+5]
			tmp[i+6] = bf[i+1] - bf[i+6]
			tmp[i+7] = bf[i+0] - bf[i+7]
			tmp[i+8] = -bf[i+8] + bf[i+15]
			tmp[i+9] = -bf[i+9] + bf[i+14]
			tmp[i+10] = -bf[i+10] + bf[i+13]
			tmp[i+11] = -bf[i+11] + bf[i+12]
			tmp[i+12] = bf[i+11] + bf[i+12]
			tmp[i+13] = bf[i+10] + bf[i+13]
			tmp[i+14] = bf[i+9] + bf[i+14]
			tmp[i+15] = bf[i+8] + bf[i+15]
		}
		bf = tmp
	}

	// Stage 7: 8 rotation pairs (Level 3) — all cospi[32].
	{
		a, b := halfBtf(cospi32, bf[4], -cospi32, bf[27]), halfBtf(cospi32, bf[4], cospi32, bf[27])
		bf[4], bf[27] = a, b
		a, b = halfBtf(cospi32, bf[5], -cospi32, bf[26]), halfBtf(cospi32, bf[5], cospi32, bf[26])
		bf[5], bf[26] = a, b
		a, b = halfBtf(cospi32, bf[6], -cospi32, bf[25]), halfBtf(cospi32, bf[6], cospi32, bf[25])
		bf[6], bf[25] = a, b
		a, b = halfBtf(cospi32, bf[7], -cospi32, bf[24]), halfBtf(cospi32, bf[7], cospi32, bf[24])
		bf[7], bf[24] = a, b
		a, b = halfBtf(-cospi32, bf[8], cospi32, bf[23]), halfBtf(cospi32, bf[8], cospi32, bf[23])
		bf[8], bf[23] = a, b
		a, b = halfBtf(-cospi32, bf[9], cospi32, bf[22]), halfBtf(cospi32, bf[9], cospi32, bf[22])
		bf[9], bf[22] = a, b
		a, b = halfBtf(-cospi32, bf[10], cospi32, bf[21]), halfBtf(cospi32, bf[10], cospi32, bf[21])
		bf[10], bf[21] = a, b
		a, b = halfBtf(-cospi32, bf[11], cospi32, bf[20]), halfBtf(cospi32, bf[11], cospi32, bf[20])
		bf[11], bf[20] = a, b
	}

	// Stage 8: butterflies — distance-15 pairing across full 32 elements.
	{
		var tmp [32]int32
		for i := 0; i < 16; i++ {
			tmp[i] = bf[i] + bf[31-i]
			tmp[31-i] = bf[i] - bf[31-i]
		}
		bf = tmp
	}

	// Stage 9: 8 rotation pairs (Level 4) — all cospi[32].
	{
		a, b := halfBtf(cospi32, bf[8], -cospi32, bf[23]), halfBtf(cospi32, bf[8], cospi32, bf[23])
		bf[8], bf[23] = a, b
		a, b = halfBtf(cospi32, bf[9], -cospi32, bf[22]), halfBtf(cospi32, bf[9], cospi32, bf[22])
		bf[9], bf[22] = a, b
		a, b = halfBtf(cospi32, bf[10], -cospi32, bf[21]), halfBtf(cospi32, bf[10], cospi32, bf[21])
		bf[10], bf[21] = a, b
		a, b = halfBtf(cospi32, bf[11], -cospi32, bf[20]), halfBtf(cospi32, bf[11], cospi32, bf[20])
		bf[11], bf[20] = a, b
		a, b = halfBtf(cospi32, bf[12], -cospi32, bf[19]), halfBtf(cospi32, bf[12], cospi32, bf[19])
		bf[12], bf[19] = a, b
		a, b = halfBtf(cospi32, bf[13], -cospi32, bf[18]), halfBtf(cospi32, bf[13], cospi32, bf[18])
		bf[13], bf[18] = a, b
		a, b = halfBtf(cospi32, bf[14], -cospi32, bf[17]), halfBtf(cospi32, bf[14], cospi32, bf[17])
		bf[14], bf[17] = a, b
		a, b = halfBtf(cospi32, bf[15], -cospi32, bf[16]), halfBtf(cospi32, bf[15], cospi32, bf[16])
		bf[15], bf[16] = a, b
	}

	// Stage 10: final butterfly combining even and odd parts.
	for i := 0; i < 32; i++ {
		data[i] = even[i] + bf[31-i]
		data[63-i] = even[i] - bf[31-i]
	}
}

// iadst4 performs a 4-point inverse ADST.
func iadst4(data []int32) {
	s0 := int64(data[0])
	s1 := int64(data[1])
	s2 := int64(data[2])
	s3 := int64(data[3])

	// sinpi constants (AV1 spec).
	const sinpi1 = 1321
	const sinpi2 = 2482
	const sinpi3 = 3344
	const sinpi4 = 3803

	b0 := sinpi1*s0 + sinpi4*s1
	b1 := sinpi2*s0 + sinpi3*s1
	b2 := sinpi3*s0 - sinpi2*s1

	c0 := b0 + sinpi3*s2
	c1 := b1 - sinpi1*s2
	c2 := sinpi3 * (s0 + s1 - s3)
	c3 := b2 + sinpi4*s2

	data[0] = int32(roundShift64(c0+sinpi2*s3, 12))
	data[1] = int32(roundShift64(c1-sinpi4*s3, 12))
	data[2] = int32(roundShift64(c2, 12))
	data[3] = int32(roundShift64(c3-sinpi1*s3, 12))
}

// iadst8 performs an 8-point inverse ADST using integer butterfly decomposition.
// Matches dav1d/libaom's av1_iadst8 structure.
func iadst8(data []int32) {
	s0 := data[0]
	s1 := data[1]
	s2 := data[2]
	s3 := data[3]
	s4 := data[4]
	s5 := data[5]
	s6 := data[6]
	s7 := data[7]

	// Stage 1: 4 butterfly rotations
	t0 := halfBtf(cospi2, s7, cospi62, s0)
	t1 := halfBtf(cospi62, s7, -cospi2, s0)
	t2 := halfBtf(cospi10, s5, cospi54, s2)
	t3 := halfBtf(cospi54, s5, -cospi10, s2)
	t4 := halfBtf(cospi18, s3, cospi46, s4)
	t5 := halfBtf(cospi46, s3, -cospi18, s4)
	t6 := halfBtf(cospi26, s1, cospi38, s6)
	t7 := halfBtf(cospi38, s1, -cospi26, s6)

	// Stage 2: butterflies
	u0 := t0 + t4
	u1 := t1 + t5
	u2 := t2 + t6
	u3 := t3 + t7
	u4 := t0 - t4
	u5 := t1 - t5
	u6 := t2 - t6
	u7 := t3 - t7

	// Stage 3: 2 rotations
	v4 := halfBtf(cospi8, u4, cospi56, u5)
	v5 := halfBtf(cospi56, u4, -cospi8, u5)
	v6 := halfBtf(cospi40, u6, cospi24, u7)
	v7 := halfBtf(cospi24, u6, -cospi40, u7)

	// Stage 4: butterflies
	w0 := u0 + u2
	w1 := u1 + u3
	w2 := u0 - u2
	w3 := u1 - u3
	w4 := v4 + v6
	w5 := v5 + v7
	w6 := v4 - v6
	w7 := v5 - v7

	// Stage 5: cospi[32] rotations
	x2 := halfBtf(cospi32, w2, cospi32, w3)
	x3 := halfBtf(cospi32, w2, -cospi32, w3)
	x6 := halfBtf(cospi32, w6, cospi32, w7)
	x7 := halfBtf(cospi32, w6, -cospi32, w7)

	// Output with sign changes for ADST
	data[0] = w0
	data[1] = -x2
	data[2] = w4
	data[3] = -x6
	data[4] = x3
	data[5] = -w1
	data[6] = x7
	data[7] = -w5
}

// iadst16 performs a 16-point inverse ADST using integer butterfly decomposition.
// Matches dav1d/libaom's av1_iadst16 structure.
func iadst16(data []int32) {
	s0 := data[0]
	s1 := data[1]
	s2 := data[2]
	s3 := data[3]
	s4 := data[4]
	s5 := data[5]
	s6 := data[6]
	s7 := data[7]
	s8 := data[8]
	s9 := data[9]
	s10 := data[10]
	s11 := data[11]
	s12 := data[12]
	s13 := data[13]
	s14 := data[14]
	s15 := data[15]

	// Stage 1: 8 butterfly rotations
	t0 := halfBtf(cospi2, s15, cospi62, s0)
	t1 := halfBtf(cospi62, s15, -cospi2, s0)
	t2 := halfBtf(cospi10, s13, cospi54, s2)
	t3 := halfBtf(cospi54, s13, -cospi10, s2)
	t4 := halfBtf(cospi18, s11, cospi46, s4)
	t5 := halfBtf(cospi46, s11, -cospi18, s4)
	t6 := halfBtf(cospi26, s9, cospi38, s6)
	t7 := halfBtf(cospi38, s9, -cospi26, s6)
	t8 := halfBtf(cospi34, s7, cospi30, s8)
	t9 := halfBtf(cospi30, s7, -cospi34, s8)
	t10 := halfBtf(cospi42, s5, cospi22, s10)
	t11 := halfBtf(cospi22, s5, -cospi42, s10)
	t12 := halfBtf(cospi50, s3, cospi14, s12)
	t13 := halfBtf(cospi14, s3, -cospi50, s12)
	t14 := halfBtf(cospi58, s1, cospi6, s14)
	t15 := halfBtf(cospi6, s1, -cospi58, s14)

	// Stage 2: butterflies
	u0 := t0 + t8
	u1 := t1 + t9
	u2 := t2 + t10
	u3 := t3 + t11
	u4 := t4 + t12
	u5 := t5 + t13
	u6 := t6 + t14
	u7 := t7 + t15
	u8 := t0 - t8
	u9 := t1 - t9
	u10 := t2 - t10
	u11 := t3 - t11
	u12 := t4 - t12
	u13 := t5 - t13
	u14 := t6 - t14
	u15 := t7 - t15

	// Stage 3: 4 rotations
	v8 := halfBtf(cospi8, u8, cospi56, u9)
	v9 := halfBtf(cospi56, u8, -cospi8, u9)
	v10 := halfBtf(cospi40, u10, cospi24, u11)
	v11 := halfBtf(cospi24, u10, -cospi40, u11)
	v12 := halfBtf(-cospi56, u12, cospi8, u13)
	v13 := halfBtf(cospi8, u12, cospi56, u13)
	v14 := halfBtf(-cospi24, u14, cospi40, u15)
	v15 := halfBtf(cospi40, u14, cospi24, u15)

	// Stage 4: butterflies
	w0 := u0 + u4
	w1 := u1 + u5
	w2 := u2 + u6
	w3 := u3 + u7
	w4 := u0 - u4
	w5 := u1 - u5
	w6 := u2 - u6
	w7 := u3 - u7
	w8 := v8 + v12
	w9 := v9 + v13
	w10 := v10 + v14
	w11 := v11 + v15
	w12 := v8 - v12
	w13 := v9 - v13
	w14 := v10 - v14
	w15 := v11 - v15

	// Stage 5: 4 rotations
	x4 := halfBtf(cospi16, w4, cospi48, w5)
	x5 := halfBtf(cospi48, w4, -cospi16, w5)
	x6 := halfBtf(-cospi48, w6, cospi16, w7)
	x7 := halfBtf(cospi16, w6, cospi48, w7)
	x12 := halfBtf(cospi16, w12, cospi48, w13)
	x13 := halfBtf(cospi48, w12, -cospi16, w13)
	x14 := halfBtf(-cospi48, w14, cospi16, w15)
	x15 := halfBtf(cospi16, w14, cospi48, w15)

	// Stage 6: butterflies
	y0 := w0 + w2
	y1 := w1 + w3
	y2 := w0 - w2
	y3 := w1 - w3
	y4 := x4 + x6
	y5 := x5 + x7
	y6 := x4 - x6
	y7 := x5 - x7
	y8 := w8 + w10
	y9 := w9 + w11
	y10 := w8 - w10
	y11 := w9 - w11
	y12 := x12 + x14
	y13 := x13 + x15
	y14 := x12 - x14
	y15 := x13 - x15

	// Stage 7: cospi[32] rotations
	z2 := halfBtf(cospi32, y2, cospi32, y3)
	z3 := halfBtf(cospi32, y2, -cospi32, y3)
	z6 := halfBtf(cospi32, y6, cospi32, y7)
	z7 := halfBtf(cospi32, y6, -cospi32, y7)
	z10 := halfBtf(cospi32, y10, cospi32, y11)
	z11 := halfBtf(cospi32, y10, -cospi32, y11)
	z14 := halfBtf(cospi32, y14, cospi32, y15)
	z15 := halfBtf(cospi32, y14, -cospi32, y15)

	// Output with sign changes for ADST
	data[0] = y0
	data[1] = -y8
	data[2] = y4
	data[3] = -y12
	data[4] = z2
	data[5] = -z10
	data[6] = z6
	data[7] = -z14
	data[8] = z3
	data[9] = -z11
	data[10] = z7
	data[11] = -z15
	data[12] = y1
	data[13] = -y9
	data[14] = y5
	data[15] = -y13
}

func roundShift64(value int64, shift int) int64 {
	if shift <= 0 {
		return value
	}
	return (value + (1 << (shift - 1))) >> shift
}

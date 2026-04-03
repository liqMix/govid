package av1

import "math"

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
const (
	cospi1  = 4096  // cos(pi/64) * 4096
	cospi2  = 4091
	cospi4  = 4076
	cospi8  = 4017
	cospi16 = 3784
	cospi32 = 2896 // cos(pi/4) * 4096 = sqrt(2) * 2048
	cospi48 = 1567
	cospi24 = 3406
	cospi40 = 2276
	cospi56 = 799
	cospi60 = 401
	cospi62 = 201
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
	t4 := roundShift(s1*799-s7*4017, 12)  // cospi[56]=799, cospi[8]=4017
	t7 := roundShift(s1*4017+s7*799, 12)
	t5 := roundShift(s3*3406-s5*2276, 12) // cospi[24]=3406, cospi[40]=2276
	t6 := roundShift(s3*2276+s5*3406, 12)

	// Stage 3: butterfly on odd part.
	u4 := t4 + t5
	u5 := t4 - t5
	u6 := t7 - t6
	u7 := t7 + t6

	// Stage 4: cos(π/4) rotation on (u6, u5).
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
	// cospi[k] = round(cos(k*π/128) * 4096)
	const (
		c4  = 4076  // cospi[4]
		c12 = 3920  // cospi[12]
		c20 = 3612  // cospi[20]
		c28 = 3166  // cospi[28]
		c36 = 2598  // cospi[36]
		c44 = 1931  // cospi[44]
		c52 = 1189  // cospi[52]
		c60 = 401   // cospi[60]
		c32 = 2896  // cospi[32]
	)

	// Stage 1: input reorder
	s := [16]int32{
		data[0], data[8], data[4], data[12],
		data[2], data[10], data[6], data[14],
		data[1], data[9], data[5], data[13],
		data[3], data[11], data[7], data[15],
	}

	// Stage 2: rotations on odd part (indices 8-15)
	t8 := halfBtf(c60, s[8], -c4, s[15])
	t15 := halfBtf(c4, s[8], c60, s[15])
	t9 := halfBtf(c28, s[9], -c36, s[14])
	t14 := halfBtf(c36, s[9], c28, s[14])
	t10 := halfBtf(c44, s[10], -c20, s[13])
	t13 := halfBtf(c20, s[10], c44, s[13])
	t11 := halfBtf(c12, s[11], -c52, s[12])
	t12 := halfBtf(c52, s[11], c12, s[12])

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
	v9 := halfBtf(c32, u9, -c32, u14)
	v14 := halfBtf(c32, u9, c32, u14)
	v10 := halfBtf(-c32, u10, c32, u13)
	v13 := halfBtf(c32, u10, c32, u13)

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
	x10 := halfBtf(c32, w10, -c32, w13)
	x13 := halfBtf(c32, w10, c32, w13)
	x11 := halfBtf(c32, w11, -c32, w12)
	x12 := halfBtf(c32, w11, c32, w12)

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
	// cospi[k] = round(cos(k*π/128) * 4096)
	const (
		c2  = 4091
		c4  = 4076
		c6  = 4052
		c8  = 4017
		c10 = 3973
		c12 = 3920
		c14 = 3859
		c16 = 3784
		c18 = 3703
		c20 = 3612
		c22 = 3513
		c24 = 3406
		c26 = 3290
		c28 = 3166
		c30 = 3035
		c32 = 2896
		c34 = 2751
		c36 = 2598
		c38 = 2440
		c40 = 2276
		c42 = 2106
		c44 = 1931
		c46 = 1751
		c48 = 1567
		c50 = 1380
		c52 = 1189
		c54 = 995
		c56 = 799
		c58 = 601
		c60 = 401
		c62 = 201
	)

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
	t0 := halfBtf(c62, in[0], -c2, in[15])
	t15 := halfBtf(c2, in[0], c62, in[15])
	t1 := halfBtf(c30, in[1], -c34, in[14])
	t14 := halfBtf(c34, in[1], c30, in[14])
	t2 := halfBtf(c46, in[2], -c18, in[13])
	t13 := halfBtf(c18, in[2], c46, in[13])
	t3 := halfBtf(c14, in[3], -c50, in[12])
	t12 := halfBtf(c50, in[3], c14, in[12])
	t4 := halfBtf(c54, in[4], -c10, in[11])
	t11 := halfBtf(c10, in[4], c54, in[11])
	t5 := halfBtf(c22, in[5], -c42, in[10])
	t10 := halfBtf(c42, in[5], c22, in[10])
	t6 := halfBtf(c38, in[6], -c26, in[9])
	t9 := halfBtf(c26, in[6], c38, in[9])
	t7 := halfBtf(c6, in[7], -c58, in[8])
	t8 := halfBtf(c58, in[7], c6, in[8])

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
	v1 := halfBtf(c56, u1, -c8, u14)
	v14 := halfBtf(c8, u1, c56, u14)
	v2 := halfBtf(-c40, u2, c24, u13)
	v13 := halfBtf(c24, u2, c40, u13)
	v5 := halfBtf(c40, u5, -c24, u10)
	v10 := halfBtf(c24, u5, c40, u10)
	v6 := halfBtf(-c56, u6, c8, u9)
	v9 := halfBtf(c8, u6, c56, u9)

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
	x2 := halfBtf(c32, w2, -c32, w13)
	x13 := halfBtf(c32, w2, c32, w13)
	x3 := halfBtf(c32, w3, -c32, w12)
	x12 := halfBtf(c32, w3, c32, w12)
	x4 := halfBtf(-c32, w4, c32, w11)
	x11 := halfBtf(c32, w4, c32, w11)
	x5 := halfBtf(-c32, w5, c32, w10)
	x10 := halfBtf(c32, w5, c32, w10)

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
	z4 := halfBtf(c32, y4, -c32, y11)
	z11 := halfBtf(c32, y4, c32, y11)
	z5 := halfBtf(c32, y5, -c32, y10)
	z10 := halfBtf(c32, y5, c32, y10)
	z6 := halfBtf(c32, y6, -c32, y9)
	z9 := halfBtf(c32, y6, c32, y9)
	z7 := halfBtf(c32, y7, -c32, y8)
	z8 := halfBtf(c32, y7, c32, y8)

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

// idct64 performs a 64-point inverse DCT.
// AV1 limits TX_64x64 coefficients to the first 32x32, so only the first 32
// inputs can be non-zero. We compute all 64 outputs using the correct 64-point
// basis functions.
//
// The AV1 butterfly IDCT applies an extra cos(π/4) factor to the DC term (j=0)
// through its recursive structure. We replicate this by scaling data[0] before
// applying the standard DCT-II formula.
func idct64(data []int32) {
	out := make([]int32, 64)
	m := 32
	if len(data) < 32 {
		m = len(data)
	}

	// Scale DC to match AV1 butterfly convention: DC *= cos(π/4) = 2896/4096.
	scaledDC := int64(roundShift(data[0]*2896, 12))

	for i := 0; i < 64; i++ {
		// DC contribution (j=0): cos(π*(2i+1)*0/128) = cos(0) = 1.
		sum := scaledDC * 4096 // scaled_DC * round(cos(0)*4096)
		// AC contributions (j=1..31).
		for j := 1; j < m; j++ {
			if data[j] == 0 {
				continue
			}
			cos := math.Cos(math.Pi * float64(2*i+1) * float64(j) / 128.0)
			sum += int64(data[j]) * int64(math.Round(cos*4096))
		}
		out[i] = int32(roundShift64(sum, 12))
	}
	copy(data[:64], out)
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

// iadst8 performs an 8-point inverse ADST using direct computation.
func iadst8(data []int32) {
	out := make([]int32, 8)
	for i := 0; i < 8; i++ {
		var sum int64
		for j := 0; j < 8; j++ {
			sin := math.Sin(math.Pi * float64(2*j+1) * float64(2*i+1) / 32.0)
			sum += int64(data[j]) * int64(math.Round(sin*4096))
		}
		out[i] = int32(roundShift64(sum, 12))
	}
	copy(data[:8], out)
}

// iadst16 performs a 16-point inverse ADST using direct computation.
func iadst16(data []int32) {
	out := make([]int32, 16)
	for i := 0; i < 16; i++ {
		var sum int64
		for j := 0; j < 16; j++ {
			sin := math.Sin(math.Pi * float64(2*j+1) * float64(2*i+1) / 64.0)
			sum += int64(data[j]) * int64(math.Round(sin*4096))
		}
		out[i] = int32(roundShift64(sum, 12))
	}
	copy(data[:16], out)
}

func roundShift64(value int64, shift int) int64 {
	if shift <= 0 {
		return value
	}
	return (value + (1 << (shift - 1))) >> shift
}

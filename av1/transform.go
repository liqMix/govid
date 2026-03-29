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

	// Column shift per AV1 spec Table 7-2.
	colShift := 2
	if txH <= 4 {
		colShift = 0
	} else if txH <= 8 {
		colShift = 1
	}

	// Work buffer.
	buf := make([]int32, txW*txH)
	copy(buf, coeffs[:txW*txH])

	// Column transform: for each column, apply colTx to the column.
	col := make([]int32, txH)
	for c := 0; c < txW; c++ {
		for r := 0; r < txH; r++ {
			col[r] = buf[r*txW+c]
		}
		colTx(col, txH)
		for r := 0; r < txH; r++ {
			buf[r*txW+c] = roundShift(col[r], colShift)
		}
	}

	// Row transform: for each row, apply rowTx to the row.
	row := make([]int32, txW)
	for r := 0; r < txH; r++ {
		copy(row, buf[r*txW:(r+1)*txW])
		rowTx(row, txW)
		for c := 0; c < txW; c++ {
			coeffs[r*txW+c] = roundShift(row[c], 4)
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
		idct32(data) // AV1 TX_64x64 uses 32-coeff limit; needs proper 64-point IDCT
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

// idct16 performs a 16-point inverse DCT.
func idct16(data []int32) {
	out := make([]int32, 16)
	for i := 0; i < 16; i++ {
		var sum int64
		for j := 0; j < 16; j++ {
			cos := math.Cos(math.Pi * float64(2*i+1) * float64(j) / 32.0)
			sum += int64(data[j]) * int64(math.Round(cos*4096))
		}
		out[i] = int32(roundShift64(sum, 12))
	}
	copy(data[:16], out)
}

// idct32 performs a 32-point inverse DCT.
func idct32(data []int32) {
	out := make([]int32, 32)
	n := 32
	if len(data) < 32 {
		n = len(data)
	}
	for i := 0; i < n; i++ {
		var sum int64
		for j := 0; j < n; j++ {
			cos := math.Cos(math.Pi * float64(2*i+1) * float64(j) / float64(2*n))
			sum += int64(data[j]) * int64(math.Round(cos*4096))
		}
		out[i] = int32(roundShift64(sum, 12))
	}
	copy(data[:n], out)
}

// idct64 performs a 64-point inverse DCT.
// AV1 limits TX_64x64 coefficients to the first 32x32, so only the first 32
// inputs can be non-zero. We compute all 64 outputs using the correct 64-point
// basis functions.
func idct64(data []int32) {
	out := make([]int32, 64)
	m := 32
	if len(data) < 32 {
		m = len(data)
	}
	for i := 0; i < 64; i++ {
		var sum int64
		for j := 0; j < m; j++ {
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

	a0 := sinpi1*s0 + sinpi2*s1 + sinpi3*s2 + sinpi4*s3
	a1 := sinpi4*s0 + sinpi3*s1 - sinpi1*s2 - sinpi2*s3
	a2 := sinpi3*(s0-s3) + sinpi2*(s1-s2) // Note: simplified
	a3 := sinpi2*s0 - sinpi4*s1 + sinpi1*s2 + sinpi3*s3

	// Actually compute per spec.
	b0 := sinpi1*s0 + sinpi4*s1
	b1 := sinpi2*s0 + sinpi3*s1
	b2 := sinpi3*s0 - sinpi2*s1
	_ = a0
	_ = a1
	_ = a2
	_ = a3

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

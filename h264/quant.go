package h264

// Inverse quantization for H.264, spec section 8.5.12.

// normAdjust4x4 tables from spec Table 8-13, indexed [qp%6][row][col] for 4x4.
// These are the V(m,i,j) values used in inverse quantization.
var levelScale4x4 = [6][4][4]int{
	{{10, 13, 10, 13}, {13, 16, 13, 16}, {10, 13, 10, 13}, {13, 16, 13, 16}},
	{{11, 14, 11, 14}, {14, 18, 14, 18}, {11, 14, 11, 14}, {14, 18, 14, 18}},
	{{13, 16, 13, 16}, {16, 20, 16, 20}, {13, 16, 13, 16}, {16, 20, 16, 20}},
	{{14, 18, 14, 18}, {18, 23, 18, 23}, {14, 18, 14, 18}, {18, 23, 18, 23}},
	{{16, 20, 16, 20}, {20, 25, 20, 25}, {16, 20, 16, 20}, {20, 25, 20, 25}},
	{{18, 23, 18, 23}, {23, 29, 23, 29}, {18, 23, 18, 23}, {23, 29, 23, 29}},
}

// defaultWeightScale is the flat 4x4 scaling matrix value (=16) used when
// no custom scaling lists are specified (Baseline/Main/High without explicit lists).
const defaultWeightScale = 16

// dequant4x4 performs inverse quantization on a 4x4 block of coefficients.
// Per spec 8.5.12.2: d[i][j] = c[i][j] * LevelScale4x4(m,i,j) << (qP/6 - 4)
// where LevelScale4x4 = normAdjust * weightScale. The flat default weightScale is 16.
// For I_16x16 AC blocks the weight scale is omitted because the DC coefficient
// (from the separate Hadamard+dequantLumaDC path) is already at the normAdjust-only
// scale, and DC/AC must be at consistent scales within the IDCT input.
func dequant4x4(coeffs []int16, qp int) {
	dequant4x4Scaled(coeffs, qp, defaultWeightScale)
}

func dequant4x4Scaled(coeffs []int16, qp, weightScale int) {
	qpDiv6 := qp / 6
	qpMod6 := qp % 6

	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			idx := j*4 + i
			if coeffs[idx] == 0 {
				continue
			}
			scale := levelScale4x4[qpMod6][j][i] * weightScale
			if qpDiv6 >= 4 {
				coeffs[idx] = int16(int(coeffs[idx]) * scale << uint(qpDiv6-4))
			} else {
				coeffs[idx] = int16((int(coeffs[idx])*scale + (1 << uint(3-qpDiv6))) >> uint(4-qpDiv6))
			}
		}
	}
}

// dequantDC4x4 dequantizes a single DC coefficient for 4x4 transform.
func dequantDC4x4(coeff int16, qp int) int16 {
	qpDiv6 := qp / 6
	qpMod6 := qp % 6
	scale := levelScale4x4[qpMod6][0][0]

	if qpDiv6 >= 2 {
		return int16(int(coeff) * scale << uint(qpDiv6-2))
	}
	return int16((int(coeff)*scale + (1 << uint(1-qpDiv6))) >> uint(2-qpDiv6))
}

// normAdjust8x8 values from spec Table 8-14, indexed [qp%6][class] where the
// class of position (i,j) comes from normAdjust8x8Class. Unlike 4x4, the 8x8
// scaling function has SIX distinct classes (spec 8.5.9):
//
//	0: i%4==0 && j%4==0
//	1: i%2==1 && j%2==1
//	2: i%4==2 && j%4==2
//	3: (i%4==0 && j%2==1) || (i%2==1 && j%4==0)
//	4: (i%4==0 && j%4==2) || (i%4==2 && j%4==0)
//	5: otherwise
var normAdjust8x8 = [6][6]int{
	{20, 18, 32, 19, 25, 24},
	{22, 19, 35, 21, 28, 26},
	{26, 23, 42, 24, 33, 31},
	{28, 25, 45, 26, 35, 33},
	{32, 28, 51, 30, 40, 38},
	{36, 32, 58, 34, 46, 43},
}

// normAdjust8x8Class[j%4][i%4] gives the Table 8-14 class of position (i,j).
var normAdjust8x8Class = [4][4]int{
	{0, 3, 4, 3},
	{3, 1, 5, 1},
	{4, 5, 2, 5},
	{3, 1, 5, 1},
}

// levelScale8x8 is the expanded [qp%6][row][col] scaling table, built from
// normAdjust8x8 via the class matrix.
var levelScale8x8 = func() [6][8][8]int {
	var t [6][8][8]int
	for m := 0; m < 6; m++ {
		for j := 0; j < 8; j++ {
			for i := 0; i < 8; i++ {
				t[m][j][i] = normAdjust8x8[m][normAdjust8x8Class[j%4][i%4]]
			}
		}
	}
	return t
}()

// dequant8x8 performs inverse quantization on an 8x8 block of coefficients.
// Per spec 8.5.12.2: d[i][j] = c[i][j] * LevelScale8x8(m,i,j) << (qP/6 - 6).
// The flat default weightScale8x8 is 16 (no custom scaling list).
func dequant8x8(coeffs []int16, qp int) {
	qpDiv6 := qp / 6
	qpMod6 := qp % 6
	scales := &levelScale8x8[qpMod6]

	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			idx := j*8 + i
			if coeffs[idx] == 0 {
				continue
			}
			scale := scales[j][i] * defaultWeightScale
			if qpDiv6 >= 6 {
				coeffs[idx] = int16(int(coeffs[idx]) * scale << uint(qpDiv6-6))
			} else {
				coeffs[idx] = int16((int(coeffs[idx])*scale + (1 << uint(5-qpDiv6))) >> uint(6-qpDiv6))
			}
		}
	}
}

// dequantLumaDC performs inverse quantization on luma 4x4 DC coefficients
// after the Hadamard transform (for I_16x16 macroblocks).
func dequantLumaDC(coeffs []int16, qp int) {
	qpDiv6 := qp / 6
	qpMod6 := qp % 6
	scale := levelScale4x4[qpMod6][0][0]

	if qpDiv6 >= 2 {
		for i := range coeffs {
			coeffs[i] = int16(int(coeffs[i]) * scale << uint(qpDiv6-2))
		}
	} else {
		for i := range coeffs {
			coeffs[i] = int16((int(coeffs[i])*scale + (1 << uint(1-qpDiv6))) >> uint(2-qpDiv6))
		}
	}
}

// dequantChromaDC performs inverse quantization on chroma 2x2 DC coefficients
// after the Hadamard transform.
func dequantChromaDC(coeffs []int16, qp int) {
	qpDiv6 := qp / 6
	qpMod6 := qp % 6
	scale := levelScale4x4[qpMod6][0][0]

	if qpDiv6 >= 1 {
		for i := range coeffs {
			coeffs[i] = int16(int(coeffs[i]) * scale << uint(qpDiv6-1))
		}
	} else {
		for i := range coeffs {
			coeffs[i] = int16((int(coeffs[i]) * scale) >> 1)
		}
	}
}

// chromaQPTable maps luma QP to chroma QP (spec table 8-15).
var chromaQPTable = [52]int{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9,
	10, 11, 12, 13, 14, 15, 16, 17, 18, 19,
	20, 21, 22, 23, 24, 25, 26, 27, 28, 29,
	29, 30, 31, 32, 32, 33, 34, 34, 35, 35,
	36, 36, 37, 37, 37, 38, 38, 38, 39, 39,
	39, 39,
}

func chromaQP(qpY, chromaQPOffset int) int {
	qpI := clampInt(qpY+chromaQPOffset, 0, 51)
	return chromaQPTable[qpI]
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

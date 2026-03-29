package h264

// H.264 inverse transforms: 4x4 integer IDCT, 4x4 Hadamard for luma DC,
// 2x2 Hadamard for chroma DC. Spec sections 8.5.12.

// idct4x4 performs the 4x4 integer inverse DCT (spec 8.5.12).
// coeffs has 16 elements in raster scan order. Result is written in place.
func idct4x4(coeffs []int16) {
	// Horizontal pass.
	for i := 0; i < 4; i++ {
		row := i * 4
		s0 := int(coeffs[row+0])
		s1 := int(coeffs[row+1])
		s2 := int(coeffs[row+2])
		s3 := int(coeffs[row+3])

		e0 := s0 + s2
		e1 := s0 - s2
		e2 := (s1 >> 1) - s3
		e3 := s1 + (s3 >> 1)

		coeffs[row+0] = int16(e0 + e3)
		coeffs[row+1] = int16(e1 + e2)
		coeffs[row+2] = int16(e1 - e2)
		coeffs[row+3] = int16(e0 - e3)
	}

	// Vertical pass.
	for i := 0; i < 4; i++ {
		s0 := int(coeffs[i])
		s1 := int(coeffs[4+i])
		s2 := int(coeffs[8+i])
		s3 := int(coeffs[12+i])

		e0 := s0 + s2
		e1 := s0 - s2
		e2 := (s1 >> 1) - s3
		e3 := s1 + (s3 >> 1)

		coeffs[i] = int16((e0 + e3 + 32) >> 6)
		coeffs[4+i] = int16((e1 + e2 + 32) >> 6)
		coeffs[8+i] = int16((e1 - e2 + 32) >> 6)
		coeffs[12+i] = int16((e0 - e3 + 32) >> 6)
	}
}

// idct4x4DC performs the inverse DCT for a block with only a DC coefficient.
// More efficient than full idct4x4 when AC coefficients are all zero.
func idct4x4DC(coeffs []int16) {
	dc := (int(coeffs[0]) + 32) >> 6
	for i := range coeffs {
		coeffs[i] = int16(dc)
	}
}

// hadamard4x4 performs the 4x4 Hadamard inverse transform for
// I_16x16 luma DC coefficients (spec section 8.5.12.1).
func hadamard4x4(coeffs []int16) {
	// Horizontal pass.
	for i := 0; i < 4; i++ {
		row := i * 4
		a := int(coeffs[row+0])
		b := int(coeffs[row+1])
		c := int(coeffs[row+2])
		d := int(coeffs[row+3])

		coeffs[row+0] = int16(a + b + c + d)
		coeffs[row+1] = int16(a + b - c - d)
		coeffs[row+2] = int16(a - b - c + d)
		coeffs[row+3] = int16(a - b + c - d)
	}

	// Vertical pass.
	for i := 0; i < 4; i++ {
		a := int(coeffs[i])
		b := int(coeffs[4+i])
		c := int(coeffs[8+i])
		d := int(coeffs[12+i])

		coeffs[i] = int16(a + b + c + d)
		coeffs[4+i] = int16(a + b - c - d)
		coeffs[8+i] = int16(a - b - c + d)
		coeffs[12+i] = int16(a - b + c - d)
	}
}

// hadamard2x2 performs the 2x2 Hadamard inverse transform for
// chroma DC coefficients (spec section 8.5.12.2).
func hadamard2x2(coeffs []int16) {
	a := int(coeffs[0])
	b := int(coeffs[1])
	c := int(coeffs[2])
	d := int(coeffs[3])

	coeffs[0] = int16(a + b + c + d)
	coeffs[1] = int16(a - b + c - d)
	coeffs[2] = int16(a + b - c - d)
	coeffs[3] = int16(a - b - c + d)
}

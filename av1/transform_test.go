package av1

import (
	"testing"
)

func TestIDCT4x4DCOnly(t *testing.T) {
	// DC-only: single coefficient, rest zero.
	// After IDCT, all outputs should be approximately equal.
	coeffs := make([]int32, 16)
	coeffs[0] = 1000

	InverseTransform2D(coeffs, 4, 4, TxTypeDCT_DCT)

	// All values should be close to each other (DC spread).
	first := coeffs[0]
	for i := 1; i < 16; i++ {
		if abs32(coeffs[i]-first) > 1 {
			t.Errorf("DC-only IDCT4x4: coeffs[%d]=%d != coeffs[0]=%d", i, coeffs[i], first)
		}
	}
}

func TestIDCT4x4SingleCoeff(t *testing.T) {
	// Single coefficient at position (0,1) = frequency component.
	// After inverse transform, output should have a cosine pattern across columns.
	coeffs := make([]int32, 16)
	coeffs[1] = 4096 // AC coefficient

	InverseTransform2D(coeffs, 4, 4, TxTypeDCT_DCT)

	// The pattern should vary across columns but be constant across rows
	// (since the coefficient is in the first row, second column of freq domain).
	// First column should differ from second column.
	if coeffs[0] == coeffs[1] && coeffs[1] == coeffs[2] && coeffs[2] == coeffs[3] {
		t.Error("expected variation across columns for AC(0,1) coefficient")
	}

	// Rows should have the same pattern (since row freq is 0).
	for r := 1; r < 4; r++ {
		for c := 0; c < 4; c++ {
			if abs32(coeffs[r*4+c]-coeffs[c]) > 2 {
				t.Errorf("row %d differs from row 0 at col %d: %d vs %d",
					r, c, coeffs[r*4+c], coeffs[c])
			}
		}
	}
}

func TestIDCT8x8DCOnly(t *testing.T) {
	coeffs := make([]int32, 64)
	coeffs[0] = 2000

	InverseTransform2D(coeffs, 8, 8, TxTypeDCT_DCT)

	first := coeffs[0]
	maxDiff := int32(0)
	for i := 1; i < 64; i++ {
		diff := abs32(coeffs[i] - first)
		if diff > maxDiff {
			maxDiff = diff
		}
	}
	if maxDiff > 2 {
		t.Errorf("DC-only IDCT8x8 max diff = %d, want <= 2", maxDiff)
	}
}

func TestIdentityTransform(t *testing.T) {
	coeffs := make([]int32, 16)
	coeffs[0] = 100
	coeffs[5] = 50

	InverseTransform2D(coeffs, 4, 4, TxTypeIDENTITY_IDENTITY)

	// Identity should scale but preserve structure.
	if coeffs[0] == 0 {
		t.Error("identity transform zeroed out coefficients")
	}
}

func abs32(x int32) int32 {
	if x < 0 {
		return -x
	}
	return x
}

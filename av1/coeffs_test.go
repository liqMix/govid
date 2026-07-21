package av1

import "testing"

func TestEOBPositionMapping(t *testing.T) {
	// Category 0 → position 0.
	// Category 1 → position 1.
	// Category 2 → positions 2-3 (1 extra bit).
	// Category 3 → positions 4-7 (2 extra bits).
	// Category 4 → positions 8-15 (3 extra bits).
	// etc.

	tests := []struct {
		cat      int
		minPos   int
		maxPos   int
	}{
		{0, 0, 0},
		{1, 1, 1},
		{2, 2, 3},
		{3, 4, 7},
		{4, 8, 15},
		{5, 16, 31},
	}

	for _, tt := range tests {
		base := eobCatBase(tt.cat)
		extraBits := eobCatExtraBits(tt.cat)

		if base != tt.minPos {
			t.Errorf("cat %d: base = %d, want %d", tt.cat, base, tt.minPos)
		}

		maxFromBits := base + (1 << extraBits) - 1
		if maxFromBits != tt.maxPos {
			t.Errorf("cat %d: max = %d, want %d", tt.cat, maxFromBits, tt.maxPos)
		}
	}
}

func TestTXSizeToCtx(t *testing.T) {
	// txSizeToCtx is the identity mapping matching dav1d's t_dim->ctx.
	if txSizeToCtx(TX4x4) != 0 {
		t.Errorf("TX4x4 ctx = %d, want 0", txSizeToCtx(TX4x4))
	}
	if txSizeToCtx(TX8x8) != 1 {
		t.Errorf("TX8x8 ctx = %d, want 1", txSizeToCtx(TX8x8))
	}
	if txSizeToCtx(TX16x16) != 2 {
		t.Errorf("TX16x16 ctx = %d, want 2", txSizeToCtx(TX16x16))
	}
	if txSizeToCtx(TX32x32) != 3 {
		t.Errorf("TX32x32 ctx = %d, want 3", txSizeToCtx(TX32x32))
	}
}

func TestCoeffBaseCtxNeighborSum(t *testing.T) {
	// Test that coefficient base context computation doesn't panic
	// with various block dimensions.
	td := &tileDecoder{}
	for _, txSize := range []int{TX4x4, TX8x8, TX16x16} {
		txW := TXWidth[txSize]
		txH := TXHeight[txSize]
		n := txW * txH
		scan := GetScanOrder(txSize, TxTypeDCT_DCT)
		levels := make([]int, n)

		// Set some coefficients.
		levels[0] = 5
		levels[1] = 3
		levels[txW] = 2

		for c := 0; c < n; c++ {
			ctx := td.getCoeffBaseCtxLo(levels, c, txW, txH, scan)
			if ctx < 0 || ctx > 40 {
				t.Errorf("txSize=%d pos=%d: ctx=%d out of range [0,40]", txSize, c, ctx)
			}
		}
	}
}

// eobCatBase returns the base EOB position for a category.
func eobCatBase(cat int) int {
	if cat == 0 {
		return 0
	}
	if cat == 1 {
		return 1
	}
	return 1 << (cat - 1)
}

// eobCatExtraBits returns the number of extra bits for an EOB category.
func eobCatExtraBits(cat int) int {
	if cat < 2 {
		return 0
	}
	return cat - 1
}

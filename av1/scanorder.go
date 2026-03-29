package av1

// Scan orders map linear coefficient positions to 2D raster positions
// within a transform block. AV1 uses a default diagonal scan.

// defaultScan4x4 is the diagonal scan order for 4x4 transforms.
var defaultScan4x4 = [16]int{
	0, 1, 4, 8,
	5, 2, 3, 6,
	9, 12, 13, 10,
	7, 11, 14, 15,
}

// defaultScan8x8 is the diagonal scan order for 8x8 transforms.
var defaultScan8x8 [64]int

// defaultScan16x16 is the diagonal scan order for 16x16 transforms.
var defaultScan16x16 [256]int

// defaultScan32x32 is the diagonal scan order for 32x32 transforms.
var defaultScan32x32 [1024]int

func init() {
	generateDiagonalScan(defaultScan8x8[:], 8, 8)
	generateDiagonalScan(defaultScan16x16[:], 16, 16)
	generateDiagonalScan(defaultScan32x32[:], 32, 32)
}

// generateDiagonalScan fills scan[] with a diagonal scan pattern for a w x h block.
// This traverses diagonals from top-right to bottom-left.
func generateDiagonalScan(scan []int, w, h int) {
	idx := 0
	for d := 0; d < w+h-1; d++ {
		// Each diagonal has constant (row + col) = d.
		// We scan from bottom-left to top-right within each diagonal.
		var r, c int
		if d < h {
			r = d
			c = 0
		} else {
			r = h - 1
			c = d - (h - 1)
		}
		for r >= 0 && c < w {
			scan[idx] = r*w + c
			idx++
			r--
			c++
		}
	}
}

// GetScanOrder returns the scan order for a given TX size and TX type.
// For Phase 3, the default diagonal scan is used for all types.
func GetScanOrder(txSize, txType int) []int {
	switch txSize {
	case TX4x4:
		return defaultScan4x4[:]
	case TX8x8:
		return defaultScan8x8[:]
	case TX16x16:
		return defaultScan16x16[:]
	case TX32x32:
		return defaultScan32x32[:]
	case TX64x64:
		// TX64x64 uses only the top-left 32x32 for coefficients.
		return defaultScan32x32[:]
	default:
		return defaultScan4x4[:]
	}
}

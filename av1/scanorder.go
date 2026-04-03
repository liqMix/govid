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

// generateDiagonalScan fills scan[] with a zigzag diagonal scan for a w x h block.
// AV1 alternates direction on each anti-diagonal:
//   odd d:  top-right → bottom-left (col decreasing, row increasing)
//   even d: bottom-left → top-right (row decreasing, col increasing)
func generateDiagonalScan(scan []int, w, h int) {
	idx := 0
	for d := 0; d < w+h-1; d++ {
		if d%2 == 1 {
			// Odd diagonal: start from top, go down-left.
			r := 0
			c := d
			if c >= w {
				r = d - (w - 1)
				c = w - 1
			}
			for r < h && c >= 0 {
				scan[idx] = r*w + c
				idx++
				r++
				c--
			}
		} else {
			// Even diagonal: start from left, go up-right.
			r := d
			c := 0
			if r >= h {
				c = d - (h - 1)
				r = h - 1
			}
			for r >= 0 && c < w {
				scan[idx] = r*w + c
				idx++
				r--
				c++
			}
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

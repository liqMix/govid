package ebiv

// Zigzag scan orders. Coefficients are coded low-frequency first so that the
// trailing high-frequency zeros collapse into a single end-of-block token. The
// tables are built once at init for each supported block size.

var scanOrders = map[int][]int{}

func init() {
	for _, n := range []int{4, 8, 16} {
		scanOrders[n] = buildZigzag(n)
	}
}

// buildZigzag returns the diagonal zigzag order for an n×n block: a list of
// row-major indices in the order they are coded.
func buildZigzag(n int) []int {
	order := make([]int, 0, n*n)
	for d := 0; d < 2*n-1; d++ {
		if d%2 == 0 {
			// Even diagonals travel up-right.
			for y := min(d, n-1); y >= 0 && d-y < n; y-- {
				x := d - y
				order = append(order, y*n+x)
			}
		} else {
			// Odd diagonals travel down-left.
			for x := min(d, n-1); x >= 0 && d-x < n; x-- {
				y := d - x
				order = append(order, y*n+x)
			}
		}
	}
	return order
}

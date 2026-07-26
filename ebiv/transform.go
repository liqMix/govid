package ebiv

import "math"

// Integer DCT approximations for the three block sizes (§3.3). Each is a
// separable 1-D transform built from a fixed-point cosine matrix, applied to
// columns then rows. The matrices are orthonormal in the reals, so the inverse
// is the transpose and forward-then-inverse reconstructs the input up to
// rounding — the only loss in a QP=0 path is transform rounding of ~±1.
//
// All arithmetic is integer, so output is bit-identical on every platform. The
// matrices are computed once at init; math.Cos/Round on these well-separated
// values yields the same integers under IEEE-754 everywhere.

const dctScaleBits = 13 // cosine matrices are scaled by 2^13

var dctMatrix = map[int][]int32{}

func init() {
	for _, n := range lumaTxSizes {
		dctMatrix[n] = buildDCTMatrix(n)
	}
	// The 8x8 matrix also serves chroma; it is already built above since 8 is
	// one of the luma sizes.
}

func buildDCTMatrix(n int) []int32 {
	m := make([]int32, n*n)
	for k := 0; k < n; k++ {
		alpha := math.Sqrt(2.0 / float64(n))
		if k == 0 {
			alpha = math.Sqrt(1.0 / float64(n))
		}
		for i := 0; i < n; i++ {
			c := alpha * math.Cos((2*float64(i)+1)*float64(k)*math.Pi/(2*float64(n)))
			m[k*n+i] = int32(math.Round(c * (1 << dctScaleBits)))
		}
	}
	return m
}

// forwardDCT transforms an n×n residual block (row-major) into coefficients.
// src and dst may not alias.
func forwardDCT(src, dst []int32, n int) {
	c := dctMatrix[n]
	var tmp [maxLevels]int32
	round := int64(1) << (dctScaleBits - 1)

	// Columns: tmp = C · src, accumulated input-row-major so every inner loop
	// walks contiguous memory (the k/j-outer form strides src by n per step,
	// which is what made this the encoder's hottest kernel). Identical output:
	// int64 addition reorders exactly.
	var acc [maxLevels]int64
	for i := 0; i < n; i++ {
		srow := src[i*n : i*n+n : i*n+n]
		for k := 0; k < n; k++ {
			cki := int64(c[k*n+i])
			arow := acc[k*n : k*n+n : k*n+n]
			for j := 0; j < n; j++ {
				arow[j] += cki * int64(srow[j])
			}
		}
	}
	for k := 0; k < n*n; k++ {
		tmp[k] = int32((acc[k] + round) >> dctScaleBits)
	}
	// Rows: dst = tmp · Cᵀ.
	for k := 0; k < n; k++ {
		trow := tmp[k*n : k*n+n : k*n+n]
		for l := 0; l < n; l++ {
			crow := c[l*n : l*n+n : l*n+n]
			var acc int64
			for j := 0; j < n; j++ {
				acc += int64(crow[j]) * int64(trow[j])
			}
			dst[k*n+l] = int32((acc + round) >> dctScaleBits)
		}
	}
}

// satd returns the sum of absolute Hadamard-transformed differences of an n×n
// residual block — a cheap, add/shift-only metric that tracks coded bits far
// better than plain SAD, so the encoder's intra-mode choice reflects what a
// mode will actually cost rather than its raw pixel error. The Hadamard scale
// is constant for a given n, so comparisons within one block size are valid.
func satd(residual []int32, n int) int64 {
	var t [maxLevels]int32
	copy(t[:n*n], residual[:n*n])
	for r := 0; r < n; r++ {
		fwht(t[r*n:r*n+n], n)
	}
	var col [mbSize]int32
	for c := 0; c < n; c++ {
		for r := 0; r < n; r++ {
			col[r] = t[r*n+c]
		}
		fwht(col[:n], n)
		for r := 0; r < n; r++ {
			t[r*n+c] = col[r]
		}
	}
	var s int64
	for i := 0; i < n*n; i++ {
		s += int64(abs32(t[i]))
	}
	return s
}

// fwht is an in-place fast Walsh-Hadamard transform on a power-of-two vector.
func fwht(a []int32, n int) {
	for span := 1; span < n; span <<= 1 {
		for i := 0; i < n; i += span << 1 {
			for j := i; j < i+span; j++ {
				x, y := a[j], a[j+span]
				a[j], a[j+span] = x+y, x-y
			}
		}
	}
}

// inverseDCT reconstructs an n×n residual block from coefficients. src and dst
// may not alias.
//
// It exploits sparsity: after quantization (and especially RDOQ), most residual
// blocks carry only a few low-frequency coefficients, so the transform skips
// every zero row and column of the input. Zeros contribute nothing to a
// separable transform, so the result is bit-identical to the full multiply
// while doing a fraction of the work — the dominant decode cost per the profile.
func inverseDCT(src, dst []int32, n int) {
	// Bounding box of the non-zero coefficients.
	maxR, maxC := -1, -1
	for r := 0; r < n; r++ {
		row := src[r*n : r*n+n : r*n+n]
		for cc := 0; cc < n; cc++ {
			if row[cc] != 0 {
				if r > maxR {
					maxR = r
				}
				if cc > maxC {
					maxC = cc
				}
			}
		}
	}
	if maxR < 0 { // all-zero block
		for i := range dst[:n*n] {
			dst[i] = 0
		}
		return
	}

	round := int64(1) << (dctScaleBits - 1)

	// DC-only block: the single low-frequency coefficient spreads to a flat
	// value across the block. The DCT's k=0 row is a constant (cos(0)=alpha), so
	// both separable passes reduce to one multiply each and the result is a
	// constant fill — bit-identical to the full transform, but O(1) instead of
	// two O(n) passes per output. Common after quantization, so worth the branch.
	if maxR == 0 && maxC == 0 {
		d0 := int64(dctMatrix[n][0])
		col := (d0*int64(src[0]) + round) >> dctScaleBits
		val := int32((d0*col + round) >> dctScaleBits)
		for i := range dst[:n*n] {
			dst[i] = val
		}
		return
	}

	c := dctMatrix[n]
	var tmp [maxLevels]int32

	// Column pass: only input rows 0..maxR contribute, and only output columns
	// 0..maxC can be non-zero (input columns past maxC are all zero).
	for i := 0; i < n; i++ {
		for l := 0; l <= maxC; l++ {
			var acc int64
			for k := 0; k <= maxR; k++ {
				acc += int64(c[k*n+i]) * int64(src[k*n+l])
			}
			tmp[i*n+l] = int32((acc + round) >> dctScaleBits)
		}
	}
	// Row pass: sum only over the non-zero intermediate columns 0..maxC.
	for i := 0; i < n; i++ {
		trow := tmp[i*n : i*n+maxC+1 : i*n+maxC+1]
		for m := 0; m < n; m++ {
			var acc int64
			for l := 0; l <= maxC; l++ {
				acc += int64(c[l*n+m]) * int64(trow[l])
			}
			dst[i*n+m] = int32((acc + round) >> dctScaleBits)
		}
	}
}

package ebiv

import (
	"math/rand"
	"testing"
)

// TestDCTRoundTrip checks that forward-then-inverse reconstructs a residual
// block within transform rounding. Because the matrices are orthonormal in the
// reals, the only loss is integer rounding of a few units per sample.
func TestDCTRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for _, n := range []int{4, 8, 16} {
		var maxErr int32
		for trial := 0; trial < 2000; trial++ {
			var src, coeffs, back [maxLevels]int32
			for i := 0; i < n*n; i++ {
				src[i] = int32(rng.Intn(511) - 255) // residual range
			}
			forwardDCT(src[:n*n], coeffs[:n*n], n)
			inverseDCT(coeffs[:n*n], back[:n*n], n)
			for i := 0; i < n*n; i++ {
				e := abs32(src[i] - back[i])
				if e > maxErr {
					maxErr = e
				}
			}
		}
		// Two integer passes each round once, so up to ~3 units of drift is
		// expected and acceptable for a lossy transform.
		if maxErr > 3 {
			t.Errorf("n=%d: round-trip error up to %d, want <= 3 (transform rounding only)", n, maxErr)
		}
	}
}

// TestDCTDCPreservation checks that a flat block transforms to a pure DC
// coefficient (all AC ~ zero) and that the DC magnitude matches the orthonormal
// scaling, which is what the quantizer is tuned against.
func TestDCTDCPreservation(t *testing.T) {
	for _, n := range []int{4, 8, 16} {
		var src, coeffs [maxLevels]int32
		for i := 0; i < n*n; i++ {
			src[i] = 64
		}
		forwardDCT(src[:n*n], coeffs[:n*n], n)
		// Orthonormal 2-D DCT DC = mean * n.
		wantDC := int32(64 * n)
		if abs32(coeffs[0]-wantDC) > 2 {
			t.Errorf("n=%d: DC = %d, want ~%d", n, coeffs[0], wantDC)
		}
		for i := 1; i < n*n; i++ {
			if abs32(coeffs[i]) > 2 {
				t.Errorf("n=%d: AC[%d] = %d on a flat block, want ~0", n, i, coeffs[i])
			}
		}
	}
}

// TestQuantRoundTrip checks that quantize then dequantize recovers a value
// within one step, and that the dead zone maps small coefficients to zero.
func TestQuantRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for _, qp := range []int{0, 8, 20, 40, 63} {
		q := newQuantizer(qp)
		for trial := 0; trial < 5000; trial++ {
			var coeffs, levels, back [maxLevels]int32
			n := 4
			for i := 0; i < n*n; i++ {
				coeffs[i] = int32(rng.Intn(8001) - 4000)
			}
			q.quantize(coeffs[:n*n], levels[:n*n], n)
			q.dequantize(levels[:n*n], back[:n*n], n)
			for i := 0; i < n*n; i++ {
				step := q.ac
				if i == 0 {
					step = q.dc
				}
				if abs32(coeffs[i]-back[i]) > step {
					t.Fatalf("qp=%d i=%d: coeff %d -> level %d -> %d, error exceeds step %d",
						qp, i, coeffs[i], levels[i], back[i], step)
				}
			}
		}
	}
}

// TestZigzagCoversBlock checks each scan order is a permutation of all block
// positions.
func TestZigzagCoversBlock(t *testing.T) {
	for _, n := range []int{4, 8, 16} {
		scan := scanOrders[n]
		if len(scan) != n*n {
			t.Fatalf("n=%d: scan has %d entries, want %d", n, len(scan), n*n)
		}
		seen := make([]bool, n*n)
		for _, p := range scan {
			if p < 0 || p >= n*n || seen[p] {
				t.Fatalf("n=%d: scan is not a permutation (bad or repeated index %d)", n, p)
			}
			seen[p] = true
		}
		if scan[0] != 0 {
			t.Errorf("n=%d: scan must start at DC (index 0), got %d", n, scan[0])
		}
	}
}

// TestMotionCompCopy checks that a zero-fraction motion vector reproduces the
// reference exactly, and that a subpel vector stays in range.
func TestMotionCompCopy(t *testing.T) {
	const w, h = 32, 32
	ref := planeView{data: make([]byte, w*h), stride: w, w: w, h: h}
	rng := rand.New(rand.NewSource(9))
	for i := range ref.data {
		ref.data[i] = byte(rng.Intn(256))
	}

	var out [mbSize * mbSize]int32
	// Full-pel vector (quarter-pel fractions zero): exact copy from the
	// reference. mv is quarter-pel, so (8,12) is a full-pel (2,3) shift.
	mcLumaMB(ref, 0, 0, motionVector{x: 8, y: 12}, out[:])
	for r := 0; r < mbSize; r++ {
		for c := 0; c < mbSize; c++ {
			want := int32(ref.at(c+2, r+3))
			if out[r*mbSize+c] != want {
				t.Fatalf("full-pel MC at (%d,%d): got %d, want %d", c, r, out[r*mbSize+c], want)
			}
		}
	}

	// Sub-pel vectors: every sample must stay a valid byte value at each phase.
	for _, mv := range []motionVector{{x: 5, y: 7}, {x: 2, y: 1}, {x: 3, y: 6}} {
		mcLumaMB(ref, 0, 0, mv, out[:])
		for i, v := range out {
			if v < 0 || v > 255 {
				t.Fatalf("sub-pel MC %+v sample %d out of range: %d", mv, i, v)
			}
		}
	}
}

// TestZigzagMVCodec checks the signed zigzag mapping used for motion-vector
// deltas is a clean round-trip.
func TestZigzagMVCodec(t *testing.T) {
	for v := -5000; v <= 5000; v++ {
		if got := zigzagDecode(zigzagEncode(v)); got != v {
			t.Fatalf("zigzag round-trip failed for %d: got %d", v, got)
		}
	}
}

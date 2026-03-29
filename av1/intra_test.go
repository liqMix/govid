package av1

import "testing"

func TestPredDC(t *testing.T) {
	dst := make([]uint8, 16)
	above := []uint8{100, 100, 100, 100}
	left := []uint8{200, 200, 200, 200}

	PredictIntra(dst, 4, IntraDC, 4, 4, above, left, 0, true, true)

	// Average of above (100) and left (200) = 150.
	for i := 0; i < 16; i++ {
		if dst[i] != 150 {
			t.Errorf("DC pred[%d] = %d, want 150", i, dst[i])
			break
		}
	}
}

func TestPredDCNoRef(t *testing.T) {
	dst := make([]uint8, 16)
	PredictIntra(dst, 4, IntraDC, 4, 4, nil, nil, 0, false, false)

	// No references: default to 128.
	for i := 0; i < 16; i++ {
		if dst[i] != 128 {
			t.Errorf("DC no-ref[%d] = %d, want 128", i, dst[i])
			break
		}
	}
}

func TestPredVertical(t *testing.T) {
	dst := make([]uint8, 16)
	above := []uint8{10, 20, 30, 40}

	PredictIntra(dst, 4, IntraVertical, 4, 4, above, nil, 0, true, false)

	// Each row should copy the above samples.
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			want := above[c]
			got := dst[r*4+c]
			if got != want {
				t.Errorf("V pred[%d,%d] = %d, want %d", r, c, got, want)
			}
		}
	}
}

func TestPredHorizontal(t *testing.T) {
	dst := make([]uint8, 16)
	left := []uint8{10, 20, 30, 40}

	PredictIntra(dst, 4, IntraHorizontal, 4, 4, nil, left, 0, false, true)

	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			want := left[r]
			got := dst[r*4+c]
			if got != want {
				t.Errorf("H pred[%d,%d] = %d, want %d", r, c, got, want)
			}
		}
	}
}

func TestPredPaeth(t *testing.T) {
	dst := make([]uint8, 16)
	above := []uint8{100, 100, 100, 100}
	left := []uint8{100, 100, 100, 100}
	topLeft := uint8(100)

	PredictIntra(dst, 4, IntraPaeth, 4, 4, above, left, topLeft, true, true)

	// When all references are equal, Paeth should output that value.
	for i := 0; i < 16; i++ {
		if dst[i] != 100 {
			t.Errorf("Paeth uniform[%d] = %d, want 100", i, dst[i])
			break
		}
	}
}

func TestPredSmooth(t *testing.T) {
	dst := make([]uint8, 16)
	above := []uint8{200, 200, 200, 200}
	left := []uint8{100, 100, 100, 100}

	PredictIntra(dst, 4, IntraSmooth, 4, 4, above, left, 0, true, true)

	// Values should be in between above and left.
	for i := 0; i < 16; i++ {
		if dst[i] < 50 || dst[i] > 250 {
			t.Errorf("Smooth[%d] = %d, out of expected range", i, dst[i])
		}
	}
}

func TestPredSmoothV(t *testing.T) {
	dst := make([]uint8, 16)
	above := []uint8{200, 200, 200, 200}
	left := []uint8{100, 100, 100, 100}

	PredictIntra(dst, 4, IntraSmoothV, 4, 4, above, left, 0, true, true)

	// Top row should be close to above (200), bottom closer to below pred (left[3]=100).
	if dst[0] < 150 {
		t.Errorf("SmoothV top = %d, expected close to 200", dst[0])
	}
	if dst[12] > 150 {
		t.Errorf("SmoothV bottom = %d, expected close to 100", dst[12])
	}
}

func TestPredDirectionalV(t *testing.T) {
	dst := make([]uint8, 16)
	above := []uint8{10, 20, 30, 40, 50, 60, 70, 80}

	PredictIntra(dst, 4, IntraD67, 4, 4, above, nil, 0, true, false)

	// D67 is a near-vertical angle. Top-left values should be close to above values.
	// Just verify no panic and values are in reasonable range.
	for i := 0; i < 16; i++ {
		if dst[i] > 80 {
			t.Errorf("D67[%d] = %d, unexpectedly high", i, dst[i])
		}
	}
}

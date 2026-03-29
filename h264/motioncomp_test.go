package h264

import (
	"image"
	"testing"
)

func TestFilter6Tap(t *testing.T) {
	// 6-tap FIR: [1, -5, 20, 20, -5, 1]
	// For uniform value 100: 1*100 - 5*100 + 20*100 + 20*100 - 5*100 + 1*100 = 100*32 = 3200
	// After (3200 + 16) >> 5 = 100.5 -> 100
	v := filter6Tap(100, 100, 100, 100, 100, 100)
	result := (v + 16) >> 5
	if result != 100 {
		t.Errorf("6-tap uniform: got %d, want 100", result)
	}

	// Edge case: step function
	v = filter6Tap(0, 0, 0, 255, 255, 255)
	result = (v + 16) >> 5
	if result < 0 || result > 255 {
		t.Errorf("6-tap step: result %d out of range", result)
	}
}

func TestHalfPelH(t *testing.T) {
	// Create a simple 8x1 reference with known values.
	ref := make([]byte, 8)
	for i := range ref {
		ref[i] = 128
	}
	// Half-pel interpolation of uniform region should return same value.
	v := halfPelH(ref, 8, 8, 1, 3, 0)
	if v != 128 {
		t.Errorf("halfPelH uniform: got %d, want 128", v)
	}
}

func TestHalfPelV(t *testing.T) {
	// Create 1x8 reference column.
	ref := make([]byte, 8)
	for i := range ref {
		ref[i] = 64
	}
	v := halfPelV(ref, 1, 1, 8, 0, 3)
	if v != 64 {
		t.Errorf("halfPelV uniform: got %d, want 64", v)
	}
}

func TestBilinearChromaFullPel(t *testing.T) {
	// Full-pixel chroma (no fractional).
	ref := make([]byte, 16)
	for i := range ref {
		ref[i] = 200
	}
	v := bilinearChroma(ref, 4, 4, 4, 1, 1, 0, 0)
	if v != 200 {
		t.Errorf("bilinear full-pel: got %d, want 200", v)
	}
}

func TestBilinearChromaFrac(t *testing.T) {
	// Test bilinear interpolation with known 2x2 pattern.
	ref := []byte{
		0, 100,
		200, 50,
	}
	// At (0,0) with fracX=4, fracY=4 (midpoint): average of all 4.
	// (4*4*0 + 4*4*100 + 4*4*200 + 4*4*50 + 32) >> 6
	// = 16*(0+100+200+50) + 32) >> 6 = (5600+32)>>6 = 88
	v := bilinearChroma(ref, 2, 2, 2, 0, 0, 4, 4)
	expected := uint8((16*0 + 16*100 + 16*200 + 16*50 + 32) >> 6)
	if v != expected {
		t.Errorf("bilinear midpoint: got %d, want %d", v, expected)
	}
}

func TestMotionCompLumaFullPel(t *testing.T) {
	d := NewDecoder()
	d.mbw = 1
	d.mbh = 1
	d.img = image.NewYCbCr(image.Rect(0, 0, 16, 16), image.YCbCrSubsampleRatio420)

	// Create a reference frame with known pattern.
	ref := image.NewYCbCr(image.Rect(0, 0, 16, 16), image.YCbCrSubsampleRatio420)
	for j := 0; j < 16; j++ {
		for i := 0; i < 16; i++ {
			ref.Y[j*ref.YStride+i] = uint8((j*16 + i) & 0xFF)
		}
	}

	// Full-pixel MV (0,0) should copy reference directly.
	d.motionCompLuma(ref, 0, 0, [2]int16{0, 0}, ybrYY, ybrYX, 16, 16)

	for j := 0; j < 16; j++ {
		for i := 0; i < 16; i++ {
			expected := uint8((j*16 + i) & 0xFF)
			got := d.ybr[ybrYY+j][ybrYX+i]
			if got != expected {
				t.Errorf("pixel (%d,%d): got %d, want %d", i, j, got, expected)
				return
			}
		}
	}
}

func TestMotionCompChromaFullPel(t *testing.T) {
	d := NewDecoder()
	d.mbw = 1
	d.mbh = 1
	d.img = image.NewYCbCr(image.Rect(0, 0, 16, 16), image.YCbCrSubsampleRatio420)

	ref := image.NewYCbCr(image.Rect(0, 0, 16, 16), image.YCbCrSubsampleRatio420)
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			ref.Cb[j*ref.CStride+i] = uint8(j*8 + i)
			ref.Cr[j*ref.CStride+i] = uint8(63 - j*8 - i)
		}
	}

	// Full-pixel MV (0,0).
	d.motionCompChroma(ref, 0, 0, [2]int16{0, 0})

	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			gotCb := d.ybr[ybrBY+j][ybrBX+i]
			wantCb := uint8(j*8 + i)
			if gotCb != wantCb {
				t.Errorf("Cb (%d,%d): got %d, want %d", i, j, gotCb, wantCb)
				return
			}
			gotCr := d.ybr[ybrRY+j][ybrRX+i]
			wantCr := uint8(63 - j*8 - i)
			if gotCr != wantCr {
				t.Errorf("Cr (%d,%d): got %d, want %d", i, j, gotCr, wantCr)
				return
			}
		}
	}
}

func TestClampPix(t *testing.T) {
	tests := []struct {
		x, lo, hi, want int
	}{
		{5, 0, 10, 5},
		{-1, 0, 10, 0},
		{15, 0, 10, 10},
		{0, 0, 0, 0},
	}
	for _, tt := range tests {
		got := clampPix(tt.x, tt.lo, tt.hi)
		if got != tt.want {
			t.Errorf("clampPix(%d, %d, %d) = %d, want %d", tt.x, tt.lo, tt.hi, got, tt.want)
		}
	}
}

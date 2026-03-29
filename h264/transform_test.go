package h264

import "testing"

func TestIDCT4x4Identity(t *testing.T) {
	// A single DC coefficient should spread uniformly.
	coeffs := make([]int16, 16)
	coeffs[0] = 64 // DC * 64 (pre-scaled)
	idct4x4(coeffs)
	for i, c := range coeffs {
		if c != 1 {
			t.Errorf("idct4x4 DC-only: coeffs[%d] = %d, want 1", i, c)
		}
	}
}

func TestIDCT4x4DCOnly(t *testing.T) {
	coeffs := make([]int16, 16)
	coeffs[0] = 128
	idct4x4DC(coeffs)
	expected := int16((128 + 32) >> 6)
	for i, c := range coeffs {
		if c != expected {
			t.Errorf("idct4x4DC: coeffs[%d] = %d, want %d", i, c, expected)
		}
	}
}

func TestHadamard4x4RoundTrip(t *testing.T) {
	// Forward Hadamard is its own inverse (up to scaling).
	// Input: only DC at [0,0].
	coeffs := make([]int16, 16)
	coeffs[0] = 16
	hadamard4x4(coeffs)
	// After Hadamard, all 16 positions should be 16 (since H*[16,0,...,0] = [16,16,...,16]).
	for i, c := range coeffs {
		if c != 16 {
			t.Errorf("hadamard4x4: coeffs[%d] = %d, want 16", i, c)
		}
	}
}

func TestHadamard2x2(t *testing.T) {
	coeffs := []int16{4, 0, 0, 0}
	hadamard2x2(coeffs)
	// H * [4,0,0,0] = [4,4,4,4]
	for i, c := range coeffs {
		if c != 4 {
			t.Errorf("hadamard2x2: coeffs[%d] = %d, want 4", i, c)
		}
	}
}

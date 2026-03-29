package av1

import "testing"

func TestDCQuantLookup(t *testing.T) {
	// Verify key values from the AV1 spec.
	if GetDCQuant(0) != 4 {
		t.Errorf("DC quant[0] = %d, want 4", GetDCQuant(0))
	}
	if GetDCQuant(255) != 1336 {
		t.Errorf("DC quant[255] = %d, want 1336", GetDCQuant(255))
	}
	// Verify monotonically non-decreasing.
	for i := 1; i < 256; i++ {
		if GetDCQuant(i) < GetDCQuant(i-1) {
			t.Errorf("DC quant not monotonic at %d: %d < %d", i, GetDCQuant(i), GetDCQuant(i-1))
		}
	}
}

func TestACQuantLookup(t *testing.T) {
	if GetACQuant(0) != 4 {
		t.Errorf("AC quant[0] = %d, want 4", GetACQuant(0))
	}
	if GetACQuant(255) != 1793 {
		t.Errorf("AC quant[255] = %d, want 1793", GetACQuant(255))
	}
	for i := 1; i < 256; i++ {
		if GetACQuant(i) < GetACQuant(i-1) {
			t.Errorf("AC quant not monotonic at %d", i)
		}
	}
}

func TestDequantCoeffs(t *testing.T) {
	coeffs := []int32{10, 5, -3, 0}
	DequantCoeffs(coeffs, 100, 50)
	if coeffs[0] != 1000 {
		t.Errorf("DC: %d, want 1000", coeffs[0])
	}
	if coeffs[1] != 250 {
		t.Errorf("AC[1]: %d, want 250", coeffs[1])
	}
	if coeffs[2] != -150 {
		t.Errorf("AC[2]: %d, want -150", coeffs[2])
	}
	if coeffs[3] != 0 {
		t.Errorf("AC[3]: %d, want 0", coeffs[3])
	}
}

func TestDCQuantClamp(t *testing.T) {
	// Out of range indices should be clamped.
	if GetDCQuant(-1) != GetDCQuant(0) {
		t.Error("negative index not clamped")
	}
	if GetDCQuant(300) != GetDCQuant(255) {
		t.Error("index > 255 not clamped")
	}
}

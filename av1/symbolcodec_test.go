package av1

import (
	"testing"
)

func TestReadLiteralSymbolCodec(t *testing.T) {
	// With all-zero data, ReadBool(128) returns false for each bit.
	sc := NewSymbolCodec([]byte{0x00, 0x00, 0x00, 0x00})
	val, err := sc.ReadLiteral(8)
	if err != nil {
		t.Fatal(err)
	}
	if val != 0 {
		t.Errorf("ReadLiteral(8) with zeros = %d, want 0", val)
	}
}

func TestReadLiteralAllOnes(t *testing.T) {
	// With 0xFF data, ReadBool(128) returns true for each bit.
	sc := NewSymbolCodec([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	val, err := sc.ReadLiteral(8)
	if err != nil {
		t.Fatal(err)
	}
	if val != 255 {
		t.Errorf("ReadLiteral(8) with 0xFF = %d, want 255", val)
	}
}

func TestReadBoolProbability(t *testing.T) {
	// With value=0x8000 (midpoint) and prob=128 (50%):
	// split ≈ range/2. value >= split → true.
	sc := NewSymbolCodec([]byte{0x80, 0x00, 0x00, 0x00})
	b, err := sc.ReadBool(128)
	if err != nil {
		t.Fatal(err)
	}
	if !b {
		t.Error("ReadBool(128) with 0x8000 = false, want true")
	}
}

func TestCDFAdaptation(t *testing.T) {
	// Test that CDF values adapt after symbol decoding (descending format).
	cdf := []uint16{16384, 0, 0} // nsymbs=2, initial prob 50/50

	// Decode symbol 0: cdf[0] should decrease (symbol 0 becomes more likely).
	UpdateCDF(cdf, 2, 0)
	if cdf[0] >= 16384 {
		t.Errorf("after symbol 0: cdf[0] = %d, want < 16384", cdf[0])
	}
	if cdf[2] != 1 {
		t.Errorf("after symbol 0: count = %d, want 1", cdf[2])
	}

	// Decode symbol 1: cdf[0] should increase (symbol 0 becomes less likely).
	prev := cdf[0]
	UpdateCDF(cdf, 2, 1)
	if cdf[0] <= prev {
		t.Errorf("after symbol 1: cdf[0] = %d, want > %d", cdf[0], prev)
	}
	if cdf[2] != 2 {
		t.Errorf("after symbol 1: count = %d, want 2", cdf[2])
	}
}

func TestCDFCountCap(t *testing.T) {
	// Count should cap at 32.
	cdf := []uint16{16384, 0, 31}
	UpdateCDF(cdf, 2, 0)
	if cdf[2] != 32 {
		t.Errorf("count after 31+1 = %d, want 32", cdf[2])
	}
	UpdateCDF(cdf, 2, 0)
	if cdf[2] != 32 {
		t.Errorf("count should stay at 32, got %d", cdf[2])
	}
}

func TestReadSymbolTwoSymbols(t *testing.T) {
	// 2-symbol descending CDF: 4768 means P(sym=0)=(32768-4768)/32768≈85%.
	// With XOR complement convention:
	// Low raw value (0x00) → high complement → symbol 0 (most probable).
	cdf := []uint16{4768, 0, 0}
	sc := NewSymbolCodec([]byte{0x00, 0x00, 0x00, 0x00})
	sym, err := sc.ReadSymbol(cdf[:], 2)
	if err != nil {
		t.Fatal(err)
	}
	if sym != 0 {
		t.Errorf("ReadSymbol with low raw value = %d, want 0", sym)
	}

	// High raw value (0xFF) → low complement → symbol 1 (least probable).
	cdf2 := []uint16{4768, 0, 0}
	sc2 := NewSymbolCodec([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	sym2, err := sc2.ReadSymbol(cdf2[:], 2)
	if err != nil {
		t.Fatal(err)
	}
	if sym2 != 1 {
		t.Errorf("ReadSymbol with high raw value = %d, want 1", sym2)
	}
}

func TestReadSymbolMultiple(t *testing.T) {
	// Decode several symbols in sequence and verify no errors.
	cdf := []uint16{8768, 16768, 24768, 0, 0} // 4 symbols, descending
	sc := NewSymbolCodec([]byte{0x40, 0x80, 0xC0, 0x20, 0x60, 0xA0})

	for i := 0; i < 4; i++ {
		_, err := sc.ReadSymbol(cdf[:], 4)
		if err != nil {
			t.Fatalf("ReadSymbol %d: %v", i, err)
		}
	}
}

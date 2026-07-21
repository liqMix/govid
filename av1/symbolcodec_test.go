package av1

import (
	"testing"
)

func TestReadLiteralSymbolCodec(t *testing.T) {
	sc := NewSymbolCodec([]byte{0x00, 0x00, 0x00, 0x00})
	val := sc.ReadLiteral(8)
	if val != 0 {
		t.Errorf("ReadLiteral(8) with zeros = %d, want 0", val)
	}
}

func TestReadLiteralAllOnes(t *testing.T) {
	sc := NewSymbolCodec([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	val := sc.ReadLiteral(8)
	if val != 255 {
		t.Errorf("ReadLiteral(8) with 0xFF = %d, want 255", val)
	}
}

func TestReadBoolProbability(t *testing.T) {
	sc := NewSymbolCodec([]byte{0x80, 0x00, 0x00, 0x00})
	b := sc.ReadBool(128)
	if !b {
		t.Error("ReadBool(128) with 0x8000 = false, want true")
	}
}

func TestReadBoolEqui(t *testing.T) {
	sc := NewSymbolCodec([]byte{0x00, 0x00, 0x00, 0x00})
	b := sc.ReadBoolEqui()
	if b != false {
		t.Errorf("ReadBoolEqui with 0x00 = %v, want false", b)
	}
	sc2 := NewSymbolCodec([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	b2 := sc2.ReadBoolEqui()
	if b2 != true {
		t.Errorf("ReadBoolEqui with 0xFF = %v, want true", b2)
	}
}

func TestCDFAdaptation(t *testing.T) {
	cdf := []uint16{16384, 0}
	UpdateCDF(cdf, 2, 0)
	if cdf[0] >= 16384 {
		t.Errorf("after symbol 0: cdf[0] = %d, want < 16384", cdf[0])
	}
	prev := cdf[0]
	UpdateCDF(cdf, 2, 1)
	if cdf[0] <= prev {
		t.Errorf("after symbol 1: cdf[0] = %d, want > %d", cdf[0], prev)
	}
}

func TestCDFCountCap(t *testing.T) {
	cdf := []uint16{16384, 31}
	UpdateCDF(cdf, 2, 0)
	if cdf[1] != 32 {
		t.Errorf("count = %d, want 32", cdf[1])
	}
	UpdateCDF(cdf, 2, 0)
	if cdf[1] != 32 {
		t.Errorf("count should stay at 32, got %d", cdf[1])
	}
}

func TestReadSymbolTwoSymbols(t *testing.T) {
	cdf := []uint16{4768, 0}
	sc := NewSymbolCodec([]byte{0x00, 0x00, 0x00, 0x00})
	sym := sc.ReadSymbol(cdf[:], 2)
	if sym != 0 {
		t.Errorf("ReadSymbol with low raw value = %d, want 0", sym)
	}

	cdf2 := []uint16{4768, 0}
	sc2 := NewSymbolCodec([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	sym2 := sc2.ReadSymbol(cdf2[:], 2)
	if sym2 != 1 {
		t.Errorf("ReadSymbol with high raw value = %d, want 1", sym2)
	}
}

func TestReadSymbolMultiple(t *testing.T) {
	cdf := []uint16{8768, 16768, 24768, 0}
	sc := NewSymbolCodec([]byte{0x40, 0x80, 0xC0, 0x20, 0x60, 0xA0})
	for i := 0; i < 4; i++ {
		sc.ReadSymbol(cdf[:], 4)
	}
}

func TestReadBoolAdapt(t *testing.T) {
	cdf := []uint16{16384, 0}
	sc := NewSymbolCodec([]byte{0x00, 0x00, 0x00, 0x00})
	b := sc.ReadBoolAdapt(cdf[:])
	if b != false {
		t.Errorf("ReadBoolAdapt with 0x00 = %v, want false", b)
	}
}

func TestAllowUpdateCDF(t *testing.T) {
	cdf := []uint16{16384, 0}
	sc := NewSymbolCodec([]byte{0x00, 0x00, 0x00, 0x00})
	sc.ReadSymbol(cdf[:], 2)
	if cdf[1] != 1 {
		t.Errorf("count = %d, want 1", cdf[1])
	}
	cdf2 := []uint16{16384, 0}
	sc2 := NewSymbolCodec([]byte{0x00, 0x00, 0x00, 0x00})
	sc2.allowUpdateCDF = false
	sc2.ReadSymbol(cdf2[:], 2)
	if cdf2[1] != 0 {
		t.Errorf("count with updates disabled = %d, want 0", cdf2[1])
	}
}

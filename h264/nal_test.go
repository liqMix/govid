package h264

import (
	"encoding/binary"
	"testing"
)

func TestParseNALUnits(t *testing.T) {
	// Create a packet with two NAL units using 4-byte length prefix.
	// NAL 1: type 7 (SPS), ref_idc=3 → header = 0x67
	spsPayload := []byte{0x42, 0x00, 0x1e} // profile=66, compat=0, level=30
	// NAL 2: type 8 (PPS), ref_idc=3 → header = 0x68
	ppsPayload := []byte{0xce, 0x38, 0x80}

	nal1 := append([]byte{0x67}, spsPayload...)
	nal2 := append([]byte{0x68}, ppsPayload...)

	data := make([]byte, 0, 4+len(nal1)+4+len(nal2))
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(len(nal1)))
	data = append(data, buf...)
	data = append(data, nal1...)
	binary.BigEndian.PutUint32(buf, uint32(len(nal2)))
	data = append(data, buf...)
	data = append(data, nal2...)

	units, err := ParseNALUnits(data, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 {
		t.Fatalf("expected 2 NAL units, got %d", len(units))
	}

	if units[0].Type != NALSPS {
		t.Fatalf("expected NAL type %d (SPS), got %d", NALSPS, units[0].Type)
	}
	if units[0].RefIDC != 3 {
		t.Fatalf("expected ref_idc 3, got %d", units[0].RefIDC)
	}
	if units[1].Type != NALPPS {
		t.Fatalf("expected NAL type %d (PPS), got %d", NALPPS, units[1].Type)
	}
}

func TestRemoveEmulationPreventionBytes(t *testing.T) {
	// 00 00 03 should become 00 00
	input := []byte{0x01, 0x00, 0x00, 0x03, 0x02, 0x03}
	got := removeEmulationPreventionBytes(input)
	expected := []byte{0x01, 0x00, 0x00, 0x02, 0x03}
	if len(got) != len(expected) {
		t.Fatalf("length mismatch: got %d, want %d", len(got), len(expected))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("byte %d: got 0x%02x, want 0x%02x", i, got[i], expected[i])
		}
	}
}

func TestParseNALUnitsEmpty(t *testing.T) {
	units, err := ParseNALUnits(nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 0 {
		t.Fatalf("expected 0 units from nil data, got %d", len(units))
	}
}

func TestParseNALUnitsTruncated(t *testing.T) {
	// Length says 100 bytes but only 2 available.
	data := make([]byte, 6)
	binary.BigEndian.PutUint32(data, 100)
	data[4] = 0x65 // IDR header
	data[5] = 0x00
	_, err := ParseNALUnits(data, 4)
	if err == nil {
		t.Fatal("expected error for truncated NAL unit")
	}
}

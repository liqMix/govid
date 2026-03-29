package av1

import (
	"testing"
)

func TestParseOBUHeader(t *testing.T) {
	tests := []struct {
		name         string
		data         []byte
		wantType     int
		wantExt      bool
		wantSize     bool
		wantTemporal int
		wantSpatial  int
		wantBytes    int
	}{
		{
			name:     "sequence header with size",
			data:     []byte{0x0A}, // 0 0001 0 1 0 = type=1, no ext, has size
			wantType: OBUSequenceHeader, wantExt: false, wantSize: true,
			wantBytes: 1,
		},
		{
			name:     "temporal delimiter with size",
			data:     []byte{0x12}, // 0 0010 0 1 0 = type=2, no ext, has size
			wantType: OBUTemporalDelimiter, wantExt: false, wantSize: true,
			wantBytes: 1,
		},
		{
			name:     "frame with extension",
			data:     []byte{0x34, 0x28}, // 0 0110 1 0 0, ext: 001 01 000 = temp=1,spatial=1
			wantType: OBUFrame, wantExt: true, wantSize: false,
			wantTemporal: 1, wantSpatial: 1,
			wantBytes: 2,
		},
		{
			name:     "frame header with size no ext",
			data:     []byte{0x1A}, // 0 0011 0 1 0 = type=3, no ext, has size
			wantType: OBUFrameHeader, wantExt: false, wantSize: true,
			wantBytes: 1,
		},
		{
			name:     "tile group with size",
			data:     []byte{0x22}, // 0 0100 0 1 0 = type=4, no ext, has size
			wantType: OBUTileGroup, wantExt: false, wantSize: true,
			wantBytes: 1,
		},
		{
			name:     "padding",
			data:     []byte{0x7A}, // 0 1111 0 1 0 = type=15, no ext, has size
			wantType: OBUPadding, wantExt: false, wantSize: true,
			wantBytes: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hdr, n, err := parseOBUHeader(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if hdr.Type != tt.wantType {
				t.Errorf("type = %d, want %d", hdr.Type, tt.wantType)
			}
			if hdr.HasExtension != tt.wantExt {
				t.Errorf("extension = %v, want %v", hdr.HasExtension, tt.wantExt)
			}
			if hdr.HasSizeField != tt.wantSize {
				t.Errorf("has_size = %v, want %v", hdr.HasSizeField, tt.wantSize)
			}
			if hdr.HasExtension {
				if hdr.TemporalID != tt.wantTemporal {
					t.Errorf("temporal_id = %d, want %d", hdr.TemporalID, tt.wantTemporal)
				}
				if hdr.SpatialID != tt.wantSpatial {
					t.Errorf("spatial_id = %d, want %d", hdr.SpatialID, tt.wantSpatial)
				}
			}
			if n != tt.wantBytes {
				t.Errorf("bytes consumed = %d, want %d", n, tt.wantBytes)
			}
		})
	}
}

func TestParseOBUHeaderForbiddenBit(t *testing.T) {
	_, _, err := parseOBUHeader([]byte{0x80}) // forbidden bit set
	if err == nil {
		t.Error("expected error for forbidden bit")
	}
}

func TestParseOBUs(t *testing.T) {
	// Construct a small OBU stream:
	// OBU 1: temporal delimiter (type=2), has size, size=0
	// OBU 2: sequence header (type=1), has size, size=3, payload=0xAA 0xBB 0xCC
	data := []byte{
		0x12, 0x00, // TD: header(type=2,has_size), leb128(0)
		0x0A, 0x03, 0xAA, 0xBB, 0xCC, // SH: header(type=1,has_size), leb128(3), 3 bytes
	}

	obus, err := ParseOBUs(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(obus) != 2 {
		t.Fatalf("got %d OBUs, want 2", len(obus))
	}

	if obus[0].Header.Type != OBUTemporalDelimiter {
		t.Errorf("OBU[0] type = %d, want %d", obus[0].Header.Type, OBUTemporalDelimiter)
	}
	if len(obus[0].Data) != 0 {
		t.Errorf("OBU[0] data len = %d, want 0", len(obus[0].Data))
	}

	if obus[1].Header.Type != OBUSequenceHeader {
		t.Errorf("OBU[1] type = %d, want %d", obus[1].Header.Type, OBUSequenceHeader)
	}
	if len(obus[1].Data) != 3 {
		t.Errorf("OBU[1] data len = %d, want 3", len(obus[1].Data))
	}
	if obus[1].Data[0] != 0xAA || obus[1].Data[1] != 0xBB || obus[1].Data[2] != 0xCC {
		t.Errorf("OBU[1] data = %X, want AABBCC", obus[1].Data)
	}
}

func TestParseOBUsEmpty(t *testing.T) {
	obus, err := ParseOBUs([]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if len(obus) != 0 {
		t.Errorf("got %d OBUs, want 0", len(obus))
	}
}

func TestParseOBUsTruncated(t *testing.T) {
	// Size says 10 bytes but only 2 available.
	data := []byte{0x0A, 0x0A, 0x00, 0x00}
	_, err := ParseOBUs(data)
	if err == nil {
		t.Error("expected error for truncated OBU data")
	}
}

func TestOBUTypeName(t *testing.T) {
	if OBUTypeName(OBUSequenceHeader) != "SEQUENCE_HEADER" {
		t.Errorf("OBUTypeName(1) = %s", OBUTypeName(OBUSequenceHeader))
	}
	if OBUTypeName(99) != "UNKNOWN(99)" {
		t.Errorf("OBUTypeName(99) = %s", OBUTypeName(99))
	}
}

package h264

import "testing"

func TestReadBits(t *testing.T) {
	// 0xAB = 10101011, 0xCD = 11001101
	br := NewBitReader([]byte{0xAB, 0xCD})

	v, err := br.ReadBits(4)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0xA {
		t.Fatalf("expected 0xA, got 0x%X", v)
	}

	v, err = br.ReadBits(8)
	if err != nil {
		t.Fatal(err)
	}
	// Next 8 bits: 1011_1100 = 0xBC
	if v != 0xBC {
		t.Fatalf("expected 0xBC, got 0x%X", v)
	}
}

func TestReadBool(t *testing.T) {
	br := NewBitReader([]byte{0x80}) // 10000000
	v, err := br.ReadBool()
	if err != nil {
		t.Fatal(err)
	}
	if !v {
		t.Fatal("expected true")
	}
	v, err = br.ReadBool()
	if err != nil {
		t.Fatal(err)
	}
	if v {
		t.Fatal("expected false")
	}
}

func TestReadUE(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want uint32
	}{
		{"ue(0)", []byte{0x80}, 0},       // 1 -> 0
		{"ue(1)", []byte{0x40}, 1},       // 010 -> 1
		{"ue(2)", []byte{0x60}, 2},       // 011 -> 2
		{"ue(3)", []byte{0x20}, 3},       // 00100 -> 3
		{"ue(4)", []byte{0x28}, 4},       // 00101 -> 4
		{"ue(5)", []byte{0x30}, 5},       // 00110 -> 5
		{"ue(6)", []byte{0x38}, 6},       // 00111 -> 6
		{"ue(7)", []byte{0x10}, 7},       // 0001000 -> 7
		{"ue(8)", []byte{0x12}, 8},       // 0001001 -> 8 (0x12 = 00010010)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := NewBitReader(tt.data)
			got, err := br.ReadUE()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("expected %d, got %d", tt.want, got)
			}
		})
	}
}

func TestReadSE(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want int32
	}{
		{"se(0)", []byte{0x80}, 0},   // ue(0) -> 0
		{"se(1)", []byte{0x40}, 1},   // ue(1) -> 1
		{"se(-1)", []byte{0x60}, -1}, // ue(2) -> -1
		{"se(2)", []byte{0x20}, 2},   // ue(3) -> 2
		{"se(-2)", []byte{0x28}, -2}, // ue(4) -> -2
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := NewBitReader(tt.data)
			got, err := br.ReadSE()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("expected %d, got %d", tt.want, got)
			}
		})
	}
}

func TestByteAlign(t *testing.T) {
	br := NewBitReader([]byte{0xFF, 0x00})
	_, _ = br.ReadBits(3)
	br.ByteAlign()
	v, err := br.ReadBits(8)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0x00 {
		t.Fatalf("expected 0x00 after align, got 0x%X", v)
	}
}

func TestMoreRBSPData(t *testing.T) {
	// Data: 0xA8 = 10101000 — last 1-bit is at position 4 (the stop bit).
	// After reading 4 bits (1010), remaining is 1000.
	// The stop bit is at bit 4, so MoreRBSPData should return false.
	br := NewBitReader([]byte{0xA8})
	_, _ = br.ReadBits(4) // consumed: 1010, remaining: 1000
	if br.MoreRBSPData() {
		t.Fatal("expected no more RBSP data")
	}

	// Data: 0xAC = 10101100 — after reading 4 bits (1010), remaining is 1100.
	// Last 1-bit at position 5, more data before stop bit.
	br = NewBitReader([]byte{0xAC})
	_, _ = br.ReadBits(4)
	if !br.MoreRBSPData() {
		t.Fatal("expected more RBSP data")
	}
}

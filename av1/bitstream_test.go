package av1

import (
	"testing"
)

func TestReadBits(t *testing.T) {
	// 0xA5 = 10100101, 0x3C = 00111100
	data := []byte{0xA5, 0x3C}
	br := NewBitReader(data)

	// Read 4 bits: 1010 = 10
	v, err := br.ReadBits(4)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0xA {
		t.Errorf("ReadBits(4) = %d, want %d", v, 0xA)
	}

	// Read 8 bits: 0101 0011 = 83
	v, err = br.ReadBits(8)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0x53 {
		t.Errorf("ReadBits(8) = 0x%X, want 0x53", v)
	}

	// Read 4 bits: 1100 = 12
	v, err = br.ReadBits(4)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0xC {
		t.Errorf("ReadBits(4) = %d, want %d", v, 0xC)
	}

	// Should be at end, reading 1 more bit should fail.
	_, err = br.ReadBits(1)
	if err == nil {
		t.Error("expected error reading past end of data")
	}
}

func TestReadBitsZero(t *testing.T) {
	br := NewBitReader([]byte{0xFF})
	v, err := br.ReadBits(0)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0 {
		t.Errorf("ReadBits(0) = %d, want 0", v)
	}
}

func TestReadBool(t *testing.T) {
	// 0b10 = first bit 1, second bit 0
	br := NewBitReader([]byte{0x80})

	b, err := br.ReadBool()
	if err != nil {
		t.Fatal(err)
	}
	if !b {
		t.Error("ReadBool() = false, want true")
	}

	b, err = br.ReadBool()
	if err != nil {
		t.Fatal(err)
	}
	if b {
		t.Error("ReadBool() = true, want false")
	}
}

func TestReadUVLC(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want uint32
	}{
		{"zero", []byte{0x80}, 0},           // 1 -> 0
		{"one", []byte{0x40}, 1},            // 01 0 -> 1
		{"two", []byte{0x60}, 2},            // 01 1 -> 2
		{"three", []byte{0x20}, 3},          // 001 00 -> 3
		{"six", []byte{0x38}, 6},            // 001 11 -> 6
		{"seven", []byte{0x10, 0x00}, 7},    // 0001 000 -> 7
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := NewBitReader(tt.data)
			got, err := br.ReadUVLC()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ReadUVLC() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestReadSU(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		n    uint
		want int32
	}{
		{"positive 3-bit", []byte{0x60}, 3, 3},    // 011 = 3
		{"negative 3-bit", []byte{0xC0}, 3, -2},   // 110 = 6, 6-8 = -2
		{"zero 4-bit", []byte{0x00}, 4, 0},         // 0000 = 0
		{"max positive 4-bit", []byte{0x70}, 4, 7}, // 0111 = 7
		{"min negative 4-bit", []byte{0x80}, 4, -8}, // 1000 = 8, 8-16 = -8
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := NewBitReader(tt.data)
			got, err := br.ReadSU(tt.n)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ReadSU(%d) = %d, want %d", tt.n, got, tt.want)
			}
		})
	}
}

func TestReadNS(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		n    uint32
		want uint32
	}{
		{"n=1", []byte{}, 1, 0},            // n=1 always returns 0
		{"n=5 val=0", []byte{0x00}, 5, 0},  // w=3, m=3: read 2 bits=00 < 3, return 0
		{"n=5 val=2", []byte{0x80}, 5, 2},  // w=3, m=3: read 2 bits=10 < 3, return 2
		{"n=5 val=3", []byte{0xC0}, 5, 3},  // w=3, m=3: read 2 bits=11 >= 3: read 1 more=0, (11<<1)-3+0=3
		{"n=5 val=4", []byte{0xE0}, 5, 4},  // w=3, m=3: read 2 bits=11 >= 3: read 1 more=1, (11<<1)-3+1=4
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			br := NewBitReader(tt.data)
			got, err := br.ReadNS(tt.n)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ReadNS(%d) = %d, want %d", tt.n, got, tt.want)
			}
		})
	}
}

func TestReadDeltaQ(t *testing.T) {
	// delta_coded = 0: return 0
	br := NewBitReader([]byte{0x00})
	got, err := br.ReadDeltaQ()
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("ReadDeltaQ() = %d, want 0", got)
	}

	// delta_coded = 1, then su(7) = 3:
	// bits: 1 0000011 = 0x83
	br = NewBitReader([]byte{0x83})
	got, err = br.ReadDeltaQ()
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Errorf("ReadDeltaQ() = %d, want 3", got)
	}
}

func TestParseLEB128(t *testing.T) {
	tests := []struct {
		name      string
		data      []byte
		wantValue uint32
		wantBytes int
	}{
		{"zero", []byte{0x00}, 0, 1},
		{"one", []byte{0x01}, 1, 1},
		{"127", []byte{0x7F}, 127, 1},
		{"128", []byte{0x80, 0x01}, 128, 2},
		{"300", []byte{0xAC, 0x02}, 300, 2},
		{"16384", []byte{0x80, 0x80, 0x01}, 16384, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, n, err := ParseLEB128(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if val != tt.wantValue {
				t.Errorf("ParseLEB128 value = %d, want %d", val, tt.wantValue)
			}
			if n != tt.wantBytes {
				t.Errorf("ParseLEB128 bytes = %d, want %d", n, tt.wantBytes)
			}
		})
	}
}

func TestFloorLog2(t *testing.T) {
	tests := []struct {
		n    uint32
		want uint
	}{
		{0, 0}, {1, 0}, {2, 1}, {3, 1}, {4, 2}, {7, 2}, {8, 3}, {255, 7}, {256, 8},
	}
	for _, tt := range tests {
		got := FloorLog2(tt.n)
		if got != tt.want {
			t.Errorf("FloorLog2(%d) = %d, want %d", tt.n, got, tt.want)
		}
	}
}

func TestCeilLog2(t *testing.T) {
	tests := []struct {
		n    uint32
		want uint
	}{
		{0, 0}, {1, 0}, {2, 1}, {3, 2}, {4, 2}, {5, 3}, {8, 3}, {9, 4},
	}
	for _, tt := range tests {
		got := CeilLog2(tt.n)
		if got != tt.want {
			t.Errorf("CeilLog2(%d) = %d, want %d", tt.n, got, tt.want)
		}
	}
}

func TestByteAlign(t *testing.T) {
	br := NewBitReader([]byte{0xFF, 0x00})
	br.ReadBits(3)
	if br.BitsRead() != 3 {
		t.Errorf("BitsRead = %d, want 3", br.BitsRead())
	}
	br.ByteAlign()
	if br.BitsRead() != 8 {
		t.Errorf("after ByteAlign: BitsRead = %d, want 8", br.BitsRead())
	}

	// Already aligned — should not advance.
	br.ByteAlign()
	if br.BitsRead() != 8 {
		t.Errorf("double ByteAlign: BitsRead = %d, want 8", br.BitsRead())
	}
}

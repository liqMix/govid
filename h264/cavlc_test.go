package h264

import "testing"

func TestReadCoeffTokenTC0(t *testing.T) {
	// Table 0, TC=0, TO=0: code "1" (1 bit).
	br := NewBitReader([]byte{0x80}) // 10000000
	tc, to, err := readCoeffToken(br, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tc != 0 || to != 0 {
		t.Errorf("got tc=%d to=%d, want 0,0", tc, to)
	}
}

func TestReadCoeffTokenTC1TO1(t *testing.T) {
	// Table 0, TC=1, TO=1: code "01" (2 bits).
	br := NewBitReader([]byte{0x40}) // 01000000
	tc, to, err := readCoeffToken(br, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tc != 1 || to != 1 {
		t.Errorf("got tc=%d to=%d, want 1,1", tc, to)
	}
}

func TestReadCoeffTokenTC2TO2(t *testing.T) {
	// Table 0, TC=2, TO=2: code "001" (3 bits).
	br := NewBitReader([]byte{0x20}) // 00100000
	tc, to, err := readCoeffToken(br, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tc != 2 || to != 2 {
		t.Errorf("got tc=%d to=%d, want 2,2", tc, to)
	}
}

func TestReadCoeffTokenChromaDC(t *testing.T) {
	// Chroma DC table, TC=0, TO=0: code "01" (2 bits).
	br := NewBitReader([]byte{0x40})
	tc, to, err := readCoeffToken(br, -1)
	if err != nil {
		t.Fatal(err)
	}
	if tc != 0 || to != 0 {
		t.Errorf("got tc=%d to=%d, want 0,0", tc, to)
	}
}

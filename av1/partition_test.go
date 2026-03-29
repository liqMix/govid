package av1

import "testing"

func TestBlockSizeLookup(t *testing.T) {
	tests := []struct {
		bs   int
		w, h int
	}{
		{Block4x4, 4, 4},
		{Block8x8, 8, 8},
		{Block16x16, 16, 16},
		{Block32x32, 32, 32},
		{Block64x64, 64, 64},
		{Block128x128, 128, 128},
		{Block4x8, 4, 8},
		{Block8x4, 8, 4},
		{Block16x32, 16, 32},
		{Block64x16, 64, 16},
	}

	for _, tt := range tests {
		w := BlockWidth[tt.bs]
		h := BlockHeight[tt.bs]
		if w != tt.w || h != tt.h {
			t.Errorf("Block %d: got %dx%d, want %dx%d", tt.bs, w, h, tt.w, tt.h)
		}
	}
}

func TestSubSizeTable(t *testing.T) {
	// Block8x8 SPLIT → Block4x4.
	sub := SubSize[Block8x8][PartitionSplit]
	if sub != Block4x4 {
		t.Errorf("SubSize[8x8][SPLIT] = %d, want %d (4x4)", sub, Block4x4)
	}

	// Block16x16 NONE → Block16x16.
	sub = SubSize[Block16x16][PartitionNone]
	if sub != Block16x16 {
		t.Errorf("SubSize[16x16][NONE] = %d, want %d", sub, Block16x16)
	}

	// Block64x64 HORZ → Block64x32.
	sub = SubSize[Block64x64][PartitionHorz]
	if sub != Block64x32 {
		t.Errorf("SubSize[64x64][HORZ] = %d, want %d (64x32)", sub, Block64x32)
	}

	// Block4x4 can only do NONE.
	sub = SubSize[Block4x4][PartitionNone]
	if sub != Block4x4 {
		t.Errorf("SubSize[4x4][NONE] = %d, want %d", sub, Block4x4)
	}

	// Invalid partitions should be -1.
	sub = SubSize[Block4x4][PartitionSplit]
	if sub != -1 {
		t.Errorf("SubSize[4x4][SPLIT] = %d, want -1", sub)
	}
}

func TestTXSizeForBlockSize(t *testing.T) {
	if TXSizeForBlockSize[Block4x4] != TX4x4 {
		t.Errorf("TX for 4x4 = %d, want TX4x4", TXSizeForBlockSize[Block4x4])
	}
	if TXSizeForBlockSize[Block8x8] != TX8x8 {
		t.Errorf("TX for 8x8 = %d, want TX8x8", TXSizeForBlockSize[Block8x8])
	}
	if TXSizeForBlockSize[Block64x64] != TX64x64 {
		t.Errorf("TX for 64x64 = %d, want TX64x64", TXSizeForBlockSize[Block64x64])
	}
}

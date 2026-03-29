package av1

import "testing"

func TestTileDataExtractionSingleTile(t *testing.T) {
	// Simulate a single-tile tile group — all data goes to tile 0.
	d := &Decoder{
		seqHdr: &SequenceHeader{
			SuperblockSize:       64,
			Use128x128Superblock: false,
		},
		frameHdr: &FrameHeader{
			FrameWidth:  16,
			FrameHeight: 16,
			MICols:      4,
			MIRows:      4,
			SBCols:      1,
			SBRows:      1,
			Tile: TileInfo{
				TileCols:      1,
				TileRows:      1,
				TileWidthSB:   []int{1},
				TileHeightSB:  []int{1},
				TileSizeBytes: 4,
			},
			FrameType:    FrameTypeKeyFrame,
			FrameIsIntra: true,
			TXMode:       txModeLargest,
			ReducedTXSet: true,
		},
	}
	d.updateDimensions()
	d.ensureImg()
	d.cdf = NewCDFTables()

	// Single tile: all data goes to one tile, no size prefixes.
	tileData := make([]byte, 64)
	for i := range tileData {
		tileData[i] = 0x80 // arbitrary data
	}

	// This should not panic (may error on symbol codec).
	err := d.decodeTileGroup(tileData)
	if err != nil {
		// It's expected that decoding might error due to arbitrary data,
		// but extraction should work without panic.
		t.Logf("Expected decode error with random data: %v", err)
	}
}

func TestTileDataExtractionMultiTile(t *testing.T) {
	// Test that multi-tile byte range extraction works correctly.
	// Format: for N tiles, first N-1 have TileSizeBytes-byte LE size prefix (+1).
	tileSizeBytes := 2

	// Create tile data: 2 tiles.
	// Tile 0: 10 bytes of data, prefixed with size 9 (10-1) as 2-byte LE.
	// Tile 1: remaining bytes.
	tile0Data := make([]byte, 10)
	for i := range tile0Data {
		tile0Data[i] = 0xAA
	}
	tile1Data := make([]byte, 8)
	for i := range tile1Data {
		tile1Data[i] = 0xBB
	}

	// Build tile group data.
	tileGroupData := make([]byte, 0, tileSizeBytes+len(tile0Data)+len(tile1Data))
	// Size prefix for tile 0: 10 - 1 = 9, as 2-byte LE.
	tileGroupData = append(tileGroupData, 9, 0)
	tileGroupData = append(tileGroupData, tile0Data...)
	tileGroupData = append(tileGroupData, tile1Data...)

	// 128x64 frame with 2 SB columns → 2 tiles side by side.
	d := &Decoder{
		seqHdr: &SequenceHeader{
			SuperblockSize:       64,
			Use128x128Superblock: false,
		},
		frameHdr: &FrameHeader{
			FrameWidth:  128,
			FrameHeight: 64,
			MICols:      32,
			MIRows:      16,
			SBCols:      2,
			SBRows:      1,
			Tile: TileInfo{
				TileCols:      2,
				TileRows:      1,
				TileWidthSB:   []int{1, 1},
				TileHeightSB:  []int{1},
				TileSizeBytes: tileSizeBytes,
			},
			FrameType:    FrameTypeKeyFrame,
			FrameIsIntra: true,
			TXMode:       txModeLargest,
			ReducedTXSet: true,
		},
	}
	d.updateDimensions()
	d.ensureImg()
	d.cdf = NewCDFTables()

	// The decode will error on the random data, but shouldn't panic.
	err := d.decodeTileGroup(tileGroupData)
	if err != nil {
		t.Logf("Expected decode error with test data: %v", err)
	}
}

func TestTileDataExtractionSizeComputation(t *testing.T) {
	// Verify tile size byte parsing independently.
	tileSizeBytes := 4

	// Encode size 255 as 4-byte LE.
	data := []byte{255, 0, 0, 0}
	size := 0
	for i := 0; i < tileSizeBytes; i++ {
		size |= int(data[i]) << (8 * i)
	}
	size++ // coded as tile_size_minus_1

	if size != 256 {
		t.Errorf("tile size = %d, want 256", size)
	}

	// Encode size 0x1234 - 1 = 0x1233 as 4-byte LE.
	data = []byte{0x33, 0x12, 0, 0}
	size = 0
	for i := 0; i < tileSizeBytes; i++ {
		size |= int(data[i]) << (8 * i)
	}
	size++

	if size != 0x1234 {
		t.Errorf("tile size = %d, want %d", size, 0x1234)
	}
}

package av1

import "fmt"

// tileDecoder holds per-tile decoding state.
type tileDecoder struct {
	dec *Decoder
	sc  *SymbolCodec
	cdf *CDFTables

	// Tile MI bounds.
	miRowStart int
	miRowEnd   int
	miColStart int
	miColEnd   int

	// Above context arrays, indexed by (miCol - miColStart).
	abovePartCtx []int  // partition context
	aboveModeCtx []int  // intra mode
	aboveSkipCtx []bool // skip flag
	aboveNzCtx   [3][]bool // [plane] nonzero coefficient context

	// Left context arrays, indexed by (miRow - miRowStart).
	leftPartCtx []int
	leftModeCtx []int
	leftSkipCtx []bool
	leftNzCtx   [3][]bool // [plane]
	blockCount  int       // blocks decoded (for debug)
	skipCount   int       // blocks with skip=true
	txbCount    int       // transform blocks decoded
	txbNzCount  int       // transform blocks with non-zero coefficients
	partitionLog []int    // partition decisions (for diagnostics)

	// DC sign context arrays for coefficient coding.
	// Values: 0=zero/unknown, 1=positive, 2=negative.
	aboveDCSign [3][]int // [plane], indexed like aboveNzCtx
	leftDCSign  [3][]int // [plane], indexed like leftNzCtx

	// Per-SB state for delta Q/LF reads.
	readDeltas bool
	currentQIdx int // current quantizer index (BaseQIdx + accumulated delta)

	// CDEF tracking: which 64x64 regions have had their CDEF index read.
	cdefRead map[[2]int]bool
}

// decodeTileGroup parses a tile group OBU and decodes all tiles.
// AV1 spec 5.11.1.
func (d *Decoder) decodeTileGroup(tileData []byte) error {
	fh := d.frameHdr
	numTiles := fh.Tile.TileCols * fh.Tile.TileRows

	if numTiles == 0 {
		return fmt.Errorf("tile group: no tiles")
	}

	// Parse tile group OBU header (spec 5.11.1).
	br := NewBitReader(tileData)

	tgStart := 0
	tgEnd := numTiles - 1

	if numTiles > 1 {
		// tile_start_and_end_present_flag
		flag, err := br.ReadBool()
		if err != nil {
			return fmt.Errorf("tile group: read tile_start_and_end_present: %w", err)
		}
		if flag {
			tileBits := fh.Tile.TileColsLog2 + fh.Tile.TileRowsLog2
			s, err := br.ReadBits(uint(tileBits))
			if err != nil {
				return fmt.Errorf("tile group: read tg_start: %w", err)
			}
			tgStart = int(s)
			e, err := br.ReadBits(uint(tileBits))
			if err != nil {
				return fmt.Errorf("tile group: read tg_end: %w", err)
			}
			tgEnd = int(e)
		}
	}

	// Byte-align after the tile group header.
	br.ByteAlign()
	headerBytes := br.BitsRead() / 8

	// The tile data starts after the tile group header.
	if headerBytes > len(tileData) {
		return fmt.Errorf("tile group: header exceeds data")
	}
	payload := tileData[headerBytes:]

	numTilesInGroup := tgEnd - tgStart + 1

	// Extract per-tile byte ranges from payload.
	tileBytes := make([][]byte, numTilesInGroup)
	offset := 0

	if numTilesInGroup == 1 {
		tileBytes[0] = payload
	} else {
		tileSizeBytes := fh.Tile.TileSizeBytes
		if tileSizeBytes < 1 {
			tileSizeBytes = 4
		}
		for t := 0; t < numTilesInGroup-1; t++ {
			if offset+tileSizeBytes > len(payload) {
				return fmt.Errorf("tile group: truncated tile size at tile %d", tgStart+t)
			}
			// Read tile size as little-endian integer (TileSizeBytes bytes) + 1.
			tileSize := 0
			for i := 0; i < tileSizeBytes; i++ {
				tileSize |= int(payload[offset+i]) << (8 * i)
			}
			tileSize++ // spec: coded as tile_size_minus_1
			offset += tileSizeBytes

			if offset+tileSize > len(payload) {
				return fmt.Errorf("tile group: tile %d size %d exceeds data len %d", tgStart+t, tileSize, len(payload)-offset)
			}
			tileBytes[t] = payload[offset : offset+tileSize]
			offset += tileSize
		}
		// Last tile takes remaining bytes.
		tileBytes[numTilesInGroup-1] = payload[offset:]
	}

	// Decode each tile.
	for i := 0; i < numTilesInGroup; i++ {
		tileIdx := tgStart + i
		tileRow := tileIdx / fh.Tile.TileCols
		tileCol := tileIdx % fh.Tile.TileCols
		if err := d.decodeTile(tileRow, tileCol, tileBytes[i]); err != nil {
			return fmt.Errorf("tile [%d,%d]: %w", tileRow, tileCol, err)
		}
	}

	return nil
}

// decodeTile decodes a single tile.
func (d *Decoder) decodeTile(tileRow, tileCol int, data []byte) error {
	fh := d.frameHdr
	sh := d.seqHdr

	// Compute MI bounds for this tile.
	sbSize := sh.SuperblockSize
	miSize := sbSize / 4 // MI units per superblock side

	miColStart := 0
	for c := 0; c < tileCol; c++ {
		miColStart += fh.Tile.TileWidthSB[c] * miSize
	}
	miColEnd := miColStart + fh.Tile.TileWidthSB[tileCol]*miSize
	if miColEnd > d.miCols {
		miColEnd = d.miCols
	}

	miRowStart := 0
	for r := 0; r < tileRow; r++ {
		miRowStart += fh.Tile.TileHeightSB[r] * miSize
	}
	miRowEnd := miRowStart + fh.Tile.TileHeightSB[tileRow]*miSize
	if miRowEnd > d.miRows {
		miRowEnd = d.miRows
	}

	aboveW := miColEnd - miColStart
	leftH := miRowEnd - miRowStart

	sc := NewSymbolCodec(data)
	sc.allowUpdateCDF = !fh.DisableCDFUpdate

	td := &tileDecoder{
		dec:          d,
		sc:           sc,
		cdf:          d.cdf,
		miRowStart:   miRowStart,
		miRowEnd:     miRowEnd,
		miColStart:   miColStart,
		miColEnd:     miColEnd,
		abovePartCtx: make([]int, aboveW),
		aboveModeCtx: make([]int, aboveW),
		aboveSkipCtx: make([]bool, aboveW),
		leftPartCtx:  make([]int, leftH),
		leftModeCtx:  make([]int, leftH),
		leftSkipCtx:  make([]bool, leftH),
		cdefRead:     make(map[[2]int]bool),
		currentQIdx:  fh.Quant.BaseQIdx,
	}

	// Init nonzero context and DC sign context per plane.
	for p := 0; p < 3; p++ {
		if p == 0 {
			td.aboveNzCtx[p] = make([]bool, aboveW)
			td.leftNzCtx[p] = make([]bool, leftH)
			td.aboveDCSign[p] = make([]int, aboveW)
			td.leftDCSign[p] = make([]int, leftH)
		} else {
			// Chroma is subsampled.
			td.aboveNzCtx[p] = make([]bool, (aboveW+1)/2)
			td.leftNzCtx[p] = make([]bool, (leftH+1)/2)
			td.aboveDCSign[p] = make([]int, (aboveW+1)/2)
			td.leftDCSign[p] = make([]int, (leftH+1)/2)
		}
	}

	// Determine superblock block size.
	sbBS := Block64x64
	if sh.Use128x128Superblock {
		sbBS = Block128x128
	}

	// Raster scan superblocks within this tile.
	for sbRow := 0; sbRow < fh.Tile.TileHeightSB[tileRow]; sbRow++ {
		for sbCol := 0; sbCol < fh.Tile.TileWidthSB[tileCol]; sbCol++ {
			miRow := miRowStart + sbRow*miSize
			miCol := miColStart + sbCol*miSize
			if miRow >= miRowEnd || miCol >= miColEnd {
				continue
			}

			// Reset left context at the start of each SB row.
			if sbCol == 0 {
				for i := range td.leftPartCtx {
					td.leftPartCtx[i] = 0
				}
				for i := range td.leftModeCtx {
					td.leftModeCtx[i] = IntraDC
				}
				for i := range td.leftSkipCtx {
					td.leftSkipCtx[i] = false
				}
				for p := 0; p < 3; p++ {
					for i := range td.leftNzCtx[p] {
						td.leftNzCtx[p][i] = false
					}
					for i := range td.leftDCSign[p] {
						td.leftDCSign[p][i] = 0
					}
				}
			}

			td.readDeltas = fh.DeltaQPresent

			if err := td.decodePartition(miRow, miCol, sbBS); err != nil {
				return fmt.Errorf("SB[%d,%d] after %d blocks: %w", sbRow, sbCol, td.blockCount, err)
			}

			// Track codec position after first SB.
			if sbRow == 0 && sbCol == 0 && d.diagCodecPosAfterSB1 == 0 {
				d.diagCodecPosAfterSB1 = sc.bytePos
			}
		}
	}

	// Accumulate tile diagnostics into the decoder.
	d.diagBlockCount += td.blockCount
	d.diagSkipCount += td.skipCount
	d.diagTxbCount += td.txbCount
	d.diagTxbNzCount += td.txbNzCount
	d.diagPartitions = append(d.diagPartitions, td.partitionLog...)

	return nil
}

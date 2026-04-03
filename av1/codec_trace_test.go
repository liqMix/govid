package av1

import "testing"

func TestTraceFirstSB(t *testing.T) {
	sample := extractFirstSample(t, "../examples/videos/baker_av1.mp4")
	obus, _ := ParseOBUs(sample)
	var sh *SequenceHeader
	var fh *FrameHeader
	var tileData []byte
	for _, obu := range obus {
		switch obu.Header.Type {
		case OBUSequenceHeader:
			sh, _ = ParseSequenceHeader(obu.Data)
		case OBUFrame:
			var hBits int
			fh, hBits, _ = ParseFrameHeader(obu.Data, sh)
			hBytes := (hBits + 7) / 8
			if hBytes < len(obu.Data) { tileData = obu.Data[hBytes:] }
		}
	}
	if tileData == nil || fh == nil { t.Fatal("no tile data") }
	br := NewBitReader(tileData)
	if fh.Tile.TileCols*fh.Tile.TileRows > 1 { br.ReadBool() }
	br.ByteAlign()
	payload := tileData[br.BitsRead()/8:]

	sc := NewSymbolCodec(payload)
	cdf := NewCDFTables(fh.Quant.BaseQIdx)
	sc.allowUpdateCDF = !fh.DisableCDFUpdate

	state := func(label string) { t.Logf("  %s: c=%d rng=%d pos=%d", label, uint32(sc.dif>>48), sc.rng, sc.bytePos) }

	state("init")
	// 1. Partition
	sym, _ := sc.ReadSymbol(cdf.Partition[16], 10)
	t.Logf("Partition=%d", sym); state("post-partition")
	// 2. Skip
	sym, _ = sc.ReadSymbol(cdf.Skip[0], 2)
	t.Logf("Skip=%d", sym); state("post-skip")
	// 3. CDEF
	v, _ := sc.ReadLiteral(int(fh.CDEF.Bits))
	t.Logf("CDEF=%d", v); state("post-cdef")
	// 4. DeltaQ
	sym, _ = sc.ReadSymbol(cdf.DeltaQ, 4)
	t.Logf("DeltaQ_base=%d", sym)
	dqAbs := sym
	if sym >= 3 {
		n, _ := sc.ReadLiteral(3)
		ex, _ := sc.ReadLiteral(int(n) + 1)
		dqAbs = (1 << (int(n) + 1)) - 1 + int(ex)
	}
	t.Logf("DeltaQ_abs=%d", dqAbs)
	if dqAbs > 0 { sc.ReadBoolEqui(); t.Log("DeltaQ sign read") }
	state("post-deltaq")
	// 5. Y mode
	sym, _ = sc.ReadSymbol(cdf.KFIntraMode[0][0], 13)
	t.Logf("YMode=%d", sym); state("post-ymode")
	// 6. Angle delta (if directional)
	if sym >= IntraVertical && sym <= IntraD67 {
		deltaIdx := sym - IntraVertical
		if deltaIdx >= 8 { deltaIdx = 7 }
		ad, _ := sc.ReadSymbol(cdf.AngleDelta[deltaIdx], 7)
		t.Logf("AngleDelta=%d (raw sym)", ad); state("post-angle")
	}
	// 7. UV mode
	uvSym, _ := sc.ReadSymbol(cdf.IntraModeUVCfL[sym], 14)
	t.Logf("UVMode=%d", uvSym); state("post-uvmode")
	// 8. Palette Y (if DC and AllowScreenContentTools)
	if sym == IntraDC && fh.AllowScreenContentTools {
		palCtx := bsizeToPaletteCtx(Block64x64)
		ps, _ := sc.ReadSymbol(cdf.PaletteY[palCtx][0], 2)
		t.Logf("PaletteY=%d", ps); state("post-palette-y")
	}
	// 9. Filter intra (if DC, no palette, block<=32)
	// Block64x64 max dim=64>32 so skipped
	// 10. TX size
	maxTx := TXSizeForBlockSize[Block64x64] // TX64x64=4
	maxDepth := maxTx; if maxDepth > 2 { maxDepth = 2 }
	txSize := maxTx
	for d := 0; d < maxDepth; d++ {
		split, _ := sc.ReadSymbol(cdf.TXSize[maxTx][d], 2)
		t.Logf("TXSplit[%d]=%d", d, split)
		if split == 0 { break }
		txSize--
	}
	t.Logf("TXSize=%d", txSize); state("post-txsize")
	// 11. TX type
	if txSize < TX32x32 && !fh.ReducedTXSet {
		txSqrCtx := 0; if txSize > TX4x4 { txSqrCtx = 1 }
		modeCtx := sym; if modeCtx >= NumIntraModes { modeCtx = 0 }
		ttSym, _ := sc.ReadSymbol(cdf.IntraTXType2[txSqrCtx][modeCtx], 7)
		t.Logf("TXType raw sym=%d", ttSym); state("post-txtype")
	}
	t.Logf("=== Total bytes consumed: %d", sc.bytePos)
}

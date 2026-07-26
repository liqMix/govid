package ebiv

// Intra frame decoder. It mirrors the encoder's traversal exactly, so the
// shared rANS stream stays in step. Tiles carry independent streams and write
// disjoint macroblock regions, so they decode concurrently with no
// synchronization until the frame is assembled (§3.5, §5).

// tileDecoder holds one tile's decode state, including a private block scratch
// so concurrent tiles never share mutable memory. ref, mv, and mvStride are set
// only for inter frames.
type tileDecoder struct {
	dec      *ransDecoder
	rec      *frameBuf
	ref      *frameBuf
	golden   *frameBuf
	q        quantizer
	inter    bool
	mv       []motionVector
	mvStride int
	sc       blockScratch
}

// decodeState owns every buffer the coded decode path reuses across frames, so
// steady-state decode allocates essentially nothing: the per-frame frequency
// tables are rebuilt into preallocated backing, and the tile workers, motion
// grid, and scratch persist. Only the tiles-list slice and goroutine handles
// remain per frame.
type decodeState struct {
	tables []ransTable
	// prevFreqs holds the last shipped frequency vector per context, the
	// reference for tblSame/tblDelta table modes. Key frames wipe it, so a
	// seek that lands on a key frame can never reference pre-seek state.
	prevFreqs [][]uint32
	mv        []motionVector
	tds       []tileDecoder
	tiles     [][]byte
	errs      []error
}

func newDecodeState() *decodeState {
	ds := &decodeState{
		tables:    make([]ransTable, numContexts),
		prevFreqs: make([][]uint32, numContexts),
	}
	for c := range ds.tables {
		ds.tables[c].enc = make([]ransSym, alphabetSizes[c])
		ds.tables[c].slot2sym = make([]uint16, ransM)
	}
	return ds
}

// decodeCoded parses a coded frame body and decodes it into rec. For an inter
// frame, ref is the previous reconstruction and golden the GOP key frame's.
// key selects self-contained table parsing.
func (ds *decodeState) decodeCoded(rec, ref, golden *frameBuf, body []byte, inter, key bool) error {
	hdr, tiles, err := ds.parsePayload(body, key)
	if err != nil {
		return err
	}
	grid := newTileGrid(rec.mbCols, rec.mbRows, hdr.tileCols, hdr.tileRows)
	if grid.count() != len(tiles) {
		return ErrCorrupt
	}
	q := newQuantizer(hdr.qp)

	nMB := rec.mbCols * rec.mbRows
	ds.mv = resetMV(ds.mv, nMB)
	ds.tds = growTDs(ds.tds, len(tiles))
	ds.errs = growErrs(ds.errs, len(tiles))

	// Tiles fan out across the shared worker pool (§5); the single-tile case
	// runs inline on the caller, so a background player's steady state spawns
	// no goroutine and allocates nothing here.
	sharedPool().run(len(tiles), func(i int) {
		td := &ds.tds[i]
		td.rec, td.ref, td.golden, td.q = rec, ref, golden, q
		td.inter, td.mv, td.mvStride = inter, ds.mv, rec.mbCols
		if td.dec == nil {
			td.dec = &ransDecoder{}
		}
		if err := td.dec.reset(tiles[i], ds.tables); err != nil {
			ds.errs[i] = err
			return
		}
		td.decodeTile(grid.bounds(i))
		ds.errs[i] = td.dec.err
	})
	for _, err := range ds.errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func resetMV(mv []motionVector, n int) []motionVector {
	if cap(mv) < n {
		return make([]motionVector, n)
	}
	mv = mv[:n]
	for i := range mv {
		mv[i] = motionVector{}
	}
	return mv
}

func growTDs(tds []tileDecoder, n int) []tileDecoder {
	if cap(tds) < n {
		return make([]tileDecoder, n)
	}
	return tds[:n]
}

func growErrs(errs []error, n int) []error {
	if cap(errs) < n {
		return make([]error, n)
	}
	return errs[:n]
}

func (td *tileDecoder) decodeTile(b tileBounds) {
	for mby := b.mbY0; mby < b.mbY1; mby++ {
		for mbx := b.mbX0; mbx < b.mbX1; mbx++ {
			if td.inter {
				td.decodeInterMB(mbx, mby, b)
			} else {
				td.decodeLumaMB(mbx, mby, b)
				td.decodeChromaMB(mbx, mby, b)
			}
			if td.dec.err != nil {
				return
			}
		}
	}
}

func (td *tileDecoder) decodeLumaMB(mbx, mby int, b tileBounds) {
	txIdx := td.dec.decode(ctxTxSize)
	if txIdx < 0 || txIdx >= len(lumaTxSizes) {
		td.dec.fail(ErrCorrupt)
		return
	}
	n := lumaTxSizes[txIdx]
	tileLeft := b.mbX0 * mbSize
	tileTop := b.mbY0 * mbSize
	tileRight := b.mbX1 * mbSize
	mbTop, mbRight := mby*mbSize, (mbx+1)*mbSize
	for by := 0; by < mbSize; by += n {
		for bx := 0; bx < mbSize; bx += n {
			x0 := mbx*mbSize + bx
			y0 := mby*mbSize + by
			mode := td.dec.decode(ctxLumaMode)
			avAR := aboveRightAvailable(x0, y0, n, tileTop, tileRight, mbTop, mbRight)
			td.reconBlock(planeLuma, txIdx, td.rec.y, x0, y0, n, mode, y0 > tileTop, x0 > tileLeft, avAR)
		}
	}
}

func (td *tileDecoder) decodeChromaMB(mbx, mby int, b tileBounds) {
	tileLeft := b.mbX0 * chromaMB
	tileTop := b.mbY0 * chromaMB
	x0 := mbx * chromaMB
	y0 := mby * chromaMB
	mode := td.dec.decode(ctxChromaMode)
	td.reconBlock(planeChroma, 0, td.rec.cb, x0, y0, chromaMB, mode, y0 > tileTop, x0 > tileLeft, false)
	td.reconBlock(planeChroma, 0, td.rec.cr, x0, y0, chromaMB, mode, y0 > tileTop, x0 > tileLeft, false)
}

// reconBlock decodes one block's residual and reconstructs it in place.
func (td *tileDecoder) reconBlock(plane, txIdx int, rec planeView, x0, y0, n, mode int, avAbove, avLeft, avAR bool) {
	if mode < 0 || mode >= numIntraModes {
		td.dec.fail(ErrCorrupt)
		return
	}
	sc := &td.sc
	for i := 0; i < n*n; i++ {
		sc.levels[i] = 0
	}
	decodeBlock(td.dec, plane, txIdx, sc.levels[:n*n], n)
	if td.dec.err != nil {
		return
	}
	td.q.dequantize(sc.levels[:n*n], sc.coeffs[:n*n], n)
	inverseDCT(sc.coeffs[:n*n], sc.recon[:n*n], n)
	predictIntra(rec, x0, y0, n, mode, avAbove, avLeft, avAR, sc.pred[:])
	reconstruct(rec, x0, y0, n, sc.pred[:], sc.recon[:])
}

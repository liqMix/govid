package ebiv

// This file defines the coding model shared by the encoder and decoder: the
// block grid, the intra/inter mode enumerations, the residual token alphabet,
// and the entropy-context layout. Both sides derive every table from these
// constants, so a change here changes both ends at once.
//
// This is bitstream v2 (see .docs/ebiv-gap-analysis.md M1): compared with v1 it
// adds a skip macroblock mode, a coded-block pattern, class+suffix coding for
// motion vectors and escapes (replacing raw LEB128 bytes), a fixed uniform
// "bypass" context for raw suffix bits, and coefficient-token contexts split by
// transform size with a dedicated DC band.

// Block grid. Luma is coded in 16x16 macroblocks; in 4:2:0 each macroblock
// carries one 8x8 block per chroma plane.
const (
	mbSize    = 16
	chromaMB  = 8
	dcPred    = 128 // neutral prediction when no neighbors are available
	maxLevels = mbSize * mbSize
)

// Luma transform sizes a macroblock may choose between (§3.3). The index into
// this table is what ctxTxSize codes.
var lumaTxSizes = [3]int{4, 8, 16}

// Intra prediction modes (§3.4). DC, vertical, horizontal, TrueMotion (Paeth),
// and six directional angles built on an HEVC-style projected reference — a
// VP9-class set. The directional modes carry their angles in predict.go.
const (
	modeDC = iota
	modeV
	modeH
	modeTM
	modeD45  // up-right 45°
	modeD27  // steep vertical-right
	modeD23  // vertical-left
	modeD135 // down-left 45°
	modeD117 // shallow horizontal-down
	modeD113 // horizontal-up
	numIntraModes
)

// Macroblock coding modes, used only in inter (P) frames.
const (
	mbSkip  = iota // motion-compensated at the predicted MV, no residual
	mbInter        // explicit MV delta plus a coded residual
	mbIntra        // coded like a key-frame macroblock
	numMBModes
)

// Coded-block pattern bits for an mbInter macroblock. A clear bit means the
// component has no residual at all: no transform-size token, no blocks, no
// EOBs — the prediction is the reconstruction.
const (
	cbpLuma = 1 << iota
	cbpCb
	cbpCr
	numCBP = 8
)

// Motion partition shapes for an mbInter macroblock: the whole 16×16, split
// into two 16×8 or 8×16 halves, or four 8×8 quarters — each partition carrying
// its own motion vector. Variable block-size motion is the main lever on the
// non-skip macroblocks that hold most of the inter-frame bits.
const (
	partWhole = iota // one 16×16
	part16x8         // two 16×8
	part8x16         // two 8×16
	part8x8          // four 8×8
	numPartModes
)

// partRects gives each partition's luma rectangle {x, y, w, h} within the
// macroblock. Chroma rectangles are these halved.
var partRects = [numPartModes][][4]int{
	partWhole: {{0, 0, 16, 16}},
	part16x8:  {{0, 0, 16, 8}, {0, 8, 16, 8}},
	part8x16:  {{0, 0, 8, 16}, {8, 0, 8, 16}},
	part8x8:   {{0, 0, 8, 8}, {8, 0, 8, 8}, {0, 8, 8, 8}, {8, 8, 8, 8}},
}

// Residual token alphabet. A coefficient in scan order is one of: an explicit
// zero, a small magnitude 1..4 coded directly, an escape for larger
// magnitudes, or end-of-block. Signs and escape magnitudes are coded in their
// own contexts.
const (
	tZero = iota
	tOne
	tTwo
	tThree
	tFour
	tEscape
	tEOB
	numTokens
)

// Class alphabets for motion-vector deltas and escape magnitudes: a value v is
// coded as class = bits.Len(v) plus class-1 raw suffix bits through the bypass
// context. 16 classes cover values to 32767, far beyond either use.
const (
	numMVClasses  = 16
	numEscClasses = 16
)

// Entropy contexts. Each context owns an independent frequency table, except
// ctxBypass, which is a fixed uniform 2-symbol table on both sides: one raw
// bit costs exactly one coded bit, and it is never counted or shipped.
const (
	ctxTxSize     = iota // luma transform-size choice, alphabet len(lumaTxSizes)
	ctxLumaMode          // intra mode per luma block, alphabet numIntraModes
	ctxChromaMode        // intra mode per macroblock chroma, alphabet numIntraModes
	ctxMBMode            // inter macroblock mode, alphabet numMBModes
	ctxPart              // inter motion partition shape, alphabet numPartModes
	ctxCBP               // coded-block pattern, alphabet numCBP
	ctxSign              // coefficient sign, alphabet 2
	ctxEscClass          // escape magnitude class, alphabet numEscClasses
	ctxMVClassX          // MV delta class, horizontal, alphabet numMVClasses
	ctxMVClassY          // MV delta class, vertical, alphabet numMVClasses
	ctxBypass            // raw suffix bits, fixed uniform, alphabet 2
	ctxTokenBase         // first of the coefficient-token contexts
)

// Coefficient-token context sub-dimensions. Luma contexts are split by
// transform size (statistics differ sharply between 4x4 and 16x16 blocks) and
// every size gets a dedicated DC band plus four AC bands.
const (
	numBands       = 5
	numPrevClass   = 3
	lumaTokenCtx   = 3 * numBands * numPrevClass // one block of contexts per luma tx size
	chromaTokenCtx = numBands * numPrevClass
	numTokenCtx    = lumaTokenCtx + chromaTokenCtx
	numContexts    = ctxTokenBase + numTokenCtx
	planeLuma      = 0
	planeChroma    = 1
)

// tokenCtx maps a coefficient position's (plane, luma tx index, band,
// previous-class) to its context id. Chroma ignores txIdx — it is always 8x8.
func tokenCtx(plane, txIdx, band, prevClass int) int {
	if plane == planeLuma {
		return ctxTokenBase + (txIdx*numBands+band)*numPrevClass + prevClass
	}
	return ctxTokenBase + lumaTokenCtx + band*numPrevClass + prevClass
}

// bandOf groups a scan position into a frequency band: DC alone, then widening
// AC bands. Positions past the table saturate at the top band.
func bandOf(scanPos int) int {
	switch {
	case scanPos == 0:
		return 0
	case scanPos <= 2:
		return 1
	case scanPos <= 8:
		return 2
	case scanPos <= 20:
		return 3
	default:
		return 4
	}
}

// classForMag buckets a just-coded magnitude for the next position's context:
// 0 for zero, 1 for a magnitude of one, 2 for anything larger.
func classForMag(mag int) int {
	switch {
	case mag <= 0:
		return 0
	case mag == 1:
		return 1
	default:
		return 2
	}
}

// alphabetSizes gives each context's symbol count. The decoder needs it to
// parse the per-frame frequency tables; the encoder needs it to size them.
var alphabetSizes = func() [numContexts]int {
	var a [numContexts]int
	a[ctxTxSize] = len(lumaTxSizes)
	a[ctxLumaMode] = numIntraModes
	a[ctxChromaMode] = numIntraModes
	a[ctxMBMode] = numMBModes
	a[ctxPart] = numPartModes
	a[ctxCBP] = numCBP
	a[ctxSign] = 2
	a[ctxEscClass] = numEscClasses
	a[ctxMVClassX] = numMVClasses
	a[ctxMVClassY] = numMVClasses
	a[ctxBypass] = 2
	for c := ctxTokenBase; c < numContexts; c++ {
		a[c] = numTokens
	}
	return a
}()

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func clampByte(v int32) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

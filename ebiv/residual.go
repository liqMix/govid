package ebiv

// Residual coefficient coding. A quantized block is scanned low-frequency
// first; each coefficient up to the last non-zero one is coded as a token, and
// the block closes with an end-of-block token unless the last coefficient fills
// the block. Signs ride in their own context; escape magnitudes are coded as a
// class token plus raw suffix bits.
//
// Encoder and decoder walk the identical scan and context sequence, which is
// what keeps the shared rANS stream in step. txIdx selects the luma context
// block (statistics differ sharply by transform size); chroma ignores it.

// encodeBlock appends one block's coefficient tokens to a tile stream. levels
// holds n*n quantized coefficients in row-major order. It reports whether the
// block was all-zero (a single EOB), which feeds the coded-block pattern.
func encodeBlock(s *tileStream, plane, txIdx int, levels []int32, n int) bool {
	scan := scanOrders[n]
	total := n * n

	last := -1
	for i := 0; i < total; i++ {
		if levels[scan[i]] != 0 {
			last = i
		}
	}

	prevClass := 0
	for i := 0; i <= last; i++ {
		c := levels[scan[i]]
		ctx := tokenCtx(plane, txIdx, bandOf(i), prevClass)
		mag := int(abs32(c))
		switch {
		case mag == 0:
			s.put(ctx, tZero)
			prevClass = 0
		case mag <= 4:
			s.put(ctx, tZero+mag) // tOne..tFour
			s.put(ctxSign, signBit(c))
			prevClass = classForMag(mag)
		default:
			s.put(ctx, tEscape)
			putEscapeValue(s, mag-5)
			s.put(ctxSign, signBit(c))
			prevClass = 2
		}
	}
	if last < total-1 {
		ctx := tokenCtx(plane, txIdx, bandOf(last+1), prevClass)
		s.put(ctx, tEOB)
	}
	return last < 0
}

// decodeBlock reads one block's coefficients into levels (row-major, n*n),
// which the caller must have zeroed.
func decodeBlock(d *ransDecoder, plane, txIdx int, levels []int32, n int) {
	scan := scanOrders[n]
	total := n * n

	prevClass := 0
	for i := 0; i < total; i++ {
		ctx := tokenCtx(plane, txIdx, bandOf(i), prevClass)
		t := d.decode(ctx)
		if t == tEOB {
			return
		}
		if t == tZero {
			levels[scan[i]] = 0
			prevClass = 0
			continue
		}
		var mag int
		if t == tEscape {
			mag = 5 + d.getEscapeValue()
			prevClass = 2
		} else {
			mag = t - tZero // tOne..tFour -> 1..4
			prevClass = classForMag(mag)
		}
		if d.decode(ctxSign) == 1 {
			mag = -mag
		}
		levels[scan[i]] = int32(mag)
	}
}

func signBit(c int32) int {
	if c < 0 {
		return 1
	}
	return 0
}

package ebiv

// Residual coefficient coding. A quantized block is scanned low-frequency
// first; each coefficient up to the last non-zero one is coded as a token, and
// the block closes with an end-of-block token unless the last coefficient fills
// the block. Signs ride in their own context; escape magnitudes are coded as a
// class token plus raw suffix bits.
//
// Sign data hiding (v3): the first nonzero coefficient's sign is not coded
// inline. When the block's nonzero span (last scan position minus first) is at
// least sdhMinSpan, the sign is inferred from the parity of the sum of
// absolute levels — the encoder adjusts one level when the parity disagrees
// (applySignDataHiding). Shorter blocks code the sign explicitly, but at the
// end of the block, since the decoder only knows the span once the block is
// fully parsed.
//
// Encoder and decoder walk the identical scan and context sequence, which is
// what keeps the shared rANS stream in step. txIdx selects the luma context
// block (statistics differ sharply by transform size); chroma ignores it.

// sdhMinSpan is the minimum nonzero span (last minus first scan position) for
// a block to hide its first sign in level parity. HEVC's value; blocks with
// fewer, tighter coefficients would pay more in parity-fixing distortion than
// the one bit saves.
const sdhMinSpan = 4

// encodeBlock appends one block's coefficient tokens to a tile stream. levels
// holds n*n quantized coefficients in row-major order. It reports whether the
// block was all-zero (a single EOB), which feeds the coded-block pattern.
func encodeBlock(s *tileStream, plane, txIdx int, levels []int32, n int) bool {
	scan := scanOrders[n]
	total := n * n

	first, last := -1, -1
	for i := 0; i < total; i++ {
		if levels[scan[i]] != 0 {
			if first < 0 {
				first = i
			}
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
			prevClass = classForMag(mag)
		default:
			s.put(ctx, tEscape)
			putEscapeValue(s, mag-5)
			prevClass = 2
		}
		if mag != 0 && i != first {
			s.put(ctxSign, signBit(c))
		}
	}
	if last < total-1 {
		ctx := tokenCtx(plane, txIdx, bandOf(last+1), prevClass)
		s.put(ctx, tEOB)
	}
	// The first sign closes the block when the span is too short to hide it in
	// parity; applySignDataHiding has already made parity match otherwise.
	if first >= 0 && last-first < sdhMinSpan {
		s.put(ctxSign, signBit(levels[scan[first]]))
	}
	return last < 0
}

// decodeBlock reads one block's coefficients into levels (row-major, n*n),
// which the caller must have zeroed.
func decodeBlock(d *ransDecoder, plane, txIdx int, levels []int32, n int) {
	scan := scanOrders[n]
	total := n * n

	first, last := -1, -1
	var sumAbs int32
	prevClass := 0
	for i := 0; i < total; i++ {
		ctx := tokenCtx(plane, txIdx, bandOf(i), prevClass)
		t := d.decode(ctx)
		if t == tEOB {
			break
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
		sumAbs += int32(mag) // mag is still absolute here
		if first < 0 {
			first = i // sign resolves at the end of the block
		} else if d.decode(ctxSign) == 1 {
			mag = -mag
		}
		levels[scan[i]] = int32(mag)
		last = i
	}
	if first < 0 {
		return
	}
	neg := false
	if last-first >= sdhMinSpan {
		neg = sumAbs&1 == 1
	} else {
		neg = d.decode(ctxSign) == 1
	}
	if neg {
		levels[scan[first]] = -levels[scan[first]]
	}
}

// applySignDataHiding makes a qualifying block's level parity encode its first
// nonzero coefficient's sign, adjusting one level when the parity disagrees.
// Adjustments never move the first or last nonzero position, so the decoder's
// qualification test sees the same span. Encoder-only policy — any level
// pattern is a legal bitstream — chosen to minimize damage: shave a magnitude
// above one, else raise an interior zero, else bump an interior one.
func applySignDataHiding(levels []int32, n int) {
	scan := scanOrders[n]
	total := n * n
	first, last := -1, -1
	var sum int32
	for i := 0; i < total; i++ {
		if v := abs32(levels[scan[i]]); v != 0 {
			if first < 0 {
				first = i
			}
			last = i
			sum += v
		}
	}
	if first < 0 || last-first < sdhMinSpan {
		return
	}
	if int(sum&1) == signBit(levels[scan[first]]) {
		return // parity already encodes the sign
	}
	// Shave one step off the latest coefficient that stays nonzero (including
	// the first — only its magnitude matters, its sign is the hidden one).
	for i := last; i >= first; i-- {
		if v := levels[scan[i]]; abs32(v) > 1 {
			if v > 0 {
				levels[scan[i]] = v - 1
			} else {
				levels[scan[i]] = v + 1
			}
			return
		}
	}
	// All magnitudes are one: raise the earliest interior zero...
	for i := first + 1; i < last; i++ {
		if levels[scan[i]] == 0 {
			levels[scan[i]] = 1
			return
		}
	}
	// ...or, with a dense run of ones, bump the one after the first. The span
	// requirement guarantees this interior position exists.
	if levels[scan[first+1]] > 0 {
		levels[scan[first+1]]++
	} else {
		levels[scan[first+1]]--
	}
}

func signBit(c int32) int {
	if c < 0 {
		return 1
	}
	return 0
}

package ebiv

import "math/bits"

// Motion-vector prediction and entropy coding, shared by encoder and decoder
// so the two can never diverge.
//
// Prediction is component-wise median-of-3 over the left, above, and
// above-right neighbors (above-left substitutes when above-right is outside
// the tile), all within the tile so tiles stay independently decodable.
// Unavailable neighbors contribute a zero vector, except that a single
// available neighbor is used directly.
//
// A delta component is coded as zigzag value u, then class = bits.Len(u) from
// a small modeled alphabet, then class-1 raw suffix bits through the uniform
// bypass context. This replaces v1's raw LEB128 bytes through a 256-symbol
// context, which the bit audit measured at 14.2% of the stream.

// predictMV returns the median MV predictor for macroblock (mbx,mby) from the
// per-frame MV grid, restricted to tile bounds b.
func predictMV(mv []motionVector, stride, mbx, mby int, b tileBounds) motionVector {
	var a, bv, c motionVector
	aOK := mbx > b.mbX0
	bOK := mby > b.mbY0
	cOK := false
	if aOK {
		a = mv[mby*stride+mbx-1]
	}
	if bOK {
		bv = mv[(mby-1)*stride+mbx]
		if mbx+1 < b.mbX1 {
			c = mv[(mby-1)*stride+mbx+1]
			cOK = true
		} else if aOK {
			c = mv[(mby-1)*stride+mbx-1] // above-left substitutes at the tile edge
			cOK = true
		}
	}
	switch {
	case !aOK && !bOK && !cOK:
		return motionVector{}
	case aOK && !bOK && !cOK:
		return a
	case !aOK && bOK && !cOK:
		return bv
	}
	// Two or three available; missing candidates contribute zero vectors.
	return motionVector{
		x: median3(a.x, bv.x, c.x),
		y: median3(a.y, bv.y, c.y),
	}
}

func median3(a, b, c int16) int16 {
	if a > b {
		a, b = b, a
	}
	if b > c {
		b = c
	}
	if a > b {
		b = a
	}
	return b
}

// putRawBits appends n raw bits of v, MSB first, through the bypass context.
func putRawBits(s *tileStream, v uint, n int) {
	for i := n - 1; i >= 0; i-- {
		s.put(ctxBypass, int(v>>uint(i))&1)
	}
}

// getRawBits reads n raw bits, MSB first.
func (d *ransDecoder) getRawBits(n int) uint {
	var v uint
	for i := 0; i < n; i++ {
		v = v<<1 | uint(d.decode(ctxBypass))
	}
	return v
}

// putClassValue codes a non-negative value as class + suffix bits: class 0 is
// the value 0; class k covers [2^(k-1), 2^k), coded as k then the low k-1 bits.
func putClassValue(s *tileStream, classCtx int, v uint) {
	cls := bits.Len(v)
	s.put(classCtx, cls)
	if cls > 1 {
		putRawBits(s, v-(1<<uint(cls-1)), cls-1)
	}
}

// getClassValue reads a class+suffix value. maxClass bounds the class so a
// corrupt stream cannot demand an absurd suffix length.
func (d *ransDecoder) getClassValue(classCtx, maxClass int) uint {
	cls := d.decode(classCtx)
	if cls < 0 || cls >= maxClass {
		d.fail(ErrCorrupt)
		return 0
	}
	if cls == 0 {
		return 0
	}
	if cls == 1 {
		return 1
	}
	return 1<<uint(cls-1) | d.getRawBits(cls-1)
}

// putMVComponent codes one motion-vector delta component.
func putMVComponent(s *tileStream, classCtx, delta int) {
	putClassValue(s, classCtx, zigzagEncode(delta))
}

// getMVComponent reads one motion-vector delta component.
func (d *ransDecoder) getMVComponent(classCtx int) int {
	return zigzagDecode(d.getClassValue(classCtx, numMVClasses))
}

// putEscapeValue codes an escape magnitude remainder (magnitude-5, >= 0).
func putEscapeValue(s *tileStream, v int) {
	putClassValue(s, ctxEscClass, uint(v))
}

// getEscapeValue reads an escape magnitude remainder.
func (d *ransDecoder) getEscapeValue() int {
	return int(d.getClassValue(ctxEscClass, numEscClasses))
}

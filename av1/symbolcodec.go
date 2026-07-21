package av1

import (
	"math/bits"
)

// SymbolCodec implements the AV1 multi-symbol arithmetic decoder.
// Uses a 64-bit value window matching dav1d's msac.c exactly.
type SymbolCodec struct {
	data           []byte
	bytePos        int
	dif            uint64
	rng            uint32
	cnt            int
	allowUpdateCDF bool
}

// NewSymbolCodec creates a new symbol codec from compressed data.
func NewSymbolCodec(data []byte) *SymbolCodec {
	sc := &SymbolCodec{
		data:           data,
		dif:            0,
		rng:            0x8000,
		cnt:            -15,
		allowUpdateCDF: true,
	}
	sc.refill()
	return sc
}

// refill reads bytes into the value window. Matches dav1d ctx_refill exactly.
func (sc *SymbolCodec) refill() {
	c := 64 - sc.cnt - 24
	for c >= 0 && sc.bytePos < len(sc.data) {
		sc.dif |= uint64(sc.data[sc.bytePos]^0xFF) << uint(c)
		sc.bytePos++
		c -= 8
	}
	if c >= 0 {
		sc.dif |= ^(^uint64(0xFF) << uint(c))
	}
	sc.cnt = 64 - c - 24
}

// normalize renormalizes the range and value. Matches dav1d ctx_norm exactly.
func (sc *SymbolCodec) normalize(dif uint64, rng uint32) {
	d := int(bits.LeadingZeros32(rng)) - 16
	cnt := sc.cnt
	sc.dif = dif << uint(d)
	sc.rng = rng << uint(d)
	sc.cnt = cnt - d
	if uint(cnt) < uint(d) {
		sc.refill()
	}
}

// ReadSymbol reads a symbol using the given CDF (descending format).
// nsymbs is the number of symbols. CDF has nsymbs entries:
// nsymbs-1 CDF values followed by 1 adaptation counter.
func (sc *SymbolCodec) ReadSymbol(cdf []uint16, nsymbs int) int {
	n := nsymbs - 1
	c := uint32(sc.dif >> 48)
	r := sc.rng >> 8
	u := sc.rng
	val := 0

	var v uint32
	for {
		v = (r * (uint32(cdf[val]) >> 6)) >> 1
		v += 4 * uint32(n-val)
		if c >= v {
			break
		}
		u = v
		val++
		if val >= n {
			v = 0
			break
		}
	}

	sc.normalize(sc.dif-uint64(v)<<48, u-v)

	if sc.allowUpdateCDF {
		UpdateCDF(cdf, nsymbs, val)
	}
	return val
}

// ReadBoolEqui reads a boolean with 50/50 probability. Matches dav1d decode_bool_equi.
func (sc *SymbolCodec) ReadBoolEqui() bool {
	v := (sc.rng>>8)<<7 + 4
	vw := uint64(v) << 48
	ret := sc.dif >= vw
	dif := sc.dif
	if ret {
		dif -= vw
		v = sc.rng - v
	}
	sc.normalize(dif, v)
	return !ret
}

// ReadBool reads a boolean with given probability (out of 256).
func (sc *SymbolCodec) ReadBool(prob int) bool {
	v := (sc.rng>>8)*uint32(prob) + 4
	vw := uint64(v) << 48
	ret := sc.dif >= vw
	dif := sc.dif
	if ret {
		dif -= vw
		v = sc.rng - v
	}
	sc.normalize(dif, v)
	return !ret
}

// ReadBoolAdapt reads an adaptive boolean from a 2-entry CDF.
func (sc *SymbolCodec) ReadBoolAdapt(cdf []uint16) bool {
	return sc.ReadSymbol(cdf, 2) == 1
}

// ReadHiTok reads a hi-tok value using iterative 4-symbol CDF reads.
// Matches dav1d dav1d_msac_decode_hi_tok_c exactly.
func (sc *SymbolCodec) ReadHiTok(cdf []uint16) int {
	tok_br := sc.ReadSymbol(cdf, 4)
	tok := 3 + tok_br
	if tok_br == 3 {
		tok_br = sc.ReadSymbol(cdf, 4)
		tok = 6 + tok_br
		if tok_br == 3 {
			tok_br = sc.ReadSymbol(cdf, 4)
			tok = 9 + tok_br
			if tok_br == 3 {
				tok_br = sc.ReadSymbol(cdf, 4)
				tok = 12 + tok_br
			}
		}
	}
	return tok
}

// ReadLiteral reads n bits using flat probability (each bit 50/50).
func (sc *SymbolCodec) ReadLiteral(n int) uint32 {
	var val uint32
	for i := 0; i < n; i++ {
		val = val << 1
		if sc.ReadBoolEqui() {
			val |= 1
		}
	}
	return val
}

// UpdateCDF adapts the CDF after decoding a symbol.
// nsymbs is the number of symbols. CDF layout: nsymbs-1 CDF values, then counter.
func UpdateCDF(cdf []uint16, nsymbs, symbol int) {
	n := nsymbs - 1
	count := cdf[n]
	rate := 4 + int(count>>4)
	if n > 2 {
		rate++
	}

	for i := 0; i < n; i++ {
		if i < symbol {
			cdf[i] += (32768 - cdf[i]) >> uint(rate)
		} else {
			cdf[i] -= cdf[i] >> uint(rate)
		}
	}

	if count < 32 {
		cdf[n] = count + 1
	}
}

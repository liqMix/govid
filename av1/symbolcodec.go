package av1

import "fmt"

// SymbolCodec implements the AV1 multi-symbol arithmetic decoder.
// Uses a 16-bit value window with range normalized to [32768, 65535].
type SymbolCodec struct {
	data    []byte
	pos     int    // byte position in data
	value   uint32 // current coded value (16-bit)
	range_  uint32 // current range, always in [32768, 65535] after normalization
	cnt     int    // bit counter for refill
	padding int    // padding bytes when data exhausted
}

// NewSymbolCodec creates a new symbol codec from compressed data.
func NewSymbolCodec(data []byte) *SymbolCodec {
	sc := &SymbolCodec{
		data:   data,
		range_: 65535, // (1 << 16) - 1
	}

	// Read initial 2 bytes into value (XOR'd per AV1/dav1d complement convention).
	if len(data) >= 1 {
		sc.value = uint32(data[0]^0xFF) << 8
		sc.pos = 1
	}
	if len(data) >= 2 {
		sc.value |= uint32(data[1] ^ 0xFF)
		sc.pos = 2
	}
	sc.cnt = -15

	return sc
}

// ReadSymbol reads a symbol using the given CDF (descending format).
// cdf has nsymbs+1 entries: cdf[0..nsymbs-1] are descending cumulative frequencies,
// cdf[nsymbs] is the adaptation counter.
func (sc *SymbolCodec) ReadSymbol(cdf []uint16, nsymbs int) (int, error) {
	if sc.padding > 4 {
		return 0, fmt.Errorf("symbol codec: data exhausted")
	}

	c := sc.range_ >> 8
	symbol := 0
	u := sc.range_

	var v uint32
	for {
		v = (c * uint32(cdf[symbol]>>6)) >> 1
		v += 4 * uint32(nsymbs-1-symbol)
		if sc.value >= v {
			break
		}
		u = v
		symbol++
		if symbol >= nsymbs-1 {
			v = 0
			break
		}
	}

	sc.value -= v
	sc.range_ = u - v

	if err := sc.renormalize(); err != nil {
		return 0, err
	}
	UpdateCDF(cdf, nsymbs, symbol)
	return symbol, nil
}

// ReadBool reads a boolean with given probability (out of 256).
// prob=128 gives 50/50.
func (sc *SymbolCodec) ReadBool(prob int) (bool, error) {
	if sc.padding > 4 {
		return false, fmt.Errorf("symbol codec: data exhausted")
	}

	split := 1 + (((sc.range_ - 1) * uint32(prob)) >> 8)

	if sc.value < split {
		sc.range_ = split
		if err := sc.renormalize(); err != nil {
			return false, err
		}
		// Complement convention: low complement (value < split) = high raw = true
		return true, nil
	}

	sc.value -= split
	sc.range_ -= split
	if err := sc.renormalize(); err != nil {
		return false, err
	}
	// Complement convention: high complement (value >= split) = low raw = false
	return false, nil
}

// ReadLiteral reads n bits using flat probability (each bit 50/50).
func (sc *SymbolCodec) ReadLiteral(n int) (uint32, error) {
	var val uint32
	for i := 0; i < n; i++ {
		bit, err := sc.ReadBool(128)
		if err != nil {
			return 0, err
		}
		val = val << 1
		if bit {
			val |= 1
		}
	}
	return val, nil
}

// renormalize adjusts range and value, keeping range in [32768, 65535].
func (sc *SymbolCodec) renormalize() error {
	for sc.range_ < 32768 {
		sc.range_ <<= 1
		sc.value <<= 1

		sc.cnt++
		if sc.cnt >= 0 {
			sc.cnt = -8
			if sc.pos < len(sc.data) {
				sc.value |= uint32(sc.data[sc.pos] ^ 0xFF)
				sc.pos++
			} else {
				sc.padding++
				if sc.padding > 4 {
					return fmt.Errorf("symbol codec: data exhausted")
				}
			}
		}
	}
	return nil
}

// UpdateCDF adapts the CDF after decoding a symbol.
// Matches dav1d's update_cdf implementation.
// cdf[nsymbs] stores the adaptation counter (0..32).
func UpdateCDF(cdf []uint16, nsymbs, symbol int) {
	count := cdf[nsymbs]
	rate := int((count>>4)|4)
	if nsymbs > 2 {
		rate++
	}

	for i := 0; i < nsymbs-1; i++ {
		if i < symbol {
			cdf[i] += (32768 - cdf[i]) >> uint(rate)
		} else {
			cdf[i] -= cdf[i] >> uint(rate)
		}
	}

	if count < 32 {
		cdf[nsymbs] = count + 1
	}
}

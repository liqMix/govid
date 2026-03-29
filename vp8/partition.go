// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vp8

// Each VP8 frame consists of between 2 and 9 bitstream partitions.
// Each partition is byte-aligned and is independently arithmetic-encoded.
//
// This file implements decoding a partition's bitstream, as specified in
// chapter 7. The implementation follows libwebp's approach instead of the
// specification's reference C implementation. For example, we use a look-up
// table instead of a for loop to recalibrate the encoded range.

var (
	lutShift = [127]uint8{
		7, 6, 6, 5, 5, 5, 5, 4, 4, 4, 4, 4, 4, 4, 4,
		3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3,
		2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
		2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	}
	lutRangeM1 = [127]uint8{
		127,
		127, 191,
		127, 159, 191, 223,
		127, 143, 159, 175, 191, 207, 223, 239,
		127, 135, 143, 151, 159, 167, 175, 183, 191, 199, 207, 215, 223, 231, 239, 247,
		127, 131, 135, 139, 143, 147, 151, 155, 159, 163, 167, 171, 175, 179, 183, 187,
		191, 195, 199, 203, 207, 211, 215, 219, 223, 227, 231, 235, 239, 243, 247, 251,
		127, 129, 131, 133, 135, 137, 139, 141, 143, 145, 147, 149, 151, 153, 155, 157,
		159, 161, 163, 165, 167, 169, 171, 173, 175, 177, 179, 181, 183, 185, 187, 189,
		191, 193, 195, 197, 199, 201, 203, 205, 207, 209, 211, 213, 215, 217, 219, 221,
		223, 225, 227, 229, 231, 233, 235, 237, 239, 241, 243, 245, 247, 249, 251, 253,
	}
)

// uniformProb represents a 50% probability that the next bit is 0.
const uniformProb = 128

// partition holds arithmetic-coded bits.
// The implementation matches libvpx's vp8dx_bool_decoder (lazy refill).
type partition struct {
	// buf is the input bytes.
	buf []byte
	// r is how many of buf's bytes have been consumed.
	r int
	// rng is the decoder range [1..255].
	rng uint32
	// value is the MSB-aligned coded data register.
	value uint32
	// count is a signed bit counter; refill when < 0.
	count int
	// unexpectedEOF tells whether we tried to read past buf.
	unexpectedEOF bool
}

// init initializes the partition.
func (p *partition) init(buf []byte) {
	p.buf = buf
	p.r = 0
	p.rng = 255
	p.value = 0
	p.count = -8
	p.unexpectedEOF = false
	p.fill()
}

// fill reads bytes from buf into value, matching vp8dx_bool_decoder_fill.
func (p *partition) fill() {
	shift := uint(24 - (p.count + 8))
	for shift < 32 && p.r < len(p.buf) {
		p.value |= uint32(p.buf[p.r]) << shift
		p.r++
		p.count += 8
		shift -= 8
	}
}

// readBit returns the next bit, matching libvpx vp8dx_decode_bool.
func (p *partition) readBit(prob uint8) bool {
	if p.count < 0 {
		p.fill()
		if p.count < 0 {
			// Buffer exhausted and fill couldn't provide enough bits.
			p.unexpectedEOF = true
		}
	}
	split := 1 + ((p.rng - 1) * uint32(prob) >> 8)
	bigsplit := split << 24
	bit := p.value >= bigsplit
	if bit {
		p.rng -= split
		p.value -= bigsplit
	} else {
		p.rng = split
	}
	// Normalize: rng is stored as rng-1 index into LUT.
	idx := p.rng - 1
	if idx < 127 {
		shift := lutShift[idx]
		p.rng = uint32(lutRangeM1[idx]) + 1
		p.value <<= shift
		p.count -= int(shift)
	}
	return bit
}

// readUint returns the next n-bit unsigned integer.
func (p *partition) readUint(prob, n uint8) uint32 {
	var u uint32
	for n > 0 {
		n--
		if p.readBit(prob) {
			u |= 1 << n
		}
	}
	return u
}

// readInt returns the next n-bit signed integer.
func (p *partition) readInt(prob, n uint8) int32 {
	u := p.readUint(prob, n)
	b := p.readBit(prob)
	if b {
		return -int32(u)
	}
	return int32(u)
}

// readOptionalInt returns the next n-bit signed integer in an encoding
// where the likely result is zero.
func (p *partition) readOptionalInt(prob, n uint8) int32 {
	if !p.readBit(prob) {
		return 0
	}
	return p.readInt(prob, n)
}

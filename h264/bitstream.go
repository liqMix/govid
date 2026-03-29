package h264

import "fmt"

// BitReader reads bits from a byte slice, supporting Exp-Golomb coding
// as used in H.264 bitstream syntax.
type BitReader struct {
	data    []byte
	bytePos int
	bitPos  uint // 0-7, counts from MSB
}

// NewBitReader creates a BitReader over the given data.
func NewBitReader(data []byte) *BitReader {
	return &BitReader{data: data}
}

// ReadBits reads up to 32 bits and returns them right-aligned.
func (br *BitReader) ReadBits(n uint) (uint32, error) {
	if n == 0 {
		return 0, nil
	}
	if n > 32 {
		return 0, fmt.Errorf("ReadBits: n=%d exceeds 32", n)
	}
	var val uint32
	for i := uint(0); i < n; i++ {
		if br.bytePos >= len(br.data) {
			return 0, fmt.Errorf("ReadBits: unexpected end of data")
		}
		bit := (br.data[br.bytePos] >> (7 - br.bitPos)) & 1
		val = (val << 1) | uint32(bit)
		br.bitPos++
		if br.bitPos == 8 {
			br.bitPos = 0
			br.bytePos++
		}
	}
	return val, nil
}

// ReadBool reads a single bit as a boolean.
func (br *BitReader) ReadBool() (bool, error) {
	v, err := br.ReadBits(1)
	return v == 1, err
}

// ReadUE reads an unsigned Exp-Golomb coded value (ue(v) in the spec).
func (br *BitReader) ReadUE() (uint32, error) {
	leadingZeros := 0
	for {
		bit, err := br.ReadBool()
		if err != nil {
			return 0, err
		}
		if bit {
			break
		}
		leadingZeros++
		if leadingZeros > 31 {
			return 0, fmt.Errorf("ReadUE: too many leading zeros")
		}
	}
	if leadingZeros == 0 {
		return 0, nil
	}
	suffix, err := br.ReadBits(uint(leadingZeros))
	if err != nil {
		return 0, err
	}
	return (1 << leadingZeros) - 1 + suffix, nil
}

// ReadSE reads a signed Exp-Golomb coded value (se(v) in the spec).
func (br *BitReader) ReadSE() (int32, error) {
	ue, err := br.ReadUE()
	if err != nil {
		return 0, err
	}
	if ue%2 == 0 {
		return -int32(ue / 2), nil
	}
	return int32((ue + 1) / 2), nil
}

// MoreRBSPData returns true if there is more RBSP data remaining
// (i.e., not just the stop bit and trailing zeros).
func (br *BitReader) MoreRBSPData() bool {
	totalBits := len(br.data) * 8
	currentBit := br.bytePos*8 + int(br.bitPos)
	if currentBit >= totalBits {
		return false
	}
	// Check if remaining bits are just the RBSP stop bit (1) followed by zeros.
	remaining := totalBits - currentBit
	if remaining <= 0 {
		return false
	}
	// Save position.
	savedByte := br.bytePos
	savedBit := br.bitPos
	defer func() {
		br.bytePos = savedByte
		br.bitPos = savedBit
	}()

	// Find the last 1-bit in remaining data (the RBSP stop bit is the
	// final 1-bit; if there is data before it beyond currentBit, there's more).
	for i := len(br.data) - 1; i >= savedByte; i-- {
		b := br.data[i]
		if b == 0 {
			continue
		}
		// Lowest set bit (LSB side) is the last in stream order for this byte.
		for bit := 0; bit < 8; bit++ {
			if b&(1<<uint(bit)) != 0 {
				lastOneBitPos := i*8 + (7 - bit)
				return lastOneBitPos > currentBit
			}
		}
	}
	return false
}

// ByteAlign advances to the next byte boundary.
func (br *BitReader) ByteAlign() {
	if br.bitPos != 0 {
		br.bitPos = 0
		br.bytePos++
	}
}

// BitsRead returns the total number of bits consumed so far.
func (br *BitReader) BitsRead() int {
	return br.bytePos*8 + int(br.bitPos)
}


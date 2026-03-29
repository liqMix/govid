package av1

import "fmt"

// BitReader reads bits from a byte slice for AV1 bitstream parsing.
// AV1 spec Section 4.10.
type BitReader struct {
	data    []byte
	bytePos int
	bitPos  uint // 0-7, counts from MSB
}

// NewBitReader creates a BitReader over the given data.
func NewBitReader(data []byte) *BitReader {
	return &BitReader{data: data}
}

// ReadBits reads up to 32 bits and returns them right-aligned (f(n) in the spec).
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
			return 0, fmt.Errorf("ReadBits: unexpected end of data at byte %d/%d", br.bytePos, len(br.data))
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

// ReadBit reads a single bit.
func (br *BitReader) ReadBit() (uint32, error) {
	return br.ReadBits(1)
}

// ReadBool reads a single bit as a boolean.
func (br *BitReader) ReadBool() (bool, error) {
	v, err := br.ReadBits(1)
	return v == 1, err
}

// ReadUVLC reads an unsigned variable-length coded integer.
// AV1 spec 4.10.3.
func (br *BitReader) ReadUVLC() (uint32, error) {
	leadingZeros := 0
	for {
		done, err := br.ReadBool()
		if err != nil {
			return 0, err
		}
		if done {
			break
		}
		leadingZeros++
		if leadingZeros >= 32 {
			return 0xFFFFFFFF, nil
		}
	}
	if leadingZeros >= 32 {
		return 0xFFFFFFFF, nil
	}
	if leadingZeros == 0 {
		return 0, nil
	}
	val, err := br.ReadBits(uint(leadingZeros))
	if err != nil {
		return 0, err
	}
	return val + (1 << uint(leadingZeros)) - 1, nil
}

// ReadSU reads a signed value using n bits.
// AV1 spec 4.10.6.
func (br *BitReader) ReadSU(n uint) (int32, error) {
	val, err := br.ReadBits(n)
	if err != nil {
		return 0, err
	}
	signMask := uint32(1) << (n - 1)
	if val&signMask != 0 {
		return int32(val) - int32(1<<n), nil
	}
	return int32(val), nil
}

// ReadNS reads a non-symmetric unsigned coded integer with max value n.
// AV1 spec 4.10.7.
func (br *BitReader) ReadNS(n uint32) (uint32, error) {
	if n <= 1 {
		return 0, nil
	}
	w := FloorLog2(n) + 1
	m := (uint32(1) << w) - n
	v, err := br.ReadBits(uint(w - 1))
	if err != nil {
		return 0, err
	}
	if v < m {
		return v, nil
	}
	extra, err := br.ReadBit()
	if err != nil {
		return 0, err
	}
	return (v << 1) - m + extra, nil
}

// ReadDeltaQ reads a delta quantizer value.
// AV1 spec 5.9.17.
func (br *BitReader) ReadDeltaQ() (int32, error) {
	deltaCoded, err := br.ReadBool()
	if err != nil {
		return 0, err
	}
	if !deltaCoded {
		return 0, nil
	}
	return br.ReadSU(7)
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

// HasMoreData returns true if there is more data to read.
func (br *BitReader) HasMoreData() bool {
	return br.bytePos < len(br.data)
}

// FloorLog2 returns floor(log2(n)) for n > 0.
func FloorLog2(n uint32) uint {
	if n == 0 {
		return 0
	}
	var log uint
	for n > 1 {
		n >>= 1
		log++
	}
	return log
}

// CeilLog2 returns ceil(log2(n)) for n > 0.
func CeilLog2(n uint32) uint {
	if n <= 1 {
		return 0
	}
	return FloorLog2(n-1) + 1
}

// TileLog2 computes the smallest k such that (1 << k) >= target, given a base.
// Used for tile size computation. AV1 spec 5.9.15.
func TileLog2(blkSize, target uint32) uint {
	var k uint
	for (blkSize << k) < target {
		k++
	}
	return k
}

// ParseLEB128 reads a LEB128 (Little Endian Base 128) encoded unsigned integer.
// AV1 spec 4.10.5. Returns the value and number of bytes consumed.
func ParseLEB128(data []byte) (value uint32, bytesRead int, err error) {
	var result uint64
	for i := 0; i < 8; i++ {
		if i >= len(data) {
			return 0, 0, fmt.Errorf("LEB128: unexpected end of data")
		}
		b := data[i]
		result |= uint64(b&0x7F) << (i * 7)
		bytesRead = i + 1
		if b&0x80 == 0 {
			if result > 0xFFFFFFFF {
				return 0, 0, fmt.Errorf("LEB128: value exceeds 32 bits")
			}
			return uint32(result), bytesRead, nil
		}
	}
	return 0, 0, fmt.Errorf("LEB128: too many bytes")
}

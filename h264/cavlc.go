package h264

import "fmt"

// CAVLC (Context-Adaptive Variable-Length Coding) as specified in
// ITU-T H.264 section 9.2. Table data from FFmpeg libavcodec/h264_cavlc.c.

var errCAVLC = fmt.Errorf("CAVLC bitstream error")

// coeff_token tables: indexed [table][totalCoeff*4 + trailingOnes].
// Each row of 4 values corresponds to trailingOnes = 0,1,2,3 for a given totalCoeff.
// Table 0: 0 ≤ nC < 2, Table 1: 2 ≤ nC < 4, Table 2: 4 ≤ nC < 8, Table 3: nC ≥ 8.
var coeffTokenLen = [4][68]uint8{
	{ // Table 0: 0 ≤ nC < 2
		1, 0, 0, 0,
		6, 2, 0, 0, 8, 6, 3, 0, 9, 8, 7, 5, 10, 9, 8, 6,
		11, 10, 9, 7, 13, 11, 10, 8, 13, 13, 11, 9, 13, 13, 13, 10,
		14, 14, 13, 11, 14, 14, 14, 13, 15, 15, 14, 14, 15, 15, 15, 14,
		16, 15, 15, 15, 16, 16, 16, 15, 16, 16, 16, 16, 16, 16, 16, 16,
	},
	{ // Table 1: 2 ≤ nC < 4
		2, 0, 0, 0,
		6, 2, 0, 0, 6, 5, 3, 0, 7, 6, 6, 4, 8, 6, 6, 4,
		8, 7, 7, 5, 9, 8, 8, 6, 11, 9, 9, 6, 11, 11, 11, 7,
		12, 11, 11, 9, 12, 12, 12, 11, 12, 12, 12, 11, 13, 13, 13, 12,
		13, 13, 13, 13, 13, 14, 13, 13, 14, 14, 14, 13, 14, 14, 14, 14,
	},
	{ // Table 2: 4 ≤ nC < 8
		4, 0, 0, 0,
		6, 4, 0, 0, 6, 5, 4, 0, 6, 5, 5, 4, 7, 5, 5, 4,
		7, 5, 5, 4, 7, 6, 6, 4, 7, 6, 6, 4, 8, 7, 7, 5,
		8, 8, 7, 6, 9, 8, 8, 7, 9, 9, 8, 8, 9, 9, 9, 8,
		10, 9, 9, 9, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
	},
	{ // Table 3: nC ≥ 8
		6, 0, 0, 0,
		6, 6, 0, 0, 6, 6, 6, 0, 6, 6, 6, 6, 6, 6, 6, 6,
		6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
		6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
		6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
	},
}

var coeffTokenBits = [4][68]uint8{
	{ // Table 0
		1, 0, 0, 0,
		5, 1, 0, 0, 7, 4, 1, 0, 7, 6, 5, 3, 7, 6, 5, 3,
		7, 6, 5, 4, 15, 6, 5, 4, 11, 14, 5, 4, 8, 10, 13, 4,
		15, 14, 9, 4, 11, 10, 13, 12, 15, 14, 9, 12, 11, 10, 13, 8,
		15, 1, 9, 12, 11, 14, 13, 8, 7, 10, 9, 12, 4, 6, 5, 8,
	},
	{ // Table 1
		3, 0, 0, 0,
		11, 2, 0, 0, 7, 7, 3, 0, 7, 10, 9, 5, 7, 6, 5, 4,
		4, 6, 5, 6, 7, 6, 5, 8, 15, 6, 5, 4, 11, 14, 13, 4,
		15, 10, 9, 4, 11, 14, 13, 12, 8, 10, 9, 8, 15, 14, 13, 12,
		11, 10, 9, 12, 7, 11, 6, 8, 9, 8, 10, 1, 7, 6, 5, 4,
	},
	{ // Table 2
		15, 0, 0, 0,
		15, 14, 0, 0, 11, 15, 13, 0, 8, 12, 14, 12, 15, 10, 11, 11,
		11, 8, 9, 10, 9, 14, 13, 9, 8, 10, 9, 8, 15, 14, 13, 13,
		11, 14, 10, 12, 15, 10, 13, 12, 11, 14, 9, 12, 8, 10, 13, 8,
		13, 7, 9, 12, 9, 12, 11, 10, 5, 8, 7, 6, 1, 4, 3, 2,
	},
	{ // Table 3
		3, 0, 0, 0,
		0, 1, 0, 0, 4, 5, 6, 0, 8, 9, 10, 11, 12, 13, 14, 15,
		16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
		32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47,
		48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63,
	},
}

// Chroma DC coeff_token (for 2x2 chroma DC blocks, 4:2:0).
var chromaDCCoeffTokenLen = [20]uint8{
	2, 0, 0, 0,
	6, 1, 0, 0,
	6, 6, 3, 0,
	6, 7, 7, 6,
	6, 8, 8, 7,
}

var chromaDCCoeffTokenBits = [20]uint8{
	1, 0, 0, 0,
	7, 1, 0, 0,
	4, 6, 1, 0,
	3, 3, 2, 5,
	2, 3, 2, 0,
}

// total_zeros tables for 4x4 blocks, indexed [totalCoeff-1][totalZeros].
var totalZerosLen = [15][]uint8{
	{1, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 9},
	{3, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 6, 6, 6, 6},
	{4, 3, 3, 3, 4, 4, 3, 3, 4, 5, 5, 6, 5, 6},
	{5, 3, 4, 4, 3, 3, 3, 4, 3, 4, 5, 5, 5},
	{4, 4, 4, 3, 3, 3, 3, 3, 4, 5, 4, 5},
	{6, 5, 3, 3, 3, 3, 3, 3, 4, 3, 6},
	{6, 5, 3, 3, 3, 2, 3, 4, 3, 6},
	{6, 4, 5, 3, 2, 2, 3, 3, 6},
	{6, 6, 4, 2, 2, 3, 2, 5},
	{5, 5, 3, 2, 2, 2, 4},
	{4, 4, 3, 3, 1, 3},
	{4, 4, 2, 1, 3},
	{3, 3, 1, 2},
	{2, 2, 1},
	{1, 1},
}

var totalZerosBits = [15][]uint8{
	{1, 3, 2, 3, 2, 3, 2, 3, 2, 3, 2, 3, 2, 3, 2, 1},
	{7, 6, 5, 4, 3, 5, 4, 3, 2, 3, 2, 3, 2, 1, 0},
	{5, 7, 6, 5, 4, 3, 4, 3, 2, 3, 2, 1, 1, 0},
	{3, 7, 5, 4, 6, 5, 4, 3, 3, 2, 2, 1, 0},
	{5, 4, 3, 7, 6, 5, 4, 3, 2, 1, 1, 0},
	{1, 1, 7, 6, 5, 4, 3, 2, 1, 1, 0},
	{1, 1, 5, 4, 3, 3, 2, 1, 1, 0},
	{1, 1, 1, 3, 3, 2, 2, 1, 0},
	{1, 0, 1, 3, 2, 1, 1, 1},
	{1, 0, 1, 3, 2, 1, 1},
	{0, 1, 1, 2, 1, 3},
	{0, 1, 1, 1, 1},
	{0, 1, 1, 1},
	{0, 1, 1},
	{0, 1},
}

// Chroma DC total_zeros (4:2:0, maxCoeff=4).
var chromaDCTotalZerosLen = [3][4]uint8{
	{1, 2, 3, 3},
	{1, 2, 2, 0},
	{1, 1, 0, 0},
}

var chromaDCTotalZerosBits = [3][4]uint8{
	{1, 1, 1, 0},
	{1, 1, 0, 0},
	{1, 0, 0, 0},
}

// run_before tables, indexed [zerosLeft-1][run_before].
var runLen = [7][]uint8{
	{1, 1},
	{1, 2, 2},
	{2, 2, 2, 2},
	{2, 2, 2, 3, 3},
	{2, 2, 3, 3, 3, 3},
	{2, 3, 3, 3, 3, 3, 3},
	{3, 3, 3, 3, 3, 3, 3, 4, 5, 6, 7, 8, 9, 10, 11},
}

var runBits = [7][]uint8{
	{1, 0},
	{1, 1, 0},
	{3, 2, 1, 0},
	{3, 2, 1, 1, 0},
	{3, 2, 3, 2, 1, 0},
	{3, 0, 1, 3, 2, 5, 4},
	{7, 6, 5, 4, 3, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1},
}

// readCoeffToken reads TotalCoeff and TrailingOnes for a residual block.
// nC is the context parameter derived from neighboring non-zero counts.
func readCoeffToken(br *BitReader, nC int) (totalCoeff, trailingOnes int, err error) {
	if nC < -1 || nC > 16 {
		return 0, 0, fmt.Errorf("invalid nC=%d", nC)
	}

	if nC == -1 {
		// Chroma DC.
		return readCoeffTokenFromTable(br, chromaDCCoeffTokenLen[:], chromaDCCoeffTokenBits[:], 5)
	}

	tableIdx := 0
	switch {
	case nC < 2:
		tableIdx = 0
	case nC < 4:
		tableIdx = 1
	case nC < 8:
		tableIdx = 2
	default:
		tableIdx = 3
	}

	return readCoeffTokenFromTable(br, coeffTokenLen[tableIdx][:], coeffTokenBits[tableIdx][:], 17)
}

func readCoeffTokenFromTable(br *BitReader, lenTab, bitsTab []uint8, maxTC int) (totalCoeff, trailingOnes int, err error) {
	// Find max bits needed.
	maxBits := uint(0)
	for _, l := range lenTab {
		if uint(l) > maxBits {
			maxBits = uint(l)
		}
	}

	// Limit to available bits.
	available := uint(len(br.data)*8 - (br.bytePos*8 + int(br.bitPos)))
	if maxBits > available {
		maxBits = available
	}
	if maxBits == 0 {
		return 0, 0, errCAVLC
	}

	// Save position.
	savedByte := br.bytePos
	savedBit := br.bitPos

	code, err := br.ReadBits(maxBits)
	if err != nil {
		return 0, 0, err
	}

	// Search table for match. Try each (tc, to) pair.
	for tc := 0; tc < maxTC; tc++ {
		for to := 0; to < 4; to++ {
			idx := tc*4 + to
			if idx >= len(lenTab) {
				continue
			}
			l := lenTab[idx]
			if l == 0 {
				continue
			}
			b := bitsTab[idx]
			extracted := code >> (maxBits - uint(l))
			if extracted == uint32(b) {
				br.bytePos = savedByte
				br.bitPos = savedBit
				_, _ = br.ReadBits(uint(l))
				return tc, to, nil
			}
		}
	}

	return 0, 0, errCAVLC
}

// readResidualBlock reads CAVLC residual coefficients into coeffs.
// maxCoeff is the block size (16 for 4x4 luma, 15 for 4x4 luma AC, 4 for 2x2 chroma DC).
// nC is the CAVLC context from neighboring non-zero counts.
// Returns the number of non-zero coefficients.
func readResidualBlock(br *BitReader, coeffs []int16, maxCoeff int, nC int) (int, error) {
	totalCoeff, trailingOnes, err := readCoeffToken(br, nC)
	if err != nil {
		return 0, err
	}
	if totalCoeff == 0 {
		return 0, nil
	}

	// Read trailing ones signs (in reverse order).
	levels := make([]int, totalCoeff)
	for i := 0; i < trailingOnes; i++ {
		sign, err := br.ReadBool()
		if err != nil {
			return 0, err
		}
		if sign {
			levels[i] = -1
		} else {
			levels[i] = 1
		}
	}

	// Read remaining levels.
	suffixLength := uint(0)
	if totalCoeff > 10 && trailingOnes < 3 {
		suffixLength = 1
	}

	for i := trailingOnes; i < totalCoeff; i++ {
		levelPrefix, err := readLevelPrefix(br)
		if err != nil {
			return 0, err
		}

		var levelCode int
		if levelPrefix == 14 && suffixLength == 0 {
			// Special case: 4-bit suffix when suffixLength is 0.
			suffix, err := br.ReadBits(4)
			if err != nil {
				return 0, err
			}
			levelCode = 14 + int(suffix)
		} else if levelPrefix >= 15 {
			// Large prefix: suffix size = levelPrefix - 3.
			suffBits := uint(levelPrefix) - 3
			suffix, err := br.ReadBits(suffBits)
			if err != nil {
				return 0, err
			}
			levelCode = (15 << suffixLength) + int(suffix)
			if suffixLength == 0 {
				levelCode += 15
			}
			if levelPrefix >= 16 {
				levelCode += (1 << (levelPrefix - 3)) - 4096
			}
		} else {
			// Normal case: levelPrefix < 14, or (== 14 with suffixLength > 0).
			levelCode = int(levelPrefix) << suffixLength
			if suffixLength > 0 {
				suffix, err := br.ReadBits(suffixLength)
				if err != nil {
					return 0, err
				}
				levelCode += int(suffix)
			}
		}

		if i == trailingOnes && trailingOnes < 3 {
			levelCode += 2
		}

		if levelCode%2 == 0 {
			levels[i] = (levelCode + 2) >> 1
		} else {
			levels[i] = -(levelCode + 1) >> 1
		}

		if suffixLength == 0 {
			suffixLength = 1
		}
		absLevel := levels[i]
		if absLevel < 0 {
			absLevel = -absLevel
		}
		if absLevel > int(3<<(suffixLength-1)) {
			suffixLength++
		}
	}

	// Read total_zeros.
	totalZeros := 0
	if totalCoeff < maxCoeff {
		totalZeros, err = readTotalZeros(br, totalCoeff, maxCoeff)
		if err != nil {
			return 0, err
		}
	}

	// Read run_before for each coefficient and place in output order.
	zerosLeft := totalZeros
	coeffIdx := totalCoeff + totalZeros - 1
	for i := 0; i < totalCoeff-1; i++ {
		runBefore := 0
		if zerosLeft > 0 {
			runBefore, err = readRunBefore(br, zerosLeft)
			if err != nil {
				return 0, err
			}
		}
		if coeffIdx >= 0 && coeffIdx < len(coeffs) {
			coeffs[coeffIdx] = int16(levels[i])
		}
		coeffIdx -= 1 + runBefore
		zerosLeft -= runBefore
	}
	if coeffIdx >= 0 && coeffIdx < len(coeffs) {
		coeffs[coeffIdx] = int16(levels[totalCoeff-1])
	}

	return totalCoeff, nil
}

func readLevelPrefix(br *BitReader) (uint, error) {
	zeros := uint(0)
	for {
		bit, err := br.ReadBool()
		if err != nil {
			return 0, err
		}
		if bit {
			return zeros, nil
		}
		zeros++
		if zeros > 32 {
			return 0, errCAVLC
		}
	}
}

func readTotalZeros(br *BitReader, totalCoeff, maxCoeff int) (int, error) {
	if maxCoeff == 4 {
		// Chroma DC 4:2:0.
		return readVLCFromTable(br, chromaDCTotalZerosLen[totalCoeff-1][:], chromaDCTotalZerosBits[totalCoeff-1][:])
	}
	// 4x4 block.
	if totalCoeff < 1 || totalCoeff > 15 {
		return 0, errCAVLC
	}
	return readVLCFromTable(br, totalZerosLen[totalCoeff-1], totalZerosBits[totalCoeff-1])
}

func readRunBefore(br *BitReader, zerosLeft int) (int, error) {
	if zerosLeft <= 0 {
		return 0, nil
	}
	tabIdx := zerosLeft - 1
	if tabIdx > 6 {
		tabIdx = 6
	}
	return readVLCFromTable(br, runLen[tabIdx], runBits[tabIdx])
}

// readVLCFromTable reads a value from paired len/bits VLC tables.
// lenTab[value] = number of bits, bitsTab[value] = codeword.
func readVLCFromTable(br *BitReader, lenTab, bitsTab []uint8) (int, error) {
	maxBits := uint(0)
	for _, l := range lenTab {
		if uint(l) > maxBits {
			maxBits = uint(l)
		}
	}
	if maxBits == 0 {
		return 0, errCAVLC
	}

	// Limit to available bits to avoid reading past RBSP end.
	available := uint(len(br.data)*8 - (br.bytePos*8 + int(br.bitPos)))
	if maxBits > available {
		maxBits = available
	}
	if maxBits == 0 {
		return 0, errCAVLC
	}

	savedByte := br.bytePos
	savedBit := br.bitPos

	code, err := br.ReadBits(maxBits)
	if err != nil {
		return 0, err
	}

	for nBits := int(maxBits); nBits >= 1; nBits-- {
		extracted := code >> (maxBits - uint(nBits))
		for val := 0; val < len(lenTab); val++ {
			if int(lenTab[val]) == nBits && uint32(bitsTab[val]) == extracted {
				br.bytePos = savedByte
				br.bitPos = savedBit
				_, _ = br.ReadBits(uint(nBits))
				return val, nil
			}
		}
	}

	return 0, errCAVLC
}

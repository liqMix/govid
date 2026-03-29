package vp8

import (
	"bytes"
	"os"
	"testing"
)

// oldPartition is the Go stdlib VP8 bool codec (eager 1-byte fill).
type oldPartition struct {
	buf           []byte
	r             int
	rangeM1       uint32
	bits          uint32
	nBits         uint8
	unexpectedEOF bool
}

func (p *oldPartition) init(buf []byte) {
	p.buf = buf
	p.r = 0
	p.rangeM1 = 254
	p.bits = 0
	p.nBits = 0
	p.unexpectedEOF = false
}

func (p *oldPartition) readBit(prob uint8) bool {
	if p.nBits < 8 {
		if p.r >= len(p.buf) {
			p.unexpectedEOF = true
			return false
		}
		x := uint32(p.buf[p.r])
		p.bits |= x << (8 - p.nBits)
		p.r++
		p.nBits += 8
	}
	split := (p.rangeM1*uint32(prob))>>8 + 1
	bit := p.bits >= split<<8
	if bit {
		p.rangeM1 -= split
		p.bits -= split << 8
	} else {
		p.rangeM1 = split - 1
	}
	if p.rangeM1 < 127 {
		shift := lutShift[p.rangeM1]
		p.rangeM1 = uint32(lutRangeM1[p.rangeM1])
		p.bits <<= shift
		p.nBits -= shift
	}
	return bit
}

// TestTokenPartitionBoolCompare runs BOTH bool codec implementations through
// the ACTUAL parseResiduals4 call sequence for all MBs in frame 1.
func TestTokenPartitionBoolCompare(t *testing.T) {
	const webmPath = "testdata/interframe64.webm"
	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("not found")
	}

	dm := openTestDemuxer(t, webmPath)
	dec := NewDecoder()

	// Decode keyframe.
	pkt0, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt0.Data), len(pkt0.Data))
	dec.DecodeFrameHeader()
	dec.ensureImg()
	dec.DecodeFrame()

	// Parse frame 1 headers.
	pkt1, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt1.Data), len(pkt1.Data))
	dec.DecodeFrameHeader()
	dec.ensureImg()
	dec.parseOtherHeaders()

	// Get the token partition bytes.
	tokBytes := make([]byte, len(dec.op[0].buf))
	copy(tokBytes, dec.op[0].buf)
	t.Logf("token partition: %d bytes", len(tokBytes))

	// Initialize the old (stdlib) partition on the same bytes.
	var oldP oldPartition
	oldP.init(tokBytes)

	// Initialize our partition on the same bytes.
	var newP partition
	newP.init(tokBytes)

	// Wrap readBit to compare both codecs and count calls.
	callNum := 0
	diverged := false
	readBitBoth := func(prob uint8) bool {
		oldBit := oldP.readBit(prob)
		newBit := newP.readBit(prob)
		if oldBit != newBit && !diverged {
			t.Errorf("DIVERGENCE at readBit #%d (prob=%d): old=%v new=%v", callNum, prob, oldBit, newBit)
			diverged = true
		}
		callNum++
		return newBit
	}

	// Simulate parseResiduals4 using both codecs.
	// Track decoded values for debugging.
	type decodedCoeff struct {
		n int; v int32; z int
	}
	var lastBlockCoeffs []decodedCoeff

	simParseResiduals4 := func(plane int, context uint8, quant [2]uint16, skipFirst bool, coeffBase int) uint8 {
		lastBlockCoeffs = nil
		prob := &dec.tokenProb[plane]
		n := 0
		if skipFirst {
			n = 1
		}
		p := prob[bands[n]][context]
		if !readBitBoth(p[0]) {
			return 0
		}
		for n != 16 {
			n++
			if !readBitBoth(p[1]) {
				p = prob[bands[n]][0]
				continue
			}
			var v uint32
			if !readBitBoth(p[2]) {
				v = 1
				p = prob[bands[n]][1]
			} else {
				if !readBitBoth(p[3]) {
					if !readBitBoth(p[4]) {
						v = 2
					} else {
						v = 3
						if readBitBoth(p[5]) { v++ }
					}
				} else if !readBitBoth(p[6]) {
					if !readBitBoth(p[7]) {
						v = 5
						if readBitBoth(159) { v++ }
					} else {
						v = 7
						if readBitBoth(165) { v += 2 }
						if readBitBoth(145) { v++ }
					}
				} else {
					b1 := uint32(0)
					if readBitBoth(p[8]) { b1 = 1 }
					b0 := uint32(0)
					if readBitBoth(p[9+b1]) { b0 = 1 }
					cat := 2*b1 + b0
					tab := &cat3456[cat]
					v = 0
					for i := 0; tab[i] != 0; i++ {
						v *= 2
						if readBitBoth(tab[i]) { v++ }
					}
					v += 3 + (8 << cat)
				}
				p = prob[bands[n]][2]
			}
			// Sign
			sign := readBitBoth(uniformProb)
			sv := int32(v)
			if sign { sv = -sv }
			z := int(zigzag[n-1])
			lastBlockCoeffs = append(lastBlockCoeffs, decodedCoeff{n: n, v: sv, z: z})
			if n == 16 || !readBitBoth(p[0]) {
				return 1
			}
		}
		return 1
	}

	// Simulate the full parseResiduals for all MBs.
	type mbState struct{ nzMask uint8; nzY16 uint8 }
	leftMB := mbState{}
	upMB := make([]mbState, dec.mbw)
	quant := &dec.quant[0]

	for mby := 0; mby < dec.mbh; mby++ {
		leftMB = mbState{}
		for mbx := 0; mbx < dec.mbw; mbx++ {
			if diverged {
				break
			}
			// Determine mode (from the actual decode).
			// For simplicity, we know: MB(0,3) is SPLIT, others are ZEROMV/NEWMV with usePredY16=true.
			isSplit := mby == 3 && mbx == 0
			isSkip := false
			// Look up skip from the actual decode.
			// For the 64x64 video: MB(1,1),(2,1),(2,2),(1,3),(2,3) are skip.
			switch {
			case mby == 1 && (mbx == 1 || mbx == 2):
				isSkip = true
			case mby == 2 && mbx == 2:
				isSkip = true
			case mby == 3 && mbx >= 1:
				isSkip = true
			}

			if isSkip {
				if !isSplit {
					leftMB.nzY16 = 0
					upMB[mbx].nzY16 = 0
				}
				leftMB.nzMask = 0
				upMB[mbx].nzMask = 0
				continue
			}

			usePredY16 := !isSplit
			plane := planeY1SansY2
			if usePredY16 {
				// Parse Y2 block.
				nz := simParseResiduals4(planeY2, leftMB.nzY16+upMB[mbx].nzY16, quant.y2, false, 0)
				leftMB.nzY16 = nz
				upMB[mbx].nzY16 = nz
				plane = planeY1WithY2
			}

			// Parse 16 Y1 blocks.
			lnz := unpack[leftMB.nzMask&0x0f]
			unz := unpack[upMB[mbx].nzMask&0x0f]
			for y := 0; y < 4; y++ {
				nz := lnz[y]
				for x := 0; x < 4; x++ {
					blkBefore := callNum
					nz = simParseResiduals4(plane, nz+unz[x], quant.y1, usePredY16, 0)
					if isSplit && y == 0 && x < 4 {
						t.Logf("  sim Y1 block(%d,%d) ctx=%d calls=%d nz=%d coeffs=%v",
							x, y, int(nz)+int(unz[x]), callNum-blkBefore, nz, lastBlockCoeffs)
					}
					unz[x] = nz
				}
				lnz[y] = nz
			}
			lnzMask := pack(lnz, 0)
			unzMask := pack(unz, 0)

			// Parse 8 chroma blocks.
			clnz := unpack[leftMB.nzMask>>4]
			cunz := unpack[upMB[mbx].nzMask>>4]
			for c := 0; c < 4; c += 2 {
				for y := 0; y < 2; y++ {
					nz := clnz[y+c]
					for x := 0; x < 2; x++ {
						nz = simParseResiduals4(planeUV, nz+cunz[x+c], quant.uv, false, 0)
						cunz[x+c] = nz
					}
					clnz[y+c] = nz
				}
			}

			leftMB.nzMask = (uint8(pack(clnz, 0)) << 4) | uint8(lnzMask)
			upMB[mbx].nzMask = (uint8(pack(cunz, 0)) << 4) | uint8(unzMask)

			if mby == 3 && mbx == 0 {
				t.Logf("After MB(0,3) sim: readBit calls=%d, diverged=%v", callNum, diverged)
				_ = lastBlockCoeffs // Use the variable
			}
		}
	}

	t.Logf("Total readBit calls: %d, diverged: %v", callNum, diverged)
}

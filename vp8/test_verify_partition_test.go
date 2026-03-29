package vp8

import (
	"bytes"
	"os"
	"testing"
)

// miniReadBit is a standalone VP8 bool decoder for cross-verification.
type miniBoolDec struct {
	buf   []byte
	r     int
	rng   uint32
	value uint32
	count int
}

func (m *miniBoolDec) init(buf []byte) {
	m.buf = buf
	m.r = 0
	m.rng = 255
	m.value = 0
	m.count = -8
	m.fill()
}

func (m *miniBoolDec) fill() {
	shift := uint(24 - (m.count + 8))
	for shift < 32 && m.r < len(m.buf) {
		m.value |= uint32(m.buf[m.r]) << shift
		m.r++
		m.count += 8
		shift -= 8
	}
}

func (m *miniBoolDec) readBit(prob uint8) bool {
	if m.count < 0 {
		m.fill()
	}
	split := 1 + ((m.rng - 1) * uint32(prob) >> 8)
	bigsplit := split << 24
	bit := m.value >= bigsplit
	if bit {
		m.rng -= split
		m.value -= bigsplit
	} else {
		m.rng = split
	}
	idx := m.rng - 1
	if idx < 127 {
		shift := lutShift[idx]
		m.rng = uint32(lutRangeM1[idx]) + 1
		m.value <<= shift
		m.count -= int(shift)
	}
	return bit
}

func (m *miniBoolDec) readUint(prob, n uint8) uint32 {
	var u uint32
	for n > 0 {
		n--
		if m.readBit(prob) {
			u |= 1 << n
		}
	}
	return u
}

func TestVerifyPartitionState(t *testing.T) {
	// Read the 64x64 interframe video's frame 1 first partition and
	// verify our main decoder and mini decoder produce identical states.
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

	// Read frame 1 header to get firstPartLen.
	pkt1, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt1.Data), len(pkt1.Data))
	fh, _ := dec.DecodeFrameHeader()

	// Extract the raw first partition bytes.
	firstPart := make([]byte, fh.FirstPartitionLen)
	dec.r.ReadFull(firstPart)

	t.Logf("First partition: %d bytes", len(firstPart))
	t.Logf("Hex: %x", firstPart)

	// Init both decoders on the same data.
	var main partition
	main.init(firstPart)

	var mini miniBoolDec
	mini.init(firstPart)

	// Verify they agree on the first 100 bool decisions using uniform prob.
	t.Log("Verifying main partition vs mini decoder on first 100 uniform reads:")
	agree := true
	for i := 0; i < 100 && agree; i++ {
		mainBit := main.readBit(128)
		miniBit := mini.readBit(128)
		if mainBit != miniBit {
			t.Errorf("Divergence at decision %d: main=%v mini=%v", i, mainBit, miniBit)
			agree = false
		}
	}
	if agree {
		t.Log("All 100 uniform decisions match")
	}

	// Now re-init and try to parse headers from the mini decoder.
	main.init(firstPart)
	mini.init(firstPart)

	// Parse segment header (should be 1 bit: useSegment=false)
	seg := main.readBit(128)
	segM := mini.readBit(128)
	t.Logf("segment useSegment: main=%v mini=%v", seg, segM)
	
	// Verify state after segment
	t.Logf("After segment: main(r=%d,count=%d,rng=%d,value=%08x) mini(r=%d,count=%d,rng=%d,value=%08x)",
		main.r, main.count, main.rng, main.value,
		mini.r, mini.count, mini.rng, mini.value)

	if main.r != mini.r || main.rng != mini.rng {
		t.Error("State mismatch!")
	} else {
		t.Log("States match perfectly after segment read")
	}
}

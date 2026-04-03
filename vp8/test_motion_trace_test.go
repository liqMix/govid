package vp8

import (
	"bytes"
	"os"
	"testing"
)

func TestMotionTestMBTrace(t *testing.T) {
	const webmPath = "testdata/motion_test.webm"
	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("not found")
	}

	dm := openTestDemuxer(t, webmPath)
	dec := NewDecoder()

	pkt0, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt0.Data), len(pkt0.Data))
	dec.DecodeFrameHeader()
	dec.DecodeFrame()

	pkt1, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt1.Data), len(pkt1.Data))
	dec.DecodeFrameHeader()
	dec.ensureImg()
	dec.parseOtherHeaders()

	headerBits := dec.fp.r*8 - dec.fp.count
	t.Logf("header bits=%d remaining=%d (of %d)", headerBits, int(dec.frameHeader.FirstPartitionLen)*8-headerBits, dec.frameHeader.FirstPartitionLen*8)

	for mbx := 0; mbx < dec.mbw; mbx++ {
		dec.upMB[mbx] = mb{}
		dec.upInterMB[mbx] = interMB{}
	}

	modeNames := []string{"NEAREST", "NEAR", "ZERO", "NEW", "SPLIT"}
	for mby := 0; mby < dec.mbh; mby++ {
		dec.leftMB = mb{}
		dec.leftInterMB = interMB{}
		var prevAbove interMB
		for mbx := 0; mbx < dec.mbw; mbx++ {
			dec.aboveLeftInterMB = prevAbove
			prevAbove = dec.upInterMB[mbx]

			b0 := dec.fp.r*8 - dec.fp.count
			eof0 := dec.fp.unexpectedEOF
			skip := dec.reconstruct(mbx, mby)
			b1 := dec.fp.r*8 - dec.fp.count

			modeName := "INTRA"
			if dec.curRefFrame != 0 && int(dec.curMode) < len(modeNames) {
				modeName = modeNames[dec.curMode]
			}

			bits := b1 - b0
			if modeName != "ZERO" || (!eof0 && dec.fp.unexpectedEOF) {
				t.Logf("MB(%d,%d) %s ref=%d mv=(%d,%d) skip=%v bits=%d eof=%v→%v",
					mbx, mby, modeName, dec.curRefFrame,
					dec.curMV[0], dec.curMV[1], skip, bits, eof0, dec.fp.unexpectedEOF)
			}

			fs := dec.lookupFilterParam()
			fs.inner = fs.inner || !skip
			dec.perMBFilterParams[dec.mbw*mby+mbx] = fs
		}
	}
	t.Logf("final: r=%d count=%d eof=%v", dec.fp.r, dec.fp.count, dec.fp.unexpectedEOF)
}

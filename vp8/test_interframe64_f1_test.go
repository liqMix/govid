package vp8

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func TestInterframe64Frame1Trace(t *testing.T) {
	const webmPath = "testdata/interframe64.webm"
	const refYUV = "testdata/interframe64_frames.yuv"
	const w, h = 64, 64

	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("ref not found")
	}
	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	dm := openTestDemuxer(t, webmPath)
	dec := NewDecoder()

	ySize := w * h
	cSize := (w / 2) * (h / 2)
	frameSize := ySize + 2*cSize

	// Decode frame 0 (key).
	pkt0, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt0.Data), len(pkt0.Data))
	dec.DecodeFrameHeader()
	dec.ensureImg()
	dec.DecodeFrame()

	// Frame 1.
	pkt1, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt1.Data), len(pkt1.Data))
	fh, _ := dec.DecodeFrameHeader()
	dec.ensureImg()
	dec.parseOtherHeaders()

	headerBits := dec.fp.r*8 - dec.fp.count
	t.Logf("frame 1: firstPartLen=%d (%d bits) headerBits=%d remaining=%d",
		fh.FirstPartitionLen, fh.FirstPartitionLen*8, headerBits,
		int(fh.FirstPartitionLen)*8-headerBits)
	t.Logf("probIntra=%d probLast=%d skipProb=%d mvProb[0]=%v", 
		dec.probIntra, dec.probLast, dec.skipProb, dec.mvProb[0])

	for mbx := 0; mbx < dec.mbw; mbx++ {
		dec.upMB[mbx] = mb{}
		dec.upInterMB[mbx] = interMB{}
	}

	modeNames := []string{"NEAREST", "NEAR", "ZERO", "NEW", "SPLIT"}
	refOff := 1 * frameSize

	for mby := 0; mby < dec.mbh; mby++ {
		dec.leftMB = mb{}
		dec.leftInterMB = interMB{}
		var prevAbove interMB
		for mbx := 0; mbx < dec.mbw; mbx++ {
			dec.aboveLeftInterMB = prevAbove
			prevAbove = dec.upInterMB[mbx]

			b0 := dec.fp.r*8 - dec.fp.count
			skip := dec.reconstruct(mbx, mby)
			b1 := dec.fp.r*8 - dec.fp.count

			modeName := "INTRA"
			if dec.curRefFrame != 0 && int(dec.curMode) < len(modeNames) {
				modeName = modeNames[dec.curMode]
			}
			if mby == 3 && mbx == 0 {
				cnt := dec.lastCnt
				t.Logf("  MB(0,3) cnt=%v probs=[%d,%d,%d,%d]",
					cnt,
					modeContexts[min(cnt[0],5)][0],
					modeContexts[min(cnt[1],5)][1],
					modeContexts[min(cnt[2],5)][2],
					modeContexts[min(cnt[3],5)][3])
				t.Logf("  above=%+v left=%+v",
					dec.upInterMB[mbx], dec.leftInterMB)
			}
			_ = mby // prevent unused

			maxErr := 0
			for j := 0; j < 16 && mby*16+j < h; j++ {
				for i := 0; i < 16 && mbx*16+i < w; i++ {
					got := int(dec.img.Y[(mby*16+j)*dec.img.YStride+mbx*16+i])
					want := int(ref[refOff+(mby*16+j)*w+mbx*16+i])
					d := got - want
					if d < 0 { d = -d }
					if d > maxErr { maxErr = d }
				}
			}

			mvStr := fmt.Sprintf("(%d,%d)", dec.curMV[0], dec.curMV[1])
			if dec.curMode == interModeSPLITMV {
				mvStr += " sub=["
				for si := 0; si < 16; si++ {
					if si > 0 { mvStr += "," }
					mvStr += fmt.Sprintf("(%d,%d)", dec.curSubMV[si][0], dec.curSubMV[si][1])
				}
				mvStr += "]"
			}

			fs := dec.computeInterFilterParam(mbx, mby)
			fs.inner = fs.inner || !skip
			dec.perMBFilterParams[dec.mbw*mby+mbx] = fs

			t.Logf("MB(%d,%d) %s ref=%d mv=%s skip=%v bits=%d err=%d eof=%v",
				mbx, mby, modeName, dec.curRefFrame, mvStr, skip, b1-b0, maxErr, dec.fp.unexpectedEOF)
		}
	}
	t.Logf("final: count=%d eof=%v", dec.fp.count, dec.fp.unexpectedEOF)
}

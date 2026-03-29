package vp8

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func TestInterframe64Frame3Trace(t *testing.T) {
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

	// Decode frames 0-2 normally.
	for i := 0; i < 3; i++ {
		pkt, _ := dm.NextPacket()
		dec.Init(bytes.NewReader(pkt.Data), len(pkt.Data))
		dec.DecodeFrameHeader()
		dec.ensureImg()
		dec.DecodeFrame()
	}

	// Frame 3: decode with detailed tracing.
	pkt, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt.Data), len(pkt.Data))
	fh, _ := dec.DecodeFrameHeader()
	dec.ensureImg()
	dec.parseOtherHeaders()

	t.Logf("frame 3: firstPartLen=%d probIntra=%d probLast=%d skipProb=%d",
		fh.FirstPartitionLen, dec.probIntra, dec.probLast, dec.skipProb)

	for mbx := 0; mbx < dec.mbw; mbx++ {
		dec.upMB[mbx] = mb{}
		dec.upInterMB[mbx] = interMB{}
	}

	modeNames := []string{"NEAREST", "NEAR", "ZERO", "NEW", "SPLIT"}
	mbw, mbh := dec.mbw, dec.mbh
	refOff := 3 * frameSize

	for mby := 0; mby < mbh; mby++ {
		dec.leftMB = mb{}
		dec.leftInterMB = interMB{}
		var prevAbove interMB
		for mbx := 0; mbx < mbw; mbx++ {
			dec.aboveLeftInterMB = prevAbove
			prevAbove = dec.upInterMB[mbx]

			skip := dec.reconstruct(mbx, mby)

			modeName := "INTRA"
			if dec.curRefFrame != 0 && int(dec.curMode) < len(modeNames) {
				modeName = modeNames[dec.curMode]
			}

			// Compute per-MB Y error.
			maxErr := 0
			for j := 0; j < 16 && mby*16+j < h; j++ {
				for i := 0; i < 16 && mbx*16+i < w; i++ {
					got := int(dec.img.Y[(mby*16+j)*dec.img.YStride+mbx*16+i])
					want := int(ref[refOff+(mby*16+j)*w+mbx*16+i])
					d := got - want
					if d < 0 {
						d = -d
					}
					if d > maxErr {
						maxErr = d
					}
				}
			}

			// Show subMVs for SPLIT
			mvStr := fmt.Sprintf("(%d,%d)", dec.curMV[0], dec.curMV[1])
			if dec.curMode == interModeSPLITMV {
				mvStr += " sub=["
				for si := 0; si < 16; si++ {
					if si > 0 {
						mvStr += ","
					}
					mvStr += fmt.Sprintf("(%d,%d)", dec.curSubMV[si][0], dec.curSubMV[si][1])
				}
				mvStr += "]"
			}

			fs := dec.computeInterFilterParam(mbx, mby)
			fs.inner = fs.inner || !skip
			dec.perMBFilterParams[dec.mbw*mby+mbx] = fs

			t.Logf("MB(%d,%d) %s ref=%d mv=%s skip=%v err=%d",
				mbx, mby, modeName, dec.curRefFrame, mvStr, skip, maxErr)
		}
	}
	t.Logf("eof=%v count=%d", dec.fp.unexpectedEOF, dec.fp.count)
}

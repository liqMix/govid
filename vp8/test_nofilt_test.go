package vp8

import (
	"bytes"
	"os"
	"testing"
)

func TestInterframe64NoFilter(t *testing.T) {
	const webmPath = "testdata/interframe64.webm"
	const refNF = "testdata/interframe64_nofilt.yuv"
	const refF = "testdata/interframe64_frames.yuv"
	const w, h = 64, 64

	if _, err := os.Stat(refNF); err != nil {
		t.Skip("nofilt ref not found")
	}

	refNoFilt, _ := os.ReadFile(refNF)
	refFilt, _ := os.ReadFile(refF)

	ySize := w * h
	cSize := (w / 2) * (h / 2)
	frameSize := ySize + 2*cSize

	// Decode with our decoder, disabling filter.
	dm := openTestDemuxer(t, webmPath)
	dec := NewDecoder()

	for i := 0; i < 2; i++ {
		pkt, _ := dm.NextPacket()
		dec.Init(bytes.NewReader(pkt.Data), len(pkt.Data))
		dec.DecodeFrameHeader()
		dec.ensureImg()
		// Decode frame but override filter level to 0.
		dec.parseOtherHeaders()
		savedLevel := dec.filterHeader.level
		dec.filterHeader.level = 0  // disable filter

		for mbx := 0; mbx < dec.mbw; mbx++ {
			dec.upMB[mbx] = mb{}
			dec.upInterMB[mbx] = interMB{}
		}
		var prevAbove interMB
		for mby := 0; mby < dec.mbh; mby++ {
			dec.leftMB = mb{}
			dec.leftInterMB = interMB{}
			prevAbove = interMB{}
			for mbx := 0; mbx < dec.mbw; mbx++ {
				dec.aboveLeftInterMB = prevAbove
				prevAbove = dec.upInterMB[mbx]
				skip := dec.reconstruct(mbx, mby)
				if !dec.frameHeader.KeyFrame {
					fs := dec.computeInterFilterParam(mbx, mby)
					fs.inner = fs.inner || !skip
					dec.perMBFilterParams[dec.mbw*mby+mbx] = fs
				} else {
					fs := dec.filterParams[dec.segment][btou(!dec.usePredY16)]
					fs.inner = fs.inner || !skip
					dec.perMBFilterParams[dec.mbw*mby+mbx] = fs
				}
			}
		}
		// No filter applied (level=0).
		dec.updateReferenceFrames()
		dec.filterHeader.level = savedLevel

		if i == 1 {
			refOff := 1 * frameSize
			// Compare against no-filter reference.
			wrongNF, maxNF := 0, 0
			wrongF, maxF := 0, 0
			for j := 0; j < h; j++ {
				for k := 0; k < w; k++ {
					got := int(dec.img.Y[j*dec.img.YStride+k])
					wantNF := int(refNoFilt[refOff+j*w+k])
					wantF := int(refFilt[refOff+j*w+k])
					dNF := got - wantNF
					if dNF < 0 { dNF = -dNF }
					if dNF > 0 { wrongNF++ }
					if dNF > maxNF { maxNF = dNF }
					dF := got - wantF
					if dF < 0 { dF = -dF }
					if dF > 0 { wrongF++ }
					if dF > maxF { maxF = dF }
				}
			}
			t.Logf("frame 1 (no filter) vs nofilt-ref: Y %d/%d wrong, max=%d", wrongNF, ySize, maxNF)
			t.Logf("frame 1 (no filter) vs filt-ref: Y %d/%d wrong, max=%d", wrongF, ySize, maxF)
		}
	}
}

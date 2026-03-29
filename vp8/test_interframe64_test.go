package vp8

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

func TestInterframe64VsRef(t *testing.T) {
	const webmPath = "testdata/interframe64.webm"
	const refYUV = "testdata/interframe64_frames.yuv"
	const numFrames = 5
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

	for i := 0; i < numFrames; i++ {
		pkt, err := dm.NextPacket()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		dec.Init(bytes.NewReader(pkt.Data), len(pkt.Data))
		fh, err := dec.DecodeFrameHeader()
		if err != nil {
			t.Fatal(err)
		}
		dec.ensureImg()
		img, err := dec.DecodeFrame()
		if err != nil {
			t.Fatal(err)
		}

		refOff := i * frameSize
		wrongY, maxY := 0, 0
		mbw := (w + 15) / 16
		mbh := (h + 15) / 16
		mbMaxErr := make([]int, mbw*mbh)
		for j := 0; j < h; j++ {
			for k := 0; k < w; k++ {
				got := int(img.Y[j*img.YStride+k])
				want := int(ref[refOff+j*w+k])
				d := got - want
				if d < 0 {
					d = -d
				}
				if d > 0 {
					wrongY++
				}
				if d > maxY {
					maxY = d
				}
				mbIdx := (j/16)*mbw + k/16
				if d > mbMaxErr[mbIdx] {
					mbMaxErr[mbIdx] = d
				}
			}
		}
		ft := "inter"
		if fh.KeyFrame {
			ft = "key"
		}
		t.Logf("frame %d (%s): Y %d/%d wrong (%.1f%%) max=%d eof=%v firstPartLen=%d",
			i, ft, wrongY, ySize, 100*float64(wrongY)/float64(ySize), maxY,
			dec.fp.unexpectedEOF, fh.FirstPartitionLen)
		if i == 1 {
			// Per-MB error for frame 1
			var line string
			for mby := 0; mby < mbh; mby++ {
				line = ""
				for mbx := 0; mbx < mbw; mbx++ {
					line += fmt.Sprintf(" %3d", mbMaxErr[mby*mbw+mbx])
				}
				t.Logf("  MB row %d:%s", mby, line)
			}
		}
	}
}

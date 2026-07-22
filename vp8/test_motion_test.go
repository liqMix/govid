package vp8

import (
	"bytes"
	"os"
	"testing"
)

func TestMotionTestMultiFrame(t *testing.T) {
	const webmPath = "testdata/motion_test.webm"
	const refYUV = "testdata/motion_test_frames.yuv"
	const numFrames = 5
	const w, h = 160, 120

	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("not found")
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
			t.Fatalf("frame %d header: %v", i, err)
		}
		dec.ensureImg()
		img, err := dec.DecodeFrame()
		if err != nil {
			t.Fatalf("frame %d decode: %v", i, err)
		}

		refOff := i * frameSize
		wrongY, maxY := 0, 0
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
			}
		}
		ft := "inter"
		if fh.KeyFrame {
			ft = "key"
		}
		t.Logf("frame %d (%s): Y %d/%d wrong (%.1f%%) max=%d eof=%v pktLen=%d firstPartLen=%d",
			i, ft, wrongY, ySize, 100*float64(wrongY)/float64(ySize), maxY,
			dec.fp.unexpectedEOF, len(pkt.Data), fh.FirstPartitionLen)
	}
}

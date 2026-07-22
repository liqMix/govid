package vp8

import (
	"bytes"
	"os"
	"testing"

	stdvp8 "golang.org/x/image/vp8"
)

// TestCompareStdlibKeyframe decodes the keyframe with BOTH our decoder and the
// Go stdlib decoder, then compares their internal tokenProb tables.
// The stdlib only supports keyframes but shares the same parseTokenProb code.
// Any difference in tokenProb after keyframe decode reveals a parsing divergence.
func TestCompareStdlibKeyframe(t *testing.T) {
	const webmPath = "testdata/interframe64.webm"
	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("not found")
	}

	dm := openTestDemuxer(t, webmPath)
	pkt0, err := dm.NextPacket()
	if err != nil {
		t.Fatal(err)
	}

	// Decode with OUR decoder.
	ourDec := NewDecoder()
	ourDec.Init(bytes.NewReader(pkt0.Data), len(pkt0.Data))
	ourFH, err := ourDec.DecodeFrameHeader()
	if err != nil {
		t.Fatal(err)
	}
	ourDec.ensureImg()
	_, err = ourDec.DecodeFrame()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Our decoder: %dx%d keyframe=%v", ourFH.Width, ourFH.Height, ourFH.KeyFrame)

	// Decode with STDLIB decoder.
	stdDec := stdvp8.NewDecoder()
	stdDec.Init(bytes.NewReader(pkt0.Data), len(pkt0.Data))
	stdFH, err := stdDec.DecodeFrameHeader()
	if err != nil {
		t.Fatal(err)
	}
	stdImg, err := stdDec.DecodeFrame()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Stdlib decoder: %dx%d keyframe=%v", stdFH.Width, stdFH.Height, stdFH.KeyFrame)

	// Compare output images pixel-by-pixel.
	ourImg := ourDec.img
	wrongY, maxY := 0, 0
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			a := int(ourImg.Y[y*ourImg.YStride+x])
			b := int(stdImg.Y[y*stdImg.YStride+x])
			d := a - b
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
	t.Logf("Our vs Stdlib keyframe: Y %d/4096 wrong, max=%d", wrongY, maxY)

	if wrongY > 0 {
		t.Errorf("Keyframe output DIFFERS between our decoder and stdlib!")
	}
}

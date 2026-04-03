package av1

import (
	"os"
	"testing"
)

func TestXORFirstPixels(t *testing.T) {
	refData, err := os.ReadFile("testdata/baker_frame0.yuv")
	if err != nil {
		t.Skip("Reference not found")
	}
	sample := extractFirstSample(t, "../examples/videos/baker_av1.mp4")
	dec := NewDecoder()
	img, err := dec.DecodePacket(sample)
	if err != nil {
		t.Logf("Error: %v", err)
	}
	if img == nil {
		t.Skip("nil image")
	}
	w := img.Rect.Dx()
	// Print first 8 pixels of rows 0-3
	for r := 0; r < 4 && r < img.Rect.Dy(); r++ {
		got := make([]int, 8)
		ref := make([]int, 8)
		for c := 0; c < 8; c++ {
			got[c] = int(img.Y[r*img.YStride+c])
			ref[c] = int(refData[r*w+c])
		}
		t.Logf("Row %d: got=%v ref=%v", r, got, ref)
	}
}

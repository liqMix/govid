package h264

import (
	"image"
	"testing"
)

func TestMedian3(t *testing.T) {
	tests := []struct {
		a, b, c int16
		want    int16
	}{
		{1, 2, 3, 2},
		{3, 2, 1, 2},
		{1, 3, 2, 2},
		{5, 5, 5, 5},
		{-3, 0, 3, 0},
		{0, -1, 1, 0},
		{10, 20, 15, 15},
	}
	for _, tt := range tests {
		got := median3(tt.a, tt.b, tt.c)
		if got != tt.want {
			t.Errorf("median3(%d, %d, %d) = %d, want %d", tt.a, tt.b, tt.c, got, tt.want)
		}
	}
}

func TestPredictMVNoNeighbors(t *testing.T) {
	d := NewDecoder()
	d.mbw = 2
	d.mbh = 2
	d.img = image.NewYCbCr(image.Rect(0, 0, 32, 32), image.YCbCrSubsampleRatio420)
	d.mbInfo = make([]mbInterInfo, 4)

	// Top-left MB, no left or above neighbors.
	mv := d.predictMVWithSize(0, 0, 0, 0, 16, 16, 0)
	if mv[0] != 0 || mv[1] != 0 {
		t.Errorf("no neighbors: got MV(%d,%d), want (0,0)", mv[0], mv[1])
	}
}

func TestPredictMVSingleNeighbor(t *testing.T) {
	d := NewDecoder()
	d.mbw = 2
	d.mbh = 2
	d.img = image.NewYCbCr(image.Rect(0, 0, 32, 32), image.YCbCrSubsampleRatio420)
	d.mbInfo = make([]mbInterInfo, 4)

	// Set left MB (0,0) with a known MV.
	d.mbInfo[0].isIntra = false
	d.mbInfo[0].predMask = [4]uint8{1, 1, 1, 1}
	for k := 0; k < 16; k++ {
		d.mbInfo[0].mv[k] = [2]int16{8, 4}
	}
	for k := 0; k < 4; k++ {
		d.mbInfo[0].refIdx[k] = 0
	}

	// Predict for MB (1,0) — only left neighbor available.
	mv := d.predictMVWithSize(1, 0, 0, 0, 16, 16, 0)
	// With only A available (B and C unavailable), should use A directly.
	if mv[0] != 8 || mv[1] != 4 {
		t.Errorf("single left neighbor: got MV(%d,%d), want (8,4)", mv[0], mv[1])
	}
}

func TestPredictMVMedian(t *testing.T) {
	d := NewDecoder()
	d.mbw = 3
	d.mbh = 2
	d.img = image.NewYCbCr(image.Rect(0, 0, 48, 32), image.YCbCrSubsampleRatio420)
	d.mbInfo = make([]mbInterInfo, 6)

	// Set left MB (0,1).
	d.mbInfo[3].isIntra = false
	d.mbInfo[3].predMask = [4]uint8{1, 1, 1, 1}
	for k := 0; k < 16; k++ {
		d.mbInfo[3].mv[k] = [2]int16{4, 2}
	}
	for k := 0; k < 4; k++ {
		d.mbInfo[3].refIdx[k] = 0
	}

	// Set above MB (1,0).
	d.mbInfo[1].isIntra = false
	d.mbInfo[1].predMask = [4]uint8{1, 1, 1, 1}
	for k := 0; k < 16; k++ {
		d.mbInfo[1].mv[k] = [2]int16{12, 8}
	}
	for k := 0; k < 4; k++ {
		d.mbInfo[1].refIdx[k] = 0
	}

	// Set above-right MB (2,0).
	d.mbInfo[2].isIntra = false
	d.mbInfo[2].predMask = [4]uint8{1, 1, 1, 1}
	for k := 0; k < 16; k++ {
		d.mbInfo[2].mv[k] = [2]int16{6, 10}
	}
	for k := 0; k < 4; k++ {
		d.mbInfo[2].refIdx[k] = 0
	}

	// Predict for MB (1,1) — all three neighbors available.
	mv := d.predictMVWithSize(1, 1, 0, 0, 16, 16, 0)
	// Median of (4,12,6) = 6, median of (2,8,10) = 8.
	if mv[0] != 6 || mv[1] != 8 {
		t.Errorf("median: got MV(%d,%d), want (6,8)", mv[0], mv[1])
	}
}

func TestPredictMVIntraNeighbor(t *testing.T) {
	d := NewDecoder()
	d.mbw = 2
	d.mbh = 2
	d.img = image.NewYCbCr(image.Rect(0, 0, 32, 32), image.YCbCrSubsampleRatio420)
	d.mbInfo = make([]mbInterInfo, 4)

	// Left MB is intra — contributes MV (0,0) with ref=0.
	d.mbInfo[0].isIntra = true

	// Above MB (1,0) not available (mby=0 for target).
	// So only A is available as intra (MV=0, ref=0).
	mv := d.predictMVWithSize(1, 0, 0, 0, 16, 16, 0)
	if mv[0] != 0 || mv[1] != 0 {
		t.Errorf("intra neighbor: got MV(%d,%d), want (0,0)", mv[0], mv[1])
	}
}

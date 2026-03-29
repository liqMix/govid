package av1

import (
	"image"
	"testing"
)

func TestReconstructSingleBlock(t *testing.T) {
	// Create a minimal decoder with a 16x16 frame.
	// Use DC prediction with zero residual (skip=true).
	// All reference samples are 128 (at edges), so DC should predict 128.
	w, h := 16, 16
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)

	d := &Decoder{
		seqHdr: &SequenceHeader{
			SuperblockSize:       64,
			Use128x128Superblock: false,
			Color: ColorConfig{
				NumPlanes: 3,
				BitDepth:  8,
			},
		},
		frameHdr: &FrameHeader{
			FrameWidth:   w,
			FrameHeight:  h,
			MICols:       w / 4,
			MIRows:       h / 4,
			SBCols:       1,
			SBRows:       1,
			FrameType:    FrameTypeKeyFrame,
			FrameIsIntra: true,
			TXMode:       txModeLargest,
			ReducedTXSet: true,
			Quant: QuantizationParams{
				BaseQIdx: 100,
			},
		},
		img: img,
	}
	d.updateDimensions()

	td := &tileDecoder{
		dec:          d,
		cdf:          NewCDFTables(),
		miRowStart:   0,
		miRowEnd:     d.miRows,
		miColStart:   0,
		miColEnd:     d.miCols,
		abovePartCtx: make([]int, d.miCols),
		aboveModeCtx: make([]int, d.miCols),
		aboveSkipCtx: make([]bool, d.miCols),
		leftPartCtx:  make([]int, d.miRows),
		leftModeCtx:  make([]int, d.miRows),
		leftSkipCtx:  make([]bool, d.miRows),
	}
	for p := 0; p < 3; p++ {
		if p == 0 {
			td.aboveNzCtx[p] = make([]bool, d.miCols)
			td.leftNzCtx[p] = make([]bool, d.miRows)
			td.aboveDCSign[p] = make([]int, d.miCols)
			td.leftDCSign[p] = make([]int, d.miRows)
		} else {
			td.aboveNzCtx[p] = make([]bool, (d.miCols+1)/2)
			td.leftNzCtx[p] = make([]bool, (d.miRows+1)/2)
			td.aboveDCSign[p] = make([]int, (d.miCols+1)/2)
			td.leftDCSign[p] = make([]int, (d.miRows+1)/2)
		}
	}

	// Create a skip block info with DC prediction.
	bi := &blockInfo{
		yMode:  IntraDC,
		uvMode: IntraDC,
		txSize: TX16x16,
		txType: TxTypeDCT_DCT,
		skip:   true,
	}

	// Reconstruct Y block at (0,0) — should be DC=128 since no neighbors.
	err := td.reconstructTXB(0, 0, 0, TX16x16, IntraDC, bi)
	if err != nil {
		t.Fatal(err)
	}

	// Verify Y plane has uniform 128 values.
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			got := img.Y[r*img.YStride+c]
			if got != 128 {
				t.Errorf("Y[%d,%d] = %d, want 128", r, c, got)
			}
		}
	}
}

func TestReconstructVerticalPrediction(t *testing.T) {
	// Create an 8x8 frame. Write known above samples, then predict with vertical mode.
	w, h := 8, 8
	img := image.NewYCbCr(image.Rect(0, 0, w, h+4), image.YCbCrSubsampleRatio420)

	// Set row 3 (pixY=3) to a pattern — these will be "above" for a block starting at pixY=4.
	for c := 0; c < w; c++ {
		img.Y[3*img.YStride+c] = uint8(100 + c)
	}

	d := &Decoder{
		seqHdr: &SequenceHeader{
			SuperblockSize:       64,
			Use128x128Superblock: false,
		},
		frameHdr: &FrameHeader{
			FrameWidth:   w,
			FrameHeight:  h + 4,
			MICols:       (w + 3) / 4,
			MIRows:       (h + 4 + 3) / 4,
			SBCols:       1,
			SBRows:       1,
			FrameType:    FrameTypeKeyFrame,
			FrameIsIntra: true,
			TXMode:       txModeLargest,
			ReducedTXSet: true,
			Quant: QuantizationParams{
				BaseQIdx: 50,
			},
		},
		img: img,
	}
	d.updateDimensions()

	td := &tileDecoder{
		dec:          d,
		cdf:          NewCDFTables(),
		miRowStart:   0,
		miRowEnd:     d.miRows,
		miColStart:   0,
		miColEnd:     d.miCols,
		abovePartCtx: make([]int, d.miCols),
		aboveModeCtx: make([]int, d.miCols),
		aboveSkipCtx: make([]bool, d.miCols),
		leftPartCtx:  make([]int, d.miRows),
		leftModeCtx:  make([]int, d.miRows),
		leftSkipCtx:  make([]bool, d.miRows),
	}
	for p := 0; p < 3; p++ {
		td.aboveNzCtx[p] = make([]bool, d.miCols)
		td.leftNzCtx[p] = make([]bool, d.miRows)
		td.aboveDCSign[p] = make([]int, d.miCols)
		td.leftDCSign[p] = make([]int, d.miRows)
	}

	bi := &blockInfo{
		yMode:  IntraVertical,
		uvMode: IntraDC,
		txSize: TX4x4,
		txType: TxTypeDCT_DCT,
		skip:   true,
	}

	// Reconstruct at pixY=4, pixX=0 — vertical should copy row 3.
	err := td.reconstructTXB(0, 0, 4, TX4x4, IntraVertical, bi)
	if err != nil {
		t.Fatal(err)
	}

	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			got := img.Y[(4+r)*img.YStride+c]
			want := uint8(100 + c)
			if got != want {
				t.Errorf("Y[%d,%d] = %d, want %d", 4+r, c, got, want)
			}
		}
	}
}

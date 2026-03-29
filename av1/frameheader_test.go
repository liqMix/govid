package av1

import (
	"testing"
)

func TestParseFrameHeaderKeyframe(t *testing.T) {
	sample := extractFirstSample(t, "testdata/idr_only.mp4")

	// First parse the sequence header.
	shData := findSequenceHeaderOBU(t, sample)
	sh, err := ParseSequenceHeader(shData)
	if err != nil {
		t.Fatal(err)
	}

	// Find the frame header or frame OBU.
	obus, err := ParseOBUs(sample)
	if err != nil {
		t.Fatal(err)
	}

	var frameData []byte
	var isFrameOBU bool
	for _, obu := range obus {
		if obu.Header.Type == OBUFrame {
			frameData = obu.Data
			isFrameOBU = true
			break
		}
		if obu.Header.Type == OBUFrameHeader {
			frameData = obu.Data
			break
		}
	}
	if frameData == nil {
		t.Fatal("no frame header or frame OBU found")
	}

	fh, bitsRead, err := ParseFrameHeader(frameData, sh)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("Frame header parsed: %d bits, isFrameOBU=%v", bitsRead, isFrameOBU)
	t.Logf("  FrameType=%d (KEY_FRAME=%d)", fh.FrameType, FrameTypeKeyFrame)
	t.Logf("  ShowFrame=%v, FrameIsIntra=%v", fh.ShowFrame, fh.FrameIsIntra)
	t.Logf("  FrameSize=%dx%d, RenderSize=%dx%d",
		fh.FrameWidth, fh.FrameHeight, fh.RenderWidth, fh.RenderHeight)
	t.Logf("  Tiles: %dx%d (cols x rows), uniform=%v",
		fh.Tile.TileCols, fh.Tile.TileRows, fh.Tile.UniformTileSpacing)
	t.Logf("  Quant: base_q_idx=%d, DeltaQYDc=%d",
		fh.Quant.BaseQIdx, fh.Quant.DeltaQYDc)
	t.Logf("  MIGrid: %dx%d, SBGrid: %dx%d",
		fh.MICols, fh.MIRows, fh.SBCols, fh.SBRows)
	t.Logf("  TXMode=%d, ReducedTXSet=%v", fh.TXMode, fh.ReducedTXSet)
	t.Logf("  LoopFilter levels=[%d,%d,%d,%d], sharpness=%d",
		fh.LoopFilter.FilterLevel[0], fh.LoopFilter.FilterLevel[1],
		fh.LoopFilter.FilterLevel[2], fh.LoopFilter.FilterLevel[3],
		fh.LoopFilter.SharpnessLevel)
	t.Logf("  CDEF bits=%d, damping=%d", fh.CDEF.Bits, fh.CDEF.DampingMinus3+3)
	t.Logf("  Segmentation enabled=%v", fh.Segmentation.Enabled)
	t.Logf("  CodedLossless=%v, AllLossless=%v", fh.CodedLossless, fh.AllLossless)

	// Verify this is a keyframe.
	if fh.FrameType != FrameTypeKeyFrame {
		t.Errorf("FrameType = %d, want %d (KEY_FRAME)", fh.FrameType, FrameTypeKeyFrame)
	}
	if !fh.FrameIsIntra {
		t.Error("FrameIsIntra = false, want true")
	}
	if !fh.ShowFrame {
		t.Error("ShowFrame = false, want true")
	}

	// Verify dimensions match sequence header.
	if fh.FrameWidth != 160 {
		t.Errorf("FrameWidth = %d, want 160", fh.FrameWidth)
	}
	if fh.FrameHeight != 120 {
		t.Errorf("FrameHeight = %d, want 120", fh.FrameHeight)
	}

	// Verify tile info is reasonable.
	if fh.Tile.TileCols < 1 {
		t.Errorf("TileCols = %d, want >= 1", fh.Tile.TileCols)
	}
	if fh.Tile.TileRows < 1 {
		t.Errorf("TileRows = %d, want >= 1", fh.Tile.TileRows)
	}

	// Verify quant params are valid.
	if fh.Quant.BaseQIdx < 0 || fh.Quant.BaseQIdx > 255 {
		t.Errorf("BaseQIdx = %d, out of range [0,255]", fh.Quant.BaseQIdx)
	}

	// Verify MI dimensions.
	expectedMICols := 2 * ((160 + 7) >> 3)
	expectedMIRows := 2 * ((120 + 7) >> 3)
	if fh.MICols != expectedMICols {
		t.Errorf("MICols = %d, want %d", fh.MICols, expectedMICols)
	}
	if fh.MIRows != expectedMIRows {
		t.Errorf("MIRows = %d, want %d", fh.MIRows, expectedMIRows)
	}
}

func TestParseFrameHeaderInterFrame(t *testing.T) {
	// Parse the test.mp4 which has inter frames.
	// We need to get the second sample which should be an inter frame.
	sample := extractFirstSample(t, "testdata/test.mp4")

	// Parse sequence header.
	shData := findSequenceHeaderOBU(t, sample)
	sh, err := ParseSequenceHeader(shData)
	if err != nil {
		t.Fatal(err)
	}

	// The first frame should be a keyframe.
	obus, err := ParseOBUs(sample)
	if err != nil {
		t.Fatal(err)
	}

	var frameData []byte
	for _, obu := range obus {
		if obu.Header.Type == OBUFrame || obu.Header.Type == OBUFrameHeader {
			frameData = obu.Data
			break
		}
	}
	if frameData == nil {
		t.Fatal("no frame OBU found")
	}

	fh, _, err := ParseFrameHeader(frameData, sh)
	if err != nil {
		t.Fatal(err)
	}

	// First frame should be a keyframe.
	if fh.FrameType != FrameTypeKeyFrame {
		t.Errorf("first frame type = %d, want KEY_FRAME", fh.FrameType)
	}

	t.Logf("First frame: type=%d, %dx%d, tiles=%dx%d, base_q=%d",
		fh.FrameType, fh.FrameWidth, fh.FrameHeight,
		fh.Tile.TileCols, fh.Tile.TileRows, fh.Quant.BaseQIdx)
}

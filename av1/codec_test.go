package av1

import (
	"os"
	"testing"

	govidmp4 "github.com/Eyevinn/mp4ff/mp4"
)

func TestDecodeFirstKeyframe(t *testing.T) {
	sample := extractFirstSample(t, "testdata/idr_only.mp4")

	dec := NewDecoder()
	img, err := dec.DecodePacket(sample)
	if err != nil {
		t.Fatalf("DecodePacket: %v", err)
	}
	if img == nil {
		t.Fatal("DecodePacket returned nil image")
	}

	if img.Rect.Dx() != 160 || img.Rect.Dy() != 120 {
		t.Errorf("frame size = %dx%d, want 160x120", img.Rect.Dx(), img.Rect.Dy())
	}

	// Verify Y plane is not all zeros (green screen check).
	allZero := true
	nonZeroCount := 0
	for _, v := range img.Y {
		if v != 0 {
			allZero = false
			nonZeroCount++
		}
	}

	if allZero {
		t.Error("Y plane is all zeros — green screen not fixed")
	}

	t.Logf("Y plane: %d/%d non-zero pixels (%.1f%%)",
		nonZeroCount, len(img.Y), 100*float64(nonZeroCount)/float64(len(img.Y)))

	// Check that Cb/Cr are also not all zeros.
	cbNonZero := 0
	for _, v := range img.Cb {
		if v != 0 {
			cbNonZero++
		}
	}
	crNonZero := 0
	for _, v := range img.Cr {
		if v != 0 {
			crNonZero++
		}
	}
	t.Logf("Cb non-zero: %d/%d, Cr non-zero: %d/%d",
		cbNonZero, len(img.Cb), crNonZero, len(img.Cr))
}

func TestDecodeKeyframeVsReference(t *testing.T) {
	// Decode the first keyframe.
	sample := extractFirstSample(t, "testdata/idr_only.mp4")

	dec := NewDecoder()
	img, err := dec.DecodePacket(sample)
	if err != nil {
		t.Fatalf("DecodePacket: %v", err)
	}
	if img == nil {
		t.Fatal("DecodePacket returned nil image")
	}

	// Load reference YUV.
	refData, err := os.ReadFile("testdata/frame0.yuv")
	if err != nil {
		t.Skipf("Reference YUV not found: %v", err)
	}

	w := 160
	h := 120
	ySize := w * h
	cbSize := (w / 2) * (h / 2)

	if len(refData) < ySize+2*cbSize {
		t.Fatalf("Reference YUV too small: %d bytes, need %d", len(refData), ySize+2*cbSize)
	}

	refY := refData[:ySize]
	refCb := refData[ySize : ySize+cbSize]
	refCr := refData[ySize+cbSize : ySize+2*cbSize]

	// Compare Y plane.
	yErrors := 0
	yTotalDiff := 0
	yMaxDiff := 0
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			got := int(img.Y[r*img.YStride+c])
			want := int(refY[r*w+c])
			diff := got - want
			if diff < 0 {
				diff = -diff
			}
			if diff > 0 {
				yErrors++
				yTotalDiff += diff
				if diff > yMaxDiff {
					yMaxDiff = diff
				}
			}
		}
	}

	totalPixels := w * h
	yErrorPct := 100.0 * float64(yErrors) / float64(totalPixels)
	t.Logf("Y plane: %d/%d pixels differ (%.1f%%), max diff=%d",
		yErrors, totalPixels, yErrorPct, yMaxDiff)
	if yErrors > 0 {
		t.Logf("Y mean absolute error: %.2f", float64(yTotalDiff)/float64(totalPixels))
	}

	// Compare Cb plane.
	cbErrors := 0
	for r := 0; r < h/2; r++ {
		for c := 0; c < w/2; c++ {
			got := int(img.Cb[r*img.CStride+c])
			want := int(refCb[r*(w/2)+c])
			diff := got - want
			if diff < 0 {
				diff = -diff
			}
			if diff > 0 {
				cbErrors++
			}
		}
	}
	t.Logf("Cb plane: %d/%d pixels differ", cbErrors, cbSize)

	// Compare Cr plane.
	crErrors := 0
	for r := 0; r < h/2; r++ {
		for c := 0; c < w/2; c++ {
			got := int(img.Cr[r*img.CStride+c])
			want := int(refCr[r*(w/2)+c])
			diff := got - want
			if diff < 0 {
				diff = -diff
			}
			if diff > 0 {
				crErrors++
			}
		}
	}
	t.Logf("Cr plane: %d/%d pixels differ", crErrors, cbSize)
}

func TestDecodeBakerAV1Keyframe(t *testing.T) {
	sample := extractFirstSample(t, "../examples/videos/baker_av1.mp4")

	// Parse OBUs to inspect the frame.
	obus, err := ParseOBUs(sample)
	if err != nil {
		t.Fatal(err)
	}

	for i, obu := range obus {
		t.Logf("OBU[%d]: type=%s size=%d", i, OBUTypeName(obu.Header.Type), len(obu.Data))
	}

	// Parse sequence header.
	shData := findSequenceHeaderOBU(t, sample)
	sh, err := ParseSequenceHeader(shData)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Sequence: profile=%d, %dx%d, %d-bit, sb=%d",
		sh.Profile, sh.MaxFrameWidth, sh.MaxFrameHeight, sh.Color.BitDepth, sh.SuperblockSize)

	// Find and parse the frame header.
	for _, obu := range obus {
		if obu.Header.Type == OBUFrame {
			fh, headerBits, err := ParseFrameHeader(obu.Data, sh)
			if err != nil {
				t.Fatal(err)
			}
			headerBytes := (headerBits + 7) / 8
			tileDataLen := len(obu.Data) - headerBytes
			t.Logf("FrameHeader: %d bits (%d bytes), tile data: %d bytes",
				headerBits, headerBytes, tileDataLen)
			t.Logf("  Type=%d, %dx%d, Tiles=%dx%d, TileSizeBytes=%d",
				fh.FrameType, fh.FrameWidth, fh.FrameHeight,
				fh.Tile.TileCols, fh.Tile.TileRows, fh.Tile.TileSizeBytes)
			t.Logf("  Quant: base_q=%d, TXMode=%d, ReducedTXSet=%v",
				fh.Quant.BaseQIdx, fh.TXMode, fh.ReducedTXSet)
			t.Logf("  Segmentation: enabled=%v", fh.Segmentation.Enabled)
			t.Logf("  TileWidthSB=%v, TileHeightSB=%v",
				fh.Tile.TileWidthSB, fh.Tile.TileHeightSB)
			t.Logf("  MIGrid: %dx%d, SBGrid: %dx%d",
				fh.MICols, fh.MIRows, fh.SBCols, fh.SBRows)

			// Show first 32 bytes of tile data for debugging.
			td := obu.Data[headerBytes:]
			show := 32
			if show > len(td) {
				show = len(td)
			}
			t.Logf("  Tile data first %d bytes: %x", show, td[:show])
			break
		}
	}

	// Find and log CDEF/DeltaQ/DeltaLF/FilterIntra details.
	for _, obu := range obus {
		if obu.Header.Type == OBUFrame {
			fh, _, err := ParseFrameHeader(obu.Data, sh)
			if err != nil {
				break
			}
			t.Logf("  CDEF.Bits=%d, DeltaQPresent=%v, DeltaLFPresent=%v",
				fh.CDEF.Bits, fh.DeltaQPresent, fh.DeltaLFPresent)
			t.Logf("  AllowScreenContentTools=%v, AllowIntraBC=%v",
				fh.AllowScreenContentTools, fh.AllowIntraBC)
			t.Logf("  EnableFilterIntra=%v, EnableCDEF=%v",
				sh.EnableFilterIntra, sh.EnableCDEF)
			break
		}
	}

	// Try decoding.
	dec := NewDecoder()
	img, err := dec.DecodePacket(sample)
	if err != nil {
		t.Logf("DecodePacket: %v", err)
	}
	if img == nil {
		t.Fatal("nil image")
	}

	nonZero := 0
	non128 := 0
	for _, v := range img.Y {
		if v != 0 {
			nonZero++
		}
		if v != 128 {
			non128++
		}
	}
	t.Logf("Y plane: %d/%d non-zero (%.1f%%), %d/%d non-128 (%.1f%%)",
		nonZero, len(img.Y), 100*float64(nonZero)/float64(len(img.Y)),
		non128, len(img.Y), 100*float64(non128)/float64(len(img.Y)))
}

func TestDecodeBakerVsReference(t *testing.T) {
	refData, err := os.ReadFile("testdata/baker_frame0.yuv")
	if err != nil {
		t.Skipf("Reference YUV not found: %v", err)
	}

	sample := extractFirstSample(t, "../examples/videos/baker_av1.mp4")
	dec := NewDecoder()
	img, err := dec.DecodePacket(sample)
	if err != nil {
		t.Logf("DecodePacket error (partial decode): %v", err)
	}
	if img == nil {
		t.Skip("nil image, cannot compare")
	}

	w := img.Rect.Dx()
	h := img.Rect.Dy()
	ySize := w * h
	cbSize := (w / 2) * (h / 2)

	if len(refData) < ySize+2*cbSize {
		t.Fatalf("Reference YUV too small: %d bytes, need %d", len(refData), ySize+2*cbSize)
	}

	refY := refData[:ySize]
	refCb := refData[ySize : ySize+cbSize]
	refCr := refData[ySize+cbSize : ySize+2*cbSize]

	// Check Y plane is not all grey (128).
	allGrey := true
	for _, v := range img.Y[:ySize] {
		if v != 128 {
			allGrey = false
			break
		}
	}
	if allGrey {
		t.Error("Y plane is all grey (128) — bitstream reads still desynced")
	}

	// Compare Y plane.
	yErrors := 0
	yTotalDiff := 0
	yMaxDiff := 0
	firstErrX, firstErrY := -1, -1
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			got := int(img.Y[r*img.YStride+c])
			want := int(refY[r*w+c])
			diff := got - want
			if diff < 0 {
				diff = -diff
			}
			if diff > 0 {
				yErrors++
				yTotalDiff += diff
				if diff > yMaxDiff {
					yMaxDiff = diff
				}
				if firstErrX < 0 {
					firstErrX = c
					firstErrY = r
				}
			}
		}
	}

	totalPixels := w * h
	yErrorPct := 100.0 * float64(yErrors) / float64(totalPixels)
	t.Logf("Y plane: %d/%d pixels differ (%.1f%%), max diff=%d",
		yErrors, totalPixels, yErrorPct, yMaxDiff)
	if yErrors > 0 {
		t.Logf("Y mean absolute error: %.2f", float64(yTotalDiff)/float64(totalPixels))
		t.Logf("First error at (%d, %d)", firstErrX, firstErrY)
	}

	// Per-SB max error map (first 4 rows of SBs).
	sbSize := 64
	sbCols := (w + sbSize - 1) / sbSize
	maxSBRows := 4
	if maxSBRows > (h+sbSize-1)/sbSize {
		maxSBRows = (h + sbSize - 1) / sbSize
	}
	for sbR := 0; sbR < maxSBRows; sbR++ {
		row := ""
		for sbC := 0; sbC < sbCols; sbC++ {
			maxD := 0
			for r := sbR * sbSize; r < (sbR+1)*sbSize && r < h; r++ {
				for c := sbC * sbSize; c < (sbC+1)*sbSize && c < w; c++ {
					got := int(img.Y[r*img.YStride+c])
					want := int(refY[r*w+c])
					d := got - want
					if d < 0 {
						d = -d
					}
					if d > maxD {
						maxD = d
					}
				}
			}
			if maxD == 0 {
				row += " ."
			} else if maxD < 10 {
				row += " " + string(rune('0'+maxD))
			} else {
				row += " X"
			}
		}
		t.Logf("SB row %d:%s", sbR, row)
	}

	// Compare Cb plane.
	cbErrors := 0
	for r := 0; r < h/2; r++ {
		for c := 0; c < w/2; c++ {
			got := int(img.Cb[r*img.CStride+c])
			want := int(refCb[r*(w/2)+c])
			diff := got - want
			if diff < 0 {
				diff = -diff
			}
			if diff > 0 {
				cbErrors++
			}
		}
	}
	t.Logf("Cb plane: %d/%d pixels differ (%.1f%%)",
		cbErrors, cbSize, 100.0*float64(cbErrors)/float64(cbSize))

	// Compare Cr plane.
	crErrors := 0
	for r := 0; r < h/2; r++ {
		for c := 0; c < w/2; c++ {
			got := int(img.Cr[r*img.CStride+c])
			want := int(refCr[r*(w/2)+c])
			diff := got - want
			if diff < 0 {
				diff = -diff
			}
			if diff > 0 {
				crErrors++
			}
		}
	}
	t.Logf("Cr plane: %d/%d pixels differ (%.1f%%)",
		crErrors, cbSize, 100.0*float64(crErrors)/float64(cbSize))
}

func TestSpecCoeffCDFsLoaded(t *testing.T) {
	// Verify that NewCDFTables loads spec-correct coefficient CDFs (not uniform).
	cdf := NewCDFTables(50) // baseQIdx=50, same as baker_av1.mp4

	// TXBSkip spec value for [0][0] is makeCDF(30371) = [32768-30371, 0, 0] = [2397, 0, 0].
	// Uniform would be makeCDF(16384) = [16384, 0, 0].
	if cdf.TXBSkip[0][0][0] == 32768-16384 {
		t.Error("TXBSkip[0][0] is uniform (16384) — spec CDFs not loaded")
	}
	if cdf.TXBSkip[0][0][0] != 32768-30371 {
		t.Errorf("TXBSkip[0][0][0] = %d, want %d (32768-30371)", cdf.TXBSkip[0][0][0], 32768-30371)
	}

	// DCSign spec values should not be uniform (16384).
	if cdf.DCSign[0][0][0] == 32768-16384 {
		t.Error("DCSign[0][0] is uniform — spec CDFs not loaded")
	}

	// EOBMulti16 spec value for luma should differ from uniform.
	if cdf.EOBMulti16[0][0] == 32768-8192 {
		t.Error("EOBMulti16[0] is uniform — spec CDFs not loaded")
	}
}

func TestDecodeBakerMultiframe(t *testing.T) {
	refData, err := os.ReadFile("testdata/baker_frames.yuv")
	if err != nil {
		t.Skipf("Reference YUV not found: %v", err)
	}

	f, err := os.Open("../examples/videos/baker_av1.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	file, err := govidmp4.DecodeFile(f)
	if err != nil {
		t.Fatal(err)
	}

	var vt *govidmp4.TrakBox
	for _, trak := range file.Moov.Traks {
		if trak.Mdia != nil && trak.Mdia.Hdlr != nil && trak.Mdia.Hdlr.HandlerType == "vide" {
			vt = trak
			break
		}
	}
	if vt == nil {
		t.Fatal("no video track")
	}

	dec := NewDecoder()
	w, h := 1280, 720
	frameSize := w*h + 2*(w/2)*(h/2)
	numFrames := len(refData) / frameSize
	if numFrames > 10 {
		numFrames = 10
	}

	for i := 0; i < numFrames; i++ {
		ranges, err := vt.GetRangesForSampleInterval(uint32(i+1), uint32(i+1))
		if err != nil {
			t.Fatalf("frame %d: get range: %v", i, err)
		}
		dr := ranges[0]
		if _, err := f.Seek(int64(dr.Offset), 0); err != nil {
			t.Fatal(err)
		}
		sample := make([]byte, dr.Size)
		if _, err := f.Read(sample); err != nil {
			t.Fatal(err)
		}

		img, err := dec.DecodePacket(sample)
		if err != nil {
			t.Logf("frame %d: DecodePacket: %v", i, err)
		}
		if img == nil {
			t.Logf("frame %d: nil image (not displayable)", i)
			continue
		}

		refFrame := refData[i*frameSize : i*frameSize+w*h]
		yErrors := 0
		yMaxDiff := 0
		yTotalDiff := 0
		for r := 0; r < h; r++ {
			for c := 0; c < w; c++ {
				got := int(img.Y[r*img.YStride+c])
				want := int(refFrame[r*w+c])
				diff := got - want
				if diff < 0 {
					diff = -diff
				}
				if diff > 0 {
					yErrors++
					yTotalDiff += diff
					if diff > yMaxDiff {
						yMaxDiff = diff
					}
				}
			}
		}
		totalPixels := w * h
		t.Logf("frame %d: Y errors=%d (%.1f%%), max=%d, mean=%.2f",
			i, yErrors, 100.0*float64(yErrors)/float64(totalPixels),
			yMaxDiff, float64(yTotalDiff)/float64(totalPixels))
	}
}

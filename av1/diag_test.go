package av1

import (
	"fmt"
	"os"
	"testing"
)

// partitionTypeName returns a human-readable name for a partition type constant.
func partitionTypeName(p int) string {
	switch p {
	case PartitionNone:
		return "NONE"
	case PartitionHorz:
		return "HORZ"
	case PartitionVert:
		return "VERT"
	case PartitionSplit:
		return "SPLIT"
	case PartitionHorzA:
		return "HORZ_A"
	case PartitionHorzB:
		return "HORZ_B"
	case PartitionVertA:
		return "VERT_A"
	case PartitionVertB:
		return "VERT_B"
	case PartitionHorz4:
		return "HORZ_4"
	case PartitionVert4:
		return "VERT_4"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", p)
	}
}

func TestDiagBakerFirstBlock(t *testing.T) {
	// --- Step 1: Decode the baker AV1 keyframe ---
	sample := extractFirstSample(t, "../examples/videos/baker_av1.mp4")

	dec := NewDecoder()
	img, err := dec.DecodePacket(sample)
	if err != nil {
		t.Logf("DecodePacket error (partial decode ok): %v", err)
	}
	if img == nil {
		t.Fatal("DecodePacket returned nil image")
	}

	w := img.Rect.Dx()
	h := img.Rect.Dy()
	t.Logf("Decoded frame: %dx%d, YStride=%d, CStride=%d", w, h, img.YStride, img.CStride)

	// --- Step 2: Load reference YUV ---
	refData, err := os.ReadFile("testdata/baker_frame0.yuv")
	if err != nil {
		t.Skipf("Reference YUV not found: %v", err)
	}

	ySize := w * h
	cbSize := (w / 2) * (h / 2)
	if len(refData) < ySize+2*cbSize {
		t.Fatalf("Reference YUV too small: %d bytes, need %d", len(refData), ySize+2*cbSize)
	}
	refY := refData[:ySize]

	// --- Step 3: Compare first 8x8 block of Y plane ---
	t.Log("=== First 8x8 block: GOT vs REF (Y plane) ===")
	blockRows := 8
	blockCols := 8
	if blockRows > h {
		blockRows = h
	}
	if blockCols > w {
		blockCols = w
	}

	diffCount := 0
	maxDiff := 0
	for r := 0; r < blockRows; r++ {
		gotLine := ""
		refLine := ""
		diffLine := ""
		for c := 0; c < blockCols; c++ {
			got := int(img.Y[r*img.YStride+c])
			ref := int(refY[r*w+c])
			d := got - ref
			if d < 0 {
				d = -d
			}
			gotLine += fmt.Sprintf("%4d", got)
			refLine += fmt.Sprintf("%4d", ref)
			if d == 0 {
				diffLine += "   ."
			} else {
				diffLine += fmt.Sprintf("%4d", d)
				diffCount++
				if d > maxDiff {
					maxDiff = d
				}
			}
		}
		t.Logf("  row %d got: %s", r, gotLine)
		t.Logf("  row %d ref: %s", r, refLine)
		t.Logf("  row %d dif: %s", r, diffLine)
	}
	t.Logf("First 8x8 block: %d/64 pixels differ, max diff=%d", diffCount, maxDiff)

	// --- Step 4: First block diagnostics ---
	t.Log("=== First block diagnostics ===")
	t.Logf("  BaseQIdx: %d", dec.diagBaseQIdx)
	if bi := dec.diagFirstBlock; bi != nil {
		txSizeNames := []string{"TX4x4", "TX8x8", "TX16x16", "TX32x32", "TX64x64"}
		txSzName := "?"
		if bi.txSize >= 0 && bi.txSize < len(txSizeNames) {
			txSzName = txSizeNames[bi.txSize]
		}
		t.Logf("  bSize=%d yMode=%d uvMode=%d txSize=%s skip=%v",
			bi.bSize, bi.yMode, bi.uvMode, txSzName, bi.skip)
	}
	if c := dec.diagFirstTXBCoeffs; c != nil {
		nz := 0
		for _, v := range c {
			if v != 0 {
				nz++
			}
		}
		t.Logf("  First TXB: DC=%d, nonzero=%d/%d, dcQuant=%d, acQuant=%d",
			c[0], nz, len(c), dec.diagFirstTXBDCQ, dec.diagFirstTXBACQ)
		// Print first 16 coefficients.
		n := 16
		if n > len(c) {
			n = len(c)
		}
		t.Logf("  First %d coeffs: %v", n, c[:n])
		t.Logf("  EOB=%d, coeffGridN=%d", dec.diagFirstTXBEOB, dec.diagFirstTXBN)
	}
	t.Logf("  Codec byte pos after SB0: %d / tile_size", dec.diagCodecPosAfterSB1)

	// --- Step 5: Tile decoder diagnostics ---
	t.Log("=== Tile decoder diagnostics ===")
	t.Logf("  Total blocks decoded:     %d", dec.diagBlockCount)
	t.Logf("  Blocks with skip=true:    %d", dec.diagSkipCount)
	t.Logf("  Transform blocks (TXBs):  %d", dec.diagTxbCount)
	t.Logf("  TXBs with non-zero coeff: %d", dec.diagTxbNzCount)

	if dec.diagBlockCount > 0 {
		t.Logf("  Skip rate: %.1f%%", 100.0*float64(dec.diagSkipCount)/float64(dec.diagBlockCount))
	}
	if dec.diagTxbCount > 0 {
		t.Logf("  TXB non-zero rate: %.1f%%", 100.0*float64(dec.diagTxbNzCount)/float64(dec.diagTxbCount))
	}

	// Show first 5 partition decisions.
	numPartToShow := 5
	if numPartToShow > len(dec.diagPartitions) {
		numPartToShow = len(dec.diagPartitions)
	}
	t.Logf("  Total partition decisions: %d", len(dec.diagPartitions))
	t.Log("  First partition decisions:")
	for i := 0; i < numPartToShow; i++ {
		t.Logf("    [%d] %s (%d)", i, partitionTypeName(dec.diagPartitions[i]), dec.diagPartitions[i])
	}

	// Count partition type distribution.
	partCounts := make(map[int]int)
	for _, p := range dec.diagPartitions {
		partCounts[p]++
	}
	t.Log("  Partition distribution:")
	for p := 0; p < NumPartitions; p++ {
		if c, ok := partCounts[p]; ok {
			t.Logf("    %s: %d (%.1f%%)", partitionTypeName(p), c,
				100.0*float64(c)/float64(len(dec.diagPartitions)))
		}
	}
}

package av1

import (
	"testing"
)

func TestDefaultCDFsMonotonic(t *testing.T) {
	cdf := NewCDFTables()

	// AV1 CDFs are stored in descending format (32768 - ascending).
	// Layout: [nsymbs-1 CDF probs, 0 (padding), 0 (counter)] = nsymbs+1 entries.
	// Only check cdf[0..nsymbs-2] for monotonicity (non-increasing).
	checkMono := func(name string, ctx int, p []uint16) {
		nsymbs := len(p) - 1 // nsymbs = len(p) - 1
		for i := 1; i < nsymbs-1; i++ {
			if p[i] > p[i-1] {
				t.Errorf("%s[%d]: cdf[%d]=%d > cdf[%d]=%d (not descending)",
					name, ctx, i, p[i], i-1, p[i-1])
			}
		}
	}

	for bl := range cdf.Partition {
		for ctx, p := range cdf.Partition[bl] {
			if p != nil {
				checkMono("Partition", bl*4+ctx, p)
			}
		}
	}
	for ctx, p := range cdf.IntraMode {
		if p != nil {
			checkMono("IntraMode", ctx, p)
		}
	}
}

func TestDefaultCDFsRange(t *testing.T) {
	cdf := NewCDFTables()

	// All CDF probability values should be in [0, 32768).
	// Check cdf[0..nsymbs-2] (the actual CDF probs).
	checkRange := func(name string, p []uint16) {
		if p == nil {
			return
		}
		nsymbs := len(p) - 1
		for i := 0; i < nsymbs-1; i++ {
			if p[i] >= 32768 {
				t.Errorf("%s: cdf[%d]=%d >= 32768", name, i, p[i])
			}
		}
	}

	for bl := range cdf.Partition {
		for _, p := range cdf.Partition[bl] {
			checkRange("Partition", p)
		}
	}
	for i, p := range cdf.IntraMode {
		checkRange("IntraMode", p)
		_ = i
	}
	for _, p := range cdf.Skip {
		checkRange("Skip", p)
	}
}

func TestCDFSaveRestore(t *testing.T) {
	cdf := NewCDFTables()

	// Save initial state.
	saved := cdf.SaveCDFs()

	// Modify the original.
	if cdf.Skip[0] != nil {
		cdf.Skip[0][0] = 12345
	}

	// Saved should be unmodified.
	if saved.Skip[0][0] == 12345 {
		t.Error("SaveCDFs did not deep copy Skip CDF")
	}

	// Restore.
	cdf.RestoreCDFs(saved)
	if cdf.Skip[0][0] == 12345 {
		t.Error("RestoreCDFs did not restore Skip CDF")
	}
}

func TestCDFCounterInitZero(t *testing.T) {
	cdf := NewCDFTables()

	// All CDF counters (last element) should be 0.
	for bl := range cdf.Partition {
		for ctx, p := range cdf.Partition[bl] {
			if p == nil {
				continue
			}
			counter := p[len(p)-1]
			if counter != 0 {
				t.Errorf("Partition[%d][%d] counter = %d, want 0", bl, ctx, counter)
			}
		}
	}
}

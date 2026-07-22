package h264

import "testing"

// scanList fills a scalingListEntry with explicit scan-order values.
func scanList(vals []uint8) (e scalingListEntry) {
	e.Present = true
	copy(e.Scan[:], vals)
	return
}

// TestScalingMatrixFallbackRuleA covers SPS-level derivation (fall-back rule
// A): explicit list, absent list copying the previous result, UseDefault
// forcing the spec default, and absent 8x8 lists resolving to defaults.
func TestScalingMatrixFallbackRuleA(t *testing.T) {
	// Scan-order list whose raster form is easy to check: value = scan pos.
	var seq [16]uint8
	for i := range seq {
		seq[i] = uint8(i + 1)
	}

	sps := &SPS{SeqScalingMatrixPresent: true}
	sps.ScalingLists[0] = scanList(seq[:])                                  // explicit
	sps.ScalingLists[3] = scalingListEntry{Present: true, UseDefault: true} // default inter
	var e8 [64]uint8
	for i := range e8 {
		e8[i] = uint8(i + 100)
	}
	sps.ScalingLists[7] = scanList(e8[:]) // explicit 8x8 inter
	pps := &PPS{}

	var d Decoder
	d.updateScalingMatrices(sps, pps)

	// List 0 explicit: raster[zigzagToRaster[k]] == scan[k].
	for k := 0; k < 16; k++ {
		if got := d.scalingWS4[0][zigzagToRaster[k]]; got != k+1 {
			t.Fatalf("list0 scan pos %d: got %d, want %d", k, got, k+1)
		}
	}
	// Lists 1, 2 absent: copy of the previous derived list.
	if d.scalingWS4[1] != d.scalingWS4[0] || d.scalingWS4[2] != d.scalingWS4[0] {
		t.Error("absent intra chroma lists must copy the previous list")
	}
	// List 3 UseDefault: spec default inter matrix.
	if d.scalingWS4[3] != default4x4Inter {
		t.Errorf("list3 UseDefault: got %v", d.scalingWS4[3])
	}
	if d.scalingWS4[4] != default4x4Inter || d.scalingWS4[5] != default4x4Inter {
		t.Error("absent inter chroma lists must copy the previous list")
	}
	// 8x8 intra absent: default; 8x8 inter explicit.
	if d.scalingWS8[0] != default8x8Intra {
		t.Error("absent 8x8 intra list must fall back to the default matrix")
	}
	for k := 0; k < 64; k++ {
		if got := d.scalingWS8[1][zigzagToRaster8x8[k]]; got != k+100 {
			t.Fatalf("8x8 inter scan pos %d: got %d, want %d", k, got, k+100)
		}
	}
}

// TestScalingMatrixFallbackRuleB covers PPS-level derivation: absent PPS
// lists 0/3/6/7 keep the SPS-derived result when the SPS coded matrices
// (fall-back rule B), while an explicit PPS list overrides it.
func TestScalingMatrixFallbackRuleB(t *testing.T) {
	var seq [16]uint8
	for i := range seq {
		seq[i] = uint8(i + 30)
	}
	sps := &SPS{SeqScalingMatrixPresent: true}
	sps.ScalingLists[0] = scanList(seq[:])

	var over [16]uint8
	for i := range over {
		over[i] = uint8(i + 60)
	}
	pps := &PPS{PicScalingMatrixPresent: true, Transform8x8Mode: true}
	pps.ScalingLists[3] = scanList(over[:]) // explicit override for Inter Y

	var d Decoder
	d.updateScalingMatrices(sps, pps)

	// PPS list 0 absent + SPS present: keep the SPS result.
	for k := 0; k < 16; k++ {
		if got := d.scalingWS4[0][zigzagToRaster[k]]; got != k+30 {
			t.Fatalf("list0 must keep SPS result: scan pos %d got %d, want %d", k, got, k+30)
		}
	}
	// PPS list 3 explicit override.
	for k := 0; k < 16; k++ {
		if got := d.scalingWS4[3][zigzagToRaster[k]]; got != k+60 {
			t.Fatalf("list3 override: scan pos %d got %d, want %d", k, got, k+60)
		}
	}
	// SPS 8x8 lists were absent (defaults); absent PPS 8x8 lists keep them.
	if d.scalingWS8[0] != default8x8Intra || d.scalingWS8[1] != default8x8Inter {
		t.Error("absent PPS 8x8 lists must keep the SPS-derived defaults")
	}

	// No matrices anywhere: flat 16s.
	var flat Decoder
	flat.updateScalingMatrices(&SPS{}, &PPS{})
	if flat.scalingWS4[0] != flatScaling4x4 || flat.scalingWS8[1] != flatScaling8x8 {
		t.Error("no scaling syntax must derive flat matrices")
	}
}

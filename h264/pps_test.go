package h264

import "testing"

func TestParsePPSFromTestFile(t *testing.T) {
	_, ppsData := loadTestSPSPPS(t)

	pps, err := ParsePPS(ppsData)
	if err != nil {
		t.Fatalf("ParsePPS: %v", err)
	}

	if pps.ID != 0 {
		t.Errorf("PPS ID: got %d, want 0", pps.ID)
	}
	if pps.SPSID != 0 {
		t.Errorf("PPS SPS ID: got %d, want 0", pps.SPSID)
	}
	// Baseline profile uses CAVLC.
	if pps.EntropyCodingModeFlag {
		t.Error("expected CAVLC (entropy_coding_mode_flag=false) for Baseline profile")
	}
	if pps.DeblockingFilterControlPresent == false {
		t.Error("expected deblocking_filter_control_present=true")
	}
}

func TestParsePPSRefCounts(t *testing.T) {
	_, ppsData := loadTestSPSPPS(t)

	pps, err := ParsePPS(ppsData)
	if err != nil {
		t.Fatal(err)
	}

	if pps.NumRefIdxL0DefaultActive == 0 {
		t.Error("NumRefIdxL0DefaultActive should be > 0")
	}
}

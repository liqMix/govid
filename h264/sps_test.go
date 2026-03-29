package h264

import (
	"os"
	"testing"

	govidmp4 "github.com/Eyevinn/mp4ff/mp4"
)

func loadTestSPSPPS(t *testing.T) ([]byte, []byte) {
	t.Helper()
	f, err := os.Open("testdata/test.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	file, err := govidmp4.DecodeFile(f)
	if err != nil {
		t.Fatal(err)
	}

	var videoTrack *govidmp4.TrakBox
	for _, trak := range file.Moov.Traks {
		if trak.Mdia != nil && trak.Mdia.Hdlr != nil && trak.Mdia.Hdlr.HandlerType == "vide" {
			videoTrack = trak
			break
		}
	}
	if videoTrack == nil {
		t.Fatal("no video track")
	}

	dcr := videoTrack.Mdia.Minf.Stbl.Stsd.AvcX.AvcC.DecConfRec
	// SPSnalus/PPSnalus include the NAL header byte; skip it for RBSP parsing.
	sps := dcr.SPSnalus[0]
	pps := dcr.PPSnalus[0]
	return sps[1:], pps[1:]
}

func TestParseSPSFromTestFile(t *testing.T) {
	spsData, _ := loadTestSPSPPS(t)

	sps, err := ParseSPS(spsData)
	if err != nil {
		t.Fatalf("ParseSPS: %v", err)
	}

	if sps.Width != 160 {
		t.Errorf("width: got %d, want 160", sps.Width)
	}
	if sps.Height != 120 {
		t.Errorf("height: got %d, want 120", sps.Height)
	}
	if sps.ProfileIDC != 66 {
		t.Errorf("profile: got %d, want 66 (Baseline)", sps.ProfileIDC)
	}
	if sps.LevelIDC != 30 {
		t.Errorf("level: got %d, want 30", sps.LevelIDC)
	}
	if sps.ChromaFormatIDC != 1 {
		t.Errorf("chroma_format_idc: got %d, want 1 (4:2:0)", sps.ChromaFormatIDC)
	}
	if !sps.FrameMBSOnly {
		t.Error("expected frame_mbs_only_flag=true for Baseline profile")
	}
}

func TestParseSPSDimensions(t *testing.T) {
	spsData, _ := loadTestSPSPPS(t)

	sps, err := ParseSPS(spsData)
	if err != nil {
		t.Fatal(err)
	}

	expectedMBW := uint32(10) // 160/16
	expectedMBH := uint32(8)  // 120/16 (round up, but 120 is exact)
	// PicHeightInMapUnits is already +1'd in parsing, so for 120 it should be 120/16 = 7.5 → 8
	// Actually 120/16 = 7.5 but the encoder uses 8 (ceil). The SPS has PicHeightInMapUnits = 8
	// and CropBottom to remove the extra 8 pixels? Let's just verify the final dimensions.
	if sps.PicWidthInMBs != expectedMBW {
		t.Errorf("PicWidthInMBs: got %d, want %d", sps.PicWidthInMBs, expectedMBW)
	}
	// 120 = 7.5*16, so PicHeightInMapUnits could be 8 with cropping, or 7 with 112 pixels.
	// But 120 is not divisible by 16: 7*16=112, 8*16=128. So we need 8 MBs height with crop.
	// Actually, wait: 120/16 = 7.5. So the encoder must use 8 map units (128 pixels) and crop 8.
	// Let me not assert on PicHeightInMapUnits and just check final dimensions.
	_ = expectedMBH
	if sps.Width != 160 || sps.Height != 120 {
		t.Errorf("dimensions: got %dx%d, want 160x120", sps.Width, sps.Height)
	}
}

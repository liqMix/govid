package av1

import (
	"io"
	"os"
	"testing"

	govidmp4 "github.com/Eyevinn/mp4ff/mp4"
)

// extractFirstSample reads the first sample from an AV1 MP4 file.
func extractFirstSample(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })

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

	ranges, err := vt.GetRangesForSampleInterval(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	dr := ranges[0]
	if _, err := f.Seek(int64(dr.Offset), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, dr.Size)
	if _, err := io.ReadFull(f, data); err != nil {
		t.Fatal(err)
	}
	return data
}

// findSequenceHeaderOBU finds and returns the sequence header OBU data from a sample.
func findSequenceHeaderOBU(t *testing.T, data []byte) []byte {
	t.Helper()
	obus, err := ParseOBUs(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, obu := range obus {
		if obu.Header.Type == OBUSequenceHeader {
			return obu.Data
		}
	}
	t.Fatal("no sequence header OBU found")
	return nil
}

func TestParseSequenceHeaderFromMP4(t *testing.T) {
	sample := extractFirstSample(t, "testdata/idr_only.mp4")

	// Parse OBUs from the sample.
	obus, err := ParseOBUs(sample)
	if err != nil {
		t.Fatal(err)
	}

	// Log all OBU types for debugging.
	for i, obu := range obus {
		t.Logf("OBU[%d]: type=%s size=%d", i, OBUTypeName(obu.Header.Type), len(obu.Data))
	}

	shData := findSequenceHeaderOBU(t, sample)
	sh, err := ParseSequenceHeader(shData)
	if err != nil {
		t.Fatal(err)
	}

	// Verify against ffprobe output:
	// profile=Main (0), width=160, height=120, pix_fmt=yuv420p, 8-bit
	if sh.Profile != 0 {
		t.Errorf("Profile = %d, want 0 (Main)", sh.Profile)
	}
	if sh.MaxFrameWidth != 160 {
		t.Errorf("MaxFrameWidth = %d, want 160", sh.MaxFrameWidth)
	}
	if sh.MaxFrameHeight != 120 {
		t.Errorf("MaxFrameHeight = %d, want 120", sh.MaxFrameHeight)
	}
	if sh.Color.BitDepth != 8 {
		t.Errorf("BitDepth = %d, want 8", sh.Color.BitDepth)
	}
	if sh.Color.MonoChrome {
		t.Error("MonoChrome = true, want false")
	}
	if sh.Color.NumPlanes != 3 {
		t.Errorf("NumPlanes = %d, want 3", sh.Color.NumPlanes)
	}
	// Profile 0 = 4:2:0
	if sh.Color.SubsamplingX != 1 || sh.Color.SubsamplingY != 1 {
		t.Errorf("Subsampling = %d,%d, want 1,1 (4:2:0)", sh.Color.SubsamplingX, sh.Color.SubsamplingY)
	}

	t.Logf("Sequence header: profile=%d, %dx%d, %d-bit, superblock=%d",
		sh.Profile, sh.MaxFrameWidth, sh.MaxFrameHeight, sh.Color.BitDepth, sh.SuperblockSize)
	t.Logf("  OrderHintBits=%d, EnableCDEF=%v, EnableRestoration=%v",
		sh.OrderHintBits, sh.EnableCDEF, sh.EnableRestoration)
	t.Logf("  StillPicture=%v, ReducedStillPictureHeader=%v",
		sh.StillPicture, sh.ReducedStillPictureHeader)
}

func TestParseSequenceHeaderFromTestMP4(t *testing.T) {
	sample := extractFirstSample(t, "testdata/test.mp4")
	shData := findSequenceHeaderOBU(t, sample)
	sh, err := ParseSequenceHeader(shData)
	if err != nil {
		t.Fatal(err)
	}

	if sh.Profile != 0 {
		t.Errorf("Profile = %d, want 0", sh.Profile)
	}
	if sh.MaxFrameWidth != 160 {
		t.Errorf("MaxFrameWidth = %d, want 160", sh.MaxFrameWidth)
	}
	if sh.MaxFrameHeight != 120 {
		t.Errorf("MaxFrameHeight = %d, want 120", sh.MaxFrameHeight)
	}

	t.Logf("Sequence header: profile=%d, %dx%d, %d-bit, sb=%d, OrderHintBits=%d",
		sh.Profile, sh.MaxFrameWidth, sh.MaxFrameHeight, sh.Color.BitDepth,
		sh.SuperblockSize, sh.OrderHintBits)
}

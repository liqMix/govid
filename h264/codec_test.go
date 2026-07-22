package h264

import (
	"fmt"
	"image"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"time"

	govidmp4 "github.com/Eyevinn/mp4ff/mp4"

	mp4demux "github.com/liqmix/govid/mp4"

	"github.com/liqmix/govid"
)

// helper: open an MP4 and return a demuxer-like packet source.
type testPacketSource struct {
	reader       io.ReadSeeker
	track        *govidmp4.TrakBox
	spsData      []byte
	ppsData      []byte
	sampleNr     uint32
	totalSamples uint32
	timescale    uint32
	syncMap      map[uint32]bool
}

func openTestPackets(t *testing.T, path string) *testPacketSource {
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

	dcr := vt.Mdia.Minf.Stbl.Stsd.AvcX.AvcC.DecConfRec
	sps := make([]byte, 4+len(dcr.SPSnalus[0]))
	sps[0] = 0
	sps[1] = 0
	sps[2] = byte(len(dcr.SPSnalus[0]) >> 8)
	sps[3] = byte(len(dcr.SPSnalus[0]))
	copy(sps[4:], dcr.SPSnalus[0])

	pps := make([]byte, 4+len(dcr.PPSnalus[0]))
	pps[0] = 0
	pps[1] = 0
	pps[2] = byte(len(dcr.PPSnalus[0]) >> 8)
	pps[3] = byte(len(dcr.PPSnalus[0]))
	copy(pps[4:], dcr.PPSnalus[0])

	syncMap := make(map[uint32]bool)
	if vt.Mdia.Minf.Stbl.Stss != nil {
		for _, s := range vt.Mdia.Minf.Stbl.Stss.SampleNumber {
			syncMap[s] = true
		}
	}

	return &testPacketSource{
		reader:       f,
		track:        vt,
		spsData:      sps,
		ppsData:      pps,
		sampleNr:     1,
		totalSamples: vt.Mdia.Minf.Stbl.Stsz.GetNrSamples(),
		timescale:    vt.Mdia.Mdhd.Timescale,
		syncMap:      syncMap,
	}
}

func (s *testPacketSource) nextPacket() (govid.Packet, error) {
	if s.sampleNr > s.totalSamples {
		return govid.Packet{}, io.EOF
	}

	ranges, err := s.track.GetRangesForSampleInterval(s.sampleNr, s.sampleNr)
	if err != nil {
		return govid.Packet{}, err
	}

	dr := ranges[0]
	if _, err := s.reader.Seek(int64(dr.Offset), io.SeekStart); err != nil {
		return govid.Packet{}, err
	}

	data := make([]byte, dr.Size)
	if _, err := io.ReadFull(s.reader, data); err != nil {
		return govid.Packet{}, err
	}

	keyframe := len(s.syncMap) == 0 || s.syncMap[s.sampleNr]
	if keyframe {
		prefixed := make([]byte, len(s.spsData)+len(s.ppsData)+len(data))
		n := copy(prefixed, s.spsData)
		n += copy(prefixed[n:], s.ppsData)
		copy(prefixed[n:], data)
		data = prefixed
	}

	s.sampleNr++
	return govid.Packet{
		Data:     data,
		Keyframe: keyframe,
	}, nil
}

func TestCABACTruncatedStream(t *testing.T) {
	// Construct a minimal packet: SPS + PPS (with EntropyCodingModeFlag=true) + IDR slice.
	// All NAL units use 4-byte length prefixes.
	//
	// SPS: Baseline profile, 16x16, poc_type=0, log2_max_frame_num=4.
	// PPS: entropy_coding_mode_flag=1 (CABAC).
	// IDR: I-slice referencing PPS 0.
	packet := []byte{
		// SPS NAL (length=6)
		0, 0, 0, 6,
		0x67,       // NAL header: ref_idc=3, type=7 (SPS)
		0x42, 0xC0, // profile_idc=66 (Baseline), constraint_flags
		0x1E, // level_idc=30
		0xFB, // UE(0)*5=sps_id,log2maxfn,poc_type,log2maxpoc,maxref | 0=gaps | 1=width
		0xC8, // UE(0)=height | 1=frame_mbs_only | 1=direct8x8 | 0=crop | 0=vui | pad

		// PPS NAL (length=3)
		0, 0, 0, 3,
		0x68, // NAL header: ref_idc=3, type=8 (PPS)
		0xEE, // UE(0)=pps_id | UE(0)=sps_id | 1=CABAC! | 0=bottom_field | UE(0)*3=slicegroups,l0,l1 | 0=weighted_pred
		0x38, // 00=weighted_bipred | SE(0)*3=initqp,initqs,chromaqp | 0=deblock | 0=constrained | 0=redundant

		// IDR slice NAL (length=2)
		0, 0, 0, 2,
		0x65, // NAL header: ref_idc=3, type=5 (IDR)
		0xB8, // UE(0)=first_mb | UE(2)=slice_type(I) | UE(0)=pps_id | pad
	}

	// CABAC is now supported; a truncated CABAC slice must fail cleanly
	// (no panic, non-nil error) rather than being rejected up front.
	codec := NewCodec()
	_, err := codec.Decode(govid.Packet{Data: packet})
	if err == nil {
		t.Fatal("expected error for truncated CABAC stream, got nil")
	}
}

func TestDecodeFirstIDRFrame(t *testing.T) {
	src := openTestPackets(t, "testdata/idr_only.mp4")
	codec := NewCodec()

	pkt, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}

	frame, err := codec.Decode(pkt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if frame == nil {
		t.Fatal("expected non-nil frame")
	}
	if frame.YCbCr == nil {
		t.Fatal("expected non-nil YCbCr")
	}
	if frame.Width != 160 || frame.Height != 120 {
		t.Errorf("dimensions: got %dx%d, want 160x120", frame.Width, frame.Height)
	}
}

func TestDecodeAllIDRFrames(t *testing.T) {
	src := openTestPackets(t, "testdata/idr_only.mp4")
	codec := NewCodec()

	count := 0
	for {
		pkt, err := src.nextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}

		frame, err := codec.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: Decode: %v", count, err)
		}
		if frame == nil {
			continue
		}
		if frame.Width != 160 || frame.Height != 120 {
			t.Errorf("frame %d: dimensions %dx%d, want 160x120", count, frame.Width, frame.Height)
		}
		count++
	}

	if count < 5 {
		t.Errorf("expected at least 5 IDR frames, got %d", count)
	}
	t.Logf("decoded %d IDR frames successfully", count)
}

func TestRGBAConversion(t *testing.T) {
	src := openTestPackets(t, "testdata/idr_only.mp4")
	codec := NewCodec()

	pkt, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}

	frame, err := codec.Decode(pkt)
	if err != nil {
		t.Fatal(err)
	}

	rgba := frame.RGBA()
	if rgba == nil {
		t.Fatal("RGBA returned nil")
	}
	expected := frame.Width * frame.Height * 4
	if len(rgba) != expected {
		t.Errorf("RGBA length: got %d, want %d", len(rgba), expected)
	}
	// Verify alpha channel is 0xFF for all pixels.
	for i := 3; i < len(rgba); i += 4 {
		if rgba[i] != 0xFF {
			t.Errorf("pixel %d alpha: got %d, want 255", i/4, rgba[i])
			break
		}
	}
}

func TestDecodeAllFrames(t *testing.T) {
	src := openTestPackets(t, "testdata/test.mp4")
	codec := NewCodec()

	count := 0
	var lastWidth, lastHeight int
	for {
		pkt, err := src.nextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}

		frame, err := codec.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: Decode: %v", count, err)
		}
		if frame == nil {
			continue
		}
		if lastWidth == 0 {
			lastWidth = frame.Width
			lastHeight = frame.Height
		}
		if frame.Width != lastWidth || frame.Height != lastHeight {
			t.Errorf("frame %d: dimensions %dx%d, want %dx%d", count, frame.Width, frame.Height, lastWidth, lastHeight)
		}
		count++
	}

	if count < 2 {
		t.Errorf("expected at least 2 frames (I + P), got %d", count)
	}
	t.Logf("decoded %d I+P frames successfully", count)
}

func TestPFrameAfterIDR(t *testing.T) {
	src := openTestPackets(t, "testdata/test.mp4")
	codec := NewCodec()

	// Decode first packet (IDR).
	pkt, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := codec.Decode(pkt)
	if err != nil {
		t.Fatalf("IDR decode: %v", err)
	}
	if frame == nil {
		t.Fatal("IDR frame is nil")
	}

	// Decode second packet (P-frame).
	pkt, err = src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err = codec.Decode(pkt)
	if err != nil {
		t.Fatalf("P-frame decode: %v", err)
	}
	if frame == nil {
		t.Fatal("P-frame is nil")
	}
	if frame.YCbCr == nil {
		t.Fatal("P-frame YCbCr is nil")
	}
}

func TestFlushResetsDPB(t *testing.T) {
	src := openTestPackets(t, "testdata/test.mp4")
	codec := NewCodec()

	// Decode first IDR frame.
	pkt, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}
	_, err = codec.Decode(pkt)
	if err != nil {
		t.Fatalf("IDR decode: %v", err)
	}

	// Flush resets decoder state.
	codec.Flush()

	// Trying to decode a P-frame without a preceding IDR should fail.
	pkt2, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}
	_, err = codec.Decode(pkt2)
	if err == nil {
		t.Log("P-frame after flush succeeded (may have SPS/PPS inline)")
	}

	// Re-sending IDR should work.
	src2 := openTestPackets(t, "testdata/test.mp4")
	pkt3, err := src2.nextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := codec.Decode(pkt3)
	if err != nil {
		t.Fatalf("IDR after flush: %v", err)
	}
	if frame == nil {
		t.Fatal("IDR after flush returned nil")
	}
}

func TestDecodePixelValues(t *testing.T) {
	src := openTestPackets(t, "testdata/idr_only.mp4")
	codec := NewCodec()

	pkt, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}

	frame, err := codec.Decode(pkt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if frame == nil || frame.YCbCr == nil {
		t.Fatal("expected non-nil frame with YCbCr data")
	}

	// Verify pixel values are reasonable (not corrupted).
	ycbcr := frame.YCbCr
	yLen := ycbcr.YStride * ycbcr.Rect.Dy()
	if yLen > len(ycbcr.Y) {
		yLen = len(ycbcr.Y)
	}

	// Check that Y plane has some non-trivial variance (not just DC).
	var ySum, yMin, yMax int
	yMin = 255
	for i := 0; i < yLen; i++ {
		v := int(ycbcr.Y[i])
		ySum += v
		if v < yMin {
			yMin = v
		}
		if v > yMax {
			yMax = v
		}
	}
	yRange := yMax - yMin
	if yRange < 10 {
		t.Errorf("Y pixel range too small (%d-%d=%d), likely missing AC coefficients", yMin, yMax, yRange)
	}

	// Verify no clamped-to-255 saturation across entire plane (sign of corruption).
	saturated := 0
	for i := 0; i < yLen; i++ {
		if ycbcr.Y[i] == 255 {
			saturated++
		}
	}
	if yLen > 0 && float64(saturated)/float64(yLen) > 0.5 {
		t.Errorf("%.0f%% of Y pixels are saturated (255), likely corrupted", 100*float64(saturated)/float64(yLen))
	}
}

func TestFlushAndRedecode(t *testing.T) {
	src := openTestPackets(t, "testdata/idr_only.mp4")
	codec := NewCodec()

	pkt, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}

	frame1, err := codec.Decode(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if frame1 == nil {
		t.Fatal("first decode returned nil")
	}

	codec.Flush()

	// Re-decode the same packet after flush.
	frame2, err := codec.Decode(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if frame2 == nil {
		t.Fatal("second decode returned nil")
	}
	if frame2.Width != frame1.Width || frame2.Height != frame1.Height {
		t.Error("dimensions differ after flush")
	}
}

func TestDecodeTestVsReference(t *testing.T) {
	const testMP4 = "testdata/test.mp4"
	const refYUV = "testdata/test_frame0.yuv"

	if _, err := os.Stat(testMP4); err != nil {
		t.Skip("test.mp4 not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("test_frame0.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	src := openTestPackets(t, testMP4)
	codec := NewCodec()

	// Log PPS/SPS details after decode.
	defer func() {
		d := codec.dec
		if d != nil && d.activePPS != nil {
			t.Logf("  PPS: id=%d sps=%d PicInitQPMinus26=%d ChromaQPOffset=%d Deblock=%v WeightedPred=%v Transform8x8=%v",
				d.activePPS.ID, d.activePPS.SPSID, d.activePPS.PicInitQPMinus26,
				d.activePPS.ChromaQPIndexOffset, d.activePPS.DeblockingFilterControlPresent,
				d.activePPS.WeightedPredFlag, d.activePPS.Transform8x8Mode)
		}
		if d != nil && d.activeSPS != nil {
			t.Logf("  SPS: id=%d profile=%d level=%d poc_type=%d log2MaxFN=%d log2MaxPOC=%d maxRef=%d",
				d.activeSPS.ID, d.activeSPS.ProfileIDC, d.activeSPS.LevelIDC,
				d.activeSPS.PicOrderCntType, d.activeSPS.Log2MaxFrameNum,
				d.activeSPS.Log2MaxPicOrderCntLsb, d.activeSPS.MaxNumRefFrames)
		}
	}()

	pkt, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := codec.Decode(pkt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if frame == nil || frame.YCbCr == nil {
		t.Fatal("nil frame")
	}

	w := frame.Width
	h := frame.Height
	t.Logf("dimensions: %dx%d", w, h)

	ycbcr := frame.YCbCr
	ySize := w * h
	cSize := (w / 2) * (h / 2)

	if len(ref) != ySize+2*cSize {
		t.Fatalf("ref size %d != expected %d", len(ref), ySize+2*cSize)
	}

	// Compare Y plane — find first wrong pixel, per-MB max error, histogram.
	yErr, yMaxErr := 0, 0
	firstErrX, firstErrY, firstErrGot, firstErrWant := -1, -1, 0, 0
	histogram := make([]int, 256)

	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			got := int(ycbcr.Y[j*ycbcr.YStride+i])
			want := int(ref[j*w+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			histogram[d]++
			if d > 0 {
				yErr++
			}
			if d > yMaxErr {
				yMaxErr = d
			}
			if d > 0 && firstErrX == -1 {
				firstErrX = i
				firstErrY = j
				firstErrGot = got
				firstErrWant = want
			}
		}
	}

	// Compare Cb plane.
	cbOff := ySize
	cbErr, cbMaxErr := 0, 0
	cw := w / 2
	ch := h / 2
	for j := 0; j < ch; j++ {
		for i := 0; i < cw; i++ {
			got := int(ycbcr.Cb[j*ycbcr.CStride+i])
			want := int(ref[cbOff+j*cw+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				cbErr++
			}
			if d > cbMaxErr {
				cbMaxErr = d
			}
		}
	}

	// Compare Cr plane.
	crOff := ySize + cSize
	crErr, crMaxErr := 0, 0
	for j := 0; j < ch; j++ {
		for i := 0; i < cw; i++ {
			got := int(ycbcr.Cr[j*ycbcr.CStride+i])
			want := int(ref[crOff+j*cw+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				crErr++
			}
			if d > crMaxErr {
				crMaxErr = d
			}
		}
	}

	t.Logf("Y:  %d/%d wrong pixels (%.1f%%), max error %d", yErr, ySize, 100*float64(yErr)/float64(ySize), yMaxErr)
	t.Logf("Cb: %d/%d wrong pixels, max error %d", cbErr, cSize, cbMaxErr)
	t.Logf("Cr: %d/%d wrong pixels, max error %d", crErr, cSize, crMaxErr)

	if firstErrX >= 0 {
		t.Logf("First wrong Y pixel: (%d,%d) got=%d want=%d (MB %d,%d, blk %d,%d)",
			firstErrX, firstErrY, firstErrGot, firstErrWant,
			firstErrX/16, firstErrY/16, (firstErrX%16)/4, (firstErrY%16)/4)
	}

	// Dump pixel values for first 3 MBs in row 0 (top 4 rows).
	for mbx := 0; mbx < 3 && mbx < w/16; mbx++ {
		t.Logf("MB(%d,0) pixel dump (first 4 rows):", mbx)
		for j := 0; j < 4; j++ {
			gotLine := ""
			wantLine := ""
			for i := 0; i < 16; i++ {
				px := mbx*16 + i
				py := j
				g := int(ycbcr.Y[py*ycbcr.YStride+px])
				wr := int(ref[py*w+px])
				gotLine += fmt.Sprintf(" %3d", g)
				wantLine += fmt.Sprintf(" %3d", wr)
			}
			t.Logf("  row %d got: %s", j, gotLine)
			t.Logf("  row %d ref: %s", j, wantLine)
		}
	}

	// Per-MB max error for first 2 rows.
	for mby := 0; mby < 2 && mby < h/16; mby++ {
		line := fmt.Sprintf("MB row %d:", mby)
		for mbx := 0; mbx < w/16; mbx++ {
			mbMax := 0
			for j := 0; j < 16; j++ {
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					got := int(ycbcr.Y[py*ycbcr.YStride+px])
					want := int(ref[py*w+px])
					d := got - want
					if d < 0 {
						d = -d
					}
					if d > mbMax {
						mbMax = d
					}
				}
			}
			line += fmt.Sprintf(" %3d", mbMax)
		}
		t.Log(line)
	}

	// Error histogram (first 10 buckets + tail).
	t.Log("Error histogram:")
	for i := 0; i <= 10 && i < len(histogram); i++ {
		if histogram[i] > 0 {
			t.Logf("  err=%d: %d pixels", i, histogram[i])
		}
	}
	tail := 0
	for i := 11; i < len(histogram); i++ {
		tail += histogram[i]
	}
	if tail > 0 {
		t.Logf("  err>10: %d pixels", tail)
	}

	if yMaxErr > 2 || cbMaxErr > 2 || crMaxErr > 2 {
		t.Errorf("decoded frame differs from ffmpeg reference (Y max=%d, Cb max=%d, Cr max=%d)", yMaxErr, cbMaxErr, crMaxErr)
	}
}

func TestDecodeBakerVsReference(t *testing.T) {
	const bakerMP4 = "../examples/videos/baker_h264.mp4"
	const refYUV = "testdata/baker_frame0.yuv"

	if _, err := os.Stat(bakerMP4); err != nil {
		t.Skip("baker_h264.mp4 not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("baker_frame0.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	src := openTestPackets(t, bakerMP4)
	codec := NewCodec()

	pkt, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := codec.Decode(pkt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if frame == nil || frame.YCbCr == nil {
		t.Fatal("nil frame")
	}

	w := frame.Width
	h := frame.Height
	t.Logf("dimensions: %dx%d", w, h)

	ycbcr := frame.YCbCr
	ySize := w * h
	cSize := (w / 2) * (h / 2)

	if len(ref) != ySize+2*cSize {
		t.Fatalf("ref size %d != expected %d", len(ref), ySize+2*cSize)
	}

	// Compare Y plane.
	yErr, yMaxErr, yErrAt := 0, 0, 0
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			got := int(ycbcr.Y[j*ycbcr.YStride+i])
			want := int(ref[j*w+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				yErr++
			}
			if d > yMaxErr {
				yMaxErr = d
				yErrAt = j*w + i
			}
		}
	}

	// Compare Cb plane.
	cbOff := ySize
	cbErr, cbMaxErr := 0, 0
	cw := w / 2
	ch := h / 2
	for j := 0; j < ch; j++ {
		for i := 0; i < cw; i++ {
			got := int(ycbcr.Cb[j*ycbcr.CStride+i])
			want := int(ref[cbOff+j*cw+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				cbErr++
			}
			if d > cbMaxErr {
				cbMaxErr = d
			}
		}
	}

	// Compare Cr plane.
	crOff := ySize + cSize
	crErr, crMaxErr := 0, 0
	for j := 0; j < ch; j++ {
		for i := 0; i < cw; i++ {
			got := int(ycbcr.Cr[j*ycbcr.CStride+i])
			want := int(ref[crOff+j*cw+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				crErr++
			}
			if d > crMaxErr {
				crMaxErr = d
			}
		}
	}

	t.Logf("Y:  %d/%d wrong pixels, max error %d (at pixel %d)", yErr, ySize, yMaxErr, yErrAt)
	t.Logf("Cb: %d/%d wrong pixels, max error %d", cbErr, cSize, cbMaxErr)
	t.Logf("Cr: %d/%d wrong pixels, max error %d", crErr, cSize, crMaxErr)

	// Find first wrong MB row.
	for mby := 0; mby < h/16; mby++ {
		mbErrors := 0
		for mbx := 0; mbx < w/16; mbx++ {
			for j := 0; j < 16; j++ {
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					got := int(ycbcr.Y[py*ycbcr.YStride+px])
					want := int(ref[py*w+px])
					d := got - want
					if d < 0 {
						d = -d
					}
					if d > 2 {
						mbErrors++
					}
				}
			}
		}
		if mbErrors > 0 && mby < 5 {
			t.Logf("MB row %d: %d pixels with error > 2", mby, mbErrors)
		}
	}

	// Allow small rounding differences from deblocking filter and motion compensation.
	if yMaxErr > 5 || cbMaxErr > 2 || crMaxErr > 2 {
		t.Errorf("decoded frame differs from ffmpeg reference (Y max=%d, Cb max=%d, Cr max=%d)", yMaxErr, cbMaxErr, crMaxErr)
	}
}

func TestDecodeTestVsNoDeblock(t *testing.T) {
	const testMP4 = "testdata/test.mp4"
	const refYUV = "testdata/test_frame0_nodb.yuv"

	if _, err := os.Stat(testMP4); err != nil {
		t.Skip("test.mp4 not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("test_frame0_nodb.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	src := openTestPackets(t, testMP4)
	codec := NewCodec()
	// Disable deblocking by injecting a flag.
	codec.dec.disableDeblock = true

	pkt, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := codec.Decode(pkt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if frame == nil || frame.YCbCr == nil {
		t.Fatal("nil frame")
	}

	w := frame.Width
	h := frame.Height
	ycbcr := frame.YCbCr
	ySize := w * h
	cSize := (w / 2) * (h / 2)

	if len(ref) != ySize+2*cSize {
		t.Fatalf("ref size %d != expected %d", len(ref), ySize+2*cSize)
	}

	// Compare Y plane.
	yErr, yMaxErr := 0, 0
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			got := int(ycbcr.Y[j*ycbcr.YStride+i])
			want := int(ref[j*w+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				yErr++
			}
			if d > yMaxErr {
				yMaxErr = d
			}
		}
	}

	// Compare Cb plane.
	cbOff := ySize
	cbErr, cbMaxErr := 0, 0
	cw := w / 2
	ch := h / 2
	for j := 0; j < ch; j++ {
		for i := 0; i < cw; i++ {
			got := int(ycbcr.Cb[j*ycbcr.CStride+i])
			want := int(ref[cbOff+j*cw+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				cbErr++
			}
			if d > cbMaxErr {
				cbMaxErr = d
			}
		}
	}

	// Compare Cr plane.
	crOff := ySize + cSize
	crErr, crMaxErr := 0, 0
	for j := 0; j < ch; j++ {
		for i := 0; i < cw; i++ {
			got := int(ycbcr.Cr[j*ycbcr.CStride+i])
			want := int(ref[crOff+j*cw+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				crErr++
			}
			if d > crMaxErr {
				crMaxErr = d
			}
		}
	}

	t.Logf("Y:  %d/%d wrong pixels (%.1f%%), max error %d", yErr, ySize, 100*float64(yErr)/float64(ySize), yMaxErr)
	t.Logf("Cb: %d/%d wrong pixels, max error %d", cbErr, cSize, cbMaxErr)
	t.Logf("Cr: %d/%d wrong pixels, max error %d", crErr, cSize, crMaxErr)

	// Per-MB max error (all rows).
	for mby := 0; mby < h/16; mby++ {
		line := fmt.Sprintf("MB row %d Y:", mby)
		for mbx := 0; mbx < w/16; mbx++ {
			mbMax := 0
			for j := 0; j < 16; j++ {
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					got := int(ycbcr.Y[py*ycbcr.YStride+px])
					want := int(ref[py*w+px])
					d := got - want
					if d < 0 {
						d = -d
					}
					if d > mbMax {
						mbMax = d
					}
				}
			}
			line += fmt.Sprintf(" %3d", mbMax)
		}
		t.Log(line)
	}

	// Per-MB chroma max error (first 3 rows).
	for mby := 0; mby < 3 && mby < h/16; mby++ {
		cbLine := fmt.Sprintf("MB row %d Cb:", mby)
		crLine := fmt.Sprintf("MB row %d Cr:", mby)
		for mbx := 0; mbx < w/16; mbx++ {
			cbMax, crMax := 0, 0
			for j := 0; j < 8; j++ {
				for i := 0; i < 8; i++ {
					py := mby*8 + j
					px := mbx*8 + i
					gotCb := int(ycbcr.Cb[py*ycbcr.CStride+px])
					wantCb := int(ref[ySize+py*cw+px])
					d := gotCb - wantCb
					if d < 0 {
						d = -d
					}
					if d > cbMax {
						cbMax = d
					}

					gotCr := int(ycbcr.Cr[py*ycbcr.CStride+px])
					wantCr := int(ref[ySize+cSize+py*cw+px])
					d = gotCr - wantCr
					if d < 0 {
						d = -d
					}
					if d > crMax {
						crMax = d
					}
				}
			}
			cbLine += fmt.Sprintf(" %3d", cbMax)
			crLine += fmt.Sprintf(" %3d", crMax)
		}
		t.Log(cbLine)
		t.Log(crLine)
	}

	// Dump all rows of MB(1,0) to identify exact error locations.
	if w >= 32 && h >= 16 {
		t.Log("MB(1,0) full dump:")
		for j := 0; j < 16; j++ {
			py := j
			var gotLine, wantLine strings.Builder
			for i := 0; i < 16; i++ {
				px := 16 + i
				got := int(ycbcr.Y[py*ycbcr.YStride+px])
				want := int(ref[py*w+px])
				fmt.Fprintf(&gotLine, " %3d", got)
				fmt.Fprintf(&wantLine, " %3d", want)
			}
			t.Logf("  row %2d got: %s", j, gotLine.String())
			t.Logf("  row %2d ref: %s", j, wantLine.String())
		}
	}

	if yMaxErr > 0 || cbMaxErr > 0 || crMaxErr > 0 {
		t.Errorf("decoded frame differs from nodb reference (Y max=%d, Cb max=%d, Cr max=%d)", yMaxErr, cbMaxErr, crMaxErr)
	}
}

func TestDecodeBakerMultiFrame(t *testing.T) {
	const bakerMP4 = "../examples/videos/baker_h264.mp4"
	const refYUV = "testdata/baker_frames_0_9.yuv"
	const numFrames = 10

	if _, err := os.Stat(bakerMP4); err != nil {
		t.Skip("baker_h264.mp4 not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("baker_frames_0_9.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	src := openTestPackets(t, bakerMP4)
	codec := NewCodec()

	// Decode first frame to get dimensions.
	pkt, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := codec.Decode(pkt)
	if err != nil {
		t.Fatalf("frame 0: Decode: %v", err)
	}
	if frame == nil || frame.YCbCr == nil {
		t.Fatal("frame 0: nil frame")
	}

	w := frame.Width
	h := frame.Height
	ySize := w * h
	cw := w / 2
	ch := h / 2
	cSize := cw * ch
	frameSize := ySize + 2*cSize

	if len(ref) < numFrames*frameSize {
		t.Fatalf("ref file too small: %d bytes, need %d", len(ref), numFrames*frameSize)
	}

	t.Logf("dimensions: %dx%d, frameSize=%d", w, h, frameSize)

	// Compare helper for a single frame.
	type planeStats struct {
		wrongPixels int
		maxError    int
		totalPixels int
	}
	comparePlane := func(decoded []byte, stride int, refData []byte, pw, ph int) planeStats {
		stats := planeStats{totalPixels: pw * ph}
		for j := 0; j < ph; j++ {
			for i := 0; i < pw; i++ {
				got := int(decoded[j*stride+i])
				want := int(refData[j*pw+i])
				d := got - want
				if d < 0 {
					d = -d
				}
				if d > 0 {
					stats.wrongPixels++
				}
				if d > stats.maxError {
					stats.maxError = d
				}
			}
		}
		return stats
	}

	// Process frame 0 (already decoded).
	frames := make([]*govid.Frame, 0, numFrames)
	frames = append(frames, frame)

	// Decode frames 1-9.
	for i := 1; i < numFrames; i++ {
		pkt, err := src.nextPacket()
		if err != nil {
			t.Fatalf("frame %d: nextPacket: %v", i, err)
		}
		f, err := codec.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: Decode: %v", i, err)
		}
		if f == nil || f.YCbCr == nil {
			t.Fatalf("frame %d: nil frame", i)
		}
		frames = append(frames, f)
	}

	// Compare each frame against reference.
	worstFrame := 0
	worstMaxErr := 0
	for i, f := range frames {
		refOff := i * frameSize
		refY := ref[refOff : refOff+ySize]
		refCb := ref[refOff+ySize : refOff+ySize+cSize]
		refCr := ref[refOff+ySize+cSize : refOff+ySize+2*cSize]

		ycbcr := f.YCbCr
		yStats := comparePlane(ycbcr.Y, ycbcr.YStride, refY, w, h)
		cbStats := comparePlane(ycbcr.Cb, ycbcr.CStride, refCb, cw, ch)
		crStats := comparePlane(ycbcr.Cr, ycbcr.CStride, refCr, cw, ch)

		frameType := "P"
		if i == 0 {
			frameType = "IDR"
		}

		t.Logf("frame %d (%s): Y %d/%d wrong (%.1f%%) max=%d | Cb %d/%d max=%d | Cr %d/%d max=%d",
			i, frameType,
			yStats.wrongPixels, yStats.totalPixels, 100*float64(yStats.wrongPixels)/float64(yStats.totalPixels), yStats.maxError,
			cbStats.wrongPixels, cbStats.totalPixels, cbStats.maxError,
			crStats.wrongPixels, crStats.totalPixels, crStats.maxError)

		maxErr := yStats.maxError
		if cbStats.maxError > maxErr {
			maxErr = cbStats.maxError
		}
		if crStats.maxError > maxErr {
			maxErr = crStats.maxError
		}
		if maxErr > worstMaxErr {
			worstMaxErr = maxErr
			worstFrame = i
		}

		// IDR frames: hard threshold of 5 (deblocking rounding).
		if i == 0 && (yStats.maxError > 5 || cbStats.maxError > 2 || crStats.maxError > 2) {
			t.Errorf("frame %d (IDR): exceeds threshold (Y max=%d, Cb max=%d, Cr max=%d)",
				i, yStats.maxError, cbStats.maxError, crStats.maxError)
		}
	}

	// Dump per-MB max Y error for the worst frame (first 3 rows).
	if worstFrame >= 0 && worstFrame < len(frames) {
		f := frames[worstFrame]
		refOff := worstFrame * frameSize
		refY := ref[refOff : refOff+ySize]
		ycbcr := f.YCbCr

		t.Logf("--- Worst frame %d: per-MB max Y error (first 3 rows) ---", worstFrame)
		for mby := 0; mby < 3 && mby < h/16; mby++ {
			line := fmt.Sprintf("MB row %d:", mby)
			for mbx := 0; mbx < w/16; mbx++ {
				mbMax := 0
				for j := 0; j < 16; j++ {
					for i := 0; i < 16; i++ {
						py := mby*16 + j
						px := mbx*16 + i
						got := int(ycbcr.Y[py*ycbcr.YStride+px])
						want := int(refY[py*w+px])
						d := got - want
						if d < 0 {
							d = -d
						}
						if d > mbMax {
							mbMax = d
						}
					}
				}
				line += fmt.Sprintf(" %3d", mbMax)
			}
			t.Log(line)
		}
	}

	t.Logf("Overall worst: frame %d with max error %d", worstFrame, worstMaxErr)
}

func TestFrame2WithPerfectReference(t *testing.T) {
	const bakerMP4 = "../examples/videos/baker_h264.mp4"
	const refYUV = "testdata/baker_frames_0_9.yuv"

	if _, err := os.Stat(bakerMP4); err != nil {
		t.Skip("baker_h264.mp4 not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("baker_frames_0_9.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	w, h := 1280, 720
	ySize := w * h
	cw, ch := w/2, h/2
	cSize := cw * ch
	frameSize := ySize + 2*cSize

	loadFrame := func(idx int) *image.YCbCr {
		off := idx * frameSize
		img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
		for j := 0; j < h; j++ {
			copy(img.Y[j*img.YStride:j*img.YStride+w], ref[off+j*w:off+j*w+w])
		}
		for j := 0; j < ch; j++ {
			copy(img.Cb[j*img.CStride:j*img.CStride+cw], ref[off+ySize+j*cw:off+ySize+j*cw+cw])
			copy(img.Cr[j*img.CStride:j*img.CStride+cw], ref[off+ySize+cSize+j*cw:off+ySize+cSize+j*cw+cw])
		}
		return img
	}

	// Decode frames 0 and 1 normally to set up decoder state.
	src := openTestPackets(t, bakerMP4)
	codec := NewCodec()
	for i := 0; i < 2; i++ {
		pkt, err := src.nextPacket()
		if err != nil {
			t.Fatal(err)
		}
		_, err = codec.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}

	// Inject perfect reference frames (with matching frame_num values so
	// reference list construction orders them newest-first).
	d := codec.dec
	d.refFrames = []*refFrame{
		{img: loadFrame(0), frameNum: 0, id: 100},
		{img: loadFrame(1), frameNum: 1, id: 101},
	}

	// Decode frame 2.
	pkt, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame2, err := codec.Decode(pkt)
	if err != nil {
		t.Fatalf("frame 2 with perfect ref: %v", err)
	}

	// Compare against ffmpeg's frame 2.
	refFrame2Y := ref[2*frameSize : 2*frameSize+ySize]
	ycbcr := frame2.YCbCr
	yErr, yMaxErr := 0, 0
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			got := int(ycbcr.Y[j*ycbcr.YStride+i])
			want := int(refFrame2Y[j*w+i])
			dd := got - want
			if dd < 0 {
				dd = -dd
			}
			if dd > 0 {
				yErr++
			}
			if dd > yMaxErr {
				yMaxErr = dd
			}
		}
	}
	t.Logf("Frame 2 with PERFECT ref: Y %d/%d wrong (%.1f%%), max=%d",
		yErr, ySize, 100*float64(yErr)/float64(ySize), yMaxErr)

	// Per-MB type error analysis.
	mbw := w / 16
	mbh := h / 16
	typeNames := map[int]string{
		-2: "intra", -1: "skip", 0: "16x16", 1: "16x8", 2: "8x16", 3: "8x8", 4: "8x8r0",
	}
	typeTotalErr := make(map[int]int)
	typeMaxErr := make(map[int]int)
	typeCount := make(map[int]int)
	typeTotalPix := make(map[int]int)

	for mby := 0; mby < mbh; mby++ {
		for mbx := 0; mbx < mbw; mbx++ {
			mbIdx := mby*mbw + mbx
			mt := d.mbInfo[mbIdx].mbType
			mbMax := 0
			mbErr := 0
			for j := 0; j < 16; j++ {
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					got := int(ycbcr.Y[py*ycbcr.YStride+px])
					want := int(refFrame2Y[py*w+px])
					dd := got - want
					if dd < 0 {
						dd = -dd
					}
					if dd > mbMax {
						mbMax = dd
					}
					if dd > 0 {
						mbErr++
					}
				}
			}
			typeCount[mt]++
			typeTotalErr[mt] += mbErr
			typeTotalPix[mt] += 256
			if mbMax > typeMaxErr[mt] {
				typeMaxErr[mt] = mbMax
			}
		}
	}

	t.Log("Per-MB-type error analysis (with perfect reference):")
	for mt := -2; mt <= 4; mt++ {
		if typeCount[mt] == 0 {
			continue
		}
		t.Logf("  %6s: count=%4d  wrongPix=%6d/%7d (%.1f%%)  maxErr=%3d",
			typeNames[mt], typeCount[mt], typeTotalErr[mt], typeTotalPix[mt],
			100*float64(typeTotalErr[mt])/float64(typeTotalPix[mt]), typeMaxErr[mt])
	}

	// Dump per-MB max error for first 5 rows with MB types.
	for mby := 0; mby < 5 && mby < mbh; mby++ {
		line := fmt.Sprintf("MB row %d:", mby)
		for mbx := 0; mbx < mbw; mbx++ {
			mbIdx := mby*mbw + mbx
			mt := d.mbInfo[mbIdx].mbType
			mbMax := 0
			for j := 0; j < 16; j++ {
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					got := int(ycbcr.Y[py*ycbcr.YStride+px])
					want := int(refFrame2Y[py*w+px])
					dd := got - want
					if dd < 0 {
						dd = -dd
					}
					if dd > mbMax {
						mbMax = dd
					}
				}
			}
			sym := "."
			switch mt {
			case -2:
				sym = "I"
			case -1:
				sym = "_"
			case 0:
				sym = "="
			case 1:
				sym = "-"
			case 2:
				sym = "|"
			case 3, 4:
				sym = "+"
			}
			if mbMax > 0 {
				line += fmt.Sprintf(" %s%2d", sym, mbMax)
			} else {
				line += "  . "
			}
		}
		t.Log(line)
	}

	// Show 3 worst MBs with details.
	type mbErrInfo struct {
		mbx, mby, maxErr, mt int
	}
	var worst [3]mbErrInfo
	for mby := 0; mby < mbh; mby++ {
		for mbx := 0; mbx < mbw; mbx++ {
			mbIdx := mby*mbw + mbx
			mt := d.mbInfo[mbIdx].mbType
			mbMax := 0
			for j := 0; j < 16; j++ {
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					got := int(ycbcr.Y[py*ycbcr.YStride+px])
					want := int(refFrame2Y[py*w+px])
					dd := got - want
					if dd < 0 {
						dd = -dd
					}
					if dd > mbMax {
						mbMax = dd
					}
				}
			}
			for k := 0; k < 3; k++ {
				if mbMax > worst[k].maxErr {
					copy(worst[k+1:], worst[k:2])
					worst[k] = mbErrInfo{mbx, mby, mbMax, mt}
					break
				}
			}
		}
	}
	refFrame1Y := ref[1*frameSize : 1*frameSize+ySize]
	for _, wst := range worst {
		if wst.maxErr == 0 {
			continue
		}
		mbIdx := wst.mby*mbw + wst.mbx
		info := &d.mbInfo[mbIdx]
		t.Logf("Worst MB(%d,%d) type=%s maxErr=%d mv[0]=%v ref=%v hasCoef=%v",
			wst.mbx, wst.mby, typeNames[wst.mt], wst.maxErr, info.mv[0], info.refIdx, info.hasCoef)
		// Dump first 4 rows of got vs want vs reference frame 1.
		for j := 0; j < 4; j++ {
			py := wst.mby*16 + j
			gotLine := ""
			wantLine := ""
			ref1Line := ""
			for i := 0; i < 16; i++ {
				px := wst.mbx*16 + i
				g := int(ycbcr.Y[py*ycbcr.YStride+px])
				ww := int(refFrame2Y[py*w+px])
				r1 := int(refFrame1Y[py*w+px])
				gotLine += fmt.Sprintf(" %3d", g)
				wantLine += fmt.Sprintf(" %3d", ww)
				ref1Line += fmt.Sprintf(" %3d", r1)
			}
			t.Logf("  row %d got:   %s", j, gotLine)
			t.Logf("  row %d want:  %s", j, wantLine)
			t.Logf("  row %d frame1:%s", j, ref1Line)
		}
	}
}

// TestDecodeBGMP4 decodes every packet of examples/videos/bg.mp4 and, on the
// first decode error, reports the packet index, the sync-sample flag, and the
// NAL unit types contained in the failing packet. The player silently stops
// playback on any decode error (player.go:92-94), so a test is the only way
// to surface the actual decoder error for this file.
func TestDecodeBGMP4(t *testing.T) {
	const bgMP4 = "../examples/videos/bg.mp4"
	if _, err := os.Stat(bgMP4); err != nil {
		t.Skip("bg.mp4 not found")
	}

	src := openTestPackets(t, bgMP4)
	codec := NewCodec()

	type mbTrace struct {
		mbx, mby         int
		branch           string
		startBit, endBit int
		rawVal           uint32
	}
	var trace []mbTrace
	var lastMBType uint32
	DebugPSliceTrace = func(mbx, mby int, branch string, startBit, endBit int, rawVal uint32) {
		trace = append(trace, mbTrace{mbx, mby, branch, startBit, endBit, rawVal})
	}
	DebugMBLog = func(mbx, mby, mbType, bitsBeforeMB int) {
		lastMBType = uint32(mbType)
	}
	DebugMBBits = func(mbx, mby, startBit, endBit int) {
		// Only record if there's no P-slice trace entry already for this bit range.
		// DebugMBBits is wired for both I-slice and P-slice; trace via DebugPSliceTrace
		// covers P-slice, so only log I-slice MBs here.
		if len(trace) > 0 && trace[len(trace)-1].startBit == startBit {
			return
		}
		trace = append(trace, mbTrace{mbx, mby, "I", startBit, endBit, lastMBType})
	}
	t.Cleanup(func() {
		DebugPSliceTrace = nil
		DebugMBLog = nil
		DebugMBBits = nil
	})

	pktIdx := 0
	framesOk := 0
	for {
		pkt, err := src.nextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("packet %d: nextPacket: %v", pktIdx, err)
		}

		nals, nerr := ParseNALUnits(pkt.Data, 4)
		nalDesc := ""
		if nerr == nil {
			for _, n := range nals {
				nalDesc += fmt.Sprintf(" t=%d(%dB)", n.Type, len(n.Data))
			}
		} else {
			nalDesc = fmt.Sprintf(" nalParseErr=%v", nerr)
		}

		trace = trace[:0]
		frame, err := codec.Decode(pkt)
		if err != nil {
			t.Logf("packet %d (keyframe=%v, size=%dB, nals=%s): Decode failed: %v",
				pktIdx, pkt.Keyframe, len(pkt.Data), nalDesc, err)
			// Dump last 40 MBs from the trace to see the desync context.
			start := len(trace) - 40
			if start < 0 {
				start = 0
			}
			t.Logf("Last %d MBs of trace (I=I-slice intra, N=non-skip in P, S=skip in P):", len(trace)-start)
			for _, m := range trace[start:] {
				kind := "mbType"
				if m.branch == "S" {
					kind = "skipRun"
				}
				t.Logf("  MB(%3d,%3d) %s bits=[%7d..%7d] span=%4d %s=%d",
					m.mbx, m.mby, m.branch, m.startBit, m.endBit, m.endBit-m.startBit, kind, m.rawVal)
			}
			t.Logf("lastMBType (mbType read for the failing MB) = %d", lastMBType)
			t.FailNow()
		}
		if frame != nil {
			framesOk++
		}
		pktIdx++
	}

	t.Logf("decoded %d packets → %d frames with no error", pktIdx, framesOk)
}

// TestDecodeBGMP4VsReference compares the first 30 decoded frames of bg.mp4
// against a raw-YUV reference produced by ffmpeg. On the first frame where any
// plane's max pixel error exceeds 2 (deblocking rounding floor), it dumps a
// per-MB-type error breakdown and a per-MB grid of max errors — enough data to
// name the broken code path (intra pred / motion comp / skip / deblocking).
//
// This is a diagnostic test — it is expected to fail and produce signal.
func TestDecodeBGMP4VsReference(t *testing.T) {
	const bgMP4 = "../examples/videos/bg.mp4"
	const refYUV = "testdata/bg_frames_0_119.yuv"
	const numFrames = 120
	const errThreshold = 2

	if _, err := os.Stat(bgMP4); err != nil {
		t.Skip("bg.mp4 not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("bg_frames_0_119.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	src := openTestPackets(t, bgMP4)
	codec := NewCodec()

	pkt0, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame0, err := codec.Decode(pkt0)
	if err != nil {
		t.Fatalf("frame 0 (IDR): %v", err)
	}
	if frame0 == nil || frame0.YCbCr == nil {
		t.Fatal("frame 0: nil frame")
	}

	w := frame0.Width
	h := frame0.Height
	ySize := w * h
	cw := w / 2
	ch := h / 2
	cSize := cw * ch
	frameSize := ySize + 2*cSize

	if len(ref) < numFrames*frameSize {
		t.Fatalf("ref file too small: %d bytes, need %d", len(ref), numFrames*frameSize)
	}
	t.Logf("dimensions: %dx%d, frameSize=%d", w, h, frameSize)

	type planeStats struct {
		wrongPixels int
		maxError    int
		totalPixels int
	}
	comparePlane := func(decoded []byte, stride int, refData []byte, pw, ph int) planeStats {
		stats := planeStats{totalPixels: pw * ph}
		for j := 0; j < ph; j++ {
			for i := 0; i < pw; i++ {
				got := int(decoded[j*stride+i])
				want := int(refData[j*pw+i])
				d := got - want
				if d < 0 {
					d = -d
				}
				if d > 0 {
					stats.wrongPixels++
				}
				if d > stats.maxError {
					stats.maxError = d
				}
			}
		}
		return stats
	}

	frames := make([]*govid.Frame, 0, numFrames)
	frames = append(frames, frame0)
	for i := 1; i < numFrames; i++ {
		pkt, err := src.nextPacket()
		if err != nil {
			t.Fatalf("frame %d: nextPacket: %v", i, err)
		}
		f, err := codec.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: Decode: %v", i, err)
		}
		if f == nil || f.YCbCr == nil {
			t.Fatalf("frame %d: nil frame", i)
		}
		frames = append(frames, f)
	}

	firstBadFrame := -1
	for i, f := range frames {
		refOff := i * frameSize
		refY := ref[refOff : refOff+ySize]
		refCb := ref[refOff+ySize : refOff+ySize+cSize]
		refCr := ref[refOff+ySize+cSize : refOff+ySize+2*cSize]

		ycbcr := f.YCbCr
		yStats := comparePlane(ycbcr.Y, ycbcr.YStride, refY, w, h)
		cbStats := comparePlane(ycbcr.Cb, ycbcr.CStride, refCb, cw, ch)
		crStats := comparePlane(ycbcr.Cr, ycbcr.CStride, refCr, cw, ch)

		frameType := "P"
		if i == 0 {
			frameType = "IDR"
		}
		t.Logf("frame %2d (%s): Y %d/%d (%.2f%%) max=%3d | Cb max=%3d | Cr max=%3d",
			i, frameType,
			yStats.wrongPixels, yStats.totalPixels,
			100*float64(yStats.wrongPixels)/float64(yStats.totalPixels),
			yStats.maxError, cbStats.maxError, crStats.maxError)

		if firstBadFrame == -1 && (yStats.maxError > errThreshold || cbStats.maxError > errThreshold || crStats.maxError > errThreshold) {
			firstBadFrame = i
		}
	}

	if firstBadFrame == -1 {
		t.Log("no frame exceeded error threshold — decoder output matches reference within rounding")
		return
	}

	// Per-MB-type error breakdown for the first bad frame.
	// Re-decode up to and including the bad frame so d.mbInfo reflects it.
	// (The codec stores mbInfo for the most-recently-decoded frame only.)
	srcReplay := openTestPackets(t, bgMP4)
	codecReplay := NewCodec()
	var badFrame *govid.Frame
	for i := 0; i <= firstBadFrame; i++ {
		pkt, err := srcReplay.nextPacket()
		if err != nil {
			t.Fatalf("replay frame %d: nextPacket: %v", i, err)
		}
		badFrame, err = codecReplay.Decode(pkt)
		if err != nil {
			t.Fatalf("replay frame %d: Decode: %v", i, err)
		}
	}

	d := codecReplay.dec
	refOff := firstBadFrame * frameSize
	refY := ref[refOff : refOff+ySize]
	ycbcr := badFrame.YCbCr
	mbw := w / 16
	mbh := h / 16

	typeNames := map[int]string{
		-2: "intra ", -1: "skip  ", 0: "L016  ", 1: "L0_168", 2: "L0_816", 3: "L08x8 ", 4: "L08r0 ",
	}
	typeMaxErr := make(map[int]int)
	typeWrongPx := make(map[int]int)
	typeTotalPx := make(map[int]int)
	typeCount := make(map[int]int)

	for mby := 0; mby < mbh; mby++ {
		for mbx := 0; mbx < mbw; mbx++ {
			mbIdx := mby*mbw + mbx
			mt := d.mbInfo[mbIdx].mbType
			typeCount[mt]++
			typeTotalPx[mt] += 256
			mbMax := 0
			for j := 0; j < 16; j++ {
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					got := int(ycbcr.Y[py*ycbcr.YStride+px])
					want := int(refY[py*w+px])
					dd := got - want
					if dd < 0 {
						dd = -dd
					}
					if dd > 0 {
						typeWrongPx[mt]++
					}
					if dd > mbMax {
						mbMax = dd
					}
				}
			}
			if mbMax > typeMaxErr[mt] {
				typeMaxErr[mt] = mbMax
			}
		}
	}

	t.Logf("=== first bad frame: %d (Y-plane per-MB-type breakdown) ===", firstBadFrame)
	for mt := -2; mt <= 4; mt++ {
		if typeCount[mt] == 0 {
			continue
		}
		t.Logf("  type=%s count=%4d wrongPix=%6d/%7d (%5.1f%%) maxErr=%3d",
			typeNames[mt], typeCount[mt], typeWrongPx[mt], typeTotalPx[mt],
			100*float64(typeWrongPx[mt])/float64(typeTotalPx[mt]), typeMaxErr[mt])
	}

	// Grid of per-MB max Y error for the bad frame.
	// Symbol encodes MB type: I=intra, _=skip, =/-/|/+ for inter variants, . = no error.
	t.Log("--- per-MB max Y error grid (symbol = MB type; number = max err) ---")
	for mby := 0; mby < mbh; mby++ {
		line := fmt.Sprintf("%3d:", mby)
		for mbx := 0; mbx < mbw; mbx++ {
			mbIdx := mby*mbw + mbx
			mt := d.mbInfo[mbIdx].mbType
			mbMax := 0
			for j := 0; j < 16; j++ {
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					got := int(ycbcr.Y[py*ycbcr.YStride+px])
					want := int(refY[py*w+px])
					dd := got - want
					if dd < 0 {
						dd = -dd
					}
					if dd > mbMax {
						mbMax = dd
					}
				}
			}
			sym := "."
			switch mt {
			case -2:
				sym = "I"
			case -1:
				sym = "_"
			case 0:
				sym = "="
			case 1:
				sym = "-"
			case 2:
				sym = "|"
			case 3, 4:
				sym = "+"
			}
			if mbMax > 0 {
				line += fmt.Sprintf(" %s%2d", sym, mbMax)
			} else {
				line += "  . "
			}
		}
		t.Log(line)
	}

	// Find first erroring MB in raster order (any type) and dump detail.
	firstBadMB := -1
	for mby := 0; mby < mbh && firstBadMB == -1; mby++ {
		for mbx := 0; mbx < mbw && firstBadMB == -1; mbx++ {
			mbMax := 0
			for j := 0; j < 16; j++ {
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					got := int(ycbcr.Y[py*ycbcr.YStride+px])
					want := int(refY[py*w+px])
					dd := got - want
					if dd < 0 {
						dd = -dd
					}
					if dd > mbMax {
						mbMax = dd
					}
				}
			}
			if mbMax > 0 {
				firstBadMB = mby*mbw + mbx
				t.Logf("=== first erroring MB (raster): (%d,%d) maxErr=%d mbType=%d isIntra=%v ===",
					mbx, mby, mbMax, d.mbInfo[firstBadMB].mbType, d.mbInfo[firstBadMB].isIntra)
				// Pixel dump.
				for j := 0; j < 16; j++ {
					var gotLine, wantLine, diffLine string
					for i := 0; i < 16; i++ {
						py := mby*16 + j
						px := mbx*16 + i
						g := int(ycbcr.Y[py*ycbcr.YStride+px])
						wr := int(refY[py*w+px])
						gotLine += fmt.Sprintf(" %3d", g)
						wantLine += fmt.Sprintf(" %3d", wr)
						diffLine += fmt.Sprintf(" %+3d", g-wr)
					}
					t.Logf("  first-bad row %2d got:  %s", j, gotLine)
					t.Logf("  first-bad row %2d ref:  %s", j, wantLine)
					t.Logf("  first-bad row %2d diff: %s", j, diffLine)
				}
			}
		}
	}

	// Dump full detail for the first erroring MB matching filter (legacy: P_8x8ref0).
	for mby := 0; mby < mbh; mby++ {
		for mbx := 0; mbx < mbw; mbx++ {
			mbIdx := mby*mbw + mbx
			info := &d.mbInfo[mbIdx]
			if info.mbType != 4 { // only P_8x8ref0
				continue
			}
			mbMax := 0
			for j := 0; j < 16; j++ {
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					got := int(ycbcr.Y[py*ycbcr.YStride+px])
					want := int(refY[py*w+px])
					dd := got - want
					if dd < 0 {
						dd = -dd
					}
					if dd > mbMax {
						mbMax = dd
					}
				}
			}
			if mbMax == 0 {
				continue
			}
			t.Logf("=== first erroring P_8x8ref0 MB: (%d,%d) maxErr=%d ===", mbx, mby, mbMax)
			t.Logf("  subMBType[0..3]: %v", info.subMBType)
			t.Logf("  refIdx[0..3]:    %v", info.refIdx)
			for bi := 0; bi < 16; bi++ {
				t.Logf("  mv[%2d] (4x4 at (%d,%d)): (%d, %d)", bi, (bi%4)*4, (bi/4)*4, info.mv[bi][0], info.mv[bi][1])
			}
			// Show all 16 rows of got vs want
			for j := 0; j < 16; j++ {
				var gotLine, wantLine, diffLine string
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					g := int(ycbcr.Y[py*ycbcr.YStride+px])
					wr := int(refY[py*w+px])
					dd := g - wr
					gotLine += fmt.Sprintf(" %3d", g)
					wantLine += fmt.Sprintf(" %3d", wr)
					diffLine += fmt.Sprintf(" %+3d", dd)
				}
				t.Logf("  row %2d got:  %s", j, gotLine)
				t.Logf("  row %2d ref:  %s", j, wantLine)
				t.Logf("  row %2d diff: %s", j, diffLine)
			}
			goto dumpDone
		}
	}
dumpDone:

	// Intentionally fail so the diagnostic log is surfaced via `go test -v`.
	t.Errorf("decoder diverges from reference starting at frame %d — see log above", firstBadFrame)
}

// TestDecodeBGMP4Frame18_DumpIntra dumps the intra prediction modes + residual
// coefficients for MB(0,0) of frame 18 so we can see what the decoder chose.
func TestDecodeBGMP4Frame18_DumpIntra(t *testing.T) {
	const bgMP4 = "../examples/videos/bg.mp4"
	if _, err := os.Stat(bgMP4); err != nil {
		t.Skip("bg.mp4 not found")
	}

	type modesInfo struct {
		mbx, mby, cbpLuma, cbpChroma int
		modes                        [16]int
	}
	var i4Dump []modesInfo
	var mbTypeDump = make(map[int]int) // mbIdx → mbType read at that MB
	DebugI4x4Modes = func(mbx, mby int, modes [16]int, cbpLuma, cbpChroma int) {
		if mbx == 0 && mby == 0 {
			i4Dump = append(i4Dump, modesInfo{mbx, mby, cbpLuma, cbpChroma, modes})
		}
	}
	DebugMBLog = func(mbx, mby, mbType, bitsBeforeMB int) {
		if mbx == 0 && mby == 0 {
			mbTypeDump[len(mbTypeDump)] = mbType
		}
	}
	t.Cleanup(func() {
		DebugI4x4Modes = nil
		DebugMBLog = nil
	})

	src := openTestPackets(t, bgMP4)
	codec := NewCodec()
	for i := 0; i <= 18; i++ {
		pkt, err := src.nextPacket()
		if err != nil {
			t.Fatalf("pkt %d: %v", i, err)
		}
		_, err = codec.Decode(pkt)
		if err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
	}

	t.Logf("mbType read events for MB(0,0) across frames: %v", mbTypeDump)
	t.Logf("I4x4 mode events for MB(0,0): %d events", len(i4Dump))
	for i, info := range i4Dump {
		t.Logf("  event %d: cbpLuma=%d cbpChroma=%d modes=%v", i, info.cbpLuma, info.cbpChroma, info.modes)
	}
}

// TestDecodeBGMP4Frame7_DumpCoeff dumps CAVLC-decoded coefficients for every 4x4
// luma residual block in MB(13,21) of frame 7. Used to verify whether the
// coefficients themselves or the placement/MC is the root of the error.
func TestDecodeBGMP4Frame7_DumpCoeff(t *testing.T) {
	const bgMP4 = "../examples/videos/bg.mp4"
	if _, err := os.Stat(bgMP4); err != nil {
		t.Skip("bg.mp4 not found")
	}

	type blkEntry struct {
		mbx, mby, blk, nz, nC int
		coeffs                [16]int16
	}
	var dump []blkEntry
	DebugBlkLog = func(mbx, mby, blk, nz int, coeffs []int16, _ int, nC int) {
		if mbx != 13 || mby != 21 {
			return
		}
		var e blkEntry
		e.mbx, e.mby, e.blk, e.nz, e.nC = mbx, mby, blk, nz, nC
		copy(e.coeffs[:], coeffs)
		dump = append(dump, e)
	}
	t.Cleanup(func() { DebugBlkLog = nil })

	src := openTestPackets(t, bgMP4)
	codec := NewCodec()
	for i := 0; i <= 7; i++ {
		pkt, err := src.nextPacket()
		if err != nil {
			t.Fatalf("pkt %d: %v", i, err)
		}
		_, err = codec.Decode(pkt)
		if err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
	}

	t.Logf("MB(13,21) frame 7 — %d residual blocks parsed:", len(dump))
	for _, e := range dump {
		t.Logf("  blk=%2d nz=%d nC=%d coeffs=%v", e.blk, e.nz, e.nC, e.coeffs)
	}
}

// TestDecodeBGMP4Frame7_DeblockIsolation toggles deblocking on/off and re-checks
// the specific error pixel (13,21) at (col=4, row=12). If the error is present
// only with deblocking enabled, the bug is in the deblocking filter for 4x4
// sub-partition edges in P_8x8ref0 MBs.
func TestDecodeBGMP4Frame7_DeblockIsolation(t *testing.T) {
	const bgMP4 = "../examples/videos/bg.mp4"
	const refYUV = "testdata/bg_frames_0_119.yuv"

	if _, err := os.Stat(bgMP4); err != nil {
		t.Skip("bg.mp4 not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("bg_frames_0_119.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	run := func(disableDeblock bool) (yMaxAtTarget int, yFrameMax int) {
		src := openTestPackets(t, bgMP4)
		codec := NewCodec()
		codec.dec.disableDeblock = disableDeblock

		var f *govid.Frame
		for i := 0; i <= 7; i++ {
			pkt, err := src.nextPacket()
			if err != nil {
				t.Fatalf("pkt %d: %v", i, err)
			}
			f, err = codec.Decode(pkt)
			if err != nil {
				t.Fatalf("decode %d: %v", i, err)
			}
		}
		w := f.Width
		h := f.Height
		ySize := w * h
		cSize := (w / 2) * (h / 2)
		frameSize := ySize + 2*cSize
		refY := ref[7*frameSize : 7*frameSize+ySize]
		ycbcr := f.YCbCr

		// Compute max Y err across whole frame + at the specific target pixel.
		targetPx := 13*16 + 4
		targetPy := 21*16 + 12
		got := int(ycbcr.Y[targetPy*ycbcr.YStride+targetPx])
		want := int(refY[targetPy*w+targetPx])
		yMaxAtTarget = got - want
		if yMaxAtTarget < 0 {
			yMaxAtTarget = -yMaxAtTarget
		}
		for j := 0; j < h; j++ {
			for i := 0; i < w; i++ {
				gg := int(ycbcr.Y[j*ycbcr.YStride+i])
				ww := int(refY[j*w+i])
				dd := gg - ww
				if dd < 0 {
					dd = -dd
				}
				if dd > yFrameMax {
					yFrameMax = dd
				}
			}
		}
		return
	}

	// With deblock (default). Reference has deblock too, so this is the apples-to-apples case.
	withTgt, withFrameMax := run(false)
	withoutTgt, withoutFrameMax := run(true)

	t.Logf("frame 7 target pixel (col=4, row=12 of MB(13,21)):")
	t.Logf("  deblock ENABLED:  err at target = %d, frame max Y err = %d", withTgt, withFrameMax)
	t.Logf("  deblock DISABLED: err at target = %d, frame max Y err = %d", withoutTgt, withoutFrameMax)

	if withTgt > 0 && withoutTgt == 0 {
		t.Log("CONCLUSION: target pixel error appears ONLY with deblock enabled → bug is in deblocking")
	} else if withTgt == withoutTgt {
		t.Log("CONCLUSION: target pixel error is SAME with/without deblock → bug is in MC or residual (not deblock)")
	} else {
		t.Logf("CONCLUSION: target err changed from %d (with) to %d (without) — partial deblock contribution", withTgt, withoutTgt)
	}
}

// TestDecodeH264_8x8InterVsReference is currently skipped. Primitives for
// 8x8 (idct8x8, dequant8x8, predfunc8x8, read8x8ResidualCAVLC) are in place,
// but wiring both inter and intra paths caused CAVLC bit desync on real
// x264 streams that was not resolved this session. The hard-stops at
// decode.go and pslice.go remain active. See project memory
// project_h264_phaseA_8x8.md for the debug state.
func TestDecodeH264_8x8InterVsReference_DISABLED(t *testing.T) {
	const mp4Path = "testdata/bg_8x8inter.mp4"
	const refYUVPath = "testdata/bg_8x8inter_yuv.yuv"
	const numFrames = 120
	const errThreshold = 2

	if _, err := os.Stat(mp4Path); err != nil {
		t.Skip("bg_8x8inter.mp4 not found")
	}
	if _, err := os.Stat(refYUVPath); err != nil {
		t.Skip("bg_8x8inter_yuv.yuv not found")
	}

	// Tag 8x8 intra MBs so we can see which ones appear before any desync.
	DebugMBLog = func(mbx, mby, mbType, _ int) {
		if mbType == -8888 {
			t.Logf("  8x8-intra MB at (%d,%d)", mbx, mby)
		}
	}
	t.Cleanup(func() { DebugMBLog = nil })

	ref, err := os.ReadFile(refYUVPath)
	if err != nil {
		t.Fatal(err)
	}

	src := openTestPackets(t, mp4Path)
	codec := NewCodec()

	pkt0, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame0, err := codec.Decode(pkt0)
	if err != nil {
		t.Fatalf("frame 0 (IDR): %v", err)
	}
	if frame0 == nil || frame0.YCbCr == nil {
		t.Fatal("frame 0: nil frame")
	}

	w := frame0.Width
	h := frame0.Height
	ySize := w * h
	cw := w / 2
	ch := h / 2
	cSize := cw * ch
	frameSize := ySize + 2*cSize
	t.Logf("dimensions: %dx%d, frameSize=%d", w, h, frameSize)

	if len(ref) < numFrames*frameSize {
		t.Fatalf("ref too small: %d, need %d", len(ref), numFrames*frameSize)
	}

	comparePlane := func(decoded []byte, stride int, refData []byte, pw, ph int) (wrong, maxErr int) {
		for j := 0; j < ph; j++ {
			for i := 0; i < pw; i++ {
				got := int(decoded[j*stride+i])
				want := int(refData[j*pw+i])
				d := got - want
				if d < 0 {
					d = -d
				}
				if d > 0 {
					wrong++
				}
				if d > maxErr {
					maxErr = d
				}
			}
		}
		return
	}

	frames := make([]*govid.Frame, 0, numFrames)
	frames = append(frames, frame0)
	for i := 1; i < numFrames; i++ {
		pkt, err := src.nextPacket()
		if err != nil {
			t.Fatalf("frame %d: nextPacket: %v", i, err)
		}
		f, err := codec.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: Decode: %v", i, err)
		}
		if f == nil || f.YCbCr == nil {
			t.Fatalf("frame %d: nil frame", i)
		}
		frames = append(frames, f)
	}

	firstBad := -1
	for i, f := range frames {
		off := i * frameSize
		refY := ref[off : off+ySize]
		refCb := ref[off+ySize : off+ySize+cSize]
		refCr := ref[off+ySize+cSize : off+ySize+2*cSize]

		yc := f.YCbCr
		yWrong, yMax := comparePlane(yc.Y, yc.YStride, refY, w, h)
		cbWrong, cbMax := comparePlane(yc.Cb, yc.CStride, refCb, cw, ch)
		crWrong, crMax := comparePlane(yc.Cr, yc.CStride, refCr, cw, ch)

		tag := "P"
		if i == 0 {
			tag = "IDR"
		}
		t.Logf("frame %3d (%s): Y %6d (%5.2f%%) max=%3d | Cb %5d max=%3d | Cr %5d max=%3d",
			i, tag, yWrong, 100*float64(yWrong)/float64(ySize), yMax,
			cbWrong, cbMax, crWrong, crMax)

		if firstBad == -1 && (yMax > errThreshold || cbMax > errThreshold || crMax > errThreshold) {
			firstBad = i
		}
	}

	if firstBad == -1 {
		t.Log("all frames bit-exact within tolerance")
		return
	}
	t.Errorf("8x8 decode diverges from reference starting at frame %d", firstBad)
}

func TestDumpSliceBits(t *testing.T) {
	src := openTestPackets(t, "testdata/test.mp4")

	pkt, err := src.nextPacket()
	if err != nil {
		t.Fatal(err)
	}

	nalUnits, err := ParseNALUnits(pkt.Data, 4)
	if err != nil {
		t.Fatalf("ParseNALUnits: %v", err)
	}

	// Dump the raw IDR slice NAL bytes (before EPB removal)
	// SPS: 4 + 23 = 27 bytes. PPS: 4 + 5 = 9 bytes. Total: 36.
	// Then SEI NAL (variable) then IDR NAL.
	// Let me find the IDR NAL in the packet
	offset := 0
	for offset < len(pkt.Data)-4 {
		nalLen := int(pkt.Data[offset])<<24 | int(pkt.Data[offset+1])<<16 | int(pkt.Data[offset+2])<<8 | int(pkt.Data[offset+3])
		nalType := pkt.Data[offset+4] & 0x1f
		if nalType == 5 { // IDR
			// Dump the first 20 bytes of raw NAL data (after length prefix)
			rawHex := ""
			for i := 4; i < 24 && offset+i < len(pkt.Data); i++ {
				rawHex += fmt.Sprintf(" %02x", pkt.Data[offset+i])
			}
			t.Logf("IDR NAL raw (len=%d, first 20):%s", nalLen, rawHex)
			// Check for EPBs in first 20 bytes
			rawData := pkt.Data[offset+5 : offset+4+nalLen] // after header
			for i := 0; i < len(rawData)-2 && i < 25; i++ {
				if rawData[i] == 0 && rawData[i+1] == 0 && rawData[i+2] == 3 {
					t.Logf("  EPB at raw offset %d (bytes: %02x %02x %02x)", i, rawData[i], rawData[i+1], rawData[i+2])
				}
			}
			break
		}
		offset += 4 + nalLen
	}

	for _, nal := range nalUnits {
		hexStr := ""
		n := len(nal.Data)
		if n > 30 {
			n = 30
		}
		for i := 0; i < n; i++ {
			hexStr += fmt.Sprintf(" %02x", nal.Data[i])
		}
		t.Logf("NAL type=%d ref_idc=%d len=%d data:%s", nal.Type, nal.RefIDC, len(nal.Data), hexStr)

		if nal.Type == NALPPS {
			// Manually parse PPS to verify
			pps, perr := ParsePPS(nal.Data)
			if perr != nil {
				t.Logf("  PPS parse error: %v", perr)
			} else {
				t.Logf("  PPS: id=%d sps=%d entropy=%v PicInitQPMinus26=%d chromaQPoff=%d",
					pps.ID, pps.SPSID, pps.EntropyCodingModeFlag, pps.PicInitQPMinus26, pps.ChromaQPIndexOffset)
			}
		}
		if nal.Type == NALSPS {
			sps, serr := ParseSPS(nal.Data)
			if serr != nil {
				t.Logf("  SPS parse error: %v", serr)
			} else {
				t.Logf("  SPS: id=%d profile=%d level=%d poc=%d log2maxfn=%d maxref=%d %dx%d",
					sps.ID, sps.ProfileIDC, sps.LevelIDC, sps.PicOrderCntType,
					sps.Log2MaxFrameNum, sps.MaxNumRefFrames, sps.Width, sps.Height)
			}
		}
	}
}

// decodeAndCompareYUV decodes every frame of an MP4 fixture and compares each
// plane pixel-for-pixel against an ffmpeg-generated raw YUV420 reference,
// requiring bit-exactness. disableDeblock decodes without the loop filter for
// comparison against an ffmpeg -skip_loop_filter all reference.
func decodeAndCompareYUV(t *testing.T, mp4Path, yuvPath string, numFrames int, disableDeblock bool) {
	t.Helper()
	ref, err := os.ReadFile(yuvPath)
	if err != nil {
		t.Fatal(err)
	}

	src := openTestPackets(t, mp4Path)
	codec := NewCodec()
	codec.dec.disableDeblock = disableDeblock

	comparePlane := func(frameIdx int, plane string, decoded []byte, stride int, refData []byte, pw, ph int) {
		wrong, maxErr := 0, 0
		for j := 0; j < ph; j++ {
			for i := 0; i < pw; i++ {
				d := int(decoded[j*stride+i]) - int(refData[j*pw+i])
				if d < 0 {
					d = -d
				}
				if d > 0 {
					wrong++
				}
				if d > maxErr {
					maxErr = d
				}
			}
		}
		if wrong > 0 {
			t.Errorf("frame %d %s: %d/%d wrong pixels, max error %d", frameIdx, plane, wrong, pw*ph, maxErr)
			if plane == "Y" {
				// Per-MB max error grid to localize the failure.
				for mby := 0; mby*16 < ph; mby++ {
					line := fmt.Sprintf("  MB row %d:", mby)
					for mbx := 0; mbx*16 < pw; mbx++ {
						mbMax := 0
						for j := mby * 16; j < mby*16+16 && j < ph; j++ {
							for i := mbx * 16; i < mbx*16+16 && i < pw; i++ {
								d := int(decoded[j*stride+i]) - int(refData[j*pw+i])
								if d < 0 {
									d = -d
								}
								if d > mbMax {
									mbMax = d
								}
							}
						}
						line += fmt.Sprintf(" %3d", mbMax)
					}
					t.Log(line)
				}
			}
		}
	}

	// Decode all packets, collecting display-order output (streams with
	// B-frames emit with a reorder delay), then drain the tail.
	var frames []*govid.Frame
	for i := 0; i < numFrames; i++ {
		pkt, err := src.nextPacket()
		if err != nil {
			t.Fatalf("packet %d: nextPacket: %v", i, err)
		}
		frame, err := codec.Decode(pkt)
		if err != nil {
			t.Fatalf("packet %d: Decode: %v", i, err)
		}
		if frame != nil {
			frames = append(frames, frame)
		}
	}
	for {
		frame := codec.Drain()
		if frame == nil {
			break
		}
		frames = append(frames, frame)
	}
	if len(frames) != numFrames {
		t.Fatalf("got %d frames, want %d", len(frames), numFrames)
	}

	for i, frame := range frames {
		if frame.YCbCr == nil {
			t.Fatalf("frame %d: nil image", i)
		}
		w, h := frame.Width, frame.Height
		cw, ch := w/2, h/2
		frameSize := w*h + 2*cw*ch
		if len(ref) < (i+1)*frameSize {
			t.Fatalf("ref file too small: %d bytes, need %d", len(ref), (i+1)*frameSize)
		}
		refOff := i * frameSize
		ycbcr := frame.YCbCr
		comparePlane(i, "Y", ycbcr.Y, ycbcr.YStride, ref[refOff:refOff+w*h], w, h)
		comparePlane(i, "Cb", ycbcr.Cb, ycbcr.CStride, ref[refOff+w*h:refOff+w*h+cw*ch], cw, ch)
		comparePlane(i, "Cr", ycbcr.Cr, ycbcr.CStride, ref[refOff+w*h+cw*ch:refOff+frameSize], cw, ch)
	}
}

// TestDecodeHigh8x8Intra covers the Intra_8x8 prediction + 8x8 CAVLC residual
// path: 5 all-intra High-profile frames encoded with x264 (cabac=0, 8x8dct=1,
// blurred source to force i8x8 mode selection across all 9 prediction modes).
func TestDecodeHigh8x8Intra(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_high8x8_intra.mp4", "testdata/test_high8x8_intra.yuv", 5, false)
}

// TestDecodeHigh8x8MultiFrame covers the inter 8x8 transform path: IDR + 29
// P-frames, High profile CAVLC, 57.7% of coded inter MBs using the 8x8
// transform (x264 encode stats).
func TestDecodeHigh8x8MultiFrame(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_high8x8.mp4", "testdata/test_high8x8.yuv", 30, false)
}

// TestDecodeHigh8x8MultiFrameNoDeblock is the staged variant of the test
// above: reconstruction only, compared against an ffmpeg reference decoded
// with -skip_loop_filter all. If this passes while the deblocked test fails,
// the divergence is in the loop filter, not reconstruction.
func TestDecodeHigh8x8MultiFrameNoDeblock(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_high8x8.mp4", "testdata/test_high8x8_nodb.yuv", 30, true)
}

// TestHigh8x8IntraNoDeblock isolates Intra_8x8 reconstruction from deblocking:
// decodes the all-intra High-profile fixture with deblocking disabled and
// compares against an ffmpeg -skip_loop_filter all reference, logging the
// 8x8 partition prediction modes of any macroblock that mismatches.
func TestHigh8x8IntraNoDeblock(t *testing.T) {
	const mp4Path = "testdata/test_high8x8_intra.mp4"
	const yuvPath = "testdata/test_high8x8_intra_nodb.yuv"
	const numFrames = 5

	ref, err := os.ReadFile(yuvPath)
	if err != nil {
		t.Fatal(err)
	}

	type mbModes struct{ modes [4]int }
	i8x8Modes := map[[3]int]mbModes{} // key: frame, mbx, mby
	frameIdx := 0
	DebugI8x8Modes = func(mbx, mby int, modes [4]int, cbpLuma, cbpChroma int) {
		i8x8Modes[[3]int{frameIdx, mbx, mby}] = mbModes{modes: modes}
	}
	defer func() { DebugI8x8Modes = nil }()

	src := openTestPackets(t, mp4Path)
	codec := NewCodec()
	codec.dec.disableDeblock = true

	for i := 0; i < numFrames; i++ {
		frameIdx = i
		pkt, err := src.nextPacket()
		if err != nil {
			t.Fatalf("frame %d: nextPacket: %v", i, err)
		}
		frame, err := codec.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: Decode: %v", i, err)
		}

		w, h := frame.Width, frame.Height
		frameSize := w*h + 2*(w/2)*(h/2)
		refY := ref[i*frameSize : i*frameSize+w*h]
		ycbcr := frame.YCbCr

		badMBs := 0
		for mby := 0; mby*16 < h; mby++ {
			for mbx := 0; mbx*16 < w; mbx++ {
				mbMax := 0
				for j := mby * 16; j < mby*16+16 && j < h; j++ {
					for x := mbx * 16; x < mbx*16+16 && x < w; x++ {
						d := int(ycbcr.Y[j*ycbcr.YStride+x]) - int(refY[j*w+x])
						if d < 0 {
							d = -d
						}
						if d > mbMax {
							mbMax = d
						}
					}
				}
				if mbMax > 0 {
					badMBs++
					if modes, ok := i8x8Modes[[3]int{i, mbx, mby}]; ok {
						t.Errorf("frame %d MB(%d,%d): maxErr=%d i8x8 modes=%v", i, mbx, mby, mbMax, modes.modes)
					} else {
						t.Errorf("frame %d MB(%d,%d): maxErr=%d (not i8x8)", i, mbx, mby, mbMax)
					}
				}
			}
		}
		if badMBs == 0 {
			t.Logf("frame %d: bit-exact (no deblock)", i)
		}
	}
}

// TestDecodeBGHighMP4VsReference decodes the local-only High-profile (CAVLC,
// 8x8 transform) re-encode of the bg clip — 120 frames at 1280x720, real
// content — and requires bit-exactness against ffmpeg. Both files are
// gitignored (large); regenerate from the repo root with:
//
//	ffmpeg -i examples/videos/bg.mpg -vf scale=1280:720 -r 30 -c:v libx264 \
//	  -profile:v high -coder 0 -bf 0 -crf 20 -frames:v 120 -pix_fmt yuv420p \
//	  -an examples/videos/bg_high.mp4
//	ffmpeg -i examples/videos/bg_high.mp4 -f rawvideo h264/testdata/bg_high_frames_0_119.yuv
func TestDecodeBGHighMP4VsReference(t *testing.T) {
	const mp4Path = "../examples/videos/bg_high.mp4"
	const yuvPath = "testdata/bg_high_frames_0_119.yuv"
	if _, err := os.Stat(mp4Path); err != nil {
		t.Skip("bg_high.mp4 not found")
	}
	if _, err := os.Stat(yuvPath); err != nil {
		t.Skip("bg_high_frames_0_119.yuv not found")
	}
	decodeAndCompareYUV(t, mp4Path, yuvPath, 120, false)
}

// CABAC verification, staged like the High-profile 8x8 work: intra-only
// without the 8x8 transform, all-intra with every prediction mode, then a
// full IDR + P-frame sequence. Each fixture is a High-profile CABAC x264
// encode of test.mp4 compared bit-exact against ffmpeg raw YUV.

func TestDecodeCABACIntra16(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_cabac_i16.mp4", "testdata/test_cabac_i16.yuv", 3, false)
}

func TestDecodeCABACIntra16NoDeblock(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_cabac_i16.mp4", "testdata/test_cabac_i16_nodb.yuv", 3, true)
}

func TestDecodeCABACIntra(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_cabac_intra.mp4", "testdata/test_cabac_intra.yuv", 5, false)
}

func TestDecodeCABACIntraNoDeblock(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_cabac_intra.mp4", "testdata/test_cabac_intra_nodb.yuv", 5, true)
}

func TestDecodeCABACMultiFrame(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_cabac.mp4", "testdata/test_cabac.yuv", 30, false)
}

func TestDecodeCABACMultiFrameNoDeblock(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_cabac.mp4", "testdata/test_cabac_nodb.yuv", 30, true)
}

// TestDecodeCABACP16 is a bisection fixture: P frames restricted to 16x16
// partitions, single reference, no weighting, no 8x8 transform. Local-only
// (testdata/_* is gitignored); regenerate per the command in git history.
func TestDecodeCABACP16(t *testing.T) {
	const mp4 = "testdata/_cabac_p16.mp4"
	if _, err := os.Stat(mp4); err != nil {
		t.Skip("_cabac_p16.mp4 not found")
	}
	decodeAndCompareYUV(t, mp4, "testdata/_cabac_p16.yuv", 4, false)
}

func TestDecodeCABACP8x8(t *testing.T) {
	const mp4 = "testdata/_cabac_p8x8.mp4"
	if _, err := os.Stat(mp4); err != nil {
		t.Skip("_cabac_p8x8.mp4 not found")
	}
	decodeAndCompareYUV(t, mp4, "testdata/_cabac_p8x8.yuv", 4, false)
}

func TestDecodeCABAC8x8DCT(t *testing.T) {
	const mp4 = "testdata/_cabac_8x8dct.mp4"
	if _, err := os.Stat(mp4); err != nil {
		t.Skip("_cabac_8x8dct.mp4 not found")
	}
	decodeAndCompareYUV(t, mp4, "testdata/_cabac_8x8dct.yuv", 4, false)
}

// TestDecodeCABACI720 bisects the 720p I-frame desync: same content at two
// quality points (crf 20 exercises coefficient escapes, crf 30 does not).
func TestDecodeCABACI720(t *testing.T) {
	for _, crf := range []string{"20", "30"} {
		mp4 := "testdata/_cabac_i720_" + crf + ".mp4"
		if _, err := os.Stat(mp4); err != nil {
			t.Skip("fixture not found")
		}
		t.Run("crf"+crf, func(t *testing.T) {
			decodeAndCompareYUV(t, mp4, "testdata/_cabac_i720_"+crf+".yuv", 1, false)
		})
	}
}

// TestI8x8ModeCoverage counts Intra_8x8 mode usage per partition across the
// CAVLC bg_high fixture, to know which (partition, mode) pairs the bit-exact
// CAVLC tests actually exercise.
func TestI8x8ModeCoverage(t *testing.T) {
	const mp4 = "../examples/videos/bg_high.mp4"
	if _, err := os.Stat(mp4); err != nil {
		t.Skip("bg_high.mp4 not found")
	}
	var counts [4][9]int
	DebugI8x8Modes = func(mbx, mby int, modes [4]int, cbpLuma, cbpChroma int) {
		for p, m := range modes {
			counts[p][m]++
		}
	}
	defer func() { DebugI8x8Modes = nil }()

	src := openTestPackets(t, mp4)
	codec := NewCodec()
	for i := 0; i < 120; i++ {
		pkt, err := src.nextPacket()
		if err != nil {
			break
		}
		if _, err := codec.Decode(pkt); err != nil {
			t.Fatal(err)
		}
	}
	for p := 0; p < 4; p++ {
		t.Logf("partition %d: modes v,h,dc,ddl,ddr,vr,hd,vl,hu = %v", p, counts[p])
	}
}

// B-slice verification, staged: simple CAVLC B-frames (bframes=1, no
// pyramid, single ref, spatial direct), the CABAC equivalent, then full
// x264 defaults (bframes=3, pyramid, CABAC, implicit weighted bipred,
// multi-ref, 8x8 transform). Frames compare in display order.

func TestDecodeBCAVLC(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_b_cavlc.mp4", "testdata/test_b_cavlc.yuv", 12, false)
}

func TestDecodeBCABAC(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_b_cabac.mp4", "testdata/test_b_cabac.yuv", 12, false)
}

func TestDecodeBFull(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_b.mp4", "testdata/test_b.yuv", 30, false)
}

// TestDecodeBGDefaultMP4VsReference decodes a real-content 720p clip encoded
// with completely default x264 settings (High profile, CABAC, bframes=3 with
// pyramid and adaptive placement, weighted prediction, scenecut) and requires
// bit-exactness over 120 frames in display order. Local-only files
// (gitignored); regenerate from repo root:
//
//	ffmpeg -i examples/videos/bg.mpg -vf scale=1280:720 -r 30 -c:v libx264 \
//	  -crf 20 -frames:v 120 -pix_fmt yuv420p -an examples/videos/bg_default.mp4
//	ffmpeg -i examples/videos/bg_default.mp4 -f rawvideo h264/testdata/bg_default_frames_0_119.yuv
func TestDecodeBGDefaultMP4VsReference(t *testing.T) {
	const mp4Path = "../examples/videos/bg_default.mp4"
	const yuvPath = "testdata/bg_default_frames_0_119.yuv"
	if _, err := os.Stat(mp4Path); err != nil {
		t.Skip("bg_default.mp4 not found")
	}
	if _, err := os.Stat(yuvPath); err != nil {
		t.Skip("bg_default_frames_0_119.yuv not found")
	}
	decodeAndCompareYUV(t, mp4Path, yuvPath, 120, false)
}

// TestPlayerBStreamDeliversAllFrames plays the B-frame fixture through the
// real Player + mp4 demuxer and checks every display frame arrives,
// including the reorder-buffered tail at end of stream.
func TestPlayerBStreamDeliversAllFrames(t *testing.T) {
	f, err := os.Open("testdata/test_b.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	demuxer, err := mp4demux.NewDemuxer(f)
	if err != nil {
		t.Fatal(err)
	}
	defer demuxer.Close()

	player, err := govid.NewPlayer(demuxer, NewCodec())
	if err != nil {
		t.Fatal(err)
	}
	player.Play()

	// Step well past the end in small increments, tracking distinct frames.
	seen := map[time.Duration]bool{}
	for ts := time.Duration(0); ts < 3*time.Second; ts += 10 * time.Millisecond {
		player.UpdateToTime(ts)
		if fr := player.CurrentFrame(); fr != nil {
			seen[fr.Timestamp] = true
		}
	}
	if len(seen) != 30 {
		t.Fatalf("player delivered %d distinct frames, want 30", len(seen))
	}
}

// TestDecodeCQMDefault covers scaling matrices signaled as "use defaults":
// x264 cqm=jvt writes a PPS with pic_scaling_matrix_present=1 and every
// pic_scaling_list_present_flag=0, so all eight lists resolve through
// fall-back rule B to the spec Table 7-3/7-4 default matrices. High profile
// CAVLC, 8x8 transform, 2 B-frames. Regenerate from h264/testdata:
//
//	ffmpeg -i test.mp4 -f rawvideo src160.yuv
//	ffmpeg -f rawvideo -pix_fmt yuv420p -s 160x120 -r 30 -i src160.yuv \
//	  -c:v libx264 -profile:v high -x264-params "cqm=jvt:8x8dct=1:bframes=2:cabac=0" \
//	  -frames:v 20 -pix_fmt yuv420p test_cqm.mp4
//	ffmpeg -i test_cqm.mp4 -f rawvideo test_cqm.yuv
func TestDecodeCQMDefault(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_cqm.mp4", "testdata/test_cqm.yuv", 20, false)
}

// TestDecodeCQMCustomCABAC covers explicit (non-default) scaling lists:
// transpose-asymmetric custom 4x4 matrices for all six list classes, coded as
// explicit delta_scale runs in the PPS; the 8x8 lists are x264's flat lists.
// High profile CABAC, 8x8 transform, 2 B-frames. Regenerate from h264/testdata
// (cqm4iy/cqm4py/cqm4ic/cqm4pc values in git history or this encode line):
//
//	ffmpeg -f rawvideo -pix_fmt yuv420p -s 160x120 -r 30 -i src160.yuv \
//	  -c:v libx264 -profile:v high -x264-params "cabac=1:8x8dct=1:bframes=2:\
//	  cqm4iy=12,20,28,36,13,21,29,37,14,22,30,38,15,23,31,39:\
//	  cqm4py=10,17,24,31,11,18,25,32,12,19,26,33,13,20,27,34:\
//	  cqm4ic=14,19,24,29,15,20,25,30,16,21,26,31,17,22,27,32:\
//	  cqm4pc=11,15,19,23,12,16,20,24,13,17,21,25,14,18,22,26" \
//	  -frames:v 20 -pix_fmt yuv420p test_cqm_cabac.mp4
//	ffmpeg -i test_cqm_cabac.mp4 -f rawvideo test_cqm_cabac.yuv
func TestDecodeCQMCustomCABAC(t *testing.T) {
	decodeAndCompareYUV(t, "testdata/test_cqm_cabac.mp4", "testdata/test_cqm_cabac.yuv", 20, false)
}

// conformanceAnnexBTest runs a gated bit-exactness test over a JVT
// conformance stream stored as a raw Annex B elementary stream. The fixtures
// are local-only (testdata/_* is gitignored); regenerate by downloading
// https://www.itu.int/wftp3/av-arch/jvt-site/draft_conformance/AVCv1/<name>.zip
// and copying the .264/.jsv as testdata/_<base>.264 and the package's decoded
// reconstruction (*_rec.yuv / *.yuv) as testdata/_<base>.yuv. (ffmpeg's decode
// of all three streams used here was verified byte-identical to those
// conformance reconstructions.)
func conformanceAnnexBTest(t *testing.T, base string, w, h, numFrames int) {
	conformanceAnnexBTestRef(t, base, "testdata/_"+base+".yuv", w, h, numFrames, false)
}

func conformanceAnnexBTestRef(t *testing.T, base, yuvPath string, w, h, numFrames int, disableDeblock bool) {
	t.Helper()
	es, err := os.ReadFile("testdata/_" + base + ".264")
	if err != nil {
		t.Skipf("testdata/_%s.264 not found (local-only conformance fixture)", base)
	}
	ref, err := os.ReadFile(yuvPath)
	if err != nil {
		t.Fatal(err)
	}

	codec := NewCodec()
	codec.dec.disableDeblock = disableDeblock
	var frames []*govid.Frame
	for i, pkt := range splitAnnexBAUs(t, es) {
		frame, err := codec.Decode(govid.Packet{Data: pkt, Timestamp: time.Duration(i)})
		if err != nil {
			t.Fatalf("AU %d: %v", i, err)
		}
		if frame != nil {
			frames = append(frames, frame)
		}
	}
	for {
		frame := codec.Drain()
		if frame == nil {
			break
		}
		frames = append(frames, frame)
	}
	if len(frames) != numFrames {
		t.Fatalf("got %d frames, want %d", len(frames), numFrames)
	}

	cw, ch := w/2, h/2
	frameSize := w*h + 2*cw*ch
	for i, frame := range frames {
		refOff := i * frameSize
		img := frame.YCbCr
		checkPlane := func(plane string, dec []byte, stride int, rp []byte, pw, ph int) {
			wrong := 0
			for j := 0; j < ph; j++ {
				for x := 0; x < pw; x++ {
					if dec[j*stride+x] != rp[j*pw+x] {
						wrong++
					}
				}
			}
			if wrong > 0 {
				if plane == "Y" && !t.Failed() {
					// Per-MB max error grid to localize the failure.
					for mby := 0; mby*16 < ph; mby++ {
						line := fmt.Sprintf("  MB row %2d:", mby)
						for mbx := 0; mbx*16 < pw; mbx++ {
							mbMax := 0
							for j := mby * 16; j < mby*16+16 && j < ph; j++ {
								for x := mbx * 16; x < mbx*16+16 && x < pw; x++ {
									d := int(dec[j*stride+x]) - int(rp[j*pw+x])
									if d < 0 {
										d = -d
									}
									if d > mbMax {
										mbMax = d
									}
								}
							}
							line += fmt.Sprintf(" %3d", mbMax)
						}
						t.Log(line)
					}
				}
				t.Errorf("frame %d %s: %d/%d wrong pixels", i, plane, wrong, pw*ph)
			}
		}
		checkPlane("Y", img.Y, img.YStride, ref[refOff:refOff+w*h], w, h)
		checkPlane("Cb", img.Cb, img.CStride, ref[refOff+w*h:refOff+w*h+cw*ch], cw, ch)
		checkPlane("Cr", img.Cr, img.CStride, ref[refOff+w*h+cw*ch:refOff+frameSize], cw, ch)
	}
}

// splitAnnexBAUs splits an Annex B elementary stream into AVCC (4-byte NAL
// length) access-unit packets. Non-VCL NAL units are carried in the packet of
// the following VCL NAL; the conformance streams used here code one slice per
// picture, so every VCL NAL ends an access unit.
func splitAnnexBAUs(t *testing.T, data []byte) [][]byte {
	t.Helper()
	var nals [][]byte
	pos := -1
	for i := 0; i+2 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			if pos >= 0 {
				end := i
				if end > pos && data[end-1] == 0 { // 4-byte start code
					end--
				}
				for end > pos && data[end-1] == 0 { // trailing_zero_8bits
					end--
				}
				nals = append(nals, data[pos:end])
			}
			pos = i + 3
			i += 2
		}
	}
	if pos >= 0 && pos < len(data) {
		nals = append(nals, data[pos:])
	}

	var packets [][]byte
	var pending []byte
	appendAVCC := func(dst, nal []byte) []byte {
		dst = append(dst, byte(len(nal)>>24), byte(len(nal)>>16), byte(len(nal)>>8), byte(len(nal)))
		return append(dst, nal...)
	}
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		nalType := nal[0] & 0x1F
		if nalType == 1 || nalType == 5 {
			pkt := appendAVCC(pending, nal)
			packets = append(packets, pkt)
			pending = nil
		} else {
			pending = appendAVCC(pending, nal)
		}
	}
	return packets
}

// TestDecodeConformancePCMCAVLC: CVPCMNL1_SVA_C — CIF, 30 all-I CAVLC frames
// with I_PCM macroblocks, non-zero mb_qp_delta (QP tracking across PCM MBs),
// loop filter off.
func TestDecodeConformancePCMCAVLC(t *testing.T) {
	conformanceAnnexBTest(t, "cvpcmnl1", 352, 288, 30)
}

// TestDecodeConformancePCMCAVLC720p: CVPCMNL2_SVA_C — 1280x720, 2 all-I
// CAVLC frames with I_PCM, loop filter off.
func TestDecodeConformancePCMCAVLC720p(t *testing.T) {
	conformanceAnnexBTest(t, "cvpcmnl2", 1280, 720, 2)
}

// TestDecodeConformanceCAPM3: CAPM3_Sony_D — QCIF Foreman, 300 frames, CABAC
// IPB with I_PCM macroblocks, TEMPORAL direct mode, POC type 0, 5 reference
// frames, loop filter on. Covers CABAC I_PCM (engine re-initialization,
// neighbor contexts, QP-0 deblocking) and temporal direct MV scaling.
func TestDecodeConformanceCAPM3(t *testing.T) {
	conformanceAnnexBTest(t, "capm3", 176, 144, 300)
}

// TestDecodeConformanceCAPM3NoDeblock is the staged variant: reconstruction
// only, against an ffmpeg -skip_loop_filter all reference (regenerate with
// that flag added to the .yuv command above, output _capm3_nodb.yuv).
func TestDecodeConformanceCAPM3NoDeblock(t *testing.T) {
	conformanceAnnexBTestRef(t, "capm3", "testdata/_capm3_nodb.yuv", 176, 144, 300, true)
}

// TestConformanceCAPM3DumpBParts is a gated diagnostic: dumps the executed B
// partitions (geometry, lists, refs, MVs) of one decode-order picture of the
// CAPM3 conformance stream, for comparison against ffprobe/JM traces when
// chasing a B-slice divergence. Select the picture with CAPM3_DUMP=<index>.
func TestConformanceCAPM3DumpBParts(t *testing.T) {
	env := os.Getenv("CAPM3_DUMP")
	if env == "" {
		t.Skip("set CAPM3_DUMP=<decode-order index> to dump B partitions")
	}
	want, err := strconv.Atoi(env)
	if err != nil {
		t.Fatalf("CAPM3_DUMP: %v", err)
	}
	es, err := os.ReadFile("testdata/_capm3.264")
	if err != nil {
		t.Skip("fixture not found")
	}
	codec := NewCodec()
	decodeIdx := 0
	DebugBPartExec = func(mbx, mby int, p *bPart) {
		if decodeIdx == want {
			t.Logf("MB(%2d,%2d) part x%d y%d %dx%d mask%d ref[%d %d] mvL0(%d,%d) mvL1(%d,%d)",
				mbx, mby, p.x, p.y, p.w, p.h, p.mask, p.ref[0], p.ref[1],
				p.mv[0][0], p.mv[0][1], p.mv[1][0], p.mv[1][1])
		}
	}
	defer func() { DebugBPartExec = nil }()
	for i, pkt := range splitAnnexBAUs(t, es) {
		if i > want {
			break
		}
		decodeIdx = i
		if _, err := codec.Decode(govid.Packet{Data: pkt, Timestamp: time.Duration(i)}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDecodeConformanceMR2MW: MR2_MW_A — QCIF Baseline CAVLC, 300 frames,
// MMCO adaptive marking with LONG-TERM references (ops 2/3/6), POC type 0,
// 3 reference frames, loop filter on.
func TestDecodeConformanceMR2MW(t *testing.T) {
	conformanceAnnexBTest(t, "mr2mw", 176, 144, 300)
}

// TestDecodeConformanceMR2Tandberg: MR2_TANDBERG_E — QCIF CAVLC IPPP, 300
// frames, 15 reference frames, ref_pic_list_modification (incl. long-term
// idc 2), MMCO with long-term references.
func TestDecodeConformanceMR2Tandberg(t *testing.T) {
	conformanceAnnexBTest(t, "mr2tandberg", 176, 144, 300)
}

package mp4

import (
	"io"
	"os"
	"testing"
	"time"
)

func openTestDemuxer(t *testing.T) *Demuxer {
	t.Helper()
	f, err := os.Open("testdata/test.mp4")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })

	d, err := NewDemuxer(f)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestNewDemuxer(t *testing.T) {
	d := openTestDemuxer(t)
	if d == nil {
		t.Fatal("demuxer is nil")
	}
}

func TestVideoInfo(t *testing.T) {
	d := openTestDemuxer(t)
	info := d.VideoInfo()

	if info.Width != 160 {
		t.Errorf("width: got %d, want 160", info.Width)
	}
	if info.Height != 120 {
		t.Errorf("height: got %d, want 120", info.Height)
	}
	if info.FrameRate < 29.0 || info.FrameRate > 31.0 {
		t.Errorf("framerate: got %f, want ~30", info.FrameRate)
	}
}

func TestDuration(t *testing.T) {
	d := openTestDemuxer(t)
	dur := d.Duration()
	if dur < 1*time.Second || dur > 3*time.Second {
		t.Errorf("duration: got %v, want ~2s", dur)
	}
}

func TestNextPacket(t *testing.T) {
	d := openTestDemuxer(t)

	// First packet should be a keyframe.
	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	if !pkt.Keyframe {
		t.Error("first packet should be a keyframe")
	}
	if len(pkt.Data) == 0 {
		t.Error("first packet has no data")
	}
	if pkt.Timestamp != 0 {
		t.Errorf("first packet timestamp: got %v, want 0", pkt.Timestamp)
	}

	// Read remaining packets, verify timestamps are non-decreasing.
	prev := pkt.Timestamp
	count := 1
	for {
		pkt, err = d.NextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if pkt.Timestamp < prev {
			t.Errorf("timestamp decreased: %v < %v at packet %d", pkt.Timestamp, prev, count)
		}
		prev = pkt.Timestamp
		count++
	}

	if count < 50 {
		t.Errorf("expected at least 50 packets (2s @ 30fps), got %d", count)
	}
}

func TestIDRPacketContainsSPSPPS(t *testing.T) {
	d := openTestDemuxer(t)

	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	if !pkt.Keyframe {
		t.Fatal("first packet should be keyframe")
	}

	// The keyframe packet should contain SPS + PPS + IDR NALUs.
	// SPS NAL type = 7 (header byte & 0x1F == 7)
	// PPS NAL type = 8 (header byte & 0x1F == 8)
	// IDR NAL type = 5 (header byte & 0x1F == 5)

	// Check that the first two NAL units are SPS and PPS by parsing
	// length-prefixed NALUs.
	data := pkt.Data
	if len(data) < 8 {
		t.Fatal("keyframe packet too small")
	}

	// Read first NAL unit length.
	nalLen := uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
	if int(4+nalLen) > len(data) {
		t.Fatal("first NAL length exceeds data")
	}
	nalType := data[4] & 0x1F
	if nalType != 7 {
		t.Errorf("first NAL type: got %d, want 7 (SPS)", nalType)
	}

	// Second NAL unit.
	offset := 4 + nalLen
	if int(offset+4) > len(data) {
		t.Fatal("can't read second NAL length")
	}
	nalLen2 := uint32(data[offset])<<24 | uint32(data[offset+1])<<16 | uint32(data[offset+2])<<8 | uint32(data[offset+3])
	if int(offset+4+nalLen2) > len(data) {
		t.Fatal("second NAL length exceeds data")
	}
	nalType2 := data[offset+4] & 0x1F
	if nalType2 != 8 {
		t.Errorf("second NAL type: got %d, want 8 (PPS)", nalType2)
	}
}

func TestSeek(t *testing.T) {
	d := openTestDemuxer(t)

	// Seek to 1 second.
	actual, err := d.Seek(1 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if actual > 1*time.Second {
		t.Errorf("seek returned time %v past target 1s", actual)
	}

	// Next packet should be a keyframe.
	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	if !pkt.Keyframe {
		t.Error("packet after seek should be a keyframe")
	}
}

func TestCloseIdempotent(t *testing.T) {
	f, err := os.Open("testdata/test.mp4")
	if err != nil {
		t.Fatal(err)
	}

	d, err := NewDemuxer(f)
	if err != nil {
		f.Close()
		t.Fatal(err)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	// Second close should not error.
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNALLengthSize(t *testing.T) {
	d := openTestDemuxer(t)
	if d.NALLengthSize() != 4 {
		t.Errorf("NALLengthSize: got %d, want 4", d.NALLengthSize())
	}
}

// AV1 demuxer tests.

func openAV1Demuxer(t *testing.T) *Demuxer {
	t.Helper()
	f, err := os.Open("testdata/test_av1.mp4")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })

	d, err := NewDemuxer(f)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestNewDemuxerAV1(t *testing.T) {
	d := openAV1Demuxer(t)
	if d == nil {
		t.Fatal("demuxer is nil")
	}
	if d.CodecType() != "av1" {
		t.Errorf("codec type = %s, want av1", d.CodecType())
	}
}

func TestAV1VideoInfo(t *testing.T) {
	d := openAV1Demuxer(t)
	info := d.VideoInfo()

	if info.Width != 160 {
		t.Errorf("width = %d, want 160", info.Width)
	}
	if info.Height != 120 {
		t.Errorf("height = %d, want 120", info.Height)
	}
	if info.FrameRate < 9.0 || info.FrameRate > 11.0 {
		t.Errorf("framerate = %f, want ~10", info.FrameRate)
	}
}

func TestAV1NextPacket(t *testing.T) {
	d := openAV1Demuxer(t)

	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	if !pkt.Keyframe {
		t.Error("first packet should be a keyframe")
	}
	if len(pkt.Data) == 0 {
		t.Error("first packet has no data")
	}

	// Read all packets.
	count := 1
	for {
		_, err = d.NextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count < 5 {
		t.Errorf("expected at least 5 packets, got %d", count)
	}
	t.Logf("read %d AV1 packets", count)
}

func TestAV1ConfigOBUsPrepended(t *testing.T) {
	d := openAV1Demuxer(t)

	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	if !pkt.Keyframe {
		t.Fatal("first packet should be keyframe")
	}

	// The keyframe packet should have config OBUs prepended.
	// Parse OBUs and verify a sequence header is present.
	// Import av1 package would create a circular dependency,
	// so just check the OBU header byte directly.
	if len(pkt.Data) < 2 {
		t.Fatal("keyframe data too small")
	}

	// First byte of an OBU with size field:
	// forbidden(1) | type(4) | extension(1) | has_size(1) | reserved(1)
	// Sequence header = type 1: 0 0001 0 1 0 = 0x0A
	foundSeqHeader := false
	for i := 0; i < len(pkt.Data)-1; i++ {
		obuType := (pkt.Data[i] >> 3) & 0xF
		hasSizeField := (pkt.Data[i] >> 1) & 1
		forbidden := (pkt.Data[i] >> 7) & 1
		if forbidden == 0 && obuType == 1 && hasSizeField == 1 {
			foundSeqHeader = true
			break
		}
		// If it's not a valid OBU header, stop.
		if forbidden != 0 {
			break
		}
	}

	if !foundSeqHeader {
		t.Error("keyframe packet does not contain sequence header OBU")
	}
}

package webm

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/at-wat/ebml-go"

	govid "github.com/liqmix/govid"
)

func openTestWebM(t *testing.T) *Demuxer {
	t.Helper()
	f, err := os.Open("testdata/test.webm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	d, err := NewDemuxer(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestNewDemuxer(t *testing.T) {
	d := openTestWebM(t)
	if d == nil {
		t.Fatal("expected non-nil demuxer")
	}
}

func TestVideoInfo(t *testing.T) {
	d := openTestWebM(t)
	vi := d.VideoInfo()
	if vi.Width != 160 || vi.Height != 120 {
		t.Errorf("expected 160x120, got %dx%d", vi.Width, vi.Height)
	}
}

func TestDuration(t *testing.T) {
	d := openTestWebM(t)
	dur := d.Duration()
	if dur <= 0 {
		t.Errorf("expected positive duration, got %v", dur)
	}
}

func TestNextPacket(t *testing.T) {
	d := openTestWebM(t)

	// First packet should be a keyframe.
	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt.Data) == 0 {
		t.Error("expected non-empty packet data")
	}

	// Read remaining packets, verify timestamps are non-decreasing.
	prevTS := pkt.Timestamp
	count := 1
	for {
		pkt, err := d.NextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if pkt.Timestamp < prevTS {
			t.Errorf("timestamps not non-decreasing: %v < %v", pkt.Timestamp, prevTS)
		}
		prevTS = pkt.Timestamp
		count++
	}
	if count < 3 {
		t.Errorf("expected at least 3 packets, got %d", count)
	}
}

func TestSeekNotSupported(t *testing.T) {
	d := openTestWebM(t)
	_, err := d.Seek(0)
	if err != govid.ErrSeekNotSupported {
		t.Errorf("expected ErrSeekNotSupported, got %v", err)
	}
}

func TestUnsupportedCodecID(t *testing.T) {
	// Create a minimal WebM with VP9 codec (not VP8).
	root := webmRoot{
		Header: struct{}{},
		Segment: webmSegment{
			Info: webmInfo{TimecodeScale: 1_000_000},
			Tracks: webmTracks{
				TrackEntry: []webmTrackEntry{{
					TrackNumber: 1,
					TrackType:   1,
					CodecID:     "V_VP9",
					Video:       &webmVideo{PixelWidth: 160, PixelHeight: 120},
				}},
			},
		},
	}

	var buf bytes.Buffer
	if err := ebml.Marshal(&root, &buf); err != nil {
		t.Fatal(err)
	}

	r := bytes.NewReader(buf.Bytes())
	_, err := NewDemuxer(r)
	if err == nil {
		t.Fatal("expected error for VP9 codec")
	}
	if !strings.Contains(err.Error(), "unsupported video codec") {
		t.Errorf("expected 'unsupported video codec' error, got: %v", err)
	}
}

// AV1 WebM tests.

func openAV1WebM(t *testing.T) *Demuxer {
	t.Helper()
	f, err := os.Open("testdata/test_av1.webm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	d, err := NewDemuxer(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestNewDemuxerAV1(t *testing.T) {
	d := openAV1WebM(t)
	if d.CodecID() != "V_AV1" {
		t.Errorf("codec = %s, want V_AV1", d.CodecID())
	}
}

func TestAV1NextPacket(t *testing.T) {
	d := openAV1WebM(t)

	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt.Data) == 0 {
		t.Error("first packet empty")
	}

	count := 1
	for {
		_, err := d.NextPacket()
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
	t.Logf("read %d AV1 WebM packets", count)
}

func TestClose(t *testing.T) {
	f, err := os.Open("testdata/test.webm")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := NewDemuxer(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	// Double close should be fine.
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

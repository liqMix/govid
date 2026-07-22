package h264

// Decode-performance benchmark for pure-Go H.264 Baseline (govid) vs
// gen2brain/mpeg (C-style ported MPEG-1). Matched at 1280x720 30fps, 120
// frames. Reports wall-clock fps, per-frame ms, and relative cost ratio so
// we can decide whether H.264 needs optimization before integrating into
// slapTrax.

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/gen2brain/mpeg"
)

const (
	benchMP4        = "../examples/videos/bg.mp4"
	benchHighMP4    = "../examples/videos/bg_high.mp4"
	benchCabacMP4   = "../examples/videos/bg_cabac.mp4"
	benchDefaultMP4 = "../examples/videos/bg_default.mp4"
	benchMPG        = "../examples/videos/bg_720p30.mpg"
	benchMaxFrames  = 120
)

// TestBenchmarkDecode is a one-shot timing harness (not a Go benchmark) so it
// runs once during `go test -run TestBenchmarkDecode` and prints clean output.
func TestBenchmarkDecode(t *testing.T) {
	if _, err := os.Stat(benchMP4); err != nil {
		t.Skip("bench mp4 missing")
	}
	if _, err := os.Stat(benchMPG); err != nil {
		t.Skip("bench mpg missing — run: ffmpeg -i bg.mpg -vf scale=1280:720 -r 30 -c:v mpeg1video -qscale:v 8 -frames:v 120 bg_720p30.mpg")
	}

	// --- H.264 via govid ---
	h264Total, h264Frames := benchH264(t, benchMP4)
	// --- H.264 High profile (8x8 transform, CAVLC) via govid, if present ---
	var highTotal time.Duration
	highFrames := 0
	if _, err := os.Stat(benchHighMP4); err == nil {
		highTotal, highFrames = benchH264(t, benchHighMP4)
	}
	// --- H.264 High profile CABAC via govid, if present ---
	var cabacTotal time.Duration
	cabacFrames := 0
	if _, err := os.Stat(benchCabacMP4); err == nil {
		cabacTotal, cabacFrames = benchH264(t, benchCabacMP4)
	}
	// --- H.264 full x264 defaults (CABAC + B-frames) via govid, if present ---
	var defTotal time.Duration
	defFrames := 0
	if _, err := os.Stat(benchDefaultMP4); err == nil {
		defTotal, defFrames = benchH264(t, benchDefaultMP4)
	}
	// --- MPEG-1 via gen2brain/mpeg ---
	mpg1Total, mpg1Frames := benchMPEG1(t)

	t.Logf("")
	t.Logf("=== Decode benchmark (1280x720, %d frames) ===", benchMaxFrames)
	t.Logf("")
	t.Logf("%-28s %10s %10s %10s", "codec", "total", "ms/frame", "fps")
	t.Logf("%-28s %10s %10s %10s", "-----", "-----", "--------", "---")
	report := func(name string, total time.Duration, frames int) {
		perFrame := float64(total.Microseconds()) / float64(frames) / 1000.0
		fps := 1000.0 / perFrame
		t.Logf("%-28s %10.0fms %9.2fms %9.1f", name, float64(total.Microseconds())/1000.0, perFrame, fps)
	}
	report("govid h264 (pure Go)", h264Total, h264Frames)
	if highFrames > 0 {
		report("govid h264 High 8x8 (pure Go)", highTotal, highFrames)
	}
	if cabacFrames > 0 {
		report("govid h264 High CABAC (pure Go)", cabacTotal, cabacFrames)
	}
	if defFrames > 0 {
		report("govid h264 x264 defaults +B", defTotal, defFrames)
	}
	report("gen2brain/mpeg (MPEG-1)", mpg1Total, mpg1Frames)

	ratio := float64(h264Total) / float64(mpg1Total)
	t.Logf("")
	t.Logf("h264 / mpeg-1 ratio: %.2fx (h264 is %.2fx the time of mpeg-1)", ratio, ratio)
}

// benchH264 decodes an H.264 MP4 file through govid and returns the
// wall-clock spent inside Codec.Decode() plus the demuxer's NextPacket(),
// skipping the file-open / demux-init costs.
func benchH264(t *testing.T, path string) (time.Duration, int) {
	src := openTestPackets(t, path)
	codec := NewCodec()

	// Warm-up: decode 3 frames so the codec's allocation paths are hot.
	for i := 0; i < 3; i++ {
		pkt, err := src.nextPacket()
		if err != nil {
			t.Fatalf("h264 warm pkt: %v", err)
		}
		if _, err := codec.Decode(pkt); err != nil {
			t.Fatalf("h264 warm decode: %v", err)
		}
	}

	// Fresh source so we decode from frame 0 in the measured window.
	src = openTestPackets(t, path)
	codec = NewCodec()

	start := time.Now()
	frames := 0
	for frames < benchMaxFrames {
		pkt, err := src.nextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("h264 pkt: %v", err)
		}
		f, err := codec.Decode(pkt)
		if err != nil {
			t.Fatalf("h264 decode frame %d: %v", frames, err)
		}
		if f != nil {
			frames++
		}
	}
	return time.Since(start), frames
}

// benchMPEG1 decodes the matched MPEG-1 file via gen2brain/mpeg.
func benchMPEG1(t *testing.T) (time.Duration, int) {
	f, err := os.Open(benchMPG)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := mpeg.New(f)
	if err != nil {
		t.Fatal(err)
	}
	m.SetAudioEnabled(false)

	// Warm-up
	for i := 0; i < 3; i++ {
		if m.DecodeVideo() == nil {
			break
		}
	}

	// Rewind by reopening.
	f2, err := os.Open(benchMPG)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	m2, err := mpeg.New(f2)
	if err != nil {
		t.Fatal(err)
	}
	m2.SetAudioEnabled(false)

	start := time.Now()
	frames := 0
	for frames < benchMaxFrames {
		frame := m2.DecodeVideo()
		if frame == nil {
			break
		}
		frames++
	}
	return time.Since(start), frames
}

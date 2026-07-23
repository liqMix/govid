package main

import (
	"bytes"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	govid "github.com/liqmix/govid"
	"github.com/liqmix/govid/ebiv"
	"github.com/liqmix/govid/h264"
	mp4pkg "github.com/liqmix/govid/mp4"
)

func TestRationalFPS(t *testing.T) {
	tests := []struct {
		name     string
		fps      float64
		num, den uint32
	}{
		{"30", 30, 30, 1},
		{"25", 25, 25, 1},
		{"60", 60, 60, 1},
		{"ntsc 30", 30000.0 / 1001.0, 30000, 1001},
		{"ntsc 24", 24000.0 / 1001.0, 24000, 1001},
		{"ntsc 60", 60000.0 / 1001.0, 60000, 1001},
		{"unusual", 15.5, 15500, 1000},
		{"zero falls back", 0, 30, 1},
		{"negative falls back", -1, 30, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			num, den := rationalFPS(tt.fps)
			if num != tt.num || den != tt.den {
				t.Errorf("rationalFPS(%v) = %d/%d, want %d/%d", tt.fps, num, den, tt.num, tt.den)
			}
		})
	}
}

// TestTranscodeRealClip runs the full pipeline the tool exists for and measures
// the result against the source: demux and decode a real H.264 clip, re-encode
// it as EBIV, decode that back, and confirm the container reports what was
// written and that every frame reconstructs the source at a reasonable PSNR.
//
// It skips when the sample clip is absent, since large fixtures are local-only.
func TestTranscodeRealClip(t *testing.T) {
	const src = "../videos/baker_h264.mp4"
	if _, err := os.Stat(src); err != nil {
		t.Skipf("sample clip %s not available", src)
	}

	const frames = 4
	source := decodeSource(t, src, frames)

	dst := filepath.Join(t.TempDir(), "out.ebiv")
	if err := transcode(src, dst, transcodeOptions{qp: 18, gop: frames, limit: frames}); err != nil {
		t.Fatalf("transcode: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}

	d, err := ebiv.NewDemuxer(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}
	defer d.Close()
	if got := d.FrameCount(); got != frames {
		t.Errorf("FrameCount = %d, want %d", got, frames)
	}

	c := ebiv.NewCodec()
	rawBytes := 0
	for i := 0; i < frames; i++ {
		pkt, err := d.NextPacket()
		if err != nil {
			t.Fatalf("NextPacket %d: %v", i, err)
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			t.Fatalf("Decode %d: %v", i, err)
		}
		psnr := imagePSNR(source[i], frame.YCbCr)
		if psnr < 34 {
			t.Errorf("frame %d: PSNR %.1f dB against source, want >= 34", i, psnr)
		}
		g := geometryFor(frame.Width, frame.Height)
		rawBytes += g
		if i == 0 {
			t.Logf("frame 0: %dx%d, PSNR %.1f dB", frame.Width, frame.Height, psnr)
		}
	}
	t.Logf("%d frames: %d compressed bytes vs %d raw (%.1fx smaller)",
		frames, len(data), rawBytes, float64(rawBytes)/float64(len(data)))
}

// TestTranscodeCapture measures the codec on a real capture named by the
// EBIV_CAPTURE environment variable: it transcodes EBIV_FRAMES frames (default
// 60) at EBIV_QP (default 20), decodes them back, and reports per-frame size,
// compression ratio, and PSNR against the source. It skips when EBIV_CAPTURE is
// unset, so it never runs in normal CI.
//
//	EBIV_CAPTURE=path EBIV_FRAMES=90 EBIV_QP=18 go test ./examples/ebiv -run TestTranscodeCapture -v -timeout 30m
func TestTranscodeCapture(t *testing.T) {
	path := os.Getenv("EBIV_CAPTURE")
	if path == "" {
		t.Skip("set EBIV_CAPTURE to a video file to measure the codec on real content")
	}
	frames := envInt("EBIV_FRAMES", 60)
	qp := envInt("EBIV_QP", 20)
	gop := envInt("EBIV_GOP", 30)

	source := decodeSource(t, path, frames)

	dst := filepath.Join(t.TempDir(), "capture.ebiv")
	start := time.Now()
	if err := transcode(path, dst, transcodeOptions{qp: qp, gop: gop, limit: frames}); err != nil {
		t.Fatalf("transcode: %v", err)
	}
	encodeDur := time.Since(start)

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	d, err := ebiv.NewDemuxer(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}
	defer d.Close()
	info := d.VideoInfo()

	c := ebiv.NewCodec()
	var minPSNR, sumPSNR float64 = 1e9, 0
	var rawBytes int
	decodeStart := time.Now()
	for i := 0; i < frames; i++ {
		pkt, err := d.NextPacket()
		if err != nil {
			t.Fatalf("NextPacket %d: %v", i, err)
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			t.Fatalf("Decode %d: %v", i, err)
		}
		p := imagePSNR(source[i], frame.YCbCr)
		sumPSNR += p
		if p < minPSNR {
			minPSNR = p
		}
		rawBytes += geometryFor(frame.Width, frame.Height)
	}
	decodeDur := time.Since(decodeStart)

	compBytes := len(data)
	t.Logf("capture: %dx%d @ %.3g fps, %d frames, qp=%d gop=%d",
		info.Width, info.Height, info.FrameRate, frames, qp, gop)
	t.Logf("size:    %.2f MiB compressed vs %.2f MiB raw YUV (%.1fx smaller), %d bytes/frame",
		mib(compBytes), mib(rawBytes), float64(rawBytes)/float64(compBytes), compBytes/frames)
	t.Logf("quality: mean PSNR %.1f dB, worst %.1f dB", sumPSNR/float64(frames), minPSNR)
	t.Logf("speed:   encode %.0f ms/frame, decode %.2f ms/frame",
		encodeDur.Seconds()*1000/float64(frames), decodeDur.Seconds()*1000/float64(frames))
}

// TestCompareCodecs measures every format govid can decode against the same
// source: it decodes each pre-encoded file, times the decode, and computes PSNR
// against the decoded original. The encoded files are produced out of band
// (ffmpeg for H.264/VP8/MPEG-1, the ebiv transcoder for EBIV) so this test only
// measures. It skips unless EBIV_COMPARE_DIR and EBIV_COMPARE_SRC are set.
//
//	EBIV_COMPARE_DIR=dir EBIV_COMPARE_SRC=orig.mp4 EBIV_FRAMES=3600 \
//	  go test ./examples/ebiv -run TestCompareCodecs -v -timeout 40m
func TestCompareCodecs(t *testing.T) {
	dir := os.Getenv("EBIV_COMPARE_DIR")
	src := os.Getenv("EBIV_COMPARE_SRC")
	if dir == "" || src == "" {
		t.Skip("set EBIV_COMPARE_DIR and EBIV_COMPARE_SRC to run the codec comparison")
	}
	frames := envInt("EBIV_FRAMES", 3600)

	entries := []struct{ name, file string }{
		{"H.264 (x264 crf20)", "h264.mp4"},
		{"VP8 (libvpx 8M VBR)", "vp8.webm"},
		{"MPEG-1 (q4)", "mpeg1.mpg"},
		{"EBIV (qp22)", "out.ebiv"},
	}

	var h264Size int64
	t.Logf("%-22s %9s %8s %13s %9s", "codec", "size MB", "PSNR dB", "decode ms/f", "vs H.264")
	for _, e := range entries {
		path := filepath.Join(dir, e.file)
		if _, err := os.Stat(path); err != nil {
			t.Logf("%-22s (missing: %v)", e.name, err)
			continue
		}
		size := fileSizeOf(t, path)
		if e.file == "h264.mp4" {
			h264Size = size
		}
		speed := decodeSpeed(t, path, frames)
		psnr := comparePSNR(t, src, path, frames)
		ratio := 0.0
		if h264Size > 0 {
			ratio = float64(size) / float64(h264Size)
		}
		t.Logf("%-22s %9.2f %8.1f %13.2f %8.2fx", e.name, mib(int(size)), psnr, speed, ratio)
	}
}

func fileSizeOf(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// decodeSpeed times a pure decode loop over the first `frames` frames and
// returns milliseconds per frame.
func decodeSpeed(t *testing.T, path string, frames int) float64 {
	t.Helper()
	d, c, closeFn := openAny(t, path)
	defer closeFn()
	start := time.Now()
	n := 0
	for n < frames {
		if _, err := readFrame(d, c); err != nil {
			break
		}
		n++
	}
	if n == 0 {
		return 0
	}
	return time.Since(start).Seconds() * 1000 / float64(n)
}

// comparePSNR decodes the original and a codec file in lockstep and returns the
// whole-clip PSNR. Streaming avoids holding thousands of 1080p frames in memory.
func comparePSNR(t *testing.T, origPath, codecPath string, frames int) float64 {
	t.Helper()
	od, oc, oClose := openAny(t, origPath)
	defer oClose()
	cd, cc, cClose := openAny(t, codecPath)
	defer cClose()

	var sumSq float64
	var count int64
	for i := 0; i < frames; i++ {
		of, e1 := readFrame(od, oc)
		cf, e2 := readFrame(cd, cc)
		if e1 != nil || e2 != nil {
			break
		}
		s, n := frameSSE(of.YCbCr, cf.YCbCr)
		sumSq += s
		count += n
	}
	if sumSq == 0 || count == 0 {
		return 99
	}
	return 10 * math.Log10(255*255*float64(count)/sumSq)
}

func frameSSE(a, b *image.YCbCr) (float64, int64) {
	var sumSq float64
	var count int64
	w := min(a.Rect.Dx(), b.Rect.Dx())
	h := min(a.Rect.Dy(), b.Rect.Dy())
	cw, ch := (w+1)/2, (h+1)/2
	plane := func(pa, pb []byte, sa, sb, pw, ph int) {
		for y := 0; y < ph; y++ {
			ra := pa[y*sa:]
			rb := pb[y*sb:]
			for x := 0; x < pw; x++ {
				d := float64(int(ra[x]) - int(rb[x]))
				sumSq += d * d
			}
		}
		count += int64(pw * ph)
	}
	plane(a.Y, b.Y, a.YStride, b.YStride, w, h)
	plane(a.Cb, b.Cb, a.CStride, b.CStride, cw, ch)
	plane(a.Cr, b.Cr, a.CStride, b.CStride, cw, ch)
	return sumSq, count
}

func openAny(t *testing.T, path string) (govid.Demuxer, govid.Codec, func()) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d, c, err := openSource(f, path)
	if err != nil {
		f.Close()
		t.Fatalf("open %s: %v", path, err)
	}
	return d, c, func() {
		d.Close()
		f.Close()
	}
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func mib(b int) float64 { return float64(b) / (1 << 20) }

// decodeSource decodes the first n frames of an H.264/MP4 clip into independent
// images, deep-copied so the codec's buffer reuse cannot alias them.
func decodeSource(t *testing.T, path string, n int) []*image.YCbCr {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	demux, err := mp4pkg.NewDemuxer(f)
	if err != nil {
		t.Fatal(err)
	}
	defer demux.Close()

	codec := h264.NewCodec()
	var out []*image.YCbCr
	for len(out) < n {
		frame, err := readFrame(demux, codec)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode source frame %d: %v", len(out), err)
		}
		out = append(out, cloneYCbCr(frame.YCbCr))
	}
	if len(out) < n {
		t.Fatalf("source has only %d frames, want %d", len(out), n)
	}
	return out
}

func cloneYCbCr(src *image.YCbCr) *image.YCbCr {
	dst := *src
	dst.Y = append([]byte(nil), src.Y...)
	dst.Cb = append([]byte(nil), src.Cb...)
	dst.Cr = append([]byte(nil), src.Cr...)
	return &dst
}

// geometryFor mirrors the ebiv plane geometry for the raw-size readout; only
// the packed size is needed here.
func geometryFor(w, h int) int {
	cw, ch := (w+1)/2, (h+1)/2
	return w*h + 2*cw*ch
}

// imagePSNR computes whole-frame PSNR between two 4:2:0 images of equal size.
func imagePSNR(a, b *image.YCbCr) float64 {
	var sumSq float64
	var count int
	planePSNR := func(pa, pb []byte, sa, sb, w, h int) {
		for y := 0; y < h; y++ {
			ra := pa[y*sa : y*sa+w]
			rb := pb[y*sb : y*sb+w]
			for x := 0; x < w; x++ {
				d := float64(int(ra[x]) - int(rb[x]))
				sumSq += d * d
			}
		}
		count += w * h
	}
	w, h := a.Rect.Dx(), a.Rect.Dy()
	cw, ch := (w+1)/2, (h+1)/2
	planePSNR(a.Y, b.Y, a.YStride, b.YStride, w, h)
	planePSNR(a.Cb, b.Cb, a.CStride, b.CStride, cw, ch)
	planePSNR(a.Cr, b.Cr, a.CStride, b.CStride, cw, ch)
	if sumSq == 0 {
		return 1e9
	}
	return 10 * math.Log10(255*255*float64(count)/sumSq)
}

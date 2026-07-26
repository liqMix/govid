package main

// The M6 corpus comparison: EBIV vs VP9 (the design plan's original yardstick)
// and x264 across a set of representative clips, at matched PSNR.
//
// Every clip is first normalized to raw YUV 4:2:0 by ffmpeg, and every codec —
// including the PSNR measurements — works from that one master, so no
// per-decoder mpeg1/h264 reconstruction differences can bias the anchor.
// x264 crf20 sets the reference PSNR; VP9 (libvpx-vp9) and EBIV are then
// bracket-encoded around that PSNR and their size at the exact anchor PSNR is
// interpolated (ln size, linear in dB) between the two bracketing points —
// the same methodology as the M4 measurements in .docs/ebiv-gap-analysis.md.
//
// Requires ffmpeg+ffprobe with libx264 and libvpx-vp9 on PATH. Anchor encodes
// are cached in EBIV_CORPUS_WORK (default <os temp>/ebiv-corpus), so re-runs
// only re-measure. Skips unless EBIV_CORPUS is set.
//
// Two tiers share this one test:
//
// Iteration (~10 min): a short clip list, few frames, fixed EBIV qp points,
// no VP9. EBIV files are re-encoded every run (no EBIV_TAG), so the numbers
// always reflect the current build:
//
//	EBIV_CORPUS=@.docs/ebiv-corpus-dev.txt EBIV_FRAMES=240 \
//	EBIV_QPS=18,22 EBIV_SKIP_VP9=1 EBIV_CORPUS_TWOPASS=1 \
//	  go test ./examples/ebiv -run TestCorpusCompare -v -timeout 2h
//
// Gate (hours): the full list, 600 frames, bracket search, VP9 context, and
// an explicit EBIV_TAG naming the build so the run can resume from cache:
//
//	EBIV_CORPUS=@.docs/ebiv-corpus-full.txt EBIV_FRAMES=600 \
//	EBIV_CORPUS_TWOPASS=1 EBIV_TAG=v3-golden-sdh \
//	  go test ./examples/ebiv -run TestCorpusCompare -v -timeout 8h
//
// EBIV_CORPUS is a ;-separated clip list, or @file naming a text file with
// one clip per line (# comments allowed). EBIV_TAG opts EBIV encodes into the
// cache under that name — only use a tag that uniquely names the encoder
// build, and bump it after any encoder change; without a tag EBIV always
// re-encodes. Anchor caches (master/x264/VP9) are keyed by content and
// settings only and are always safe to reuse.
//
// VP9 uses -deadline good -cpu-used 2 (not the slowest/best), a few percent
// off its best size, noted with the results.

import (
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liqmix/govid/ebiv"
)

func TestCorpusCompare(t *testing.T) {
	clips := os.Getenv("EBIV_CORPUS")
	if clips == "" {
		t.Skip("set EBIV_CORPUS to a ;-separated list of clips to run the corpus comparison")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	frames := envInt("EBIV_FRAMES", 600)
	work := os.Getenv("EBIV_CORPUS_WORK")
	if work == "" {
		work = filepath.Join(os.TempDir(), "ebiv-corpus")
	}

	var results []clipResult
	for _, clip := range corpusClips(t, clips) {
		r, err := measureClip(t, clip, work, frames)
		if err != nil {
			t.Errorf("%s: %v", clipName(clip), err)
			continue
		}
		results = append(results, r)
		vp9Part := ""
		if r.vp9Size > 0 {
			vp9Part = fmt.Sprintf(", %.2fx VP9", float64(r.ebivSize)/float64(r.vp9Size))
		}
		t.Logf("%-28s %dx%d@%.3g  x264 %.2fdB %s | x264-g %.2fdB %s | EBIV %s = %.2fx x264, %.2fx x264-g%s | decode %.2f/%.2f ms/f (1T/par), vp9 C %.2f",
			r.name, r.w, r.h, r.fps, r.anchorPSNR, fmtMB(r.x264Size),
			r.anchorGPSNR, fmtMB(r.x264gSize),
			fmtMB(r.ebivSize), float64(r.ebivSize)/float64(r.x264Size),
			float64(r.ebivSizeG)/float64(r.x264gSize), vp9Part,
			r.ebivDecode1T, r.ebivDecodePar, r.vp9Decode1T)
	}
	if len(results) == 0 {
		t.Fatal("no clips measured")
	}

	var vsX264, vsX264g, vsVP9 []float64
	for _, r := range results {
		vsX264 = append(vsX264, float64(r.ebivSize)/float64(r.x264Size))
		vsX264g = append(vsX264g, float64(r.ebivSizeG)/float64(r.x264gSize))
		if r.vp9Size > 0 {
			vsVP9 = append(vsVP9, float64(r.ebivSize)/float64(r.vp9Size))
		}
	}
	t.Logf("=== corpus summary (%d clips, %d frames each, matched-PSNR sizes) ===", len(results), frames)
	t.Logf("EBIV vs x264 (default keyint): median %.2fx, range %.2fx-%.2fx", median(vsX264), minOf(vsX264), maxOf(vsX264))
	t.Logf("EBIV vs x264 (matched ~1s cadence): median %.2fx, range %.2fx-%.2fx", median(vsX264g), minOf(vsX264g), maxOf(vsX264g))
	if len(vsVP9) > 0 {
		t.Logf("EBIV vs VP9 (context only): median %.2fx, range %.2fx-%.2fx", median(vsVP9), minOf(vsVP9), maxOf(vsVP9))
	}
	t.Logf("EBIV encode: %s; VP9 good/cpu-used 2 (not best), default cadence", map[bool]string{true: "two-pass (final-asset config)", false: "single-pass -fast (~+3.5%)"}[os.Getenv("EBIV_CORPUS_TWOPASS") != ""])
}

type clipResult struct {
	name          string
	w, h          int
	fps           float64
	anchorPSNR    float64 // x264 default keyframe cadence
	anchorGPSNR   float64 // x264 at EBIV's ~1s seek cadence (-g ≈ fps)
	x264Size      int64
	x264gSize     int64
	vp9Size       int64 // interpolated at anchorPSNR
	ebivSize      int64 // interpolated at anchorPSNR
	ebivSizeG     int64 // interpolated at anchorGPSNR
	ebivDecode1T  float64
	ebivDecodePar float64
	vp9Decode1T   float64 // libvpx via ffmpeg -threads 1 (C reference, not pure Go)
}

type ratePoint struct {
	psnr float64
	size int64
}

func measureClip(t *testing.T, clip, work string, frames int) (clipResult, error) {
	name := clipName(clip)
	dir := filepath.Join(work, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return clipResult{}, err
	}
	w, h, fpsNum, fpsDen, err := probeClip(clip)
	if err != nil {
		return clipResult{}, fmt.Errorf("probe: %w", err)
	}
	fps := float64(fpsNum) / float64(fpsDen)

	master := filepath.Join(dir, fmt.Sprintf("master_%d.yuv", frames))
	if err := runCached(master, "ffmpeg", "-v", "error", "-y", "-i", clip,
		"-frames:v", strconv.Itoa(frames), "-f", "rawvideo", "-pix_fmt", "yuv420p", master); err != nil {
		return clipResult{}, fmt.Errorf("extract master: %w", err)
	}
	frameSize := w*h + 2*(w/2)*(h/2)
	if fi, err := os.Stat(master); err != nil || fi.Size() < int64(frameSize) {
		return clipResult{}, fmt.Errorf("master extraction produced no frames")
	}
	actualFrames := int(fileSize(master) / int64(frameSize))
	if actualFrames < frames {
		frames = actualFrames // short clip: use what exists
	}

	// Anchor: x264 crf20 (the same operating point as every earlier EBIV
	// measurement), PSNR computed against the shared YUV master.
	x264File := filepath.Join(dir, fmt.Sprintf("x264_crf20_%d.mp4", frames))
	if err := runCached(x264File, "ffmpeg", "-v", "error", "-y",
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", fmt.Sprintf("%dx%d", w, h),
		"-r", fmt.Sprintf("%d/%d", fpsNum, fpsDen), "-i", master,
		"-frames:v", strconv.Itoa(frames),
		"-c:v", "libx264", "-crf", "20", "-preset", "medium", "-pix_fmt", "yuv420p", x264File); err != nil {
		return clipResult{}, fmt.Errorf("x264 encode: %w", err)
	}
	anchorPSNR, err := psnrGovid(t, x264File, master, w, h, frames)
	if err != nil {
		return clipResult{}, fmt.Errorf("x264 psnr: %w", err)
	}

	// Cadence-matched x264: the same ~1 s keyframe interval EBIV pays for.
	// Seeking is a product requirement for BGA, so this is the like-for-like
	// anchor; scene-cut keys stay enabled, so x264 may place extra keys, never
	// fewer. The default-cadence anchor above stays on record alongside it.
	gop := max(1, int(math.Round(fps)))
	x264gFile := filepath.Join(dir, fmt.Sprintf("x264_crf20_g%d_%d.mp4", gop, frames))
	if err := runCached(x264gFile, "ffmpeg", "-v", "error", "-y",
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", fmt.Sprintf("%dx%d", w, h),
		"-r", fmt.Sprintf("%d/%d", fpsNum, fpsDen), "-i", master,
		"-frames:v", strconv.Itoa(frames),
		"-c:v", "libx264", "-crf", "20", "-preset", "medium", "-g", strconv.Itoa(gop),
		"-pix_fmt", "yuv420p", x264gFile); err != nil {
		return clipResult{}, fmt.Errorf("x264 -g encode: %w", err)
	}
	anchorGPSNR, err := psnrGovid(t, x264gFile, master, w, h, frames)
	if err != nil {
		return clipResult{}, fmt.Errorf("x264 -g psnr: %w", err)
	}

	// VP9: bracket the anchor PSNR with crf points, interpolate size. Skipped
	// in iteration runs (EBIV_SKIP_VP9) — VP9 is context, not the gate.
	var vp9Points []ratePoint
	if os.Getenv("EBIV_SKIP_VP9") == "" {
		vp9Points, err = bracket(anchorPSNR, 30, 6, 4, 63, func(crf int) (ratePoint, error) {
			f := filepath.Join(dir, fmt.Sprintf("vp9_crf%d_%d.webm", crf, frames))
			if err := runCached(f, "ffmpeg", "-v", "error", "-y",
				"-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", fmt.Sprintf("%dx%d", w, h),
				"-r", fmt.Sprintf("%d/%d", fpsNum, fpsDen), "-i", master,
				"-frames:v", strconv.Itoa(frames),
				"-c:v", "libvpx-vp9", "-crf", strconv.Itoa(crf), "-b:v", "0",
				"-deadline", "good", "-cpu-used", "2", "-row-mt", "1", f); err != nil {
				return ratePoint{}, err
			}
			p, err := psnrFFmpeg(f, master, w, h, frames)
			return ratePoint{psnr: p, size: fileSize(f)}, err
		})
		if err != nil {
			return clipResult{}, fmt.Errorf("vp9: %w", err)
		}
	}

	// EBIV: ~1s GOP, auto tiles. EBIV_CORPUS_TWOPASS selects the full two-pass
	// RDO encode (the final-asset configuration); default is the single-pass
	// fast mode. EBIV_QPS pins fixed operating points (iteration — no bracket
	// search); otherwise points are bracketed around the anchor. Without an
	// EBIV_TAG the encode always re-runs, so results reflect the current
	// build; a tag opts into the cache under that build name.
	twoPass := os.Getenv("EBIV_CORPUS_TWOPASS") != ""
	suffix := ""
	if twoPass {
		suffix = "_2p"
	}
	if tag := os.Getenv("EBIV_TAG"); tag != "" {
		suffix = "_" + tag + suffix
	}
	var lastEbiv string
	measureEbiv := func(qp int) (ratePoint, error) {
		f := filepath.Join(dir, fmt.Sprintf("ebiv_v%d_qp%d_gop%d_%d%s.ebiv", ebiv.Version, qp, gop, frames, suffix))
		if os.Getenv("EBIV_TAG") == "" {
			os.Remove(f) // untagged: never trust a file from another build
		}
		if err := encodeEbivFromYUV(f, master, w, h, fpsNum, fpsDen, qp, gop, frames, twoPass); err != nil {
			return ratePoint{}, err
		}
		lastEbiv = f
		p, err := psnrGovid(t, f, master, w, h, frames)
		return ratePoint{psnr: p, size: fileSize(f)}, err
	}
	var ebivPoints []ratePoint
	if qps := envQPList(); qps != nil {
		for _, qp := range qps {
			pt, err := measureEbiv(qp)
			if err != nil {
				return clipResult{}, fmt.Errorf("ebiv qp%d: %w", qp, err)
			}
			ebivPoints = append(ebivPoints, pt)
		}
		sort.Slice(ebivPoints, func(i, j int) bool { return ebivPoints[i].psnr < ebivPoints[j].psnr })
	} else {
		ebivPoints, err = bracket(anchorPSNR, 18, 4, 0, 63, measureEbiv)
		if err != nil {
			return clipResult{}, fmt.Errorf("ebiv: %w", err)
		}
	}

	r := clipResult{
		name: name, w: w, h: h, fps: fps,
		anchorPSNR:  anchorPSNR,
		anchorGPSNR: anchorGPSNR,
		x264Size:    fileSize(x264File),
		x264gSize:   fileSize(x264gFile),
		ebivSize:    interpolateSize(ebivPoints, anchorPSNR),
		ebivSizeG:   interpolateSize(ebivPoints, anchorGPSNR),
	}

	// Decode timings on the EBIV point nearest the anchor (lastEbiv is fine —
	// decode cost barely moves with qp) and the corresponding VP9 file.
	r.ebivDecodePar = decodeSpeed(t, lastEbiv, frames)
	prev := runtime.GOMAXPROCS(1)
	r.ebivDecode1T = decodeSpeed(t, lastEbiv, frames)
	runtime.GOMAXPROCS(prev)
	if len(vp9Points) > 0 {
		r.vp9Size = interpolateSize(vp9Points, anchorPSNR)
		r.vp9Decode1T = ffmpegDecodeSpeed(filepath.Join(dir, nearestVP9(dir, vp9Points, frames)), frames)
	}
	return r, nil
}

// bracket measures rate points along a quality parameter (higher = lower
// quality for both VP9 crf and EBIV qp) until two points straddle the target
// PSNR, up to five encodes. It returns every point measured.
func bracket(target float64, start, step, lo, hi int, measure func(int) (ratePoint, error)) ([]ratePoint, error) {
	seen := map[int]ratePoint{}
	p := start
	for range 5 {
		if _, ok := seen[p]; !ok {
			pt, err := measure(p)
			if err != nil {
				return nil, err
			}
			seen[p] = pt
		}
		above, below := false, false
		for _, pt := range seen {
			if pt.psnr >= target {
				above = true
			} else {
				below = true
			}
		}
		if above && below {
			break
		}
		if seen[p].psnr >= target {
			p = min(p+step, hi) // too good: reduce quality
		} else {
			p = max(p-step, lo)
		}
	}
	pts := make([]ratePoint, 0, len(seen))
	for _, pt := range seen {
		pts = append(pts, pt)
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].psnr < pts[j].psnr })
	return pts, nil
}

// interpolateSize evaluates ln(size) linearly in PSNR at the target, using the
// two points bracketing it (or the nearest two when the bracket fell short —
// then it is an extrapolation from the closest measurements).
func interpolateSize(pts []ratePoint, target float64) int64 {
	if len(pts) == 1 {
		return pts[0].size
	}
	i := sort.Search(len(pts), func(i int) bool { return pts[i].psnr >= target })
	if i == 0 {
		i = 1
	}
	if i == len(pts) {
		i = len(pts) - 1
	}
	a, b := pts[i-1], pts[i]
	if b.psnr == a.psnr {
		return a.size
	}
	f := (target - a.psnr) / (b.psnr - a.psnr)
	ln := math.Log(float64(a.size)) + f*(math.Log(float64(b.size))-math.Log(float64(a.size)))
	return int64(math.Exp(ln))
}

// encodeEbivFromYUV encodes a raw YUV 4:2:0 master into an EBIV file with the
// shipping transcoder configuration (auto tiles); twoPass selects the full
// real-cost RDO encode, else the single-pass fast mode.
func encodeEbivFromYUV(dst, master string, w, h, fpsNum, fpsDen, qp, gop, frames int, twoPass bool) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	in, err := os.Open(master)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer out.Close()

	opts := []ebiv.EncoderOption{
		ebiv.WithIntra(qp), ebiv.WithGOP(gop), ebiv.WithAutoTiles(runtime.NumCPU()),
	}
	if !twoPass {
		opts = append(opts, ebiv.WithFastEncode())
	}
	enc, err := ebiv.NewEncoder(out, ebiv.Config{
		Width: w, Height: h, FPSNum: uint32(fpsNum), FPSDen: uint32(fpsDen),
	}, opts...)
	if err != nil {
		return err
	}
	for i := 0; i < frames; i++ {
		img, err := readYUVFrame(in, w, h)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := enc.WriteFrame(img); err != nil {
			return err
		}
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// readYUVFrame reads one packed 4:2:0 frame into a fresh image.YCbCr.
func readYUVFrame(r io.Reader, w, h int) (*image.YCbCr, error) {
	cw, ch := w/2, h/2
	buf := make([]byte, w*h+2*cw*ch)
	if _, err := io.ReadFull(r, buf); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, io.EOF
		}
		return nil, err
	}
	return &image.YCbCr{
		Y: buf[:w*h], Cb: buf[w*h : w*h+cw*ch], Cr: buf[w*h+cw*ch:],
		YStride: w, CStride: cw,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, w, h),
	}, nil
}

// psnrGovid decodes a govid-supported file and computes whole-clip PSNR
// against the raw YUV master.
func psnrGovid(t *testing.T, codecFile, master string, w, h, frames int) (float64, error) {
	t.Helper()
	d, c, closeFn := openAny(t, codecFile)
	defer closeFn()
	in, err := os.Open(master)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	var sumSq float64
	var count int64
	for i := 0; i < frames; i++ {
		ref, err := readYUVFrame(in, w, h)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		frame, err := readFrame(d, c)
		if err != nil {
			return 0, fmt.Errorf("decode frame %d: %w", i, err)
		}
		s, n := frameSSE(ref, frame.YCbCr)
		sumSq += s
		count += n
	}
	if count == 0 {
		return 0, fmt.Errorf("no frames compared")
	}
	if sumSq == 0 {
		return 99, nil
	}
	return 10 * math.Log10(255*255*float64(count)/sumSq), nil
}

// psnrFFmpeg decodes a file govid cannot (VP9) through an ffmpeg rawvideo pipe
// and computes whole-clip PSNR against the raw YUV master.
func psnrFFmpeg(codecFile, master string, w, h, frames int) (float64, error) {
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", codecFile,
		"-frames:v", strconv.Itoa(frames), "-f", "rawvideo", "-pix_fmt", "yuv420p", "-")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	defer cmd.Wait()

	in, err := os.Open(master)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	var sumSq float64
	var count int64
	for i := 0; i < frames; i++ {
		ref, err := readYUVFrame(in, w, h)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		dec, err := readYUVFrame(pipe, w, h)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("ffmpeg decode frame %d: %w", i, err)
		}
		s, n := frameSSE(ref, dec)
		sumSq += s
		count += n
	}
	if count == 0 {
		return 0, fmt.Errorf("no frames compared")
	}
	if sumSq == 0 {
		return 99, nil
	}
	return 10 * math.Log10(255*255*float64(count)/sumSq), nil
}

// ffmpegDecodeSpeed times a single-threaded libvpx decode as the C-reference
// decode cost (not comparable line-for-line with pure Go, but it is the
// software decoder the design plan's speed claim was made against).
func ffmpegDecodeSpeed(path string, frames int) float64 {
	start := time.Now()
	cmd := exec.Command("ffmpeg", "-v", "error", "-threads", "1", "-i", path,
		"-frames:v", strconv.Itoa(frames), "-f", "null", "-")
	if err := cmd.Run(); err != nil {
		return 0
	}
	return time.Since(start).Seconds() * 1000 / float64(frames)
}

// nearestVP9 returns the cached VP9 file name whose size matches one of the
// measured points, preferring the largest (highest-quality) bracketing point.
func nearestVP9(dir string, pts []ratePoint, frames int) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(pts) == 0 {
		return ""
	}
	want := pts[len(pts)-1].size
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "vp9_crf") && strings.HasSuffix(e.Name(), fmt.Sprintf("_%d.webm", frames)) {
			if fi, err := e.Info(); err == nil && fi.Size() == want {
				return e.Name()
			}
		}
	}
	return ""
}

// runCached runs the command unless its output file already exists (encode
// caching, so corpus re-runs only re-measure). The command must write outFile
// itself; a partial file from a failed run is removed.
func runCached(outFile string, name string, args ...string) error {
	if _, err := os.Stat(outFile); err == nil {
		return nil
	}
	cmd := exec.Command(name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.Remove(outFile)
		return fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func probeClip(path string) (w, h, fpsNum, fpsDen int, err error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) < 3 {
		return 0, 0, 0, 0, fmt.Errorf("ffprobe output %q", out)
	}
	w, _ = strconv.Atoi(parts[0])
	h, _ = strconv.Atoi(parts[1])
	if n, d, ok := strings.Cut(parts[2], "/"); ok {
		fpsNum, _ = strconv.Atoi(n)
		fpsDen, _ = strconv.Atoi(d)
	}
	if w == 0 || h == 0 || fpsNum == 0 || fpsDen == 0 {
		return 0, 0, 0, 0, fmt.Errorf("bad probe %q", out)
	}
	// Odd dimensions would break the packed 4:2:0 frame math; masters are even.
	if w%2 != 0 || h%2 != 0 {
		return 0, 0, 0, 0, fmt.Errorf("odd dimensions %dx%d", w, h)
	}
	return w, h, fpsNum, fpsDen, nil
}

// corpusClips resolves EBIV_CORPUS: a ;-separated list, or @file naming a
// text file with one clip path per line (blank lines and # comments skipped).
func corpusClips(t *testing.T, spec string) []string {
	t.Helper()
	raw := spec
	sep := ";"
	if strings.HasPrefix(spec, "@") {
		// go test runs in the package directory; accept repo-root-relative
		// list paths too.
		path := spec[1:]
		data, err := os.ReadFile(path)
		if err != nil && !filepath.IsAbs(path) {
			data, err = os.ReadFile(filepath.Join("..", "..", path))
		}
		if err != nil {
			t.Fatalf("EBIV_CORPUS list file: %v", err)
		}
		raw = string(data)
		sep = "\n"
	}
	var clips []string
	for _, line := range strings.Split(raw, sep) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		clips = append(clips, line)
	}
	return clips
}

// envQPList parses EBIV_QPS ("18,22") into fixed EBIV operating points, or
// nil when unset (bracket search).
func envQPList() []int {
	v := os.Getenv("EBIV_QPS")
	if v == "" {
		return nil
	}
	var qps []int
	for _, p := range strings.Split(v, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			qps = append(qps, n)
		}
	}
	return qps
}

func clipName(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	// Per-song BGA files are all named bg; use the song directory instead.
	if base == "bg" {
		return filepath.Base(filepath.Dir(path))
	}
	return base
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func fmtMB(b int64) string { return fmt.Sprintf("%.2fMB", float64(b)/(1<<20)) }

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func minOf(v []float64) float64 {
	m := v[0]
	for _, x := range v[1:] {
		m = math.Min(m, x)
	}
	return m
}

func maxOf(v []float64) float64 {
	m := v[0]
	for _, x := range v[1:] {
		m = math.Max(m, x)
	}
	return m
}

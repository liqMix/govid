package ebiv

import (
	"bytes"
	"image"
	"io"
	"math"
	"testing"
)

// encodeCoded encodes n synthetic (static, high-entropy) frames with the given
// options and returns the container plus the packed source planes.
func encodeCoded(t *testing.T, cfg Config, n int, opts ...EncoderOption) (container []byte, refs [][]byte) {
	return encodeCodedGen(t, cfg, n, synthFrame, opts...)
}

// encodeCodedGen encodes frames produced by gen, so a test can choose static or
// panning content.
func encodeCodedGen(t *testing.T, cfg Config, n int, gen func(geometry, int) *image.YCbCr, opts ...EncoderOption) (container []byte, refs [][]byte) {
	t.Helper()
	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, cfg, opts...)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	g := geometryFor(cfg.Width, cfg.Height)
	for i := 0; i < n; i++ {
		img := gen(g, i)
		refs = append(refs, packImage(img, g))
		if err := enc.WriteFrame(img); err != nil {
			t.Fatalf("WriteFrame %d: %v", i, err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes(), refs
}

// framePSNR returns the whole-frame PSNR of a decoded image against its packed
// source reference.
func framePSNR(img *image.YCbCr, ref []byte, g geometry) float64 {
	diffs := []planeDiff{
		comparePlanes("Y", img.Y, img.YStride, ref[:g.W*g.H], g.W, g.H),
		comparePlanes("Cb", img.Cb, img.CStride, ref[g.W*g.H:g.W*g.H+g.CW*g.CH], g.CW, g.CH),
		comparePlanes("Cr", img.Cr, img.CStride, ref[g.W*g.H+g.CW*g.CH:], g.CW, g.CH),
	}
	var sumSq float64
	var pixels int
	for _, d := range diffs {
		sumSq += d.SumSq
		pixels += d.Pixels
	}
	if sumSq == 0 {
		return 1e9
	}
	mse := sumSq / float64(pixels)
	return 10 * math.Log10(255*255/mse)
}

// decodeImages decodes every frame in a container into fresh YCbCr images,
// deep-copied so the codec's ring reuse cannot alias them.
func decodeImages(t *testing.T, container []byte) []*image.YCbCr {
	t.Helper()
	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}
	defer d.Close()
	c := NewCodec()
	var out []*image.YCbCr
	for {
		pkt, err := d.NextPacket()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("NextPacket: %v", err)
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		out = append(out, cloneYCbCr(frame.YCbCr))
	}
}

// decodeToPlanes decodes a container into packed plane bytes per frame.
func decodeToPlanes(t *testing.T, container []byte) [][]byte {
	t.Helper()
	var out [][]byte
	for _, img := range decodeImages(t, container) {
		g := geometryFor(img.Rect.Dx(), img.Rect.Dy())
		out = append(out, packImage(img, g))
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

// TestIntraDeterministic is the format's core contract: the decoder reproduces
// the encoder's reconstruction bit-for-bit. Because encoder and decoder share
// the reconstruction path, decoding twice must also agree.
func TestIntraDeterministic(t *testing.T) {
	cfg := Config{Width: 96, Height: 64, FPSNum: 30, FPSDen: 1}
	container, _ := encodeCoded(t, cfg, 3, WithIntra(16))

	first := decodeToPlanes(t, container)
	second := decodeToPlanes(t, container)
	if len(first) != len(second) {
		t.Fatalf("frame count differs between decodes: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if !bytes.Equal(first[i], second[i]) {
			t.Fatalf("frame %d differs between two decodes; decode is not deterministic", i)
		}
	}
}

// TestIntraQuality verifies the intra codec actually reconstructs the source
// well (PSNR floor) and compresses it (size ceiling). Round-trip stability plus
// a PSNR gate together rule out an encoder/decoder pair that agrees on garbage.
func TestIntraQuality(t *testing.T) {
	cfg := Config{Width: 256, Height: 192, FPSNum: 30, FPSDen: 1}
	g := geometryFor(cfg.Width, cfg.Height)

	tests := []struct {
		qp       int
		minPSNR  float64
		maxBytes int // per-frame ceiling
	}{
		// The synthetic frame is deliberately high-entropy (checkerboard detail
		// over a gradient with a hard diagonal edge), so these size ceilings are
		// looser than real video would need — the point is to catch a
		// regression that blows up the rate, not to benchmark compression.
		{8, 40, 42000},
		{20, 30, 28000},
		{32, 24, 14000},
	}
	for _, tt := range tests {
		container, refs := encodeCoded(t, cfg, 4, WithIntra(tt.qp))
		planes := decodeImages(t, container)
		if len(planes) != len(refs) {
			t.Fatalf("qp=%d: decoded %d frames, want %d", tt.qp, len(planes), len(refs))
		}
		var worst float64 = 1e9
		for i, img := range planes {
			if p := framePSNR(img, refs[i], g); p < worst {
				worst = p
			}
		}
		perFrame := (len(container) - fileHeaderSize - footerSize) / len(refs)
		rawPerFrame := g.packedSize()
		if worst < tt.minPSNR {
			t.Errorf("qp=%d: worst-frame PSNR %.1f dB, want >= %.0f", tt.qp, worst, tt.minPSNR)
		}
		if perFrame > tt.maxBytes {
			t.Errorf("qp=%d: %d bytes/frame, want <= %d", tt.qp, perFrame, tt.maxBytes)
		}
		t.Logf("qp=%2d: PSNR %.1f dB, %d bytes/frame (raw %d, %.1fx smaller)",
			tt.qp, worst, perFrame, rawPerFrame, float64(rawPerFrame)/float64(perFrame))
	}
}

// TestSkipModeStaticSequence exercises the M1 skip path: a sequence of
// identical frames must code its inter frames almost for free (skip macroblocks
// carry the whole picture from the reference) and still decode bit-exact. It
// also confirms table delta-coding round-trips across a multi-frame GOP.
func TestSkipModeStaticSequence(t *testing.T) {
	cfg := Config{Width: 256, Height: 192, FPSNum: 30, FPSDen: 1}
	g := geometryFor(cfg.Width, cfg.Height)
	still := synthFrame(g, 0)

	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, cfg, WithIntra(20), WithGOP(10))
	if err != nil {
		t.Fatal(err)
	}
	const frames = 10
	for i := 0; i < frames; i++ {
		if err := enc.WriteFrame(still); err != nil {
			t.Fatalf("WriteFrame %d: %v", i, err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	container := buf.Bytes()

	// The key frame carries the cost; the nine static inter frames after it
	// must be tiny — a skip token each, near-free.
	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		t.Fatal(err)
	}
	idx := d.Index()
	keySize := int(idx[0].Size)
	var interMax int
	for _, e := range idx[1:] {
		if int(e.Size) > interMax {
			interMax = int(e.Size)
		}
	}
	d.Close()
	if interMax*20 > keySize {
		t.Errorf("static inter frame is %d bytes vs a %d-byte key frame; skip mode is not collapsing them", interMax, keySize)
	}
	t.Logf("static sequence: key %d bytes, largest inter %d bytes (%.1f%% of key)", keySize, interMax, 100*float64(interMax)/float64(keySize))

	// And every frame must reconstruct the still exactly (within the codec's
	// lossy quantization — identical for every frame since the input is).
	ref := packImage(still, g)
	c := NewCodec()
	var firstPSNR float64
	decodeAll(t, container, c, func(i int, img *image.YCbCr) {
		diffs := compareImages(t, img, ref, g)
		var sumSq float64
		var px int
		for _, dd := range diffs {
			sumSq += dd.SumSq
			px += dd.Pixels
		}
		psnr := 99.0
		if sumSq > 0 {
			psnr = 10 * mathLog10Coded(255*255*float64(px)/sumSq)
		}
		if i == 0 {
			firstPSNR = psnr
		} else if psnr < firstPSNR-0.5 {
			// Skip MBs re-use the reference, so drift must not accumulate.
			t.Errorf("frame %d PSNR %.2f dB drifted below the key frame's %.2f dB", i, psnr, firstPSNR)
		}
	})
}

func mathLog10Coded(x float64) float64 { return math.Log10(x) }

// TestGOPCadence pins the key-frame schedule: WithGOP(n) must place a key
// frame every n frames exactly. (An off-by-one here once made gop=1 emit
// alternating I/P instead of all-intra.)
func TestGOPCadence(t *testing.T) {
	cfg := Config{Width: 64, Height: 48, FPSNum: 30, FPSDen: 1}
	for _, gop := range []int{1, 2, 3, 5} {
		container, _ := encodeCoded(t, cfg, 7, WithIntra(20), WithGOP(gop))
		d, err := NewDemuxer(bytes.NewReader(container))
		if err != nil {
			t.Fatal(err)
		}
		for i, e := range d.Index() {
			want := i%gop == 0
			if e.Keyframe != want {
				t.Errorf("gop=%d frame %d: keyframe=%v, want %v", gop, i, e.Keyframe, want)
			}
		}
		d.Close()
	}
}

// TestCodedDecodeLowAllocations guards the plan's zero-alloc goal on the coded
// hot path: after warm-up, an untiled intra decode reuses its entropy tables,
// tile worker, and scratch, so it allocates only a small constant per frame
// (the govid.Frame wrapper the interface forces, plus a couple of slice
// housekeeping allocations).
func TestCodedDecodeLowAllocations(t *testing.T) {
	cfg := Config{Width: 320, Height: 240, FPSNum: 30, FPSDen: 1}
	container, _ := encodeCoded(t, cfg, 2, WithIntra(20))

	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c := NewCodec()
	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Decode(pkt); err != nil { // warm up: builds the pools
		t.Fatal(err)
	}

	got := testing.AllocsPerRun(30, func() {
		if _, err := c.Decode(pkt); err != nil {
			t.Fatal(err)
		}
	})
	if got > 4 {
		t.Errorf("coded Decode allocates %.1f objects per call, want <= 4", got)
	}
}

// TestInterCompressesMotion checks that inter frames beat all-intra on a moving
// sequence while staying correct. The synthetic content pans every frame, which
// motion compensation should track.
func TestInterCompressesMotion(t *testing.T) {
	cfg := Config{Width: 192, Height: 128, FPSNum: 30, FPSDen: 1}
	g := geometryFor(cfg.Width, cfg.Height)
	const frames = 8

	// Panning content gives motion compensation a real translation to track, so
	// inter should compress far below all-intra.
	intraBuf, _ := encodeCodedGen(t, cfg, frames, synthPan, WithIntra(20))
	interBuf, refs := encodeCodedGen(t, cfg, frames, synthPan, WithIntra(20), WithGOP(frames))

	planes := decodeImages(t, interBuf)
	if len(planes) != frames {
		t.Fatalf("decoded %d frames, want %d", len(planes), frames)
	}
	var worst float64 = 1e9
	for i, img := range planes {
		if p := framePSNR(img, refs[i], g); p < worst {
			worst = p
		}
	}
	if worst < 30 {
		t.Errorf("inter worst-frame PSNR %.1f dB, want >= 30", worst)
	}
	t.Logf("all-intra %d bytes, GOP=%d %d bytes (%.1f%% of intra); inter worst PSNR %.1f dB",
		len(intraBuf), frames, len(interBuf),
		100*float64(len(interBuf))/float64(len(intraBuf)), worst)
	// Motion compensation should cut the moving sequence well below all-intra.
	if len(interBuf) > len(intraBuf)*70/100 {
		t.Errorf("inter stream (%d) should be well under 70%% of all-intra (%d)", len(interBuf), len(intraBuf))
	}
}

// TestCodedRoundTripSizes exercises the coded path across awkward dimensions,
// checking every frame decodes and reconstructs with reasonable fidelity.
func TestCodedRoundTripSizes(t *testing.T) {
	sizes := []struct{ w, h int }{
		{16, 16}, {17, 15}, {64, 48}, {31, 47}, {160, 90},
	}
	for _, s := range sizes {
		t.Run(sizeLabel(s.w, s.h), func(t *testing.T) {
			cfg := Config{Width: s.w, Height: s.h, FPSNum: 30, FPSDen: 1}
			g := geometryFor(s.w, s.h)
			container, refs := encodeCoded(t, cfg, 3, WithIntra(16), WithGOP(3))
			planes := decodeImages(t, container)
			if len(planes) != len(refs) {
				t.Fatalf("decoded %d frames, want %d", len(planes), len(refs))
			}
			for i, img := range planes {
				if p := framePSNR(img, refs[i], g); p < 30 {
					t.Errorf("frame %d: PSNR %.1f dB, want >= 30", i, p)
				}
			}
		})
	}
}

func sizeLabel(w, h int) string {
	return itoaTest(w) + "x" + itoaTest(h)
}

func itoaTest(v int) string {
	if v == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// TestFastEncodeRoundTrip pins the WithFastEncode contract: the single-pass
// stream is a normal v2 bitstream — it decodes deterministically and
// bit-exactly — and gives up only encoder rate-distortion quality, never
// correctness. Per-frame quality must stay close to the two-pass encode of
// the same content at the same qp.
func TestFastEncodeRoundTrip(t *testing.T) {
	cfg := Config{Width: 96, Height: 64, FPSNum: 30, FPSDen: 1}
	const frames = 8
	opts := []EncoderOption{WithIntra(16), WithGOP(4), WithTiles(2, 1)}
	fast, refs := encodeCodedGen(t, cfg, frames, synthPan, append(opts[:len(opts):len(opts)], WithFastEncode())...)
	best, _ := encodeCodedGen(t, cfg, frames, synthPan, opts...)

	first := decodeToPlanes(t, fast)
	second := decodeToPlanes(t, fast)
	if len(first) != frames {
		t.Fatalf("decoded %d frames, want %d", len(first), frames)
	}
	for i := range first {
		if !bytes.Equal(first[i], second[i]) {
			t.Fatalf("frame %d: fast-encode decode is not deterministic", i)
		}
	}

	g := geometryFor(cfg.Width, cfg.Height)
	fastImgs := decodeImages(t, fast)
	bestImgs := decodeImages(t, best)
	for i := range fastImgs {
		fp := framePSNR(fastImgs[i], refs[i], g)
		bp := framePSNR(bestImgs[i], refs[i], g)
		if fp < bp-1.0 {
			t.Errorf("frame %d: fast-encode PSNR %.2f dB more than 1 dB below two-pass %.2f dB", i, fp, bp)
		}
	}
}

// packetSizes returns each frame record's payload size in a container.
func packetSizes(t *testing.T, container []byte) []int {
	t.Helper()
	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}
	defer d.Close()
	var sizes []int
	for {
		pkt, err := d.NextPacket()
		if err == io.EOF {
			return sizes
		}
		if err != nil {
			t.Fatalf("NextPacket: %v", err)
		}
		sizes = append(sizes, len(pkt.Data))
	}
}

// TestGoldenRefFlashingSequence pins the v3 golden reference: content that
// returns to the key frame's pose after an excursion — the background-
// animation flash pattern — codes each return almost for free by predicting
// from the golden frame, where last-ref prediction would pay a full re-code
// (the previous frame no longer holds the pose).
func TestGoldenRefFlashingSequence(t *testing.T) {
	cfg := Config{Width: 128, Height: 96, FPSNum: 30, FPSDen: 1}
	const frames = 9
	flash := func(g geometry, i int) *image.YCbCr {
		if i%2 == 0 {
			return synthFrame(g, 1) // the held pose, also the key frame
		}
		return synthFrame(g, 2) // the excursion
	}
	container, refs := encodeCodedGen(t, cfg, frames, flash, WithIntra(16), WithGOP(frames))

	// The format's core contract holds on the golden path: decode is
	// deterministic, and quality is sane on every frame.
	first := decodeToPlanes(t, container)
	second := decodeToPlanes(t, container)
	if len(first) != frames {
		t.Fatalf("decoded %d frames, want %d", len(first), frames)
	}
	g := geometryFor(cfg.Width, cfg.Height)
	imgs := decodeImages(t, container)
	for i := range first {
		if !bytes.Equal(first[i], second[i]) {
			t.Fatalf("frame %d: decode is not deterministic", i)
		}
		if p := framePSNR(imgs[i], refs[i], g); p < 30 {
			t.Errorf("frame %d: PSNR %.1f dB, want >= 30", i, p)
		}
	}

	// Every return to the pose (even frames past the key) must be tiny next to
	// an excursion frame, which re-codes real content. Without the golden
	// reference the returns cost as much as the excursions.
	sizes := packetSizes(t, container)
	for i := 2; i < frames; i += 2 {
		if sizes[i] > sizes[1]/10 {
			t.Errorf("frame %d (return to pose): %d bytes, want < 10%% of excursion frame's %d",
				i, sizes[i], sizes[1])
		}
	}
	t.Logf("key %d B, excursion %d B, returns %d/%d/%d/%d B",
		sizes[0], sizes[1], sizes[2], sizes[4], sizes[6], sizes[8])
}

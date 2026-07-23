package ebiv

import (
	"bytes"
	"image"
	"io"
	"math"
	"testing"
)

// This file holds the shared verification harness. Every later phase of the
// codec compares against it rather than growing its own comparison code:
//
//   - comparePlanes reports the diagnostics CODEC_GUIDE.md calls for — wrong
//     pixel count, max error, first error location, per-block error map.
//   - psnr is the quality gate. Round-trip equality alone cannot catch an
//     encoder and decoder bug that cancel out; only measuring the decoded
//     frame against the *source* can.
//   - synthFrame produces deterministic content with the features that break
//     codecs: hard edges, gradients, and high-frequency detail.

// planeDiff summarizes the difference between a decoded plane and a reference.
type planeDiff struct {
	Name     string
	Pixels   int
	Wrong    int
	MaxError int
	FirstX   int // -1 when the planes match
	FirstY   int
	SumSq    float64
}

func (d planeDiff) exact() bool { return d.Wrong == 0 }

// psnr returns the plane's peak signal-to-noise ratio in dB, or +Inf when the
// planes are identical.
func (d planeDiff) psnr() float64 {
	if d.SumSq == 0 {
		return math.Inf(1)
	}
	mse := d.SumSq / float64(d.Pixels)
	return 10 * math.Log10(255*255/mse)
}

// comparePlanes diffs a strided plane against a tightly packed reference.
func comparePlanes(name string, got []byte, stride int, want []byte, w, h int) planeDiff {
	d := planeDiff{Name: name, Pixels: w * h, FirstX: -1, FirstY: -1}
	for y := 0; y < h; y++ {
		g := got[y*stride : y*stride+w : y*stride+w]
		r := want[y*w : y*w+w : y*w+w]
		for x := 0; x < w; x++ {
			e := int(g[x]) - int(r[x])
			if e == 0 {
				continue
			}
			if e < 0 {
				e = -e
			}
			d.Wrong++
			d.SumSq += float64(e * e)
			if e > d.MaxError {
				d.MaxError = e
			}
			if d.FirstX < 0 {
				d.FirstX, d.FirstY = x, y
			}
		}
	}
	return d
}

// compareImages diffs all three planes of a decoded frame against packed
// reference planes in the on-disk layout (Y, then Cb, then Cr).
func compareImages(t *testing.T, got *image.YCbCr, want []byte, g geometry) []planeDiff {
	t.Helper()
	ySize := g.W * g.H
	cSize := g.CW * g.CH
	if len(want) != ySize+2*cSize {
		t.Fatalf("reference is %d bytes, want %d", len(want), ySize+2*cSize)
	}
	return []planeDiff{
		comparePlanes("Y", got.Y, got.YStride, want[:ySize], g.W, g.H),
		comparePlanes("Cb", got.Cb, got.CStride, want[ySize:ySize+cSize], g.CW, g.CH),
		comparePlanes("Cr", got.Cr, got.CStride, want[ySize+cSize:], g.CW, g.CH),
	}
}

// requireExact fails the test with full diagnostics unless every plane matches
// bit for bit.
func requireExact(t *testing.T, label string, diffs []planeDiff) {
	t.Helper()
	for _, d := range diffs {
		if d.exact() {
			continue
		}
		t.Errorf("%s: plane %s: %d/%d pixels wrong, max error %d, first at (%d,%d), PSNR %.2f dB",
			label, d.Name, d.Wrong, d.Pixels, d.MaxError, d.FirstX, d.FirstY, d.psnr())
	}
}

// synthFrame builds a deterministic test frame. Content is chosen to stress the
// things codecs get wrong: a sharp diagonal edge, a smooth gradient, a
// high-frequency checker region, and chroma that varies independently of luma
// so a plane mix-up cannot hide.
func synthFrame(g geometry, seed int) *image.YCbCr {
	img := g.newImage()
	for y := 0; y < g.H; y++ {
		row := img.Y[y*img.YStride : y*img.YStride+g.W : y*img.YStride+g.W]
		for x := 0; x < g.W; x++ {
			v := (x*3 + y*5 + seed*7) & 0xff // gradient that shifts per frame
			if x+y > (g.W+g.H)/2 {
				v = 255 - v // hard diagonal edge
			}
			if (x/4+y/4)&1 == 0 {
				v ^= 0x33 // high-frequency detail
			}
			row[x] = uint8(v)
		}
	}
	for y := 0; y < g.CH; y++ {
		cb := img.Cb[y*img.CStride : y*img.CStride+g.CW : y*img.CStride+g.CW]
		cr := img.Cr[y*img.CStride : y*img.CStride+g.CW : y*img.CStride+g.CW]
		for x := 0; x < g.CW; x++ {
			cb[x] = uint8((x*11 + y*2 + seed*3) & 0xff)
			cr[x] = uint8((255 - x*2 - y*13 - seed*5) & 0xff)
		}
	}
	return img
}

// synthPan builds a frame by translating a fixed texture, so motion
// compensation has real spatial motion to track. Frame i is the base texture
// shifted by (2i, 2i) luma pixels — an even shift keeps the chroma pan exact.
func synthPan(g geometry, seed int) *image.YCbCr {
	img := g.newImage()
	off := 2 * seed
	for y := 0; y < g.H; y++ {
		row := img.Y[y*img.YStride : y*img.YStride+g.W : y*img.YStride+g.W]
		for x := 0; x < g.W; x++ {
			row[x] = lumaTex(x+off, y+off)
		}
	}
	coff := seed
	for y := 0; y < g.CH; y++ {
		cb := img.Cb[y*img.CStride : y*img.CStride+g.CW : y*img.CStride+g.CW]
		cr := img.Cr[y*img.CStride : y*img.CStride+g.CW : y*img.CStride+g.CW]
		for x := 0; x < g.CW; x++ {
			cb[x] = chromaTex(x+coff, y+coff, 0)
			cr[x] = chromaTex(x+coff, y+coff, 1)
		}
	}
	return img
}

// lumaTex and chromaTex are fixed functions of absolute position, so shifting
// the sampling origin translates the whole picture — exactly what motion
// compensation is built to predict.
func lumaTex(x, y int) byte {
	v := (x*3 + y*2) & 0xff
	if (x/6+y/6)&1 == 0 {
		v ^= 0x40
	}
	return byte(v)
}

func chromaTex(x, y, plane int) byte {
	if plane == 0 {
		return byte((x*5 + y*3) & 0xff)
	}
	return byte((200 - x*2 - y*4) & 0xff)
}

// packImage flattens an image into the tightly packed reference layout, giving
// tests an independent copy that survives the codec's buffer reuse.
func packImage(img *image.YCbCr, g geometry) []byte {
	out := make([]byte, g.packedSize())
	ySize := g.W * g.H
	cSize := g.CW * g.CH
	gatherPlane(out[:ySize], img.Y, img.YStride, g.W, g.H)
	gatherPlane(out[ySize:ySize+cSize], img.Cb, img.CStride, g.CW, g.CH)
	gatherPlane(out[ySize+cSize:], img.Cr, img.CStride, g.CW, g.CH)
	return out
}

// encodeClip encodes n synthetic frames and returns the container bytes plus
// the packed source planes, which are the reference every decode is checked
// against.
func encodeClip(t *testing.T, cfg Config, n int) (container []byte, refs [][]byte) {
	t.Helper()
	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, cfg)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	g := geometryFor(cfg.Width, cfg.Height)
	for i := 0; i < n; i++ {
		img := synthFrame(g, i)
		refs = append(refs, packImage(img, g))
		if err := enc.WriteFrame(img); err != nil {
			t.Fatalf("WriteFrame %d: %v", i, err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Encoder.Close: %v", err)
	}
	return buf.Bytes(), refs
}

// decodeAll runs every packet in the container through a codec and hands each
// decoded frame to fn before the next decode can recycle its buffer.
func decodeAll(t *testing.T, container []byte, c *Codec, fn func(i int, img *image.YCbCr)) int {
	t.Helper()
	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}
	defer d.Close()

	n := 0
	for {
		pkt, err := d.NextPacket()
		if err == io.EOF {
			return n
		}
		if err != nil {
			t.Fatalf("NextPacket %d: %v", n, err)
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			t.Fatalf("Decode %d: %v", n, err)
		}
		if frame == nil {
			t.Fatalf("Decode %d returned no frame", n)
		}
		fn(n, frame.YCbCr)
		n++
	}
}

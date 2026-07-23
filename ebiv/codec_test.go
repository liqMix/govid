package ebiv

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"testing"

	govid "github.com/liqmix/govid"
)

// Compile-time proof that the package satisfies govid's plug-in interfaces.
var (
	_ govid.Demuxer = (*Reader)(nil)
	_ govid.Codec   = (*Codec)(nil)
)

// TestRoundTripBitExact is the contract the whole format rests on: what the
// encoder was given is exactly what the decoder produces. Every later coding
// mode that claims to be lossless must pass this same test.
func TestRoundTripBitExact(t *testing.T) {
	sizes := []struct{ w, h int }{
		{64, 48},     // aligned
		{1, 1},       // degenerate
		{65, 49},     // odd dimensions, chroma rounds up
		{37, 21},     // width below one stride unit
		{1920, 1080}, // realistic
	}
	for _, s := range sizes {
		t.Run(fmt.Sprintf("%dx%d", s.w, s.h), func(t *testing.T) {
			cfg := Config{Width: s.w, Height: s.h, FPSNum: 30, FPSDen: 1}
			g := geometryFor(s.w, s.h)
			container, refs := encodeClip(t, cfg, 4)

			c := NewCodec()
			n := decodeAll(t, container, c, func(i int, img *image.YCbCr) {
				diffs := compareImages(t, img, refs[i], g)
				requireExact(t, "round trip", diffs)
				for _, d := range diffs {
					if psnr := d.psnr(); !math.IsInf(psnr, 1) {
						t.Errorf("frame %d plane %s: PSNR %.2f dB, want infinite for a lossless mode", i, d.Name, psnr)
					}
				}
			})
			if n != len(refs) {
				t.Errorf("decoded %d frames, want %d", n, len(refs))
			}
		})
	}
}

// TestCodecReusesRingBuffers proves the ring actually recycles: after ring-size
// decodes the codec must hand back the same underlying plane buffers rather
// than allocating new ones.
func TestCodecReusesRingBuffers(t *testing.T) {
	const ring = 3
	cfg := testConfig()
	container, _ := encodeClip(t, cfg, ring*2)

	c := NewCodec(WithFrameRing(ring))
	var first [ring]*image.YCbCr
	decodeAll(t, container, c, func(i int, img *image.YCbCr) {
		if i < ring {
			first[i] = img
			return
		}
		if want := first[i%ring]; img != want {
			t.Errorf("frame %d used a different buffer than frame %d; the ring is not recycling", i, i%ring)
		}
	})
}

// TestDecodeSteadyStateAllocations guards the plan's zero-allocation goal. The
// only allocation the govid.Codec interface forces is the small Frame wrapper;
// no plane buffer may be allocated after the first key frame.
func TestDecodeSteadyStateAllocations(t *testing.T) {
	cfg := Config{Width: 320, Height: 240, FPSNum: 30, FPSDen: 1}
	g := geometryFor(cfg.Width, cfg.Height)
	img := synthFrame(g, 0)

	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := enc.WriteFrame(img); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}

	d, err := NewDemuxer(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}

	c := NewCodec()
	if _, err := c.Decode(pkt); err != nil { // warm up: allocates the ring
		t.Fatal(err)
	}

	got := testing.AllocsPerRun(50, func() {
		if _, err := c.Decode(pkt); err != nil {
			t.Fatal(err)
		}
	})
	// One allocation: the govid.Frame wrapper. Anything more means a plane
	// buffer escaped into the steady-state path.
	if got > 1 {
		t.Errorf("Decode allocates %.1f objects per call, want at most 1 (the Frame wrapper)", got)
	}
}

func TestDecodeRejectsBadPackets(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"empty", nil, ErrCorrupt},
		{"inter before key", []byte{uint8(FrameInter)}, ErrNoKeyframe},
		{"truncated key header", []byte{uint8(FrameKey), 1, 0}, ErrCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCodec()
			if _, err := c.Decode(govid.Packet{Data: tt.data}); !errors.Is(err, tt.want) {
				t.Errorf("Decode = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDecodeRejectsWrongPayloadLength(t *testing.T) {
	g := geometryFor(16, 16)
	hdr := frameHeader{Type: FrameKey, Coding: CodingRaw, Width: 16, Height: 16}
	pkt := govid.Packet{Data: hdr.appendTo(nil)} // header with no plane data at all

	c := NewCodec()
	_, err := c.Decode(pkt)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Decode(short raw payload) = %v, want %v", err, ErrCorrupt)
	}
	// And with one byte too many.
	pkt.Data = append(hdr.appendTo(nil), make([]byte, g.packedSize()+1)...)
	if _, err := c.Decode(pkt); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Decode(long raw payload) = %v, want %v", err, ErrCorrupt)
	}
}

func TestDecodeRejectsResolutionChange(t *testing.T) {
	c := NewCodec()
	small := frameHeader{Type: FrameKey, Coding: CodingRaw, Width: 16, Height: 16}
	g := geometryFor(16, 16)
	pkt := govid.Packet{Data: append(small.appendTo(nil), make([]byte, g.packedSize())...)}
	if _, err := c.Decode(pkt); err != nil {
		t.Fatalf("first key frame: %v", err)
	}

	big := frameHeader{Type: FrameKey, Coding: CodingRaw, Width: 32, Height: 32}
	g2 := geometryFor(32, 32)
	pkt2 := govid.Packet{Data: append(big.appendTo(nil), make([]byte, g2.packedSize())...)}
	if _, err := c.Decode(pkt2); !errors.Is(err, ErrDimensions) {
		t.Errorf("Decode(resolution change) = %v, want %v", err, ErrDimensions)
	}
}

func TestEncoderRejectsMismatchedImages(t *testing.T) {
	cfg := testConfig()
	enc, err := NewEncoder(io.Discard, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := enc.WriteFrame(nil); !errors.Is(err, ErrDimensions) {
		t.Errorf("WriteFrame(nil) = %v, want %v", err, ErrDimensions)
	}
	wrongSize := geometryFor(cfg.Width+2, cfg.Height).newImage()
	if err := enc.WriteFrame(wrongSize); !errors.Is(err, ErrDimensions) {
		t.Errorf("WriteFrame(wrong size) = %v, want %v", err, ErrDimensions)
	}
	wrongChroma := image.NewYCbCr(image.Rect(0, 0, cfg.Width, cfg.Height), image.YCbCrSubsampleRatio422)
	if err := enc.WriteFrame(wrongChroma); !errors.Is(err, ErrDimensions) {
		t.Errorf("WriteFrame(4:2:2) = %v, want %v", err, ErrDimensions)
	}
}

// TestEncoderAcceptsSubImages checks the plane-origin arithmetic: a YCbCr that
// is a crop of a larger frame must encode its visible pixels, not the parent's
// top-left corner.
func TestEncoderAcceptsSubImages(t *testing.T) {
	const w, h = 32, 32
	parent := geometryFor(w*2, h*2)
	full := synthFrame(parent, 1)
	sub := full.SubImage(image.Rect(w, h, w*2, h*2)).(*image.YCbCr)

	cfg := Config{Width: w, Height: h, FPSNum: 30, FPSDen: 1}
	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := enc.WriteFrame(sub); err != nil {
		t.Fatalf("WriteFrame(sub-image): %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}

	g := geometryFor(w, h)
	want := packImage(sub, g)
	c := NewCodec()
	decodeAll(t, buf.Bytes(), c, func(_ int, img *image.YCbCr) {
		requireExact(t, "sub-image", compareImages(t, img, want, g))
	})
}

// TestPlayerIntegration drives the format through the same path the Ebitengine
// bridge uses: a govid.Player over this package's Demuxer and Codec.
func TestPlayerIntegration(t *testing.T) {
	const frames = 12
	cfg := testConfig()
	container, refs := encodeClip(t, cfg, frames)
	g := geometryFor(cfg.Width, cfg.Height)

	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	p, err := govid.NewPlayer(d, NewCodec())
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	defer p.Close()

	hdr := d.FileHeader()
	if got, want := p.Duration(), hdr.frameTime(frames); got != want {
		t.Errorf("Player.Duration = %v, want %v", got, want)
	}

	p.Play()
	seen := 0
	for i := 0; i < frames; i++ {
		p.UpdateToTime(hdr.frameTime(i))
		frame := p.CurrentFrame()
		if frame == nil {
			t.Fatalf("no current frame at index %d", i)
		}
		if frame.Width != cfg.Width || frame.Height != cfg.Height {
			t.Fatalf("frame %d is %dx%d, want %dx%d", i, frame.Width, frame.Height, cfg.Width, cfg.Height)
		}
		if frame.Timestamp != hdr.frameTime(i) {
			t.Errorf("frame at index %d has timestamp %v, want %v", i, frame.Timestamp, hdr.frameTime(i))
		}
		requireExact(t, "player frame", compareImages(t, frame.YCbCr, refs[i], g))
		seen++
	}
	if seen != frames {
		t.Errorf("saw %d frames, want %d", seen, frames)
	}

	// Seek must land on the requested frame and show its pixels.
	if err := p.Seek(hdr.frameTime(4)); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	requireExact(t, "after player seek", compareImages(t, p.CurrentFrame().YCbCr, refs[4], g))
}

// TestPlayerRGBAConversion checks the frame reaches the Ebitengine bridge's
// input format with plausible pixels — the bridge uploads Frame.RGBA().
func TestPlayerRGBAConversion(t *testing.T) {
	cfg := testConfig()
	container, _ := encodeClip(t, cfg, 2)

	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	p, err := govid.NewPlayer(d, NewCodec())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	rgba := p.CurrentFrame().RGBA()
	if want := cfg.Width * cfg.Height * 4; len(rgba) != want {
		t.Fatalf("RGBA is %d bytes, want %d", len(rgba), want)
	}
	for i := 3; i < len(rgba); i += 4 {
		if rgba[i] != 0xff {
			t.Fatalf("alpha at pixel %d = %d, want 255", i/4, rgba[i])
		}
	}
}

func BenchmarkDecodeRaw1080p(b *testing.B) {
	cfg := Config{Width: 1920, Height: 1080, FPSNum: 30, FPSDen: 1}
	g := geometryFor(cfg.Width, cfg.Height)

	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, cfg)
	if err != nil {
		b.Fatal(err)
	}
	if err := enc.WriteFrame(synthFrame(g, 0)); err != nil {
		b.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		b.Fatal(err)
	}

	d, err := NewDemuxer(bytes.NewReader(buf.Bytes()))
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	pkt, err := d.NextPacket()
	if err != nil {
		b.Fatal(err)
	}

	c := NewCodec()
	if _, err := c.Decode(pkt); err != nil {
		b.Fatal(err)
	}

	b.SetBytes(int64(g.packedSize()))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Decode(pkt); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDemuxNextPacket(b *testing.B) {
	cfg := Config{Width: 320, Height: 240, FPSNum: 30, FPSDen: 1}
	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, cfg)
	if err != nil {
		b.Fatal(err)
	}
	g := geometryFor(cfg.Width, cfg.Height)
	for i := 0; i < 30; i++ {
		if err := enc.WriteFrame(synthFrame(g, i)); err != nil {
			b.Fatal(err)
		}
	}
	if err := enc.Close(); err != nil {
		b.Fatal(err)
	}

	d, err := NewDemuxer(bytes.NewReader(buf.Bytes()))
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := d.NextPacket(); err == io.EOF {
			if _, err := d.Seek(0); err != nil {
				b.Fatal(err)
			}
		} else if err != nil {
			b.Fatal(err)
		}
	}
}

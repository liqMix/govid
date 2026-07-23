package ebiv

import (
	"errors"
	"testing"
	"time"
)

func TestFileHeaderRoundTrip(t *testing.T) {
	in := FileHeader{
		Version: Version, Width: 1920, Height: 1080,
		FPSNum: 30000, FPSDen: 1001,
		ChromaFormat: Chroma420, BitDepth: depth8,
		ColorRange: 0, ColorPrimaries: 0,
		TileCols: 4, TileRows: 2, Flags: 0,
	}
	var b [fileHeaderSize]byte
	in.marshal(b[:])

	var out FileHeader
	if err := out.unmarshal(b[:]); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round trip changed the header:\n got %+v\nwant %+v", out, in)
	}
}

func TestFileHeaderRejects(t *testing.T) {
	valid := func() []byte {
		h := headerFor(Config{Width: 64, Height: 48, FPSNum: 30, FPSDen: 1})
		b := make([]byte, fileHeaderSize)
		h.marshal(b)
		return b
	}

	tests := []struct {
		name    string
		corrupt func([]byte)
		want    error
	}{
		{"bad magic", func(b []byte) { b[0] = 'X' }, ErrBadMagic},
		{"future version", func(b []byte) { b[4] = Version + 1 }, ErrVersion},
		{"wrong header size", func(b []byte) { b[6] = fileHeaderSize + 1 }, ErrCorrupt},
		{"zero width", func(b []byte) { b[8], b[9], b[10], b[11] = 0, 0, 0, 0 }, ErrCorrupt},
		{"zero frame rate", func(b []byte) { b[16], b[17], b[18], b[19] = 0, 0, 0, 0 }, ErrCorrupt},
		{"chroma 4:2:2", func(b []byte) { b[28] = 1 }, ErrUnsupported},
		{"10-bit", func(b []byte) { b[29] = 1 }, ErrUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := valid()
			tt.corrupt(b)
			var h FileHeader
			err := h.unmarshal(b)
			if !errors.Is(err, tt.want) {
				t.Errorf("unmarshal error = %v, want %v", err, tt.want)
			}
		})
	}

	t.Run("truncated", func(t *testing.T) {
		var h FileHeader
		if err := h.unmarshal(valid()[:10]); !errors.Is(err, ErrCorrupt) {
			t.Errorf("unmarshal error = %v, want %v", err, ErrCorrupt)
		}
	})
}

func TestFrameTimeIsExactForNTSCRates(t *testing.T) {
	h := FileHeader{FPSNum: 30000, FPSDen: 1001}
	// 30000/1001 fps: frame 30000 lands on exactly 1001 seconds.
	if got, want := h.frameTime(30000), 1001*time.Second; got != want {
		t.Errorf("frameTime(30000) = %v, want %v", got, want)
	}
	// frameIndexAt must invert frameTime on frame boundaries.
	for _, i := range []int{0, 1, 29, 30, 1000, 30000} {
		if got := h.frameIndexAt(h.frameTime(i)); got != i {
			t.Errorf("frameIndexAt(frameTime(%d)) = %d, want %d", i, got, i)
		}
	}
	// A time just short of a frame boundary still maps to the previous frame.
	if got := h.frameIndexAt(h.frameTime(10) - 1); got != 9 {
		t.Errorf("frameIndexAt(just before frame 10) = %d, want 9", got)
	}
	if got := h.frameIndexAt(-time.Second); got != 0 {
		t.Errorf("frameIndexAt(negative) = %d, want 0", got)
	}
}

func TestFrameHeaderRoundTrip(t *testing.T) {
	tests := []frameHeader{
		{Type: FrameKey, Coding: CodingRaw, Width: 1920, Height: 1080},
		{Type: FrameKey, Coding: CodingRaw, Width: 1, Height: 1},
		{Type: FrameInter, Coding: CodingRaw},
	}
	for _, in := range tests {
		t.Run(in.Type.String(), func(t *testing.T) {
			payload := []byte{0xAA, 0xBB}
			buf := in.appendTo(nil)
			if len(buf) != in.size() {
				t.Fatalf("encoded %d bytes, size() says %d", len(buf), in.size())
			}
			buf = append(buf, payload...)

			out, body, err := parseFrameHeader(buf)
			if err != nil {
				t.Fatalf("parseFrameHeader: %v", err)
			}
			if out != in {
				t.Errorf("round trip changed the header:\n got %+v\nwant %+v", out, in)
			}
			if string(body) != string(payload) {
				t.Errorf("body = %x, want %x", body, payload)
			}
		})
	}
}

func TestFrameHeaderRejects(t *testing.T) {
	key := frameHeader{Type: FrameKey, Coding: CodingRaw, Width: 64, Height: 48}

	t.Run("empty", func(t *testing.T) {
		if _, _, err := parseFrameHeader(nil); !errors.Is(err, ErrCorrupt) {
			t.Errorf("error = %v, want %v", err, ErrCorrupt)
		}
	})
	t.Run("truncated key header", func(t *testing.T) {
		b := key.appendTo(nil)
		if _, _, err := parseFrameHeader(b[:3]); !errors.Is(err, ErrCorrupt) {
			t.Errorf("error = %v, want %v", err, ErrCorrupt)
		}
	})
	t.Run("zero dimensions", func(t *testing.T) {
		b := key.appendTo(nil)
		b[1], b[2] = 0, 0
		if _, _, err := parseFrameHeader(b); !errors.Is(err, ErrCorrupt) {
			t.Errorf("error = %v, want %v", err, ErrCorrupt)
		}
	})
	t.Run("unsupported chroma", func(t *testing.T) {
		b := key.appendTo(nil)
		b[5] = 1<<4 | depth8
		if _, _, err := parseFrameHeader(b); !errors.Is(err, ErrUnsupported) {
			t.Errorf("error = %v, want %v", err, ErrUnsupported)
		}
	})
	t.Run("unsupported depth", func(t *testing.T) {
		b := key.appendTo(nil)
		b[5] = Chroma420<<4 | 1
		if _, _, err := parseFrameHeader(b); !errors.Is(err, ErrUnsupported) {
			t.Errorf("error = %v, want %v", err, ErrUnsupported)
		}
	})
	t.Run("reserved frame type", func(t *testing.T) {
		if _, _, err := parseFrameHeader([]byte{0x02}); !errors.Is(err, ErrCorrupt) {
			t.Errorf("error = %v, want %v", err, ErrCorrupt)
		}
	})
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"minimal", Config{Width: 1, Height: 1, FPSNum: 1, FPSDen: 1}, true},
		{"1080p ntsc", Config{Width: 1920, Height: 1080, FPSNum: 30000, FPSDen: 1001}, true},
		{"zero width", Config{Width: 0, Height: 48, FPSNum: 30, FPSDen: 1}, false},
		{"oversize", Config{Width: 65536, Height: 48, FPSNum: 30, FPSDen: 1}, false},
		{"zero fps", Config{Width: 64, Height: 48, FPSNum: 0, FPSDen: 1}, false},
		{"zero fps den", Config{Width: 64, Height: 48, FPSNum: 30, FPSDen: 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.validate()
			if tt.ok && err != nil {
				t.Errorf("validate() = %v, want nil", err)
			}
			if !tt.ok && !errors.Is(err, ErrConfig) {
				t.Errorf("validate() = %v, want %v", err, ErrConfig)
			}
		})
	}
}

func TestGeometry(t *testing.T) {
	tests := []struct {
		w, h             int
		cw, ch           int
		yStride, cStride int
		packed           int
	}{
		{64, 48, 32, 24, 64, 64, 64*48 + 2*32*24},
		{1920, 1080, 960, 540, 1920, 960, 1920*1080 + 2*960*540},
		{65, 49, 33, 25, 128, 64, 65*49 + 2*33*25}, // odd dims round chroma up
		{1, 1, 1, 1, 64, 64, 1 + 2},
	}
	for _, tt := range tests {
		g := geometryFor(tt.w, tt.h)
		if g.CW != tt.cw || g.CH != tt.ch {
			t.Errorf("geometryFor(%d,%d) chroma = %dx%d, want %dx%d", tt.w, tt.h, g.CW, g.CH, tt.cw, tt.ch)
		}
		if g.YStride != tt.yStride || g.CStride != tt.cStride {
			t.Errorf("geometryFor(%d,%d) strides = %d/%d, want %d/%d", tt.w, tt.h, g.YStride, g.CStride, tt.yStride, tt.cStride)
		}
		if got := g.packedSize(); got != tt.packed {
			t.Errorf("geometryFor(%d,%d).packedSize() = %d, want %d", tt.w, tt.h, got, tt.packed)
		}
		if g.YStride%strideAlign != 0 || g.CStride%strideAlign != 0 {
			t.Errorf("geometryFor(%d,%d) strides %d/%d are not %d-byte aligned", tt.w, tt.h, g.YStride, g.CStride, strideAlign)
		}
		img := g.newImage()
		if !g.matches(img) {
			t.Errorf("geometryFor(%d,%d).newImage() does not match its own geometry", tt.w, tt.h)
		}
	}
}

func TestScatterGatherRoundTrip(t *testing.T) {
	g := geometryFor(37, 21) // deliberately unaligned width
	packed := make([]byte, g.W*g.H)
	for i := range packed {
		packed[i] = uint8(i * 7 % 251)
	}
	strided := make([]byte, g.YStride*g.H)
	// Poison the stride padding so a gather that reads past the row width
	// produces a mismatch rather than silently passing.
	for i := range strided {
		strided[i] = 0xEE
	}
	scatterPlane(strided, g.YStride, packed, g.W, g.H)

	out := make([]byte, len(packed))
	gatherPlane(out, strided, g.YStride, g.W, g.H)
	for i := range packed {
		if out[i] != packed[i] {
			t.Fatalf("round trip differs at %d: got %d, want %d", i, out[i], packed[i])
		}
	}
	// Padding must be untouched by the scatter.
	for y := 0; y < g.H; y++ {
		for x := g.W; x < g.YStride; x++ {
			if strided[y*g.YStride+x] != 0xEE {
				t.Fatalf("scatterPlane wrote into stride padding at (%d,%d)", x, y)
			}
		}
	}
}

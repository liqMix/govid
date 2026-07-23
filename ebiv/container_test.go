package ebiv

import (
	"bytes"
	"errors"
	"image"
	"io"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Width: 64, Height: 48, FPSNum: 30, FPSDen: 1}
}

func TestWriterRejectsBadConfig(t *testing.T) {
	if _, err := NewWriter(io.Discard, Config{}); !errors.Is(err, ErrConfig) {
		t.Errorf("NewWriter(zero config) = %v, want %v", err, ErrConfig)
	}
}

func TestWriterRejectsInterFirst(t *testing.T) {
	w, err := NewWriter(io.Discard, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame([]byte{0x04}, false); !errors.Is(err, ErrConfig) {
		t.Errorf("WriteFrame(inter first) = %v, want %v", err, ErrConfig)
	}
}

func TestWriterErrorsAreSticky(t *testing.T) {
	w, err := NewWriter(io.Discard, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	first := w.WriteFrame(nil, true)
	if first == nil {
		t.Fatal("WriteFrame(empty payload) succeeded, want an error")
	}
	if got := w.WriteFrame([]byte{1, 2, 3}, true); !errors.Is(got, first) {
		t.Errorf("second WriteFrame = %v, want the sticky error %v", got, first)
	}
	if got := w.Close(); !errors.Is(got, first) {
		t.Errorf("Close = %v, want the sticky error %v", got, first)
	}
}

func TestDemuxerReadsMetadata(t *testing.T) {
	cfg := Config{Width: 1920, Height: 1080, FPSNum: 30000, FPSDen: 1001, TileCols: 4, TileRows: 2}
	container, _ := encodeClip(t, cfg, 3)

	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}
	defer d.Close()

	info := d.VideoInfo()
	if info.Width != cfg.Width || info.Height != cfg.Height {
		t.Errorf("VideoInfo dimensions = %dx%d, want %dx%d", info.Width, info.Height, cfg.Width, cfg.Height)
	}
	if want := float64(cfg.FPSNum) / float64(cfg.FPSDen); info.FrameRate != want {
		t.Errorf("VideoInfo.FrameRate = %v, want %v", info.FrameRate, want)
	}
	if got := d.FrameCount(); got != 3 {
		t.Errorf("FrameCount = %d, want 3", got)
	}
	hdr := d.FileHeader()
	if hdr.TileCols != 4 || hdr.TileRows != 2 {
		t.Errorf("tile grid = %dx%d, want 4x2", hdr.TileCols, hdr.TileRows)
	}
	if got, want := d.Duration(), hdr.frameTime(3); got != want {
		t.Errorf("Duration = %v, want %v", got, want)
	}
}

func TestDemuxerPacketSequence(t *testing.T) {
	const frames = 5
	container, _ := encodeClip(t, testConfig(), frames)

	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}
	defer d.Close()

	hdr := d.FileHeader()
	for i := 0; i < frames; i++ {
		pkt, err := d.NextPacket()
		if err != nil {
			t.Fatalf("NextPacket %d: %v", i, err)
		}
		if !pkt.Keyframe {
			t.Errorf("packet %d: Keyframe = false, want true (all raw frames are key frames)", i)
		}
		if want := hdr.frameTime(i); pkt.Timestamp != want {
			t.Errorf("packet %d: Timestamp = %v, want %v", i, pkt.Timestamp, want)
		}
		if len(pkt.Data) == 0 {
			t.Errorf("packet %d: empty data", i)
		}
	}
	if _, err := d.NextPacket(); err != io.EOF {
		t.Errorf("NextPacket past the end = %v, want io.EOF", err)
	}
	// EOF must be sticky rather than wrapping around.
	if _, err := d.NextPacket(); err != io.EOF {
		t.Errorf("second NextPacket past the end = %v, want io.EOF", err)
	}
}

func TestDemuxerSeek(t *testing.T) {
	const frames = 10
	cfg := testConfig()
	container, refs := encodeClip(t, cfg, frames)
	g := geometryFor(cfg.Width, cfg.Height)

	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		t.Fatalf("NewDemuxer: %v", err)
	}
	defer d.Close()
	c := NewCodec()
	hdr := d.FileHeader()

	// Every frame here is a key frame, so a seek must land exactly on the
	// requested frame and decode it correctly with no preroll.
	for _, target := range []int{7, 0, 9, 3} {
		actual, err := d.Seek(hdr.frameTime(target))
		if err != nil {
			t.Fatalf("Seek to frame %d: %v", target, err)
		}
		if want := hdr.frameTime(target); actual != want {
			t.Errorf("Seek to frame %d returned %v, want %v", target, actual, want)
		}
		pkt, err := d.NextPacket()
		if err != nil {
			t.Fatalf("NextPacket after seek to %d: %v", target, err)
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			t.Fatalf("Decode after seek to %d: %v", target, err)
		}
		requireExact(t, "after seek", compareImages(t, frame.YCbCr, refs[target], g))
	}

	// Seeking past the end clamps to the last frame rather than failing.
	actual, err := d.Seek(time.Hour)
	if err != nil {
		t.Fatalf("Seek past end: %v", err)
	}
	if want := hdr.frameTime(frames - 1); actual != want {
		t.Errorf("Seek past end returned %v, want %v", actual, want)
	}

	// A negative position clamps to the start.
	if actual, err := d.Seek(-time.Second); err != nil || actual != 0 {
		t.Errorf("Seek(-1s) = (%v, %v), want (0, nil)", actual, err)
	}
}

func TestDemuxerRebuildsIndexWithoutFooter(t *testing.T) {
	const frames = 4
	cfg := testConfig()
	container, refs := encodeClip(t, cfg, frames)
	g := geometryFor(cfg.Width, cfg.Height)

	// Simulate an encode killed before Close: drop the index and footer.
	payloadEnd := int64(fileHeaderSize)
	for i := 0; i < frames; i++ {
		payloadEnd += frameRecordSize + int64(len(refs[i])) + frameHeaderBase + frameHeaderKeyExtra
	}
	truncated := container[:payloadEnd]

	d, err := NewDemuxer(bytes.NewReader(truncated))
	if err != nil {
		t.Fatalf("NewDemuxer on footerless file: %v", err)
	}
	defer d.Close()
	if got := d.FrameCount(); got != frames {
		t.Fatalf("rebuilt index has %d frames, want %d", got, frames)
	}

	c := NewCodec()
	n := decodeAll(t, truncated, c, func(i int, img *image.YCbCr) {
		requireExact(t, "footerless", compareImages(t, img, refs[i], g))
	})
	if n != frames {
		t.Errorf("decoded %d frames, want %d", n, frames)
	}
}

func TestDemuxerRejectsCorruptFiles(t *testing.T) {
	container, _ := encodeClip(t, testConfig(), 3)

	tests := []struct {
		name  string
		build func() []byte
		want  error
	}{
		{"empty", func() []byte { return nil }, ErrBadMagic},
		{"bad magic", func() []byte {
			b := append([]byte(nil), container...)
			b[0] = 'X'
			return b
		}, ErrBadMagic},
		{"header only", func() []byte { return container[:fileHeaderSize] }, ErrCorrupt},
		{"first frame record claims an impossible size", func() []byte {
			// Keep the sync word intact so the scan accepts the record, then
			// give it a length no file could satisfy. Truncating to the record
			// also removes the footer, forcing the scan path.
			b := append([]byte(nil), container[:fileHeaderSize+frameRecordSize]...)
			for i := 4; i < frameRecordSize; i++ {
				b[fileHeaderSize+i] = 0xFF
			}
			return b
		}, ErrCorrupt},
		{"frame record sync word destroyed", func() []byte {
			b := append([]byte(nil), container[:len(container)-footerSize]...)
			b[fileHeaderSize] ^= 0xFF
			return b
		}, ErrCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewDemuxer(bytes.NewReader(tt.build()))
			if !errors.Is(err, tt.want) {
				t.Errorf("NewDemuxer = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDemuxerFallsBackWhenIndexIsCorrupt(t *testing.T) {
	const frames = 3
	cfg := testConfig()
	container, refs := encodeClip(t, cfg, frames)
	g := geometryFor(cfg.Width, cfg.Height)

	// The frame records are self-delimiting, so a damaged index must never cost
	// more than a rebuild scan. Both failure modes are checked: a footer that
	// does not parse at all, and one whose checksum no longer covers the index.
	damage := map[string]func([]byte){
		"footer magic": func(b []byte) { b[len(b)-1] = 'Z' },
		"index checksum": func(b []byte) {
			b[len(b)-footerSize-indexEntrySize]++
		},
	}
	for name, corrupt := range damage {
		t.Run(name, func(t *testing.T) {
			b := append([]byte(nil), container...)
			corrupt(b)

			d, err := NewDemuxer(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("NewDemuxer: %v", err)
			}
			defer d.Close()
			if got := d.FrameCount(); got != frames {
				t.Fatalf("rebuilt index has %d frames, want %d", got, frames)
			}

			c := NewCodec()
			decodeAll(t, b, c, func(i int, img *image.YCbCr) {
				requireExact(t, "scanned index", compareImages(t, img, refs[i], g))
			})
		})
	}
}

func TestIndexEntryRoundTrip(t *testing.T) {
	for _, in := range []IndexEntry{
		{Offset: fileHeaderSize, Size: 1, Keyframe: true},
		{Offset: 1 << 40, Size: maxFrameSize, Keyframe: false},
	} {
		var b [indexEntrySize]byte
		in.marshal(b[:])
		var out IndexEntry
		out.unmarshal(b[:])
		if out != in {
			t.Errorf("round trip changed the entry:\n got %+v\nwant %+v", out, in)
		}
	}
}

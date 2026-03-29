package govid

import (
	"image"
	"image/color"
	"io"
	"testing"
	"time"
)

type testDemuxer struct {
	packets []Packet
	index   int
}

func newTestDemuxer(count int, interval time.Duration) *testDemuxer {
	packets := make([]Packet, count)
	for i := range packets {
		packets[i] = Packet{
			Data:      []byte{byte(i)},
			Timestamp: time.Duration(i) * interval,
			Keyframe:  i == 0,
		}
	}
	return &testDemuxer{packets: packets}
}

func (d *testDemuxer) NextPacket() (Packet, error) {
	if d.index >= len(d.packets) {
		return Packet{}, io.EOF
	}
	pkt := d.packets[d.index]
	d.index++
	return pkt, nil
}

func (d *testDemuxer) Seek(t time.Duration) (time.Duration, error) {
	for i, pkt := range d.packets {
		if pkt.Timestamp >= t {
			d.index = i
			return pkt.Timestamp, nil
		}
	}
	d.index = len(d.packets)
	return d.Duration(), nil
}

func (d *testDemuxer) Duration() time.Duration {
	if len(d.packets) == 0 {
		return 0
	}
	return d.packets[len(d.packets)-1].Timestamp + 33*time.Millisecond
}

func (d *testDemuxer) VideoInfo() VideoInfo {
	return VideoInfo{Width: 4, Height: 4, FrameRate: 30}
}

func (d *testDemuxer) Close() error { return nil }

type testCodec struct{}

func (c *testCodec) Decode(pkt Packet) (*Frame, error) {
	ycbcr := image.NewYCbCr(image.Rect(0, 0, 4, 4), image.YCbCrSubsampleRatio420)
	for i := range ycbcr.Y {
		ycbcr.Y[i] = pkt.Data[0]
	}
	return &Frame{
		YCbCr:     ycbcr,
		Timestamp: pkt.Timestamp,
		Width:     4,
		Height:    4,
	}, nil
}

func (c *testCodec) Flush() {}

func TestNewPlayer(t *testing.T) {
	d := newTestDemuxer(10, 33*time.Millisecond)
	p, err := NewPlayer(d, &testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	if p.State() != StatePaused {
		t.Errorf("expected StatePaused, got %v", p.State())
	}
	if p.CurrentFrame() == nil {
		t.Fatal("expected non-nil current frame")
	}
	if p.CurrentFrame().Timestamp != 0 {
		t.Errorf("expected timestamp 0, got %v", p.CurrentFrame().Timestamp)
	}
}

func TestPlayPause(t *testing.T) {
	d := newTestDemuxer(10, 33*time.Millisecond)
	p, err := NewPlayer(d, &testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	p.Play()
	if p.State() != StatePlaying {
		t.Errorf("expected StatePlaying, got %v", p.State())
	}
	p.Pause()
	if p.State() != StatePaused {
		t.Errorf("expected StatePaused, got %v", p.State())
	}
}

func TestUpdateToTimeFrameDelivery(t *testing.T) {
	d := newTestDemuxer(10, 33*time.Millisecond)
	p, err := NewPlayer(d, &testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	p.Play()

	// At t=0, current frame is frame 0 (already decoded in NewPlayer)
	if p.CurrentFrame().YCbCr.Y[0] != 0 {
		t.Errorf("expected frame 0, got %d", p.CurrentFrame().YCbCr.Y[0])
	}

	// Advance to t=33ms, should deliver frame 1
	changed := p.UpdateToTime(33 * time.Millisecond)
	if !changed {
		t.Error("expected frame change")
	}
	if p.CurrentFrame().YCbCr.Y[0] != 1 {
		t.Errorf("expected frame 1, got %d", p.CurrentFrame().YCbCr.Y[0])
	}

	// Advance to t=66ms, should deliver frame 2
	changed = p.UpdateToTime(66 * time.Millisecond)
	if !changed {
		t.Error("expected frame change")
	}
	if p.CurrentFrame().YCbCr.Y[0] != 2 {
		t.Errorf("expected frame 2, got %d", p.CurrentFrame().YCbCr.Y[0])
	}
}

func TestFrameDropping(t *testing.T) {
	d := newTestDemuxer(10, 33*time.Millisecond)
	p, err := NewPlayer(d, &testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	p.Play()

	// Jump to t=100ms, should skip frames 1-2 and land on frame 3
	changed := p.UpdateToTime(100 * time.Millisecond)
	if !changed {
		t.Error("expected frame change")
	}
	if p.CurrentFrame().YCbCr.Y[0] != 3 {
		t.Errorf("expected frame 3, got %d", p.CurrentFrame().YCbCr.Y[0])
	}
}

func TestEOFStopState(t *testing.T) {
	d := newTestDemuxer(3, 33*time.Millisecond)
	p, err := NewPlayer(d, &testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	p.Play()

	// Advance past all frames
	p.UpdateToTime(200 * time.Millisecond)
	if p.State() != StateStopped {
		t.Errorf("expected StateStopped, got %v", p.State())
	}
	if p.CurrentFrame().YCbCr.Y[0] != 2 {
		t.Errorf("expected last frame (2), got %d", p.CurrentFrame().YCbCr.Y[0])
	}
}

func TestLoop(t *testing.T) {
	d := newTestDemuxer(3, 33*time.Millisecond)
	p, err := NewPlayer(d, &testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	p.SetLoop(true)
	p.Play()

	// Advance past all frames — should loop
	p.UpdateToTime(200 * time.Millisecond)
	if p.State() != StatePlaying {
		t.Errorf("expected StatePlaying after loop, got %v", p.State())
	}
	if p.CurrentFrame().YCbCr.Y[0] != 0 {
		t.Errorf("expected frame 0 after loop, got %d", p.CurrentFrame().YCbCr.Y[0])
	}
}

func TestSeek(t *testing.T) {
	d := newTestDemuxer(10, 33*time.Millisecond)
	p, err := NewPlayer(d, &testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Seek(99 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	// testDemuxer.Seek finds first packet >= 99ms, which is frame 3 at 99ms
	if p.CurrentFrame().YCbCr.Y[0] != 3 {
		t.Errorf("expected frame 3 after seek, got %d", p.CurrentFrame().YCbCr.Y[0])
	}
}

func TestFrameRGBACaching(t *testing.T) {
	d := newTestDemuxer(1, 33*time.Millisecond)
	p, err := NewPlayer(d, &testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	f := p.CurrentFrame()
	rgba1 := f.RGBA()
	rgba2 := f.RGBA()
	if &rgba1[0] != &rgba2[0] {
		t.Error("expected RGBA to return cached slice")
	}
	if len(rgba1) != 4*4*4 {
		t.Errorf("expected %d bytes, got %d", 4*4*4, len(rgba1))
	}
}

func TestDuration(t *testing.T) {
	d := newTestDemuxer(10, 33*time.Millisecond)
	p, err := NewPlayer(d, &testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Duration() != d.Duration() {
		t.Errorf("expected %v, got %v", d.Duration(), p.Duration())
	}
}

func makeTestFrame(w, h int) *Frame {
	ycbcr := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	for i := range ycbcr.Y {
		ycbcr.Y[i] = uint8((i * 7) % 256)
	}
	for i := range ycbcr.Cb {
		ycbcr.Cb[i] = uint8((i * 13) % 256)
		ycbcr.Cr[i] = uint8((i * 23) % 256)
	}
	return &Frame{
		YCbCr:  ycbcr,
		Width:  w,
		Height: h,
	}
}

func TestConvertRGBA(t *testing.T) {
	f := makeTestFrame(16, 16)
	got := f.ConvertRGBA(nil)
	want := f.RGBA()

	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("mismatch at byte %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestConvertRGBAMatchesStdlib(t *testing.T) {
	f := makeTestFrame(8, 8)
	rgba := f.ConvertRGBA(nil)

	for y := 0; y < f.Height; y++ {
		for x := 0; x < f.Width; x++ {
			yi := y*f.YCbCr.YStride + x
			ci := (y/2)*f.YCbCr.CStride + x/2
			wantR, wantG, wantB := color.YCbCrToRGB(
				f.YCbCr.Y[yi], f.YCbCr.Cb[ci], f.YCbCr.Cr[ci],
			)
			pi := (y*f.Width + x) * 4
			gotR, gotG, gotB, gotA := rgba[pi], rgba[pi+1], rgba[pi+2], rgba[pi+3]
			if gotR != wantR || gotG != wantG || gotB != wantB || gotA != 0xff {
				t.Fatalf("pixel (%d,%d): got (%d,%d,%d,%d), want (%d,%d,%d,255)",
					x, y, gotR, gotG, gotB, gotA, wantR, wantG, wantB)
			}
		}
	}
}

func TestConvertRGBABufferReuse(t *testing.T) {
	f := makeTestFrame(16, 16)
	buf := make([]byte, f.Width*f.Height*4)
	got := f.ConvertRGBA(buf)
	if &got[0] != &buf[0] {
		t.Error("expected ConvertRGBA to reuse the provided buffer")
	}
}

func TestConvertRGBANilBuffer(t *testing.T) {
	f := makeTestFrame(16, 16)
	got := f.ConvertRGBA(nil)
	if got == nil {
		t.Fatal("expected non-nil result from ConvertRGBA(nil)")
	}
	if len(got) != f.Width*f.Height*4 {
		t.Errorf("expected %d bytes, got %d", f.Width*f.Height*4, len(got))
	}
}

func BenchmarkFrameRGBA(b *testing.B) {
	f := makeTestFrame(1920, 1080)
	for b.Loop() {
		f.rgba = nil
		f.RGBA()
	}
}

func BenchmarkConvertRGBA(b *testing.B) {
	f := makeTestFrame(1920, 1080)
	buf := make([]byte, f.Width*f.Height*4)
	for b.Loop() {
		f.ConvertRGBA(buf)
	}
}

func BenchmarkConvertRGBA1080p(b *testing.B) {
	f := makeTestFrame(1920, 1080)
	buf := make([]byte, 1920*1080*4)
	for b.Loop() {
		buf = f.ConvertRGBA(buf)
	}
}

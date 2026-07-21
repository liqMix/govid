package govid

import (
	"image"
	"io"
	"sync"
	"testing"
	"time"
)

// slowCodec decodes with a fixed delay and counts how many frames it produced,
// standing in for a real decoder's per-frame cost.
type slowCodec struct {
	delay time.Duration

	mu    sync.Mutex
	count int
}

func (c *slowCodec) Decode(pkt Packet) (*Frame, error) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	c.mu.Lock()
	c.count++
	c.mu.Unlock()

	ycbcr := image.NewYCbCr(image.Rect(0, 0, 4, 4), image.YCbCrSubsampleRatio420)
	for i := range ycbcr.Y {
		ycbcr.Y[i] = pkt.Data[0]
	}
	return &Frame{YCbCr: ycbcr, Timestamp: pkt.Timestamp, Width: 4, Height: 4}, nil
}

func (c *slowCodec) Flush() {}

func (c *slowCodec) decodeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// waitForFrame repeatedly advances the player to at until the current frame
// carries wantY, allowing the background decoder time to catch up.
func waitForFrame(t *testing.T, p *Player, at time.Duration, wantY byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.UpdateToTime(at)
		if p.CurrentFrame().YCbCr.Y[0] == wantY {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for frame %d at %v, got frame %d",
				wantY, at, p.CurrentFrame().YCbCr.Y[0])
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAsyncPlayerDeliversFramesInOrder(t *testing.T) {
	d := newTestDemuxer(10, 33*time.Millisecond)
	p, err := NewAsyncPlayer(d, &testCodec{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if p.CurrentFrame().YCbCr.Y[0] != 0 {
		t.Fatalf("expected frame 0 at start, got %d", p.CurrentFrame().YCbCr.Y[0])
	}
	p.Play()

	for i := 1; i < 10; i++ {
		waitForFrame(t, p, time.Duration(i)*33*time.Millisecond, byte(i))
	}
}

func TestAsyncPlayerDropsFramesOnTimeJump(t *testing.T) {
	d := newTestDemuxer(10, 33*time.Millisecond)
	p, err := NewAsyncPlayer(d, &testCodec{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Play()

	waitForFrame(t, p, 100*time.Millisecond, 3)
}

func TestAsyncPlayerDoesNotBlockOnDecode(t *testing.T) {
	const decodeDelay = 100 * time.Millisecond
	d := newTestDemuxer(10, time.Millisecond)
	c := &slowCodec{delay: decodeDelay}
	p, err := NewAsyncPlayer(d, c, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Play()

	// Ask for a position far beyond what the decoder can have reached. The call
	// must return promptly instead of waiting on decodes.
	start := time.Now()
	p.UpdateToTime(9 * time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > decodeDelay/2 {
		t.Errorf("UpdateToTime blocked for %v; expected it to return without waiting on a decode", elapsed)
	}
	if p.CurrentFrame() == nil {
		t.Fatal("current frame went nil while the decoder was behind")
	}
}

func TestAsyncPlayerDecodesAhead(t *testing.T) {
	const depth = 4
	d := newTestDemuxer(20, 33*time.Millisecond)
	c := &slowCodec{}
	p, err := NewAsyncPlayer(d, c, depth)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// The player is paused and has consumed 2 frames; the decode goroutine
	// should still fill the queue to its depth.
	deadline := time.Now().Add(5 * time.Second)
	for c.decodeCount() < depth+2 {
		if time.Now().After(deadline) {
			t.Fatalf("decoder only produced %d frames; expected at least %d", c.decodeCount(), depth+2)
		}
		time.Sleep(time.Millisecond)
	}

	// Backpressure: the queue is bounded, so decoding must not run away.
	time.Sleep(50 * time.Millisecond)
	if got := c.decodeCount(); got > depth+3 {
		t.Errorf("decoder produced %d frames with depth %d; queue is not bounded", got, depth)
	}
}

func TestAsyncPlayerSeek(t *testing.T) {
	d := newTestDemuxer(10, 33*time.Millisecond)
	p, err := NewAsyncPlayer(d, &testCodec{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.Seek(99 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// Frames queued from before the seek must not leak through.
	if got := p.CurrentFrame().YCbCr.Y[0]; got != 3 {
		t.Fatalf("expected frame 3 after seek, got %d", got)
	}
	p.Play()
	waitForFrame(t, p, 132*time.Millisecond, 4)
}

func TestAsyncPlayerSeekAfterEOF(t *testing.T) {
	d := newTestDemuxer(3, 33*time.Millisecond)
	p, err := NewAsyncPlayer(d, &testCodec{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Play()

	waitForFrame(t, p, 200*time.Millisecond, 2)
	deadline := time.Now().Add(5 * time.Second)
	for p.State() != StateStopped {
		if time.Now().After(deadline) {
			t.Fatalf("expected StateStopped at end of stream, got %v", p.State())
		}
		p.UpdateToTime(200 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}

	// A seek must revive the parked decode goroutine.
	if err := p.Seek(0); err != nil {
		t.Fatal(err)
	}
	if got := p.CurrentFrame().YCbCr.Y[0]; got != 0 {
		t.Fatalf("expected frame 0 after seeking back, got %d", got)
	}
	p.Play()
	waitForFrame(t, p, 33*time.Millisecond, 1)
}

func TestAsyncPlayerLoop(t *testing.T) {
	d := newTestDemuxer(3, 33*time.Millisecond)
	p, err := NewAsyncPlayer(d, &testCodec{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.SetLoop(true)
	p.Play()

	deadline := time.Now().Add(5 * time.Second)
	for {
		p.UpdateToTime(200 * time.Millisecond)
		if p.CurrentFrame().YCbCr.Y[0] == 0 && p.State() == StatePlaying && p.Position() < 200*time.Millisecond {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected loop back to frame 0, got frame %d state %v",
				p.CurrentFrame().YCbCr.Y[0], p.State())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAsyncPlayerCloseStopsDecoding(t *testing.T) {
	d := newTestDemuxer(1000, 33*time.Millisecond)
	c := &slowCodec{delay: time.Millisecond}
	p, err := NewAsyncPlayer(d, c, 4)
	if err != nil {
		t.Fatal(err)
	}
	p.Play()
	p.UpdateToTime(100 * time.Millisecond)

	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	// Close returns only once the goroutine has stopped, so the decode count
	// must be final.
	after := c.decodeCount()
	time.Sleep(50 * time.Millisecond)
	if got := c.decodeCount(); got != after {
		t.Errorf("decoder kept running after Close: %d -> %d", after, got)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close returned %v", err)
	}
}

func TestAsyncPlayerPreparesRGBAOnDecodeGoroutine(t *testing.T) {
	d := newTestDemuxer(10, 33*time.Millisecond)
	p, err := NewAsyncPlayer(d, &testCodec{}, 4, WithRGBA())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	f := p.CurrentFrame()
	if !f.HasRGBA() {
		t.Fatal("expected the decode goroutine to have converted the frame already")
	}
	// The prepared pixels must match what an inline conversion produces.
	want := convertYCbCr420ToRGBA(f.YCbCr, nil)
	got := f.RGBA()
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("prepared RGBA differs at byte %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestAsyncPlayerWithoutRGBAOptionDefersConversion(t *testing.T) {
	d := newTestDemuxer(10, 33*time.Millisecond)
	p, err := NewAsyncPlayer(d, &testCodec{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if p.CurrentFrame().HasRGBA() {
		t.Error("expected no conversion without WithRGBA")
	}
	if p.CurrentFrame().RGBA() == nil {
		t.Error("expected RGBA to convert on demand")
	}
}

func TestAsyncPlayerRecyclesRGBABuffers(t *testing.T) {
	const frames = 40
	d := newTestDemuxer(frames, 33*time.Millisecond)
	p, err := NewAsyncPlayer(d, &testCodec{}, 4, WithRGBA())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Play()

	seen := make(map[*byte]bool)
	delivered := 0
	for i := 1; i < frames; i++ {
		waitForFrame(t, p, time.Duration(i)*33*time.Millisecond, byte(i))
		f := p.CurrentFrame()
		if !f.HasRGBA() {
			t.Fatalf("frame %d arrived without prepared RGBA", i)
		}
		buf := f.RGBA()
		seen[&buf[0]] = true
		delivered++

		// A live frame must never share a buffer with the queued next frame.
		if p.nextFrame != nil && p.nextFrame.HasRGBA() {
			next := p.nextFrame.RGBA()
			if &next[0] == &buf[0] {
				t.Fatalf("frame %d shares its buffer with the queued next frame", i)
			}
		}
	}

	if len(seen) >= delivered {
		t.Errorf("no buffer reuse: %d distinct buffers for %d frames", len(seen), delivered)
	}
	t.Logf("%d distinct buffers for %d frames", len(seen), delivered)
}

func TestAsyncPlayerRetiredFrameSurvivesOneChange(t *testing.T) {
	d := newTestDemuxer(10, 33*time.Millisecond)
	p, err := NewAsyncPlayer(d, &testCodec{}, 4, WithRGBA())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Play()

	first := p.CurrentFrame()
	firstPix := first.RGBA()
	firstVal := firstPix[0]

	// One frame change: the frame just displaced keeps its buffer, so a
	// consumer reading it during the following Draw sees intact pixels.
	waitForFrame(t, p, 33*time.Millisecond, 1)
	if !first.HasRGBA() {
		t.Fatal("frame buffer was recycled one change too early")
	}
	if got := first.RGBA()[0]; got != firstVal {
		t.Fatalf("retired frame pixels changed: got %d, want %d", got, firstVal)
	}

	// Second change: now it may be recycled, and RGBA must still be safe to
	// call — it recomputes rather than returning a reused buffer.
	waitForFrame(t, p, 66*time.Millisecond, 2)
	if recomputed := first.RGBA(); len(recomputed) != 4*4*4 {
		t.Fatalf("recycled frame recomputed %d bytes, want %d", len(recomputed), 4*4*4)
	}
}

func TestAsyncPlayerSeekRecyclesDiscardedFrames(t *testing.T) {
	d := newTestDemuxer(20, 33*time.Millisecond)
	p, err := NewAsyncPlayer(d, &testCodec{}, 4, WithRGBA())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Play()

	waitForFrame(t, p, 99*time.Millisecond, 3)
	if err := p.Seek(0); err != nil {
		t.Fatal(err)
	}
	if got := p.CurrentFrame().YCbCr.Y[0]; got != 0 {
		t.Fatalf("expected frame 0 after seek, got %d", got)
	}
	if !p.CurrentFrame().HasRGBA() {
		t.Error("post-seek frame lost its prepared RGBA")
	}
	// Frames decoded before the seek must not be displayed after it.
	waitForFrame(t, p, 33*time.Millisecond, 1)
}

func TestAsyncPlayerEmptyStream(t *testing.T) {
	d := newTestDemuxer(0, 33*time.Millisecond)
	p, err := NewAsyncPlayer(d, &testCodec{}, 4)
	if err != io.EOF {
		if p != nil {
			p.Close()
		}
		t.Fatalf("expected io.EOF for an empty stream, got %v", err)
	}
	if p != nil {
		t.Fatal("expected nil player for an empty stream")
	}
}

func TestSyncPlayerCloseIsNoOp(t *testing.T) {
	d := newTestDemuxer(3, 33*time.Millisecond)
	p, err := NewPlayer(d, &testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close on a sync player returned %v", err)
	}
}

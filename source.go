package govid

import (
	"io"
	"sync"
	"time"
)

// frameSource produces decoded frames in presentation order. It exists so the
// player can drive either an inline decoder (syncSource) or a decode-ahead
// goroutine (asyncSource) through the same code path.
type frameSource interface {
	// next blocks until a frame is available. It returns io.EOF at the end of
	// the stream.
	next() (*Frame, error)

	// poll returns the next frame if one is ready. It returns (nil, nil) when
	// no frame is ready yet and the caller should retry later. Only an async
	// source can report "not ready"; a sync source always decodes inline.
	poll() (*Frame, error)

	// seek repositions the underlying demuxer, flushes the codec, and discards
	// any frames already decoded from the previous position.
	seek(time.Duration) (time.Duration, error)

	// release returns a retired frame's pixel buffer for reuse. It is a no-op
	// for sources that do not pool buffers.
	release(*Frame)

	// close releases the source. It returns once no further calls will be made
	// to the underlying demuxer or codec, so the caller may then close them.
	close() error
}

// syncSource decodes on the calling goroutine.
type syncSource struct {
	d Demuxer
	c Codec
}

func (s *syncSource) next() (*Frame, error) { return readNextFrame(s.d, s.c) }

func (s *syncSource) poll() (*Frame, error) { return s.next() }

func (s *syncSource) seek(t time.Duration) (time.Duration, error) {
	actual, err := s.d.Seek(t)
	if err != nil {
		return 0, err
	}
	s.c.Flush()
	return actual, nil
}

func (s *syncSource) release(*Frame) {}

func (s *syncSource) close() error { return nil }

// AsyncOption configures a player created by NewAsyncPlayer.
type AsyncOption func(*asyncSource)

// WithRGBA makes the decode goroutine convert each frame to packed RGBA, so
// Frame.RGBA() costs nothing on the consumer's goroutine — it becomes a field
// read instead of a full-frame color conversion (~6 ms at 1080p, per
// BenchmarkConvertRGBA1080p). Use it when the consumer uploads RGBA, as the
// Ebitengine bridge does.
//
// Conversion buffers are pooled and recycled as frames retire, which avoids
// allocating a frame-sized buffer per frame (~110 MB/s at 720p30). The slice
// returned by Frame.RGBA() is therefore only valid until the second frame
// change after the one that delivered it — copy it if you need to keep it.
// Frame.RGBA() itself stays safe: a recycled frame recomputes on demand.
func WithRGBA() AsyncOption {
	return func(a *asyncSource) { a.prepareRGBA = true }
}

// decoded is one result from the decode-ahead goroutine. gen identifies the
// seek generation it was decoded for, so results from before a seek can be
// discarded.
type decoded struct {
	frame *Frame
	err   error
	gen   uint64
}

// asyncSource decodes on a background goroutine, keeping a bounded queue of
// frames ready so the caller never waits on a decode.
//
// The goroutine holds mu for the duration of each decode, so a seek costs at
// most one in-flight frame decode. Codecs deep-copy their output, so queued
// frames do not alias decoder-internal buffers.
type asyncSource struct {
	d Demuxer
	c Codec

	mu     sync.Mutex // guards d, c, gen, parked
	gen    uint64
	parked bool // decoder stopped after an error; waits for a seek to resume

	frames chan decoded
	wake   chan struct{}
	done   chan struct{}

	// prepareRGBA converts frames on the decode goroutine; pool recycles the
	// conversion buffers handed back through release.
	prepareRGBA bool
	pool        sync.Pool // holds *[]byte

	closeOnce sync.Once
	wg        sync.WaitGroup
}

func newAsyncSource(d Demuxer, c Codec, depth int, opts ...AsyncOption) *asyncSource {
	if depth < 1 {
		depth = 1
	}
	a := &asyncSource{
		d:      d,
		c:      c,
		frames: make(chan decoded, depth),
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	for _, opt := range opts {
		opt(a)
	}
	a.wg.Add(1)
	go a.run()
	return a
}

// getBuf returns a recycled conversion buffer, or nil to let ConvertRGBA
// allocate one sized for the stream.
func (a *asyncSource) getBuf() []byte {
	if p, _ := a.pool.Get().(*[]byte); p != nil {
		return (*p)[:0]
	}
	return nil
}

// release returns a frame's conversion buffer to the pool and detaches it from
// the frame, so a caller still holding the frame recomputes rather than reading
// a buffer the decoder may have overwritten.
func (a *asyncSource) release(f *Frame) {
	if f == nil || f.rgba == nil {
		return
	}
	buf := f.rgba[:0]
	f.rgba = nil
	a.pool.Put(&buf)
}

func (a *asyncSource) run() {
	defer a.wg.Done()
	for {
		a.mu.Lock()
		if a.parked {
			a.mu.Unlock()
			select {
			case <-a.wake:
				continue
			case <-a.done:
				return
			}
		}
		gen := a.gen
		frame, err := readNextFrame(a.d, a.c)
		if err != nil {
			// Stop decoding until a seek resets the position, so a stream that
			// has ended does not spin producing errors.
			a.parked = true
		}
		prepare := a.prepareRGBA
		a.mu.Unlock()

		if prepare && frame != nil {
			// Pay the color conversion here rather than on the consumer's
			// goroutine.
			frame.rgba = frame.ConvertRGBA(a.getBuf())
		}

		select {
		case a.frames <- decoded{frame: frame, err: err, gen: gen}:
		case <-a.done:
			return
		}
	}
}

// currentGen reports whether gen matches the live seek generation.
func (a *asyncSource) currentGen(gen uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return gen == a.gen
}

func (a *asyncSource) next() (*Frame, error) {
	for {
		select {
		case d := <-a.frames:
			if !a.currentGen(d.gen) {
				a.release(d.frame)
				continue
			}
			return d.frame, d.err
		case <-a.done:
			return nil, io.EOF
		}
	}
}

func (a *asyncSource) poll() (*Frame, error) {
	for {
		select {
		case d := <-a.frames:
			if !a.currentGen(d.gen) {
				a.release(d.frame)
				continue
			}
			return d.frame, d.err
		default:
			return nil, nil
		}
	}
}

func (a *asyncSource) seek(t time.Duration) (time.Duration, error) {
	a.mu.Lock()
	a.gen++
	actual, err := a.d.Seek(t)
	if err != nil {
		a.mu.Unlock()
		return 0, err
	}
	a.c.Flush()
	wasParked := a.parked
	a.parked = false
	a.mu.Unlock()

	a.drain()
	if wasParked {
		select {
		case a.wake <- struct{}{}:
		default:
		}
	}
	return actual, nil
}

// drain frees queue slots holding frames from before the current generation.
// Any straggler that lands after this is rejected by its generation stamp.
func (a *asyncSource) drain() {
	for {
		select {
		case d := <-a.frames:
			a.release(d.frame)
		default:
			return
		}
	}
}

func (a *asyncSource) close() error {
	a.closeOnce.Do(func() { close(a.done) })
	a.wg.Wait()
	return nil
}

// FrameDrainer is implemented by codecs that hold decoded frames back for
// display reordering (e.g., H.264 with B-frames). Drain returns the next
// buffered frame in display order, or nil when none remain. The player
// drains the codec at end of stream so the final frames are not lost.
type FrameDrainer interface {
	Drain() *Frame
}

func readNextFrame(d Demuxer, c Codec) (*Frame, error) {
	for {
		pkt, err := d.NextPacket()
		if err != nil {
			if err == io.EOF {
				if dr, ok := c.(FrameDrainer); ok {
					if frame := dr.Drain(); frame != nil {
						return frame, nil
					}
				}
			}
			return nil, err
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			return nil, err
		}
		if frame != nil {
			return frame, nil
		}
	}
}

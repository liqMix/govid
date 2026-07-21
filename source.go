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

func (s *syncSource) close() error { return nil }

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

	closeOnce sync.Once
	wg        sync.WaitGroup
}

func newAsyncSource(d Demuxer, c Codec, depth int) *asyncSource {
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
	a.wg.Add(1)
	go a.run()
	return a
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
		a.mu.Unlock()

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
		case <-a.frames:
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

func readNextFrame(d Demuxer, c Codec) (*Frame, error) {
	for {
		pkt, err := d.NextPacket()
		if err != nil {
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

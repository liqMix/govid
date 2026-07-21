package govid

import (
	"time"
)

// State represents the player's playback state.
type State int

const (
	StatePaused  State = iota
	StatePlaying
	StateStopped
)

// Player orchestrates demuxing, decoding, and frame timing.
// It is not thread-safe; the caller must synchronize access.
type Player struct {
	src          frameSource
	currentFrame *Frame
	nextFrame    *Frame
	state        State
	loop         bool
	eof          bool
	startTime    time.Time
	pauseOffset  time.Duration
	duration     time.Duration
}

// NewPlayer creates a player, decodes the first frame, and reads one ahead.
// Decoding happens inline on the goroutine that calls Update or UpdateToTime.
func NewPlayer(d Demuxer, c Codec) (*Player, error) {
	return newPlayer(d, &syncSource{d: d, c: c})
}

// NewAsyncPlayer creates a player that decodes on a background goroutine,
// keeping up to depth frames ready ahead of playback. Update and UpdateToTime
// never block on a decode: if the decoder has not caught up they leave the
// current frame in place and report no change.
//
// The caller must call Close before closing the demuxer or codec, so that the
// decode goroutine has stopped touching them.
func NewAsyncPlayer(d Demuxer, c Codec, depth int) (*Player, error) {
	src := newAsyncSource(d, c, depth)
	p, err := newPlayer(d, src)
	if err != nil {
		src.close()
		return nil, err
	}
	return p, nil
}

func newPlayer(d Demuxer, src frameSource) (*Player, error) {
	p := &Player{
		src:      src,
		state:    StatePaused,
		duration: d.Duration(),
	}
	first, err := src.next()
	if err != nil {
		return nil, err
	}
	p.currentFrame = first
	next, err := src.next()
	if err != nil {
		p.eof = true
	}
	p.nextFrame = next
	return p, nil
}

// Close releases the player's decoding resources. It returns once no further
// calls will be made to the demuxer or codec. Closing a player created by
// NewPlayer is a no-op. The demuxer and codec are not closed.
func (p *Player) Close() error {
	return p.src.close()
}

// Play begins or resumes playback.
func (p *Player) Play() {
	if p.state == StatePlaying {
		return
	}
	p.startTime = time.Now().Add(-p.pauseOffset)
	p.state = StatePlaying
}

// Pause pauses playback, preserving position.
func (p *Player) Pause() {
	if p.state != StatePlaying {
		return
	}
	p.pauseOffset = time.Since(p.startTime)
	p.state = StatePaused
}

// Update advances the player using wall-clock time.
// Returns true if the current frame changed.
func (p *Player) Update() bool {
	if p.state != StatePlaying {
		return false
	}
	elapsed := time.Since(p.startTime)
	return p.advanceTo(elapsed)
}

// UpdateToTime advances the player to the given position using an external clock.
// Returns true if the current frame changed.
func (p *Player) UpdateToTime(t time.Duration) bool {
	if p.state != StatePlaying {
		return false
	}
	p.pauseOffset = t
	return p.advanceTo(t)
}

func (p *Player) advanceTo(elapsed time.Duration) bool {
	changed := false
	for {
		if p.nextFrame == nil {
			if p.eof {
				break
			}
			next, err := p.src.poll()
			if err != nil {
				p.eof = true
				break
			}
			if next == nil {
				// Async source: the decoder has not produced the next frame
				// yet. Hold the current frame rather than blocking.
				break
			}
			p.nextFrame = next
		}
		if elapsed < p.nextFrame.Timestamp {
			break
		}
		p.currentFrame = p.nextFrame
		p.nextFrame = nil
		changed = true
	}
	if p.eof && p.nextFrame == nil && changed {
		if p.loop {
			p.restartLoop()
			return true
		}
		p.state = StateStopped
	}
	return changed
}

func (p *Player) restartLoop() {
	if _, err := p.reload(0); err != nil {
		p.state = StateStopped
		return
	}
	p.startTime = time.Now()
	p.pauseOffset = 0
}

// reload seeks the source and refills the current and next frames. It returns
// the position actually seeked to.
func (p *Player) reload(t time.Duration) (time.Duration, error) {
	actual, err := p.src.seek(t)
	if err != nil {
		return 0, err
	}
	p.eof = false
	p.nextFrame = nil
	first, err := p.src.next()
	if err != nil {
		p.eof = true
		return 0, err
	}
	p.currentFrame = first
	next, err := p.src.next()
	if err != nil {
		p.eof = true
	}
	p.nextFrame = next
	return actual, nil
}

// Seek jumps to the given position.
func (p *Player) Seek(t time.Duration) error {
	actual, err := p.reload(t)
	if err != nil {
		return err
	}
	p.pauseOffset = actual
	if p.state == StatePlaying {
		p.startTime = time.Now().Add(-actual)
	}
	return nil
}

// SetLoop enables or disables looping.
func (p *Player) SetLoop(v bool) {
	p.loop = v
}

// CurrentFrame returns the current decoded frame.
func (p *Player) CurrentFrame() *Frame {
	return p.currentFrame
}

// Position returns the current playback position.
func (p *Player) Position() time.Duration {
	if p.state == StatePlaying {
		return time.Since(p.startTime)
	}
	return p.pauseOffset
}

// Duration returns the total duration of the video.
func (p *Player) Duration() time.Duration {
	return p.duration
}

// State returns the current playback state.
func (p *Player) State() State {
	return p.state
}

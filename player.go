package govid

import (
	"io"
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
	demuxer      Demuxer
	codec        Codec
	currentFrame *Frame
	nextFrame    *Frame
	state        State
	loop         bool
	startTime    time.Time
	pauseOffset  time.Duration
	duration     time.Duration
}

// NewPlayer creates a player, decodes the first frame, and reads one ahead.
func NewPlayer(d Demuxer, c Codec) (*Player, error) {
	p := &Player{
		demuxer:  d,
		codec:    c,
		state:    StatePaused,
		duration: d.Duration(),
	}
	first, err := readNextFrame(d, c)
	if err != nil {
		return nil, err
	}
	p.currentFrame = first
	p.nextFrame, _ = readNextFrame(d, c)
	return p, nil
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
	for p.nextFrame != nil && elapsed >= p.nextFrame.Timestamp {
		p.currentFrame = p.nextFrame
		changed = true
		next, err := readNextFrame(p.demuxer, p.codec)
		if err != nil {
			p.nextFrame = nil
			break
		}
		p.nextFrame = next
	}
	if p.nextFrame == nil && changed {
		if p.loop {
			p.restartLoop()
			return true
		}
		p.state = StateStopped
	}
	return changed
}

func (p *Player) restartLoop() {
	_, err := p.demuxer.Seek(0)
	if err != nil {
		p.state = StateStopped
		return
	}
	p.codec.Flush()
	first, err := readNextFrame(p.demuxer, p.codec)
	if err != nil {
		p.state = StateStopped
		return
	}
	p.currentFrame = first
	p.nextFrame, _ = readNextFrame(p.demuxer, p.codec)
	p.startTime = time.Now()
	p.pauseOffset = 0
}

// Seek jumps to the given position.
func (p *Player) Seek(t time.Duration) error {
	actual, err := p.demuxer.Seek(t)
	if err != nil {
		return err
	}
	p.codec.Flush()
	first, err := readNextFrame(p.demuxer, p.codec)
	if err != nil {
		return err
	}
	p.currentFrame = first
	p.nextFrame, _ = readNextFrame(p.demuxer, p.codec)
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

func readNextFrame(d Demuxer, c Codec) (*Frame, error) {
	for {
		pkt, err := d.NextPacket()
		if err != nil {
			if err == io.EOF {
				return nil, err
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

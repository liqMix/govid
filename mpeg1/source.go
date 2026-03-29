package mpeg1

import (
	"image"
	"io"
	"time"

	govid "github.com/liqmix/govid"
	"github.com/gen2brain/mpeg"
)

// Source implements both govid.Demuxer and govid.Codec for MPEG-1 video.
// Usage: src := mpeg1.NewSource(reader); player := govid.NewPlayer(src, src)
type Source struct {
	m           *mpeg.MPEG
	cachedFrame *govid.Frame
	width       int
	height      int
	frameRate   float64
	duration    time.Duration
}

// NewSource creates an MPEG-1 source from a seekable reader.
func NewSource(r io.ReadSeeker) (*Source, error) {
	m, err := mpeg.New(r)
	if err != nil {
		return nil, err
	}
	m.SetAudioEnabled(false)

	if m.NumVideoStreams() == 0 {
		return nil, govid.ErrNoVideoTrack
	}

	return &Source{
		m:         m,
		width:     m.Width(),
		height:    m.Height(),
		frameRate: m.Framerate(),
		duration:  m.Duration(),
	}, nil
}

// NextPacket returns a token packet. The actual frame is cached internally.
func (s *Source) NextPacket() (govid.Packet, error) {
	if s.cachedFrame != nil {
		return govid.Packet{
			Timestamp: s.cachedFrame.Timestamp,
			Keyframe:  true,
		}, nil
	}

	if s.m.HasEnded() {
		return govid.Packet{}, io.EOF
	}

	frame := s.m.DecodeVideo()
	if frame == nil {
		s.drainDone()
		return govid.Packet{}, io.EOF
	}

	s.cachedFrame = convertFrame(frame)
	return govid.Packet{
		Timestamp: s.cachedFrame.Timestamp,
		Keyframe:  true,
	}, nil
}

// Decode returns the cached frame from the last NextPacket call.
func (s *Source) Decode(pkt govid.Packet) (*govid.Frame, error) {
	f := s.cachedFrame
	s.cachedFrame = nil
	return f, nil
}

// Flush resets decoder state.
func (s *Source) Flush() {}

// Seek seeks to the given time position.
func (s *Source) Seek(t time.Duration) (time.Duration, error) {
	s.drainDone()
	frame := s.m.SeekFrame(t, false)
	if frame == nil {
		return 0, govid.ErrSeekNotSupported
	}
	s.cachedFrame = convertFrame(frame)
	return s.cachedFrame.Timestamp, nil
}

// Duration returns the video duration.
func (s *Source) Duration() time.Duration {
	return s.duration
}

// VideoInfo returns the video stream properties.
func (s *Source) VideoInfo() govid.VideoInfo {
	return govid.VideoInfo{
		Width:     s.width,
		Height:    s.height,
		FrameRate: s.frameRate,
	}
}

// Close releases resources.
func (s *Source) Close() error {
	s.drainDone()
	return nil
}

// drainDone reads any pending value from the gen2brain/mpeg Done channel
// to prevent handleEnd from blocking on a full buffered channel.
func (s *Source) drainDone() {
	select {
	case <-s.m.Done():
	default:
	}
}

func convertFrame(f *mpeg.Frame) *govid.Frame {
	// Deep copy YCbCr data since the decoder reuses buffers.
	ycbcr := &image.YCbCr{
		Y:              make([]byte, len(f.Y.Data)),
		Cb:             make([]byte, len(f.Cb.Data)),
		Cr:             make([]byte, len(f.Cr.Data)),
		YStride:        f.Y.Width,
		CStride:        f.Cb.Width,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, f.Width, f.Height),
	}
	copy(ycbcr.Y, f.Y.Data)
	copy(ycbcr.Cb, f.Cb.Data)
	copy(ycbcr.Cr, f.Cr.Data)

	return &govid.Frame{
		YCbCr:     ycbcr,
		Timestamp: time.Duration(f.Time * float64(time.Second)),
		Width:     f.Width,
		Height:    f.Height,
	}
}

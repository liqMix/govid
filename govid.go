package govid

import (
	"image"
	"time"
)

// Packet is a compressed data unit from a demuxer.
type Packet struct {
	Data      []byte
	Timestamp time.Duration
	Keyframe  bool
}

// Frame is a decoded video frame.
type Frame struct {
	YCbCr     *image.YCbCr
	Timestamp time.Duration
	Width     int
	Height    int
	rgba      []byte
}

// VideoInfo describes the video stream properties.
type VideoInfo struct {
	Width     int
	Height    int
	FrameRate float64
}

// Demuxer reads compressed packets from a container format.
type Demuxer interface {
	NextPacket() (Packet, error)
	Seek(time.Duration) (time.Duration, error)
	Duration() time.Duration
	VideoInfo() VideoInfo
	Close() error
}

// Codec decodes compressed packets into frames.
type Codec interface {
	Decode(Packet) (*Frame, error)
	Flush()
}

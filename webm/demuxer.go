package webm

import (
	"fmt"
	"io"
	"time"

	"github.com/at-wat/ebml-go"
	"github.com/at-wat/ebml-go/mkvcore"

	govid "github.com/liqmix/govid"
)

// EBML structs for metadata parsing.
type webmVideo struct {
	PixelWidth  uint64 `ebml:"PixelWidth"`
	PixelHeight uint64 `ebml:"PixelHeight"`
}

type webmTrackEntry struct {
	TrackNumber     uint64     `ebml:"TrackNumber"`
	TrackType       uint64     `ebml:"TrackType"`
	CodecID         string     `ebml:"CodecID"`
	CodecPrivate    []byte     `ebml:"CodecPrivate,omitempty"`
	DefaultDuration uint64     `ebml:"DefaultDuration,omitempty"`
	Video           *webmVideo `ebml:"Video"`
}

type webmTracks struct {
	TrackEntry []webmTrackEntry `ebml:"TrackEntry"`
}

type webmInfo struct {
	TimecodeScale uint64  `ebml:"TimecodeScale"`
	Duration      float64 `ebml:"Duration,omitempty"`
}

type webmSegment struct {
	Info   webmInfo   `ebml:"Info"`
	Tracks webmTracks `ebml:"Tracks,stop"`
}

type webmRoot struct {
	Header  any         `ebml:"EBML"`
	Segment webmSegment `ebml:"Segment,size=unknown"`
}

// Demuxer reads video packets from a WebM container.
// Supports VP8 (V_VP8) and AV1 (V_AV1) codecs.
type Demuxer struct {
	reader       mkvcore.BlockReadCloser
	readers      []mkvcore.BlockReadCloserWithTrackEntry
	info         govid.VideoInfo
	duration     time.Duration
	codecID      string
	codecPrivate []byte
	firstPacket  bool // true until first keyframe has been sent
	closed       bool
}

// NewDemuxer creates a WebM demuxer from a seekable reader.
func NewDemuxer(r io.ReadSeeker) (*Demuxer, error) {
	// First pass: parse metadata.
	var root webmRoot
	err := ebml.Unmarshal(r, &root)
	if err != nil && err != ebml.ErrReadStopped {
		if err != io.EOF {
			return nil, err
		}
	}

	seg := root.Segment
	timestampScale := seg.Info.TimecodeScale
	if timestampScale == 0 {
		timestampScale = 1_000_000 // default: 1ms
	}

	// Find video track.
	var videoTrack *webmTrackEntry
	for i := range seg.Tracks.TrackEntry {
		if seg.Tracks.TrackEntry[i].TrackType == 1 {
			videoTrack = &seg.Tracks.TrackEntry[i]
			break
		}
	}
	if videoTrack == nil {
		return nil, govid.ErrNoVideoTrack
	}
	if videoTrack.CodecID != "V_VP8" && videoTrack.CodecID != "V_AV1" {
		return nil, fmt.Errorf("webm: unsupported video codec %s (expected V_VP8 or V_AV1)", videoTrack.CodecID)
	}

	var vi govid.VideoInfo
	if videoTrack.Video != nil {
		vi.Width = int(videoTrack.Video.PixelWidth)
		vi.Height = int(videoTrack.Video.PixelHeight)
	}
	if videoTrack.DefaultDuration > 0 {
		vi.FrameRate = 1e9 / float64(videoTrack.DefaultDuration)
	}

	dur := time.Duration(seg.Info.Duration * float64(timestampScale))

	// Second pass: create block reader.
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	readers, err := mkvcore.NewSimpleBlockReader(r,
		mkvcore.WithOnFatalHandler(func(err error) {}),
	)
	if err != nil {
		return nil, err
	}

	// Find the video track reader and drain non-video tracks.
	// mkvcore's parser goroutine blocks if any track's channel is full,
	// so unused tracks must be continuously drained.
	var videoReader mkvcore.BlockReadCloserWithTrackEntry
	for _, rd := range readers {
		if rd.TrackEntry().TrackType == 1 && videoReader == nil {
			videoReader = rd
		} else {
			go func(r mkvcore.BlockReadCloser) {
				for {
					_, _, _, err := r.Read()
					if err != nil {
						return
					}
				}
			}(rd)
		}
	}
	if videoReader == nil {
		return nil, govid.ErrNoVideoTrack
	}

	return &Demuxer{
		reader:       videoReader,
		readers:      readers,
		info:         vi,
		duration:     dur,
		codecID:      videoTrack.CodecID,
		codecPrivate: videoTrack.CodecPrivate,
		firstPacket:  true,
	}, nil
}

// NextPacket returns the next video packet.
func (d *Demuxer) NextPacket() (govid.Packet, error) {
	data, keyframe, timestamp, err := d.reader.Read()
	if err != nil {
		return govid.Packet{}, err
	}

	// For AV1: prepend CodecPrivate (AV1CodecConfigurationRecord with
	// sequence header OBU) to the first keyframe packet.
	if d.codecID == "V_AV1" && keyframe && d.firstPacket && len(d.codecPrivate) > 0 {
		// AV1CodecConfigurationRecord has a 4-byte header before the config OBUs.
		// Skip the 4-byte header to get raw OBU bytes.
		configOBUs := d.codecPrivate
		if len(configOBUs) > 4 {
			configOBUs = configOBUs[4:]
		}
		if len(configOBUs) > 0 {
			prefixed := make([]byte, len(configOBUs)+len(data))
			n := copy(prefixed, configOBUs)
			copy(prefixed[n:], data)
			data = prefixed
		}
	}
	if keyframe {
		d.firstPacket = false
	}

	return govid.Packet{
		Data:      data,
		Timestamp: time.Duration(timestamp) * time.Millisecond,
		Keyframe:  keyframe,
	}, nil
}

// CodecID returns the detected codec identifier ("V_VP8" or "V_AV1").
func (d *Demuxer) CodecID() string {
	return d.codecID
}

// Seek is not supported in v1.
func (d *Demuxer) Seek(time.Duration) (time.Duration, error) {
	return 0, govid.ErrSeekNotSupported
}

// Duration returns the video duration.
func (d *Demuxer) Duration() time.Duration {
	return d.duration
}

// VideoInfo returns the video stream properties.
func (d *Demuxer) VideoInfo() govid.VideoInfo {
	return d.info
}

// Close releases resources.
func (d *Demuxer) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	for _, r := range d.readers {
		r.Close()
	}
	return nil
}

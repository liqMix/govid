package webm

import (
	"bytes"
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
	// newView returns an independent reader over the whole stream. Each
	// mkvcore reader stack parses in a background goroutine, so rebuilding
	// on Seek must not share a file position with an abandoned stack.
	newView        func() io.Reader
	reader         mkvcore.BlockReadCloser
	readers        []mkvcore.BlockReadCloserWithTrackEntry
	info           govid.VideoInfo
	duration       time.Duration
	timestampScale uint64 // nanoseconds per timestamp tick
	codecID        string
	codecPrivate   []byte
	firstPacket    bool // true until first keyframe has been sent
	closed         bool
}

// NewDemuxer creates a WebM demuxer from a seekable reader.
func NewDemuxer(r io.ReadSeeker) (*Demuxer, error) {
	// First pass: parse metadata.
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
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

	// Build the per-open independent view factory: a SectionReader when the
	// source supports ReaderAt (files do), else a one-time in-memory copy.
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	var newView func() io.Reader
	if ra, ok := r.(io.ReaderAt); ok {
		newView = func() io.Reader { return io.NewSectionReader(ra, 0, size) }
	} else {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		newView = func() io.Reader { return bytes.NewReader(data) }
	}

	// Second pass: create block reader.
	videoReader, readers, err := openBlockReaders(newView())
	if err != nil {
		return nil, err
	}

	return &Demuxer{
		newView:        newView,
		reader:         videoReader,
		readers:        readers,
		info:           vi,
		duration:       dur,
		timestampScale: timestampScale,
		codecID:        videoTrack.CodecID,
		codecPrivate:   videoTrack.CodecPrivate,
		firstPacket:    true,
	}, nil
}

// openBlockReaders builds the mkvcore block reader stack over r, returning
// the video track reader. Non-video tracks are continuously drained:
// mkvcore's parser goroutine blocks if any track's channel fills.
func openBlockReaders(r io.Reader) (mkvcore.BlockReadCloserWithTrackEntry, []mkvcore.BlockReadCloserWithTrackEntry, error) {
	readers, err := mkvcore.NewSimpleBlockReader(r,
		mkvcore.WithOnFatalHandler(func(err error) {}),
	)
	if err != nil {
		return nil, nil, err
	}
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
		return nil, nil, govid.ErrNoVideoTrack
	}
	return videoReader, readers, nil
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
		Timestamp: d.tsToDuration(timestamp),
		Keyframe:  keyframe,
	}, nil
}

// tsToDuration converts a block timestamp (in TimecodeScale ticks, 1 ms by
// default) to a duration.
func (d *Demuxer) tsToDuration(ts int64) time.Duration {
	return time.Duration(ts) * time.Duration(d.timestampScale)
}

// CodecID returns the detected codec identifier ("V_VP8" or "V_AV1").
func (d *Demuxer) CodecID() string {
	return d.codecID
}

// Seek repositions to the last keyframe at or before t, returning that
// keyframe's timestamp. WebM Cues are not consulted; the stream is rescanned
// from the start, which is fine for the short clips this library targets.
func (d *Demuxer) Seek(t time.Duration) (time.Duration, error) {
	if d.closed {
		return 0, fmt.Errorf("webm: demuxer closed")
	}
	reopen := func() error {
		for _, r := range d.readers {
			r.Close()
		}
		d.readers = nil
		d.reader = nil
		videoReader, readers, err := openBlockReaders(d.newView())
		if err != nil {
			return err
		}
		d.reader = videoReader
		d.readers = readers
		d.firstPacket = true
		return nil
	}

	if err := reopen(); err != nil {
		return 0, err
	}
	if t <= 0 {
		return 0, nil
	}

	// Scan forward to find the last keyframe at or before t and how many
	// packets precede it.
	skip := -1
	var keyTS time.Duration
	for n := 0; ; n++ {
		_, keyframe, ts, err := d.reader.Read()
		if err != nil {
			break // EOF: use the last keyframe found
		}
		pts := d.tsToDuration(ts)
		if pts > t {
			break
		}
		if keyframe {
			skip = n
			keyTS = pts
		}
	}
	if skip < 0 {
		// No keyframe at or before t; restart from the beginning.
		if err := reopen(); err != nil {
			return 0, err
		}
		return 0, nil
	}

	// Reopen and consume packets up to (not including) the keyframe.
	if err := reopen(); err != nil {
		return 0, err
	}
	for n := 0; n < skip; n++ {
		if _, _, _, err := d.reader.Read(); err != nil {
			return 0, err
		}
	}
	return keyTS, nil
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

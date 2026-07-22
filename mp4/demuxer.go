package mp4

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/liqmix/govid"
)

// Codec type constants.
const (
	codecH264 = iota
	codecAV1
)

// Demuxer reads video packets from an MP4 container.
// Supports H.264 (avc1) and AV1 (av01) video tracks.
type Demuxer struct {
	reader       io.ReadSeeker
	videoTrack   *mp4.TrakBox
	info         govid.VideoInfo
	duration     time.Duration
	sampleNr     uint32
	totalSamples uint32
	timescale    uint32
	syncSamples  map[uint32]bool
	codecType    int
	spsData      []byte // length-prefixed SPS NAL (H.264 only)
	ppsData      []byte // length-prefixed PPS NAL (H.264 only)
	configOBUs   []byte // AV1 config OBUs (sequence header, AV1 only)
	closed       bool
}

// NewDemuxer creates an MP4 demuxer from a seekable reader.
func NewDemuxer(r io.ReadSeeker) (*Demuxer, error) {
	f, err := mp4.DecodeFile(r)
	if err != nil {
		return nil, fmt.Errorf("decode mp4: %w", err)
	}
	if f.Moov == nil {
		return nil, fmt.Errorf("mp4: no moov box")
	}

	var videoTrack *mp4.TrakBox
	for _, trak := range f.Moov.Traks {
		if trak.Mdia != nil && trak.Mdia.Hdlr != nil &&
			trak.Mdia.Hdlr.HandlerType == "vide" {
			videoTrack = trak
			break
		}
	}
	if videoTrack == nil {
		return nil, fmt.Errorf("mp4: no video track")
	}

	stbl := videoTrack.Mdia.Minf.Stbl
	stsd := stbl.Stsd

	d := &Demuxer{
		reader:     r,
		videoTrack: videoTrack,
	}

	if stsd.AvcX != nil {
		// H.264 track.
		d.codecType = codecH264
		if stsd.AvcX.AvcC == nil {
			return nil, fmt.Errorf("mp4: no avcC box")
		}
		dcr := stsd.AvcX.AvcC.DecConfRec
		if len(dcr.SPSnalus) == 0 {
			return nil, fmt.Errorf("mp4: no SPS in avcC")
		}
		if len(dcr.PPSnalus) == 0 {
			return nil, fmt.Errorf("mp4: no PPS in avcC")
		}
		d.spsData = lengthPrefix(dcr.SPSnalus[0])
		d.ppsData = lengthPrefix(dcr.PPSnalus[0])
		d.info.Width = int(stsd.AvcX.Width)
		d.info.Height = int(stsd.AvcX.Height)
	} else if stsd.Av01 != nil {
		// AV1 track.
		d.codecType = codecAV1
		d.info.Width = int(stsd.Av01.Width)
		d.info.Height = int(stsd.Av01.Height)
		if stsd.Av01.Av1C != nil {
			d.configOBUs = make([]byte, len(stsd.Av01.Av1C.ConfigOBUs))
			copy(d.configOBUs, stsd.Av01.Av1C.ConfigOBUs)
		}
	} else {
		return nil, fmt.Errorf("mp4: unsupported video codec (no avc1 or av01)")
	}

	// Build sync sample set.
	syncSamples := make(map[uint32]bool)
	if stbl.Stss != nil {
		for _, sn := range stbl.Stss.SampleNumber {
			syncSamples[sn] = true
		}
	}

	mdhd := videoTrack.Mdia.Mdhd
	timescale := mdhd.Timescale
	dur := time.Duration(float64(mdhd.Duration) / float64(timescale) * float64(time.Second))

	totalSamples := stbl.Stsz.GetNrSamples()

	if totalSamples > 0 && timescale > 0 {
		d.info.FrameRate = float64(totalSamples) / (float64(mdhd.Duration) / float64(timescale))
	}

	d.duration = dur
	d.sampleNr = 1
	d.totalSamples = totalSamples
	d.timescale = timescale
	d.syncSamples = syncSamples

	return d, nil
}

func (d *Demuxer) NextPacket() (govid.Packet, error) {
	if d.closed {
		return govid.Packet{}, fmt.Errorf("demuxer closed")
	}
	if d.sampleNr > d.totalSamples {
		return govid.Packet{}, io.EOF
	}

	ranges, err := d.videoTrack.GetRangesForSampleInterval(d.sampleNr, d.sampleNr)
	if err != nil {
		return govid.Packet{}, fmt.Errorf("get sample range: %w", err)
	}
	if len(ranges) == 0 {
		return govid.Packet{}, fmt.Errorf("no data range for sample %d", d.sampleNr)
	}

	dr := ranges[0]
	if _, err := d.reader.Seek(int64(dr.Offset), io.SeekStart); err != nil {
		return govid.Packet{}, fmt.Errorf("seek to sample: %w", err)
	}

	data := make([]byte, dr.Size)
	if _, err := io.ReadFull(d.reader, data); err != nil {
		return govid.Packet{}, fmt.Errorf("read sample: %w", err)
	}

	stts := d.videoTrack.Mdia.Minf.Stbl.Stts
	decTime, _ := stts.GetDecodeTime(d.sampleNr)
	// Composition (presentation) time = decode time + ctts offset. Streams
	// with B-frames deliver packets in decode order; the timestamp must be
	// the display time so reordered frames carry correct presentation times.
	compTime := int64(decTime)
	if ctts := d.videoTrack.Mdia.Minf.Stbl.Ctts; ctts != nil {
		compTime += int64(ctts.GetCompositionTimeOffset(d.sampleNr))
	}
	ts := time.Duration(float64(compTime) / float64(d.timescale) * float64(time.Second))

	keyframe := d.isSync(d.sampleNr)

	if keyframe {
		switch d.codecType {
		case codecH264:
			// Prepend SPS + PPS to IDR packets.
			prefixed := make([]byte, len(d.spsData)+len(d.ppsData)+len(data))
			n := copy(prefixed, d.spsData)
			n += copy(prefixed[n:], d.ppsData)
			copy(prefixed[n:], data)
			data = prefixed
		case codecAV1:
			// Prepend config OBUs (sequence header) to keyframes.
			if len(d.configOBUs) > 0 {
				prefixed := make([]byte, len(d.configOBUs)+len(data))
				n := copy(prefixed, d.configOBUs)
				copy(prefixed[n:], data)
				data = prefixed
			}
		}
	}

	d.sampleNr++

	return govid.Packet{
		Data:      data,
		Timestamp: ts,
		Keyframe:  keyframe,
	}, nil
}

func (d *Demuxer) Seek(t time.Duration) (time.Duration, error) {
	if d.closed {
		return 0, fmt.Errorf("demuxer closed")
	}

	targetTime := uint64(t.Seconds() * float64(d.timescale))
	stts := d.videoTrack.Mdia.Minf.Stbl.Stts

	// Find the sample at or before the target time.
	targetSample, err := stts.GetSampleNrAtTime(targetTime)
	if err != nil {
		// If target is past end, seek to last sync sample.
		targetSample = d.totalSamples
	}

	// Walk backward to find the nearest sync sample at or before target.
	syncSample := d.findSyncBefore(targetSample)
	d.sampleNr = syncSample

	decTime, _ := stts.GetDecodeTime(syncSample)
	actualTime := time.Duration(float64(decTime) / float64(d.timescale) * float64(time.Second))
	return actualTime, nil
}

func (d *Demuxer) Duration() time.Duration {
	return d.duration
}

func (d *Demuxer) VideoInfo() govid.VideoInfo {
	return d.info
}

func (d *Demuxer) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	if c, ok := d.reader.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// NALLengthSize returns the byte size of NAL unit length prefixes (always 4).
// Only meaningful for H.264 tracks.
func (d *Demuxer) NALLengthSize() int {
	return 4
}

// CodecType returns the detected codec type ("h264" or "av1").
func (d *Demuxer) CodecType() string {
	if d.codecType == codecAV1 {
		return "av1"
	}
	return "h264"
}

func (d *Demuxer) isSync(sampleNr uint32) bool {
	// If there's no Stss box, all samples are sync samples.
	if len(d.syncSamples) == 0 {
		return true
	}
	return d.syncSamples[sampleNr]
}

func (d *Demuxer) findSyncBefore(sampleNr uint32) uint32 {
	if len(d.syncSamples) == 0 {
		return sampleNr
	}
	for s := sampleNr; s >= 1; s-- {
		if d.syncSamples[s] {
			return s
		}
	}
	return 1
}

// lengthPrefix wraps raw NAL data with a 4-byte big-endian length prefix.
func lengthPrefix(nalu []byte) []byte {
	buf := make([]byte, 4+len(nalu))
	binary.BigEndian.PutUint32(buf, uint32(len(nalu)))
	copy(buf[4:], nalu)
	return buf
}

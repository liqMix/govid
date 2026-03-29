package vp8

import (
	"bytes"
	"os"
	"testing"
)

func TestMotionTestHeaderTrace(t *testing.T) {
	const webmPath = "testdata/motion_test.webm"

	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("not found")
	}

	dm := openTestDemuxer(t, webmPath)
	dec := NewDecoder()

	// Decode keyframe.
	pkt0, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt0.Data), len(pkt0.Data))
	dec.DecodeFrameHeader()
	dec.DecodeFrame()

	// Frame 1 header trace.
	pkt1, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt1.Data), len(pkt1.Data))
	fh, _ := dec.DecodeFrameHeader()
	t.Logf("frame 1: firstPartLen=%d pktLen=%d keyframe=%v", fh.FirstPartitionLen, len(pkt1.Data), fh.KeyFrame)

	// Manually parse headers, logging bit positions.
	firstPartition := make([]byte, fh.FirstPartitionLen)
	dec.r.ReadFull(firstPartition)
	dec.fp.init(firstPartition)
	t.Logf("after init: r=%d count=%d (bits=%d)", dec.fp.r, dec.fp.count, dec.fp.r*8-dec.fp.count)

	// Segment header
	b0 := dec.fp.r*8 - dec.fp.count
	dec.parseSegmentHeader()
	t.Logf("after parseSegmentHeader: bits=%d (+%d) seg.use=%v seg.update=%v",
		dec.fp.r*8-dec.fp.count, dec.fp.r*8-dec.fp.count-b0, dec.segmentHeader.useSegment, dec.segmentHeader.updateMap)

	b0 = dec.fp.r*8 - dec.fp.count
	dec.parseFilterHeader()
	t.Logf("after parseFilterHeader: bits=%d (+%d)", dec.fp.r*8-dec.fp.count, dec.fp.r*8-dec.fp.count-b0)

	b0 = dec.fp.r*8 - dec.fp.count
	dec.parseOtherPartitions()
	t.Logf("after parseOtherPartitions: bits=%d (+%d) nOP=%d", dec.fp.r*8-dec.fp.count, dec.fp.r*8-dec.fp.count-b0, dec.nOP)

	b0 = dec.fp.r*8 - dec.fp.count
	dec.parseQuant()
	t.Logf("after parseQuant: bits=%d (+%d)", dec.fp.r*8-dec.fp.count, dec.fp.r*8-dec.fp.count-b0)

	b0 = dec.fp.r*8 - dec.fp.count
	dec.parseInterFrameHeader()
	t.Logf("after parseInterFrameHeader: bits=%d (+%d) refreshG=%v refreshA=%v",
		dec.fp.r*8-dec.fp.count, dec.fp.r*8-dec.fp.count-b0, dec.refreshGolden, dec.refreshAltRef)

	b0 = dec.fp.r*8 - dec.fp.count
	dec.refreshEntropyProbs = dec.fp.readBit(uniformProb)
	t.Logf("after refreshEntropyProbs: bits=%d (+%d) refresh=%v",
		dec.fp.r*8-dec.fp.count, dec.fp.r*8-dec.fp.count-b0, dec.refreshEntropyProbs)

	b0 = dec.fp.r*8 - dec.fp.count
	dec.refreshLast = dec.fp.readBit(uniformProb)
	t.Logf("after refreshLast: bits=%d (+%d) refresh=%v",
		dec.fp.r*8-dec.fp.count, dec.fp.r*8-dec.fp.count-b0, dec.refreshLast)

	b0 = dec.fp.r*8 - dec.fp.count
	dec.parseTokenProb()
	t.Logf("after parseTokenProb: bits=%d (+%d)", dec.fp.r*8-dec.fp.count, dec.fp.r*8-dec.fp.count-b0)

	b0 = dec.fp.r*8 - dec.fp.count
	dec.useSkipProb = dec.fp.readBit(uniformProb)
	if dec.useSkipProb {
		dec.skipProb = uint8(dec.fp.readUint(uniformProb, 8))
	}
	t.Logf("after skipProb: bits=%d (+%d) useSkip=%v skipProb=%d",
		dec.fp.r*8-dec.fp.count, dec.fp.r*8-dec.fp.count-b0, dec.useSkipProb, dec.skipProb)

	b0 = dec.fp.r*8 - dec.fp.count
	dec.parseInterFrameProbs()
	t.Logf("after parseInterFrameProbs: bits=%d (+%d) probIntra=%d probLast=%d probGf=%d",
		dec.fp.r*8-dec.fp.count, dec.fp.r*8-dec.fp.count-b0, dec.probIntra, dec.probLast, dec.probGf)

	totalHeaderBits := dec.fp.r*8 - dec.fp.count
	t.Logf("total header bits: %d, partition bits: %d, remaining for mode/MV: %d",
		totalHeaderBits, fh.FirstPartitionLen*8, int(fh.FirstPartitionLen)*8-totalHeaderBits)
	t.Logf("unexpectedEOF after headers: %v", dec.fp.unexpectedEOF)
}

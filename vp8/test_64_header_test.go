package vp8

import (
	"bytes"
	"os"
	"testing"
)

func TestInterframe64HeaderTrace(t *testing.T) {
	const webmPath = "testdata/interframe64.webm"
	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("not found")
	}

	dm := openTestDemuxer(t, webmPath)
	dec := NewDecoder()

	pkt0, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt0.Data), len(pkt0.Data))
	dec.DecodeFrameHeader()
	dec.ensureImg()
	dec.DecodeFrame()

	pkt1, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt1.Data), len(pkt1.Data))
	fh, _ := dec.DecodeFrameHeader()

	firstPartition := make([]byte, fh.FirstPartitionLen)
	dec.r.ReadFull(firstPartition)
	dec.fp.init(firstPartition)

	b := func() int { return dec.fp.r*8 - dec.fp.count }
	b0 := b()

	dec.parseSegmentHeader()
	t.Logf("segment: +%d (total %d) use=%v update=%v", b()-b0, b(), dec.segmentHeader.useSegment, dec.segmentHeader.updateMap)
	b0 = b()

	dec.parseFilterHeader()
	t.Logf("filter: +%d (total %d) simple=%v level=%d", b()-b0, b(), dec.filterHeader.simple, dec.filterHeader.level)
	b0 = b()

	dec.parseOtherPartitions()
	t.Logf("partitions: +%d (total %d) nOP=%d", b()-b0, b(), dec.nOP)
	b0 = b()

	dec.parseQuant()
	t.Logf("quant: +%d (total %d)", b()-b0, b())
	b0 = b()

	dec.parseInterFrameHeader()
	t.Logf("inter header: +%d (total %d)", b()-b0, b())
	b0 = b()

	dec.refreshEntropyProbs = dec.fp.readBit(uniformProb)
	t.Logf("refresh_entropy: +%d (total %d) val=%v", b()-b0, b(), dec.refreshEntropyProbs)
	if !dec.refreshEntropyProbs {
		dec.savedTokenProb = dec.tokenProb
		dec.savedMVProb = dec.mvProb
		dec.savedYModeProb = dec.yModeProb
		dec.savedUVModeProb = dec.uvModeProb
	}
	b0 = b()

	dec.refreshLast = dec.fp.readBit(uniformProb)
	t.Logf("refresh_last: +%d (total %d) val=%v", b()-b0, b(), dec.refreshLast)
	b0 = b()

	dec.parseTokenProb()
	t.Logf("token_prob: +%d (total %d)", b()-b0, b())
	b0 = b()

	dec.useSkipProb = dec.fp.readBit(uniformProb)
	if dec.useSkipProb {
		dec.skipProb = uint8(dec.fp.readUint(uniformProb, 8))
	}
	t.Logf("skip: +%d (total %d) use=%v prob=%d", b()-b0, b(), dec.useSkipProb, dec.skipProb)
	b0 = b()

	dec.parseInterFrameProbs()
	t.Logf("inter_probs: +%d (total %d) probIntra=%d probLast=%d probGf=%d",
		b()-b0, b(), dec.probIntra, dec.probLast, dec.probGf)

	total := b()
	t.Logf("TOTAL header: %d bits, partition: %d bits, remaining: %d bits",
		total, fh.FirstPartitionLen*8, int(fh.FirstPartitionLen)*8-total)
	t.Logf("Final state: r=%d count=%d rng=%d value=%08x eof=%v",
		dec.fp.r, dec.fp.count, dec.fp.rng, dec.fp.value, dec.fp.unexpectedEOF)
}

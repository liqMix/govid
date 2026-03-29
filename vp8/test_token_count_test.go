package vp8

import (
	"bytes"
	"os"
	"testing"
)

func TestCountTokenProbUpdates(t *testing.T) {
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
	dec.DecodeFrameHeader()

	firstPartition := make([]byte, dec.frameHeader.FirstPartitionLen)
	dec.r.ReadFull(firstPartition)
	dec.fp.init(firstPartition)

	// Skip headers before token prob.
	dec.parseSegmentHeader()
	dec.parseFilterHeader()
	dec.parseOtherPartitions()
	dec.parseQuant()
	dec.parseInterFrameHeader()
	dec.refreshEntropyProbs = dec.fp.readBit(uniformProb)
	dec.refreshLast = dec.fp.readBit(uniformProb)

	b0 := dec.fp.r*8 - dec.fp.count

	// Count updates manually.
	updates := 0
	for i := range dec.tokenProb {
		for j := range dec.tokenProb[i] {
			for k := range dec.tokenProb[i][j] {
				for l := range dec.tokenProb[i][j][k] {
					if dec.fp.readBit(tokenProbUpdateProb[i][j][k][l]) {
						dec.tokenProb[i][j][k][l] = uint8(dec.fp.readUint(uniformProb, 8))
						updates++
					}
				}
			}
		}
	}

	b1 := dec.fp.r*8 - dec.fp.count
	t.Logf("token prob: %d bits consumed, %d updates (expected: 1056 flags + %d*8 = %d bits)",
		b1-b0, updates, updates, 1056*0 + updates*9)
	t.Logf("avg bits per flag (no update): %.3f", float64(b1-b0-updates*8)/float64(1056-updates))
}

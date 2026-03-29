package vp8

import (
	"os"
	"testing"
)

func TestVerifyFirstPartitionLen(t *testing.T) {
	const webmPath = "testdata/interframe64.webm"
	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("not found")
	}

	dm := openTestDemuxer(t, webmPath)

	// Skip keyframe.
	dm.NextPacket()

	// Frame 1.
	pkt, _ := dm.NextPacket()
	data := pkt.Data
	t.Logf("packet len=%d, first 10 bytes: %02x", len(data), data[:10])

	// Parse frame tag manually.
	b0, b1, b2 := data[0], data[1], data[2]
	keyFrame := (b0 & 1) == 0
	version := (b0 >> 1) & 7
	showFrame := (b0 >> 4) & 1
	firstPartLen := uint32(b0)>>5 | uint32(b1)<<3 | uint32(b2)<<11

	t.Logf("b0=%08b b1=%08b b2=%08b", b0, b1, b2)
	t.Logf("keyFrame=%v version=%d showFrame=%d firstPartLen=%d", keyFrame, version, showFrame, firstPartLen)
	t.Logf("remaining after tag: %d bytes", len(data)-3)
	t.Logf("data after first partition: %d bytes (for coefficient data)", len(data)-3-int(firstPartLen))

	// Verify the partition length makes sense.
	if int(firstPartLen) > len(data)-3 {
		t.Error("FirstPartitionLen exceeds packet data!")
	}
}

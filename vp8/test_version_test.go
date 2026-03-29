package vp8

import (
	"bytes"
	"os"
	"testing"
)

func TestCheckVersionNumbers(t *testing.T) {
	videos := []string{
		"testdata/interframe64.webm",
		"testdata/interframe.webm",
		"testdata/static.webm",
		"testdata/motion_test.webm",
	}
	for _, path := range videos {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		dm := openTestDemuxer(t, path)
		dec := NewDecoder()
		for i := 0; i < 3; i++ {
			pkt, err := dm.NextPacket()
			if err != nil {
				break
			}
			dec.Init(bytes.NewReader(pkt.Data), len(pkt.Data))
			fh, err := dec.DecodeFrameHeader()
			if err != nil {
				break
			}
			t.Logf("%s frame %d: key=%v version=%d firstPartLen=%d",
				path, i, fh.KeyFrame, fh.VersionNumber, fh.FirstPartitionLen)
		}
	}
}

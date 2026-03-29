package vp8

import (
	"bytes"
	"os"
	"testing"
)

func TestVerifyMCFix(t *testing.T) {
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

	ref := dec.refFrame[1]
	t.Logf("ref stride=%d, Y len=%d, width=%d height=%d",
		ref.YStride, len(ref.Y), ref.Rect.Max.X, ref.Rect.Max.Y)

	// Compute what the filter gets for sub-block 0 of MB(0,3) with MV=(2,-2).
	mvRow, mvCol := int16(2), int16(-2)
	mbx, mby := 0, 3
	subCol, subRow := 0, 0
	srcX := mbx*16 + subCol*4 + int(mvCol>>3)
	srcY := mby*16 + subRow*4 + int(mvRow>>3)
	xFrac := int(mvCol & 7)
	yFrac := int(mvRow & 7)

	t.Logf("Sub-block 0: MV=(%d,%d) srcX=%d srcY=%d xFrac=%d yFrac=%d",
		mvRow, mvCol, srcX, srcY, xFrac, yFrac)

	// What old code would compute:
	oldSrcOff := srcY*ref.YStride + srcX
	oldBaseY := oldSrcOff / ref.YStride
	oldBaseX := oldSrcOff - oldBaseY*ref.YStride
	if oldBaseX < 0 {
		oldBaseY--
		oldBaseX += ref.YStride
	}
	t.Logf("OLD: srcOff=%d baseX=%d baseY=%d", oldSrcOff, oldBaseX, oldBaseY)
	t.Logf("NEW: baseX=%d baseY=%d", srcX, srcY)

	// Show reference pixels at both locations.
	t.Log("OLD reference area (baseX=63, baseY=47):")
	for y := 0; y < 4; y++ {
		var vals string
		for x := 0; x < 4; x++ {
			px := 63 + x
			py := 47 + y
			if px >= 64 { px = 63 }
			if py >= 64 { py = 63 }
			vals += " " + string(rune('0'+ref.Y[py*ref.YStride+px]/26))
		}
		t.Logf("  row %d:%s (%d %d %d %d)", y,
			vals,
			ref.Y[(47+y)*ref.YStride+63],
			ref.Y[(47+y)*ref.YStride+63],
			ref.Y[(47+y)*ref.YStride+63],
			ref.Y[(47+y)*ref.YStride+63])
	}
	t.Log("NEW reference area (baseX=0 [clamped from -1], baseY=48):")
	for y := 0; y < 4; y++ {
		t.Logf("  row %d: %d %d %d %d", y,
			ref.Y[(48+y)*ref.YStride+0],
			ref.Y[(48+y)*ref.YStride+1],
			ref.Y[(48+y)*ref.YStride+2],
			ref.Y[(48+y)*ref.YStride+3])
	}
}

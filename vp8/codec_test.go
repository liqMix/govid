package vp8

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	govid "github.com/liqmix/govid"
	"github.com/liqmix/govid/webm"
)

func openTestDemuxer(t *testing.T, path string) *webm.Demuxer {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	d, err := webm.NewDemuxer(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestCodecDecodeKeyframe(t *testing.T) {
	d := openTestDemuxer(t, "../webm/testdata/test.webm")
	c := NewCodec()

	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}

	frame, err := c.Decode(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if frame == nil {
		t.Fatal("expected non-nil frame")
	}
	if frame.Width != 160 || frame.Height != 120 {
		t.Errorf("expected 160x120, got %dx%d", frame.Width, frame.Height)
	}
	if frame.YCbCr == nil {
		t.Fatal("expected non-nil YCbCr")
	}
}

func TestCodecDecodeAllKeyframes(t *testing.T) {
	d := openTestDemuxer(t, "../webm/testdata/test.webm")
	c := NewCodec()

	count := 0
	for {
		pkt, err := d.NextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !pkt.Keyframe {
			continue // skip inter-frames for now
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: %v", count, err)
		}
		if frame == nil {
			t.Fatalf("frame %d: expected non-nil frame", count)
		}
		count++
	}
	if count == 0 {
		t.Error("expected at least 1 keyframe")
	}
}

func TestCodecFlush(t *testing.T) {
	c := NewCodec()
	c.Flush()
	// Should be able to decode after flush.
	d := openTestDemuxer(t, "../webm/testdata/test.webm")
	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := c.Decode(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if frame == nil {
		t.Fatal("expected non-nil frame")
	}
}

func TestFrameRGBA(t *testing.T) {
	d := openTestDemuxer(t, "../webm/testdata/test.webm")
	c := NewCodec()

	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := c.Decode(pkt)
	if err != nil {
		t.Fatal(err)
	}
	rgba := frame.RGBA()
	expected := frame.Width * frame.Height * 4
	if len(rgba) != expected {
		t.Errorf("expected %d RGBA bytes, got %d", expected, len(rgba))
	}
}

func TestEndToEndWithPlayer(t *testing.T) {
	d := openTestDemuxer(t, "../webm/testdata/test.webm")
	c := NewCodec()

	p, err := govid.NewPlayer(d, c)
	if err != nil {
		t.Fatal(err)
	}
	if p.CurrentFrame() == nil {
		t.Fatal("expected non-nil current frame")
	}
	if p.State() != govid.StatePaused {
		t.Errorf("expected StatePaused, got %v", p.State())
	}
}

func TestCodecDecodeAllFrames(t *testing.T) {
	d := openTestDemuxer(t, "testdata/interframe.webm")
	c := NewCodec()

	count := 0
	for {
		pkt, err := d.NextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: %v", count, err)
		}
		if frame == nil {
			t.Fatalf("frame %d: expected non-nil frame", count)
		}
		if frame.Width != 160 || frame.Height != 120 {
			t.Errorf("frame %d: expected 160x120, got %dx%d", count, frame.Width, frame.Height)
		}
		count++
	}
	if count < 5 {
		t.Errorf("expected at least 5 frames (keyframe + interframes), got %d", count)
	}
	t.Logf("decoded %d frames successfully", count)
}

func TestInterframeEndToEnd(t *testing.T) {
	d := openTestDemuxer(t, "testdata/interframe.webm")
	c := NewCodec()

	p, err := govid.NewPlayer(d, c)
	if err != nil {
		t.Fatal(err)
	}
	p.Play()

	// Advance through all frames.
	for i := 0; i < 20; i++ {
		p.UpdateToTime(time.Duration(i) * 100 * time.Millisecond)
	}

	if p.CurrentFrame() == nil {
		t.Fatal("expected non-nil current frame after playback")
	}
	rgba := p.CurrentFrame().RGBA()
	if len(rgba) == 0 {
		t.Error("expected non-empty RGBA data")
	}
}

func TestInterframeRGBAValid(t *testing.T) {
	d := openTestDemuxer(t, "testdata/interframe.webm")
	c := NewCodec()

	// Decode several frames and verify RGBA is valid.
	for i := 0; i < 5; i++ {
		pkt, err := d.NextPacket()
		if err != nil {
			t.Fatal(err)
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		rgba := frame.RGBA()
		expected := frame.Width * frame.Height * 4
		if len(rgba) != expected {
			t.Errorf("frame %d: expected %d RGBA bytes, got %d", i, expected, len(rgba))
		}
		// Verify alpha channel is 0xFF (opaque).
		for j := 3; j < len(rgba); j += 4 {
			if rgba[j] != 0xFF {
				t.Errorf("frame %d: pixel %d alpha = %d, expected 255", i, j/4, rgba[j])
				break
			}
		}
	}
}

func TestDecodeVsReference(t *testing.T) {
	const webmPath = "testdata/interframe.webm"
	const refYUV = "testdata/interframe_frame0.yuv"

	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("interframe.webm not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("interframe_frame0.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	d := openTestDemuxer(t, webmPath)
	c := NewCodec()

	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := c.Decode(pkt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if frame == nil || frame.YCbCr == nil {
		t.Fatal("nil frame")
	}

	w := frame.Width
	h := frame.Height
	t.Logf("dimensions: %dx%d", w, h)

	ycbcr := frame.YCbCr
	ySize := w * h
	cw := w / 2
	ch := h / 2
	cSize := cw * ch

	if len(ref) != ySize+2*cSize {
		t.Fatalf("ref size %d != expected %d", len(ref), ySize+2*cSize)
	}

	// Compare Y plane.
	yErr, yMaxErr := 0, 0
	firstErrX, firstErrY, firstErrGot, firstErrWant := -1, -1, 0, 0
	histogram := make([]int, 256)

	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			got := int(ycbcr.Y[j*ycbcr.YStride+i])
			want := int(ref[j*w+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			histogram[d]++
			if d > 0 {
				yErr++
			}
			if d > yMaxErr {
				yMaxErr = d
			}
			if d > 0 && firstErrX == -1 {
				firstErrX = i
				firstErrY = j
				firstErrGot = got
				firstErrWant = want
			}
		}
	}

	// Compare Cb plane.
	cbOff := ySize
	cbErr, cbMaxErr := 0, 0
	for j := 0; j < ch; j++ {
		for i := 0; i < cw; i++ {
			got := int(ycbcr.Cb[j*ycbcr.CStride+i])
			want := int(ref[cbOff+j*cw+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				cbErr++
			}
			if d > cbMaxErr {
				cbMaxErr = d
			}
		}
	}

	// Compare Cr plane.
	crOff := ySize + cSize
	crErr, crMaxErr := 0, 0
	for j := 0; j < ch; j++ {
		for i := 0; i < cw; i++ {
			got := int(ycbcr.Cr[j*ycbcr.CStride+i])
			want := int(ref[crOff+j*cw+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				crErr++
			}
			if d > crMaxErr {
				crMaxErr = d
			}
		}
	}

	t.Logf("Y:  %d/%d wrong pixels (%.1f%%), max error %d", yErr, ySize, 100*float64(yErr)/float64(ySize), yMaxErr)
	t.Logf("Cb: %d/%d wrong pixels, max error %d", cbErr, cSize, cbMaxErr)
	t.Logf("Cr: %d/%d wrong pixels, max error %d", crErr, cSize, crMaxErr)

	if firstErrX >= 0 {
		t.Logf("First wrong Y pixel: (%d,%d) got=%d want=%d (MB %d,%d, blk %d,%d)",
			firstErrX, firstErrY, firstErrGot, firstErrWant,
			firstErrX/16, firstErrY/16, (firstErrX%16)/4, (firstErrY%16)/4)
	}

	// Per-MB max error for first 2 rows.
	for mby := 0; mby < 2 && mby < h/16; mby++ {
		line := fmt.Sprintf("MB row %d:", mby)
		for mbx := 0; mbx < w/16; mbx++ {
			mbMax := 0
			for j := 0; j < 16; j++ {
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					if py >= h || px >= w {
						continue
					}
					got := int(ycbcr.Y[py*ycbcr.YStride+px])
					want := int(ref[py*w+px])
					d := got - want
					if d < 0 {
						d = -d
					}
					if d > mbMax {
						mbMax = d
					}
				}
			}
			line += fmt.Sprintf(" %3d", mbMax)
		}
		t.Log(line)
	}

	// Error histogram (first 10 buckets + tail).
	t.Log("Error histogram:")
	for i := 0; i <= 10 && i < len(histogram); i++ {
		if histogram[i] > 0 {
			t.Logf("  err=%d: %d pixels", i, histogram[i])
		}
	}
	tail := 0
	for i := 11; i < len(histogram); i++ {
		tail += histogram[i]
	}
	if tail > 0 {
		t.Logf("  err>10: %d pixels", tail)
	}

	if yMaxErr > 2 || cbMaxErr > 2 || crMaxErr > 2 {
		t.Errorf("decoded frame differs from ffmpeg reference (Y max=%d, Cb max=%d, Cr max=%d)", yMaxErr, cbMaxErr, crMaxErr)
	}
}

func TestDecodeSmallMultiFrame(t *testing.T) {
	const webmPath = "testdata/interframe.webm"
	const refYUV = "testdata/interframe_frames_0_9.yuv"
	const numFrames = 10

	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("interframe_frames_0_9.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	d := openTestDemuxer(t, webmPath)
	c := NewCodec()

	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := c.Decode(pkt)
	if err != nil {
		t.Fatalf("frame 0: %v", err)
	}

	w := frame.Width
	h := frame.Height
	ySize := w * h
	cw, ch := w/2, h/2
	cSize := cw * ch
	frameSize := ySize + 2*cSize
	t.Logf("dimensions: %dx%d, frameSize=%d", w, h, frameSize)

	comparePlane := func(decoded []byte, stride int, refData []byte, pw, ph int) (wrong, maxErr int) {
		for j := 0; j < ph; j++ {
			for i := 0; i < pw; i++ {
				got := int(decoded[j*stride+i])
				want := int(refData[j*pw+i])
				d := got - want
				if d < 0 {
					d = -d
				}
				if d > 0 {
					wrong++
				}
				if d > maxErr {
					maxErr = d
				}
			}
		}
		return
	}

	// Compare all frames.
	ycbcr := frame.YCbCr
	for i := 0; i < numFrames; i++ {
		if i > 0 {
			pkt, err = d.NextPacket()
			if err != nil {
				t.Fatalf("frame %d: NextPacket: %v", i, err)
			}
			frame, err = c.Decode(pkt)
			if err != nil {
				t.Fatalf("frame %d: Decode: %v", i, err)
			}
			ycbcr = frame.YCbCr
		}

		refOff := i * frameSize
		yW, yMax := comparePlane(ycbcr.Y, ycbcr.YStride, ref[refOff:refOff+ySize], w, h)
		cbW, cbMax := comparePlane(ycbcr.Cb, ycbcr.CStride, ref[refOff+ySize:refOff+ySize+cSize], cw, ch)
		crW, crMax := comparePlane(ycbcr.Cr, ycbcr.CStride, ref[refOff+ySize+cSize:refOff+ySize+2*cSize], cw, ch)

		frameType := "inter"
		if i == 0 {
			frameType = "key"
		}
		t.Logf("frame %d (%s): Y %d/%d wrong (%.1f%%) max=%d | Cb %d/%d max=%d | Cr %d/%d max=%d",
			i, frameType,
			yW, ySize, 100*float64(yW)/float64(ySize), yMax,
			cbW, cSize, cbMax,
			crW, cSize, crMax)
	}
}

func TestDecodeSmallFrame1PerMB(t *testing.T) {
	const webmPath = "testdata/interframe.webm"
	const refYUV = "testdata/interframe_frames_0_9.yuv"

	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("interframe_frames_0_9.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	dm := openTestDemuxer(t, webmPath)
	dec := NewDecoder()

	// Decode frame 0 (keyframe).
	pkt0, err := dm.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	dec.Init(bytes.NewReader(pkt0.Data), len(pkt0.Data))
	if _, err := dec.DecodeFrameHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := dec.DecodeFrame(); err != nil {
		t.Fatal(err)
	}

	w, h := dec.frameHeader.Width, dec.frameHeader.Height
	mbw, mbh := dec.mbw, dec.mbh
	t.Logf("dimensions: %dx%d (%dx%d MBs)", w, h, mbw, mbh)

	// Decode frame 1.
	pkt1, err := dm.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	dec.Init(bytes.NewReader(pkt1.Data), len(pkt1.Data))
	fh, err := dec.DecodeFrameHeader()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("frame 1: keyframe=%v firstPartLen=%d", fh.KeyFrame, fh.FirstPartitionLen)

	img, err := dec.DecodeFrame()
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	t.Logf("unexpectedEOF=%v", dec.fp.unexpectedEOF)

	// Frame 1 reference Y data.
	ySize := w * h
	cSize := (w / 2) * (h / 2)
	frameSize := ySize + 2*cSize
	refY := ref[frameSize : frameSize+ySize]

	// Per-MB max Y error.
	modeNames := []string{"NEAREST", "NEAR", "ZERO", "NEW", "SPLIT"}
	for mby := 0; mby < mbh; mby++ {
		var line string
		for mbx := 0; mbx < mbw; mbx++ {
			maxErr := 0
			for j := 0; j < 16 && mby*16+j < h; j++ {
				for i := 0; i < 16 && mbx*16+i < w; i++ {
					got := int(img.Y[(mby*16+j)*img.YStride+mbx*16+i])
					want := int(refY[(mby*16+j)*w+mbx*16+i])
					d := got - want
					if d < 0 {
						d = -d
					}
					if d > maxErr {
						maxErr = d
					}
				}
			}

			imb := dec.upInterMB[mbx]
			modeName := "INTRA"
			if imb.refFrame != 0 && int(imb.mode) < len(modeNames) {
				modeName = modeNames[imb.mode]
			}
			line += fmt.Sprintf("  %3d(%s mv=%d,%d)", maxErr, modeName[:1], imb.mv[0], imb.mv[1])
		}
		t.Logf("MB row %d:%s", mby, line)
	}
}

func TestSmallFrame1BitTrace(t *testing.T) {
	const webmPath = "testdata/interframe.webm"

	dm := openTestDemuxer(t, webmPath)
	dec := NewDecoder()

	// Decode keyframe.
	pkt0, err := dm.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	dec.Init(bytes.NewReader(pkt0.Data), len(pkt0.Data))
	if _, err := dec.DecodeFrameHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := dec.DecodeFrame(); err != nil {
		t.Fatal(err)
	}

	// Set up frame 1.
	pkt1, err := dm.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	dec.Init(bytes.NewReader(pkt1.Data), len(pkt1.Data))
	fh, err := dec.DecodeFrameHeader()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("frame 1: keyframe=%v firstPartLen=%d dataLen=%d", fh.KeyFrame, fh.FirstPartitionLen, len(pkt1.Data))

	dec.ensureImg()
	if err := dec.parseOtherHeaders(); err != nil {
		t.Fatal(err)
	}

	t.Logf("probIntra=%d probLast=%d skipProb=%d useSkipProb=%v",
		dec.probIntra, dec.probLast, dec.skipProb, dec.useSkipProb)
	t.Logf("mvProb[0]=%v", dec.mvProb[0])
	t.Logf("mvProb[1]=%v", dec.mvProb[1])
	t.Logf("after headers: fp.r=%d fp.count=%d fp.rng=%d", dec.fp.r, dec.fp.count, dec.fp.rng)

	// Dump first partition raw bytes (first 32).
	n := 32
	if n > len(dec.fp.buf) {
		n = len(dec.fp.buf)
	}
	t.Logf("first partition bytes [0:%d]: %x", n, dec.fp.buf[:n])

	// Now decode MBs and trace bit consumption per MB.
	for mbx := 0; mbx < dec.mbw; mbx++ {
		dec.upMB[mbx] = mb{}
		dec.upInterMB[mbx] = interMB{}
	}

	modeNames := []string{"NEAREST", "NEAR", "ZERO", "NEW", "SPLIT"}
	mbCount := 0
	for mby := 0; mby < dec.mbh; mby++ {
		dec.leftMB = mb{}
		dec.leftInterMB = interMB{}
		var prevAbove interMB
		for mbx := 0; mbx < dec.mbw; mbx++ {
			dec.aboveLeftInterMB = prevAbove
			prevAbove = dec.upInterMB[mbx]

			// Capture state before this MB.
			rBefore := dec.fp.r
			countBefore := dec.fp.count
			rngBefore := dec.fp.rng
			valBefore := dec.fp.value

			skip := dec.reconstruct(mbx, mby)

			rAfter := dec.fp.r
			countAfter := dec.fp.count
			bitsBefore := rBefore*8 - countBefore
			bitsAfter := rAfter*8 - countAfter
			bitsUsed := bitsAfter - bitsBefore

			modeName := "INTRA"
			if dec.curRefFrame != 0 && int(dec.curMode) < len(modeNames) {
				modeName = modeNames[dec.curMode]
			}

			// Log first 20 MBs and any with motion.
			if mbCount < 20 || modeName != "ZERO" {
				t.Logf("MB(%d,%d) mode=%s ref=%d mv=(%d,%d) skip=%v bits=%d [r=%d cnt=%d rng=%d val=%08x]",
					mbx, mby, modeName, dec.curRefFrame,
					dec.curMV[0], dec.curMV[1], skip, bitsUsed,
					rBefore, countBefore, rngBefore, valBefore)
			}

			// Compute filter param like main decode loop does.
			fs := dec.lookupFilterParam()
			fs.inner = fs.inner || !skip
			dec.perMBFilterParams[dec.mbw*mby+mbx] = fs

			mbCount++
		}
	}
	t.Logf("total MBs: %d, final fp.r=%d fp.count=%d unexpectedEOF=%v",
		mbCount, dec.fp.r, dec.fp.count, dec.fp.unexpectedEOF)
}

func TestDecodeStaticMultiFrame(t *testing.T) {
	const webmPath = "testdata/static.webm"
	const refYUV = "testdata/static_frames_0_4.yuv"
	const numFrames = 5

	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("static_frames_0_4.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	d := openTestDemuxer(t, webmPath)
	c := NewCodec()

	const w, h = 160, 120
	ySize := w * h
	cw, ch := w/2, h/2
	cSize := cw * ch
	frameSize := ySize + 2*cSize

	comparePlane := func(decoded []byte, stride int, refData []byte, pw, ph int) (wrong, maxErr int) {
		for j := 0; j < ph; j++ {
			for i := 0; i < pw; i++ {
				got := int(decoded[j*stride+i])
				want := int(refData[j*pw+i])
				d := got - want
				if d < 0 {
					d = -d
				}
				if d > 0 {
					wrong++
				}
				if d > maxErr {
					maxErr = d
				}
			}
		}
		return
	}

	for i := 0; i < numFrames; i++ {
		pkt, err := d.NextPacket()
		if err != nil {
			t.Fatalf("frame %d: NextPacket: %v", i, err)
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: Decode: %v", i, err)
		}
		ycbcr := frame.YCbCr
		refOff := i * frameSize
		yW, yMax := comparePlane(ycbcr.Y, ycbcr.YStride, ref[refOff:refOff+ySize], w, h)
		cbW, cbMax := comparePlane(ycbcr.Cb, ycbcr.CStride, ref[refOff+ySize:refOff+ySize+cSize], cw, ch)
		crW, crMax := comparePlane(ycbcr.Cr, ycbcr.CStride, ref[refOff+ySize+cSize:refOff+ySize+2*cSize], cw, ch)

		frameType := "inter"
		if i == 0 {
			frameType = "key"
		}
		t.Logf("frame %d (%s): Y %d/%d wrong (%.1f%%) max=%d | Cb %d/%d max=%d | Cr %d/%d max=%d",
			i, frameType,
			yW, ySize, 100*float64(yW)/float64(ySize), yMax,
			cbW, cSize, cbMax,
			crW, cSize, crMax)

		if yMax > 2 {
			t.Errorf("frame %d: Y max error %d exceeds threshold", i, yMax)
		}
	}
}

func TestDecodeBakerVsReference(t *testing.T) {
	const webmPath = "../examples/videos/baker_vp8.webm"
	const refYUV = "testdata/baker_frame0.yuv"

	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("baker_vp8.webm not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("baker_frame0.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	d := openTestDemuxer(t, webmPath)
	c := NewCodec()

	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := c.Decode(pkt)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if frame == nil || frame.YCbCr == nil {
		t.Fatal("nil frame")
	}

	w := frame.Width
	h := frame.Height
	t.Logf("dimensions: %dx%d", w, h)

	ycbcr := frame.YCbCr
	ySize := w * h
	cw := w / 2
	ch := h / 2
	cSize := cw * ch

	if len(ref) != ySize+2*cSize {
		t.Fatalf("ref size %d != expected %d", len(ref), ySize+2*cSize)
	}

	// Compare Y plane.
	yErr, yMaxErr, yErrAt := 0, 0, 0
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			got := int(ycbcr.Y[j*ycbcr.YStride+i])
			want := int(ref[j*w+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				yErr++
			}
			if d > yMaxErr {
				yMaxErr = d
				yErrAt = j*w + i
			}
		}
	}

	// Compare Cb plane.
	cbOff := ySize
	cbErr, cbMaxErr := 0, 0
	for j := 0; j < ch; j++ {
		for i := 0; i < cw; i++ {
			got := int(ycbcr.Cb[j*ycbcr.CStride+i])
			want := int(ref[cbOff+j*cw+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				cbErr++
			}
			if d > cbMaxErr {
				cbMaxErr = d
			}
		}
	}

	// Compare Cr plane.
	crOff := ySize + cSize
	crErr, crMaxErr := 0, 0
	for j := 0; j < ch; j++ {
		for i := 0; i < cw; i++ {
			got := int(ycbcr.Cr[j*ycbcr.CStride+i])
			want := int(ref[crOff+j*cw+i])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				crErr++
			}
			if d > crMaxErr {
				crMaxErr = d
			}
		}
	}

	t.Logf("Y:  %d/%d wrong pixels, max error %d (at pixel %d)", yErr, ySize, yMaxErr, yErrAt)
	t.Logf("Cb: %d/%d wrong pixels, max error %d", cbErr, cSize, cbMaxErr)
	t.Logf("Cr: %d/%d wrong pixels, max error %d", crErr, cSize, crMaxErr)

	// Per-MB max error for first 5 rows + worst-MB details.
	worstMBx, worstMBy, worstMBmax := 0, 0, 0
	for mby := 0; mby < h/16; mby++ {
		line := ""
		if mby < 5 {
			line = fmt.Sprintf("MB row %d:", mby)
		}
		for mbx := 0; mbx < w/16; mbx++ {
			mbMax := 0
			for j := 0; j < 16; j++ {
				for i := 0; i < 16; i++ {
					py := mby*16 + j
					px := mbx*16 + i
					if py >= h || px >= w {
						continue
					}
					got := int(ycbcr.Y[py*ycbcr.YStride+px])
					want := int(ref[py*w+px])
					d := got - want
					if d < 0 {
						d = -d
					}
					if d > mbMax {
						mbMax = d
					}
				}
			}
			if mby < 5 {
				line += fmt.Sprintf(" %3d", mbMax)
			}
			if mbMax > worstMBmax {
				worstMBmax = mbMax
				worstMBx = mbx
				worstMBy = mby
			}
		}
		if mby < 5 {
			t.Log(line)
		}
	}
	if worstMBmax > 0 {
		t.Logf("Worst MB(%d,%d) max error %d", worstMBx, worstMBy, worstMBmax)
	}

	if yMaxErr > 5 || cbMaxErr > 2 || crMaxErr > 2 {
		t.Errorf("decoded frame differs from ffmpeg reference (Y max=%d, Cb max=%d, Cr max=%d)", yMaxErr, cbMaxErr, crMaxErr)
	}
}

func TestDiagnosticBakerFrame1(t *testing.T) {
	const webmPath = "../examples/videos/baker_vp8.webm"

	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("baker_vp8.webm not found")
	}

	dm := openTestDemuxer(t, webmPath)
	dec := NewDecoder()

	// Decode frame 0 (keyframe) to set up reference.
	pkt0, err := dm.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	dec.Init(bytes.NewReader(pkt0.Data), len(pkt0.Data))
	if _, err := dec.DecodeFrameHeader(); err != nil {
		t.Fatal(err)
	}
	if _, err := dec.DecodeFrame(); err != nil {
		t.Fatal(err)
	}
	t.Logf("Keyframe decoded: %dx%d (%dx%d MBs)", dec.frameHeader.Width, dec.frameHeader.Height, dec.mbw, dec.mbh)

	// Now decode frame 1 manually to trace mode/MV decisions.
	pkt1, err := dm.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	dec.Init(bytes.NewReader(pkt1.Data), len(pkt1.Data))
	fh, err := dec.DecodeFrameHeader()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Frame 1: keyframe=%v version=%d firstPartLen=%d dataLen=%d",
		fh.KeyFrame, fh.VersionNumber, fh.FirstPartitionLen, len(pkt1.Data))

	dec.ensureImg()
	if err := dec.parseOtherHeaders(); err != nil {
		t.Fatal(err)
	}
	t.Logf("useSkipProb=%v skipProb=%d probIntra=%d probLast=%d probGf=%d nOP=%d seg.use=%v seg.update=%v",
		dec.useSkipProb, dec.skipProb, dec.probIntra, dec.probLast, dec.probGf, dec.nOP,
		dec.segmentHeader.useSegment, dec.segmentHeader.updateMap)
	t.Logf("First partition: %d bytes consumed so far (r=%d), buf size=%d",
		dec.fp.r, dec.fp.r, len(dec.fp.buf))

	// Reset state for MB decoding.
	for mbx := 0; mbx < dec.mbw; mbx++ {
		dec.upMB[mbx] = mb{}
		dec.upInterMB[mbx] = interMB{}
	}

	modeNames := []string{"NEAREST", "NEAR", "ZERO", "NEW", "SPLIT"}
	modeCounts := make(map[string]int)
	totalBits := 0
	mbCount := 0
	eofMB := -1

	for mby := 0; mby < dec.mbh; mby++ {
		dec.leftMB = mb{}
		dec.leftInterMB = interMB{}
		var prevAbove interMB
		for mbx := 0; mbx < dec.mbw; mbx++ {
			dec.aboveLeftInterMB = prevAbove
			prevAbove = dec.upInterMB[mbx]
			bitsBefore := dec.fp.r*8 - dec.fp.count

			// Read segment.
			if dec.segmentHeader.updateMap {
				if !dec.fp.readBit(dec.segmentHeader.prob[0]) {
					dec.segment = int(dec.fp.readUint(dec.segmentHeader.prob[1], 1))
				} else {
					dec.segment = int(dec.fp.readUint(dec.segmentHeader.prob[2], 1)) + 2
				}
			}

			skip := false
			if dec.useSkipProb {
				skip = dec.fp.readBit(dec.skipProb)
			}

			eofBefore := dec.fp.unexpectedEOF
			dec.readInterMBModeAndMV(mbx, mby)
			bitsAfter := dec.fp.r*8 - dec.fp.count

			modeName := "INTRA"
			if dec.curRefFrame != 0 {
				if int(dec.curMode) < len(modeNames) {
					modeName = modeNames[dec.curMode]
				}
			}
			modeCounts[modeName]++
			bits := bitsAfter - bitsBefore
			totalBits += bits

			if !eofBefore && dec.fp.unexpectedEOF && eofMB < 0 {
				eofMB = mbCount
				t.Logf("EOF hit at MB %d (%d,%d) skip=%v mode=%s bits=%d",
					mbCount, mbx, mby, skip, modeName, bits)
			}

			// Log first 10 and around EOF.
			if mbCount < 10 {
				t.Logf("MB(%d,%d) skip=%v ref=%d mode=%s mv=(%d,%d) bits=%d",
					mbx, mby, skip, dec.curRefFrame, modeName,
					dec.curMV[0], dec.curMV[1], bits)
			}

			mbCount++
		}
	}

	t.Logf("Total MBs: %d, total bits consumed: %d (%.1f bytes), partition size: %d bytes",
		mbCount, totalBits, float64(totalBits)/8, len(dec.fp.buf))
	t.Logf("Bits per MB avg: %.1f", float64(totalBits)/float64(mbCount))
	t.Logf("Mode distribution: %v", modeCounts)
	if eofMB >= 0 {
		t.Logf("First EOF at MB %d / %d (%.1f%%)", eofMB, mbCount, 100*float64(eofMB)/float64(mbCount))
	} else {
		t.Log("No EOF encountered")
	}
}

func TestDecodeBakerMultiFrame(t *testing.T) {
	const webmPath = "../examples/videos/baker_vp8.webm"
	const refYUV = "testdata/baker_spread_frames.yuv"

	// Spread frame indices to test across the full 71-frame video.
	spreadFrames := []int{0, 1, 5, 10, 20, 30, 40, 50, 60, 70}

	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("baker_vp8.webm not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("baker_spread_frames.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	d := openTestDemuxer(t, webmPath)
	c := NewCodec()

	// Decode first frame to get dimensions.
	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := c.Decode(pkt)
	if err != nil {
		t.Fatalf("frame 0: Decode: %v", err)
	}
	if frame == nil || frame.YCbCr == nil {
		t.Fatal("frame 0: nil frame")
	}

	w := frame.Width
	h := frame.Height
	ySize := w * h
	cw := w / 2
	ch := h / 2
	cSize := cw * ch
	frameSize := ySize + 2*cSize

	if len(ref) < len(spreadFrames)*frameSize {
		t.Fatalf("ref file too small: %d bytes, need %d", len(ref), len(spreadFrames)*frameSize)
	}

	t.Logf("dimensions: %dx%d, frameSize=%d", w, h, frameSize)

	type planeStats struct {
		wrongPixels int
		maxError    int
		totalPixels int
	}
	comparePlane := func(decoded []byte, stride int, refData []byte, pw, ph int) planeStats {
		stats := planeStats{totalPixels: pw * ph}
		for j := 0; j < ph; j++ {
			for i := 0; i < pw; i++ {
				got := int(decoded[j*stride+i])
				want := int(refData[j*pw+i])
				d := got - want
				if d < 0 {
					d = -d
				}
				if d > 0 {
					stats.wrongPixels++
				}
				if d > stats.maxError {
					stats.maxError = d
				}
			}
		}
		return stats
	}

	// Decode all frames, comparing at spread indices.
	totalFrames := spreadFrames[len(spreadFrames)-1] + 1
	refIdx := 0
	decoded := 0

	// Frame 0 is already decoded.
	worstFrame := -1
	worstMaxErr := 0

	for decoded < totalFrames {
		var f *govid.Frame
		if decoded == 0 {
			f = frame
		} else {
			pkt, err = d.NextPacket()
			if err != nil {
				t.Fatalf("frame %d: NextPacket: %v", decoded, err)
			}
			f, err = c.Decode(pkt)
			if err != nil {
				t.Fatalf("frame %d: Decode: %v", decoded, err)
			}
			if f == nil || f.YCbCr == nil {
				t.Fatalf("frame %d: nil frame", decoded)
			}
		}

		if refIdx < len(spreadFrames) && decoded == spreadFrames[refIdx] {
			refOff := refIdx * frameSize
			refY := ref[refOff : refOff+ySize]
			refCb := ref[refOff+ySize : refOff+ySize+cSize]
			refCr := ref[refOff+ySize+cSize : refOff+ySize+2*cSize]

			ycbcr := f.YCbCr
			yStats := comparePlane(ycbcr.Y, ycbcr.YStride, refY, w, h)
			cbStats := comparePlane(ycbcr.Cb, ycbcr.CStride, refCb, cw, ch)
			crStats := comparePlane(ycbcr.Cr, ycbcr.CStride, refCr, cw, ch)

			frameType := "inter"
			if decoded == 0 {
				frameType = "key"
			}

			yPct := 100 * float64(yStats.wrongPixels) / float64(yStats.totalPixels)
			t.Logf("frame %d (%s): Y %d/%d wrong (%.1f%%) max=%d | Cb %d/%d max=%d | Cr %d/%d max=%d",
				decoded, frameType,
				yStats.wrongPixels, yStats.totalPixels, yPct, yStats.maxError,
				cbStats.wrongPixels, cbStats.totalPixels, cbStats.maxError,
				crStats.wrongPixels, crStats.totalPixels, crStats.maxError)

			maxErr := yStats.maxError
			if cbStats.maxError > maxErr {
				maxErr = cbStats.maxError
			}
			if crStats.maxError > maxErr {
				maxErr = crStats.maxError
			}
			if maxErr > worstMaxErr {
				worstMaxErr = maxErr
				worstFrame = decoded
			}

			if decoded == 0 && (yStats.maxError > 2 || cbStats.maxError > 2 || crStats.maxError > 2) {
				t.Errorf("keyframe (frame 0) exceeds threshold (Y max=%d, Cb max=%d, Cr max=%d)",
					yStats.maxError, cbStats.maxError, crStats.maxError)
			}

			refIdx++
		}

		decoded++
	}

	t.Logf("Overall worst: frame %d with max error %d", worstFrame, worstMaxErr)
}

// TestBakerFirstDivergence finds the first inexact frame in 0..10 and its
// first divergent macroblock, against an ffmpeg reference (local-only dump).
func TestBakerFirstDivergence(t *testing.T) {
	const webmPath = "../examples/videos/baker_vp8.webm"
	const refYUV = "testdata/_baker_f0_10.yuv"
	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("baker_vp8.webm not found")
	}
	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Skip("reference not found")
	}

	d := openTestDemuxer(t, webmPath)
	c := NewCodec()
	const w, h = 1280, 720
	frameSize := w*h + 2*(w/2)*(h/2)

	for i := 0; i <= 10; i++ {
		pkt, err := d.NextPacket()
		if err != nil {
			t.Fatal(err)
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		refY := ref[i*frameSize : i*frameSize+w*h]
		ycbcr := frame.YCbCr
		bad := 0
		firstMB := -1
		for mby := 0; mby < h/16; mby++ {
			for mbx := 0; mbx < w/16; mbx++ {
				mx := 0
				for j := mby * 16; j < mby*16+16; j++ {
					for x := mbx * 16; x < mbx*16+16; x++ {
						diff := int(ycbcr.Y[j*ycbcr.YStride+x]) - int(refY[j*w+x])
						if diff < 0 {
							diff = -diff
						}
						if diff > mx {
							mx = diff
						}
					}
				}
				if mx > 0 {
					bad++
					if firstMB < 0 {
						firstMB = mby*(w/16) + mbx
					}
					if bad <= 12 {
						t.Logf("frame %d: bad MB (%d,%d) maxErr=%d", i, mbx, mby, mx)
					}
				}
			}
		}
		if bad > 0 {
			t.Logf("frame %d: %d bad MBs", i, bad)
			return
		}
		t.Logf("frame %d: exact", i)
	}
}

// TestBakerFirstDivergenceNoFilter is the staged variant: loop filter off on
// both sides, isolating reconstruction from filtering.
func TestBakerFirstDivergenceNoFilter(t *testing.T) {
	const webmPath = "../examples/videos/baker_vp8.webm"
	const refYUV = "testdata/_baker_f0_10_nodb.yuv"
	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("baker_vp8.webm not found")
	}
	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Skip("reference not found")
	}

	d := openTestDemuxer(t, webmPath)
	c := NewCodec()
	c.dec.disableFilter = true
	const w, h = 1280, 720
	frameSize := w*h + 2*(w/2)*(h/2)

	for i := 0; i <= 10; i++ {
		pkt, err := d.NextPacket()
		if err != nil {
			t.Fatal(err)
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		refY := ref[i*frameSize : i*frameSize+w*h]
		ycbcr := frame.YCbCr
		bad := 0
		for mby := 0; mby < h/16; mby++ {
			for mbx := 0; mbx < w/16; mbx++ {
				mx := 0
				for j := mby * 16; j < mby*16+16; j++ {
					for x := mbx * 16; x < mbx*16+16; x++ {
						diff := int(ycbcr.Y[j*ycbcr.YStride+x]) - int(refY[j*w+x])
						if diff < 0 {
							diff = -diff
						}
						if diff > mx {
							mx = diff
						}
					}
				}
				if mx > 0 {
					bad++
					if bad <= 6 {
						t.Logf("frame %d: bad MB (%d,%d) maxErr=%d", i, mbx, mby, mx)
					}
				}
			}
		}
		if bad > 0 {
			t.Logf("frame %d: %d bad MBs (no filter)", i, bad)
			return
		}
		t.Logf("frame %d: exact (no filter)", i)
	}
	t.Log("frames 0-10 exact without loop filter")
}

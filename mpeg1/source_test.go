package mpeg1

import (
	"fmt"
	"io"
	"os"
	"testing"

	govid "github.com/liqmix/govid"
)

func openTestSourceFromPath(t *testing.T, path string) *Source {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	s, err := NewSource(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func openTestSource(t *testing.T) *Source {
	return openTestSourceFromPath(t, "testdata/test.mpg")
}

func TestNewSource(t *testing.T) {
	s := openTestSource(t)
	if s == nil {
		t.Fatal("expected non-nil source")
	}
}

func TestVideoInfo(t *testing.T) {
	s := openTestSource(t)
	vi := s.VideoInfo()
	if vi.Width != 160 || vi.Height != 120 {
		t.Errorf("expected 160x120, got %dx%d", vi.Width, vi.Height)
	}
	if vi.FrameRate <= 0 {
		t.Errorf("expected positive frame rate, got %f", vi.FrameRate)
	}
}

func TestDuration(t *testing.T) {
	s := openTestSource(t)
	dur := s.Duration()
	if dur <= 0 {
		t.Errorf("expected positive duration, got %v", dur)
	}
}

func TestDecodeAllFrames(t *testing.T) {
	s := openTestSource(t)
	count := 0
	for {
		pkt, err := s.NextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		frame, err := s.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: %v", count, err)
		}
		if frame == nil {
			t.Fatalf("frame %d: nil frame", count)
		}
		if frame.Width != 160 || frame.Height != 120 {
			t.Errorf("frame %d: expected 160x120, got %dx%d", count, frame.Width, frame.Height)
		}
		count++
	}
	if count < 3 {
		t.Errorf("expected at least 3 frames, got %d", count)
	}
	t.Logf("decoded %d frames", count)
}

func TestFrameRGBA(t *testing.T) {
	s := openTestSource(t)
	pkt, err := s.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := s.Decode(pkt)
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
	s := openTestSource(t)
	p, err := govid.NewPlayer(s, s)
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

func TestDecodeVsReference(t *testing.T) {
	const mpgPath = "testdata/test.mpg"
	const refYUV = "testdata/test_frame0.yuv"

	if _, err := os.Stat(mpgPath); err != nil {
		t.Skip("test.mpg not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("test_frame0.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	s := openTestSourceFromPath(t, mpgPath)

	pkt, err := s.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := s.Decode(pkt)
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

	if yMaxErr > 3 || cbMaxErr > 3 || crMaxErr > 3 {
		t.Errorf("decoded frame differs from ffmpeg reference (Y max=%d, Cb max=%d, Cr max=%d)", yMaxErr, cbMaxErr, crMaxErr)
	}
}

func TestDecodeBakerVsReference(t *testing.T) {
	const mpgPath = "../examples/videos/baker.mpg"
	const refYUV = "testdata/baker_frame0.yuv"

	if _, err := os.Stat(mpgPath); err != nil {
		t.Skip("baker.mpg not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("baker_frame0.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	s := openTestSourceFromPath(t, mpgPath)

	pkt, err := s.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := s.Decode(pkt)
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

func TestDecodeBakerMultiFrame(t *testing.T) {
	const mpgPath = "../examples/videos/baker.mpg"
	const refYUV = "testdata/baker_frames_0_9.yuv"
	const numFrames = 10

	if _, err := os.Stat(mpgPath); err != nil {
		t.Skip("baker.mpg not found")
	}
	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("baker_frames_0_9.yuv not found")
	}

	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	s := openTestSourceFromPath(t, mpgPath)

	// Decode first frame to get dimensions.
	pkt, err := s.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := s.Decode(pkt)
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

	if len(ref) < numFrames*frameSize {
		t.Fatalf("ref file too small: %d bytes, need %d", len(ref), numFrames*frameSize)
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

	// Process frame 0 (already decoded).
	frames := make([]*govid.Frame, 0, numFrames)
	frames = append(frames, frame)

	// Decode frames 1-9.
	for i := 1; i < numFrames; i++ {
		pkt, err := s.NextPacket()
		if err != nil {
			t.Fatalf("frame %d: NextPacket: %v", i, err)
		}
		f, err := s.Decode(pkt)
		if err != nil {
			t.Fatalf("frame %d: Decode: %v", i, err)
		}
		if f == nil || f.YCbCr == nil {
			t.Fatalf("frame %d: nil frame", i)
		}
		frames = append(frames, f)
	}

	// Compare each frame against reference.
	worstFrame := 0
	worstMaxErr := 0
	for i, f := range frames {
		refOff := i * frameSize
		refY := ref[refOff : refOff+ySize]
		refCb := ref[refOff+ySize : refOff+ySize+cSize]
		refCr := ref[refOff+ySize+cSize : refOff+ySize+2*cSize]

		ycbcr := f.YCbCr
		yStats := comparePlane(ycbcr.Y, ycbcr.YStride, refY, w, h)
		cbStats := comparePlane(ycbcr.Cb, ycbcr.CStride, refCb, cw, ch)
		crStats := comparePlane(ycbcr.Cr, ycbcr.CStride, refCr, cw, ch)

		frameType := "P"
		if i == 0 {
			frameType = "I"
		}

		t.Logf("frame %d (%s): Y %d/%d wrong (%.1f%%) max=%d | Cb %d/%d max=%d | Cr %d/%d max=%d",
			i, frameType,
			yStats.wrongPixels, yStats.totalPixels, 100*float64(yStats.wrongPixels)/float64(yStats.totalPixels), yStats.maxError,
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
			worstFrame = i
		}
	}

	// Dump per-MB max Y error for worst frame (first 3 rows).
	if worstFrame >= 0 && worstFrame < len(frames) {
		f := frames[worstFrame]
		refOff := worstFrame * frameSize
		refY := ref[refOff : refOff+ySize]
		ycbcr := f.YCbCr

		t.Logf("--- Worst frame %d: per-MB max Y error (first 3 rows) ---", worstFrame)
		for mby := 0; mby < 3 && mby < h/16; mby++ {
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
						want := int(refY[py*w+px])
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
	}

	t.Logf("Overall worst: frame %d with max error %d", worstFrame, worstMaxErr)

	if worstMaxErr > 10 {
		t.Errorf("multi-frame max error %d exceeds threshold 10 (worst frame %d)", worstMaxErr, worstFrame)
	}
}

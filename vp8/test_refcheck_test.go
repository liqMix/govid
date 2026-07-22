package vp8

import (
	"bytes"
	"os"
	"testing"
)

func TestReferenceFrameCorrect(t *testing.T) {
	const webmPath = "testdata/interframe64.webm"
	const refYUV = "testdata/interframe64_frames.yuv"
	const w, h = 64, 64

	if _, err := os.Stat(refYUV); err != nil {
		t.Skip("ref not found")
	}
	ref, err := os.ReadFile(refYUV)
	if err != nil {
		t.Fatal(err)
	}

	dm := openTestDemuxer(t, webmPath)
	dec := NewDecoder()

	// Decode keyframe
	pkt0, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt0.Data), len(pkt0.Data))
	dec.DecodeFrameHeader()
	dec.ensureImg()
	dec.DecodeFrame()

	// Check that refFrame[1] (LAST) matches ffmpeg's frame 0 output.
	lastRef := dec.refFrame[1]
	if lastRef == nil {
		t.Fatal("refFrame[1] is nil")
	}

	ySize := w * h
	wrongY, maxY := 0, 0
	for j := 0; j < h; j++ {
		for k := 0; k < w; k++ {
			got := int(lastRef.Y[j*lastRef.YStride+k])
			want := int(ref[j*w+k])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				wrongY++
			}
			if d > maxY {
				maxY = d
			}
		}
	}
	t.Logf("refFrame[1] vs ffmpeg frame 0: Y %d/%d wrong, max=%d", wrongY, ySize, maxY)

	// Also check the output image matches.
	wrongImg, maxImg := 0, 0
	for j := 0; j < h; j++ {
		for k := 0; k < w; k++ {
			got := int(dec.img.Y[j*dec.img.YStride+k])
			want := int(ref[j*w+k])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > 0 {
				wrongImg++
			}
			if d > maxImg {
				maxImg = d
			}
		}
	}
	t.Logf("d.img vs ffmpeg frame 0: Y %d/%d wrong, max=%d", wrongImg, ySize, maxImg)

	// Check refFrame[1] pixels vs d.img pixels (should be identical deep copy)
	wrongCopy := 0
	for j := 0; j < h; j++ {
		for k := 0; k < w; k++ {
			a := lastRef.Y[j*lastRef.YStride+k]
			b := dec.img.Y[j*dec.img.YStride+k]
			if a != b {
				wrongCopy++
			}
		}
	}
	t.Logf("refFrame[1] vs d.img: %d different pixels", wrongCopy)

	// Now decode frame 1 and check the FIRST pixel of an error MB.
	pkt1, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt1.Data), len(pkt1.Data))
	dec.DecodeFrameHeader()
	dec.ensureImg()
	dec.DecodeFrame()

	// Check MB(0,3) pixel values: reference frame (frame 0), decoded frame 1, expected frame 1.
	cSize := (w / 2) * (h / 2)
	frameSize := ySize + 2*cSize
	refF0 := ref[0:ySize]                     // frame 0 (keyframe) reference
	refF1 := ref[frameSize : frameSize+ySize] // frame 1 expected

	t.Log("MB(0,3) first row: refF0=reference_pixel, got=decoded, want=expected")
	for x := 0; x < 16; x++ {
		f0pix := refF0[48*w+x]
		got := dec.img.Y[48*dec.img.YStride+x]
		want := refF1[48*w+x]
		subIdx := x / 4
		t.Logf("  pixel(%d,48) sub=%d: refF0=%d got=%d want=%d | mc_err=%d res_should=%d res_actual=%d",
			x, subIdx, f0pix, got, want,
			int(got)-int(f0pix),                        // how much decoded differs from pure MC prediction (MV=0)
			int(want)-int(f0pix),                       // residual the encoder intended
			int(got)-int(f0pix)-(int(want)-int(f0pix))) // difference between actual and intended residual
	}

	// Also show all 4 rows of sub-block 2 (cols 8-11, rows 48-51) where MV=0 but errors exist.
	t.Log("Sub-block 2 (MV=0,0) full 4x4:")
	for dy := 0; dy < 4; dy++ {
		row := 48 + dy
		for dx := 0; dx < 4; dx++ {
			col := 8 + dx
			f0 := int(refF0[row*w+col])
			got := int(dec.img.Y[row*dec.img.YStride+col])
			want := int(refF1[row*w+col])
			t.Logf("  (%d,%d): refF0=%d got=%d want=%d | applied_res=%d expected_res=%d",
				col, row, f0, got, want, got-f0, want-f0)
		}
	}

	// Show decoded coefficients for MB(0,3) blocks 0-3 (first row of sub-blocks).
	// Re-decode frame 1 to capture coefficients.
	dm2 := openTestDemuxer(t, webmPath)
	dec2 := NewDecoder()
	p0, _ := dm2.NextPacket()
	dec2.Init(bytes.NewReader(p0.Data), len(p0.Data))
	dec2.DecodeFrameHeader()
	dec2.ensureImg()
	dec2.DecodeFrame()

	p1, _ := dm2.NextPacket()
	dec2.Init(bytes.NewReader(p1.Data), len(p1.Data))
	dec2.DecodeFrameHeader()
	dec2.ensureImg()
	dec2.parseOtherHeaders()

	t.Logf("nOP=%d, quant[0].y1=%v y2=%v pktLen=%d", dec2.nOP, dec2.quant[0].y1, dec2.quant[0].y2, len(p1.Data))
	// Print first few bytes of token partition vs raw packet data.
	t.Logf("token partition first 8 bytes: %x", dec2.op[0].buf[:8])
	offset := 3 + int(dec2.frameHeader.FirstPartitionLen) // frame_tag + first_partition
	t.Logf("raw packet[%d:%d]: %x", offset, offset+8, p1.Data[offset:offset+8])
	t.Logf("token partition len=%d, expected=%d", len(dec2.op[0].buf), len(p1.Data)-offset)

	// Verify MC for sub-block 0 (MV=2,-2) by manually running sixtap on reference pixels.
	mcRef := dec2.refFrame[1]
	if mcRef != nil {
		// Sub-block 0 of MB(0,3): MV=(2,-2) eighth-pixel, position (48,0)
		mvRow, mvCol := int16(2), int16(-2)
		srcX := 0*16 + 0*4 + int(mvCol>>3) // = -1
		srcY := 3*16 + 0*4 + int(mvRow>>3) // = 48
		xFrac := int(mvCol & 7)            // = 6
		yFrac := int(mvRow & 7)            // = 2
		t.Logf("MC sub-block 0: srcX=%d srcY=%d xFrac=%d yFrac=%d", srcX, srcY, xFrac, yFrac)

		var mcOut [4 * 4]byte
		sixtapFilter(mcRef.Y, mcRef.YStride, srcX, srcY, xFrac, yFrac, mcOut[:], 4, 4, 4, mcRef.YStride, len(mcRef.Y)/mcRef.YStride)
		t.Log("MC prediction for sub-block 0 first row:")
		refF0 := ref[0:ySize]
		refF1 := ref[frameSize : frameSize+ySize]
		for dx := 0; dx < 4; dx++ {
			mc := int(mcOut[dx])
			want := int(refF1[48*w+dx])
			f0 := int(refF0[48*w+dx])
			t.Logf("  pixel(%d,48): MC=%d want=%d refF0=%d | needed_res=%d", dx, mc, want, f0, want-mc)
		}
	}

	// Manually decode MBs up to (0,3) capturing token partition state.
	for mbx := 0; mbx < dec2.mbw; mbx++ {
		dec2.upMB[mbx] = mb{}
		dec2.upInterMB[mbx] = interMB{}
	}
	mbIdx := 0
	for mby := 0; mby < dec2.mbh; mby++ {
		dec2.leftMB = mb{}
		dec2.leftInterMB = interMB{}
		var prevAbove interMB
		for mbx := 0; mbx < dec2.mbw; mbx++ {
			dec2.aboveLeftInterMB = prevAbove
			prevAbove = dec2.upInterMB[mbx]

			// Capture token partition state BEFORE this MB.
			tp := &dec2.op[mby&(dec2.nOP-1)]
			tpBefore := tp.r*8 - tp.count

			// Capture full token partition state BEFORE MB(0,3).
			if mby == 3 && mbx == 0 {
				t.Logf("  BEFORE MB(0,3) tok: r=%d count=%d rng=%d value=%08x",
					tp.r, tp.count, tp.rng, tp.value)
				t.Logf("  BEFORE nz: leftMB.nzMask=%08b nzY16=%d upMB[0].nzMask=%08b nzY16=%d",
					dec2.leftMB.nzMask, dec2.leftMB.nzY16, dec2.upMB[0].nzMask, dec2.upMB[0].nzY16)
				// Print tokenProb for planeY1SansY2 band 0 all contexts.
				for ctx := 0; ctx < 3; ctx++ {
					t.Logf("  tokenProb[Y1SansY2][band0][ctx%d] = %v",
						ctx, dec2.tokenProb[planeY1SansY2][0][ctx])
				}
				// Also show planeY1WithY2 for comparison.
				for ctx := 0; ctx < 3; ctx++ {
					t.Logf("  tokenProb[Y1WithY2][band0][ctx%d] = %v",
						ctx, dec2.tokenProb[planeY1WithY2][0][ctx])
				}
			}

			skip := dec2.reconstruct(mbx, mby)

			tpAfter := tp.r*8 - tp.count

			// Print token partition info for ALL MBs.
			if true {
				modeName := "?"
				modeNames := []string{"NEAREST", "NEAR", "ZERO", "NEW", "SPLIT"}
				if dec2.curRefFrame == 0 {
					modeName = "INTRA"
				} else if int(dec2.curMode) < len(modeNames) {
					modeName = modeNames[dec2.curMode]
				}
				t.Logf("MB(%d,%d) %s skip=%v tokBits=%d (pos %d→%d) usePredY16=%v",
					mbx, mby, modeName, skip, tpAfter-tpBefore, tpBefore, tpAfter, dec2.usePredY16)
				if dec2.usePredY16 {
					var y2c [16]int16
					copy(y2c[:], dec2.coeff[384:400])
					t.Logf("  Y2 coeff: %v", y2c)
				}
				var dcs [16]int16
				for blk := 0; blk < 16; blk++ {
					dcs[blk] = dec2.coeff[blk*16]
				}
				t.Logf("  Y1 DCs: %v", dcs)
			}
			if mby == 3 && mbx == 0 {
				t.Logf("MB(0,3) token bits consumed=%d (before=%d after=%d) skip=%v mode=%d usePredY16=%v",
					tpAfter-tpBefore, tpBefore, tpAfter, skip, dec2.curMode, dec2.usePredY16)
				// Show token partition state at start of MB(0,3).
				t.Logf("  tok state: r=%d count=%d rng=%d value=%08x bufLen=%d",
					tp.r, tp.count, tp.rng, tp.value, len(tp.buf))
				// Show nz context for this MB.
				t.Logf("  nz context: leftMB.nzMask=%08b nzY16=%d upMB[0].nzMask=%08b nzY16=%d",
					dec2.leftMB.nzMask, dec2.leftMB.nzY16, dec2.upMB[0].nzMask, dec2.upMB[0].nzY16)
				// Show the token bytes at the current read position (r bytes into the buffer).
				startByte := tp.r - 4
				if startByte < 0 {
					startByte = 0
				}
				endByte := tp.r + 8
				if endByte > len(tp.buf) {
					endByte = len(tp.buf)
				}
				t.Logf("  tok bytes[%d:%d]: %x", startByte, endByte, tp.buf[startByte:endByte])
				// Print ALL Y1 block DCs.
				var dcs [16]int16
				for blk := 0; blk < 16; blk++ {
					dcs[blk] = dec2.coeff[blk*16]
				}
				t.Logf("  Y1 DCs: %v", dcs)
				// Count non-zero coefficients per block.
				for blk := 0; blk < 16; blk++ {
					nz := 0
					for c := 0; c < 16; c++ {
						if dec2.coeff[blk*16+c] != 0 {
							nz++
						}
					}
					if nz > 0 {
						t.Logf("  Y1 block %d: %d non-zero coeffs, DC=%d", blk, nz, dec2.coeff[blk*16])
					}
				}
			}

			fs := dec2.lookupFilterParam()
			fs.inner = fs.inner || !skip
			dec2.perMBFilterParams[dec2.mbw*mby+mbx] = fs
			mbIdx++
		}
	}
}

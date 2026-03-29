package vp8

import "image"

// Motion compensation per RFC 6386 §18.

// motionCompensateMB performs motion compensation for the current macroblock,
// writing the predicted block into d.ybr workspace.
func (d *Decoder) motionCompensateMB(mbx, mby int) {
	ref := d.refFrame[d.curRefFrame]
	if ref == nil {
		return
	}

	if d.curMode == interModeSPLITMV {
		d.motionCompensateSplitFull(ref, mbx, mby)
		return
	}

	mv := d.curMV
	// Luma: 16x16 block.
	d.motionCompensateLumaBlock(ref.Y, ref.YStride, mbx, mby, mv[0], mv[1], 16)
	// Chroma: 8x8 blocks.
	d.motionCompensateChromaBlock(ref.Cb, ref.Cr, ref.CStride, mbx, mby, mv[0], mv[1])
}

// motionCompensateLumaBlock performs luma motion compensation for a block of given size.
func (d *Decoder) motionCompensateLumaBlock(refY []byte, refStride, mbx, mby int, mvRow, mvCol int16, blockSize int) {
	// MV is in eighth-pixel units (component * 2 from readMVComponent).
	srcX := mbx*16 + int(mvCol>>3)
	srcY := mby*16 + int(mvRow>>3)
	xFrac := int(mvCol & 7)
	yFrac := int(mvRow & 7)

	lumaW := refStride
	lumaH := len(refY) / refStride

	tmp := make([]byte, blockSize*blockSize)
	if d.frameHeader.VersionNumber >= 1 {
		bilinearFilter(refY, refStride, srcX, srcY, xFrac, yFrac, tmp, blockSize, blockSize, blockSize, lumaW, lumaH)
	} else {
		sixtapFilter(refY, refStride, srcX, srcY, xFrac, yFrac, tmp, blockSize, blockSize, blockSize, lumaW, lumaH)
	}

	for y := 0; y < blockSize; y++ {
		for x := 0; x < blockSize; x++ {
			d.ybr[ybrYY+y][ybrYX+x] = tmp[y*blockSize+x]
		}
	}
}

// motionCompensateChromaBlock performs chroma motion compensation for 8x8 blocks.
func (d *Decoder) motionCompensateChromaBlock(refCb, refCr []byte, refCStride, mbx, mby int, mvRow, mvCol int16) {
	chromaRow, chromaCol, yFrac, xFrac := chromaMV(mvRow, mvCol)

	cbSrcX := mbx*8 + int(chromaCol)
	cbSrcY := mby*8 + int(chromaRow)
	chromaW := refCStride
	chromaH := len(refCb) / refCStride

	var tmpCb, tmpCr [8 * 8]byte
	if d.frameHeader.VersionNumber >= 1 {
		bilinearFilter(refCb, refCStride, cbSrcX, cbSrcY, xFrac, yFrac, tmpCb[:], 8, 8, 8, chromaW, chromaH)
		bilinearFilter(refCr, refCStride, cbSrcX, cbSrcY, xFrac, yFrac, tmpCr[:], 8, 8, 8, chromaW, chromaH)
	} else {
		sixtapFilter(refCb, refCStride, cbSrcX, cbSrcY, xFrac, yFrac, tmpCb[:], 8, 8, 8, chromaW, chromaH)
		sixtapFilter(refCr, refCStride, cbSrcX, cbSrcY, xFrac, yFrac, tmpCr[:], 8, 8, 8, chromaW, chromaH)
	}

	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			d.ybr[ybrBY+y][ybrBX+x] = tmpCb[y*8+x]
			d.ybr[ybrRY+y][ybrRX+x] = tmpCr[y*8+x]
		}
	}
}

// motionCompensateSplitFull handles SPLITMV with per-4x4-block luma and averaged chroma MVs.
func (d *Decoder) motionCompensateSplitFull(ref *image.YCbCr, mbx, mby int) {
	lumaW := ref.YStride
	lumaH := len(ref.Y) / ref.YStride

	// Luma: per-4x4 sub-block.
	for subIdx := 0; subIdx < 16; subIdx++ {
		subRow := subIdx / 4
		subCol := subIdx % 4
		mv := d.curSubMV[subIdx]

		srcX := mbx*16 + subCol*4 + int(mv[1]>>3)
		srcY := mby*16 + subRow*4 + int(mv[0]>>3)
		xFrac := int(mv[1] & 7)
		yFrac := int(mv[0] & 7)

		var tmp [4 * 4]byte
		if d.frameHeader.VersionNumber >= 1 {
			bilinearFilter(ref.Y, ref.YStride, srcX, srcY, xFrac, yFrac, tmp[:], 4, 4, 4, lumaW, lumaH)
		} else {
			sixtapFilter(ref.Y, ref.YStride, srcX, srcY, xFrac, yFrac, tmp[:], 4, 4, 4, lumaW, lumaH)
		}

		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				d.ybr[ybrYY+subRow*4+y][ybrYX+subCol*4+x] = tmp[y*4+x]
			}
		}
	}

	// Chroma: average 2x2 groups of luma MVs.
	d.motionCompensateChromaSplit(ref, mbx, mby)
}

// motionCompensateChromaSplit computes chroma for SPLITMV by averaging 2x2 luma MV groups.
// Uses libvpx's single-step formula: chromaMV = (sum + 4 + sign_bias) / 8.
func (d *Decoder) motionCompensateChromaSplit(ref *image.YCbCr, mbx, mby int) {
	chromaW := ref.CStride
	chromaH := len(ref.Cb) / ref.CStride

	for chromaRow := 0; chromaRow < 2; chromaRow++ {
		for chromaCol := 0; chromaCol < 2; chromaCol++ {
			var sumRow, sumCol int32
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					subIdx := (chromaRow*2+dy)*4 + chromaCol*2 + dx
					sumRow += int32(d.curSubMV[subIdx][0])
					sumCol += int32(d.curSubMV[subIdx][1])
				}
			}
			// libvpx build_4x4uvmvs: temp += 4 + ((temp >> 31) * 8); result = temp / 8;
			mvRow := splitChromaMV(sumRow)
			mvCol := splitChromaMV(sumCol)

			srcX := mbx*8 + chromaCol*4 + int(mvCol>>3)
			srcY := mby*8 + chromaRow*4 + int(mvRow>>3)
			xFrac := int(mvCol & 7)
			yFrac := int(mvRow & 7)

			var tmpCb, tmpCr [4 * 4]byte
			if d.frameHeader.VersionNumber >= 1 {
				bilinearFilter(ref.Cb, ref.CStride, srcX, srcY, xFrac, yFrac, tmpCb[:], 4, 4, 4, chromaW, chromaH)
				bilinearFilter(ref.Cr, ref.CStride, srcX, srcY, xFrac, yFrac, tmpCr[:], 4, 4, 4, chromaW, chromaH)
			} else {
				sixtapFilter(ref.Cb, ref.CStride, srcX, srcY, xFrac, yFrac, tmpCb[:], 4, 4, 4, chromaW, chromaH)
				sixtapFilter(ref.Cr, ref.CStride, srcX, srcY, xFrac, yFrac, tmpCr[:], 4, 4, 4, chromaW, chromaH)
			}

			for y := 0; y < 4; y++ {
				for x := 0; x < 4; x++ {
					d.ybr[ybrBY+chromaRow*4+y][ybrBX+chromaCol*4+x] = tmpCb[y*4+x]
					d.ybr[ybrRY+chromaRow*4+y][ybrRX+chromaCol*4+x] = tmpCr[y*4+x]
				}
			}
		}
	}
}

// chromaMV converts luma MV (eighth-pixel) to chroma coordinates.
func chromaMV(lumaRow, lumaCol int16) (row, col int16, yFrac, xFrac int) {
	row, yFrac = chromaMVComp(lumaRow)
	col, xFrac = chromaMVComp(lumaCol)
	return
}

func chromaMVComp(v int16) (intPart int16, frac int) {
	// Convert luma eighth-pixel → chroma eighth-pixel: divide by 2 with
	// round-toward-zero, matching libvpx: v += 1|(v>>15); v /= 2
	if v >= 0 {
		v = (v + 1) / 2
	} else {
		v = (v - 1) / 2
	}
	// Split chroma eighth-pixel into integer pixel + fractional eighth.
	if v >= 0 {
		intPart = v >> 3
		frac = int(v & 7)
	} else {
		neg := -v
		intPart = -(neg >> 3)
		frac = int(neg & 7)
		if frac != 0 {
			intPart--
			frac = 8 - frac
		}
	}
	return
}

// splitChromaMV computes the chroma MV for a SPLIT sub-block group from the
// sum of 4 luma eighth-pixel MVs. Matches libvpx build_4x4uvmvs:
//
//	temp += 4 + ((temp >> 31) * 8); result = temp / 8;
//
// Returns chroma MV in eighth-pixel units (for >> 3 / & 7 splitting).
func splitChromaMV(sum int32) int16 {
	if sum >= 0 {
		return int16((sum + 4) >> 3)
	}
	return int16(-(((-sum) + 4) >> 3))
}

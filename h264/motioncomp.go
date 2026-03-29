package h264

import "image"

// Motion compensation per H.264 spec 8.4.2.
// Supports quarter-pixel luma and eighth-pixel chroma interpolation.

// motionCompLuma performs luma motion compensation for a block of size blockW x blockH.
// mv is in quarter-pixel units. Output is written to d.ybr at (dstY, dstX).
func (d *Decoder) motionCompLuma(ref *image.YCbCr, mbx, mby int, mv [2]int16, dstY, dstX, blockW, blockH int) {
	// Full-pixel position of MB's top-left corner.
	baseX := mbx*16 + (dstX - ybrYX)
	baseY := mby*16 + (dstY - ybrYY)

	// Quarter-pixel MV components.
	qpx := int(mv[0])
	qpy := int(mv[1])

	// Full-pixel offset.
	fpX := baseX + (qpx >> 2)
	fpY := baseY + (qpy >> 2)

	// Fractional parts (0-3).
	fracX := qpx & 3
	fracY := qpy & 3

	w := ref.Rect.Dx()
	h := ref.Rect.Dy()

	for j := 0; j < blockH; j++ {
		for i := 0; i < blockW; i++ {
			d.ybr[dstY+j][dstX+i] = interpolateLuma(ref.Y, ref.YStride, w, h, fpX+i, fpY+j, fracX, fracY)
		}
	}
}

// motionCompChroma performs chroma motion compensation for an 8x8 chroma block pair.
// Chroma MVs are luma MVs divided by 2, giving 1/8-pixel precision.
func (d *Decoder) motionCompChroma(ref *image.YCbCr, mbx, mby int, mv [2]int16) {
	// Chroma MV: luma MV / 2 (keeping quarter-pel, so chroma is eighth-pel).
	cmvx := int(mv[0])
	cmvy := int(mv[1])

	// Full-pixel position in chroma plane.
	baseX := mbx * 8
	baseY := mby * 8

	fpX := baseX + (cmvx >> 3)
	fpY := baseY + (cmvy >> 3)

	// Fractional in 1/8ths (0-7).
	// Go's arithmetic right shift and bitwise AND already produce
	// correct floor division and non-negative remainder for negative values.
	fracX := cmvx & 7
	fracY := cmvy & 7

	cw := ref.Rect.Dx() / 2
	ch := ref.Rect.Dy() / 2

	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			d.ybr[ybrBY+j][ybrBX+i] = bilinearChroma(ref.Cb, ref.CStride, cw, ch, fpX+i, fpY+j, fracX, fracY)
			d.ybr[ybrRY+j][ybrRX+i] = bilinearChroma(ref.Cr, ref.CStride, cw, ch, fpX+i, fpY+j, fracX, fracY)
		}
	}
}

// motionCompChromaBlock performs chroma motion compensation for a sub-block.
func (d *Decoder) motionCompChromaBlock(ref *image.YCbCr, mbx, mby int, mv [2]int16, offX, offY, blockW, blockH int) {
	cmvx := int(mv[0])
	cmvy := int(mv[1])

	baseX := mbx*8 + offX
	baseY := mby*8 + offY

	fpX := baseX + (cmvx >> 3)
	fpY := baseY + (cmvy >> 3)

	fracX := cmvx & 7
	fracY := cmvy & 7

	cw := ref.Rect.Dx() / 2
	ch := ref.Rect.Dy() / 2

	for j := 0; j < blockH; j++ {
		for i := 0; i < blockW; i++ {
			d.ybr[ybrBY+offY+j][ybrBX+offX+i] = bilinearChroma(ref.Cb, ref.CStride, cw, ch, fpX+i, fpY+j, fracX, fracY)
			d.ybr[ybrRY+offY+j][ybrRX+offX+i] = bilinearChroma(ref.Cr, ref.CStride, cw, ch, fpX+i, fpY+j, fracX, fracY)
		}
	}
}

func clampPix(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func getPixY(pix []byte, stride, w, h, x, y int) int {
	x = clampPix(x, 0, w-1)
	y = clampPix(y, 0, h-1)
	return int(pix[y*stride+x])
}

// interpolateLuma implements H.264 6-tap FIR for half-pel and averaging for quarter-pel.
func interpolateLuma(pix []byte, stride, w, h, x, y, fracX, fracY int) uint8 {
	if fracX == 0 && fracY == 0 {
		return uint8(getPixY(pix, stride, w, h, x, y))
	}

	if fracY == 0 {
		// Horizontal only.
		a := halfPelH(pix, stride, w, h, x, y)
		if fracX == 2 {
			return a
		}
		if fracX == 1 {
			return uint8((int(a) + getPixY(pix, stride, w, h, x, y) + 1) >> 1)
		}
		// fracX == 3
		return uint8((int(a) + getPixY(pix, stride, w, h, x+1, y) + 1) >> 1)
	}

	if fracX == 0 {
		// Vertical only.
		a := halfPelV(pix, stride, w, h, x, y)
		if fracY == 2 {
			return a
		}
		if fracY == 1 {
			return uint8((int(a) + getPixY(pix, stride, w, h, x, y) + 1) >> 1)
		}
		// fracY == 3
		return uint8((int(a) + getPixY(pix, stride, w, h, x, y+1) + 1) >> 1)
	}

	if fracX == 2 && fracY == 2 {
		return halfPelHV(pix, stride, w, h, x, y)
	}

	if fracX == 2 {
		// (2,1)=f or (2,3)=q: average of j and b or s (halfPelH).
		hv := halfPelHV(pix, stride, w, h, x, y)
		var hp uint8
		if fracY == 1 {
			hp = halfPelH(pix, stride, w, h, x, y) // b
		} else {
			hp = halfPelH(pix, stride, w, h, x, y+1) // s
		}
		return uint8((int(hv) + int(hp) + 1) >> 1)
	}

	if fracY == 2 {
		// (1,2)=i or (3,2)=k: average of j and h or m (halfPelV).
		hv := halfPelHV(pix, stride, w, h, x, y)
		var vp uint8
		if fracX == 1 {
			vp = halfPelV(pix, stride, w, h, x, y) // h
		} else {
			vp = halfPelV(pix, stride, w, h, x+1, y) // m
		}
		return uint8((int(hv) + int(vp) + 1) >> 1)
	}

	// Diagonal quarter-pels: (1,1)=e, (3,1)=g, (1,3)=p, (3,3)=r.
	// e=avg(b,h), g=avg(b,m), p=avg(s,h), r=avg(s,m).
	hpY := y
	if fracY == 3 {
		hpY = y + 1 // s row instead of b row
	}
	vpX := x
	if fracX == 3 {
		vpX = x + 1 // m column instead of h column
	}
	hp := halfPelH(pix, stride, w, h, x, hpY)
	vp := halfPelV(pix, stride, w, h, vpX, y)
	return uint8((int(hp) + int(vp) + 1) >> 1)
}

// 6-tap FIR filter: [1, -5, 20, 20, -5, 1] / 32 with rounding.
func filter6Tap(a, b, c, d, e, f int) int {
	return a - 5*b + 20*c + 20*d - 5*e + f
}

func halfPelH(pix []byte, stride, w, h, x, y int) uint8 {
	v := filter6Tap(
		getPixY(pix, stride, w, h, x-2, y),
		getPixY(pix, stride, w, h, x-1, y),
		getPixY(pix, stride, w, h, x, y),
		getPixY(pix, stride, w, h, x+1, y),
		getPixY(pix, stride, w, h, x+2, y),
		getPixY(pix, stride, w, h, x+3, y),
	)
	v = (v + 16) >> 5
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func halfPelV(pix []byte, stride, w, h, x, y int) uint8 {
	v := filter6Tap(
		getPixY(pix, stride, w, h, x, y-2),
		getPixY(pix, stride, w, h, x, y-1),
		getPixY(pix, stride, w, h, x, y),
		getPixY(pix, stride, w, h, x, y+1),
		getPixY(pix, stride, w, h, x, y+2),
		getPixY(pix, stride, w, h, x, y+3),
	)
	v = (v + 16) >> 5
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func halfPelHV(pix []byte, stride, w, h, x, y int) uint8 {
	// First apply horizontal 6-tap to get half-pel horizontally for rows y-2..y+3,
	// then apply vertical 6-tap on those intermediate values.
	var tmp [6]int
	for k := 0; k < 6; k++ {
		row := y - 2 + k
		tmp[k] = filter6Tap(
			getPixY(pix, stride, w, h, x-2, row),
			getPixY(pix, stride, w, h, x-1, row),
			getPixY(pix, stride, w, h, x, row),
			getPixY(pix, stride, w, h, x+1, row),
			getPixY(pix, stride, w, h, x+2, row),
			getPixY(pix, stride, w, h, x+3, row),
		)
	}
	v := filter6Tap(tmp[0], tmp[1], tmp[2], tmp[3], tmp[4], tmp[5])
	v = (v + 512) >> 10
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// bilinearChroma performs chroma interpolation at 1/8-pixel precision.
func bilinearChroma(pix []byte, stride, w, h, x, y, fracX, fracY int) uint8 {
	if fracX == 0 && fracY == 0 {
		return uint8(getPixY(pix, stride, w, h, x, y))
	}
	a := getPixY(pix, stride, w, h, x, y)
	b := getPixY(pix, stride, w, h, x+1, y)
	c := getPixY(pix, stride, w, h, x, y+1)
	dd := getPixY(pix, stride, w, h, x+1, y+1)

	val := (8-fracX)*(8-fracY)*a +
		fracX*(8-fracY)*b +
		(8-fracX)*fracY*c +
		fracX*fracY*dd
	return uint8((val + 32) >> 6)
}

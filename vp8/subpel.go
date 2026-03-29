package vp8

// Sub-pixel interpolation filters per RFC 6386 §18.3.

// subPelFilters are the 6-tap filter kernels for 1/8-pixel positions.
var subPelFilters = [8][6]int16{
	{0, 0, 128, 0, 0, 0},     // 0/8 full pixel
	{0, -6, 123, 12, -1, 0},  // 1/8
	{2, -11, 108, 36, -8, 1}, // 2/8 quarter
	{0, -9, 93, 50, -6, 0},   // 3/8
	{3, -16, 77, 77, -16, 3}, // 4/8 half
	{0, -6, 50, 93, -9, 0},   // 5/8
	{1, -8, 36, 108, -11, 2}, // 6/8
	{0, -1, 12, 123, -6, 0},  // 7/8
}

// sixtapFilter performs 2D separable 6-tap sub-pixel interpolation.
// src is the reference plane, srcStride is its stride.
// baseX, baseY are the integer-pixel source coordinates (may be negative for edge clamping).
// xFrac and yFrac are 0..7 fractional positions.
// dst is the output buffer, dstStride is its stride. w and h are block dimensions.
// srcWidth and srcHeight are the source plane dimensions for 2D edge clamping.
func sixtapFilter(src []byte, srcStride int, baseX, baseY, xFrac, yFrac int, dst []byte, dstStride, w, h, srcWidth, srcHeight int) {

	if xFrac == 0 && yFrac == 0 {
		for y := 0; y < h; y++ {
			di := y * dstStride
			for x := 0; x < w; x++ {
				dst[di+x] = clampSrc2D(src, srcStride, srcWidth, srcHeight, baseX+x, baseY+y)
			}
		}
		return
	}

	intH := h + 5
	intW := w
	intermediate := make([]int32, intH*intW)

	// Horizontal pass: round and clamp after each pass per libvpx.
	if xFrac == 0 {
		for y := 0; y < intH; y++ {
			for x := 0; x < intW; x++ {
				intermediate[y*intW+x] = int32(clampSrc2D(src, srcStride, srcWidth, srcHeight, baseX+x, baseY+y-2))
			}
		}
	} else {
		filt := subPelFilters[xFrac]
		for y := 0; y < intH; y++ {
			for x := 0; x < intW; x++ {
				var sum int32
				for k := 0; k < 6; k++ {
					sum += int32(filt[k]) * int32(clampSrc2D(src, srcStride, srcWidth, srcHeight, baseX+x+k-2, baseY+y-2))
				}
				intermediate[y*intW+x] = int32(clamp255byte((sum + 64) >> 7))
			}
		}
	}

	// Vertical pass: intermediate values are already rounded to [0,255].
	if yFrac == 0 {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				// xFrac==0 && yFrac==0 is handled by early return above,
				// so intermediate is always a rounded pixel value here.
				dst[y*dstStride+x] = byte(intermediate[(y+2)*intW+x])
			}
		}
	} else {
		filt := subPelFilters[yFrac]
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var sum int32
				for k := 0; k < 6; k++ {
					sum += int32(filt[k]) * intermediate[(y+k)*intW+x]
				}
				v := (sum + 64) >> 7
				dst[y*dstStride+x] = clamp255byte(v)
			}
		}
	}
}

// bilinearFilter performs bilinear sub-pixel interpolation.
// srcWidth and srcHeight are the source plane dimensions for 2D edge clamping.
func bilinearFilter(src []byte, srcStride int, baseX, baseY, xFrac, yFrac int, dst []byte, dstStride, w, h, srcWidth, srcHeight int) {

	if xFrac == 0 && yFrac == 0 {
		for y := 0; y < h; y++ {
			di := y * dstStride
			for x := 0; x < w; x++ {
				dst[di+x] = clampSrc2D(src, srcStride, srcWidth, srcHeight, baseX+x, baseY+y)
			}
		}
		return
	}

	intermediate := make([]int32, (h+1)*w)

	// Horizontal pass: round per pass per libvpx/spec.
	if xFrac == 0 {
		for y := 0; y < h+1; y++ {
			for x := 0; x < w; x++ {
				intermediate[y*w+x] = int32(clampSrc2D(src, srcStride, srcWidth, srcHeight, baseX+x, baseY+y))
			}
		}
	} else {
		for y := 0; y < h+1; y++ {
			for x := 0; x < w; x++ {
				a := int32(clampSrc2D(src, srcStride, srcWidth, srcHeight, baseX+x, baseY+y))
				b := int32(clampSrc2D(src, srcStride, srcWidth, srcHeight, baseX+x+1, baseY+y))
				intermediate[y*w+x] = int32(clamp255byte((a*(8-int32(xFrac)) + b*int32(xFrac) + 4) >> 3))
			}
		}
	}

	// Vertical pass: intermediate values are already rounded to [0,255].
	if yFrac == 0 {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				// xFrac==0 && yFrac==0 is handled by early return above.
				dst[y*dstStride+x] = byte(intermediate[y*w+x])
			}
		}
	} else {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				a := intermediate[y*w+x]
				b := intermediate[(y+1)*w+x]
				v := (a*(8-int32(yFrac)) + b*int32(yFrac) + 4) >> 3
				dst[y*dstStride+x] = clamp255byte(v)
			}
		}
	}
}

// clampSrc2D returns the source pixel at (x, y) with independent x/y edge clamping.
// This implements VP8's edge replication for out-of-bounds reference pixels.
func clampSrc2D(src []byte, stride, width, height, x, y int) byte {
	if x < 0 {
		x = 0
	} else if x >= width {
		x = width - 1
	}
	if y < 0 {
		y = 0
	} else if y >= height {
		y = height - 1
	}
	return src[y*stride+x]
}

func clamp255byte(v int32) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

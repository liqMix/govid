package govid

import "image"

// convertYCbCr420ToRGBA converts a 4:2:0 YCbCr image directly into packed
// RGBA bytes using fixed-point math identical to Go's color.YCbCrToRGB.
// dst is reused if it has sufficient capacity; otherwise a new slice is allocated.
func convertYCbCr420ToRGBA(ycbcr *image.YCbCr, dst []byte) []byte {
	bounds := ycbcr.Rect
	w := bounds.Dx()
	h := bounds.Dy()
	need := w * h * 4

	if cap(dst) < need {
		dst = make([]byte, need)
	} else {
		dst = dst[:need]
	}

	yOff := bounds.Min.Y*ycbcr.YStride + bounds.Min.X
	cOff := (bounds.Min.Y/2)*ycbcr.CStride + (bounds.Min.X / 2)

	yData := ycbcr.Y
	cbData := ycbcr.Cb
	crData := ycbcr.Cr
	yStride := ycbcr.YStride
	cStride := ycbcr.CStride

	di := 0
	for row := 0; row < h; row++ {
		yi := yOff + row*yStride
		ci := cOff + (row/2)*cStride

		for col := 0; col < w; col++ {
			yy1 := int32(yData[yi+col]) * 0x10101
			cb1 := int32(cbData[ci+col/2]) - 128
			cr1 := int32(crData[ci+col/2]) - 128

			r := yy1 + 91881*cr1
			g := yy1 - 22554*cb1 - 46802*cr1
			b := yy1 + 116130*cb1

			if r < 0 {
				r = 0
			} else if r > 0xff0000 {
				r = 0xff0000
			}
			if g < 0 {
				g = 0
			} else if g > 0xff0000 {
				g = 0xff0000
			}
			if b < 0 {
				b = 0
			} else if b > 0xff0000 {
				b = 0xff0000
			}

			dst[di] = uint8(r >> 16)
			dst[di+1] = uint8(g >> 16)
			dst[di+2] = uint8(b >> 16)
			dst[di+3] = 0xff
			di += 4
		}
	}
	return dst
}

// ConvertRGBA converts the frame's YCbCr data to packed RGBA bytes.
// If dst has sufficient capacity it is reused; otherwise a new slice is allocated.
// Returns the (possibly grown) buffer.
func (f *Frame) ConvertRGBA(dst []byte) []byte {
	return convertYCbCr420ToRGBA(f.YCbCr, dst)
}

// HasRGBA reports whether the frame already holds converted RGBA pixels, so
// RGBA costs nothing. True for frames from a player created with WithRGBA, and
// for any frame whose RGBA has already been computed.
func (f *Frame) HasRGBA() bool {
	return f.rgba != nil
}

// RGBA returns the frame pixels as packed RGBA bytes.
// The result is cached; subsequent calls return the same slice.
func (f *Frame) RGBA() []byte {
	if f.rgba != nil {
		return f.rgba
	}
	f.rgba = f.ConvertRGBA(nil)
	return f.rgba
}

package ebiv

import "image"

// strideAlign pads every plane row up to a cache-line boundary. Two goroutines
// working on vertically adjacent tiles then never write the same 64-byte line,
// which is the false-sharing ceiling described in the design plan's §6.5.
const strideAlign = 64

// geometry holds the derived plane dimensions for one stream. Every buffer
// allocation and every plane copy goes through it, so there is exactly one
// place where subsampling arithmetic lives.
type geometry struct {
	W, H    int // luma dimensions
	CW, CH  int // chroma dimensions
	YStride int
	CStride int
}

// geometryFor derives the plane layout for a luma size. Odd dimensions round
// chroma up, matching image.YCbCrSubsampleRatio420.
func geometryFor(w, h int) geometry {
	cw, ch := (w+1)/2, (h+1)/2
	return geometry{
		W: w, H: h,
		CW: cw, CH: ch,
		YStride: alignUp(w, strideAlign),
		CStride: alignUp(cw, strideAlign),
	}
}

func alignUp(v, align int) int { return (v + align - 1) &^ (align - 1) }

// packedSize is the number of bytes the three planes occupy when stored
// tightly packed, with no stride padding — the on-disk layout of a raw frame.
func (g geometry) packedSize() int { return g.W*g.H + 2*g.CW*g.CH }

// newImage allocates a strided YCbCr image matching g.
func (g geometry) newImage() *image.YCbCr {
	return &image.YCbCr{
		Y:              make([]byte, g.YStride*g.H),
		Cb:             make([]byte, g.CStride*g.CH),
		Cr:             make([]byte, g.CStride*g.CH),
		YStride:        g.YStride,
		CStride:        g.CStride,
		SubsampleRatio: image.YCbCrSubsampleRatio420,
		Rect:           image.Rect(0, 0, g.W, g.H),
	}
}

// matches reports whether img has exactly the layout g describes.
//
// An odd Rect.Min is rejected: image.YCbCr's COffset maps a crop origin of x to
// chroma column x/2, so cropping at an odd column would shift chroma half a
// luma pixel against the luma it is paired with.
func (g geometry) matches(img *image.YCbCr) bool {
	return img != nil &&
		img.SubsampleRatio == image.YCbCrSubsampleRatio420 &&
		img.Rect.Dx() == g.W && img.Rect.Dy() == g.H &&
		img.Rect.Min.X%2 == 0 && img.Rect.Min.Y%2 == 0
}

// scatterPlane copies a tightly packed w-by-h plane into a strided destination.
//
// Both slices are re-sliced per row with a three-index expression so the
// compiler can prove the copy is in range; copy itself lowers to a memmove.
func scatterPlane(dst []byte, stride int, src []byte, w, h int) {
	for y := 0; y < h; y++ {
		d := dst[y*stride : y*stride+w : y*stride+w]
		s := src[y*w : y*w+w : y*w+w]
		copy(d, s)
	}
}

// gatherPlane is the inverse of scatterPlane: it packs a strided plane into a
// tightly packed destination.
func gatherPlane(dst []byte, src []byte, stride, w, h int) {
	for y := 0; y < h; y++ {
		d := dst[y*w : y*w+w : y*w+w]
		s := src[y*stride : y*stride+w : y*stride+w]
		copy(d, s)
	}
}

// image.YCbCr indexes its planes relative to Rect.Min — YOffset and COffset
// both subtract the origin, and SubImage advances the plane slices to match. A
// crop's first visible sample is therefore always at index 0, and no origin
// adjustment is needed when reading an encoder input.

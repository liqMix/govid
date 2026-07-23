package ebiv

// Motion compensation (§3.4). A single reference frame, luma predicted at
// quarter-pel with a separable 4-tap 4-phase filter, chroma at eighth-pel with
// bilinear interpolation. All motion-search quality is the encoder's problem;
// the decoder just samples where it is told.
//
// Reference coordinates are clamped to the padded plane, so a motion vector
// pointing off the edge reads a replicated border rather than crashing.

// motionVector is a displacement in quarter-pel luma units.
type motionVector struct {
	x, y int16
}

// mvClampRange bounds a motion vector so a search or a corrupt stream cannot
// drive the reference sample position wildly out of range. In quarter-pel units
// this is one frame width of slack past the edge.
const mvClampRange = 4096

// lumaTaps is the 4-tap luma interpolation bank indexed by the quarter-pel
// phase. Phase 0 copies; phase 2 is the half-pel (−1,5,5,−1)-shape kernel;
// phases 1 and 3 are the quarter positions. Each row sums to 64.
var lumaTaps = [4][4]int32{
	{0, 64, 0, 0},
	{-6, 55, 18, -3},
	{-8, 40, 40, -8},
	{-3, 18, 55, -6},
}

func clampCoord(v, hi int) int {
	if v < 0 {
		return 0
	}
	if v >= hi {
		return hi - 1
	}
	return v
}

// lumaSubpel samples the reference at (px,py) with quarter-pel fractions
// fx,fy in 0..3.
func lumaSubpel(ref planeView, px, py, fx, fy int) int32 {
	g := func(x, y int) int32 {
		return int32(ref.at(clampCoord(x, ref.w), clampCoord(y, ref.h)))
	}
	switch {
	case fx == 0 && fy == 0:
		return g(px, py)
	case fy == 0:
		t := lumaTaps[fx]
		v := t[0]*g(px-1, py) + t[1]*g(px, py) + t[2]*g(px+1, py) + t[3]*g(px+2, py)
		return int32(clampByte((v + 32) >> 6))
	case fx == 0:
		t := lumaTaps[fy]
		v := t[0]*g(px, py-1) + t[1]*g(px, py) + t[2]*g(px, py+1) + t[3]*g(px, py+2)
		return int32(clampByte((v + 32) >> 6))
	default:
		// Separable: horizontal filter into four intermediate rows, then
		// vertical. Intermediate values keep extra precision (shift by 6 only
		// at the end) to avoid double-rounding.
		tx := lumaTaps[fx]
		var h [4]int32
		for k := 0; k < 4; k++ {
			yy := py - 1 + k
			h[k] = tx[0]*g(px-1, yy) + tx[1]*g(px, yy) + tx[2]*g(px+1, yy) + tx[3]*g(px+2, yy)
		}
		ty := lumaTaps[fy]
		v := ty[0]*h[0] + ty[1]*h[1] + ty[2]*h[2] + ty[3]*h[3]
		return int32(clampByte((v + (1 << 11)) >> 12))
	}
}

// chromaSubpel samples the reference at (px,py) with eighth-pel fractions
// fx,fy in 0..7 using bilinear interpolation.
func chromaSubpel(ref planeView, px, py, fx, fy int) int32 {
	g := func(x, y int) int32 {
		return int32(ref.at(clampCoord(x, ref.w), clampCoord(y, ref.h)))
	}
	if fx == 0 && fy == 0 {
		return g(px, py)
	}
	a := g(px, py)
	b := g(px+1, py)
	c := g(px, py+1)
	d := g(px+1, py+1)
	fxi := int32(fx)
	fyi := int32(fy)
	v := (8-fxi)*(8-fyi)*a + fxi*(8-fyi)*b + (8-fxi)*fyi*c + fxi*fyi*d
	return (v + 32) >> 6
}

// mcLumaMB fills a 16×16 luma prediction for the macroblock at (mbx,mby) from
// the reference under motion vector mv (quarter-pel).
func mcLumaMB(ref planeView, mbx, mby int, mv motionVector, out []int32) {
	mcLuma(ref, mbx*mbSize+int(mv.x>>2), mby*mbSize+int(mv.y>>2),
		int(mv.x&3), int(mv.y&3), mbSize, mbSize, out, 0, 0, mbSize)
}

// mcLuma fills a pw×ph luma block into out at (dstX,dstY,dstStride), sampling
// the reference at (srcX,srcY) with quarter-pel fractions fx,fy. When the whole
// footprint — including the 4-tap filter's ±1..+2 support — lies inside the
// reference, it takes the interior fast path: no per-pixel clamp, no closure,
// hoisted row slices so the compiler drops the bounds checks (§6.2). This is
// the dominant decode cost on real content, where most blocks are interior.
func mcLuma(ref planeView, srcX, srcY, fx, fy, pw, ph int, out []int32, dstX, dstY, dstStride int) {
	if interiorLuma(ref, srcX, srcY, fx, fy, pw, ph) {
		mcLumaInterior(ref, srcX, srcY, fx, fy, pw, ph, out, dstX, dstY, dstStride)
		return
	}
	for r := 0; r < ph; r++ {
		db := (dstY+r)*dstStride + dstX
		for c := 0; c < pw; c++ {
			out[db+c] = lumaSubpel(ref, srcX+c, srcY+r, fx, fy)
		}
	}
}

func interiorLuma(ref planeView, srcX, srcY, fx, fy, pw, ph int) bool {
	if fx == 0 && fy == 0 {
		return srcX >= 0 && srcY >= 0 && srcX+pw <= ref.w && srcY+ph <= ref.h
	}
	return srcX-1 >= 0 && srcY-1 >= 0 && srcX+pw+2 <= ref.w && srcY+ph+2 <= ref.h
}

func mcLumaInterior(ref planeView, srcX, srcY, fx, fy, pw, ph int, out []int32, dstX, dstY, dstStride int) {
	data, stride := ref.data, ref.stride
	switch {
	case fx == 0 && fy == 0:
		for r := 0; r < ph; r++ {
			sb := (srcY+r)*stride + srcX
			src := data[sb : sb+pw : sb+pw]
			db := (dstY+r)*dstStride + dstX
			dst := out[db : db+pw : db+pw]
			for c := 0; c < pw; c++ {
				dst[c] = int32(src[c])
			}
		}
	case fy == 0:
		t := lumaTaps[fx]
		for r := 0; r < ph; r++ {
			sb := (srcY+r)*stride + srcX
			db := (dstY+r)*dstStride + dstX
			dst := out[db : db+pw : db+pw]
			for c := 0; c < pw; c++ {
				b := sb + c
				v := t[0]*int32(data[b-1]) + t[1]*int32(data[b]) + t[2]*int32(data[b+1]) + t[3]*int32(data[b+2])
				dst[c] = int32(clampByte((v + 32) >> 6))
			}
		}
	case fx == 0:
		t := lumaTaps[fy]
		for r := 0; r < ph; r++ {
			sb := (srcY+r)*stride + srcX
			db := (dstY+r)*dstStride + dstX
			dst := out[db : db+pw : db+pw]
			for c := 0; c < pw; c++ {
				b := sb + c
				v := t[0]*int32(data[b-stride]) + t[1]*int32(data[b]) + t[2]*int32(data[b+stride]) + t[3]*int32(data[b+2*stride])
				dst[c] = int32(clampByte((v + 32) >> 6))
			}
		}
	default:
		tx, ty := lumaTaps[fx], lumaTaps[fy]
		for r := 0; r < ph; r++ {
			db := (dstY+r)*dstStride + dstX
			dst := out[db : db+pw : db+pw]
			for c := 0; c < pw; c++ {
				var h [4]int32
				for k := 0; k < 4; k++ {
					b := (srcY+r-1+k)*stride + srcX + c
					h[k] = tx[0]*int32(data[b-1]) + tx[1]*int32(data[b]) + tx[2]*int32(data[b+1]) + tx[3]*int32(data[b+2])
				}
				v := ty[0]*h[0] + ty[1]*h[1] + ty[2]*h[2] + ty[3]*h[3]
				dst[c] = int32(clampByte((v + (1 << 11)) >> 12))
			}
		}
	}
}

// mcLumaRect fills one motion partition's luma samples into a 16×16 macroblock
// buffer: the rectangle (px,py,pw,ph) within the macroblock at (mbx,mby), from
// the reference under mv.
func mcLumaRect(ref planeView, mbx, mby, px, py, pw, ph int, mv motionVector, out []int32) {
	mcLuma(ref, mbx*mbSize+px+int(mv.x>>2), mby*mbSize+py+int(mv.y>>2),
		int(mv.x&3), int(mv.y&3), pw, ph, out, px, py, mbSize)
}

// mcChromaBlock fills an 8×8 chroma prediction at (x0,y0) from the reference. A
// quarter-pel luma vector maps to an eighth-pel chroma vector, since chroma is
// half resolution.
func mcChromaBlock(ref planeView, x0, y0 int, mv motionVector, out []int32) {
	mcChroma(ref, x0+int(mv.x>>3), y0+int(mv.y>>3),
		int(mv.x&7), int(mv.y&7), chromaMB, chromaMB, out, 0, 0, chromaMB)
}

// mcChromaRect fills one partition's chroma samples into an 8×8 buffer. The
// luma rectangle halves to chroma.
func mcChromaRect(ref planeView, cx, cy, px, py, pw, ph int, mv motionVector, out []int32) {
	cpx, cpy, cpw, cph := px/2, py/2, pw/2, ph/2
	mcChroma(ref, cx+cpx+int(mv.x>>3), cy+cpy+int(mv.y>>3),
		int(mv.x&7), int(mv.y&7), cpw, cph, out, cpx, cpy, chromaMB)
}

// mcChroma fills a pw×ph chroma block into out at (dstX,dstY,dstStride) with
// eighth-pel bilinear interpolation, taking the interior fast path when the
// 2×2 bilinear footprint stays inside the reference.
func mcChroma(ref planeView, srcX, srcY, fx, fy, pw, ph int, out []int32, dstX, dstY, dstStride int) {
	interior := srcX >= 0 && srcY >= 0 && srcX+pw+1 <= ref.w && srcY+ph+1 <= ref.h
	if fx == 0 && fy == 0 {
		interior = srcX >= 0 && srcY >= 0 && srcX+pw <= ref.w && srcY+ph <= ref.h
	}
	if !interior {
		for r := 0; r < ph; r++ {
			db := (dstY+r)*dstStride + dstX
			for c := 0; c < pw; c++ {
				out[db+c] = chromaSubpel(ref, srcX+c, srcY+r, fx, fy)
			}
		}
		return
	}
	data, stride := ref.data, ref.stride
	if fx == 0 && fy == 0 {
		for r := 0; r < ph; r++ {
			sb := (srcY+r)*stride + srcX
			src := data[sb : sb+pw : sb+pw]
			db := (dstY+r)*dstStride + dstX
			dst := out[db : db+pw : db+pw]
			for c := 0; c < pw; c++ {
				dst[c] = int32(src[c])
			}
		}
		return
	}
	fxi, fyi := int32(fx), int32(fy)
	w00 := (8 - fxi) * (8 - fyi)
	w01 := fxi * (8 - fyi)
	w10 := (8 - fxi) * fyi
	w11 := fxi * fyi
	for r := 0; r < ph; r++ {
		sb := (srcY+r)*stride + srcX
		db := (dstY+r)*dstStride + dstX
		dst := out[db : db+pw : db+pw]
		for c := 0; c < pw; c++ {
			b := sb + c
			v := w00*int32(data[b]) + w01*int32(data[b+1]) + w10*int32(data[b+stride]) + w11*int32(data[b+stride+1])
			dst[c] = (v + 32) >> 6
		}
	}
}

// clampMV keeps a motion vector within the search/decode range.
func clampMV(v int) int16 {
	if v < -mvClampRange {
		v = -mvClampRange
	}
	if v > mvClampRange {
		v = mvClampRange
	}
	return int16(v)
}

// zigzagEncode maps a signed value to an unsigned one for LEB128 coding, and
// zigzagDecode inverts it.
func zigzagEncode(v int) uint {
	return uint((v << 1) ^ (v >> 31))
}

func zigzagDecode(u uint) int {
	return int(u>>1) ^ -int(u&1)
}

package av1

import "math"

// PredictIntra performs intra prediction for a block.
// dst is the output buffer (row-major), stride is the output stride.
// above and left are the reference samples (above[0..w-1], left[0..h-1]).
// topLeft is the top-left corner sample.
func PredictIntra(dst []uint8, stride int, mode int, w, h int,
	above, left []uint8, topLeft uint8, haveAbove, haveLeft bool) {

	switch mode {
	case IntraDC:
		predDC(dst, stride, w, h, above, left, haveAbove, haveLeft)
	case IntraVertical:
		predVertical(dst, stride, w, h, above)
	case IntraHorizontal:
		predHorizontal(dst, stride, w, h, left)
	case IntraPaeth:
		predPaeth(dst, stride, w, h, above, left, topLeft)
	case IntraSmooth:
		predSmooth(dst, stride, w, h, above, left)
	case IntraSmoothV:
		predSmoothV(dst, stride, w, h, above, left)
	case IntraSmoothH:
		predSmoothH(dst, stride, w, h, above, left)
	case IntraD45, IntraD135, IntraD113, IntraD157, IntraD203, IntraD67:
		predDirectional(dst, stride, mode, w, h, above, left, topLeft)
	default:
		predDC(dst, stride, w, h, above, left, haveAbove, haveLeft)
	}
}

// predDC fills the block with the average of above and left samples.
func predDC(dst []uint8, stride, w, h int, above, left []uint8, haveAbove, haveLeft bool) {
	var sum int
	var count int

	if haveAbove {
		for i := 0; i < w; i++ {
			sum += int(above[i])
		}
		count += w
	}
	if haveLeft {
		for i := 0; i < h; i++ {
			sum += int(left[i])
		}
		count += h
	}

	var dc uint8
	if count > 0 {
		dc = uint8((sum + count/2) / count)
	} else {
		dc = 128
	}

	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			dst[r*stride+c] = dc
		}
	}
}

// predVertical fills each row with the above samples.
func predVertical(dst []uint8, stride, w, h int, above []uint8) {
	for r := 0; r < h; r++ {
		copy(dst[r*stride:r*stride+w], above[:w])
	}
}

// predHorizontal fills each column with the left samples.
func predHorizontal(dst []uint8, stride, w, h int, left []uint8) {
	for r := 0; r < h; r++ {
		val := left[r]
		for c := 0; c < w; c++ {
			dst[r*stride+c] = val
		}
	}
}

// predPaeth predicts using the Paeth filter (nearest of above, left, top-left).
func predPaeth(dst []uint8, stride, w, h int, above, left []uint8, topLeft uint8) {
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			a := int(above[c])
			l := int(left[r])
			tl := int(topLeft)
			base := a + l - tl
			pA := abs(base - a)
			pL := abs(base - l)
			pTL := abs(base - tl)

			var pred int
			if pA <= pL && pA <= pTL {
				pred = a
			} else if pL <= pTL {
				pred = l
			} else {
				pred = tl
			}
			dst[r*stride+c] = uint8(pred)
		}
	}
}

// predSmooth performs SMOOTH prediction (weighted blend of above and left).
func predSmooth(dst []uint8, stride, w, h int, above, left []uint8) {
	wOff := SmoothWeightsOffset(w)
	hOff := SmoothWeightsOffset(h)
	belowPred := int(left[h-1])
	rightPred := int(above[w-1])

	for r := 0; r < h; r++ {
		smWeightR := SmoothWeights[hOff+r]
		for c := 0; c < w; c++ {
			smWeightC := SmoothWeights[wOff+c]
			pred := smWeightR*int(above[c]) + (256-smWeightR)*belowPred +
				smWeightC*int(left[r]) + (256-smWeightC)*rightPred
			dst[r*stride+c] = uint8((pred + 256) >> 9)
		}
	}
}

// predSmoothV performs SMOOTH_V prediction (vertical gradient).
func predSmoothV(dst []uint8, stride, w, h int, above, left []uint8) {
	hOff := SmoothWeightsOffset(h)
	belowPred := int(left[h-1])

	for r := 0; r < h; r++ {
		smWeight := SmoothWeights[hOff+r]
		for c := 0; c < w; c++ {
			pred := smWeight*int(above[c]) + (256-smWeight)*belowPred
			dst[r*stride+c] = uint8((pred + 128) >> 8)
		}
	}
}

// predSmoothH performs SMOOTH_H prediction (horizontal gradient).
func predSmoothH(dst []uint8, stride, w, h int, above, left []uint8) {
	wOff := SmoothWeightsOffset(w)
	rightPred := int(above[w-1])

	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			smWeight := SmoothWeights[wOff+c]
			pred := smWeight*int(left[r]) + (256-smWeight)*rightPred
			dst[r*stride+c] = uint8((pred + 128) >> 8)
		}
	}
}

// Directional mode angles.
var modeToAngle = [NumIntraModes]int{
	0,   // DC
	90,  // V
	180, // H
	45,  // D45
	135, // D135
	113, // D113
	157, // D157
	203, // D203
	67,  // D67
	0, 0, 0, 0, // SMOOTH, SMOOTH_V, SMOOTH_H, PAETH
}

// predDirectional performs directional intra prediction.
func predDirectional(dst []uint8, stride, mode, w, h int,
	above, left []uint8, topLeft uint8) {

	angle := modeToAngle[mode]
	if angle == 0 {
		return
	}

	// Compute dx and dy from angle.
	dx := getDx(angle)
	dy := getDy(angle)

	if angle < 90 {
		// Mainly above reference.
		for r := 0; r < h; r++ {
			for c := 0; c < w; c++ {
				idx := (r+1)*dx + (c << 8)
				base := idx >> 8
				frac := idx & 0xFF
				if base >= 0 && base < len(above)-1 {
					dst[r*stride+c] = uint8(
						(int(above[base])*(256-frac) + int(above[base+1])*frac + 128) >> 8)
				} else if base >= 0 && base < len(above) {
					dst[r*stride+c] = above[base]
				}
			}
		}
	} else if angle > 90 && angle < 180 {
		// Mix of above and left.
		for r := 0; r < h; r++ {
			for c := 0; c < w; c++ {
				idx := (c+1)*dy + (r << 8)
				base := idx >> 8
				frac := idx & 0xFF
				if base >= 0 && base < len(left)-1 {
					dst[r*stride+c] = uint8(
						(int(left[base])*(256-frac) + int(left[base+1])*frac + 128) >> 8)
				} else if base >= 0 && base < len(left) {
					dst[r*stride+c] = left[base]
				}
			}
		}
	} else if angle > 180 {
		// Mainly left reference.
		for r := 0; r < h; r++ {
			for c := 0; c < w; c++ {
				idx := (c+1)*dx + (r << 8)
				base := idx >> 8
				frac := idx & 0xFF
				if base >= 0 && base < len(left)-1 {
					dst[r*stride+c] = uint8(
						(int(left[base])*(256-frac) + int(left[base+1])*frac + 128) >> 8)
				} else if base >= 0 && base < len(left) {
					dst[r*stride+c] = left[base]
				}
			}
		}
	} else {
		// angle == 90 or 180: handled by V_PRED or H_PRED
		if angle == 90 {
			predVertical(dst, stride, w, h, above)
		} else {
			predHorizontal(dst, stride, w, h, left)
		}
	}
}

// getDx returns the horizontal displacement for a given angle (scaled by 256).
func getDx(angle int) int {
	if angle == 90 {
		return 0
	}
	rad := float64(angle) * math.Pi / 180.0
	return int(math.Round(256.0 / math.Tan(rad)))
}

// getDy returns the vertical displacement for a given angle (scaled by 256).
func getDy(angle int) int {
	if angle == 180 {
		return 0
	}
	rad := float64(angle) * math.Pi / 180.0
	return int(math.Round(256.0 * math.Tan(rad-math.Pi)))
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

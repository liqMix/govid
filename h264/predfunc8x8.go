package h264

// H.264 Intra_8x8 luma prediction modes (spec 8.3.2.2).
//
// Intra_8x8 prediction differs from Intra_4x4 in two ways:
//  1. Neighbor samples are low-pass filtered before use (spec 8.3.2.2.1).
//  2. Prediction indices run 0..15 horizontally (top row needs up to 16
//     samples for Diagonal-Down-Left / Vertical-Left modes) and 0..7 vertically.
//
// All 9 mode functions take filtered samples via *intra8x8Samples and write
// the 8×8 prediction into d.ybr[y..y+7][x..x+7].

// intra8x8Samples holds the filtered neighbor samples for an Intra_8x8 block.
//
//	top[0..15]  — filtered p'[0..15, -1]. Positions 8..15 may be replicated
//	              from position 7 when the above-right block isn't available.
//	left[0..7]  — filtered p'[-1, 0..7]
//	corner      — filtered p'[-1, -1]
//
// The has* flags mark whether the raw samples were available before filtering.
type intra8x8Samples struct {
	top       [16]uint8
	left      [8]uint8
	corner    uint8
	hasTop    bool
	hasLeft   bool
	hasCorner bool
}

// topAt returns the filtered top sample, treating index -1 as the corner.
// Used by modes that walk a diagonal past position 0 (DDR, VR, HD).
func (s *intra8x8Samples) topAt(i int) int {
	if i < 0 {
		return int(s.corner)
	}
	return int(s.top[i])
}

// leftAt returns the filtered left sample, treating index -1 as the corner.
func (s *intra8x8Samples) leftAt(i int) int {
	if i < 0 {
		return int(s.corner)
	}
	return int(s.left[i])
}

// prepareIntra8x8Samples reads raw neighbor samples around the 8x8 block at
// d.ybr[y..y+7][x..x+7] and applies the low-pass filter per spec 8.3.2.2.1.
//
// hasTop/hasLeft/hasCorner say whether each direction's raw samples exist.
// aboveRightAvail says whether p[8..15, -1] are usable (end-of-row or
// not-yet-decoded blocks cause replication from p[7, -1] per spec).
func (d *Decoder) prepareIntra8x8Samples(y, x int, hasTop, hasLeft, hasCorner, aboveRightAvail bool) intra8x8Samples {
	var s intra8x8Samples
	s.hasTop = hasTop
	s.hasLeft = hasLeft
	s.hasCorner = hasCorner

	var rawTop [16]uint8
	var rawLeft [8]uint8
	var rawCorner uint8

	if hasTop {
		for i := 0; i < 8; i++ {
			rawTop[i] = d.ybr[y-1][x+i]
		}
		if aboveRightAvail {
			for i := 8; i < 16; i++ {
				rawTop[i] = d.ybr[y-1][x+i]
			}
		} else {
			for i := 8; i < 16; i++ {
				rawTop[i] = rawTop[7]
			}
		}
	}
	if hasLeft {
		for i := 0; i < 8; i++ {
			rawLeft[i] = d.ybr[y+i][x-1]
		}
	}
	if hasCorner {
		rawCorner = d.ybr[y-1][x-1]
	}

	// Filter top row (spec 8.3.2.2.1).
	if hasTop {
		if hasCorner {
			s.top[0] = uint8((int(rawCorner) + 2*int(rawTop[0]) + int(rawTop[1]) + 2) >> 2)
		} else {
			s.top[0] = uint8((3*int(rawTop[0]) + int(rawTop[1]) + 2) >> 2)
		}
		for i := 1; i < 15; i++ {
			s.top[i] = uint8((int(rawTop[i-1]) + 2*int(rawTop[i]) + int(rawTop[i+1]) + 2) >> 2)
		}
		s.top[15] = uint8((int(rawTop[14]) + 3*int(rawTop[15]) + 2) >> 2)
	}

	// Filter left column.
	if hasLeft {
		if hasCorner {
			s.left[0] = uint8((int(rawCorner) + 2*int(rawLeft[0]) + int(rawLeft[1]) + 2) >> 2)
		} else {
			s.left[0] = uint8((3*int(rawLeft[0]) + int(rawLeft[1]) + 2) >> 2)
		}
		for i := 1; i < 7; i++ {
			s.left[i] = uint8((int(rawLeft[i-1]) + 2*int(rawLeft[i]) + int(rawLeft[i+1]) + 2) >> 2)
		}
		s.left[7] = uint8((int(rawLeft[6]) + 3*int(rawLeft[7]) + 2) >> 2)
	}

	// Filter corner. Uses top[0] and/or left[0] depending on availability.
	if hasCorner {
		switch {
		case hasTop && hasLeft:
			s.corner = uint8((int(rawTop[0]) + 2*int(rawCorner) + int(rawLeft[0]) + 2) >> 2)
		case hasTop:
			s.corner = uint8((3*int(rawCorner) + int(rawTop[0]) + 2) >> 2)
		case hasLeft:
			s.corner = uint8((3*int(rawCorner) + int(rawLeft[0]) + 2) >> 2)
		default:
			s.corner = rawCorner
		}
	}

	return s
}

// --- Mode functions: 9 directional predictors per spec 8.3.2.2.2–8.3.2.2.10 ---

// Mode 0: Vertical. Requires top samples.
func predIntra8x8Vertical(d *Decoder, y, x int, s *intra8x8Samples) {
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			d.ybr[y+j][x+i] = s.top[i]
		}
	}
}

// Mode 1: Horizontal. Requires left samples.
func predIntra8x8Horizontal(d *Decoder, y, x int, s *intra8x8Samples) {
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			d.ybr[y+j][x+i] = s.left[j]
		}
	}
}

// Mode 2: DC. Uses whichever of top/left are available; 128 if neither.
func predIntra8x8DC(d *Decoder, y, x int, s *intra8x8Samples) {
	var dc uint8
	switch {
	case s.hasTop && s.hasLeft:
		sum := 0
		for i := 0; i < 8; i++ {
			sum += int(s.top[i]) + int(s.left[i])
		}
		dc = uint8((sum + 8) >> 4)
	case s.hasTop:
		sum := 0
		for i := 0; i < 8; i++ {
			sum += int(s.top[i])
		}
		dc = uint8((sum + 4) >> 3)
	case s.hasLeft:
		sum := 0
		for i := 0; i < 8; i++ {
			sum += int(s.left[i])
		}
		dc = uint8((sum + 4) >> 3)
	default:
		dc = 128
	}
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			d.ybr[y+j][x+i] = dc
		}
	}
}

// Mode 3: Diagonal Down-Left. Requires top (and uses top[8..15] which may be
// replicated if above-right unavailable).
func predIntra8x8DDL(d *Decoder, y, x int, s *intra8x8Samples) {
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			var v int
			if i == 7 && j == 7 {
				v = int(s.top[14]) + 3*int(s.top[15]) + 2
			} else {
				v = int(s.top[i+j]) + 2*int(s.top[i+j+1]) + int(s.top[i+j+2]) + 2
			}
			d.ybr[y+j][x+i] = uint8(v >> 2)
		}
	}
}

// Mode 4: Diagonal Down-Right. Requires top, left, corner.
func predIntra8x8DDR(d *Decoder, y, x int, s *intra8x8Samples) {
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			var v int
			switch {
			case i > j:
				v = s.topAt(i-j-2) + 2*s.topAt(i-j-1) + s.topAt(i-j)
			case i < j:
				v = s.leftAt(j-i-2) + 2*s.leftAt(j-i-1) + s.leftAt(j-i)
			default: // i == j
				v = s.topAt(0) + 2*int(s.corner) + s.leftAt(0)
			}
			d.ybr[y+j][x+i] = uint8((v + 2) >> 2)
		}
	}
}

// Mode 5: Vertical Right. Requires top, left, corner.
// zVR = 2*i - j controls which branch is used (spec 8.3.2.2.6).
func predIntra8x8VR(d *Decoder, y, x int, s *intra8x8Samples) {
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			zVR := 2*i - j
			var v int
			switch {
			case zVR >= 0 && zVR%2 == 0: // 0,2,4,6,8,10,12,14 — 2-tap
				idx := i - (j >> 1)
				v = s.topAt(idx-1) + s.topAt(idx) + 1
				d.ybr[y+j][x+i] = uint8(v >> 1)
				continue
			case zVR > 0 && zVR%2 == 1: // 1,3,5,7,9,11,13 — 3-tap top
				idx := i - (j >> 1)
				v = s.topAt(idx-2) + 2*s.topAt(idx-1) + s.topAt(idx) + 2
			case zVR == -1:
				v = s.leftAt(0) + 2*int(s.corner) + s.topAt(0) + 2
			default: // zVR in {-2, -3, -4, -5, -6, -7} — 3-tap left
				idx := j - 2*i - 1
				v = s.leftAt(idx) + 2*s.leftAt(idx-1) + s.leftAt(idx-2) + 2
			}
			d.ybr[y+j][x+i] = uint8(v >> 2)
		}
	}
}

// Mode 6: Horizontal Down. Requires top, left, corner.
// zHD = 2*j - i controls which branch (spec 8.3.2.2.7).
func predIntra8x8HD(d *Decoder, y, x int, s *intra8x8Samples) {
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			zHD := 2*j - i
			var v int
			switch {
			case zHD >= 0 && zHD%2 == 0: // 0,2,4,...,14 — 2-tap
				idx := j - (i >> 1)
				v = s.leftAt(idx-1) + s.leftAt(idx) + 1
				d.ybr[y+j][x+i] = uint8(v >> 1)
				continue
			case zHD > 0 && zHD%2 == 1: // 1,3,5,...,13 — 3-tap left
				idx := j - (i >> 1)
				v = s.leftAt(idx-2) + 2*s.leftAt(idx-1) + s.leftAt(idx) + 2
			case zHD == -1:
				v = s.leftAt(0) + 2*int(s.corner) + s.topAt(0) + 2
			default: // zHD in {-2, -3, -4, -5, -6, -7} — 3-tap top
				idx := i - 2*j - 1
				v = s.topAt(idx) + 2*s.topAt(idx-1) + s.topAt(idx-2) + 2
			}
			d.ybr[y+j][x+i] = uint8(v >> 2)
		}
	}
}

// Mode 7: Vertical Left. Requires top (and uses top[8..15] possibly replicated).
func predIntra8x8VL(d *Decoder, y, x int, s *intra8x8Samples) {
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			var v int
			if j%2 == 0 { // j even — 2-tap
				idx := i + (j >> 1)
				v = int(s.top[idx]) + int(s.top[idx+1]) + 1
				d.ybr[y+j][x+i] = uint8(v >> 1)
				continue
			}
			// j odd — 3-tap
			idx := i + (j >> 1)
			v = int(s.top[idx]) + 2*int(s.top[idx+1]) + int(s.top[idx+2]) + 2
			d.ybr[y+j][x+i] = uint8(v >> 2)
		}
	}
}

// Mode 8: Horizontal Up. Requires left samples.
// zHU = i + 2*j controls which branch (spec 8.3.2.2.10).
func predIntra8x8HU(d *Decoder, y, x int, s *intra8x8Samples) {
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			zHU := i + 2*j
			var v int
			switch {
			case zHU < 13 && zHU%2 == 0: // 0,2,4,6,8,10,12 — 2-tap
				idx := j + (i >> 1)
				v = int(s.left[idx]) + int(s.left[idx+1]) + 1
				d.ybr[y+j][x+i] = uint8(v >> 1)
				continue
			case zHU < 13 && zHU%2 == 1: // 1,3,5,7,9,11 — 3-tap
				idx := j + (i >> 1)
				v = int(s.left[idx]) + 2*int(s.left[idx+1]) + int(s.left[idx+2]) + 2
			case zHU == 13: // spec 8.3.2.2.10: (p'[-1,6] + 3*p'[-1,7] + 2) >> 2
				v = int(s.left[6]) + 3*int(s.left[7]) + 2
				d.ybr[y+j][x+i] = uint8(v >> 2)
				continue
			default: // zHU > 13 — all-bottom-left replicate
				d.ybr[y+j][x+i] = s.left[7]
				continue
			}
			d.ybr[y+j][x+i] = uint8(v >> 2)
		}
	}
}

// predIntra8x8Func is the 9-entry dispatch table keyed by the 4x4-compatible
// intra4x4* mode constants (which double as 8x8 mode numbers in the spec).
var predIntra8x8Func = [9]func(d *Decoder, y, x int, s *intra8x8Samples){
	predIntra8x8Vertical,
	predIntra8x8Horizontal,
	predIntra8x8DC,
	predIntra8x8DDL,
	predIntra8x8DDR,
	predIntra8x8VR,
	predIntra8x8HD,
	predIntra8x8VL,
	predIntra8x8HU,
}

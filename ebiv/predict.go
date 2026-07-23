package ebiv

// Intra prediction. The mode set is DC, vertical, horizontal, TrueMotion
// (Paeth), and six directional angles — VP9-class breadth (§3.4), with the
// angular modes built on an HEVC-style projected reference array.
//
// Encoder and decoder call predictIntra with identical arguments, so the
// prediction is deterministic and the two can never disagree: a mode that
// predicts poorly simply loses the encoder's rate-distortion race, it cannot
// desync the stream.

// planeView is a mutable window onto one image plane, used by both the encoder
// (writing reconstructed pixels) and the decoder. Intra prediction reads
// already-reconstructed neighbors through it.
type planeView struct {
	data   []byte
	stride int
	w, h   int
}

func (p planeView) at(x, y int) byte { return p.data[y*p.stride+x] }
func (p planeView) set(x, y int, v byte) {
	p.data[y*p.stride+x] = v
}

// Directional modes carry an angle in 1/32-pixel units and a family: vertical
// modes project onto the top row, horizontal modes onto the left column.
// invAngle is the HEVC reciprocal used to fold the far reference into the main
// one for negative angles; it is unused for non-negative angles.
type angMode struct {
	angle    int
	invAngle int
	vertical bool
}

var angModes = [numIntraModes]angMode{
	modeD45:  {angle: 32, vertical: true},                  // up-right 45°
	modeD27:  {angle: 13, vertical: true},                  // steep vertical-right
	modeD23:  {angle: -13, invAngle: -630, vertical: true}, // vertical-left
	modeD135: {angle: 32, vertical: false},                 // down-left 45°
	modeD117: {angle: 13, vertical: false},                 // shallow horizontal-down
	modeD113: {angle: -13, invAngle: -630, vertical: false},
}

// aboveRightAvailable reports whether the n samples past the top-right corner
// of a block are reconstructed, using only block geometry and tile bounds so
// encoder and decoder agree. Above-right lies in the row above: it is available
// when that row belongs to the fully-decoded macroblock-row above, or stays
// within the current macroblock's already-decoded region.
func aboveRightAvailable(x0, y0, n, tileTopPx, tileRightPx, mbTopPx, mbRightPx int) bool {
	if y0 <= tileTopPx || x0+2*n > tileRightPx {
		return false
	}
	if y0 == mbTopPx {
		return true
	}
	return x0+2*n <= mbRightPx
}

// predictIntra fills an n×n prediction block at (x0,y0). It gathers the
// reference samples (with substitution for unavailable ones) then dispatches by
// mode. pred is caller-owned scratch of at least n*n int32s.
func predictIntra(p planeView, x0, y0, n, mode int, avAbove, avLeft, avAboveRight bool, pred []int32) {
	// Combined reference array, HEVC layout:
	//   ref[0..2n-1] left column bottom-to-top, ref[2n] corner,
	//   ref[2n+1..4n] top row left-to-right (above then above-right).
	var ref [4*mbSize + 1]int32
	var avail [4*mbSize + 1]bool
	corner := 2 * n

	for i := 0; i < 2*n; i++ {
		// ref[i] maps to left-column row y0 + (2n-1-i); the lower n
		// (below-left) are never reconstructed in raster order.
		row := 2*n - 1 - i
		if avLeft && row < n {
			ref[i] = int32(p.at(x0-1, y0+row))
			avail[i] = true
		}
	}
	if avAbove && avLeft {
		ref[corner] = int32(p.at(x0-1, y0-1))
		avail[corner] = true
	}
	for j := 0; j < 2*n; j++ {
		if avAbove && (j < n || avAboveRight) {
			ref[corner+1+j] = int32(p.at(x0+j, y0-1))
			avail[corner+1+j] = true
		}
	}
	substituteRefs(ref[:4*n+1], avail[:4*n+1])

	topOf := func(x int) int32 { return ref[corner+1+x] }  // above sample x, 0-based
	leftOf := func(y int) int32 { return ref[corner-1-y] } // left sample row y

	switch mode {
	case modeV:
		for r := 0; r < n; r++ {
			for c := 0; c < n; c++ {
				pred[r*n+c] = topOf(c)
			}
		}
	case modeH:
		for r := 0; r < n; r++ {
			v := leftOf(r)
			for c := 0; c < n; c++ {
				pred[r*n+c] = v
			}
		}
	case modeTM:
		crn := ref[corner]
		for r := 0; r < n; r++ {
			l := leftOf(r)
			for c := 0; c < n; c++ {
				pred[r*n+c] = int32(clampByte(topOf(c) + l - crn))
			}
		}
	case modeDC:
		var sum int32
		for i := 0; i < n; i++ {
			sum += topOf(i) + leftOf(i)
		}
		dc := (sum + int32(n)) / int32(2*n)
		for i := 0; i < n*n; i++ {
			pred[i] = dc
		}
	default:
		angularPredict(ref[:], corner, n, angModes[mode], pred)
	}
}

// substituteRefs fills unavailable reference samples per HEVC 8.4.4.2.1: if none
// are available use the mid value, otherwise scan up filling each gap from its
// predecessor (and the leading gap from the first available sample).
func substituteRefs(ref []int32, avail []bool) {
	first := -1
	for i := range avail {
		if avail[i] {
			first = i
			break
		}
	}
	if first < 0 {
		for i := range ref {
			ref[i] = dcPred
		}
		return
	}
	for i := 0; i < first; i++ {
		ref[i] = ref[first]
	}
	for i := first + 1; i < len(ref); i++ {
		if !avail[i] {
			ref[i] = ref[i-1]
		}
	}
}

// angularPredict runs the HEVC angular predictor for one directional mode over
// the combined reference array. It works from a "main" reference (top for
// vertical modes, left for horizontal) extended into negative indices from the
// "side" reference when the angle is negative.
func angularPredict(ref []int32, corner, n int, m angMode, pred []int32) {
	// Build the main reference line main[0..2n] (main[0] = corner) and, for a
	// negative angle, extend it below zero into ext, offset by n.
	var ext [3*mbSize + 2]int32
	off := n
	if m.vertical {
		for k := 0; k <= 2*n; k++ {
			ext[off+k] = ref[corner+k] // corner then above/above-right
		}
	} else {
		for k := 0; k <= 2*n; k++ {
			ext[off+k] = ref[corner-k] // corner then left/below-left
		}
	}
	if m.angle < 0 {
		low := (n * m.angle) >> 5
		for x := -1; x >= low; x-- {
			idx := (x*m.invAngle + 128) >> 8 // >= 0
			if m.vertical {
				ext[off+x] = ref[corner-idx]
			} else {
				ext[off+x] = ref[corner+idx]
			}
		}
	}

	for a := 0; a < n; a++ { // a = row (vertical) or column (horizontal)
		pos := (a + 1) * m.angle
		idx := pos >> 5
		fact := pos & 31
		for b := 0; b < n; b++ { // b = column (vertical) or row (horizontal)
			s0 := ext[off+b+idx+1]
			s1 := ext[off+b+idx+2]
			v := ((32-int32(fact))*s0 + int32(fact)*s1 + 16) >> 5
			if m.vertical {
				pred[a*n+b] = v
			} else {
				pred[b*n+a] = v
			}
		}
	}
}

// reconstruct adds a residual block to a prediction and writes clamped pixels
// back into the plane. The destination row is sliced once with a three-index
// expression so the compiler proves the inner store is in range and drops the
// per-pixel bounds check (§6.2).
func reconstruct(p planeView, x0, y0, n int, pred, residual []int32) {
	for r := 0; r < n; r++ {
		base := (y0+r)*p.stride + x0
		dst := p.data[base : base+n : base+n]
		pr := pred[r*n : r*n+n : r*n+n]
		rs := residual[r*n : r*n+n : r*n+n]
		for c := 0; c < n; c++ {
			dst[c] = clampByte(pr[c] + rs[c])
		}
	}
}

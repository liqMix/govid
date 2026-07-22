package h264

// H.264 intra prediction functions for I-frames.
// Spec section 8.3.1 (Intra_4x4), 8.3.2 (Intra_16x16), 8.3.3 (Intra_Chroma).

// Intra 4x4 prediction mode constants.
const (
	intra4x4Vertical      = 0
	intra4x4Horizontal    = 1
	intra4x4DC            = 2
	intra4x4DiagDownLeft  = 3
	intra4x4DiagDownRight = 4
	intra4x4VerticalRight = 5
	intra4x4HorizDown     = 6
	intra4x4VerticalLeft  = 7
	intra4x4HorizUp       = 8
)

// Intra 16x16 prediction mode constants.
const (
	intra16x16Vertical   = 0
	intra16x16Horizontal = 1
	intra16x16DC         = 2
	intra16x16Plane      = 3
)

// Intra chroma prediction mode constants.
const (
	intraChromaDC         = 0
	intraChromaHorizontal = 1
	intraChromaVertical   = 2
	intraChromaPlane      = 3
)

// Intra 4x4 prediction functions.
// Each function writes predicted values into the 4x4 block at (y,x) in d.ybr.
// Neighboring samples are at ybr[y-1][x..x+7] (above) and ybr[y..y+3][x-1] (left).

func predIntra4x4Vertical(d *Decoder, y, x int) {
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			d.ybr[y+j][x+i] = d.ybr[y-1][x+i]
		}
	}
}

func predIntra4x4Horizontal(d *Decoder, y, x int) {
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			d.ybr[y+j][x+i] = d.ybr[y+j][x-1]
		}
	}
}

func predIntra4x4DC(d *Decoder, y, x int) {
	// Determine neighbor availability per spec 8.3.1.2.3.
	// Above is available if this block is not at the top frame edge.
	// Left is available if this block is not at the left frame edge.
	blockRow := (y - ybrYY) / 4
	blockCol := (x - ybrYX) / 4
	hasAbove := blockRow > 0 || d.mbAvailable(d.curMB[0], d.curMB[1]-1)
	hasLeft := blockCol > 0 || d.mbAvailable(d.curMB[0]-1, d.curMB[1])

	var avg uint8
	switch {
	case hasAbove && hasLeft:
		sum := 0
		for i := 0; i < 4; i++ {
			sum += int(d.ybr[y-1][x+i])
		}
		for j := 0; j < 4; j++ {
			sum += int(d.ybr[y+j][x-1])
		}
		avg = uint8((sum + 4) >> 3)
	case hasAbove:
		sum := 0
		for i := 0; i < 4; i++ {
			sum += int(d.ybr[y-1][x+i])
		}
		avg = uint8((sum + 2) >> 2)
	case hasLeft:
		sum := 0
		for j := 0; j < 4; j++ {
			sum += int(d.ybr[y+j][x-1])
		}
		avg = uint8((sum + 2) >> 2)
	default:
		avg = 128
	}
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			d.ybr[y+j][x+i] = avg
		}
	}
}

func predIntra4x4DiagDownLeft(d *Decoder, y, x int) {
	t := &d.ybr[y-1]
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			if i+j == 6 {
				d.ybr[y+j][x+i] = uint8((int(t[x+6]) + 3*int(t[x+7]) + 2) >> 2)
			} else {
				d.ybr[y+j][x+i] = uint8((int(t[x+i+j]) + 2*int(t[x+i+j+1]) + int(t[x+i+j+2]) + 2) >> 2)
			}
		}
	}
}

func predIntra4x4DiagDownRight(d *Decoder, y, x int) {
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			if i > j {
				d.ybr[y+j][x+i] = uint8((int(d.ybr[y-1][x+i-j-2]) + 2*int(d.ybr[y-1][x+i-j-1]) + int(d.ybr[y-1][x+i-j]) + 2) >> 2)
			} else if i < j {
				d.ybr[y+j][x+i] = uint8((int(d.ybr[y+j-i-2][x-1]) + 2*int(d.ybr[y+j-i-1][x-1]) + int(d.ybr[y+j-i][x-1]) + 2) >> 2)
			} else {
				d.ybr[y+j][x+i] = uint8((int(d.ybr[y-1][x]) + 2*int(d.ybr[y-1][x-1]) + int(d.ybr[y][x-1]) + 2) >> 2)
			}
		}
	}
}

func predIntra4x4VerticalRight(d *Decoder, y, x int) {
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			zVR := 2*i - j
			switch {
			case zVR >= 0 && zVR%2 == 0:
				idx := i - (j >> 1)
				d.ybr[y+j][x+i] = uint8((int(d.ybr[y-1][x+idx-1]) + int(d.ybr[y-1][x+idx]) + 1) >> 1)
			case zVR >= 0 && zVR%2 == 1:
				idx := i - (j >> 1)
				d.ybr[y+j][x+i] = uint8((int(d.ybr[y-1][x+idx-2]) + 2*int(d.ybr[y-1][x+idx-1]) + int(d.ybr[y-1][x+idx]) + 2) >> 2)
			case zVR == -1:
				d.ybr[y+j][x+i] = uint8((int(d.ybr[y][x-1]) + 2*int(d.ybr[y-1][x-1]) + int(d.ybr[y-1][x]) + 2) >> 2)
			default: // zVR < -1
				d.ybr[y+j][x+i] = uint8((int(d.ybr[y+j-1][x-1]) + 2*int(d.ybr[y+j-2][x-1]) + int(d.ybr[y+j-3][x-1]) + 2) >> 2)
			}
		}
	}
}

func predIntra4x4HorizDown(d *Decoder, y, x int) {
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			zHD := 2*j - i
			switch {
			case zHD >= 0 && zHD%2 == 0:
				idx := j - (i >> 1)
				d.ybr[y+j][x+i] = uint8((int(d.ybr[y+idx][x-1]) + int(d.ybr[y+idx-1][x-1]) + 1) >> 1)
			case zHD >= 0 && zHD%2 == 1:
				idx := j - (i >> 1)
				d.ybr[y+j][x+i] = uint8((int(d.ybr[y+idx-2][x-1]) + 2*int(d.ybr[y+idx-1][x-1]) + int(d.ybr[y+idx][x-1]) + 2) >> 2)
			case zHD == -1:
				d.ybr[y+j][x+i] = uint8((int(d.ybr[y-1][x]) + 2*int(d.ybr[y-1][x-1]) + int(d.ybr[y][x-1]) + 2) >> 2)
			default: // zHD < -1
				d.ybr[y+j][x+i] = uint8((int(d.ybr[y-1][x+i-1]) + 2*int(d.ybr[y-1][x+i-2]) + int(d.ybr[y-1][x+i-3]) + 2) >> 2)
			}
		}
	}
}

func predIntra4x4VerticalLeft(d *Decoder, y, x int) {
	t := &d.ybr[y-1]
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			idx := i + (j >> 1)
			if j%2 == 0 {
				d.ybr[y+j][x+i] = uint8((int(t[x+idx]) + int(t[x+idx+1]) + 1) >> 1)
			} else {
				d.ybr[y+j][x+i] = uint8((int(t[x+idx]) + 2*int(t[x+idx+1]) + int(t[x+idx+2]) + 2) >> 2)
			}
		}
	}
}

func predIntra4x4HorizUp(d *Decoder, y, x int) {
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			zHU := i + 2*j
			switch {
			case zHU < 5:
				idx := j + (i >> 1)
				if i%2 == 0 {
					d.ybr[y+j][x+i] = uint8((int(d.ybr[y+idx][x-1]) + int(d.ybr[y+idx+1][x-1]) + 1) >> 1)
				} else {
					d.ybr[y+j][x+i] = uint8((int(d.ybr[y+idx][x-1]) + 2*int(d.ybr[y+idx+1][x-1]) + int(d.ybr[y+idx+2][x-1]) + 2) >> 2)
				}
			case zHU == 5:
				d.ybr[y+j][x+i] = uint8((int(d.ybr[y+2][x-1]) + 3*int(d.ybr[y+3][x-1]) + 2) >> 2)
			default:
				d.ybr[y+j][x+i] = d.ybr[y+3][x-1]
			}
		}
	}
}

// predIntra4x4 dispatches the appropriate 4x4 prediction function.
var predIntra4x4Func = [9]func(*Decoder, int, int){
	predIntra4x4Vertical,
	predIntra4x4Horizontal,
	predIntra4x4DC,
	predIntra4x4DiagDownLeft,
	predIntra4x4DiagDownRight,
	predIntra4x4VerticalRight,
	predIntra4x4HorizDown,
	predIntra4x4VerticalLeft,
	predIntra4x4HorizUp,
}

// Intra 16x16 prediction functions.
// Write predicted values into the 16x16 luma block at (y,x) in d.ybr.

func predIntra16x16Vertical(d *Decoder, y, x int) {
	for j := 0; j < 16; j++ {
		for i := 0; i < 16; i++ {
			d.ybr[y+j][x+i] = d.ybr[y-1][x+i]
		}
	}
}

func predIntra16x16Horizontal(d *Decoder, y, x int) {
	for j := 0; j < 16; j++ {
		for i := 0; i < 16; i++ {
			d.ybr[y+j][x+i] = d.ybr[y+j][x-1]
		}
	}
}

func predIntra16x16DC(d *Decoder, y, x int) {
	hasAbove := d.mbAvailable(d.curMB[0], d.curMB[1]-1)
	hasLeft := d.mbAvailable(d.curMB[0]-1, d.curMB[1])

	var avg uint8
	switch {
	case hasAbove && hasLeft:
		sum := 0
		for i := 0; i < 16; i++ {
			sum += int(d.ybr[y-1][x+i])
		}
		for j := 0; j < 16; j++ {
			sum += int(d.ybr[y+j][x-1])
		}
		avg = uint8((sum + 16) >> 5)
	case hasAbove:
		sum := 0
		for i := 0; i < 16; i++ {
			sum += int(d.ybr[y-1][x+i])
		}
		avg = uint8((sum + 8) >> 4)
	case hasLeft:
		sum := 0
		for j := 0; j < 16; j++ {
			sum += int(d.ybr[y+j][x-1])
		}
		avg = uint8((sum + 8) >> 4)
	default:
		avg = 128
	}
	for j := 0; j < 16; j++ {
		for i := 0; i < 16; i++ {
			d.ybr[y+j][x+i] = avg
		}
	}
}

func predIntra16x16Plane(d *Decoder, y, x int) {
	// Plane prediction per spec 8.3.3.4.
	H := 0
	for i := 1; i <= 8; i++ {
		H += i * (int(d.ybr[y-1][x+7+i]) - int(d.ybr[y-1][x+7-i]))
	}
	V := 0
	for j := 1; j <= 8; j++ {
		V += j * (int(d.ybr[y+7+j][x-1]) - int(d.ybr[y+7-j][x-1]))
	}
	a := 16 * (int(d.ybr[y-1][x+15]) + int(d.ybr[y+15][x-1]))
	b := (5*H + 32) >> 6
	c := (5*V + 32) >> 6
	for j := 0; j < 16; j++ {
		for i := 0; i < 16; i++ {
			val := (a + b*(i-7) + c*(j-7) + 16) >> 5
			if val < 0 {
				val = 0
			} else if val > 255 {
				val = 255
			}
			d.ybr[y+j][x+i] = uint8(val)
		}
	}
}

var predIntra16x16Func = [4]func(*Decoder, int, int){
	predIntra16x16Vertical,
	predIntra16x16Horizontal,
	predIntra16x16DC,
	predIntra16x16Plane,
}

// Intra chroma prediction functions (8x8).
func predIntraChromaDC(d *Decoder, y, x int) {
	// Chroma DC prediction per spec Section 8.3.4.1.
	// Each 4x4 sub-block uses only MB-level border samples (not inner borders).
	// The above border is ybr[y-1][x..x+7], left border is ybr[y..y+7][x-1].
	hasAbove := d.mbAvailable(d.curMB[0], d.curMB[1]-1)
	hasLeft := d.mbAvailable(d.curMB[0]-1, d.curMB[1])

	// Precompute border sums for each half.
	var aboveSum [2]int // [0]=cols 0-3, [1]=cols 4-7
	var leftSum [2]int  // [0]=rows 0-3, [1]=rows 4-7
	if hasAbove {
		for i := 0; i < 4; i++ {
			aboveSum[0] += int(d.ybr[y-1][x+i])
			aboveSum[1] += int(d.ybr[y-1][x+4+i])
		}
	}
	if hasLeft {
		for j := 0; j < 4; j++ {
			leftSum[0] += int(d.ybr[y+j][x-1])
			leftSum[1] += int(d.ybr[y+4+j][x-1])
		}
	}

	// Compute prediction for each 4x4 sub-block.
	// Sub-block indices: (0,0)=top-left, (0,1)=top-right, (1,0)=bot-left, (1,1)=bot-right.
	var pred [2][2]uint8
	switch {
	case hasAbove && hasLeft:
		pred[0][0] = uint8((aboveSum[0] + leftSum[0] + 4) >> 3)
		pred[0][1] = uint8((aboveSum[1] + 2) >> 2)
		pred[1][0] = uint8((leftSum[1] + 2) >> 2)
		pred[1][1] = uint8((aboveSum[1] + leftSum[1] + 4) >> 3)
	case hasAbove:
		pred[0][0] = uint8((aboveSum[0] + 2) >> 2)
		pred[0][1] = uint8((aboveSum[1] + 2) >> 2)
		pred[1][0] = uint8((aboveSum[0] + 2) >> 2)
		pred[1][1] = uint8((aboveSum[1] + 2) >> 2)
	case hasLeft:
		pred[0][0] = uint8((leftSum[0] + 2) >> 2)
		pred[0][1] = uint8((leftSum[0] + 2) >> 2)
		pred[1][0] = uint8((leftSum[1] + 2) >> 2)
		pred[1][1] = uint8((leftSum[1] + 2) >> 2)
	default:
		pred[0][0] = 128
		pred[0][1] = 128
		pred[1][0] = 128
		pred[1][1] = 128
	}

	for by := 0; by < 2; by++ {
		for bx := 0; bx < 2; bx++ {
			val := pred[by][bx]
			sy := y + by*4
			sx := x + bx*4
			for j := 0; j < 4; j++ {
				for i := 0; i < 4; i++ {
					d.ybr[sy+j][sx+i] = val
				}
			}
		}
	}
}

func predIntraChromaHorizontal(d *Decoder, y, x int) {
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			d.ybr[y+j][x+i] = d.ybr[y+j][x-1]
		}
	}
}

func predIntraChromaVertical(d *Decoder, y, x int) {
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			d.ybr[y+j][x+i] = d.ybr[y-1][x+i]
		}
	}
}

func predIntraChromaPlane(d *Decoder, y, x int) {
	H := 0
	for i := 1; i <= 4; i++ {
		H += i * (int(d.ybr[y-1][x+3+i]) - int(d.ybr[y-1][x+3-i]))
	}
	V := 0
	for j := 1; j <= 4; j++ {
		V += j * (int(d.ybr[y+3+j][x-1]) - int(d.ybr[y+3-j][x-1]))
	}
	a := 16 * (int(d.ybr[y-1][x+7]) + int(d.ybr[y+7][x-1]))
	b := (17*H + 16) >> 5
	c := (17*V + 16) >> 5
	for j := 0; j < 8; j++ {
		for i := 0; i < 8; i++ {
			val := (a + b*(i-3) + c*(j-3) + 16) >> 5
			if val < 0 {
				val = 0
			} else if val > 255 {
				val = 255
			}
			d.ybr[y+j][x+i] = uint8(val)
		}
	}
}

var predIntraChromaFunc = [4]func(*Decoder, int, int){
	predIntraChromaDC,
	predIntraChromaHorizontal,
	predIntraChromaVertical,
	predIntraChromaPlane,
}

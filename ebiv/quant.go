package ebiv

import (
	"math"
	"math/bits"
)

// Scalar quantization. Coefficients are orthonormal-DCT values (pixel-scale),
// so one geometric step table spans near-lossless to coarse. The step doubles
// every six QP values, matching the familiar H.264 progression.
//
// Encoding uses a dead zone: the rounding bias is pulled below one half so
// small coefficients quantize to zero more readily, which costs little quality
// and saves substantial rate. Decoding is a plain multiply — the whole point of
// pushing cost to the encoder.

const maxQP = 63

var quantStep [maxQP + 1]int32

func init() {
	for qp := 0; qp <= maxQP; qp++ {
		step := math.Round(math.Pow(2, float64(qp)/6.0))
		if step < 1 {
			step = 1
		}
		quantStep[qp] = int32(step)
	}
}

// quantizer holds the step sizes derived from a frame's QP. DC is quantized one
// step finer than AC, since DC error is the most visible as blocking.
type quantizer struct {
	dc int32
	ac int32
}

func newQuantizer(qp int) quantizer {
	if qp < 0 {
		qp = 0
	}
	if qp > maxQP {
		qp = maxQP
	}
	ac := quantStep[qp]
	dc := ac
	if qp >= 6 {
		dc = quantStep[qp-6] // one octave finer
	}
	return quantizer{dc: dc, ac: ac}
}

// quantize maps a coefficient block to integer levels in place-free form: it
// reads coeffs and writes levels. Position 0 (DC) uses the DC step.
func (q quantizer) quantize(coeffs, levels []int32, n int) {
	for i := 0; i < n*n; i++ {
		step := q.ac
		if i == 0 {
			step = q.dc
		}
		c := coeffs[i]
		// Dead-zone rounding: bias 3/8 of a step instead of 1/2.
		if c >= 0 {
			levels[i] = (c*8 + 3*step) / (step * 8)
		} else {
			levels[i] = -((-c*8 + 3*step) / (step * 8))
		}
	}
}

// dequantize reconstructs coefficients from levels.
func (q quantizer) dequantize(levels, coeffs []int32, n int) {
	for i := 0; i < n*n; i++ {
		step := q.ac
		if i == 0 {
			step = q.dc
		}
		coeffs[i] = levels[i] * step
	}
}

// costModel holds the real per-context, per-symbol coding cost in bits
// (−log2(freq/M)) measured from a first encoding pass. RDOQ and — later — the
// mode decisions price against it instead of a fixed guess.
type costModel [][]float64

func (c costModel) bits(ctx, sym int) float64 { return c[ctx][sym] }

// quantizeRDOQ is rate-distortion-optimized quantization (M2): rather than a
// fixed dead-zone rounding, it chooses each coefficient's level and the block's
// end-of-block position to minimize distortion + lambda·rate, pricing rate with
// the real token costs from the first pass. Because the DCT is orthonormal,
// coefficient-domain squared error equals pixel SSE (Parseval), so distortion is
// directly comparable to lambda, which is calibrated for pixel SSE. This is
// encoder-only: the decoder dequantizes the resulting levels exactly as before,
// so the bitstream and decoder are unchanged.
//
// It attacks the audit's largest lever — trailing ±1 coefficients, 31.5% of
// coefficient bits — by trimming the block wherever the coding cost of the tail
// exceeds the distortion of dropping it, and by rounding coefficients toward
// zero when the real bits saved outweigh the added distortion.
func (q quantizer) quantizeRDOQ(coeffs, levels []int32, n, plane, txIdx int, lambda float64, cost costModel) {
	scan := scanOrders[n]
	total := n * n
	signBits := (cost.bits(ctxSign, 0) + cost.bits(ctxSign, 1)) * 0.5

	// Step 1: choose each coefficient's level in scan order (so the token
	// context's previous-class evolves as it will when coded), recording the
	// per-position context, chosen level, distortion, and non-zero rate.
	var (
		ctxScan [maxLevels]int   // token context per scan position
		lvlScan [maxLevels]int32 // signed chosen level
		dScan   [maxLevels]float64
		rNZScan [maxLevels]float64 // rate if this position is coded non-zero
	)
	prevClass := 0
	for si := 0; si < total; si++ {
		nat := scan[si]
		step := float64(q.ac)
		if nat == 0 {
			step = float64(q.dc)
		}
		c := coeffs[nat]
		ac := float64(abs32(c))
		ctx := tokenCtx(plane, txIdx, bandOf(si), prevClass)
		ctxScan[si] = ctx

		l0 := int((ac + step/2) / step) // nearest
		bestMag := 0
		bestD := ac * ac // level 0
		bestCost := bestD
		bestR := 0.0
		for _, cand := range [2]int{l0 - 1, l0} {
			if cand < 1 {
				continue
			}
			d := ac - float64(cand)*step
			d *= d
			r := coeffRate(cand, ctx, cost) + signBits
			if cst := d + lambda*r; cst < bestCost {
				bestCost, bestMag, bestD, bestR = cst, cand, d, r
			}
		}
		dScan[si] = bestD
		rNZScan[si] = bestR
		if bestMag > 0 {
			lv := int32(bestMag)
			if c < 0 {
				lv = -lv
			}
			lvlScan[si] = lv
			prevClass = classForMag(bestMag)
		} else {
			lvlScan[si] = 0
			prevClass = 0
		}
	}

	// Step 2: end-of-block trimming. Find the truncation position T minimizing
	// distortion + lambda·rate; everything past it is forced to zero.
	var suffixEnergy [maxLevels + 1]float64
	for i := total - 1; i >= 0; i-- {
		c := float64(coeffs[scan[i]])
		suffixEnergy[i] = suffixEnergy[i+1] + c*c
	}
	lastNZ := -1
	for si := 0; si < total; si++ {
		if lvlScan[si] != 0 {
			lastNZ = si
		}
	}
	// T = −1: whole block zero, one EOB token in the DC context.
	bestT := -1
	bestCost := suffixEnergy[0] + lambda*cost.bits(ctxScan[0], tEOB)
	prefD, prefR := 0.0, 0.0
	for t := 0; t <= lastNZ; t++ {
		prefD += dScan[t]
		if lvlScan[t] != 0 {
			prefR += rNZScan[t]
		} else {
			prefR += cost.bits(ctxScan[t], tZero)
		}
		eob := 0.0
		if t < total-1 {
			eob = cost.bits(ctxScan[t+1], tEOB)
		}
		if cst := prefD + suffixEnergy[t+1] + lambda*(prefR+eob); cst < bestCost {
			bestCost, bestT = cst, t
		}
	}
	for si := 0; si < total; si++ {
		if si <= bestT {
			levels[scan[si]] = lvlScan[si]
		} else {
			levels[scan[si]] = 0
		}
	}
}

// coeffRate is the real cost in bits of coding a magnitude at a token context:
// the magnitude token plus, for escapes, the class and suffix bits.
func coeffRate(m, ctx int, cost costModel) float64 {
	if m <= 4 {
		return cost.bits(ctx, tZero+m) // tOne..tFour
	}
	v := m - 5
	return cost.bits(ctx, tEscape) + cost.bits(ctxEscClass, bits.Len(uint(v))) + float64(max(0, bits.Len(uint(v))-1))
}

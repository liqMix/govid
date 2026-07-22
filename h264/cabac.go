package h264

import "fmt"

// CABAC arithmetic decoding engine, spec 9.3.
//
// The engine reads bits from the slice-data BitReader (which operates on RBSP
// bytes with emulation prevention already removed). Context variables use the
// spec convention: pStateIdx 0..63 plus valMPS, transitioned via
// transIdxMPS / transIdxLPS and rangeTabLPS (cabac_tables.go).

type cabacCtx struct {
	state uint8 // pStateIdx, 0..63
	mps   uint8 // valMPS, 0 or 1
}

type cabacDecoder struct {
	br         *BitReader
	codIRange  uint32
	codIOffset uint32
	ctx        [1024]cabacCtx
	err        error // sticky bitstream error
}

// initContexts initializes all context variables per spec 9.3.1.1 using the
// (m, n) tables selected by slice type and cabac_init_idc, at SliceQPY.
func (cd *cabacDecoder) initContexts(sliceType uint32, cabacInitIdc int, qp int) {
	var tab *[1024][2]int8
	if sliceType == sliceTypeI || sliceType == sliceTypeSI {
		tab = &cabacContextInitI
	} else {
		tab = &cabacContextInitPB[cabacInitIdc]
	}
	if qp < 0 {
		qp = 0
	} else if qp > 51 {
		qp = 51
	}
	for i := range cd.ctx {
		m, n := int(tab[i][0]), int(tab[i][1])
		pre := clampInt(((m*qp)>>4)+n, 1, 126)
		if pre <= 63 {
			cd.ctx[i] = cabacCtx{state: uint8(63 - pre), mps: 0}
		} else {
			cd.ctx[i] = cabacCtx{state: uint8(pre - 64), mps: 1}
		}
	}
}

// initEngine initializes the arithmetic decoding engine (spec 9.3.1.2).
// The BitReader must already be positioned after cabac_alignment_one_bit.
func (cd *cabacDecoder) initEngine() {
	cd.codIRange = 510
	v, err := cd.br.ReadBits(9)
	if err != nil {
		cd.err = err
		return
	}
	cd.codIOffset = v
}

func (cd *cabacDecoder) readBit() uint32 {
	if cd.err != nil {
		return 0
	}
	v, err := cd.br.ReadBits(1)
	if err != nil {
		// A conforming stream never underruns mid-decode; remember the error
		// and let the MB loop surface it.
		cd.err = err
		return 0
	}
	return v
}

// decodeDecision decodes one bin with the context variable at ctxIdx
// (spec 9.3.3.2.1) including renormalization.
func (cd *cabacDecoder) decodeDecision(ctxIdx int) int {
	c := &cd.ctx[ctxIdx]
	qIdx := (cd.codIRange >> 6) & 3
	lps := uint32(rangeTabLPS[c.state][qIdx])
	cd.codIRange -= lps

	var bin int
	if cd.codIOffset >= cd.codIRange {
		bin = int(1 - c.mps)
		cd.codIOffset -= cd.codIRange
		cd.codIRange = lps
		if c.state == 0 {
			c.mps = 1 - c.mps
		}
		c.state = transIdxLPS[c.state]
	} else {
		bin = int(c.mps)
		c.state = transIdxMPS[c.state]
	}

	for cd.codIRange < 256 {
		cd.codIRange <<= 1
		cd.codIOffset = cd.codIOffset<<1 | cd.readBit()
	}
	return bin
}

// decodeBypass decodes one bin with the bypass process (spec 9.3.3.2.3).
func (cd *cabacDecoder) decodeBypass() int {
	cd.codIOffset = cd.codIOffset<<1 | cd.readBit()
	if cd.codIOffset >= cd.codIRange {
		cd.codIOffset -= cd.codIRange
		return 1
	}
	return 0
}

// decodeBypassSign returns +mag or -mag based on one bypass sign bin
// (sign bin 1 means negative, spec 9.3.3.2.3 / coeff_sign_flag).
func (cd *cabacDecoder) decodeBypassSign(mag int) int {
	if cd.decodeBypass() == 1 {
		return -mag
	}
	return mag
}

// decodeTerminate decodes the special end-of-slice / I_PCM bin
// (spec 9.3.3.2.4). Renormalization only happens when the bin is 0.
func (cd *cabacDecoder) decodeTerminate() int {
	cd.codIRange -= 2
	if cd.codIOffset >= cd.codIRange {
		return 1
	}
	for cd.codIRange < 256 {
		cd.codIRange <<= 1
		cd.codIOffset = cd.codIOffset<<1 | cd.readBit()
	}
	return 0
}

// checkErr surfaces a sticky bitstream error, if any.
func (cd *cabacDecoder) checkErr() error {
	if cd.err != nil {
		return fmt.Errorf("cabac: bitstream underrun: %w", cd.err)
	}
	return nil
}

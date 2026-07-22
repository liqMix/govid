package h264

// Scaling matrix support (spec 7.3.2.1.1 scaling_list(), Table 7-2 fall-back
// rules, Tables 7-3/7-4 default matrices). The active weightScale matrices
// feed inverse quantization (8.5.9): six 4x4 lists (Intra Y/Cb/Cr, Inter
// Y/Cb/Cr) and two 8x8 luma lists (Intra, Inter). Matrices here are stored in
// raster order; the bitstream codes them in zigzag scan order.

// scalingListEntry is one scaling_list() syntax element as parsed from an SPS
// or PPS: absent, "use default" (first delta_scale drives next_scale to 0), or
// explicit values in scan order.
type scalingListEntry struct {
	Present    bool
	UseDefault bool
	Scan       [64]uint8 // scan-order values; first 16 used for 4x4 lists
}

// parseScalingList reads one scaling_list() of the given size (16 or 64).
func parseScalingList(br *BitReader, e *scalingListEntry, size int) error {
	e.Present = true
	lastScale, nextScale := 8, 8
	for j := 0; j < size; j++ {
		if nextScale != 0 {
			delta, err := br.ReadSE()
			if err != nil {
				return err
			}
			nextScale = (lastScale + int(delta) + 256) % 256
			if j == 0 && nextScale == 0 {
				e.UseDefault = true
			}
		}
		if nextScale != 0 {
			e.Scan[j] = uint8(nextScale)
			lastScale = nextScale
		} else {
			e.Scan[j] = uint8(lastScale)
		}
	}
	return nil
}

// Default scaling matrices, raster order (spec Tables 7-3 and 7-4).
var default4x4Intra = [16]int{
	6, 13, 20, 28,
	13, 20, 28, 32,
	20, 28, 32, 37,
	28, 32, 37, 42,
}

var default4x4Inter = [16]int{
	10, 14, 20, 24,
	14, 20, 24, 27,
	20, 24, 27, 30,
	24, 27, 30, 34,
}

var default8x8Intra = [64]int{
	6, 10, 13, 16, 18, 23, 25, 27,
	10, 11, 16, 18, 23, 25, 27, 29,
	13, 16, 18, 23, 25, 27, 29, 31,
	16, 18, 23, 25, 27, 29, 31, 33,
	18, 23, 25, 27, 29, 31, 33, 36,
	23, 25, 27, 29, 31, 33, 36, 38,
	25, 27, 29, 31, 33, 36, 38, 40,
	27, 29, 31, 33, 36, 38, 40, 42,
}

var default8x8Inter = [64]int{
	9, 13, 15, 17, 19, 21, 22, 24,
	13, 13, 17, 19, 21, 22, 24, 25,
	15, 17, 19, 21, 22, 24, 25, 27,
	17, 19, 21, 22, 24, 25, 27, 28,
	19, 21, 22, 24, 25, 27, 28, 30,
	21, 22, 24, 25, 27, 28, 30, 32,
	22, 24, 25, 27, 28, 30, 32, 33,
	24, 25, 27, 28, 30, 32, 33, 35,
}

var flatScaling4x4 = func() (t [16]int) {
	for i := range t {
		t[i] = 16
	}
	return
}()

var flatScaling8x8 = func() (t [64]int) {
	for i := range t {
		t[i] = 16
	}
	return
}()

// updateScalingMatrices derives the active weightScale matrices for the given
// SPS/PPS pair. SPS lists resolve with fall-back rule A (defaults / previous
// list); PPS lists resolve with fall-back rule B (SPS result / previous list).
// Only 4:2:0 list layouts are handled (8 SPS lists, up to 8 PPS lists).
func (d *Decoder) updateScalingMatrices(sps *SPS, pps *PPS) {
	d.scalingSPS, d.scalingPPS = sps, pps

	rasterize4 := func(e *scalingListEntry) (t [16]int) {
		for k := 0; k < 16; k++ {
			t[zigzagToRaster[k]] = int(e.Scan[k])
		}
		return
	}
	rasterize8 := func(e *scalingListEntry) (t [64]int) {
		for k := 0; k < 64; k++ {
			t[zigzagToRaster8x8[k]] = int(e.Scan[k])
		}
		return
	}
	def4 := func(i int) [16]int {
		if i < 3 {
			return default4x4Intra
		}
		return default4x4Inter
	}
	def8 := func(i int) [64]int {
		if i == 6 {
			return default8x8Intra
		}
		return default8x8Inter
	}

	// SPS level, fall-back rule A.
	var s4 [6][16]int
	var s8 [2][64]int
	if sps.SeqScalingMatrixPresent {
		for i := 0; i < 6; i++ {
			e := &sps.ScalingLists[i]
			switch {
			case !e.Present:
				if i == 0 || i == 3 {
					s4[i] = def4(i)
				} else {
					s4[i] = s4[i-1]
				}
			case e.UseDefault:
				s4[i] = def4(i)
			default:
				s4[i] = rasterize4(e)
			}
		}
		for i := 6; i < 8; i++ {
			e := &sps.ScalingLists[i]
			if !e.Present || e.UseDefault {
				s8[i-6] = def8(i)
			} else {
				s8[i-6] = rasterize8(e)
			}
		}
	} else {
		for i := range s4 {
			s4[i] = flatScaling4x4
		}
		s8[0], s8[1] = flatScaling8x8, flatScaling8x8
	}

	// PPS level, fall-back rule B (rule A shapes when no SPS matrices).
	p4 := s4
	p8 := s8
	if pps.PicScalingMatrixPresent {
		for i := 0; i < 6; i++ {
			e := &pps.ScalingLists[i]
			switch {
			case !e.Present:
				if i == 0 || i == 3 {
					if !sps.SeqScalingMatrixPresent {
						p4[i] = def4(i)
					} // else keep the SPS result already in p4[i]
				} else {
					p4[i] = p4[i-1]
				}
			case e.UseDefault:
				p4[i] = def4(i)
			default:
				p4[i] = rasterize4(e)
			}
		}
		if pps.Transform8x8Mode {
			for i := 6; i < 8; i++ {
				e := &pps.ScalingLists[i]
				switch {
				case !e.Present:
					if !sps.SeqScalingMatrixPresent {
						p8[i-6] = def8(i)
					}
				case e.UseDefault:
					p8[i-6] = def8(i)
				default:
					p8[i-6] = rasterize8(e)
				}
			}
		}
	}
	d.scalingWS4 = p4
	d.scalingWS8 = p8
}

// wsLuma4 returns the active 4x4 luma weight matrix (list 0 intra, 3 inter).
func (d *Decoder) wsLuma4(intra bool) *[16]int {
	if intra {
		return &d.scalingWS4[0]
	}
	return &d.scalingWS4[3]
}

// wsChroma4 returns the active 4x4 chroma weight matrix
// (lists 1/2 intra Cb/Cr, 4/5 inter Cb/Cr).
func (d *Decoder) wsChroma4(intra, cr bool) *[16]int {
	i := 1
	if !intra {
		i = 4
	}
	if cr {
		i++
	}
	return &d.scalingWS4[i]
}

// wsLuma8 returns the active 8x8 luma weight matrix (intra or inter).
func (d *Decoder) wsLuma8(intra bool) *[64]int {
	if intra {
		return &d.scalingWS8[0]
	}
	return &d.scalingWS8[1]
}

package h264

import "image"

// Decoded Picture Buffer (DPB) management for reference frames.
// Baseline profile uses sliding window eviction only (no MMCO).

func (d *Decoder) initDPB() {
	maxRef := 1
	if d.activeSPS != nil && d.activeSPS.MaxNumRefFrames > 0 {
		maxRef = int(d.activeSPS.MaxNumRefFrames)
	}
	if d.refFrames == nil {
		d.refFrames = make([]*image.YCbCr, 0, maxRef)
	}
}

func (d *Decoder) storeRefFrame() {
	maxRef := 1
	if d.activeSPS != nil && d.activeSPS.MaxNumRefFrames > 0 {
		maxRef = int(d.activeSPS.MaxNumRefFrames)
	}
	// Sliding window: evict oldest if at capacity.
	for len(d.refFrames) >= maxRef {
		d.refFrames = d.refFrames[1:]
	}
	d.refFrames = append(d.refFrames, deepCopyYCbCr(d.img))
}

func (d *Decoder) resetDPB() {
	d.refFrames = d.refFrames[:0]
}

func (d *Decoder) getRefFrame(idx int) *image.YCbCr {
	// Reference list L0 is in reverse order (most recent first).
	ridx := len(d.refFrames) - 1 - idx
	if ridx < 0 || ridx >= len(d.refFrames) {
		return nil
	}
	return d.refFrames[ridx]
}

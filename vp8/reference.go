package vp8

import "image"

// updateReferenceFrames updates reference frame buffers after decoding a frame.
// RFC 6386 §9.7, §9.8.
func (d *Decoder) updateReferenceFrames() {
	if d.frameHeader.KeyFrame {
		// Keyframe: all references point to the current frame.
		cur := deepCopyYCbCr(d.img)
		d.refFrame[1] = cur
		d.refFrame[2] = deepCopyYCbCr(d.img)
		d.refFrame[3] = deepCopyYCbCr(d.img)
		return
	}

	// Inter-frame reference update order is critical, and matches libvpx
	// swap_frame_buffers: the altref copy happens first, and a subsequent
	// golden-from-altref copy sees the already-updated altref.
	if !d.refreshAltRef {
		switch d.copyAltRef {
		case 1: // copy last
			d.refFrame[3] = deepCopyYCbCr(d.refFrame[1])
		case 2: // copy golden
			d.refFrame[3] = deepCopyYCbCr(d.refFrame[2])
		}
	}
	if !d.refreshGolden {
		switch d.copyGolden {
		case 1: // copy last
			d.refFrame[2] = deepCopyYCbCr(d.refFrame[1])
		case 2: // copy altref
			d.refFrame[2] = deepCopyYCbCr(d.refFrame[3])
		}
	}
	// 2. Refresh from current decoded frame.
	if d.refreshGolden {
		d.refFrame[2] = deepCopyYCbCr(d.img)
	}
	if d.refreshAltRef {
		d.refFrame[3] = deepCopyYCbCr(d.img)
	}
	if d.refreshLast {
		d.refFrame[1] = deepCopyYCbCr(d.img)
	}
}

// FlushReferences clears all reference frame buffers (for seeking).
func (d *Decoder) FlushReferences() {
	d.refFrame = [4]*image.YCbCr{}
}

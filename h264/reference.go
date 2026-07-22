package h264

import (
	"fmt"
	"image"
	"sort"
)

// Decoded Picture Buffer (DPB) management for reference frames.
// Short-term references with sliding window eviction only (no MMCO, no
// long-term references).

// refFrame is one stored short-term reference picture together with the
// side data needed for reference list construction (frameNum, poc), deblock
// identity (id), and direct-mode co-located motion (colMV / colRef).
type refFrame struct {
	img      *image.YCbCr
	frameNum uint32
	poc      int
	id       int
	// colMV / colRef hold, per 4x4 block of the whole frame, the stored
	// motion used by B direct modes (spec 8.4.1.2.2): the block's L0 motion
	// when present, else its L1 motion; ref is the index within the stored
	// frame's own list, -1 for intra blocks.
	colMV  [][2]int16
	colRef []int8
}

func (d *Decoder) initDPB() {
	maxRef := 1
	if d.activeSPS != nil && d.activeSPS.MaxNumRefFrames > 0 {
		maxRef = int(d.activeSPS.MaxNumRefFrames)
	}
	if d.refFrames == nil {
		d.refFrames = make([]*refFrame, 0, maxRef)
	}
}

func (d *Decoder) storeRefFrame(frameNum uint32, mmco []mmcoOp) error {
	maxRef := 1
	if d.activeSPS != nil && d.activeSPS.MaxNumRefFrames > 0 {
		maxRef = int(d.activeSPS.MaxNumRefFrames)
	}
	if len(mmco) > 0 {
		// Adaptive marking (spec 8.2.5.4): the listed short-term pictures are
		// removed instead of sliding-window eviction.
		maxFrameNum := 1 << uint(d.activeSPS.Log2MaxFrameNum)
		curr := int(frameNum)
		for _, m := range mmco {
			if m.op != 1 {
				return fmt.Errorf("MMCO operation %d not supported", m.op)
			}
			picNumX := curr - int(m.arg1) - 1
			for i, rf := range d.refFrames {
				fn := int(rf.frameNum)
				if fn > curr {
					fn -= maxFrameNum
				}
				if fn == picNumX {
					d.refFrames = append(d.refFrames[:i], d.refFrames[i+1:]...)
					break
				}
			}
		}
	}
	// Sliding window: evict oldest (smallest FrameNumWrap ~ oldest stored)
	// if at capacity.
	for len(d.refFrames) >= maxRef {
		d.refFrames = d.refFrames[1:]
	}

	// Capture per-4x4 motion for later B direct modes.
	n := d.mbw * d.mbh * 16
	rf := &refFrame{
		img:      deepCopyYCbCr(d.img),
		frameNum: frameNum,
		poc:      d.curPOC,
		id:       d.nextRefID,
		colMV:    make([][2]int16, n),
		colRef:   make([]int8, n),
	}
	d.nextRefID++
	for mbIdx := range d.mbInfo {
		info := &d.mbInfo[mbIdx]
		base := mbIdx * 16
		for k := 0; k < 16; k++ {
			part := (k/8)*2 + (k%4)/2
			switch {
			case info.isIntra:
				rf.colRef[base+k] = -1
			case info.predMask[part]&1 != 0:
				rf.colMV[base+k] = info.mv[k]
				rf.colRef[base+k] = int8(info.refIdx[part])
			default:
				rf.colMV[base+k] = info.mvL1[k]
				rf.colRef[base+k] = int8(info.refIdxL1[part])
			}
		}
	}
	d.refFrames = append(d.refFrames, rf)
	return nil
}

func (d *Decoder) resetDPB() {
	d.refFrames = d.refFrames[:0]
}

// sliceRefPic is a working entry during reference list construction.
type sliceRefPic struct {
	rf     *refFrame
	picNum int
}

// buildRefLists constructs the L0 (and for B slices L1) reference picture
// lists per spec 8.2.4: initial ordering, then ref_pic_list_modification.
// A modified list may repeat pictures. Entries never filled remain nil;
// getRefFrame reports them as missing only if a macroblock uses that index.
func (d *Decoder) buildRefLists(sh *sliceHeader) error {
	maxFrameNum := 1 << uint(d.activeSPS.Log2MaxFrameNum)
	curr := int(sh.frameNum)

	pics := make([]sliceRefPic, 0, len(d.refFrames))
	for _, rf := range d.refFrames {
		fn := int(rf.frameNum)
		// FrameNumWrap (spec 8.2.4.1): stored frame_num values above the
		// current one are from before a frame_num wraparound.
		if fn > curr {
			fn -= maxFrameNum
		}
		pics = append(pics, sliceRefPic{rf: rf, picNum: fn})
	}

	if sh.sliceType == sliceTypeB {
		// Initial L0: POC < curr descending, then POC > curr ascending.
		l0 := make([]sliceRefPic, len(pics))
		copy(l0, pics)
		sort.SliceStable(l0, func(a, b int) bool {
			pa, pb := l0[a].rf.poc, l0[b].rf.poc
			ba, bb := pa < d.curPOC, pb < d.curPOC
			if ba != bb {
				return ba
			}
			if ba {
				return pa > pb
			}
			return pa < pb
		})
		// Initial L1: POC > curr ascending, then POC < curr descending.
		l1 := make([]sliceRefPic, len(pics))
		copy(l1, pics)
		sort.SliceStable(l1, func(a, b int) bool {
			pa, pb := l1[a].rf.poc, l1[b].rf.poc
			ba, bb := pa > d.curPOC, pb > d.curPOC
			if ba != bb {
				return ba
			}
			if ba {
				return pa < pb
			}
			return pa > pb
		})
		// Spec 8.2.4.2.3: if L1 has more than one entry and is identical to
		// L0, swap its first two entries.
		if len(l1) > 1 {
			same := true
			for i := range l1 {
				if l1[i].rf != l0[i].rf {
					same = false
					break
				}
			}
			if same {
				l1[0], l1[1] = l1[1], l1[0]
			}
		}
		var err error
		d.curRefList[0], err = d.applyRefListMods(l0, pics, sh.refPicListModL0, int(sh.numRefIdxL0Active), curr, maxFrameNum)
		if err != nil {
			return err
		}
		d.curRefList[1], err = d.applyRefListMods(l1, pics, sh.refPicListModL1, int(sh.numRefIdxL1Active), curr, maxFrameNum)
		return err
	}

	// P slices: initial list is descending PicNum (most recent first).
	l0 := make([]sliceRefPic, len(pics))
	copy(l0, pics)
	sort.SliceStable(l0, func(a, b int) bool { return l0[a].picNum > l0[b].picNum })
	var err error
	d.curRefList[0], err = d.applyRefListMods(l0, pics, sh.refPicListModL0, int(sh.numRefIdxL0Active), curr, maxFrameNum)
	d.curRefList[1] = d.curRefList[1][:0]
	return err
}

// applyRefListMods applies the spec 8.2.4.3.1 modification process to an
// initial list, returning the final list truncated to numActive entries.
func (d *Decoder) applyRefListMods(initial, pics []sliceRefPic, ops []refListModOp, numActive, curr, maxFrameNum int) ([]*refFrame, error) {
	list := make([]sliceRefPic, len(initial))
	copy(list, initial)

	if len(ops) > 0 {
		for len(list) < numActive+1 {
			list = append(list, sliceRefPic{})
		}
		refIdx := 0
		pred := curr
		for _, op := range ops {
			if op.idc > 1 {
				return nil, fmt.Errorf("long-term ref_pic_list_modification not supported")
			}
			diff := int(op.val) + 1
			var noWrap int
			if op.idc == 0 {
				noWrap = pred - diff
				if noWrap < 0 {
					noWrap += maxFrameNum
				}
			} else {
				noWrap = pred + diff
				if noWrap >= maxFrameNum {
					noWrap -= maxFrameNum
				}
			}
			pred = noWrap
			picNum := noWrap
			if picNum > curr {
				picNum -= maxFrameNum
			}

			var pic sliceRefPic
			found := false
			for _, p := range pics {
				if p.rf != nil && p.picNum == picNum {
					pic = p
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("no short-term reference with PicNum %d", picNum)
			}

			for c := numActive; c > refIdx; c-- {
				list[c] = list[c-1]
			}
			list[refIdx] = pic
			refIdx++
			nIdx := refIdx
			for c := refIdx; c <= numActive; c++ {
				p := list[c]
				if p.rf != nil && p.picNum == picNum {
					continue
				}
				list[nIdx] = p
				nIdx++
			}
		}
	}
	if len(list) > numActive {
		list = list[:numActive]
	}

	out := make([]*refFrame, len(list))
	for i, p := range list {
		out[i] = p.rf
	}
	return out, nil
}

// getRefFrameEntry returns the stored reference at index idx of the current
// slice's list, or nil if the index is unpopulated.
func (d *Decoder) getRefFrameEntry(list, idx int) *refFrame {
	if list < 0 || list > 1 || idx < 0 || idx >= len(d.curRefList[list]) {
		return nil
	}
	return d.curRefList[list][idx]
}

// getRefFrame returns the L0 reference picture at index idx (P-slice paths).
func (d *Decoder) getRefFrame(idx int) *image.YCbCr {
	rf := d.getRefFrameEntry(0, idx)
	if rf == nil {
		return nil
	}
	return rf.img
}

// refPicID returns the identity of the reference picture at L0 index idx,
// or -1 if unpopulated. Two indices that alias the same stored picture (via
// ref_pic_list_modification) return the same ID.
func (d *Decoder) refPicID(idx int) int {
	return d.refPicIDL(0, idx)
}

func (d *Decoder) refPicIDL(list, idx int) int {
	rf := d.getRefFrameEntry(list, idx)
	if rf == nil {
		return -1
	}
	return rf.id
}

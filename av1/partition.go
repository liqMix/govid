package av1

// Block level constants matching dav1d.
const (
	BL128x128 = 0
	BL64x64   = 1
	BL32x32   = 2
	BL16x16   = 3
	BL8x8     = 4
)

// bSizeToBlockLevel maps block size to dav1d block level.
func bSizeToBlockLevel(bSize int) int {
	switch bSize {
	case Block128x128:
		return BL128x128
	case Block64x64:
		return BL64x64
	case Block32x32:
		return BL32x32
	case Block16x16:
		return BL16x16
	case Block8x8:
		return BL8x8
	default:
		return BL8x8
	}
}

// partitionNsymbs returns the number of partition symbols for a block level.
func partitionNsymbs(bl int) int {
	switch bl {
	case BL8x8:
		return 4 // NONE, HORZ, VERT, SPLIT
	case BL128x128:
		return 8 // no HORZ_4/VERT_4
	default:
		return 10
	}
}

// decodePartition recursively decodes the partition tree for a superblock.
// AV1 spec 5.11.4.
func (td *tileDecoder) decodePartition(miRow, miCol, bSize int) error {
	if miRow >= td.miRowEnd || miCol >= td.miColEnd {
		return nil
	}

	if bSize < Block8x8 {
		return td.decodeBlock(miRow, miCol, bSize)
	}

	bw4 := MISizeWide[bSize]
	bh4 := MISizeTall[bSize]
	halfW := bw4 / 2
	halfH := bh4 / 2

	hasRows := (miRow + halfH) < td.miRowEnd
	hasCols := (miCol + halfW) < td.miColEnd

	bl := bSizeToBlockLevel(bSize)
	nsymbs := partitionNsymbs(bl)

	var partition int
	if hasRows && hasCols {
		ctx := td.getPartitionCtx(miRow, miCol, bSize)
		partition = td.sc.ReadSymbol(td.cdf.Partition[bl][ctx], nsymbs)
	} else if hasCols {
		ctx := td.getPartitionCtx(miRow, miCol, bSize)
		p := td.sc.ReadSymbol(td.cdf.Partition[bl][ctx], nsymbs)
		if p == PartitionNone || p == PartitionHorz || p == PartitionHorzA || p == PartitionHorzB || p == PartitionHorz4 {
			partition = p
		} else {
			partition = PartitionSplit
		}
	} else if hasRows {
		ctx := td.getPartitionCtx(miRow, miCol, bSize)
		p := td.sc.ReadSymbol(td.cdf.Partition[bl][ctx], nsymbs)
		if p == PartitionNone || p == PartitionVert || p == PartitionVertA || p == PartitionVertB || p == PartitionVert4 {
			partition = p
		} else {
			partition = PartitionSplit
		}
	} else {
		partition = PartitionSplit
	}

	subSize := SubSize[bSize][partition]
	if subSize < 0 || subSize >= NumBlockSizes {
		partition = PartitionSplit
		subSize = SubSize[bSize][PartitionSplit]
	}

	td.partitionLog = append(td.partitionLog, partition)

	switch partition {
	case PartitionNone:
		if err := td.decodeBlock(miRow, miCol, subSize); err != nil {
			return err
		}

	case PartitionHorz:
		if err := td.decodeBlock(miRow, miCol, subSize); err != nil {
			return err
		}
		if miRow+halfH < td.miRowEnd {
			if err := td.decodeBlock(miRow+halfH, miCol, subSize); err != nil {
				return err
			}
		}

	case PartitionVert:
		if err := td.decodeBlock(miRow, miCol, subSize); err != nil {
			return err
		}
		if miCol+halfW < td.miColEnd {
			if err := td.decodeBlock(miRow, miCol+halfW, subSize); err != nil {
				return err
			}
		}

	case PartitionSplit:
		splitSize := SubSize[bSize][PartitionSplit]
		if splitSize < 0 {
			return td.decodeBlock(miRow, miCol, bSize)
		}
		if err := td.decodePartition(miRow, miCol, splitSize); err != nil {
			return err
		}
		if err := td.decodePartition(miRow, miCol+halfW, splitSize); err != nil {
			return err
		}
		if err := td.decodePartition(miRow+halfH, miCol, splitSize); err != nil {
			return err
		}
		if err := td.decodePartition(miRow+halfH, miCol+halfW, splitSize); err != nil {
			return err
		}

	case PartitionHorzA:
		splitSize := SubSize[bSize][PartitionSplit]
		if err := td.decodeBlock(miRow, miCol, splitSize); err != nil {
			return err
		}
		if err := td.decodeBlock(miRow, miCol+halfW, splitSize); err != nil {
			return err
		}
		if err := td.decodeBlock(miRow+halfH, miCol, subSize); err != nil {
			return err
		}

	case PartitionHorzB:
		splitSize := SubSize[bSize][PartitionSplit]
		if err := td.decodeBlock(miRow, miCol, subSize); err != nil {
			return err
		}
		if err := td.decodeBlock(miRow+halfH, miCol, splitSize); err != nil {
			return err
		}
		if err := td.decodeBlock(miRow+halfH, miCol+halfW, splitSize); err != nil {
			return err
		}

	case PartitionVertA:
		splitSize := SubSize[bSize][PartitionSplit]
		if err := td.decodeBlock(miRow, miCol, splitSize); err != nil {
			return err
		}
		if err := td.decodeBlock(miRow+halfH, miCol, splitSize); err != nil {
			return err
		}
		if err := td.decodeBlock(miRow, miCol+halfW, subSize); err != nil {
			return err
		}

	case PartitionVertB:
		splitSize := SubSize[bSize][PartitionSplit]
		if err := td.decodeBlock(miRow, miCol, subSize); err != nil {
			return err
		}
		if err := td.decodeBlock(miRow, miCol+halfW, splitSize); err != nil {
			return err
		}
		if err := td.decodeBlock(miRow+halfH, miCol+halfW, splitSize); err != nil {
			return err
		}

	case PartitionHorz4:
		quarterH := bh4 / 4
		for i := 0; i < 4; i++ {
			r := miRow + i*quarterH
			if r < td.miRowEnd {
				if err := td.decodeBlock(r, miCol, subSize); err != nil {
					return err
				}
			}
		}

	case PartitionVert4:
		quarterW := bw4 / 4
		for i := 0; i < 4; i++ {
			c := miCol + i*quarterW
			if c < td.miColEnd {
				if err := td.decodeBlock(miRow, c, subSize); err != nil {
					return err
				}
			}
		}
	}

	td.updatePartitionCtx(miRow, miCol, bSize, subSize)
	return nil
}

// getPartitionCtx computes the 2-bit partition context from above/left neighbors.
func (td *tileDecoder) getPartitionCtx(miRow, miCol, bSize int) int {
	above := 0
	if miRow > td.miRowStart {
		idx := miCol - td.miColStart
		if idx >= 0 && idx < len(td.abovePartCtx) {
			if td.abovePartCtx[idx] < BlockWidthLog2[bSize] {
				above = 1
			}
		}
	}

	left := 0
	if miCol > td.miColStart {
		idx := miRow - td.miRowStart
		if idx >= 0 && idx < len(td.leftPartCtx) {
			if td.leftPartCtx[idx] < BlockHeightLog2[bSize] {
				left = 1
			}
		}
	}

	ctx := 2*left + above
	if ctx > 3 {
		ctx = 3
	}
	return ctx
}

// updatePartitionCtx updates the above/left partition context arrays.
func (td *tileDecoder) updatePartitionCtx(miRow, miCol, bSize, subSize int) {
	bw4 := MISizeWide[bSize]
	bh4 := MISizeTall[bSize]

	aboveCtxVal := BlockWidthLog2[subSize]
	leftCtxVal := BlockHeightLog2[subSize]

	for i := miCol; i < miCol+bw4 && i < td.miColEnd; i++ {
		idx := i - td.miColStart
		if idx >= 0 && idx < len(td.abovePartCtx) {
			td.abovePartCtx[idx] = aboveCtxVal
		}
	}
	for i := miRow; i < miRow+bh4 && i < td.miRowEnd; i++ {
		idx := i - td.miRowStart
		if idx >= 0 && idx < len(td.leftPartCtx) {
			td.leftPartCtx[idx] = leftCtxVal
		}
	}
}

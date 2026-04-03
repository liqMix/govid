package av1

// decodePartition recursively decodes the partition tree for a superblock.
// AV1 spec 5.11.4.
func (td *tileDecoder) decodePartition(miRow, miCol, bSize int) error {
	if miRow >= td.miRowEnd || miCol >= td.miColEnd {
		return nil
	}

	// For blocks smaller than 8x8, only PARTITION_NONE is allowed.
	if bSize < Block8x8 {
		return td.decodeBlock(miRow, miCol, bSize)
	}

	bw4 := MISizeWide[bSize]
	bh4 := MISizeTall[bSize]
	halfW := bw4 / 2
	halfH := bh4 / 2

	hasRows := (miRow + halfH) < td.miRowEnd
	hasCols := (miCol + halfW) < td.miColEnd

	var partition int
	if hasRows && hasCols {
		ctx := td.getPartitionCtx(miRow, miCol, bSize)
		var nsymbs int
		if bSize == Block8x8 {
			nsymbs = 4 // NONE, HORZ, VERT, SPLIT
		} else {
			nsymbs = 10
		}
		var err error
		partition, err = td.sc.ReadSymbol(td.cdf.Partition[ctx], nsymbs)
		if err != nil {
			return err
		}
	} else if hasCols {
		// Only bottom half might be out of frame → force HORZ or SPLIT.
		ctx := td.getPartitionCtx(miRow, miCol, bSize)
		var nsymbs int
		if bSize == Block8x8 {
			nsymbs = 4
		} else {
			nsymbs = 10
		}
		p, err := td.sc.ReadSymbol(td.cdf.Partition[ctx], nsymbs)
		if err != nil {
			return err
		}
		if p == PartitionNone || p == PartitionHorz || p == PartitionHorzA || p == PartitionHorzB || p == PartitionHorz4 {
			partition = p
		} else {
			partition = PartitionSplit
		}
	} else if hasRows {
		// Only right half might be out of frame → force VERT or SPLIT.
		ctx := td.getPartitionCtx(miRow, miCol, bSize)
		var nsymbs int
		if bSize == Block8x8 {
			nsymbs = 4
		} else {
			nsymbs = 10
		}
		p, err := td.sc.ReadSymbol(td.cdf.Partition[ctx], nsymbs)
		if err != nil {
			return err
		}
		if p == PartitionNone || p == PartitionVert || p == PartitionVertA || p == PartitionVertB || p == PartitionVert4 {
			partition = p
		} else {
			partition = PartitionSplit
		}
	} else {
		// Both halves out of frame → must split.
		partition = PartitionSplit
	}

	subSize := SubSize[bSize][partition]
	if subSize < 0 || subSize >= NumBlockSizes {
		// Fallback: split.
		partition = PartitionSplit
		subSize = SubSize[bSize][PartitionSplit]
	}

	// Log partition decision for diagnostics.
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
		// Top-left and top-right are half-width, bottom is full-width.
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
		// Top is full-width, bottom-left and bottom-right are half-width.
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

	// Update above/left partition context.
	td.updatePartitionCtx(miRow, miCol, bSize, subSize)

	return nil
}

// getPartitionCtx computes the partition CDF context index.
// ctx = bsl * 4 + 2*left + above where bsl = BlockWidthLog2[bSize].
func (td *tileDecoder) getPartitionCtx(miRow, miCol, bSize int) int {
	bsl := BlockWidthLog2[bSize]

	above := 0
	if miRow > td.miRowStart {
		aboveIdx := miCol - td.miColStart
		if aboveIdx >= 0 && aboveIdx < len(td.abovePartCtx) {
			if td.abovePartCtx[aboveIdx] < BlockWidthLog2[bSize] {
				above = 1
			}
		}
	}

	left := 0
	if miCol > td.miColStart {
		leftIdx := miRow - td.miRowStart
		if leftIdx >= 0 && leftIdx < len(td.leftPartCtx) {
			if td.leftPartCtx[leftIdx] < BlockHeightLog2[bSize] {
				left = 1
			}
		}
	}

	ctx := bsl*4 + 2*left + above
	if ctx > 23 {
		ctx = 23
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

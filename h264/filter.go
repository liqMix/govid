package h264

// H.264 in-loop deblocking filter per spec 8.7.

// Alpha, Beta, and tc0 lookup tables indexed by indexA/indexB (0-51).
// Spec Tables 8-16 and 8-17.
var alphaTable = [52]int{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 4, 4, 5, 6,
	7, 8, 9, 10, 12, 13, 15, 17, 20, 22,
	25, 28, 32, 36, 40, 45, 50, 56, 63, 71,
	80, 90, 101, 113, 127, 144, 162, 182, 203, 226,
	255, 255,
}

var betaTable = [52]int{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 2, 2, 2, 3,
	3, 3, 3, 4, 4, 4, 6, 6, 7, 7,
	8, 8, 9, 9, 10, 10, 11, 11, 12, 12,
	13, 13, 14, 14, 15, 15, 16, 16, 17, 17,
	18, 18,
}

// tc0 table indexed by [indexA][bS-1] (bS 1-3 only; bS=4 uses strong filter).
var tc0Table = [52][3]int{
	{0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0},
	{0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0},
	{0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 1},
	{0, 0, 1}, {0, 0, 1}, {0, 0, 1}, {0, 1, 1}, {0, 1, 1}, {1, 1, 1},
	{1, 1, 1}, {1, 1, 1}, {1, 1, 1}, {1, 1, 2}, {1, 1, 2}, {1, 1, 2},
	{1, 1, 2}, {1, 2, 3}, {1, 2, 3}, {2, 2, 3}, {2, 2, 4}, {2, 3, 4},
	{2, 3, 4}, {3, 3, 5}, {3, 4, 6}, {3, 4, 6}, {4, 5, 7}, {4, 5, 8},
	{4, 6, 9}, {5, 7, 10}, {6, 8, 11}, {6, 8, 13}, {7, 10, 14}, {8, 11, 16},
	{9, 12, 18}, {10, 13, 20}, {11, 15, 23}, {13, 17, 25},
}

func (d *Decoder) deblockFrame(sh *sliceHeader) {
	if sh.disableDeblockingFilter == 1 || d.disableDeblock {
		return
	}

	for mby := 0; mby < d.mbh; mby++ {
		for mbx := 0; mbx < d.mbw; mbx++ {
			d.deblockMB(mbx, mby, sh)
		}
	}
}

func (d *Decoder) deblockMB(mbx, mby int, sh *sliceHeader) {
	mbIdx := mby*d.mbw + mbx
	qpCur := d.mbInfo[mbIdx].qp

	// Vertical edges first, then horizontal.
	// Hoist invariants: bS and alpha/beta/tc0 only change per 4x4 block along
	// the edge, not per pixel. Prior code recomputed these 16× per edge.
	for edge := 0; edge < 4; edge++ {
		x := mbx*16 + edge*4
		if edge == 0 && mbx == 0 {
			continue
		}

		// Per spec 8.7.2.2: QPav = (QPp + QPq + 1) >> 1
		var qp int
		if edge == 0 {
			leftIdx := mby*d.mbw + mbx - 1
			qp = (d.mbInfo[leftIdx].qp + qpCur + 1) >> 1
		} else {
			qp = qpCur
		}

		// alpha/beta are edge-level invariants (depend only on qp + slice offsets).
		indexA := clampInt(qp+int(sh.sliceAlphaC0Offset), 0, 51)
		indexB := clampInt(qp+int(sh.sliceBetaOffset), 0, 51)
		alpha := alphaTable[indexA]
		beta := betaTable[indexB]

		for blkJ := 0; blkJ < 4; blkJ++ {
			bS := d.computeBSVertical(mbx, mby, edge, blkJ)
			if bS == 0 {
				continue
			}
			var tc0 int
			if bS < 4 {
				tc0 = int(tc0Table[indexA][bS-1])
			}
			// Filter 4 pixels along this 4x4 block edge.
			for j := 0; j < 4; j++ {
				y := mby*16 + blkJ*4 + j
				if bS < 4 {
					filterLumaEdgeSample(d.img.Y, d.img.YStride, x, y, true, alpha, beta, tc0)
				} else {
					filterLumaStrongSample(d.img.Y, d.img.YStride, x, y, true, alpha, beta)
				}
			}
		}
	}

	// Horizontal edges.
	for edge := 0; edge < 4; edge++ {
		y := mby*16 + edge*4
		if edge == 0 && mby == 0 {
			continue
		}

		var qp int
		if edge == 0 {
			aboveIdx := (mby-1)*d.mbw + mbx
			qp = (d.mbInfo[aboveIdx].qp + qpCur + 1) >> 1
		} else {
			qp = qpCur
		}

		indexA := clampInt(qp+int(sh.sliceAlphaC0Offset), 0, 51)
		indexB := clampInt(qp+int(sh.sliceBetaOffset), 0, 51)
		alpha := alphaTable[indexA]
		beta := betaTable[indexB]

		for blkI := 0; blkI < 4; blkI++ {
			bS := d.computeBSHorizontal(mbx, mby, edge, blkI)
			if bS == 0 {
				continue
			}
			var tc0 int
			if bS < 4 {
				tc0 = int(tc0Table[indexA][bS-1])
			}
			for i := 0; i < 4; i++ {
				x := mbx*16 + blkI*4 + i
				if bS < 4 {
					filterLumaEdgeSample(d.img.Y, d.img.YStride, x, y, false, alpha, beta, tc0)
				} else {
					filterLumaStrongSample(d.img.Y, d.img.YStride, x, y, false, alpha, beta)
				}
			}
		}
	}

	// Chroma deblocking: edges at MB boundary and at 8-pixel midpoint.
	// Chroma uses luma bS values. Map chroma coords to luma 4x4 block indices.

	// Vertical chroma edges (edge 0 = MB boundary, edge 1 = midpoint at chroma x=4).
	for edge := 0; edge < 2; edge++ {
		cx := mbx*8 + edge*4
		if edge == 0 && mbx == 0 {
			continue
		}
		lumaEdge := edge * 2 // chroma edge 0->luma edge 0, chroma edge 1->luma edge 2

		// Chroma QP: average of neighboring MBs' chroma QPs.
		var cqp int
		if edge == 0 {
			leftIdx := mby*d.mbw + mbx - 1
			cqpP := chromaQP(d.mbInfo[leftIdx].qp, int(d.activePPS.ChromaQPIndexOffset))
			cqpQ := chromaQP(qpCur, int(d.activePPS.ChromaQPIndexOffset))
			cqp = (cqpP + cqpQ + 1) >> 1
		} else {
			cqp = chromaQP(qpCur, int(d.activePPS.ChromaQPIndexOffset))
		}

		for j := 0; j < 8; j++ {
			cy := mby*8 + j
			lumaBlkRow := j / 2 // chroma row j maps to luma block row j/2 (0-3)
			bS := d.computeBSVertical(mbx, mby, lumaEdge, lumaBlkRow)
			if bS == 0 {
				continue
			}
			indexA := clampInt(cqp+int(sh.sliceAlphaC0Offset), 0, 51)
			indexB := clampInt(cqp+int(sh.sliceBetaOffset), 0, 51)
			alpha := alphaTable[indexA]
			beta := betaTable[indexB]
			if bS < 4 {
				tc0 := tc0Table[indexA][bS-1] + 1
				filterChromaEdgeSample(d.img.Cb, d.img.CStride, cx, cy, true, alpha, beta, tc0)
				filterChromaEdgeSample(d.img.Cr, d.img.CStride, cx, cy, true, alpha, beta, tc0)
			} else {
				filterChromaStrongSample(d.img.Cb, d.img.CStride, cx, cy, true, alpha, beta)
				filterChromaStrongSample(d.img.Cr, d.img.CStride, cx, cy, true, alpha, beta)
			}
		}
	}

	// Horizontal chroma edges.
	for edge := 0; edge < 2; edge++ {
		cy := mby*8 + edge*4
		if edge == 0 && mby == 0 {
			continue
		}
		lumaEdge := edge * 2

		var cqp int
		if edge == 0 {
			aboveIdx := (mby-1)*d.mbw + mbx
			cqpP := chromaQP(d.mbInfo[aboveIdx].qp, int(d.activePPS.ChromaQPIndexOffset))
			cqpQ := chromaQP(qpCur, int(d.activePPS.ChromaQPIndexOffset))
			cqp = (cqpP + cqpQ + 1) >> 1
		} else {
			cqp = chromaQP(qpCur, int(d.activePPS.ChromaQPIndexOffset))
		}

		for i := 0; i < 8; i++ {
			cx := mbx*8 + i
			lumaBlkCol := i / 2
			bS := d.computeBSHorizontal(mbx, mby, lumaEdge, lumaBlkCol)
			if bS == 0 {
				continue
			}
			indexA := clampInt(cqp+int(sh.sliceAlphaC0Offset), 0, 51)
			indexB := clampInt(cqp+int(sh.sliceBetaOffset), 0, 51)
			alpha := alphaTable[indexA]
			beta := betaTable[indexB]
			if bS < 4 {
				tc0 := tc0Table[indexA][bS-1] + 1
				filterChromaEdgeSample(d.img.Cb, d.img.CStride, cx, cy, false, alpha, beta, tc0)
				filterChromaEdgeSample(d.img.Cr, d.img.CStride, cx, cy, false, alpha, beta, tc0)
			} else {
				filterChromaStrongSample(d.img.Cb, d.img.CStride, cx, cy, false, alpha, beta)
				filterChromaStrongSample(d.img.Cr, d.img.CStride, cx, cy, false, alpha, beta)
			}
		}
	}
}

func (d *Decoder) computeBSVertical(mbx, mby, edge, blkRow int) int {
	mbIdx := mby*d.mbw + mbx
	// Current 4x4 block: column = edge, row = blkRow.
	qBlk4x4 := blkRow*4 + edge

	// Neighbor (left of edge).
	var pmbIdx int
	var pBlk4x4 int
	isMBEdge := edge == 0
	if isMBEdge {
		if mbx == 0 {
			return 0
		}
		pmbIdx = mby*d.mbw + mbx - 1
		pBlk4x4 = blkRow*4 + 3 // rightmost column of left MB
	} else {
		pmbIdx = mbIdx
		pBlk4x4 = blkRow*4 + edge - 1
	}

	return d.computeBS(pmbIdx, mbIdx, pBlk4x4, qBlk4x4, isMBEdge)
}

func (d *Decoder) computeBSHorizontal(mbx, mby, edge, blkCol int) int {
	mbIdx := mby*d.mbw + mbx
	qBlk4x4 := edge*4 + blkCol

	var pmbIdx int
	var pBlk4x4 int
	isMBEdge := edge == 0
	if isMBEdge {
		if mby == 0 {
			return 0
		}
		pmbIdx = (mby-1)*d.mbw + mbx
		pBlk4x4 = 3*4 + blkCol // bottom row of above MB
	} else {
		pmbIdx = mbIdx
		pBlk4x4 = (edge-1)*4 + blkCol
	}

	return d.computeBS(pmbIdx, mbIdx, pBlk4x4, qBlk4x4, isMBEdge)
}

func (d *Decoder) computeBS(pmbIdx, qmbIdx, pBlk, qBlk int, isMBEdge bool) int {
	if pmbIdx < 0 || pmbIdx >= len(d.mbInfo) || qmbIdx < 0 || qmbIdx >= len(d.mbInfo) {
		return 0
	}

	pInfo := &d.mbInfo[pmbIdx]
	qInfo := &d.mbInfo[qmbIdx]

	// Per spec 8.7.2.1: bS=4 at MB edge if intra, bS=3 at internal edge if intra.
	if pInfo.isIntra || qInfo.isIntra {
		if isMBEdge {
			return 4
		}
		return 3
	}

	// Check non-zero coefficients (convert raster index to scan order).
	pNZ := d.nzCoeff[pmbIdx*24+rasterToScan[pBlk]]
	qNZ := d.nzCoeff[qmbIdx*24+rasterToScan[qBlk]]
	if pNZ > 0 || qNZ > 0 {
		return 2
	}

	// Check different ref frames.
	pPart := (pBlk/4/2)*2 + (pBlk%4)/2
	qPart := (qBlk/4/2)*2 + (qBlk%4)/2
	if pInfo.refIdx[pPart] != qInfo.refIdx[qPart] {
		return 1
	}

	// Check MV difference >= 4 quarter-pixels.
	pMV := pInfo.mv[pBlk]
	qMV := qInfo.mv[qBlk]
	if abs16(pMV[0]-qMV[0]) >= 4 || abs16(pMV[1]-qMV[1]) >= 4 {
		return 1
	}

	return 0
}

func abs16(x int16) int16 {
	if x < 0 {
		return -x
	}
	return x
}

// filterLumaEdgeSample applies the normal (bS 1-3) luma deblocking filter at a single edge pixel.
// vertical=true means vertical edge (filter horizontally across x).
func filterLumaEdgeSample(pix []byte, stride, x, y int, vertical bool, alpha, beta, tc0 int) {
	var p0, p1, p2, q0, q1, q2 int
	var p0Idx, p1Idx, q0Idx, q1Idx int

	if vertical {
		p0Idx = y*stride + x - 1
		p1Idx = y*stride + x - 2
		q0Idx = y*stride + x
		q1Idx = y*stride + x + 1
		p0, p1, p2 = int(pix[p0Idx]), int(pix[p1Idx]), int(pix[y*stride+x-3])
		q0, q1, q2 = int(pix[q0Idx]), int(pix[q1Idx]), int(pix[y*stride+x+2])
	} else {
		p0Idx = (y-1)*stride + x
		p1Idx = (y-2)*stride + x
		q0Idx = y*stride + x
		q1Idx = (y+1)*stride + x
		p0, p1, p2 = int(pix[p0Idx]), int(pix[p1Idx]), int(pix[(y-3)*stride+x])
		q0, q1, q2 = int(pix[q0Idx]), int(pix[q1Idx]), int(pix[(y+2)*stride+x])
	}

	if absInt(p0-q0) >= alpha || absInt(p1-p0) >= beta || absInt(q1-q0) >= beta {
		return
	}

	tc := tc0
	if absInt(p2-p0) < beta {
		tc++
	}
	if absInt(q2-q0) < beta {
		tc++
	}

	delta := clampInt(((q0-p0)*4+(p1-q1)+4)>>3, -tc, tc)
	pix[p0Idx] = uint8(clampInt(p0+delta, 0, 255))
	pix[q0Idx] = uint8(clampInt(q0-delta, 0, 255))

	if absInt(p2-p0) < beta {
		delta2 := clampInt((p2+(p0+q0+1)/2-2*p1)>>1, -tc0, tc0)
		pix[p1Idx] = uint8(clampInt(p1+delta2, 0, 255))
	}
	if absInt(q2-q0) < beta {
		delta2 := clampInt((q2+(p0+q0+1)/2-2*q1)>>1, -tc0, tc0)
		pix[q1Idx] = uint8(clampInt(q1+delta2, 0, 255))
	}
}

// filterLumaStrongSample applies the strong (bS=4) luma deblocking filter.
func filterLumaStrongSample(pix []byte, stride, x, y int, vertical bool, alpha, beta int) {
	var p0, p1, p2, q0, q1, q2 int
	var p0Idx, p1Idx, p2Idx, q0Idx, q1Idx, q2Idx int

	if vertical {
		p0Idx = y*stride + x - 1
		p1Idx = y*stride + x - 2
		p2Idx = y*stride + x - 3
		q0Idx = y*stride + x
		q1Idx = y*stride + x + 1
		q2Idx = y*stride + x + 2
	} else {
		p0Idx = (y-1)*stride + x
		p1Idx = (y-2)*stride + x
		p2Idx = (y-3)*stride + x
		q0Idx = y*stride + x
		q1Idx = (y+1)*stride + x
		q2Idx = (y+2)*stride + x
	}

	p0 = int(pix[p0Idx])
	p1 = int(pix[p1Idx])
	p2 = int(pix[p2Idx])
	q0 = int(pix[q0Idx])
	q1 = int(pix[q1Idx])
	q2 = int(pix[q2Idx])

	if absInt(p0-q0) >= alpha || absInt(p1-p0) >= beta || absInt(q1-q0) >= beta {
		return
	}

	if absInt(p0-q0) < ((alpha>>2)+2) {
		if absInt(p2-p0) < beta {
			pix[p0Idx] = uint8((p2 + 2*p1 + 2*p0 + 2*q0 + q1 + 4) >> 3)
			pix[p1Idx] = uint8((p2 + p1 + p0 + q0 + 2) >> 2)
			pix[p2Idx] = uint8((2*pix2to3(pix, stride, x, y, vertical) + 3*p2 + p1 + p0 + q0 + 4) >> 3)
		} else {
			pix[p0Idx] = uint8((2*p1 + p0 + q1 + 2) >> 2)
		}
		if absInt(q2-q0) < beta {
			pix[q0Idx] = uint8((q2 + 2*q1 + 2*q0 + 2*p0 + p1 + 4) >> 3)
			pix[q1Idx] = uint8((q2 + q1 + q0 + p0 + 2) >> 2)
			pix[q2Idx] = uint8((2*pix2to3Q(pix, stride, x, y, vertical) + 3*q2 + q1 + q0 + p0 + 4) >> 3)
		} else {
			pix[q0Idx] = uint8((2*q1 + q0 + p1 + 2) >> 2)
		}
	} else {
		pix[p0Idx] = uint8((2*p1 + p0 + q1 + 2) >> 2)
		pix[q0Idx] = uint8((2*q1 + q0 + p1 + 2) >> 2)
	}
}

// helper to get p3 pixel value
func pix2to3(pix []byte, stride, x, y int, vertical bool) int {
	if vertical {
		return int(pix[y*stride+x-4])
	}
	return int(pix[(y-4)*stride+x])
}

func pix2to3Q(pix []byte, stride, x, y int, vertical bool) int {
	if vertical {
		return int(pix[y*stride+x+3])
	}
	return int(pix[(y+3)*stride+x])
}

// filterChromaEdgeSample applies normal chroma deblocking.
func filterChromaEdgeSample(pix []byte, stride, x, y int, vertical bool, alpha, beta, tc int) {
	var p0, p1, q0, q1 int
	var p0Idx, q0Idx int

	if vertical {
		p0Idx = y*stride + x - 1
		q0Idx = y*stride + x
		p0, p1 = int(pix[p0Idx]), int(pix[y*stride+x-2])
		q0, q1 = int(pix[q0Idx]), int(pix[y*stride+x+1])
	} else {
		p0Idx = (y-1)*stride + x
		q0Idx = y*stride + x
		p0, p1 = int(pix[p0Idx]), int(pix[(y-2)*stride+x])
		q0, q1 = int(pix[q0Idx]), int(pix[(y+1)*stride+x])
	}

	if absInt(p0-q0) >= alpha || absInt(p1-p0) >= beta || absInt(q1-q0) >= beta {
		return
	}

	delta := clampInt(((q0-p0)*4+(p1-q1)+4)>>3, -tc, tc)
	pix[p0Idx] = uint8(clampInt(p0+delta, 0, 255))
	pix[q0Idx] = uint8(clampInt(q0-delta, 0, 255))
}

// filterChromaStrongSample applies strong chroma deblocking.
func filterChromaStrongSample(pix []byte, stride, x, y int, vertical bool, alpha, beta int) {
	var p0, p1, q0, q1 int
	var p0Idx, q0Idx int

	if vertical {
		p0Idx = y*stride + x - 1
		q0Idx = y*stride + x
		p0, p1 = int(pix[p0Idx]), int(pix[y*stride+x-2])
		q0, q1 = int(pix[q0Idx]), int(pix[y*stride+x+1])
	} else {
		p0Idx = (y-1)*stride + x
		q0Idx = y*stride + x
		p0, p1 = int(pix[p0Idx]), int(pix[(y-2)*stride+x])
		q0, q1 = int(pix[q0Idx]), int(pix[(y+1)*stride+x])
	}

	if absInt(p0-q0) >= alpha || absInt(p1-p0) >= beta || absInt(q1-q0) >= beta {
		return
	}

	pix[p0Idx] = uint8(clampInt((2*p1+p0+q1+2)>>2, 0, 255))
	pix[q0Idx] = uint8(clampInt((2*q1+q0+p1+2)>>2, 0, 255))
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

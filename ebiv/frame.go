package ebiv

import (
	"encoding/binary"
	"fmt"
	"image"
	"math"
)

// This file coordinates the coded frame paths. A frame is a padded set of
// planes divided into 16x16 luma macroblocks (8x8 per chroma plane). Each tile
// is a rectangular span of macroblocks with its own rANS stream, so tiles
// decode independently (§3.5). Intra prediction treats a tile edge like a frame
// edge, which is what makes the independence real.

// frameBuf holds one frame's planes padded out to whole macroblocks, so block
// coding never has to special-case a partial edge block. The visible region is
// copied in and out at the boundaries.
type frameBuf struct {
	y, cb, cr planeView
	mbCols    int
	mbRows    int
}

func newFrameBuf(g geometry) *frameBuf {
	mbCols := (g.W + mbSize - 1) / mbSize
	mbRows := (g.H + mbSize - 1) / mbSize
	pw, ph := mbCols*mbSize, mbRows*mbSize
	cw, ch := mbCols*chromaMB, mbRows*chromaMB
	return &frameBuf{
		y:      planeView{data: make([]byte, pw*ph), stride: pw, w: pw, h: ph},
		cb:     planeView{data: make([]byte, cw*ch), stride: cw, w: cw, h: ch},
		cr:     planeView{data: make([]byte, cw*ch), stride: cw, w: cw, h: ch},
		mbCols: mbCols,
		mbRows: mbRows,
	}
}

// loadImage copies a source image into the padded buffer, replicating the
// visible edge into the padding so edge blocks predict from plausible pixels.
func (f *frameBuf) loadImage(g geometry, img *image.YCbCr) {
	loadPlane(f.y, img.Y, img.YStride, g.W, g.H)
	loadPlane(f.cb, img.Cb, img.CStride, g.CW, g.CH)
	loadPlane(f.cr, img.Cr, img.CStride, g.CW, g.CH)
}

func loadPlane(dst planeView, src []byte, sStride, vw, vh int) {
	for y := 0; y < dst.h; y++ {
		sy := min(y, vh-1)
		drow := dst.data[y*dst.stride : y*dst.stride+dst.w]
		for x := 0; x < dst.w; x++ {
			drow[x] = src[sy*sStride+min(x, vw-1)]
		}
	}
}

// storeImage copies the visible region back out into a strided image.
func (f *frameBuf) storeImage(g geometry, img *image.YCbCr) {
	storePlane(img.Y, img.YStride, f.y, g.W, g.H)
	storePlane(img.Cb, img.CStride, f.cb, g.CW, g.CH)
	storePlane(img.Cr, img.CStride, f.cr, g.CW, g.CH)
}

func storePlane(dst []byte, dStride int, src planeView, vw, vh int) {
	for y := 0; y < vh; y++ {
		copy(dst[y*dStride:y*dStride+vw], src.data[y*src.stride:y*src.stride+vw])
	}
}

// tileGrid describes how macroblocks are partitioned into tiles.
type tileGrid struct {
	cols     int
	rows     int
	colEdges []int // len cols+1, macroblock column boundaries
	rowEdges []int // len rows+1, macroblock row boundaries
}

func newTileGrid(mbCols, mbRows, cols, rows int) tileGrid {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	cols = min(cols, mbCols)
	rows = min(rows, mbRows)
	return tileGrid{
		cols:     cols,
		rows:     rows,
		colEdges: evenSplit(mbCols, cols),
		rowEdges: evenSplit(mbRows, rows),
	}
}

// evenSplit returns count+1 boundaries dividing n items as evenly as possible.
func evenSplit(n, count int) []int {
	edges := make([]int, count+1)
	for i := 0; i <= count; i++ {
		edges[i] = n * i / count
	}
	return edges
}

// Auto-tiling bounds. Two constants shape the grid, chosen from the measured
// size/speed curve rather than the core count:
//
//   - Decode speed plateaus past a handful of tiles (a 512×384 frame decodes
//     1.19 ms/frame at 8 tiles vs 0.70 ms at 64 — 1.7× faster for +40% size),
//     so there is no reason to split past maxAutoTiles even on a many-core host.
//   - The prediction cost at tile edges shrinks as tiles grow, so a minimum
//     tile size keeps small frames from being shredded (at 32 tiles a 512×384
//     frame costs +34% but a 1080p frame only +3%). minTileMB is the floor.
const (
	maxAutoTiles = 16
	minTileMB    = 16 // no tile narrower/shorter than this many macroblocks (256 px)
)

// autoTileGrid picks a tile grid sized for real-time decode with the least
// compression cost, not to saturate cores: it targets min(workers,
// maxAutoTiles) tiles, keeps each tile at least minTileMB across, and biases the
// split toward the frame's aspect ratio so tiles stay roughly square. It returns
// (1,1) when the frame is too small to split without violating the floor.
//
// The trade this encodes: more tiles speed up the (offline, once) encode but
// grow the (shipped, replayed) file and barely help decode past the knee, so
// the default favors the decoder. Callers who want maximum encode throughput
// can override with an explicit WithTiles grid.
func autoTileGrid(mbCols, mbRows, workers int) (cols, rows int) {
	target := min(workers, maxAutoTiles)
	if target < 2 || mbCols*mbRows < 2*minTileMB*minTileMB {
		return 1, 1
	}
	maxCols := max(1, mbCols/minTileMB)
	maxRows := max(1, mbRows/minTileMB)

	// Aim for cols*rows ≈ target with cols/rows ≈ mbCols/mbRows.
	cols = int(math.Round(math.Sqrt(float64(target) * float64(mbCols) / float64(mbRows))))
	cols = clampInt(cols, 1, maxCols)
	rows = clampInt((target+cols-1)/cols, 1, maxRows)
	return cols, rows
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (g tileGrid) count() int { return g.cols * g.rows }

// tileBounds is one tile's macroblock span.
type tileBounds struct {
	mbX0, mbX1 int
	mbY0, mbY1 int
}

func (g tileGrid) bounds(index int) tileBounds {
	tc := index % g.cols
	tr := index / g.cols
	return tileBounds{
		mbX0: g.colEdges[tc], mbX1: g.colEdges[tc+1],
		mbY0: g.rowEdges[tr], mbY1: g.rowEdges[tr+1],
	}
}

// --- Coded frame payload framing --------------------------------------------

// codedHeader carries the per-frame parameters that follow the frame-type byte.
type codedHeader struct {
	qp       int
	tileCols int
	tileRows int
}

func (h codedHeader) appendTo(dst []byte) []byte {
	dst = append(dst, byte(h.qp))
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], uint64(h.tileCols))
	dst = append(dst, tmp[:n]...)
	n = binary.PutUvarint(tmp[:], uint64(h.tileRows))
	return append(dst, tmp[:n]...)
}

func parseCodedHeader(b []byte) (codedHeader, int, error) {
	var h codedHeader
	if len(b) < 1 {
		return h, 0, fmt.Errorf("%w: coded header truncated", ErrCorrupt)
	}
	h.qp = int(b[0])
	pos := 1
	cols, n := binary.Uvarint(b[pos:])
	if n <= 0 {
		return h, 0, fmt.Errorf("%w: coded header tile columns", ErrCorrupt)
	}
	pos += n
	rows, n := binary.Uvarint(b[pos:])
	if n <= 0 {
		return h, 0, fmt.Errorf("%w: coded header tile rows", ErrCorrupt)
	}
	pos += n
	if h.qp > maxQP || cols == 0 || rows == 0 || cols > 65535 || rows > 65535 {
		return h, 0, fmt.Errorf("%w: coded header values out of range", ErrCorrupt)
	}
	h.tileCols = int(cols)
	h.tileRows = int(rows)
	return h, pos, nil
}

// assemblePayload builds a coded frame body: coded header, frequency tables,
// then each tile's length-prefixed rANS stream.
func assemblePayload(hdr codedHeader, tables []byte, tiles [][]byte) []byte {
	out := hdr.appendTo(nil)
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], uint64(len(tables)))
	out = append(out, tmp[:n]...)
	out = append(out, tables...)
	for _, t := range tiles {
		n := binary.PutUvarint(tmp[:], uint64(len(t)))
		out = append(out, tmp[:n]...)
		out = append(out, t...)
	}
	return out
}

// parsePayload is the inverse of assemblePayload. It fills the pooled frequency
// tables and reuses the tiles-list slice, so re-decoding a frame does not
// reallocate them. The returned tile byte slices alias body. key selects
// self-contained table parsing (relative table modes rejected, history wiped).
func (ds *decodeState) parsePayload(body []byte, key bool) (codedHeader, [][]byte, error) {
	hdr, pos, err := parseCodedHeader(body)
	if err != nil {
		return hdr, nil, err
	}
	tablesLen, n := binary.Uvarint(body[pos:])
	if n <= 0 || pos+n+int(tablesLen) > len(body) {
		return hdr, nil, fmt.Errorf("%w: bad table length", ErrCorrupt)
	}
	pos += n
	consumed, err := parseTablesInto(ds.tables, ds.prevFreqs, body[pos:pos+int(tablesLen)], key)
	if err != nil {
		return hdr, nil, err
	}
	if consumed != int(tablesLen) {
		return hdr, nil, fmt.Errorf("%w: trailing table bytes", ErrCorrupt)
	}
	pos += int(tablesLen)

	want := hdr.tileCols * hdr.tileRows
	tiles := ds.tiles[:0]
	for len(tiles) < want {
		tlen, n := binary.Uvarint(body[pos:])
		if n <= 0 || pos+n+int(tlen) > len(body) {
			return hdr, nil, fmt.Errorf("%w: bad tile length", ErrCorrupt)
		}
		pos += n
		tiles = append(tiles, body[pos:pos+int(tlen)])
		pos += int(tlen)
	}
	ds.tiles = tiles
	return hdr, tiles, nil
}

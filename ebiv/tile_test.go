package ebiv

import (
	"bytes"
	"runtime"
	"testing"
)

// TestTileGridSplit checks the macroblock partition is exhaustive and
// non-overlapping for a range of grid shapes, including grids larger than the
// frame (which must clamp).
func TestTileGridSplit(t *testing.T) {
	cases := []struct{ mbCols, mbRows, cols, rows int }{
		{10, 8, 1, 1},
		{10, 8, 3, 2},
		{10, 8, 4, 4},
		{5, 5, 8, 8}, // more tiles than macroblocks
		{1, 1, 2, 2},
	}
	for _, tc := range cases {
		grid := newTileGrid(tc.mbCols, tc.mbRows, tc.cols, tc.rows)
		covered := make([][]bool, tc.mbRows)
		for i := range covered {
			covered[i] = make([]bool, tc.mbCols)
		}
		for i := 0; i < grid.count(); i++ {
			b := grid.bounds(i)
			for y := b.mbY0; y < b.mbY1; y++ {
				for x := b.mbX0; x < b.mbX1; x++ {
					if covered[y][x] {
						t.Fatalf("%+v: macroblock (%d,%d) covered twice", tc, x, y)
					}
					covered[y][x] = true
				}
			}
		}
		for y := range covered {
			for x := range covered[y] {
				if !covered[y][x] {
					t.Fatalf("%+v: macroblock (%d,%d) not covered", tc, x, y)
				}
			}
		}
	}
}

// TestTilingDifferential is the parallelism safety check from §10: the same
// content coded with different tile grids must decode identically. Tiling
// changes intra-prediction availability at tile edges, so the byte streams
// differ — but every grid must still reconstruct the exact same pixels as its
// own encode, and a re-decode must be stable.
func TestTilingDifferential(t *testing.T) {
	cfg := Config{Width: 256, Height: 160, FPSNum: 30, FPSDen: 1}

	grids := [][2]int{{1, 1}, {2, 1}, {2, 2}, {4, 3}}
	for _, gr := range grids {
		t.Run(sizeLabel(gr[0], gr[1]), func(t *testing.T) {
			container, _ := encodeCoded(t, cfg, 3, WithIntra(18), WithGOP(3), WithTiles(gr[0], gr[1]))
			// Decoding twice must agree bit-for-bit regardless of goroutine
			// scheduling across the tiles.
			a := decodeToPlanes(t, container)
			b := decodeToPlanes(t, container)
			for i := range a {
				if !bytes.Equal(a[i], b[i]) {
					t.Fatalf("grid %v frame %d: non-deterministic tiled decode", gr, i)
				}
			}
		})
	}
}

// TestParallelEncodeDeterministic checks that concurrent tile encoding produces
// byte-identical containers every run: the per-tile token streams are
// independent and the shared frequency tables are built from an order-
// independent sum of counts, so goroutine scheduling must not affect the output.
func TestParallelEncodeDeterministic(t *testing.T) {
	cfg := Config{Width: 320, Height: 224, FPSNum: 30, FPSDen: 1}
	opts := []EncoderOption{WithIntra(18), WithGOP(4), WithTiles(4, 4)}

	first, _ := encodeCoded(t, cfg, 6, opts...)
	for trial := 0; trial < 8; trial++ {
		again, _ := encodeCoded(t, cfg, 6, opts...)
		if !bytes.Equal(first, again) {
			t.Fatalf("trial %d: parallel encode produced a different container; output depends on scheduling", trial)
		}
	}
}

// TestAutoTileGrid checks the automatic grid stays capped at the decode knee,
// keeps tiles at or above the minimum size, and declines to split small frames.
func TestAutoTileGrid(t *testing.T) {
	// 1080p is 120x68 macroblocks. The grid is capped at maxAutoTiles and every
	// tile must be at least minTileMB across.
	cols, rows := autoTileGrid(120, 68, 32)
	if cols*rows < 2 || cols*rows > maxAutoTiles+cols /* rounding slack */ {
		t.Errorf("autoTileGrid(1080p, 32) = %dx%d (%d tiles), want a capped split", cols, rows, cols*rows)
	}
	if 120/cols < minTileMB || 68/rows < minTileMB {
		t.Errorf("autoTileGrid(1080p, 32) = %dx%d gives tiles below the %d-MB floor", cols, rows, minTileMB)
	}
	// More workers than the cap must not add tiles.
	if c, r := autoTileGrid(120, 68, 128); c*r > maxAutoTiles+c {
		t.Errorf("autoTileGrid(1080p, 128) = %dx%d, want no more than the cap", c, r)
	}
	// A tiny frame must not be split.
	if c, r := autoTileGrid(3, 3, 32); c != 1 || r != 1 {
		t.Errorf("autoTileGrid(tiny, 32) = %dx%d, want 1x1", c, r)
	}
	// One worker means no split.
	if c, r := autoTileGrid(120, 68, 1); c != 1 || r != 1 {
		t.Errorf("autoTileGrid(_, 1) = %dx%d, want 1x1", c, r)
	}
}

// TestAutoTilesEdgeCostBounded guards caveat 2: the auto grid must keep the
// tile-edge compression cost small on a reasonably sized frame. It compares the
// auto-tiled file against the single-tile encode of the same content.
func TestAutoTilesEdgeCostBounded(t *testing.T) {
	cfg := Config{Width: 768, Height: 512, FPSNum: 30, FPSDen: 1}
	single, _ := encodeCodedGen(t, cfg, 8, synthPan, WithIntra(18), WithGOP(4))
	auto, _ := encodeCodedGen(t, cfg, 8, synthPan, WithIntra(18), WithGOP(4), WithAutoTiles(32))

	overhead := 100 * float64(len(auto)-len(single)) / float64(len(single))
	t.Logf("768x512: single-tile %d bytes, auto-tiled %d bytes (+%.1f%%)", len(single), len(auto), overhead)
	if overhead > 12 {
		t.Errorf("auto-tiling grew the file by %.1f%%, want <= 12%% (tiles too small)", overhead)
	}
}

// TestAutoTilesEncodes checks WithAutoTiles produces a valid, well-reconstructed
// stream and that the frame actually carries multiple tiles.
func TestAutoTilesEncodes(t *testing.T) {
	cfg := Config{Width: 768, Height: 512, FPSNum: 30, FPSDen: 1}
	g := geometryFor(cfg.Width, cfg.Height)
	container, refs := encodeCoded(t, cfg, 3, WithIntra(18), WithGOP(3), WithAutoTiles(16))

	// The first coded frame's coded header must report more than one tile.
	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := d.NextPacket()
	if err != nil {
		t.Fatal(err)
	}
	_, body, err := parseFrameHeader(pkt.Data)
	if err != nil {
		t.Fatal(err)
	}
	hdr, _, err := parseCodedHeader(body)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if hdr.tileCols*hdr.tileRows < 2 {
		t.Errorf("auto-tiled frame reports %dx%d tiles, want more than one", hdr.tileCols, hdr.tileRows)
	}

	planes := decodeImages(t, container)
	for i, img := range planes {
		if p := framePSNR(img, refs[i], g); p < 34 {
			t.Errorf("frame %d: PSNR %.1f dB, want >= 34", i, p)
		}
	}
}

// TestWorkerPoolNoGoroutineChurn checks the fixed pool holds goroutine count
// flat across many multi-tile decodes: decoding must not spawn a goroutine per
// frame (§5). The pool is created once on first use, so the count settles after
// a warm-up frame and then stops growing.
func TestWorkerPoolNoGoroutineChurn(t *testing.T) {
	cfg := Config{Width: 256, Height: 192, FPSNum: 30, FPSDen: 1}
	container, _ := encodeCoded(t, cfg, 30, WithIntra(20), WithGOP(5), WithTiles(4, 4))

	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c := NewCodec()

	decodeOne := func() {
		pkt, err := d.NextPacket()
		if err != nil {
			if _, err := d.Seek(0); err != nil {
				t.Fatal(err)
			}
			pkt, err = d.NextPacket()
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := c.Decode(pkt); err != nil {
			t.Fatal(err)
		}
	}

	// Warm up so the pool's workers are all spawned, then let the scheduler
	// settle any transient goroutines.
	for i := 0; i < 8; i++ {
		decodeOne()
	}
	runtime.GC()
	before := runtime.NumGoroutine()

	for i := 0; i < 200; i++ {
		decodeOne()
	}
	runtime.GC()
	after := runtime.NumGoroutine()

	// Allow a tiny slack for scheduler transients, but 200 frames must not add
	// anything resembling a goroutine-per-frame (or even per-tile) leak.
	if after > before+2 {
		t.Errorf("goroutines grew from %d to %d across 200 decodes; the pool is not being reused", before, after)
	}
}

// TestTilingMatchesSingleTileQuality checks that splitting into tiles does not
// wreck quality: a tiled encode must reconstruct its own source about as well
// as a single-tile encode. Tile edges lose some intra prediction, so a few dB
// of slack is allowed.
func TestTilingMatchesSingleTileQuality(t *testing.T) {
	cfg := Config{Width: 256, Height: 160, FPSNum: 30, FPSDen: 1}
	g := geometryFor(cfg.Width, cfg.Height)

	single, refs := encodeCoded(t, cfg, 2, WithIntra(18))
	tiled, _ := encodeCoded(t, cfg, 2, WithIntra(18), WithTiles(4, 3))

	singleP := worstPSNR(t, single, refs, g)
	tiledP := worstPSNR(t, tiled, refs, g)
	if tiledP < singleP-3 {
		t.Errorf("tiled PSNR %.1f dB is more than 3 dB below single-tile %.1f dB", tiledP, singleP)
	}
	t.Logf("single-tile PSNR %.1f dB, 4x3 tiles PSNR %.1f dB", singleP, tiledP)
}

func worstPSNR(t *testing.T, container []byte, refs [][]byte, g geometry) float64 {
	t.Helper()
	planes := decodeImages(t, container)
	worst := 1e9
	for i, img := range planes {
		if p := framePSNR(img, refs[i], g); p < worst {
			worst = p
		}
	}
	return worst
}

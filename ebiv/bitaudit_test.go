package ebiv

import (
	"fmt"
	"io"
	"math"
	"os"
	"testing"

	govid "github.com/liqmix/govid"
	"github.com/liqmix/govid/h264"
	mp4pkg "github.com/liqmix/govid/mp4"
)

// TestBitAudit measures where an EBIV stream's bits actually go on real
// content: per-context-group entropy, coefficient-token composition, and — the
// number that motivates a skip mode — how many inter macroblocks are fully
// static (zero MV delta, all-zero residual) and what they cost today.
//
// It walks the encoder's token streams with the same grammar the decoder uses
// and prices each token against the frame's final rANS tables, then
// cross-checks that the walked total matches the aggregate entropy, so the
// reported numbers cannot drift from the real bitstream.
//
// Env-gated; it needs a govid-decodable H.264/MP4 clip:
//
//	EBIV_AUDIT_SRC=clean.mp4 EBIV_AUDIT_FRAMES=60 go test ./ebiv -run TestBitAudit -v
func TestBitAudit(t *testing.T) {
	src := os.Getenv("EBIV_AUDIT_SRC")
	if src == "" {
		t.Skip("set EBIV_AUDIT_SRC to a govid-decodable mp4 to audit bit allocation")
	}
	nFrames := 60
	if v := os.Getenv("EBIV_AUDIT_FRAMES"); v != "" {
		fmt.Sscanf(v, "%d", &nFrames)
	}
	const qp = 22
	const tileCols, tileRows = 5, 4

	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	demux, err := mp4pkg.NewDemuxer(f)
	if err != nil {
		t.Fatal(err)
	}
	defer demux.Close()
	codec := h264.NewCodec()

	var (
		g           geometry
		ref         *frameBuf
		golden      *frameBuf
		prevShipped [][]uint32
		agg         = newAuditTally()
		interAgg    = newAuditTally()
		intraBits   float64
		payloadSum  int
		tablesSum   int
		mbStats     auditMBStats
	)

	for i := 0; i < nFrames; i++ {
		frame, err := nextAuditFrame(demux, codec)
		if err == io.EOF {
			nFrames = i
			break
		}
		if err != nil {
			t.Fatalf("decode source frame %d: %v", i, err)
		}
		if i == 0 {
			g = geometryFor(frame.Width, frame.Height)
		}

		inter := i > 0
		e := newFrameEncoder(g, frame.YCbCr, ref, golden, qp, tileCols, tileRows)
		e.encodeTilesParallel(inter)
		var prev [][]uint32
		if inter {
			prev = prevShipped
		}
		payload, rec, shipped := e.finish(qp, prev)
		ref = rec
		if !inter {
			golden = rec // the key frame's reconstruction is the GOP's golden
		}
		payloadSum += len(payload)
		if !inter {
			prevShipped = make([][]uint32, numContexts)
		}
		for c, f := range shipped {
			if f != nil {
				prevShipped[c] = f
			}
		}

		// Rebuild the exact tables finish used, price every token, and walk.
		freqs := make([][]uint32, numContexts)
		for c := range freqs {
			if c == ctxBypass {
				continue
			}
			freqs[c] = normalizeFreqs(e.counts[c])
		}
		tablesSum += len(serializeTables(freqs, prev))
		cost := makeCostTable(freqs)

		frameTally := newAuditTally()
		for _, s := range e.streams {
			w := &auditWalker{toks: s.toks, cost: cost, tally: frameTally}
			w.walkStream(t, inter, &mbStats)
			if w.pos != len(s.toks) {
				t.Fatalf("frame %d: walker consumed %d of %d tokens — grammar mismatch", i, w.pos, len(s.toks))
			}
		}
		// Cross-check: walked bits must equal the entropy of the counts.
		entropy := entropyBits(e.counts, freqs)
		if d := math.Abs(frameTally.total() - entropy); d > 1 {
			t.Fatalf("frame %d: walked %.0f bits but counts say %.0f — pricing mismatch", i, frameTally.total(), entropy)
		}
		agg.add(frameTally)
		if inter {
			interAgg.add(frameTally)
		} else {
			intraBits += frameTally.total()
		}
		// e.counts was mutated by finish; each frame builds a fresh encoder, so
		// no reset needed.
	}

	total := agg.total()
	t.Logf("audited %d frames @ qp%d, %dx%d, %d tiles", nFrames, qp, g.W, g.H, tileCols*tileRows)
	t.Logf("payload %.2f MiB total (%.0f bytes/frame), entropy-coded bits %.2f MiB, tables %.1f KiB (%.2f%% of payload)",
		float64(payloadSum)/(1<<20), float64(payloadSum)/float64(nFrames),
		total/8/(1<<20), float64(tablesSum)/1024, 100*float64(tablesSum)/float64(payloadSum))
	t.Logf("intra frame: %.0f bits; inter frames: %.0f bits", intraBits, interAgg.total())

	t.Logf("--- bits by group (all frames) ---")
	for _, gr := range auditGroupNames {
		b := agg.groups[gr]
		t.Logf("  %-12s %12.0f bits  %5.1f%%", gr, b, 100*b/total)
	}
	t.Logf("--- coefficient tokens by symbol (all frames) ---")
	symNames := []string{"zero", "one", "two", "three", "four", "escape", "eob"}
	var coefTotal float64
	for s := range symNames {
		coefTotal += agg.tokenSym[s]
	}
	for s, name := range symNames {
		t.Logf("  %-8s %12.0f bits  %5.1f%% of coef bits", name, agg.tokenSym[s], 100*agg.tokenSym[s]/coefTotal)
	}

	total3 := mbStats.interMBs + mbStats.intraMBs + mbStats.skipMBs
	t.Logf("--- inter macroblock census (%d frames) ---", nFrames-1)
	t.Logf("  skip MBs: %d (%.1f%%), inter MBs: %d, intra-in-inter MBs: %d",
		mbStats.skipMBs, 100*float64(mbStats.skipMBs)/float64(max(1, total3)),
		mbStats.interMBs, mbStats.intraMBs)
	t.Logf("  bits spent on skip MBs: %.0f (%.2f%% of inter-frame bits), avg %.2f bits each",
		mbStats.skipBits, 100*mbStats.skipBits/interAgg.total(),
		mbStats.skipBits/float64(max(1, mbStats.skipMBs)))
}

func nextAuditFrame(d govid.Demuxer, c govid.Codec) (*govid.Frame, error) {
	for {
		pkt, err := d.NextPacket()
		if err != nil {
			return nil, err
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			return nil, err
		}
		if frame != nil {
			return frame, nil
		}
	}
}

// --- pricing --------------------------------------------------------------

// makeCostTable returns cost-in-bits per (ctx, sym) under the frame's tables.
// The bypass context costs exactly one bit per symbol on both sides.
func makeCostTable(freqs [][]uint32) func(ctx, sym int) float64 {
	costs := make([][]float64, len(freqs))
	for c, f := range freqs {
		if f == nil {
			continue
		}
		costs[c] = make([]float64, len(f))
		for s, fr := range f {
			if fr > 0 {
				costs[c][s] = math.Log2(float64(ransM) / float64(fr))
			}
		}
	}
	return func(ctx, sym int) float64 {
		if ctx == ctxBypass {
			return 1
		}
		return costs[ctx][sym]
	}
}

func entropyBits(counts [][]uint32, freqs [][]uint32) float64 {
	var bits float64
	for c := range counts {
		if c == ctxBypass {
			for _, n := range counts[c] {
				bits += float64(n) // uniform: one bit per symbol
			}
			continue
		}
		if freqs[c] == nil {
			continue
		}
		for s, n := range counts[c] {
			if n > 0 && freqs[c][s] > 0 {
				bits += float64(n) * math.Log2(float64(ransM)/float64(freqs[c][s]))
			}
		}
	}
	return bits
}

// --- tallying ----------------------------------------------------------------

var auditGroupNames = []string{
	"mbmode", "part", "txsize", "cbp", "mode.luma", "mode.chroma", "sign", "escape", "mv", "coef.luma", "coef.chroma",
}

type auditTally struct {
	groups   map[string]float64
	tokenSym [numTokens]float64 // bits by coefficient-token symbol, both planes
}

func newAuditTally() *auditTally { return &auditTally{groups: map[string]float64{}} }

// record tallies a token. Bypass suffix bits carry no group of their own — the
// walker attributes them via recordAs, since only the grammar knows whether a
// raw bit belongs to an MV or an escape.
func (a *auditTally) record(ctx, sym int, bits float64) {
	switch {
	case ctx == ctxMBMode:
		a.groups["mbmode"] += bits
	case ctx == ctxRef:
		a.groups["ref"] += bits
	case ctx == ctxPart:
		a.groups["part"] += bits
	case ctx == ctxTxSize:
		a.groups["txsize"] += bits
	case ctx == ctxCBP:
		a.groups["cbp"] += bits
	case ctx == ctxLumaMode:
		a.groups["mode.luma"] += bits
	case ctx == ctxChromaMode:
		a.groups["mode.chroma"] += bits
	case ctx == ctxSign:
		a.groups["sign"] += bits
	case ctx == ctxEscClass:
		a.groups["escape"] += bits
	case ctx == ctxMVClassX || ctx == ctxMVClassY:
		a.groups["mv"] += bits
	case ctx >= ctxTokenBase:
		if ctx < ctxTokenBase+lumaTokenCtx {
			a.groups["coef.luma"] += bits
		} else {
			a.groups["coef.chroma"] += bits
		}
		a.tokenSym[sym] += bits
	default:
		a.groups["mv"] += bits // unreachable fallback
	}
}

func (a *auditTally) add(b *auditTally) {
	for k, v := range b.groups {
		a.groups[k] += v
	}
	for i := range b.tokenSym {
		a.tokenSym[i] += b.tokenSym[i]
	}
}

func (a *auditTally) total() float64 {
	var t float64
	for _, v := range a.groups {
		t += v
	}
	return t
}

type auditMBStats struct {
	interMBs int
	intraMBs int
	skipMBs  int
	skipBits float64
}

// --- token-stream walker -------------------------------------------------

// auditWalker re-parses a tile's token stream with the decoder's grammar,
// pricing each token as it goes.
type auditWalker struct {
	toks  []entToken
	pos   int
	cost  func(ctx, sym int) float64
	tally *auditTally
	bits  float64
}

func (w *auditWalker) next() (ctx, sym int) {
	tk := w.toks[w.pos]
	w.pos++
	ctx, sym = int(tk.ctx), int(tk.sym)
	b := w.cost(ctx, sym)
	w.bits += b
	w.tally.record(ctx, sym, b)
	return ctx, sym
}

// nextAs consumes a token but attributes its bits to an explicit group — used
// for bypass suffix bits, whose owner only the grammar knows.
func (w *auditWalker) nextAs(group string) (ctx, sym int) {
	tk := w.toks[w.pos]
	w.pos++
	ctx, sym = int(tk.ctx), int(tk.sym)
	b := w.cost(ctx, sym)
	w.bits += b
	w.tally.groups[group] += b
	return ctx, sym
}

// classValue walks a class token plus its suffix bits (attributed to group)
// and returns the decoded value, mirroring getClassValue exactly.
func (w *auditWalker) classValue(group string) uint {
	_, cls := w.next() // class token; record() maps it to its group
	if cls == 0 {
		return 0
	}
	if cls == 1 {
		return 1
	}
	v := uint(1) << uint(cls-1)
	for i := 0; i < cls-1; i++ {
		_, bit := w.nextAs(group)
		v |= uint(bit) << uint(cls-2-i) // suffix is MSB first
	}
	return v
}

func (w *auditWalker) walkStream(t *testing.T, inter bool, stats *auditMBStats) {
	t.Helper()
	for w.pos < len(w.toks) {
		if inter {
			w.walkInterMB(stats)
		} else {
			w.walkIntraMB()
		}
	}
}

func (w *auditWalker) walkIntraMB() {
	_, txSym := w.next() // ctxTxSize
	n := lumaTxSizes[txSym]
	for b := 0; b < (mbSize/n)*(mbSize/n); b++ {
		w.next() // ctxLumaMode
		w.block(n)
	}
	w.next() // ctxChromaMode
	w.block(chromaMB)
	w.block(chromaMB)
}

func (w *auditWalker) walkInterMB(stats *auditMBStats) {
	start := w.bits
	_, mode := w.next() // ctxMBMode
	switch mode {
	case mbSkip:
		stats.skipMBs++
		stats.skipBits += w.bits - start
		return
	case mbIntra:
		stats.intraMBs++
		w.walkIntraMB()
		return
	}
	stats.interMBs++
	w.next() // ctxRef
	_, part := w.next() // ctxPart
	for range partRects[part] {
		w.classValue("mv") // dx
		w.classValue("mv") // dy
	}
	_, cbp := w.next() // ctxCBP
	if cbp&cbpLuma != 0 {
		_, txSym := w.next() // ctxTxSize
		n := lumaTxSizes[txSym]
		for b := 0; b < (mbSize/n)*(mbSize/n); b++ {
			w.block(n)
		}
	}
	if cbp&cbpCb != 0 {
		w.block(chromaMB)
	}
	if cbp&cbpCr != 0 {
		w.block(chromaMB)
	}
}

// block walks one residual block and reports whether it was all-zero
// (immediate EOB).
func (w *auditWalker) block(n int) bool {
	total := n * n
	for i := 0; i < total; i++ {
		_, sym := w.next()
		switch {
		case sym == tEOB:
			return i == 0
		case sym == tZero:
			// keep scanning
		case sym == tEscape:
			w.classValue("escape")
			w.next() // ctxSign
		default: // tOne..tFour
			w.next() // ctxSign
		}
	}
	return false
}

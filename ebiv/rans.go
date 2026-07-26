package ebiv

import (
	"encoding/binary"
	"fmt"
)

// Interleaved range Asymmetric Numeral Systems (rANS) with static per-frame
// frequency tables — the entropy layer from §3.1.
//
// Probabilities never adapt mid-stream: decode is a table lookup plus a
// multiply, with no per-symbol state update to serialize. Several independent
// states are interleaved so the decoder keeps multiple lookups in flight, which
// suits Go's execution model far better than a branchy binary arithmetic coder.
//
// rANS is LIFO: the encoder emits symbols in reverse of decode order, writing
// bytes onto a stack, and the decoder pops them forward. Neighbor-context
// modeling still works because the encoder walks the frame forward to collect
// the symbol sequence, then encodes that sequence backward.
const (
	ransScaleBits = 12
	ransM         = 1 << ransScaleBits // frequency-table total
	ransLower     = 1 << 23            // lower bound of the normalized interval
	ransStates    = 4                  // interleaved states (§3.1, N=4)
)

// ransSym is a symbol's slot in the cumulative frequency line.
type ransSym struct {
	start uint32
	freq  uint32
}

// ransTable is one context's frequency model. enc drives encoding and the
// forward half of decoding; slot2sym is the reverse map decode needs, built
// only for tables that are actually used.
type ransTable struct {
	enc      []ransSym
	slot2sym []uint16
	used     bool
}

// normalizeFreqs scales symbol counts to a distribution summing to exactly
// ransM, giving every symbol that actually occurs a frequency of at least one
// (a zero-frequency symbol could never be decoded). Returns nil for an unused
// context.
func normalizeFreqs(counts []uint32) []uint32 {
	var total uint64
	for _, c := range counts {
		total += uint64(c)
	}
	if total == 0 {
		return nil
	}

	freq := make([]uint32, len(counts))
	var used uint32
	largest := 0
	for s, c := range counts {
		if c == 0 {
			continue
		}
		f := uint32(uint64(c) * ransM / total)
		if f == 0 {
			f = 1
		}
		freq[s] = f
		used += f
		if freq[s] > freq[largest] {
			largest = s
		}
	}

	// Reconcile rounding error against the exact total. Shrinking pulls from
	// whichever symbol currently has the most to give, so no frequency is ever
	// driven below one.
	for used > ransM {
		victim := largestReducible(freq)
		freq[victim]--
		used--
	}
	if used < ransM {
		freq[largest] += ransM - used
	}
	return freq
}

func largestReducible(freq []uint32) int {
	best := -1
	for s, f := range freq {
		if f > 1 && (best < 0 || f > freq[best]) {
			best = s
		}
	}
	if best < 0 {
		// Every present symbol has frequency one; the total cannot exceed
		// ransM in that case, so this is unreachable in practice.
		for s, f := range freq {
			if f > 0 {
				return s
			}
		}
	}
	return best
}

// buildTable turns a frequency vector into a ransTable. A nil or all-zero
// vector yields an unused table.
func buildTable(freq []uint32, forDecode bool) ransTable {
	var t ransTable
	if forDecode {
		t.slot2sym = make([]uint16, ransM)
	}
	buildTableInto(&t, freq, forDecode)
	return t
}

// buildTableInto fills t from a frequency vector, reusing t.enc and t.slot2sym
// backing storage when they are large enough, so a decoder can rebuild its
// per-frame tables without allocating.
func buildTableInto(t *ransTable, freq []uint32, forDecode bool) {
	t.used = false
	if cap(t.enc) >= len(freq) {
		t.enc = t.enc[:len(freq)]
	} else {
		t.enc = make([]ransSym, len(freq))
	}
	var cum uint32
	for s, f := range freq {
		t.enc[s] = ransSym{start: cum, freq: f}
		cum += f
	}
	if cum != ransM {
		return // unused context
	}
	t.used = true
	if forDecode {
		if cap(t.slot2sym) >= ransM {
			t.slot2sym = t.slot2sym[:ransM]
		} else {
			t.slot2sym = make([]uint16, ransM)
		}
		for s, f := range freq {
			for k := uint32(0); k < f; k++ {
				t.slot2sym[t.enc[s].start+k] = uint16(s)
			}
		}
	}
}

// --- Byte stack -------------------------------------------------------------

// ransStack collects encoder output. Encoding pushes bytes; decoding pops them
// from the top, so the two mirror each other exactly.
type ransStack struct {
	buf []byte
}

func (s *ransStack) push(b byte) { s.buf = append(s.buf, b) }

func (s *ransStack) push32(x uint32) {
	s.push(byte(x))
	s.push(byte(x >> 8))
	s.push(byte(x >> 16))
	s.push(byte(x >> 24))
}

// --- Encoder ----------------------------------------------------------------

// entToken is one (context, symbol) pair in decode order.
type entToken struct {
	ctx uint16
	sym uint16
}

// tileStream accumulates the tokens for one tile in decode order.
type tileStream struct {
	toks []entToken
}

func (s *tileStream) put(ctx, sym int) {
	s.toks = append(s.toks, entToken{uint16(ctx), uint16(sym)})
}

// ransEncPut folds one symbol into a state, spilling low bytes onto the stack
// to keep the state within its normalized interval.
func ransEncPut(x uint32, st *ransStack, sym ransSym) uint32 {
	xmax := ((ransLower >> ransScaleBits) << 8) * sym.freq
	for x >= xmax {
		st.push(byte(x))
		x >>= 8
	}
	return ((x / sym.freq) << ransScaleBits) + (x % sym.freq) + sym.start
}

// ransEncode encodes one tile's token stream against the shared tables and
// returns the byte stack. Symbols are processed in reverse; the N states are
// assigned round-robin by decode index so the forward decoder pairs each
// symbol with the same state.
func ransEncode(toks []entToken, tables []ransTable) []byte {
	var st ransStack
	var state [ransStates]uint32
	for i := range state {
		state[i] = ransLower
	}
	for j := len(toks) - 1; j >= 0; j-- {
		t := toks[j]
		sym := tables[t.ctx].enc[t.sym]
		state[j%ransStates] = ransEncPut(state[j%ransStates], &st, sym)
	}
	for i := 0; i < ransStates; i++ {
		st.push32(state[i])
	}
	return st.buf
}

// --- Decoder ----------------------------------------------------------------

// ransDecoder pulls symbols from one tile's byte stream. decode is driven by
// the frame decoder, which requests contexts in the same order the encoder
// produced them. The reader is inlined and the whole struct is reusable across
// frames via reset, so steady-state decode allocates nothing here.
type ransDecoder struct {
	buf     []byte
	pos     int
	tables  []ransTable
	state   [ransStates]uint32
	counter int
	err     error
}

func newRansDecoder(buf []byte, tables []ransTable) (*ransDecoder, error) {
	d := &ransDecoder{}
	return d, d.reset(buf, tables)
}

// reset repoints a decoder at a new byte stream, reinitializing its states.
func (d *ransDecoder) reset(buf []byte, tables []ransTable) error {
	if len(buf) < ransStates*4 {
		return fmt.Errorf("%w: tile stream too short to initialize rANS", ErrCorrupt)
	}
	d.buf = buf
	d.pos = len(buf)
	d.tables = tables
	d.counter = 0
	d.err = nil
	for i := ransStates - 1; i >= 0; i-- {
		d.state[i] = d.pop32()
	}
	return nil
}

func (d *ransDecoder) pop() byte {
	d.pos--
	return d.buf[d.pos]
}

func (d *ransDecoder) pop32() uint32 {
	a := uint32(d.pop())
	b := uint32(d.pop())
	c := uint32(d.pop())
	e := uint32(d.pop())
	return a<<24 | b<<16 | c<<8 | e
}

// decode returns the next symbol from the given context. This is the decoder's
// hottest loop, so it is kept tight: the interleave index is a mask (ransStates
// is a power of two), the byte position is checked once per two renorm bytes
// (a state below ransLower needs at most two), and the frequency table's
// backing slices are hoisted so the compiler can drop their bounds checks.
func (d *ransDecoder) decode(ctx int) int {
	if d.err != nil {
		return 0
	}
	t := &d.tables[ctx]
	if !t.used {
		d.fail(fmt.Errorf("%w: symbol decoded from unused context %d", ErrCorrupt, ctx))
		return 0
	}
	i := d.counter & (ransStates - 1)
	d.counter++

	x := d.state[i]
	slot := x & (ransM - 1)
	sym := t.slot2sym[slot]
	s := t.enc[sym]
	x = s.freq*(x>>ransScaleBits) + slot - s.start
	if x < ransLower {
		// A normalized state drops by at most 8 bits per symbol, so one or two
		// bytes always suffice; guard the buffer once.
		buf, pos := d.buf, d.pos
		for x < ransLower {
			if pos == 0 {
				d.pos = 0
				d.fail(fmt.Errorf("%w: rANS stream underrun", ErrCorrupt))
				return 0
			}
			pos--
			x = (x << 8) | uint32(buf[pos])
		}
		d.pos = pos
	}
	d.state[i] = x
	return int(sym)
}

func (d *ransDecoder) fail(err error) {
	if d.err == nil {
		d.err = err
	}
}

// --- Bypass context ----------------------------------------------------------

// bypassFreqs is the fixed uniform distribution for ctxBypass: a raw bit costs
// exactly one coded bit. It is never counted, serialized, or adapted — both
// sides install it after building the per-frame tables.
var bypassFreqs = []uint32{ransM / 2, ransM / 2}

// installBypass forces the bypass context's table, for encode or decode.
func installBypass(tables []ransTable, forDecode bool) {
	buildTableInto(&tables[ctxBypass], bypassFreqs, forDecode)
}

// --- Table serialization ----------------------------------------------------

// Per-context table modes. Inter frames may code tables relative to the
// previous frame's (the audit measured full per-frame tables at 4% of the
// payload); key frames must be self-contained so seeks stay exact.
const (
	tblUnused = 0 // context did not occur; no table
	tblSame   = 1 // identical to the previous frame's table
	tblFull   = 2 // full frequency vector, uvarint per symbol
	tblDelta  = 3 // per-symbol signed delta vs the previous frame, zigzag varint
)

// serializeTables writes each context's frequency vector, choosing the
// cheapest of full/same/delta against prev (nil prev — a key frame — forces
// full). ctxBypass is never shipped.
func serializeTables(freqs, prev [][]uint32) []byte {
	var out []byte
	var tmp [binary.MaxVarintLen64]byte
	for c := 0; c < numContexts; c++ {
		f := freqs[c]
		if c == ctxBypass || f == nil {
			out = append(out, tblUnused)
			continue
		}
		var pf []uint32
		if prev != nil {
			pf = prev[c]
		}
		if pf != nil && equalFreqs(f, pf) {
			out = append(out, tblSame)
			continue
		}
		fullBuf := make([]byte, 0, len(f)*2)
		for _, v := range f {
			n := binary.PutUvarint(tmp[:], uint64(v))
			fullBuf = append(fullBuf, tmp[:n]...)
		}
		if pf != nil {
			deltaBuf := make([]byte, 0, len(f)*2)
			for s, v := range f {
				n := binary.PutVarint(tmp[:], int64(v)-int64(pf[s]))
				deltaBuf = append(deltaBuf, tmp[:n]...)
			}
			if len(deltaBuf) < len(fullBuf) {
				out = append(out, tblDelta)
				out = append(out, deltaBuf...)
				continue
			}
		}
		out = append(out, tblFull)
		out = append(out, fullBuf...)
	}
	return out
}

func equalFreqs(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// parseTablesInto fills tables (len numContexts) from the serialized form,
// resolving same/delta modes against prev, which it updates in place so the
// next frame can reference this one. key rejects the relative modes and wipes
// prev first, making key frames self-contained (seek safety). ctxBypass is
// installed as uniform regardless of the stream.
func parseTablesInto(tables []ransTable, prev [][]uint32, b []byte, key bool) (int, error) {
	if key {
		// A key frame's tables are self-contained: invalidate every delta
		// reference so a seek that lands here can never read pre-seek state.
		// Length-zero (not nil) keeps the backing arrays, so re-decoding
		// allocates nothing.
		for c := range prev {
			prev[c] = prev[c][:0]
		}
	}
	pos := 0
	for c := 0; c < numContexts; c++ {
		if pos >= len(b) {
			return 0, fmt.Errorf("%w: truncated table modes", ErrCorrupt)
		}
		mode := b[pos]
		pos++
		if c == ctxBypass && mode != tblUnused {
			return 0, fmt.Errorf("%w: bypass context must not ship a table", ErrCorrupt)
		}
		haveRef := len(prev[c]) == alphabetSizes[c]
		switch mode {
		case tblUnused:
			tables[c].used = false
			// prev survives an unused frame: encoder references its last
			// shipped table, not its last frame.
		case tblSame:
			if key || !haveRef {
				return 0, fmt.Errorf("%w: context %d references a previous table that does not exist", ErrCorrupt, c)
			}
			buildTableInto(&tables[c], prev[c], true)
			if !tables[c].used {
				return 0, fmt.Errorf("%w: context %d frequencies do not sum to %d", ErrCorrupt, c, ransM)
			}
		case tblFull:
			f := growFreqs(&prev[c], alphabetSizes[c])
			for s := range f {
				v, n := binary.Uvarint(b[pos:])
				if n <= 0 {
					return 0, fmt.Errorf("%w: truncated frequency table body", ErrCorrupt)
				}
				pos += n
				f[s] = uint32(v)
			}
			buildTableInto(&tables[c], f, true)
			if !tables[c].used {
				return 0, fmt.Errorf("%w: context %d frequencies do not sum to %d", ErrCorrupt, c, ransM)
			}
		case tblDelta:
			if key || !haveRef {
				return 0, fmt.Errorf("%w: context %d deltas a previous table that does not exist", ErrCorrupt, c)
			}
			f := prev[c]
			for s := range f {
				v, n := binary.Varint(b[pos:])
				if n <= 0 {
					return 0, fmt.Errorf("%w: truncated frequency table delta", ErrCorrupt)
				}
				pos += n
				nv := int64(f[s]) + v
				if nv < 0 || nv > ransM {
					return 0, fmt.Errorf("%w: context %d delta out of range", ErrCorrupt, c)
				}
				f[s] = uint32(nv)
			}
			buildTableInto(&tables[c], f, true)
			if !tables[c].used {
				return 0, fmt.Errorf("%w: context %d frequencies do not sum to %d", ErrCorrupt, c, ransM)
			}
		default:
			return 0, fmt.Errorf("%w: unknown table mode %d", ErrCorrupt, mode)
		}
	}
	installBypass(tables, true)
	return pos, nil
}

// growFreqs returns *p resized to n, reusing its backing array when possible.
func growFreqs(p *[]uint32, n int) []uint32 {
	if cap(*p) >= n {
		*p = (*p)[:n]
	} else {
		*p = make([]uint32, n)
	}
	return *p
}

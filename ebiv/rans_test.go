package ebiv

import (
	"math/rand"
	"testing"
)

// buildTablesFrom counts a token stream and returns encode and decode tables
// plus the serialized form, mirroring what the frame encoder does: the bypass
// context is never counted or shipped and is fixed uniform on both sides.
func buildTablesFrom(toks []entToken) (enc, dec []ransTable) {
	counts := make([][]uint32, numContexts)
	for c := range counts {
		counts[c] = make([]uint32, alphabetSizes[c])
	}
	for _, t := range toks {
		counts[t.ctx][t.sym]++
	}
	freqs := make([][]uint32, numContexts)
	enc = make([]ransTable, numContexts)
	for c := 0; c < numContexts; c++ {
		if c == ctxBypass {
			continue
		}
		freqs[c] = normalizeFreqs(counts[c])
		enc[c] = buildTable(freqOrZero(freqs[c], alphabetSizes[c]), false)
	}
	installBypass(enc, false)

	// Round-trip the tables through serialization so the test also covers that.
	blob := serializeTables(freqs, nil)
	dec = make([]ransTable, numContexts)
	prev := make([][]uint32, numContexts)
	if _, err := parseTablesInto(dec, prev, blob, true); err != nil {
		panic(err)
	}
	return enc, dec
}

// TestRansRoundTrip fuzzes the interleaved rANS coder: a random token stream
// over every context must decode back exactly, which is the entropy layer's
// entire contract.
func TestRansRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 200; trial++ {
		n := rng.Intn(4000) + 1
		toks := make([]entToken, n)
		for i := range toks {
			ctx := rng.Intn(numContexts)
			toks[i] = entToken{uint16(ctx), uint16(rng.Intn(alphabetSizes[ctx]))}
		}

		enc, dec := buildTablesFrom(toks)
		stream := ransEncode(toks, enc)

		d, err := newRansDecoder(stream, dec)
		if err != nil {
			t.Fatalf("trial %d: newRansDecoder: %v", trial, err)
		}
		for i, want := range toks {
			got := d.decode(int(want.ctx))
			if d.err != nil {
				t.Fatalf("trial %d symbol %d: decode error: %v", trial, i, d.err)
			}
			if got != int(want.sym) {
				t.Fatalf("trial %d symbol %d (ctx %d): got %d, want %d", trial, i, want.ctx, got, want.sym)
			}
		}
	}
}

// TestRansSingleSymbolContext checks the degenerate table where one symbol
// takes the whole distribution — a real case for a context that only ever sees
// one value.
func TestRansSingleSymbolContext(t *testing.T) {
	toks := make([]entToken, 500)
	for i := range toks {
		toks[i] = entToken{ctxSign, 0} // always symbol 0
	}
	enc, dec := buildTablesFrom(toks)
	stream := ransEncode(toks, enc)
	d, err := newRansDecoder(stream, dec)
	if err != nil {
		t.Fatal(err)
	}
	for i := range toks {
		if got := d.decode(ctxSign); got != 0 {
			t.Fatalf("symbol %d: got %d, want 0", i, got)
		}
	}
}

// TestNormalizeFreqsSumsToTotal asserts the invariant the coder relies on:
// present symbols get a non-zero frequency and the vector sums to exactly ransM.
func TestNormalizeFreqsSumsToTotal(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 1000; trial++ {
		size := rng.Intn(256) + 1
		counts := make([]uint32, size)
		nonZero := 0
		for i := range counts {
			if rng.Intn(3) == 0 {
				counts[i] = uint32(rng.Intn(100000))
				if counts[i] > 0 {
					nonZero++
				}
			}
		}
		freq := normalizeFreqs(counts)
		if nonZero == 0 {
			if freq != nil {
				t.Fatalf("trial %d: empty counts produced a table", trial)
			}
			continue
		}
		var sum uint32
		for i, f := range freq {
			sum += f
			if counts[i] > 0 && f == 0 {
				t.Fatalf("trial %d: present symbol %d got zero frequency", trial, i)
			}
			if counts[i] == 0 && f != 0 {
				t.Fatalf("trial %d: absent symbol %d got frequency %d", trial, i, f)
			}
		}
		if sum != ransM {
			t.Fatalf("trial %d: frequencies sum to %d, want %d", trial, sum, ransM)
		}
	}
}

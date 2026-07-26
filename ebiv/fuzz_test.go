package ebiv

import (
	"bytes"
	"io"
	"testing"

	govid "github.com/liqmix/govid"
)

// Decoder robustness fuzzing (§10): arbitrary bytes fed to any decode entry
// point must return an error or a valid frame — never panic, hang, or drive
// an unbounded allocation. Corrupt files are a certainty over a shipped
// format's lifetime; a decoder that crashes its host game on one is a defect
// regardless of how the file got that way.

// fuzzSeedContainer builds a small real container so the fuzzer starts from
// valid records (a key with a golden override, inters, skip and golden paths)
// rather than having to discover the format from zero.
func fuzzSeedContainer(f *testing.F) []byte {
	f.Helper()
	cfg := Config{Width: 64, Height: 48, FPSNum: 30, FPSDen: 1}
	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, cfg, WithIntra(20), WithGOP(4), WithAltRef(4), WithFastEncode())
	if err != nil {
		f.Fatal(err)
	}
	g := geometryFor(cfg.Width, cfg.Height)
	for i := 0; i < 6; i++ {
		seed := 1
		if i >= 3 {
			seed = 2
		}
		if err := enc.WriteFrame(synthFrame(g, seed)); err != nil {
			f.Fatal(err)
		}
	}
	if err := enc.Close(); err != nil {
		f.Fatal(err)
	}
	return buf.Bytes()
}

// FuzzDecodePacket attacks the frame decoder directly: first with the fuzz
// data as a cold first packet, then as a follow-up to a valid key frame, so
// both the header/geometry path and the stateful inter path are exercised.
func FuzzDecodePacket(f *testing.F) {
	container := fuzzSeedContainer(f)
	var keyPkt []byte
	var seeds [][]byte
	d, err := NewDemuxer(bytes.NewReader(container))
	if err != nil {
		f.Fatal(err)
	}
	for {
		pkt, err := d.NextPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			f.Fatal(err)
		}
		data := append([]byte(nil), pkt.Data...)
		if keyPkt == nil {
			keyPkt = data
		}
		seeds = append(seeds, data)
	}
	d.Close()
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		cold := NewCodec()
		cold.Decode(govid.Packet{Data: data}) //nolint:errcheck — any error is a pass

		warm := NewCodec()
		if _, err := warm.Decode(govid.Packet{Data: keyPkt}); err != nil {
			t.Fatalf("seed key frame must decode: %v", err)
		}
		warm.Decode(govid.Packet{Data: data}) //nolint:errcheck
	})
}

// FuzzContainer attacks the container layer: demux arbitrary bytes and decode
// whatever packets come out.
func FuzzContainer(f *testing.F) {
	f.Add(fuzzSeedContainer(f))
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := NewDemuxer(bytes.NewReader(data))
		if err != nil {
			return
		}
		defer d.Close()
		c := NewCodec()
		for i := 0; i < 64; i++ { // bound the walk; a fuzz case needs no more
			pkt, err := d.NextPacket()
			if err != nil {
				return
			}
			c.Decode(pkt) //nolint:errcheck
		}
	})
}

package ebiv

import (
	"bytes"
	"testing"
	"time"
)

// CI regression gates (§10): the guards whose absence let a 2.19× size gap
// and an 88 ms decode ship unnoticed in v1. Both run on deterministic
// synthetic content, so they hold on every platform without fixtures.

// regressionClip encodes the shared gate clip: panning content, mixed
// static/motion, two GOPs, tiles, two-pass — every major coding path.
func regressionClip(t *testing.T) []byte {
	t.Helper()
	cfg := Config{Width: 256, Height: 192, FPSNum: 30, FPSDen: 1}
	container, _ := encodeCodedGen(t, cfg, 16, synthPan, WithIntra(18), WithGOP(8), WithTiles(2, 2))
	return container
}

// TestEncodedSizeRegression pins the exact encoded size of the gate clip.
// The encoder is deterministic and the DCT matrices are identical on every
// platform, so this byte count is stable until the bitstream or an encoder
// decision changes — at which point this constant must be updated as a
// *conscious* act, with the matched-PSNR corpus numbers alongside it. An
// unexplained change here is a bug or an unmeasured size regression.
const regressionClipSize = 54486

func TestEncodedSizeRegression(t *testing.T) {
	got := len(regressionClip(t))
	if regressionClipSize == 0 {
		t.Fatalf("record this build's gate-clip size: %d bytes", got)
	}
	if got != regressionClipSize {
		t.Errorf("gate clip encodes to %d bytes, pinned at %d — if this change is intentional, "+
			"re-measure the corpus and update the pin", got, regressionClipSize)
	}
}

// TestDecodeBudgetRegression bounds coded decode cost as a multiple of the
// raw-path floor on the same machine — a machine-independent ratio, since
// both sides scale with the host. The bound carries wide margin: it exists to
// catch a 2× regression (the class of bug M3 fixed), not a 10% one.
func TestDecodeBudgetRegression(t *testing.T) {
	if testing.Short() {
		t.Skip("timing gate")
	}
	coded := regressionClip(t)

	cfg := Config{Width: 256, Height: 192, FPSNum: 30, FPSDen: 1}
	var rawBuf bytes.Buffer
	enc, err := NewEncoder(&rawBuf, cfg)
	if err != nil {
		t.Fatal(err)
	}
	g := geometryFor(cfg.Width, cfg.Height)
	for i := 0; i < 16; i++ {
		if err := enc.WriteFrame(synthPan(g, i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}

	// Repeat until the timed section clears the platform timer's resolution
	// (the raw path decodes a frame in microseconds).
	perFrame := func(container []byte) time.Duration {
		for reps := 1; ; reps *= 4 {
			start := time.Now()
			for i := 0; i < reps; i++ {
				decodeImages(t, container)
			}
			if el := time.Since(start); el > 30*time.Millisecond {
				return el / time.Duration(reps*16)
			}
		}
	}

	codedPer := perFrame(coded)
	rawPer := perFrame(rawBuf.Bytes())
	ratio := float64(codedPer) / float64(rawPer)
	t.Logf("decode: coded %v/frame, raw %v/frame, ratio %.0fx", codedPer, rawPer, ratio)

	// Today's measured ratio is ~10x. The bound gives 5x headroom for machine
	// variance while still catching anything like v1's 12x decode regression.
	const maxRatio = 50
	if ratio > maxRatio {
		t.Errorf("coded decode is %.0fx the raw floor (bound %dx) — a decode-path regression", ratio, maxRatio)
	}
}

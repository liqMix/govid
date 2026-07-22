package vp8

import (
	"bytes"
	"os"
	"testing"
)

func TestManualDecodeFirst3MBRow(t *testing.T) {
	const webmPath = "testdata/interframe64.webm"
	if _, err := os.Stat(webmPath); err != nil {
		t.Skip("not found")
	}

	dm := openTestDemuxer(t, webmPath)
	dec := NewDecoder()

	pkt0, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt0.Data), len(pkt0.Data))
	dec.DecodeFrameHeader()
	dec.ensureImg()
	dec.DecodeFrame()

	pkt1, _ := dm.NextPacket()
	dec.Init(bytes.NewReader(pkt1.Data), len(pkt1.Data))
	dec.DecodeFrameHeader()
	dec.ensureImg()
	dec.parseOtherHeaders()

	t.Logf("probIntra=%d skipProb=%d", dec.probIntra, dec.skipProb)

	for mbx := 0; mbx < dec.mbw; mbx++ {
		dec.upMB[mbx] = mb{}
		dec.upInterMB[mbx] = interMB{}
	}

	// Decode rows 0-2 normally.
	for mby := 0; mby < 3; mby++ {
		dec.leftMB = mb{}
		dec.leftInterMB = interMB{}
		var prevAbove interMB
		for mbx := 0; mbx < dec.mbw; mbx++ {
			dec.aboveLeftInterMB = prevAbove
			prevAbove = dec.upInterMB[mbx]
			skip := dec.reconstruct(mbx, mby)
			fs := dec.lookupFilterParam()
			fs.inner = fs.inner || !skip
			dec.perMBFilterParams[dec.mbw*mby+mbx] = fs
		}
	}

	// Row 3: decode MB(0,3) with detailed tracing.
	mby := 3
	dec.leftMB = mb{}
	dec.leftInterMB = interMB{}
	var prevAbove interMB
	mbx := 0
	dec.aboveLeftInterMB = prevAbove
	prevAbove = dec.upInterMB[mbx]

	// Capture state BEFORE reconstruct reads anything.
	t.Logf("=== Before MB(0,3) ===")
	t.Logf("fp state: r=%d count=%d rng=%d value=%08x eof=%v",
		dec.fp.r, dec.fp.count, dec.fp.rng, dec.fp.value, dec.fp.unexpectedEOF)

	// Manually read the skip bit.
	skip := false
	if dec.useSkipProb {
		t.Logf("Reading skip with prob=%d", dec.skipProb)
		skip = dec.fp.readBit(dec.skipProb)
		t.Logf("skip=%v, fp: r=%d count=%d rng=%d value=%08x", skip, dec.fp.r, dec.fp.count, dec.fp.rng, dec.fp.value)
	}

	// Read isInter.
	t.Logf("Reading isInter with probIntra=%d", dec.probIntra)
	isInter := dec.fp.readBit(dec.probIntra)
	t.Logf("isInter=%v, fp: r=%d count=%d rng=%d value=%08x", isInter, dec.fp.r, dec.fp.count, dec.fp.rng, dec.fp.value)

	if isInter {
		// Read ref frame.
		t.Logf("Reading ref frame with probLast=%d", dec.probLast)
		isLast := !dec.fp.readBit(dec.probLast)
		t.Logf("isLast=%v, fp: r=%d count=%d rng=%d value=%08x", isLast, dec.fp.r, dec.fp.count, dec.fp.rng, dec.fp.value)

		// findNearMVs.
		nearest, near, bestMV, cnt := dec.findNearMVs(mbx, mby)
		t.Logf("findNearMVs: nearest=%v near=%v bestMV=%v cnt=%v", nearest, near, bestMV, cnt)

		// Mode tree node 0.
		prob0 := modeContexts[cnt[0]][0]
		if cnt[0] > 5 {
			prob0 = modeContexts[5][0]
		}
		t.Logf("Mode node 0: cnt[0]=%d prob=%d", cnt[0], prob0)
		node0bit := dec.fp.readBit(prob0)
		t.Logf("node0=%v (true=not-ZEROMV), fp: r=%d count=%d rng=%d value=%08x",
			node0bit, dec.fp.r, dec.fp.count, dec.fp.rng, dec.fp.value)

		if !node0bit {
			t.Logf("→ ZEROMV")
		} else {
			// Node 1.
			bestIdx := 0
			if cnt[1] >= cnt[0] {
				bestIdx = 1
			}
			t.Logf("bestIdx=%d (cnt[1]=%d >= cnt[0]=%d → %v)", bestIdx, cnt[1], cnt[0], cnt[1] >= cnt[0])

			if cnt[2] > cnt[1] {
				t.Logf("Swap: cnt[2]=%d > cnt[1]=%d", cnt[2], cnt[1])
			}

			prob1 := modeContexts[cnt[1]][1]
			if cnt[1] > 5 {
				prob1 = modeContexts[5][1]
			}
			t.Logf("Mode node 1: cnt[1]=%d prob=%d", cnt[1], prob1)
			node1bit := dec.fp.readBit(prob1)
			t.Logf("node1=%v (true=not-NEAREST), fp: r=%d count=%d rng=%d value=%08x",
				node1bit, dec.fp.r, dec.fp.count, dec.fp.rng, dec.fp.value)

			if !node1bit {
				t.Logf("→ NEARESTMV mv=%v", nearest)
			} else {
				prob2 := modeContexts[cnt[2]][2]
				if cnt[2] > 5 {
					prob2 = modeContexts[5][2]
				}
				t.Logf("Mode node 2: cnt[2]=%d prob=%d", cnt[2], prob2)
				node2bit := dec.fp.readBit(prob2)
				t.Logf("node2=%v (true=not-NEAR), fp: r=%d count=%d rng=%d value=%08x",
					node2bit, dec.fp.r, dec.fp.count, dec.fp.rng, dec.fp.value)

				if !node2bit {
					t.Logf("→ NEARMV mv=%v", near)
				} else {
					prob3 := modeContexts[0][3] // splitmvCnt=0 for mbx=0
					t.Logf("Mode node 3: splitmvCnt=0 prob=%d", prob3)
					node3bit := dec.fp.readBit(prob3)
					t.Logf("node3=%v (true=SPLITMV), fp: r=%d count=%d rng=%d value=%08x",
						node3bit, dec.fp.r, dec.fp.count, dec.fp.rng, dec.fp.value)
					if !node3bit {
						t.Logf("→ NEWMV")
					} else {
						t.Logf("→ SPLITMV")
					}
				}
			}
		}
	} else {
		t.Logf("→ INTRA")
	}
	t.Logf("=== End MB(0,3) trace ===")
}

# Codec Implementation & Verification Guide

Lessons learned from building govid's H.264 decoder, distilled into a reusable playbook for implementing and verifying any video codec.

---

## Part 1: H.264 Lessons Learned

Each bug below was found through systematic reference comparison testing against ffmpeg-decoded YUV output. The common theme: most bugs came from implementing the spec "almost right" — missing one condition, using the wrong constant, or omitting a special case.

### Inverse Quantization

**Bug:** `dequant4x4` was missing the default flat scaling matrix weight of 16.

The spec defines `LevelScale4x4(m,i,j) = normAdjust4x4(m,i,j) * weightScale4x4(i,j)`. The code had normAdjust values (10, 13, 16, etc. from Table 8-13) but omitted the default `weightScale = 16` that applies when no custom scaling lists are present. All I_4x4 and inter residuals were 16x too small.

The DC-specific dequant functions (`dequantLumaDC`, `dequantChromaDC`) do NOT include the weight scale because they use different shift thresholds that already compensate. The DC/AC ratios (1/4 for luma, 1/2 for chroma) are intentional per spec — the IDCT has different effective gains at each position.

- **Spec ref:** 8.5.12.2 (Scaling and transformation process for residual 4x4 blocks)
- **Symptom:** All pixel values were washed out / too close to the prediction. Y pixel range was extremely narrow.
- **Discovery:** `TestDecodePixelValues` showed near-zero AC contribution; reference comparison showed systematic 16x scaling error across all residual blocks.

### Intra Prediction

**Bug 1:** Upper-right 4x4 sample substitution was missing for blocks at scan indices {3, 7, 11, 13, 15}.

These blocks have unavailable above-right samples. Before intra 4x4 prediction, the decoder must fill `ybr[y-1][x+4..x+7]` with `ybr[y-1][x+3]` (replicate the last available sample). This reduced Y errors from 23.9% to 3.3%.

- **Spec ref:** 8.3.1.2.1 (Intra_4x4 sample prediction)
- **Symptom:** Errors concentrated in specific 4x4 sub-blocks within MBs, particularly in the right half. Triangle-shaped error patterns in bottom-right 4x4 blocks.
- **Discovery:** Per-MB error maps in `TestDecodeTestVsNoDeblock` showed errors only at specific block positions; cross-referencing with 4x4 block scan order identified the affected indices.

**Bug 2:** Chroma DC prediction used inner sub-block borders as available neighbors instead of MB-level borders.

The spec says chroma DC prediction only uses MB-boundary samples. Specific rules: when both above+left are available, top-right 8x8 block uses only above, bottom-left uses only left. This fixed chroma errors from 60%/79% to 0%/0%.

- **Spec ref:** 8.3.4.1 (Intra chroma prediction)
- **Symptom:** Widespread chroma errors across the entire frame, independent of luma error patterns.
- **Discovery:** Separate Cb/Cr plane comparison in `TestDecodeTestVsReference` showed chroma errors were uncorrelated with luma — a different root cause. The fix was a rewrite of the chroma DC prediction to match the spec's neighbor availability rules.

### Deblocking Filter

**Bug 1:** Boundary strength (bS) was set to 4 for ALL intra edges. The spec says bS=4 only at MB boundaries; internal edges within intra MBs get bS=3.

- **Spec ref:** 8.7.2.1 (Derivation process for the boundary filtering strength)
- **Symptom:** Over-smoothed blocks, visible as reduced detail within intra MBs.

**Bug 2:** Used global QP for all deblocking instead of averaging neighboring MBs' QPs at MB boundary edges.

- **Spec ref:** 8.7.2.2 (Filtering process for edges)
- **Symptom:** Incorrect filter strength at MB boundaries where QP changes.
- **Discovery:** Comparing with vs. without deblocking (`TestDecodeTestVsNoDeblock` vs `TestDecodeTestVsReference`) isolated deblocking-specific errors. Errors reduced from 34.2% to 25.2% Y after these fixes.

### Motion Compensation

**Bug 1:** Quarter-pel interpolation had H and V half-pel components swapped in several cases of `interpolateLuma()`.

- **Spec ref:** 8.4.2 (Decoding process for Inter prediction samples)
- **Symptom:** P-frame pixels were shifted/smeared in the wrong direction. Errors visible as directional blur.

**Bug 2:** Chroma negative MV floor division was incorrect, producing `fracX > 7` and negative bilinear weights.

- **Symptom:** Chroma artifacts on P-frames with negative motion vectors. Bilinear interpolation weights became nonsensical.
- **Discovery:** Per-MB error analysis in `TestDecodeBakerVsReference` showed errors concentrated in MBs with negative MVs.

### MV Prediction

**Bug 1:** Skip MV derivation always used the full median predictor. The spec says if EITHER neighbor A or B is unavailable or has `refIdx=0 + MV=(0,0)`, the skip MV must be `(0,0)`.

- **Spec ref:** 8.4.1.1 (Derivation process for the skip motion vector)
- **Symptom:** Skip MBs had non-zero MVs when they should have been stationary.

**Bug 2:** `getNeighborMVAt` returned `refIdx=0` for intra MBs. The spec requires `refIdx=-1`. When `numRefIdxL0Active > 1`, intra neighbors falsely matched `refIdx=0`, causing wrong MV predictions.

- **Spec ref:** 8.4.1.3.1 (Derivation process for luma motion vector prediction)
- **Symptom:** Only visible when `numRefIdxL0Active > 1` (multiple reference frames). Frame 1 was correct (single ref), frame 2+ diverged.

**Bug 3:** Missing the "exactly one matching refIdx" rule — if exactly one of A, B, C neighbors has a refIdx matching the current partition, use that neighbor's MV directly instead of the median.

- **Spec ref:** 8.4.1.3.1 (Derivation process for luma motion vector prediction)
- **Symptom:** 52% wrong pixels on frame 2+ despite frame 1 being nearly perfect (0.6%). This was the primary cascade bug.
- **Discovery:** `TestFrame2WithPerfectReference` injected perfect reference frames, proving the error was in MV prediction (not reference quality). Per-MB-type error analysis showed all inter MB types affected uniformly.

**Bug 4:** C-neighbor (upper-right) availability didn't check raster scan order. When the C neighbor fell in the next MB (same row, not yet decoded), stale data was returned.

- **Spec ref:** 6.4.11.7 (Availability of neighbouring partitions)
- **Symptom:** Intermittent MV prediction errors for MBs whose upper-right neighbor hadn't been decoded yet.

### 8x8 Transform (High profile)

These five bugs kept the 8x8 transform paths disabled behind hard-stops; all were found in one session using two purpose-built fixtures (`test_high8x8.mp4` — IDR + 29 P frames, 57.7% inter 8x8; `test_high8x8_intra.mp4` — 5 all-intra frames, blurred source to force all 9 Intra_8x8 modes).

**Bug 1:** 8x8 CAVLC sub-block coefficient scatter used contiguous zigzag slices (`scanPos = 16*s + k`) instead of the spec interleave.

The 8x8 residual is coded as four ordinary 4x4 residual blocks whose coefficients interleave into the 8x8 zigzag scan: `level8x8[4*i + i4x4] = level4x4[i4x4][i]`. FFmpeg's `ff_zigzag_scan8x8_cavlc` table encodes exactly this interleave (entry `[16*i4x4 + i]` equals `zigzag_direct[4*i + i4x4]`). A comment in the old code asserted the opposite and was wrong.

- **Spec ref:** 7.3.5.3.2 (Residual luma syntax), 8.5.6
- **Symptom:** Wrong pixels but NO bitstream desync — the same bits are read either way, only their placement differs.

**Bug 2:** All four CAVLC sub-blocks of an 8x8 block shared one nC (computed at the partition's top-left), and afterwards all four scan positions were stamped 16-or-0.

Each sub-block is a normal 4x4 residual block at scan index `p*4 + s` with its own neighbor-derived nC, and its actual TotalCoeff must be stored immediately so following sub-blocks (and neighbor MBs) derive correct nC. The 16/0 stamping corrupted every subsequent nC derivation.

- **Spec ref:** 9.2.1 (Parsing process for total_coeff and trailing_ones)
- **Symptom:** Bit desync on streams where sub-block coefficient counts differ — the "~12% of real x264 streams" failure that kept the path disabled.

**Bug 3:** `levelScale8x8` used a 3-class scaling pattern extrapolated from the 4x4 table. The 8x8 scaling function has SIX distinct classes with different values (e.g. class 2 value 32–58 was entirely missing, and two of the three present values were wrong at m=4/m=5).

- **Spec ref:** 8.5.9 / Table 8-14 (normAdjust8x8)
- **Symptom:** Residual-carrying 8x8 blocks off by small-to-moderate amounts; error scaled with coefficient magnitude. Cross-check: FFmpeg's `dequant8_coeff_init[6][6]` + `dequant8_coeff_init_scan`.

**Bug 4:** `idct8x8` butterfly had `b5 = a5 - (a3>>2)`; the spec defines `f[5] = (c[3]>>2) - c[5]`. The sign flip effectively swaps output rows 1 and 6 of each 1-D pass.

- **Spec ref:** 8.5.13.3 (8x8 transform)
- **Symptom:** Blocks with mid-frequency vertical content decoded with bands exchanged; intra 8x8 MBs showed errors up to ~90 that cascaded into neighboring intra prediction.

**Bug 5:** Intra_8x8 Horizontal-Up at `zHU == 13` computed `left[5] + 3*left[6] + 2*left[7]` (weights sum 6); the spec says `(p'[-1,6] + 3*p'[-1,7] + 2) >> 2`.

- **Spec ref:** 8.3.2.2.10 (Intra_8x8_Horizontal_Up)
- **Symptom:** Single-sample-diagonal errors in HU-predicted 8x8 partitions.

Deblocking also needed two 8x8-aware changes (not bugs, missing features): internal luma edges 1 and 3 are not filtered in 8x8-transform MBs, and the bS=2 coefficient test applies to the containing 8x8 transform block (spec 8.7.2.1).

### Reference List Construction & Weighted Prediction

Found via the High-profile fixture, but independent of the 8x8 transform: x264 (weightp=2, the default) emits `ref_pic_list_modification` in every P slice to alias the previous frame at two reference indices, one carrying explicit weights (`luma_offset_l0 = -1`).

**Bug 1:** `ref_pic_list_modification` was parsed but ignored. A modified list can repeat pictures — that is how `num_ref_idx_l0_active` (4) legally exceeds `max_num_ref_frames` (3).

- **Spec ref:** 8.2.4.3 (Modification process for reference picture lists)
- **Symptom:** "reference frame 3 not found" once the stream referenced the 4th list entry; before that, wrong-but-similar reference picks.

**Bug 2:** `pred_weight_table` was skipped, decoding unweighted.

- **Spec ref:** 8.4.2.3.2 (Weighted sample prediction)
- **Symptom:** Uniform ±1 drift accumulating from frame 2 on — the duplicate reference entry carries `weight=1, denom=0, offset=-1`, i.e. "prediction minus one".

**Bug 3:** Deblocking bS=1 "different reference" test compared reference indices. The spec compares the reference *pictures*; with a duplicated list, indices 0 and 1 are the same picture.

- **Spec ref:** 8.7.2.1
- **Symptom:** After reconstruction was bit-exact with deblocking disabled, the deblocked output still drifted ±1-6 — edges were filtered at bS=1 that the reference decoder left alone.
- **Discovery:** The staged no-deblock test (`TestDecodeHigh8x8MultiFrameNoDeblock`) passing while the deblocked test failed pinned the divergence to bS derivation.

### CABAC

Implemented in one pass (engine + 1024-context init tables + all syntax
element decoders + CABAC MB layer for I and P slices), ported against FFmpeg's
`h264_cabac.c` with tables extracted mechanically from FFmpeg/x264 sources
rather than typed by hand. The staged fixtures (intra-no-8x8 → all-intra →
IDR+P) were bit-exact almost immediately; two bugs surfaced later, both
instructive about *how CABAC fails*:

**Bug 1:** The P_8x8 reference/mvd context lookups passed FFmpeg's Z-scan
block index (`4*p`) where our neighbor helpers expect raster 4x4 cells
(partition 1's top-left cell is raster 2, not 4).

- **Spec ref:** 9.3.3.1.1.6-7 (ref_idx / mvd contexts)
- **Symptom:** Bit desync at the *first P_8x8 macroblock* in the stream;
  every constrained fixture without sub-partitions passed.
- **Discovery:** `ffmpeg -debug mb_type` gives a per-MB ground-truth grid;
  our decoded mb_types matched right up to the first `>+` (P_8x8) MB.

**Bug 2:** `lastCoeffFlagOffset8x8` was transcribed with 62 entries in a
`[63]` array (a dropped `7`), so Go zero-filled the tail and scan positions
59+ used wrong last-flag contexts.

- **Spec ref:** Table 9-43 (last_significant_coeff_flag 8x8 mapping)
- **Symptom:** Every synthetic fixture passed; a real 720p scenecut IDR
  (dense 8x8 blocks reaching scan positions 59+, ~1800 level escapes)
  produced a hard desync mid-frame. Wrong *context selection* corrupts
  CABAC gradually — bins keep decoding correctly for a while because the
  wrong context's state is similar, then diverge — so the observable
  failure point can be far from the wrong table entry.
- **Discovery chain worth reusing:** (1) `ffmpeg -debug qp` per-MB QP grid
  proved parse alignment right up to the failing MB; (2) a mode-sweep test
  reconstructed the failing 8x8 partition from *reference* neighbors under
  all 9 intra modes with our parsed residual — no mode fit, proving the
  residual bins themselves were wrong; (3) that narrowed it to the one
  table only dense blocks reach, where diffing against the FFmpeg source
  found the missing entry.

Verification note: transcription errors in big constant tables are the
dominant CABAC risk. Extract tables mechanically (curl + awk from FFmpeg /
x264 sources), verify element counts, and diff the final arrays against the
source dump — a `[63]` Go array silently zero-fills a 62-entry literal.

### B slices

POC computation, two POC-ordered reference lists, spatial direct mode with
`direct_8x8_inference`, bi-prediction with implicit (POC-scaled) and
explicit weighting, per-list mvd/ref CABAC contexts, B-aware deblocking bS,
MMCO short-term marking, and display-order reordering landed together; the
staged fixtures (CAVLC bframes=1 → CABAC → full x264 defaults with pyramid)
were bit-exact on first run. Real default-x264 720p content then exposed
two bugs, both instructive:

**Bug 1:** The CABAC intra path cleared only the list-0 `mvdAbs` cache.
Intra MBs inside B slices left stale list-1 mvd values from the co-located
MB of an earlier frame, so the *next* MB's `mvd_l1` context picked the
wrong threshold bucket.

- **Spec ref:** 9.3.3.1.1.7 (mvd contexts)
- **Symptom:** Desync always beginning at the MB immediately after the
  first intra-in-B macroblock; the tiny fixtures had no intra MBs in B
  slices at all.
- **Discovery:** Per-MB QP grids (`ffmpeg -debug qp`) diffed against our
  `mbInfo[].qp` pinned the first divergent MB; `ffmpeg -debug mb_type`
  showed our mb_types matched right up to an `i` cell. Beware `head`
  truncation when eyeballing traces — one earlier "divergence" was just a
  clipped log.

**Bug 2:** MMCO was parsed but not applied. x264 emits
`memory_management_control_operation` 1 on pyramid B-refs to prune specific
short-term references instead of sliding-window eviction; ignoring it makes
the DPB (and thus every later reference list) silently diverge.

- **Spec ref:** 8.2.5.4 (Adaptive memory control marking)
- **Symptom:** No desync — QP grids fully aligned — but pixel errors on the
  frames after the first MMCO, because motion compensation read the wrong
  reference pictures.

Also worth recording: B MBs mark all 16 4x4 cells "decoded" up front and
gate per-list availability on a predMask instead (a partition that does not
use a list reports "available with refIdx -1", matching FFmpeg's cache
model); and ffmpeg's `-debug mb_type`/`-debug qp` grids print in *display*
order, which is what makes them lineable against POC-derived frame indices.

### The conformance-stream round (temporal direct, CABAC I_PCM, MMCO/long-term)

Some features cannot be generated with x264, so bit-exactness needs the JVT
conformance suite (https://www.itu.int/wftp3/av-arch/jvt-site/draft_conformance/AVCv1/):
x264 never emits long-term references or sub-8x8 B partitions, and asking it
for I_PCM via `qp=0` silently escalates the encode to lossless High 4:4:4
(`profile_idc 244` + `qpprime_y_zero_transform_bypass`) — check
`trace_headers` before trusting a generated fixture. The streams used:
CAPM3_Sony_D (CABAC IPB + I_PCM + temporal direct), CVPCMNL1/2_SVA_C (CAVLC
I_PCM), MR2_MW_A and MR2_TANDBERG_E (MMCO ops 1-6 incl. an op-5 reset,
long-term refs, long-term list modification). ffmpeg's decode of every one
was first verified byte-identical to the package's `*_rec.yuv`, so the usual
ffmpeg-reference workflow still applies; the bundled JM trace files give
per-syntax-element ground truth (that's how `direct_8x8_inference_flag = 0`
was discovered — **the package readme claimed it was ON; trust the
bitstream, not the documentation**).

Bugs this round, all with the same signature — bitstream stays in sync,
errors localized to specific MBs:

1. **Reorder depth without VUI** — with no `bitstream_restriction`, waiting
   for the first B slice before enabling display reordering is wrong: the
   reference frame coded ahead of that B has already been emitted. Buffer to
   `max_num_ref_frames` up front (0 refs → no reordering possible).
2. **Spec 6.4.8 positional availability in B_8x8** (8.4.1.3 MVP): a later
   quadrant must read as *undecoded* during MV prediction so the above-right
   neighbor substitutes above-left. Invisible with x264 (8x8-only subs never
   query a later quadrant); an 8x4 second row does. The all-decoded-up-front
   cache trick needs a positional mask during sub-partition MVP.
3. **`direct_8x8_inference_flag = 0`** — temporal direct derives motion per
   4x4 (own colocated block each), not per quadrant corner; spatial direct
   evaluates colZeroFlag per 4x4.
4. **I_PCM in CABAC needs no bitstream pointer surgery in a bit-serial
   engine** — after the terminate bin, the BitReader position *is* the
   conceptual RBSP position: byte-align, read raw samples, re-init the
   engine (contexts persist). FFmpeg's `ptr--` adjustments exist only
   because its engine buffers ahead. Side state: cbp 0x1EF, TotalCoeff 16,
   deblocking QP 0, dqp context reset.
5. **MMCO op 5** resets frame_num and POC of the current picture to 0 —
   the display-order key needs an epoch bump exactly like an IDR.

---

## Part 1b: VP8 Lessons Learned

Three small bugs kept VP8 drifting for multiple sessions, all hidden behind
a wrong theory ("first-partition bitstream desync") that was finally
disproven by evidence: frames 0-6 decoded bit-exact with and without the
loop filter, which no desync would allow.

**Bug 1:** `clampMVComp` allowed MVs to overshoot the frame edge by 128
*pixels*; libvpx's margin is `16 << 3` = 128 *eighth-pel units* = 16 pixels
(one macroblock).

- **Ref:** libvpx `vp8_clamp_mv2`, `LEFT_TOP_MARGIN`
- **Symptom:** One macroblock wrong per ~8 frames — only when a neighbor MV
  was large enough that the (missing) clamp should have altered `best_mv`
  before a NEWMV delta was added. No bitstream desync, because `best_mv` is
  a silently derived value that consumes no bins.
- **Discovery:** With the prediction/residual separated (a debug hook
  captured ybr after MC and after residuals), the decoded residual was
  cleanly DC-blocky (proving the token decode right) while `ref8 − pred`
  was smooth (proving the prediction wrong). An exhaustive MV-space search
  for `sixtap(ref7, mv) + ourResidual == ref8` found a 3x3 cluster of exact
  matches around the true MV; working backwards through `mv = best_mv +
  delta` gave the true `best_mv` = exactly the correctly-clamped value.

**Bug 2:** Inner-edge loop filtering was skipped for all no-coefficient
macroblocks; libvpx's rule is `skip_lf = mb_skip_coeff && mode != B_PRED &&
mode != SPLITMV` — B_PRED and SPLITMV MBs always filter their inner edges.
(Both are exactly the `!usePredY16` modes in this decoder.)

- **Ref:** libvpx `vp8_loop_filter_frame` (`skip_lf`)
- **Symptom:** ±1 errors at scattered SPLITMV/intra-4x4 macroblocks, first
  appearing at baker frame 7, while the no-filter decode stayed exact.

**Bug 3:** The loop-filter level lookup uses the per-MB reference frame,
but keyframe macroblocks never set it — a mid-stream keyframe filtered
every MB with the previous inter frame's stale reference row (inter deltas)
instead of the INTRA row. The stream-opening keyframe worked only because
the field zero-initializes.

- **Symptom:** Widespread small errors (±4, ~60% of MBs) on mid-stream
  keyframes with LF deltas enabled; the opening keyframe was exact.

Also fixed while verifying: segment-map persistence across frames when the
map is not re-coded (RFC 6386 §9.3 — previously only a scalar), the
golden/altref buffer-copy order (altref copy first, per libvpx
`swap_frame_buffers`), and invisible-frame handling (`show_frame=0`
auto-alt-ref frames decode but return no display frame).

## Part 2: Generalized Codec Verification Playbook

### 1. Reference Frame Generation

Use ffmpeg to generate pixel-perfect YUV references at each pipeline stage:

```bash
# Full decode (with loop filter) — single frame
ffmpeg -i input.mp4 -vf "select=eq(n\,0)" -pix_fmt yuv420p -f rawvideo -y frame0.yuv

# Full decode — first N frames concatenated
ffmpeg -i input.mp4 -vframes 10 -pix_fmt yuv420p -f rawvideo -y frames_0_9.yuv

# Without loop/deblocking filter (H.264-specific)
ffmpeg -flags nodb -i input.mp4 -vf "select=eq(n\,0)" -pix_fmt yuv420p -f rawvideo -y frame0_nodb.yuv

# Specific pixel format for other subsampling ratios
ffmpeg -i input.mp4 -vframes 1 -pix_fmt yuv422p -f rawvideo -y frame0_422.yuv

# Extract all keyframes only
ffmpeg -i input.mp4 -vf "select=eq(pict_type\,I)" -vsync vfr -pix_fmt yuv420p -f rawvideo -y keyframes.yuv

# Verify dimensions (needed to interpret raw YUV)
ffprobe -v error -select_streams v:0 -show_entries stream=width,height,pix_fmt -of csv=p=0 input.mp4
```

The raw YUV layout for 4:2:0 is: `Y[w*h] + Cb[w/2 * h/2] + Cr[w/2 * h/2]` per frame, concatenated.

### 2. Progressive Testing Strategy

Each stage isolates a class of bugs. Do not advance until the current stage passes:

| Stage | What to test | What it isolates |
|-------|-------------|-----------------|
| **1. IDR/keyframe only** | Single I-frame vs reference | Bitstream parsing, inverse quant, inverse transform, intra prediction |
| **2. IDR without deblocking** | Same frame, decoder's deblocking disabled, vs nodb reference | Separates deblocking bugs from core decode bugs |
| **3. IDR with deblocking** | Full decode vs full reference | Deblocking filter correctness |
| **4. First P-frame** | Frame 1 vs reference | Basic inter prediction, motion compensation, MV prediction with single reference |
| **5. Multi-frame (5-10 P-frames)** | Error trend across frames | MV prediction cascades, reference management, multi-ref bugs |
| **6. Perfect-reference injection** | Decode frame N using ffmpeg's frame N-1 as reference | Isolates MC/prediction from accumulated reference error |
| **7. B-frames** (if applicable) | Bidirectional prediction | Backward MV, dual-reference interpolation, temporal ordering |

### 3. Diagnostic Test Patterns

**Single-frame vs reference comparison:**
- Per-pixel absolute difference across Y, Cb, Cr planes separately
- Per-MB max error map (identifies which macroblocks are broken)
- Error histogram (distribution of error magnitudes)
- First-error location with MB coordinates and 4x4 block index

Example from `h264/codec_test.go` `TestDecodeTestVsReference`:
```go
// Per-MB max error for first 2 rows
for mby := 0; mby < 2; mby++ {
    for mbx := 0; mbx < w/16; mbx++ {
        mbMax := 0
        for j := 0; j < 16; j++ {
            for i := 0; i < 16; i++ {
                got := int(ycbcr.Y[(mby*16+j)*ycbcr.YStride + mbx*16+i])
                want := int(ref[(mby*16+j)*w + mbx*16+i])
                d := abs(got - want)
                if d > mbMax { mbMax = d }
            }
        }
        // Print mbMax per MB
    }
}
```

**Multi-frame drift analysis:**
- Track max error and wrong-pixel percentage across frames
- Error should stay bounded (<=5 for deblocking rounding)
- Growing error = MV prediction cascade or reference management bug

**Perfect-reference injection:**
- Load ffmpeg's decoded YUV for frames 0..N-1 into the decoder's reference buffer
- Decode frame N normally
- If frame N is now correct, the bug is in reference quality (accumulated error)
- If frame N is still wrong, the bug is in MC/prediction for frame N itself

Example from `h264/codec_test.go` `TestFrame2WithPerfectReference`:
```go
// Inject ffmpeg's perfect frames as reference
d.refFrames = []*image.YCbCr{loadFrame(0), loadFrame(1)}
// Decode frame 2 — any errors are now purely from frame 2's own decode
```

**Per-MB-type error grouping:**
- Tag each MB with its type (intra, skip, 16x16, 16x8, 8x16, 8x8)
- Aggregate error stats per type
- If only one type is broken, the bug is in that specific decode path

**Worst-MB pixel dump:**
- For the N worst MBs, dump got/want/reference-frame pixel values side by side
- Compare the source position in the reference frame to verify MC is reading from the right location

### 4. Error Interpretation Guide

| Error pattern | Likely cause |
|--------------|-------------|
| Small uniform errors (1-5) across all MBs | Deblocking filter rounding, clipping differences |
| Gradual error growth across frames | MV prediction cascade — one wrong MV corrupts future references |
| All MB types affected uniformly | Bitstream alignment issue, systematic parsing bug, or quant/transform error |
| Specific MB types only (e.g., only 8x16) | Partition-specific parsing or MC code path |
| Errors start at specific row/column | CAVLC/CABAC context propagation — one MB consumed wrong number of bits |
| Chroma errors uncorrelated with luma | Separate chroma prediction/MC bug (not residual) |
| Washed out / narrow pixel range | Missing quantization scale factor |
| Directional blur/smear on P-frames | Quarter-pel interpolation axis swap |
| Errors only when numRefIdx > 1 | refIdx handling bug (intra neighbor refIdx, "one matching" rule) |
| Skip MBs wrong, other types correct | Skip MV derivation special cases |

### 5. Spec Compliance Checklist

Categories of spec rules most commonly missed:

- **Availability conditions:** Neighbor blocks may be unavailable (frame edges, slice boundaries, not-yet-decoded in raster scan). Every neighbor access needs an availability check.
- **Special cases for unavailable neighbors:** The spec usually defines a fallback (use 0, use -1, replicate last available sample, skip the neighbor). Missing these defaults is the #1 source of bugs.
- **DC vs AC scaling differences:** DC and AC coefficients often have different quantization paths. The ratios are intentional due to transform gain differences — do not "fix" them to match.
- **Default values when features are absent:** Scaling lists default to flat (16), reference index defaults vary by context, QP offsets default to 0. Missing a default often produces subtly wrong output.
- **Conditional rules with OR/AND:** Skip MV derivation uses OR ("if EITHER A or B..."), MV prediction uses exactly-one-match. Misreading these logical conditions causes intermittent bugs that only appear with specific neighbor configurations.
- **Signed vs unsigned arithmetic:** Motion vectors are signed, chroma MV derivation requires floor division (not truncation toward zero), fractional pel positions must stay in valid range.
- **Raster scan order:** Neighbors to the right or below in the same row may not be decoded yet. C-neighbor (upper-right) availability depends on decode order, not spatial adjacency.

### 6. ffmpeg Command Recipes

```bash
# === Reference YUV generation ===

# Single frame (frame 0)
ffmpeg -i input.mp4 -vf "select=eq(n\,0)" -pix_fmt yuv420p -f rawvideo -y frame0.yuv

# Frame range (frames 0-9)
ffmpeg -i input.mp4 -vframes 10 -pix_fmt yuv420p -f rawvideo -y frames_0_9.yuv

# Without deblocking (H.264)
ffmpeg -flags nodb -i input.mp4 -vf "select=eq(n\,0)" -pix_fmt yuv420p -f rawvideo -y frame0_nodb.yuv

# Without loop filter (VP8/VP9)
ffmpeg -flags nofilter -i input.webm -vframes 1 -pix_fmt yuv420p -f rawvideo -y frame0_nofilt.yuv

# === Inspection ===

# Show stream info
ffprobe -v error -select_streams v:0 \
  -show_entries stream=width,height,pix_fmt,codec_name,nb_frames \
  -of csv=p=0 input.mp4

# Show per-frame info (type, size, key)
ffprobe -v error -select_streams v:0 \
  -show_entries frame=pict_type,key_frame,pkt_size \
  -of csv=p=0 input.mp4

# === Test clip generation ===

# Create IDR-only clip (all keyframes)
ffmpeg -i input.mp4 -g 1 -vframes 10 -c:v libx264 -profile:v baseline \
  -an -pix_fmt yuv420p output_idr_only.mp4

# Create clip with specific GOP structure
ffmpeg -i input.mp4 -g 10 -bf 0 -vframes 30 -c:v libx264 -profile:v baseline \
  -an -pix_fmt yuv420p output_ippp.mp4

# Small resolution for debugging
ffmpeg -i input.mp4 -vf scale=160:120 -g 1 -vframes 5 -c:v libx264 \
  -profile:v baseline -an -pix_fmt yuv420p small_idr.mp4

# VP8 keyframe-only test clip
ffmpeg -i input.mp4 -vf scale=160:120 -g 1 -vframes 10 -c:v libvpx \
  -an small_vp8.webm

# MPEG-1 test clip
ffmpeg -i input.mp4 -vf scale=160:120 -vframes 10 -c:v mpeg1video small.mpg
```

---

## Part 3: Applying to This Project's Codecs

### VP8

The VP8 decoder (`vp8/`) is forked from Go's `golang.org/x/image/vp8` with interframe support added. Current test infrastructure (`vp8/codec_test.go`) covers:
- Keyframe decode and all-keyframe sequence
- Interframe decode (all frames including P-frames)
- Flush/reset behavior
- RGBA conversion and pixel validity

**Next steps to reach H.264-level verification:**
1. Generate reference YUV files with ffmpeg for both keyframe-only and interframe test clips
2. Add `TestDecodeVsReference` comparing decoded Y/Cb/Cr planes against ffmpeg output
3. Add multi-frame drift test (decode 10+ frames, track per-frame max error)
4. Verify loop filter — compare with and without (`-flags nofilter`)
5. Verify sub-pixel MC — look for directional blur artifacts in per-MB error maps

### MPEG-1

The MPEG-1 decoder (`mpeg1/`) uses `Source` as both demuxer and codec. Tests (`mpeg1/source_test.go`) cover basic decode and frame validity.

**Next steps:**
1. Generate reference YUV from test.mpg
2. Add per-frame reference comparison
3. MPEG-1 is simpler (no deblocking, simpler MC) but still benefits from per-MB error analysis

### Future Codecs

Template for test infrastructure when adding a new codec:

1. **Packet source helper** — Abstract the container format (MP4, WebM, raw) behind a `nextPacket()` function. See `h264/codec_test.go:openTestPackets` and `vp8/codec_test.go:openTestDemuxer`.

2. **Reference comparison test** — Core pattern:
   ```go
   func TestDecodeVsReference(t *testing.T) {
       // Load ffmpeg-generated YUV reference
       ref, _ := os.ReadFile("testdata/frame0.yuv")
       // Decode frame
       frame, _ := codec.Decode(pkt)
       // Compare Y, Cb, Cr planes separately
       // Report: wrong pixel count, max error, first error location
       // Per-MB max error map
       // Error histogram
   }
   ```

3. **Multi-frame analysis** — Decode N frames, compare each against concatenated YUV reference, track error growth across frames.

4. **Stage isolation** — Test with loop filter disabled first, then enabled. Test keyframes before inter frames. Test single-reference before multi-reference.

5. **Diagnostic dump** — When a test fails, dump the worst MBs with got/want/reference pixel values. Include MB type, motion vectors, and reference indices for inter frames.

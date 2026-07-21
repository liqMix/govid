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

---

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

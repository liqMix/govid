# govid

### Disclaimer: This repo is written by Claude.

Pure-Go, codec-agnostic video decoding and playback library — no cgo, no ffmpeg. Built for embedding video in Go applications (games in particular): decode H.264/MP4, VP8/WebM, or MPEG-1 into `image.YCbCr`/RGBA frames with playback, seeking, and looping handled for you.

## Status

Every decoder is verified plane-by-plane against raw YUV dumped from ffmpeg for the same clips (`go test ./...`).

| Package | What it is | State |
|---|---|---|
| `h264/` | From-scratch H.264 decoder (CAVLC + CABAC, I/P/B, High profile) | **Working** — bit-exact vs ffmpeg |
| `vp8/` | Fork of `golang.org/x/image/vp8` + hand-written inter-frame support | **Working** — bit-exact vs ffmpeg |
| `mpeg1/` | Thin wrapper over [`gen2brain/mpeg`](https://github.com/gen2brain/mpeg) | **Working** — matches ffmpeg to ±3 (IDCT rounding) |
| `mp4/`, `webm/` | Container demuxers (mp4ff / ebml-go) with keyframe-accurate seek | **Working** |
| `player.go`, `ebitengine/` | Playback orchestration + Ebitengine bridge | **Working** |
| `av1/` | From-scratch AV1 decoder | **Not usable** — parses structure, output is wrong |
| `ebiv/` | EBIV — a decode-optimized codec of our own ([design plan](.docs/codec-design-plan.md)) | **Working, feature-complete** — measured 1.16× x264 size on background-animation content at matched PSNR and seek cadence; 1080p decode 4–11 ms single-thread, **1.0–1.8 ms tiled**; fuzz-hardened, CI-pinned size and decode budgets |

## Codec / container support

| Container | Tracks read |
|---|---|
| MP4 (`mp4/`) | `avc1` (H.264), `av01` (AV1) |
| WebM (`webm/`) | `V_VP8`, `V_AV1` |
| MPEG-PS/ES (`mpeg1/`) | MPEG-1 video (`Source` is both demuxer and codec) |
| EBIV (`ebiv/`) | our own decode-optimized codec + container |

**H.264:** decodes everything x264 emits (any preset/tune, CAVLC or CABAC, B-frames/pyramid, 8x8 transform, custom scaling matrices, weighted prediction, I_PCM) plus multi-slice pictures (hardware/capture encoders), temporal direct mode, and long-term references / full MMCO, verified bit-exact against ffmpeg and JVT conformance streams. Not supported (returns an error): interlaced (field/MBAFF) coding, multiple slice groups (FMO), POC type 1, 4:2:2/4:4:4 chroma, lossless transform bypass. **Known defect:** High 4:4:4 Predictive streams (profile 244 — what OBS/NVENC "lossless" screen capture produces, even with 4:2:0 pixels) currently decode to wrong pixels *without* an error; convert with `ffmpeg -c:v libx264 -profile:v high -pix_fmt yuv420p` first.

**VP8:** full decode including inter frames, loop filter, and invisible auto-alt-ref frames; encode with libvpx defaults.

**AV1:** do not use; intra-only and incorrect.

With B-frames, frames come out in display order after a small reorder delay; `Player`/`AsyncPlayer` handle this transparently, including draining tail frames at end of stream.

## Performance

720p, 120 frames, single run on one machine (`h264/bench_decode_test.go`):

| codec | ms/frame | fps |
|---|---|---|
| govid h264 (x264 defaults, +B) | 15.2 | 66 |
| govid h264 (High, no B) | 11.5 | 87 |
| gen2brain/mpeg (MPEG-1) | 0.9 | 1152 |

Enough for 720p30 real-time playback. The tradeoff: MPEG-1 decodes ~15x cheaper, but at matched quality H.264 files are about half the size. H.264's per-frame cost eats most of a 60 TPS tick budget, so use `NewAsyncPlayer` to keep decoding off the game thread.

## Architecture

```
Demuxer (container) ── NextPacket() → Packet
Codec   (decoding)  ── Decode(Packet) → *Frame
Player  (playback)  ── Update()/UpdateToTime()/Seek()/SetLoop()/CurrentFrame()
Frame               ── YCbCr (*image.YCbCr) / RGBA() ([]byte)
```

Anything implementing the `Demuxer` and `Codec` interfaces plugs into `Player`:

```go
type Demuxer interface {
    NextPacket() (Packet, error)
    Seek(time.Duration) (time.Duration, error)
    Duration() time.Duration
    VideoInfo() VideoInfo
    Close() error
}

type Codec interface {
    Decode(Packet) (*Frame, error)
    Flush()
}
```

## Installation

```bash
go get github.com/liqmix/govid
```

## Usage

### H.264 in MP4

```go
f, _ := os.Open("video.mp4")
defer f.Close()

demuxer, _ := mp4.NewDemuxer(f)
defer demuxer.Close()

player, _ := govid.NewPlayer(demuxer, h264.NewCodec())
player.Play()
player.UpdateToTime(1 * time.Second)

frame := player.CurrentFrame() // frame.YCbCr, frame.RGBA(), frame.Timestamp
```

Encode with plain x264 defaults (add `-bf 0` if you want zero-latency decoding):

```bash
ffmpeg -i input.mov -c:v libx264 -pix_fmt yuv420p out.mp4
```

### VP8 in WebM

```go
demuxer, _ := webm.NewDemuxer(f)
player, _ := govid.NewPlayer(demuxer, vp8.NewCodec())
```

```bash
ffmpeg -i input.mov -c:v libvpx -crf 20 -b:v 4M -pix_fmt yuv420p out.webm
```

### MPEG-1

`Source` is both demuxer and codec:

```go
source, _ := mpeg1.NewSource(f)
player, _ := govid.NewPlayer(source, source)
```

### EBIV

A decode-optimized codec built for this use case: static-table interleaved
rANS, no in-loop filter, a golden reference for looping/flashing content, and
independent tiles for parallel decode. See the
[design plan](.docs/codec-design-plan.md).

```go
demuxer, _ := ebiv.NewDemuxer(f)
player, _ := govid.NewPlayer(demuxer, ebiv.NewCodec())
```

Encode from any govid-supported source with the transcoder (`-fast` halves
encode time during iteration at ~3.5% size cost; drop it for final assets):

```bash
go run ./examples/ebiv -q 22 -gop 60 -tilecols 4 -tilerows 3 in.mp4 out.ebiv
```

For distribution settings tuned to the machines you ship to — including why the
tile count matters and what to expect on a Steam Deck — see
[Encoding EBIV for game assets](#encoding-ebiv-for-game-assets).

### Decoding off the game thread

`NewPlayer` decodes inline on whatever goroutine calls `Update`. `NewAsyncPlayer` decodes on a background goroutine and keeps a bounded queue of frames ready:

```go
player, _ := govid.NewAsyncPlayer(demuxer, h264.NewCodec(), 4, govid.WithRGBA())
defer player.Close() // required; close before closing the demuxer
```

Same API, different behavior under load: if the decoder falls behind, `Update`/`UpdateToTime` return `false` and leave the current frame on screen instead of blocking. Notes:

- The queue depth bounds memory (~1.4 MB per queued 720p frame) and applies backpressure — a paused player stops decoding.
- `Seek` and loop restarts still block briefly, by design: you want the frame you seeked to. Frames decoded before a seek are discarded, never shown.
- `WithRGBA()` performs the YCbCr→RGBA conversion (~6 ms at 1080p) on the decode goroutine too, making `Frame.RGBA()` a field read. Conversion buffers are pooled; the returned slice is valid until the second frame change after it was delivered — copy it if you keep it longer.

### Ebitengine

A complete background-video loop with EBIV — decode runs on a background
goroutine (`NewAsyncPlayer`), so a slow frame holds the last video image
instead of stalling the game:

```go
package main

import (
	"os"

	"github.com/hajimehoshi/ebiten/v2"
	govid "github.com/liqmix/govid"
	govidebiten "github.com/liqmix/govid/ebitengine"
	"github.com/liqmix/govid/ebiv"
)

type Game struct {
	video *govidebiten.VideoImage
}

func (g *Game) Update() error {
	g.video.Update()
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.DrawImage(g.video.Image(), nil) // draw first: it's the background
}

func (g *Game) Layout(_, _ int) (int, int) {
	f := g.video.Player().CurrentFrame()
	return f.Width, f.Height
}

func main() {
	f, _ := os.Open("bg.ebiv")
	defer f.Close()

	demuxer, _ := ebiv.NewDemuxer(f)
	defer demuxer.Close()

	const decodeAhead = 4
	codec := ebiv.NewCodec(ebiv.WithFrameRing(decodeAhead + 2))
	player, _ := govid.NewAsyncPlayer(demuxer, codec, decodeAhead, govid.WithRGBA())
	defer player.Close()

	player.SetLoop(true)
	player.Play()

	game := &Game{video: govidebiten.New(player)}
	ebiten.SetWindowTitle("govid")
	ebiten.RunGame(game)
}
```

The same skeleton plays any govid format — swap the demuxer/codec pair (see
the MPEG-1/H.264/VP8 sections above). See [`examples/`](examples) for
runnable programs, one per codec.

## EBIV for Ebitengine: when it earns its place

EBIV exists because none of the other options fit the job this library was
built for: **background animation playing inside a game loop that can't
stall**. H.264 ships the smallest files but eats most of a 60 Hz tick on one
core and won't parallelize (and shipping an H.264 decoder in a commercial game
touches the MPEG-LA patent pool). MPEG-1 decodes for free but ships files
nearly twice as large. EBIV is a clean-room, royalty-free format that splits
the difference on size and then wins decisively on decode by being the only
decoder here that **scales across cores**: encoded once offline, decoded in
1–2 ms/frame at 1080p on a desktop, with graceful degradation below that.
(Its v1 measured 2.19× x264 at 88 ms/frame; every number below is the result
of a measured, gate-driven program to fix that — the full history lives in the
[gap analysis](.docs/ebiv-gap-analysis.md).)

Measured against the *other govid decoders* (the real alternatives), 1080p, at
matched quality and seek granularity, across an 11-clip corpus:

| codec (via govid) | file size vs H.264 | decode, 1 core | decode, tiled |
|---|---:|---:|---:|
| H.264 | 1.00× (smallest) | ~27 ms | — (single-threaded) |
| VP8 | ~1.5× | ~24 ms | — |
| MPEG-1 | ~1.7× | **1.9 ms** | — |
| **EBIV** | **1.16× (BGA median)** | 4–11 ms | **1.0–1.8 ms** |

EBIV is the **Pareto point** between "MPEG-1: trivially cheap decode, fat
files" and "H.264: smallest files, expensive single-threaded decode." On the
background-animation content it targets, half the corpus encodes **at or
below x264's size** (0.70–0.99×) while the median sits at 1.16×; it ships
~40% smaller than MPEG-1, dominates VP8 (smaller *and* faster), and its tiled
decode outruns even libvpx-vp9's hand-vectorized C decoder on every measured
clip. It holds ~zero per-frame allocation, decodes bit-identically on every
platform, and corrupt files return errors — the decoder is fuzz-tested, never
panicking or hanging on hostile input.

Know its measured limits too: grain-heavy or shimmer-heavy animation encodes
~1.6× x264, and high-motion 60 fps screen capture (not its use case) runs
1.6–1.9×.

**Reach for EBIV when:**

- **1080p60, or a weak CPU (Steam Deck class).** H.264 decode (~27 ms/core)
  can't fit a 60 Hz tick and won't parallelize; MPEG-1 fits but ships much
  bigger. EBIV's tiled ~1.7 ms decode is the only thing that's both real-time
  *and* reasonably small. This is its strongest case.
- **Several videos playing at once.** Per-core decode cost multiplies; EBIV's
  cheap, parallel decode keeps the frame budget intact.
- **You want to spend the CPU on the game, not the video.** Tiled EBIV leaves
  almost the whole tick free.

**Prefer H.264 or MPEG-1 when:**

- **720p30, a single video, a healthy CPU.** govid's H.264 already decodes in
  ~15 ms and ships smaller — EBIV's cheaper decode buys nothing you needed, and
  you pay ~16% more bytes. This is the common case; don't reach for EBIV
  reflexively.
- **File size is paramount** → H.264 (~16% smaller than EBIV on BGA content,
  more on grain-heavy clips).
- **You want the simplest, most decode-cheap option and can spare the bytes**
  → MPEG-1.

**Also weigh:** EBIV's encoder is offline (a few hundred ms per 720p frame
two-pass — slower than ffmpeg, fine for an asset pipeline, `-fast` halves it),
and it is code this repo owns and maintains versus mature H.264/MPEG-1 paths.
The format's size and decode budgets are pinned by CI regression gates, so
neither can silently drift. Full measured status and the milestone history
live in the [gap analysis](.docs/ebiv-gap-analysis.md).

Playing an `.ebiv` file is identical to any other format — the Ebitengine
example above works unchanged; just point it at a `.ebiv` and size the codec's
frame ring for your decode-ahead depth:

```go
demuxer, _ := ebiv.NewDemuxer(f)
codec := ebiv.NewCodec(ebiv.WithFrameRing(decodeAhead + 2))
player, _ := govid.NewAsyncPlayer(demuxer, codec, decodeAhead, govid.WithRGBA())
```

To produce `.ebiv` assets sized for your target hardware, see the next section.

## Encoding EBIV for game assets

EBIV is encoded once, offline, and decoded on every player's machine — so the
encode settings you ship should target your **lowest-end playback machine**, not
your encode box.

### The tile count is baked in — set it for the target

Decode parallelism is bounded by the number of tiles in the file. The decoder
runs a `runtime.NumCPU()`-worker pool, but it can only spread work as wide as the
file was tiled:

- **More tiles than the player's cores** → they queue through the pool. Harmless.
- **Fewer tiles than cores** → cores sit idle, and you may miss framerate.

The transcoder's automatic tiling keys off the **encode** machine's core count,
which is the wrong number for a shipped asset. **Set the grid explicitly** with
`-tilecols`/`-tilerows` so the file is deterministic and sized to fill your
target. Aim for about **8 tiles** (Steam Deck class); that also runs fine on
bigger machines (extra cores idle, already under budget) and on smaller ones
(tiles queue). Keep tiles large — 8–12 on 1080p — so the tile-edge compression
cost stays ~1–2%.

### Recommended settings

```bash
# 1080p, targets Steam-Deck-and-up (12 tiles)
go run ./examples/ebiv -q 22 -gop 60 -tilecols 4 -tilerows 3  in.mp4  out.ebiv

# 720p, the safe choice for "most machines" at 60 fps (8 tiles)
go run ./examples/ebiv -q 22 -gop 60 -tilecols 4 -tilerows 2  in.mp4  out.ebiv
```

| Flag | Meaning | Guidance |
|---|---|---|
| `-q` | quantizer 0–63, lower = better | **22** is a good ship point (~40+ dB). `18` for hero/archival content; `26+` for out-of-focus background where size wins. `-1` stores lossless (huge — not for distribution). |
| `-gop` | key-frame interval | **60** (~1 s at 60 fps) balances size, seek granularity, and — because there is no in-loop filter — how far blocking artifacts propagate. Use `~30` for tight song-position seeking. The encoder also starts a fresh GOP at detected scene cuts automatically. |
| `-tilecols`/`-tilerows` | tile grid | Set explicitly; ~8 tiles for 1080p/720p (see above). |
| `-fast` | single-pass encode | ~2× faster encode, ~3.5% larger files. Use while iterating; drop it for the final asset. |

### Play it back off the game thread

Always use `NewAsyncPlayer` (the Ebitengine example does). Its graceful
degradation is the safety net: if a weak machine can't sustain the framerate it
holds the current frame and the **game** stays smooth — the background video just
updates a little slower, which is usually imperceptible.

### What to expect

Measured on one desktop (Ryzen 9 7950X: 1080p decode 4–11 ms/frame
single-threaded, **1.0–1.8 ms/frame tiled**), extrapolated for slower cores
and lower thread counts — **profile on your actual targets before
committing**:

| Target | 1080p60 | 1080p30 | 720p60 |
|---|---|---|---|
| Desktop, 8+ cores | easy | easy | easy |
| Steam Deck (8 threads) | comfortable (est. ~5–8 ms tiled) | easy | easy |
| Low-end 4-core | comfortable | easy | easy |

Even the pessimistic case — a weak 4-core running the busiest clip
single-threaded — sits near 30–40 ms/frame, and `NewAsyncPlayer` degrades
gracefully from there. For background animation, 720p and 1080p are both
realistic 60 fps targets on anything Steam-Deck-class or better.

### Notes for asset pipelines

- Files are **self-contained and decode bit-identically on every platform** (the
  transform is pure-Go integer math), so one encode ships everywhere — no
  per-machine variants.
- Memory per active video runs to ~100 MB at 1080p: each buffered frame is
  ~3 MB of YCbCr plus ~8 MB of RGBA (with `WithRGBA`), times the `decodeAhead`
  queue and the decoder's frame ring. Budget for it if several play at once;
  drop `WithRGBA` or the resolution if that is tight.
- **Final measured status (11-clip corpus, matched PSNR and seek cadence):**
  size is **1.16× x264 on the background-animation median** — half the target
  clips at or below x264 parity (0.70–0.99×) — and **~0.6× MPEG-1**, via skip
  mode, motion partitions, quarter-pel, directional intra, a golden reference
  for looping/flashing content, sign data hiding, scene-cut keyframes, and a
  two-pass real-cost RD encoder. Decode is **4–11 ms/frame single-thread and
  1.0–1.8 ms/frame tiled at 1080p** (v1 shipped at 2.19× x264 and 88 ms).
  Both numbers are pinned by CI regression gates. Full history:
  [gap analysis](.docs/ebiv-gap-analysis.md).

## Development

```bash
go test ./...   # full suite (h264 takes ~50s)
```

Short test fixtures with ffmpeg-generated YUV references are committed under each package's `testdata/`; large clips and conformance streams are local-only (gitignored) with regeneration commands in the test docs.

[CODEC_GUIDE.md](CODEC_GUIDE.md) documents the verification methodology — ffmpeg reference generation, staged testing, diagnostic patterns — and records every H.264/VP8 bug found with its spec reference.

## Roadmap

- EBIV integration (M9): zero-copy YCbCr plane accessor + Kage-shader color conversion in the Ebitengine bridge, in-engine frame-time measurements against the H.264 path, low-core-count profiling. The codec itself is feature-complete — its size/decode plateau is measured, gated in CI, and documented (eleven optimization experiments recorded in `.docs/`, kept or reverted strictly by measurement)
- EBIV half-resolution experiment: encode below display resolution and reconstruct in a display shader — the remaining size lever, judged perceptually in-engine rather than by PSNR
- Fix govid/h264 silent mis-decode of High 4:4:4 Predictive (profile 244): must return an error
- Get AV1 intra reconstruction correct before adding inter prediction
- H.264 leftovers if ever needed: interlaced (field/MBAFF) coding, FMO, POC type 1

## License

MIT — see [LICENSE](LICENSE).

The `vp8/` package is forked from `golang.org/x/image/vp8` and is licensed under the BSD 3-Clause license — see [vp8/LICENSE](vp8/LICENSE).

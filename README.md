# govid

### Disclaimer: This repo is written by Claude.

Pure-Go, codec-agnostic video decoding and playback library — no cgo, no ffmpeg.

> **Status: work in progress.** This is an in-progress project to write video decoders from the specs in Go. Some of it works well (the H.264 decoder is bit-exact against ffmpeg on the test clips); some of it is half-finished (VP8 inter frames drift); and some of it does not produce correct pictures yet at all (AV1). See the status table below before depending on any of it.

## Status

| Package | What it is | State |
|---|---|---|
| `h264/` | From-scratch H.264 decoder (CAVLC + CABAC, I/P/B slices, High profile) — decodes default x264 output | **Working** — bit-exact vs ffmpeg on the tracked test clips |
| `mpeg1/` | Thin wrapper over [`gen2brain/mpeg`](https://github.com/gen2brain/mpeg) | **Working** — third-party decoder, matches ffmpeg to ±3 |
| `mp4/`, `webm/` | Container demuxers (mp4ff / ebml-go) | **Working** for the tracks listed below |
| `player.go`, `ebitengine/` | Playback orchestration + Ebitengine bridge | **Working** |
| `vp8/` | Fork of `golang.org/x/image/vp8` + hand-written inter-frame support (incl. loop filter, alt-ref/invisible frames) | **Working** — bit-exact vs ffmpeg on the tracked test clips |
| `av1/` | From-scratch AV1 decoder (intra only) | **Early / not usable** — decodes structure, output is wrong |

### What "working" means here

Every decoder is checked against raw YUV dumped from ffmpeg for the same clip, plane by plane, pixel by pixel. Current numbers, reproducible with `go test ./...`:

**H.264** (`TestDecodeBakerMultiFrame`, 1280x720, IDR + 9 P-frames) — bit-exact:

```
frame 0 (IDR): Y 0/921600 wrong (0.0%) max=0 | Cb 0/230400 max=0 | Cr 0/230400 max=0
frame 9 (P):   Y 0/921600 wrong (0.0%) max=0 | Cb 0/230400 max=0 | Cr 0/230400 max=0
```

A longer 120-frame 720p run (`TestDecodeBGMP4VsReference`) is also bit-exact, but it skips unless you supply the large source clip and reference YUV locally (both are gitignored).

**MPEG-1** (`TestDecodeBakerMultiFrame`) — ~2% of luma pixels differ by at most 3, i.e. IDCT rounding differences in the upstream decoder, not structural errors.

**VP8** (`TestDecodeBakerMultiFrame`, 1280x720, key + 70 inter frames) — bit-exact on every checked frame:

```
frame  0 (key):   Y 0/921600 wrong (0.0%) max=0 | Cb 0/230400 max=0 | Cr 0/230400 max=0
frame 70 (inter): Y 0/921600 wrong (0.0%) max=0 | Cb 0/230400 max=0 | Cr 0/230400 max=0
```

Gated local-fixture tests additionally verify the full 71-frame clip and two 120-frame 720p real-content clips (single-pass with a mid-stream keyframe and LF deltas, and a two-pass encode with invisible auto-alt-ref frames), all bit-exact.

**AV1** (`TestDecodeBakerMultiframe`) — headers, OBUs, the symbol decoder, and block structure parse, but reconstruction is wrong: ~99.5% of luma pixels differ with a mean absolute error around 44–75. Multiple bugs appear to compensate for each other, so fixing them one at a time has repeatedly made the error worse. Do not use this package.

## Codec / container support

| Container | Tracks read |
|---|---|
| MP4 (`mp4/`) | `avc1` (H.264), `av01` (AV1) |
| WebM (`webm/`) | `V_VP8`, `V_AV1` |
| MPEG-PS/ES (`mpeg1/`) | MPEG-1 video (`Source` is both demuxer and codec) |

H.264 decoder coverage:

- **Supported:** CAVLC and CABAC entropy coding; I/SI, P, and B slices (spatial direct mode, bi-prediction with implicit and explicit weighting, B-pyramid); intra 4x4 / 8x8 / 16x16 / chroma prediction; the 8x8 transform (High profile); quarter-pel luma and bilinear chroma motion compensation; multi-reference MV prediction; reference picture list modification; MMCO short-term marking; explicit weighted prediction; picture-order display reordering; the deblocking filter (including 8x8-transform and B-slice bS rules). Everything x264 emits by default — `ffmpeg -c:v libx264` with no flags — decodes bit-exact, verified against ffmpeg on 120-frame 720p real content (`TestDecodeBGDefaultMP4VsReference`) plus the committed staged fixtures.
- **Not supported (returns an error):** temporal direct mode (x264 `direct=temporal`/`auto`; the default `spatial` works), multiple slice groups, long-term references / MMCO ops 2-6, I_PCM inside CABAC slices, interlaced (field/MBAFF) coding. Custom (non-flat) scaling matrices are parsed but not applied — x264's default flat CQM decodes correctly.
- **B-frame note:** with B-frames the decoder emits frames in display order with a small delay (from the stream's VUI `max_num_reorder_frames`). `Player`/`AsyncPlayer` handle this transparently, including draining the buffered tail frames at end of stream (`govid.FrameDrainer`).

AV1 decoder coverage: intra frames only — there is no inter prediction, no film grain, no loop restoration.

## Performance

`h264/bench_decode_test.go` on 1280x720, 120 frames (this machine, one run — not a controlled benchmark):

```
codec                               total   ms/frame        fps
govid h264 Baseline (pure Go)      1600ms    13.33ms       75.0
govid h264 High 8x8 CAVLC          1379ms    11.49ms       87.0
govid h264 High CABAC              1378ms    11.48ms       87.1
govid h264 x264 defaults (+B)      1792ms    15.19ms       65.8
gen2brain/mpeg (MPEG-1)             104ms     0.87ms     1152.0
```

Enough for 720p30 real-time playback in a game loop; MPEG-1 is far cheaper if you control the encoding.

Per second of 30 fps video that works out to ~414 ms of CPU for H.264 (~41% of one core) versus ~28 ms for MPEG-1 (~2.8%). With `NewPlayer` that cost lands on whichever goroutine calls `Update` — in a 60 TPS game loop, H.264's 13.8 ms decode eats most of the 16.7 ms tick budget on the ticks where a new frame is due. Use [`NewAsyncPlayer`](#decoding-off-the-game-thread) to move it off that goroutine.

At matched quality (SSIM measured against the same 720p source), Baseline H.264 is roughly half the size of MPEG-1 — 0.945 SSIM costs ~886 KB as MPEG-1 versus ~410 KB as H.264. So: MPEG-1 buys frame budget, H.264 buys install size.

## Architecture

```
Demuxer (container parsing)
   │
   ├── NextPacket() → Packet{Data, Timestamp, Keyframe}
   │
Codec (frame decoding)
   │
   ├── Decode(Packet) → *Frame
   │
Player (orchestration)
   │
   ├── Update() / UpdateToTime() / Seek() / SetLoop()
   ├── CurrentFrame() → *Frame
   ├── Close()  — stops the decode goroutine (async players)
   │
Frame
   │
   └── RGBA() → []byte (packed pixel data)
```

`Demuxer` reads compressed packets from a container. `Codec` decodes packets into frames. `Player` ties them together and manages playback state, timing, and looping. `Frame.YCbCr` is a standard `*image.YCbCr`; `Frame.RGBA()` gives raw pixel data ready for upload to a texture.

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

Anything implementing these two interfaces plugs into `Player`.

## Installation

```bash
go get github.com/liqmix/govid
```

## Usage

### H.264 in MP4 (recommended path)

```go
package main

import (
	"fmt"
	"os"
	"time"

	govid "github.com/liqmix/govid"
	"github.com/liqmix/govid/h264"
	"github.com/liqmix/govid/mp4"
)

func main() {
	f, err := os.Open("video.mp4")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	demuxer, err := mp4.NewDemuxer(f)
	if err != nil {
		panic(err)
	}
	defer demuxer.Close()

	player, err := govid.NewPlayer(demuxer, h264.NewCodec())
	if err != nil {
		panic(err)
	}

	player.Play()
	player.UpdateToTime(1 * time.Second)

	frame := player.CurrentFrame()
	fmt.Printf("Frame: %dx%d at %v\n", frame.Width, frame.Height, frame.Timestamp)
	// frame.RGBA() returns packed RGBA pixel data
}
```

Encode input with plain x264 defaults — no special flags needed:

```bash
ffmpeg -i input.mov -c:v libx264 -pix_fmt yuv420p out.mp4
```

On the bg test clip at matched crf, default x264 (CABAC + B-frames) is ~11% smaller than CABAC without B-frames and ~17% smaller than Baseline. If you want zero-latency decoding (no display reordering), add `-bf 0`.

### Decoding off the game thread

`NewPlayer` decodes inline: `Update` demuxes and decodes the next frame on the calling goroutine, so a 13.8 ms H.264 decode is 13.8 ms your game loop does not get. `NewAsyncPlayer` runs the demux+decode on a background goroutine and keeps a bounded queue of frames ready:

```go
// Keep 4 frames decoded ahead, converting to RGBA on the decode goroutine.
player, err := govid.NewAsyncPlayer(demuxer, h264.NewCodec(), 4, govid.WithRGBA())
if err != nil {
	panic(err)
}
// Close before closing the demuxer — it returns only once the decode
// goroutine has stopped touching it.
defer player.Close()
```

The rest of the API is identical. The behavioral difference is what happens when the decoder falls behind: `Update` and `UpdateToTime` return `false` and leave the current frame on screen instead of blocking. A dropped tick shows a stale frame; it does not stall the loop.

Details worth knowing:

- **Depth** bounds both memory and latency. Each queued frame is a full YCbCr copy (~1.4 MB at 720p), and the queue applies backpressure — the decoder stops once it is full, so a paused player does not run away decoding.
- **`Seek`, loop restart, and the initial two frames still block**, by design: you want the frame you seeked to, now. A seek waits at most one in-flight decode, because the decode goroutine holds the demuxer lock while decoding.
- **Frames decoded before a seek are discarded**, not displayed — each frame carries a generation stamp that a seek invalidates.
- **`Close` is required** and is safe to call twice. It stops the goroutine and waits for it to exit, which is what makes closing the demuxer or file afterwards safe. `Close` on a `NewPlayer` player is a no-op, so the two are interchangeable.

`WithRGBA` moves the other per-frame cost off the consumer's goroutine. Without it, `Frame.RGBA()` runs a full YCbCr→RGBA conversion wherever you call it — on the game thread, that is ~6 ms at 1080p (`BenchmarkConvertRGBA1080p`) on top of the decode. With it, the decode goroutine converts, `Frame.HasRGBA()` reports true, and `Frame.RGBA()` is a field read; the Ebitengine bridge then does nothing but `WritePixels`. Conversion buffers are pooled and recycled as frames retire, so a long video does not allocate a frame-sized buffer per frame (~110 MB/s at 720p30). The tradeoff is a lifetime rule: **the slice returned by `Frame.RGBA()` is valid until the second frame change after the one that delivered it** — long enough for the `Update` → `Draw` pair that received it, but copy it if you intend to keep it. Calling `Frame.RGBA()` on a recycled frame stays safe; it recomputes.

`examples/ebitengine/` uses this path for every format it loads.

### MPEG-1

`Source` implements both `Demuxer` and `Codec`, so it is passed as both arguments:

```go
f, _ := os.Open("video.mpg")
defer f.Close()

source, _ := mpeg1.NewSource(f)
defer source.Close()

player, _ := govid.NewPlayer(source, source)
player.Play()
player.UpdateToTime(1 * time.Second)
```

### VP8 in WebM

Bit-exact against ffmpeg on the tracked clips, including inter frames, the loop filter, and invisible auto-alt-ref frames. Encode with libvpx defaults:

```bash
ffmpeg -i input.mov -c:v libvpx -crf 20 -b:v 4M -pix_fmt yuv420p out.webm
```

```go
f, _ := os.Open("video.webm")
defer f.Close()

demuxer, _ := webm.NewDemuxer(f)
defer demuxer.Close()

player, _ := govid.NewPlayer(demuxer, vp8.NewCodec())
```

### Ebitengine

```go
package main

import (
	"os"

	"github.com/hajimehoshi/ebiten/v2"
	govid "github.com/liqmix/govid"
	govidebiten "github.com/liqmix/govid/ebitengine"
	"github.com/liqmix/govid/mpeg1"
)

type Game struct {
	video *govidebiten.VideoImage
}

func (g *Game) Update() error {
	g.video.Update()
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.DrawImage(g.video.Image(), nil)
}

func (g *Game) Layout(_, _ int) (int, int) {
	f := g.video.Player().CurrentFrame()
	return f.Width, f.Height
}

func main() {
	f, _ := os.Open(os.Args[1])
	defer f.Close()

	source, _ := mpeg1.NewSource(f)
	defer source.Close()

	player, _ := govid.NewPlayer(source, source)
	player.Play()

	game := &Game{video: govidebiten.New(player)}
	ebiten.SetWindowTitle("govid")
	ebiten.RunGame(game)
}
```

Runnable examples live in [`examples/`](examples) — one per codec plus the Ebitengine bridge.

## Development

```bash
go test ./...            # full suite (h264 takes ~50s)
go test ./h264/ -v -run TestDecodeBakerMultiFrame
```

Test fixtures under each package's `testdata/` (short clips plus ffmpeg-generated raw YUV references) are committed on purpose; large local-only clips are gitignored.

[CODEC_GUIDE.md](CODEC_GUIDE.md) documents the verification methodology used here — ffmpeg reference generation, the staged testing strategy (keyframe → no-deblock → deblock → first P-frame → multi-frame drift → perfect-reference injection), diagnostic patterns, and an error-pattern-to-root-cause table. It also records every H.264 bug found and its spec reference.

## Roadmap

- Get AV1 intra reconstruction correct before adding inter prediction
- H.264 leftovers if ever needed: temporal direct mode, long-term references, interlaced coding

## License

MIT — see [LICENSE](LICENSE).

The `vp8/` package is forked from `golang.org/x/image/vp8` and is licensed under the BSD 3-Clause license — see [vp8/LICENSE](vp8/LICENSE).

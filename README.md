# govid

### Disclaimer: This repo is written by Claude.

Pure-Go, codec-agnostic video decoding and playback library — no cgo, no ffmpeg.

> **Status: work in progress.** This is an in-progress project to write video decoders from the specs in Go. Some of it works well (the H.264 decoder is bit-exact against ffmpeg on the test clips); some of it is half-finished (VP8 inter frames drift); and some of it does not produce correct pictures yet at all (AV1). See the status table below before depending on any of it.

## Status

| Package | What it is | State |
|---|---|---|
| `h264/` | From-scratch H.264 decoder (CAVLC, I + P slices) | **Working** — bit-exact vs ffmpeg on the tracked test clips |
| `mpeg1/` | Thin wrapper over [`gen2brain/mpeg`](https://github.com/gen2brain/mpeg) | **Working** — third-party decoder, matches ffmpeg to ±3 |
| `mp4/`, `webm/` | Container demuxers (mp4ff / ebml-go) | **Working** for the tracks listed below |
| `player.go`, `ebitengine/` | Playback orchestration + Ebitengine bridge | **Working** |
| `vp8/` | Fork of `golang.org/x/image/vp8` + hand-written inter-frame support | **Partial** — keyframes and early inter frames exact, then drifts |
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

**VP8** (`TestDecodeBakerMultiFrame`) — exact through frame 5, then diverges:

```
frame  0 (key):   Y 0/921600 wrong (0.0%)  max=0
frame  5 (inter): Y 0/921600 wrong (0.0%)  max=0
frame 10 (inter): Y 47495/921600 (5.2%)    max=113
frame 70 (inter): Y 767272/921600 (83.3%)  max=116
```

The suspected cause is a bitstream desync in the first partition / token partition boundary; `vp8/test_verify_partition_test.go` currently **fails** and documents the mismatch.

**AV1** (`TestDecodeBakerMultiframe`) — headers, OBUs, the symbol decoder, and block structure parse, but reconstruction is wrong: ~99.5% of luma pixels differ with a mean absolute error around 44–75. Multiple bugs appear to compensate for each other, so fixing them one at a time has repeatedly made the error worse. Do not use this package.

## Codec / container support

| Container | Tracks read |
|---|---|
| MP4 (`mp4/`) | `avc1` (H.264), `av01` (AV1) |
| WebM (`webm/`) | `V_VP8`, `V_AV1` |
| MPEG-PS/ES (`mpeg1/`) | MPEG-1 video (`Source` is both demuxer and codec) |

H.264 decoder coverage:

- **Supported:** CAVLC entropy coding, I/SI and P slices, intra 4x4 / 16x16 / chroma prediction, quarter-pel luma and bilinear chroma motion compensation, multi-reference MV prediction, the deblocking filter.
- **Not supported (returns an error):** CABAC, B slices, 8x8 transform (High profile), multiple slice groups. 8x8 support is partially built (`h264/predfunc8x8.go`, 8x8 IDCT/dequant) but is not wired into the decode path.

AV1 decoder coverage: intra frames only — there is no inter prediction, no film grain, no loop restoration.

## Performance

`h264/bench_decode_test.go` on 1280x720, 120 frames (this machine, one run — not a controlled benchmark):

```
codec                             total   ms/frame        fps
govid h264 (pure Go)             1657ms     13.81ms       72.4
gen2brain/mpeg (MPEG-1)           113ms      0.94ms     1063.6
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

Encode input with a Baseline-profile, CAVLC, no-B-frame configuration:

```bash
ffmpeg -i input.mov -c:v libx264 -profile:v baseline -bf 0 -pix_fmt yuv420p out.mp4
```

### Decoding off the game thread

`NewPlayer` decodes inline: `Update` demuxes and decodes the next frame on the calling goroutine, so a 13.8 ms H.264 decode is 13.8 ms your game loop does not get. `NewAsyncPlayer` runs the demux+decode on a background goroutine and keeps a bounded queue of frames ready:

```go
// Keep 4 frames decoded ahead.
player, err := govid.NewAsyncPlayer(demuxer, h264.NewCodec(), 4)
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

Works for keyframe-only content; long inter-frame sequences currently drift (see Status).

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

- Fix the VP8 first-partition desync so inter frames stop drifting
- Wire up H.264 8x8 transform + intra 8x8 to unlock High profile
- Get AV1 intra reconstruction correct before adding inter prediction
- CABAC and B-slice support for H.264

## License

MIT — see [LICENSE](LICENSE).

The `vp8/` package is forked from `golang.org/x/image/vp8` and is licensed under the BSD 3-Clause license — see [vp8/LICENSE](vp8/LICENSE).

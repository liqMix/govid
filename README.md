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

## Codec / container support

| Container | Tracks read |
|---|---|
| MP4 (`mp4/`) | `avc1` (H.264), `av01` (AV1) |
| WebM (`webm/`) | `V_VP8`, `V_AV1` |
| MPEG-PS/ES (`mpeg1/`) | MPEG-1 video (`Source` is both demuxer and codec) |

**H.264:** decodes everything x264 emits (any preset/tune, CAVLC or CABAC, B-frames/pyramid, 8x8 transform, custom scaling matrices, weighted prediction, I_PCM) plus multi-slice pictures (hardware/capture encoders), temporal direct mode, and long-term references / full MMCO, verified bit-exact against ffmpeg and JVT conformance streams. Not supported (returns an error): interlaced (field/MBAFF) coding, multiple slice groups (FMO), POC type 1, 4:2:2/4:4:4 chroma, lossless transform bypass.

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

See [`examples/`](examples) for runnable programs, one per codec.

## Development

```bash
go test ./...   # full suite (h264 takes ~50s)
```

Short test fixtures with ffmpeg-generated YUV references are committed under each package's `testdata/`; large clips and conformance streams are local-only (gitignored) with regeneration commands in the test docs.

[CODEC_GUIDE.md](CODEC_GUIDE.md) documents the verification methodology — ffmpeg reference generation, staged testing, diagnostic patterns — and records every H.264/VP8 bug found with its spec reference.

## Roadmap

- Get AV1 intra reconstruction correct before adding inter prediction
- H.264 leftovers if ever needed: interlaced (field/MBAFF) coding, FMO, POC type 1

## License

MIT — see [LICENSE](LICENSE).

The `vp8/` package is forked from `golang.org/x/image/vp8` and is licensed under the BSD 3-Clause license — see [vp8/LICENSE](vp8/LICENSE).

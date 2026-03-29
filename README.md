# govid

Pure-Go, codec-agnostic video playback library.

## Features

- **VP8/WebM** — WebM container demuxing with VP8 decoding (including inter-frame/P-frame support)
- **MPEG-1** — MPEG-1 video decoding via a single unified source
- **Ebitengine integration** — drop-in `VideoImage` bridge for rendering video in [Ebitengine](https://ebitengine.org) games
- **Codec-agnostic architecture** — plug in any demuxer/codec pair through the `Demuxer` and `Codec` interfaces

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
   ├── Update() / UpdateToTime()
   ├── CurrentFrame() → *Frame
   │
Frame
   │
   └── RGBA() → []byte (packed pixel data)
```

`Demuxer` reads compressed packets from a container. `Codec` decodes packets into frames. `Player` ties them together and manages playback state, timing, and looping. `Frame.RGBA()` gives you raw pixel data ready for rendering.

## Installation

```bash
go get github.com/liqmix/govid
```

## Usage

### VP8/WebM

```go
package main

import (
	"fmt"
	"os"

	govid "github.com/liqmix/govid"
	"github.com/liqmix/govid/vp8"
	"github.com/liqmix/govid/webm"
)

func main() {
	f, err := os.Open("video.webm")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	demuxer, err := webm.NewDemuxer(f)
	if err != nil {
		panic(err)
	}
	defer demuxer.Close()

	codec := vp8.NewCodec()
	player, err := govid.NewPlayer(demuxer, codec)
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

### MPEG-1

MPEG-1's `Source` implements both `Demuxer` and `Codec`, so you pass it as both arguments:

```go
package main

import (
	"fmt"
	"os"

	govid "github.com/liqmix/govid"
	"github.com/liqmix/govid/mpeg1"
)

func main() {
	f, err := os.Open("video.mpg")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	source, err := mpeg1.NewSource(f)
	if err != nil {
		panic(err)
	}
	defer source.Close()

	player, err := govid.NewPlayer(source, source)
	if err != nil {
		panic(err)
	}

	player.Play()
	player.UpdateToTime(1 * time.Second)

	frame := player.CurrentFrame()
	fmt.Printf("Frame: %dx%d at %v\n", frame.Width, frame.Height, frame.Timestamp)
}
```

### Ebitengine

```go
package main

import (
	"os"

	govid "github.com/liqmix/govid"
	govidebiten "github.com/liqmix/govid/ebitengine"
	"github.com/liqmix/govid/mpeg1"
	"github.com/hajimehoshi/ebiten/v2"
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

	video := govidebiten.New(player)
	game := &Game{video: video}

	ebiten.SetWindowTitle("govid")
	ebiten.RunGame(game)
}
```

## Interfaces

### Demuxer

```go
type Demuxer interface {
    NextPacket() (Packet, error)
    Seek(time.Duration) (time.Duration, error)
    Duration() time.Duration
    VideoInfo() VideoInfo
    Close() error
}
```

### Codec

```go
type Codec interface {
    Decode(Packet) (*Frame, error)
    Flush()
}
```

## License

MIT — see [LICENSE](LICENSE).

The `vp8/` package is forked from `golang.org/x/image/vp8` and is licensed under the BSD 3-Clause license — see [vp8/LICENSE](vp8/LICENSE).

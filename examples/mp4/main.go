// Example: decode an MP4/H.264 video and save a frame as PNG.
//
// Usage: go run . <video.mp4>
package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"time"

	govid "github.com/liqmix/govid"
	"github.com/liqmix/govid/h264"
	mp4pkg "github.com/liqmix/govid/mp4"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mp4-example <file.mp4>")
		os.Exit(1)
	}

	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	demuxer, err := mp4pkg.NewDemuxer(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demuxer:", err)
		os.Exit(1)
	}
	defer demuxer.Close()

	codec := h264.NewCodec()
	player, err := govid.NewPlayer(demuxer, codec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "player:", err)
		os.Exit(1)
	}

	info := demuxer.VideoInfo()
	fmt.Printf("Video: %dx%d @ %.2f fps, duration %v\n",
		info.Width, info.Height, info.FrameRate, demuxer.Duration())

	// Advance to 1 second (or as far as the video allows).
	player.Play()
	player.UpdateToTime(1 * time.Second)

	frame := player.CurrentFrame()
	fmt.Printf("Frame: %dx%d at %v\n", frame.Width, frame.Height, frame.Timestamp)

	// Save frame as PNG.
	rgba := frame.RGBA()
	img := &image.RGBA{
		Pix:    rgba,
		Stride: frame.Width * 4,
		Rect:   image.Rect(0, 0, frame.Width, frame.Height),
	}

	out, err := os.Create("frame.png")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer out.Close()

	if err := png.Encode(out, img); err != nil {
		fmt.Fprintln(os.Stderr, "png:", err)
		os.Exit(1)
	}
	fmt.Println("Saved frame.png")
}

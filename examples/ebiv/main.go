// Example: transcode any govid-supported video into an EBIV container.
//
// Usage: go run . <input.mp4|.webm|.mpg> <output.ebiv> [-frames N]
//
// EBIV is the decode-optimized format described in .docs/codec-design-plan.md.
// Version 1 stores uncompressed frames, so output files are large — the point
// of this tool is to produce clips for the decoder and for the compression
// measurements the later coding modes are judged against.
package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	govid "github.com/liqmix/govid"
	"github.com/liqmix/govid/av1"
	"github.com/liqmix/govid/ebiv"
	"github.com/liqmix/govid/h264"
	mp4pkg "github.com/liqmix/govid/mp4"
	"github.com/liqmix/govid/mpeg1"
	"github.com/liqmix/govid/vp8"
	"github.com/liqmix/govid/webm"
)

func main() {
	frames := flag.Int("frames", 0, "stop after N frames (0 = all)")
	qp := flag.Int("q", 20, "quantizer 0..63 (lower = higher quality); -1 stores raw/lossless")
	gop := flag.Int("gop", 30, "key-frame interval; 1 keeps every frame intra")
	tileCols := flag.Int("tilecols", 0, "tile columns for parallel decode (0 = 1)")
	tileRows := flag.Int("tilerows", 0, "tile rows for parallel decode (0 = 1)")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s [-q QP] [-gop N] [-frames N] [-tilecols C -tilerows R] <input> <output.ebiv>\n", filepath.Base(os.Args[0]))
		os.Exit(2)
	}
	opt := transcodeOptions{qp: *qp, gop: *gop, tileCols: *tileCols, tileRows: *tileRows, limit: *frames}
	if err := transcode(flag.Arg(0), flag.Arg(1), opt); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// transcodeOptions configures an EBIV encode.
type transcodeOptions struct {
	qp       int // -1 selects the raw/lossless mode
	gop      int
	tileCols int
	tileRows int
	limit    int
}

func (o transcodeOptions) encoderOptions() []ebiv.EncoderOption {
	if o.qp < 0 {
		return nil // raw mode
	}
	opts := []ebiv.EncoderOption{
		ebiv.WithIntra(o.qp),
		ebiv.WithGOP(o.gop),
	}
	if o.tileCols > 1 || o.tileRows > 1 {
		// Explicit grid the caller asked for.
		opts = append(opts, ebiv.WithTiles(o.tileCols, o.tileRows))
	} else {
		// Default: size the tile grid to the machine so encode and decode both
		// use every core. Tiles cost a fraction of a dB at their edges.
		opts = append(opts, ebiv.WithAutoTiles(runtime.NumCPU()))
	}
	return opts
}

func transcode(inPath, outPath string, opt transcodeOptions) error {
	in, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer in.Close()

	demuxer, codec, err := openSource(in, inPath)
	if err != nil {
		return err
	}
	defer demuxer.Close()

	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	// The encoder needs the exact frame geometry up front, and a demuxer's
	// advertised size can disagree with what the codec actually produces
	// (cropping, alignment). Decode the first frame and trust that instead.
	first, err := readFrame(demuxer, codec)
	if err != nil {
		return fmt.Errorf("decode first frame: %w", err)
	}
	num, den := rationalFPS(demuxer.VideoInfo().FrameRate)
	cfg := ebiv.Config{Width: first.Width, Height: first.Height, FPSNum: num, FPSDen: den}

	enc, err := ebiv.NewEncoder(out, cfg, opt.encoderOptions()...)
	if err != nil {
		return err
	}
	if err := enc.WriteFrame(first.YCbCr); err != nil {
		return fmt.Errorf("encode frame 0: %w", err)
	}

	for opt.limit == 0 || enc.FrameCount() < opt.limit {
		frame, err := readFrame(demuxer, codec)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("decode frame %d: %w", enc.FrameCount(), err)
		}
		if err := enc.WriteFrame(frame.YCbCr); err != nil {
			return fmt.Errorf("encode frame %d: %w", enc.FrameCount(), err)
		}
	}
	if err := enc.Close(); err != nil {
		return err
	}

	size, _ := out.Seek(0, io.SeekCurrent)
	fmt.Printf("%s: %d frames, %dx%d @ %d/%d fps, %.1f MiB\n",
		outPath, enc.FrameCount(), cfg.Width, cfg.Height, num, den, float64(size)/(1<<20))
	return nil
}

// readFrame pulls packets until the codec yields a displayable frame. Codecs
// legitimately return nil for a packet that only updates reference state.
func readFrame(d govid.Demuxer, c govid.Codec) (*govid.Frame, error) {
	for {
		pkt, err := d.NextPacket()
		if err != nil {
			if err == io.EOF {
				if drainer, ok := c.(govid.FrameDrainer); ok {
					if f := drainer.Drain(); f != nil {
						return f, nil
					}
				}
			}
			return nil, err
		}
		frame, err := c.Decode(pkt)
		if err != nil {
			return nil, err
		}
		if frame != nil {
			return frame, nil
		}
	}
}

func openSource(f *os.File, path string) (govid.Demuxer, govid.Codec, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4":
		d, err := mp4pkg.NewDemuxer(f)
		if err != nil {
			return nil, nil, fmt.Errorf("mp4 demuxer: %w", err)
		}
		if d.CodecType() == "av1" {
			return d, av1.NewCodec(), nil
		}
		return d, h264.NewCodec(), nil

	case ".webm":
		d, err := webm.NewDemuxer(f)
		if err != nil {
			return nil, nil, fmt.Errorf("webm demuxer: %w", err)
		}
		if d.CodecID() == "V_AV1" {
			return d, av1.NewCodec(), nil
		}
		return d, vp8.NewCodec(), nil

	case ".mpg", ".mpeg":
		s, err := mpeg1.NewSource(f)
		if err != nil {
			return nil, nil, fmt.Errorf("mpeg1 source: %w", err)
		}
		return s, s, nil

	case ".ebiv":
		d, err := ebiv.NewDemuxer(f)
		if err != nil {
			return nil, nil, fmt.Errorf("ebiv demuxer: %w", err)
		}
		return d, ebiv.NewCodec(), nil

	default:
		return nil, nil, fmt.Errorf("unsupported input format: %s", filepath.Ext(path))
	}
}

// rationalFPS recovers an exact frame-rate fraction from a demuxer's float.
// The NTSC family (n*1000/1001) is checked explicitly because rounding it to
// three decimals would drift by a frame every few minutes.
func rationalFPS(fps float64) (num, den uint32) {
	if fps <= 0 || math.IsNaN(fps) || math.IsInf(fps, 0) {
		return 30, 1
	}
	for _, n := range []uint32{24, 25, 30, 48, 50, 60, 120} {
		if math.Abs(fps-float64(n)) < 1e-6 {
			return n, 1
		}
		if math.Abs(fps-float64(n)*1000/1001) < 1e-3 {
			return n * 1000, 1001
		}
	}
	return uint32(math.Round(fps * 1000)), 1000
}

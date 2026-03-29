// Example: play videos in an Ebitengine window with left/right arrow cycling.
//
// Usage: go run . [videos-dir]
//
// Discovers all supported video files (.webm, .mpg, .mpeg) in the given
// directory (default: examples/videos/) and lets you cycle through them
// with the left/right arrow keys.
package main

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	govid "github.com/liqmix/govid"
	"github.com/liqmix/govid/av1"
	govidebiten "github.com/liqmix/govid/ebitengine"
	"github.com/liqmix/govid/h264"
	mp4pkg "github.com/liqmix/govid/mp4"
	"github.com/liqmix/govid/mpeg1"
	"github.com/liqmix/govid/vp8"
	"github.com/liqmix/govid/webm"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	etext "github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"golang.org/x/image/font/gofont/goregular"
)

const (
	seekDelta      = 5 * time.Second
	wheelSeekDelta = 2 * time.Second
	hudShowTicks   = 180 // ~3s at 60 TPS
	hudHeight      = 100
	progressHeight = 16
	hudPad         = 8
	primaryFontSz  = 20.0
	secondFontSz   = 14.0
	hudSlideSpeed  = 0.1 // ~167ms full slide at 60 TPS
	dblClickTicks  = 20  // ~333ms at 60 TPS
	btnWidth       = 36
	btnHeight      = 28
	btnGap         = 6
	btnCount       = 5 // |<  <<  >/||  >>  >|
)

var fontSource *etext.GoTextFaceSource

func init() {
	s, err := etext.NewGoTextFaceSource(bytes.NewReader(goregular.TTF))
	if err != nil {
		panic(err)
	}
	fontSource = s
}

var supportedExts = map[string]bool{
	".webm": true,
	".mpg":  true,
	".mpeg": true,
	".mp4":  true,
}

func discoverVideos(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if supportedExts[ext] {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)
	return paths
}

func loadVideo(path string) (*govidebiten.VideoImage, []io.Closer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}

	ext := strings.ToLower(filepath.Ext(path))
	var player *govid.Player
	var closers []io.Closer

	switch ext {
	case ".webm":
		demuxer, err := webm.NewDemuxer(f)
		if err != nil {
			f.Close()
			return nil, nil, fmt.Errorf("webm demuxer: %w", err)
		}
		var codec govid.Codec
		switch demuxer.CodecID() {
		case "V_AV1":
			codec = av1.NewCodec()
		default:
			codec = vp8.NewCodec()
		}
		player, err = govid.NewPlayer(demuxer, codec)
		if err != nil {
			demuxer.Close()
			f.Close()
			return nil, nil, fmt.Errorf("player: %w", err)
		}
		closers = []io.Closer{demuxer, f}

	case ".mpg", ".mpeg":
		source, err := mpeg1.NewSource(f)
		if err != nil {
			f.Close()
			return nil, nil, fmt.Errorf("mpeg1 source: %w", err)
		}
		player, err = govid.NewPlayer(source, source)
		if err != nil {
			source.Close()
			f.Close()
			return nil, nil, fmt.Errorf("player: %w", err)
		}
		closers = []io.Closer{source, f}

	case ".mp4":
		demuxer, err := mp4pkg.NewDemuxer(f)
		if err != nil {
			f.Close()
			return nil, nil, fmt.Errorf("mp4 demuxer: %w", err)
		}
		var codec govid.Codec
		switch demuxer.CodecType() {
		case "av1":
			codec = av1.NewCodec()
		default:
			codec = h264.NewCodec()
		}
		player, err = govid.NewPlayer(demuxer, codec)
		if err != nil {
			demuxer.Close()
			f.Close()
			return nil, nil, fmt.Errorf("player: %w", err)
		}
		closers = []io.Closer{demuxer, f}

	default:
		f.Close()
		return nil, nil, fmt.Errorf("unsupported format: %s", ext)
	}

	player.Play()
	video := govidebiten.New(player)
	return video, closers, nil
}

type Game struct {
	paths      []string
	index      int
	video      *govidebiten.VideoImage
	closers    []io.Closer
	looping    bool
	hudVisible bool
	hudTimer   int
	hudSlide   float64 // 0.0 = off-screen, 1.0 = fully visible

	// Mouse tracking
	lastMouseX int
	lastMouseY int

	// Double-click detection
	lastClickTick int
	tickCount     int

	// Help overlay
	helpVisible bool

	// Font faces
	primaryFace *etext.GoTextFace
	secondFace  *etext.GoTextFace
}

func (g *Game) switchVideo(index int) error {
	n := len(g.paths)
	for attempts := 0; attempts < n; attempts++ {
		idx := (index + attempts) % n
		video, closers, err := loadVideo(g.paths[idx])
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", filepath.Base(g.paths[idx]), err)
			continue
		}
		for _, c := range g.closers {
			c.Close()
		}
		g.video = video
		g.closers = closers
		g.index = idx
		g.looping = false
		g.showHUD()
		ebiten.SetWindowTitle(fmt.Sprintf("govid — %s", filepath.Base(g.paths[idx])))
		return nil
	}
	return fmt.Errorf("no loadable video found")
}

func togglePlayPause(player *govid.Player) {
	switch player.State() {
	case govid.StatePlaying:
		player.Pause()
	case govid.StatePaused:
		player.Play()
	case govid.StateStopped:
		if err := player.Seek(0); err != nil {
			fmt.Fprintf(os.Stderr, "seek: %v\n", err)
		} else {
			player.Play()
		}
	}
}

func (g *Game) hudYOffset() int {
	return int(float64(hudHeight) * (1.0 - g.hudSlide))
}

// progressBarRect returns the progress bar bounds in logical coordinates.
func (g *Game) progressBarRect(sw, sh int) (x0, y0, x1, y1 int) {
	off := g.hudYOffset()
	return hudPad, sh - hudPad - progressHeight + off, sw - hudPad, sh - hudPad + off
}

func (g *Game) hitTestProgressBar(mx, my, sw, sh int) bool {
	x0, y0, x1, y1 := g.progressBarRect(sw, sh)
	return mx >= x0 && mx <= x1 && my >= y0 && my <= y1
}

// buttonRect returns the bounding box of the i-th transport button.
func (g *Game) buttonRect(i, sw, sh int) (x0, y0, x1, y1 int) {
	totalW := btnCount*btnWidth + (btnCount-1)*btnGap
	startX := (sw - totalW) / 2
	hudTop := sh - hudHeight + g.hudYOffset()
	bx := startX + i*(btnWidth+btnGap)
	by := hudTop + 34
	return bx, by, bx + btnWidth, by + btnHeight
}

func (g *Game) hitTestButton(mx, my, sw, sh int) int {
	for i := 0; i < btnCount; i++ {
		x0, y0, x1, y1 := g.buttonRect(i, sw, sh)
		if mx >= x0 && mx < x1 && my >= y0 && my < y1 {
			return i
		}
	}
	return -1
}

func (g *Game) seekToClickPosition(mx, sw int, player *govid.Player) {
	dur := player.Duration()
	if dur <= 0 {
		return
	}
	barWidth := sw - 2*hudPad
	if barWidth <= 0 {
		return
	}
	frac := float64(mx-hudPad) / float64(barWidth)
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	target := time.Duration(float64(dur) * frac)
	if err := player.Seek(target); err != nil {
		fmt.Fprintf(os.Stderr, "seek: %v\n", err)
	}
}

func (g *Game) updateCursorShape(mx, my, sw, sh int) {
	if g.hitTestProgressBar(mx, my, sw, sh) || g.hitTestButton(mx, my, sw, sh) >= 0 {
		ebiten.SetCursorShape(ebiten.CursorShapePointer)
	} else {
		ebiten.SetCursorShape(ebiten.CursorShapeDefault)
	}
}

func (g *Game) Update() error {
	g.tickCount++

	// F1 toggles help overlay.
	if inpututil.IsKeyJustPressed(ebiten.KeyF1) {
		g.helpVisible = !g.helpVisible
	}
	// Escape dismisses help overlay.
	if g.helpVisible && inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		g.helpVisible = false
	}

	// Any key press shows the HUD.
	if len(inpututil.AppendJustPressedKeys(nil)) > 0 {
		g.showHUD()
	}

	// Mouse movement shows HUD.
	mx, my := ebiten.CursorPosition()
	if mx != g.lastMouseX || my != g.lastMouseY {
		g.showHUD()
		g.lastMouseX = mx
		g.lastMouseY = my
	}

	// Screen size for hit tests (logical coordinates from Layout).
	frame := g.video.Player().CurrentFrame()
	sw, sh := frame.Width, frame.Height

	// Cursor shape.
	g.updateCursorShape(mx, my, sw, sh)

	if g.hudTimer > 0 {
		g.hudTimer--
		if g.hudTimer == 0 {
			g.hudVisible = false
		}
	}

	// Animate HUD slide.
	if g.hudVisible {
		if g.hudSlide < 1 {
			g.hudSlide += hudSlideSpeed
			if g.hudSlide > 1 {
				g.hudSlide = 1
			}
		}
	} else {
		if g.hudSlide > 0 {
			g.hudSlide -= hudSlideSpeed
			if g.hudSlide < 0 {
				g.hudSlide = 0
			}
		}
	}

	// When help is visible, skip all other input but still update video.
	if g.helpVisible {
		g.video.Update()
		return nil
	}

	player := g.video.Player()

	// Left-click handling.
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		if g.lastClickTick > 0 && (g.tickCount-g.lastClickTick) <= dblClickTicks {
			// Double-click: toggle fullscreen.
			ebiten.SetFullscreen(!ebiten.IsFullscreen())
			g.lastClickTick = 0
		} else {
			g.lastClickTick = g.tickCount
			if g.hitTestProgressBar(mx, my, sw, sh) {
				g.seekToClickPosition(mx, sw, player)
				g.showHUD()
			} else if btnIdx := g.hitTestButton(mx, my, sw, sh); btnIdx >= 0 {
				switch btnIdx {
				case 0: // |< prev video
					prev := (g.index - 1 + len(g.paths)) % len(g.paths)
					if err := g.switchVideo(prev); err != nil {
						fmt.Fprintf(os.Stderr, "switch: %v\n", err)
					}
				case 1: // << rewind 5s
					pos := player.Position() - seekDelta
					if pos < 0 {
						pos = 0
					}
					if err := player.Seek(pos); err != nil {
						fmt.Fprintf(os.Stderr, "seek: %v\n", err)
					}
				case 2: // > / || play/pause
					togglePlayPause(player)
				case 3: // >> forward 5s
					pos := player.Position() + seekDelta
					if dur := player.Duration(); dur > 0 && pos > dur {
						pos = dur
					}
					if err := player.Seek(pos); err != nil {
						fmt.Fprintf(os.Stderr, "seek: %v\n", err)
					}
				case 4: // >| next video
					next := (g.index + 1) % len(g.paths)
					if err := g.switchVideo(next); err != nil {
						fmt.Fprintf(os.Stderr, "switch: %v\n", err)
					}
				}
				g.showHUD()
			}
		}
	}

	// Mouse wheel seek.
	_, yoff := ebiten.Wheel()
	if yoff > 0 {
		pos := player.Position() + wheelSeekDelta
		if dur := player.Duration(); dur > 0 && pos > dur {
			pos = dur
		}
		if err := player.Seek(pos); err != nil {
			fmt.Fprintf(os.Stderr, "seek: %v\n", err)
		}
		g.showHUD()
	} else if yoff < 0 {
		pos := player.Position() - wheelSeekDelta
		if pos < 0 {
			pos = 0
		}
		if err := player.Seek(pos); err != nil {
			fmt.Fprintf(os.Stderr, "seek: %v\n", err)
		}
		g.showHUD()
	}

	// Video cycling.
	next := g.index
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
		next = (g.index + 1) % len(g.paths)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) {
		next = (g.index - 1 + len(g.paths)) % len(g.paths)
	}
	if next != g.index {
		if err := g.switchVideo(next); err != nil {
			fmt.Fprintf(os.Stderr, "switch: %v\n", err)
		}
	}

	// Space: play/pause toggle.
	if inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		togglePlayPause(player)
	}

	// Seek backward.
	if inpututil.IsKeyJustPressed(ebiten.KeyBracketLeft) {
		pos := player.Position() - seekDelta
		if pos < 0 {
			pos = 0
		}
		if err := player.Seek(pos); err != nil {
			fmt.Fprintf(os.Stderr, "seek: %v\n", err)
		}
	}

	// Seek forward.
	if inpututil.IsKeyJustPressed(ebiten.KeyBracketRight) {
		pos := player.Position() + seekDelta
		if dur := player.Duration(); dur > 0 && pos > dur {
			pos = dur
		}
		if err := player.Seek(pos); err != nil {
			fmt.Fprintf(os.Stderr, "seek: %v\n", err)
		}
	}

	// Toggle loop.
	if inpututil.IsKeyJustPressed(ebiten.KeyL) {
		g.looping = !g.looping
		player.SetLoop(g.looping)
	}

	g.video.Update()
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.DrawImage(g.video.Image(), nil)
	if g.hudSlide > 0 {
		g.drawHUD(screen)
	}
	if g.helpVisible {
		g.drawHelp(screen)
	}
}

func (g *Game) showHUD() {
	g.hudVisible = true
	g.hudTimer = hudShowTicks
}

func (g *Game) drawHUD(screen *ebiten.Image) {
	sw, sh := screen.Bounds().Dx(), screen.Bounds().Dy()
	hudTop := float32(sh - hudHeight + g.hudYOffset())

	// Semi-transparent background.
	vector.FillRect(screen, 0, hudTop, float32(sw), hudHeight, color.RGBA{0, 0, 0, 180}, false)

	player := g.video.Player()

	filename := filepath.Base(g.paths[g.index])

	// Time info.
	pos := player.Position()
	dur := player.Duration()
	posStr := formatDuration(pos)
	durStr := "--:--"
	if dur > 0 {
		durStr = formatDuration(dur)
	}

	loopTag := ""
	if g.looping {
		loopTag = "[LOOP] "
	}

	leftText := filename
	rightText := fmt.Sprintf("%s%d/%d  %s/%s", loopTag, g.index+1, len(g.paths), posStr, durStr)

	// Draw left text (primary face, white).
	{
		op := &etext.DrawOptions{}
		op.GeoM.Translate(float64(hudPad), float64(hudTop)+8)
		op.ColorScale.ScaleWithColor(color.White)
		etext.Draw(screen, leftText, g.primaryFace, op)
	}

	// Draw right text (secondary face, light gray), right-aligned.
	{
		w, _ := etext.Measure(rightText, g.secondFace, 0)
		op := &etext.DrawOptions{}
		op.GeoM.Translate(float64(sw)-w-float64(hudPad), float64(hudTop)+10)
		op.ColorScale.ScaleWithColor(color.RGBA{200, 200, 200, 255})
		etext.Draw(screen, rightText, g.secondFace, op)
	}

	// Transport buttons.
	g.drawButtons(screen, sw, sh)

	// Progress bar.
	x0, y0, x1, _ := g.progressBarRect(sw, sh)
	barWidth := float32(x1 - x0)

	// Gray track.
	vector.FillRect(screen, float32(x0), float32(y0), barWidth, progressHeight, color.RGBA{80, 80, 80, 255}, false)

	// Green fill.
	if dur > 0 {
		frac := float32(pos) / float32(dur)
		if frac > 1 {
			frac = 1
		}
		vector.FillRect(screen, float32(x0), float32(y0), barWidth*frac, progressHeight, color.RGBA{0, 200, 0, 255}, false)
	}
}

func (g *Game) drawButtons(screen *ebiten.Image, sw, sh int) {
	player := g.video.Player()
	mx, my := ebiten.CursorPosition()

	labels := [btnCount]string{"|<", "<<", ">", ">>", ">|"}
	if player.State() == govid.StatePaused || player.State() == govid.StateStopped {
		labels[2] = ">"
	} else {
		labels[2] = "||"
	}

	for i := 0; i < btnCount; i++ {
		x0, y0, x1, y1 := g.buttonRect(i, sw, sh)
		hovered := mx >= x0 && mx < x1 && my >= y0 && my < y1

		bg := color.RGBA{60, 60, 60, 255}
		if hovered {
			bg = color.RGBA{90, 90, 90, 255}
		}
		vector.FillRect(screen, float32(x0), float32(y0), float32(x1-x0), float32(y1-y0), bg, false)

		// Center label text in button.
		tw, th := etext.Measure(labels[i], g.secondFace, 0)
		tx := float64(x0) + (float64(x1-x0)-tw)/2
		ty := float64(y0) + (float64(y1-y0)-th)/2
		op := &etext.DrawOptions{}
		op.GeoM.Translate(tx, ty)
		op.ColorScale.ScaleWithColor(color.White)
		etext.Draw(screen, labels[i], g.secondFace, op)
	}
}

func (g *Game) drawHelp(screen *ebiten.Image) {
	sw, sh := screen.Bounds().Dx(), screen.Bounds().Dy()

	// Full-screen semi-transparent overlay.
	vector.FillRect(screen, 0, 0, float32(sw), float32(sh), color.RGBA{0, 0, 0, 160}, false)

	// Centered panel.
	panelW, panelH := 400, 320
	px := (sw - panelW) / 2
	py := (sh - panelH) / 2
	vector.FillRect(screen, float32(px), float32(py), float32(panelW), float32(panelH), color.RGBA{30, 30, 30, 220}, false)

	// Title.
	{
		title := "Keyboard Shortcuts"
		tw, _ := etext.Measure(title, g.primaryFace, 0)
		op := &etext.DrawOptions{}
		op.GeoM.Translate(float64(px)+(float64(panelW)-tw)/2, float64(py)+16)
		op.ColorScale.ScaleWithColor(color.White)
		etext.Draw(screen, title, g.primaryFace, op)
	}

	// Keybind rows.
	type row struct{ key, desc string }
	rows := []row{
		{"Space", "Play / Pause"},
		{"[ / ]", "Seek -5s / +5s"},
		{"Left / Right", "Prev / Next video"},
		{"L", "Toggle loop"},
		{"Scroll", "Seek -2s / +2s"},
		{"Double-click", "Toggle fullscreen"},
		{"F1 / Esc", "Close this help"},
	}

	colKey := float64(px) + 24
	colDesc := float64(px) + 180
	startY := float64(py) + 60

	for i, r := range rows {
		y := startY + float64(i)*34
		// Key name.
		{
			op := &etext.DrawOptions{}
			op.GeoM.Translate(colKey, y)
			op.ColorScale.ScaleWithColor(color.RGBA{220, 220, 220, 255})
			etext.Draw(screen, r.key, g.secondFace, op)
		}
		// Description.
		{
			op := &etext.DrawOptions{}
			op.GeoM.Translate(colDesc, y)
			op.ColorScale.ScaleWithColor(color.RGBA{180, 180, 180, 255})
			etext.Draw(screen, r.desc, g.secondFace, op)
		}
	}
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	m := total / 60
	s := total % 60
	return fmt.Sprintf("%02d:%02d", m, s)
}

func (g *Game) Layout(_, _ int) (int, int) {
	f := g.video.Player().CurrentFrame()
	return f.Width, f.Height
}

func main() {
	dir := "examples/videos/"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	paths := discoverVideos(dir)
	if len(paths) == 0 {
		fmt.Fprintf(os.Stderr, "no supported videos (.webm, .mpg, .mpeg, .mp4) found in %s\n", dir)
		os.Exit(1)
	}

	var video *govidebiten.VideoImage
	var closers []io.Closer
	startIdx := 0
	for i, p := range paths {
		v, c, err := loadVideo(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", filepath.Base(p), err)
			continue
		}
		video = v
		closers = c
		startIdx = i
		break
	}
	if video == nil {
		fmt.Fprintln(os.Stderr, "no loadable videos found")
		os.Exit(1)
	}

	g := &Game{
		paths:       paths,
		index:       startIdx,
		video:       video,
		closers:     closers,
		hudVisible:  true,
		hudTimer:    hudShowTicks,
		hudSlide:    1,
		primaryFace: &etext.GoTextFace{Source: fontSource, Size: primaryFontSz},
		secondFace:  &etext.GoTextFace{Source: fontSource, Size: secondFontSz},
	}

	ebiten.SetWindowTitle(fmt.Sprintf("govid — %s", filepath.Base(paths[startIdx])))
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)

	if err := ebiten.RunGame(g); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

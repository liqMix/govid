// Example: a generic video player built on govid + Ebitengine.
//
// Usage: go run . [file-or-dir]
//
// No video is loaded by default — use the toolbar's Open button (or press O)
// to pick a file with the OS file dialog. Opening a file builds a playlist
// from its directory so left/right cycle through neighboring videos. Pass a
// file to open it immediately, or a directory to start the picker there.
//
// Keys: O open, Space play/pause, [ ] seek, arrows prev/next,
// A aspect mode (Fit / Stretch / 1:1), L loop, F fullscreen, F1 help.
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
	"github.com/ncruces/zenity"
	"golang.org/x/image/font/gofont/goregular"
)

const (
	seekDelta      = 5 * time.Second
	wheelSeekDelta = 2 * time.Second
	hudShowTicks   = 180 // ~3s at 60 TPS
	hudHeight      = 100
	toolbarHeight  = 32
	toolbarPad     = 10
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
	initialWinW    = 960
	initialWinH    = 540
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

// decodeAhead is how many frames the background decoder keeps queued. Four
// frames absorbs a slow decode without adding noticeable seek latency.
const decodeAhead = 4

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
		player, err = govid.NewAsyncPlayer(demuxer, codec, decodeAhead, govid.WithRGBA())
		if err != nil {
			demuxer.Close()
			f.Close()
			return nil, nil, fmt.Errorf("player: %w", err)
		}
		// The player closes first: it returns only once the decode goroutine
		// has stopped touching the demuxer and file.
		closers = []io.Closer{player, demuxer, f}

	case ".mpg", ".mpeg":
		source, err := mpeg1.NewSource(f)
		if err != nil {
			f.Close()
			return nil, nil, fmt.Errorf("mpeg1 source: %w", err)
		}
		player, err = govid.NewAsyncPlayer(source, source, decodeAhead, govid.WithRGBA())
		if err != nil {
			source.Close()
			f.Close()
			return nil, nil, fmt.Errorf("player: %w", err)
		}
		closers = []io.Closer{player, source, f}

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
		player, err = govid.NewAsyncPlayer(demuxer, codec, decodeAhead, govid.WithRGBA())
		if err != nil {
			demuxer.Close()
			f.Close()
			return nil, nil, fmt.Errorf("player: %w", err)
		}
		closers = []io.Closer{player, demuxer, f}

	default:
		f.Close()
		return nil, nil, fmt.Errorf("unsupported format: %s", ext)
	}

	player.Play()
	video := govidebiten.New(player)
	return video, closers, nil
}

// viewMode selects how the video maps onto the window.
type viewMode int

const (
	viewFit     viewMode = iota // letterboxed, preserves aspect ratio
	viewStretch                 // fills the window, ignores aspect ratio
	viewActual                  // 1:1 pixels, centered
	viewModeCount
)

func (m viewMode) String() string {
	switch m {
	case viewStretch:
		return "Stretch"
	case viewActual:
		return "1:1"
	default:
		return "Fit"
	}
}

// Toolbar button identifiers.
const (
	tbOpen = iota
	tbAspect
	tbLoop
	tbFullscreen
	tbHelp
)

type tbButton struct {
	id     int
	label  string
	x0, x1 int
}

type Game struct {
	paths   []string
	index   int
	video   *govidebiten.VideoImage
	closers []io.Closer
	looping bool
	view    viewMode

	// OS file picker state: pickerOpen guards against double-launch; the
	// dialog runs on its own goroutine and delivers through pickerCh.
	pickerOpen bool
	pickerCh   chan string
	pickerDir  string

	// statusMsg shows transient errors (e.g. a file that failed to open).
	statusMsg   string
	statusTimer int

	hudVisible bool
	hudTimer   int
	hudSlide   float64 // 0.0 = off-screen, 1.0 = fully visible

	// Logical screen size, recorded by Layout for input hit-testing.
	screenW int
	screenH int

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

func (g *Game) setStatus(msg string) {
	g.statusMsg = msg
	g.statusTimer = hudShowTicks * 2
	g.showHUD()
}

// openPicker launches the OS file dialog on a background goroutine so the
// game loop keeps running while it is up.
func (g *Game) openPicker() {
	if g.pickerOpen {
		return
	}
	g.pickerOpen = true
	dir := g.pickerDir
	go func() {
		opts := []zenity.Option{
			zenity.Title("Open video"),
			zenity.FileFilter{Name: "Videos", Patterns: []string{"*.mp4", "*.webm", "*.mpg", "*.mpeg"}, CaseFold: true},
		}
		if dir != "" {
			opts = append(opts, zenity.Filename(dir+string(filepath.Separator)))
		}
		path, err := zenity.SelectFile(opts...)
		if err != nil {
			path = "" // canceled or dialog unavailable
		}
		g.pickerCh <- path
	}()
}

// openFile loads path, replaces the current video, and rebuilds the playlist
// from the file's directory.
func (g *Game) openFile(path string) {
	video, closers, err := loadVideo(path)
	if err != nil {
		g.setStatus(err.Error())
		return
	}
	for _, c := range g.closers {
		c.Close()
	}
	g.video = video
	g.closers = closers
	g.looping = false
	dir := filepath.Dir(path)
	g.pickerDir = dir
	g.paths = discoverVideos(dir)
	g.index = 0
	for i, p := range g.paths {
		if filepath.Clean(p) == filepath.Clean(path) {
			g.index = i
			break
		}
	}
	g.statusMsg = ""
	g.showHUD()
	ebiten.SetWindowTitle(fmt.Sprintf("govid — %s", filepath.Base(path)))
}

func (g *Game) switchVideo(index int) error {
	n := len(g.paths)
	if n == 0 {
		return fmt.Errorf("no playlist")
	}
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

// toolbarYOffset slides the toolbar up and out of view with the HUD; with no
// video loaded it stays pinned.
func (g *Game) toolbarYOffset() int {
	if g.video == nil {
		return 0
	}
	return -int(float64(toolbarHeight) * (1.0 - g.hudSlide))
}

// toolbarButtons lays out the toolbar buttons with widths measured from the
// current labels.
func (g *Game) toolbarButtons() []tbButton {
	loopLabel := "Loop: Off"
	if g.looping {
		loopLabel = "Loop: On"
	}
	fsLabel := "Fullscreen"
	if ebiten.IsFullscreen() {
		fsLabel = "Windowed"
	}
	defs := []tbButton{
		{id: tbOpen, label: "Open…"},
		{id: tbAspect, label: fmt.Sprintf("Aspect: %s", g.view)},
		{id: tbLoop, label: loopLabel},
		{id: tbFullscreen, label: fsLabel},
		{id: tbHelp, label: "Help"},
	}
	x := toolbarPad
	for i := range defs {
		w, _ := etext.Measure(defs[i].label, g.secondFace, 0)
		defs[i].x0 = x
		defs[i].x1 = x + int(w) + 2*toolbarPad
		x = defs[i].x1 + btnGap
	}
	return defs
}

// hitToolbar returns the toolbar button id under (mx,my), or -1.
func (g *Game) hitToolbar(mx, my int) int {
	top := g.toolbarYOffset()
	if my < top || my >= top+toolbarHeight {
		return -1
	}
	for _, b := range g.toolbarButtons() {
		if mx >= b.x0 && mx < b.x1 {
			return b.id
		}
	}
	return -1
}

func (g *Game) toolbarAction(id int) {
	switch id {
	case tbOpen:
		g.openPicker()
	case tbAspect:
		g.view = (g.view + 1) % viewModeCount
	case tbLoop:
		if g.video != nil {
			g.looping = !g.looping
			g.video.Player().SetLoop(g.looping)
		}
	case tbFullscreen:
		ebiten.SetFullscreen(!ebiten.IsFullscreen())
	case tbHelp:
		g.helpVisible = !g.helpVisible
	}
	g.showHUD()
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
	hot := g.hitToolbar(mx, my) >= 0
	if g.video != nil {
		hot = hot || g.hitTestProgressBar(mx, my, sw, sh) || g.hitTestButton(mx, my, sw, sh) >= 0
	}
	if hot {
		ebiten.SetCursorShape(ebiten.CursorShapePointer)
	} else {
		ebiten.SetCursorShape(ebiten.CursorShapeDefault)
	}
}

func (g *Game) Update() error {
	g.tickCount++

	// Deliver a finished file-picker result.
	select {
	case path := <-g.pickerCh:
		g.pickerOpen = false
		if path != "" {
			g.openFile(path)
		}
	default:
	}

	// F1 toggles help overlay.
	if inpututil.IsKeyJustPressed(ebiten.KeyF1) {
		g.helpVisible = !g.helpVisible
	}
	if g.helpVisible {
		if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
			g.helpVisible = false
		}
		if g.video != nil {
			g.video.Update()
		}
		return nil
	}

	// O opens the OS file picker.
	if inpututil.IsKeyJustPressed(ebiten.KeyO) {
		g.openPicker()
	}

	// Any key press or mouse movement shows the HUD.
	if len(inpututil.AppendJustPressedKeys(nil)) > 0 {
		g.showHUD()
	}
	mx, my := ebiten.CursorPosition()
	if mx != g.lastMouseX || my != g.lastMouseY {
		g.showHUD()
		g.lastMouseX = mx
		g.lastMouseY = my
	}

	sw, sh := g.screenW, g.screenH
	g.updateCursorShape(mx, my, sw, sh)

	if g.hudTimer > 0 {
		g.hudTimer--
		if g.hudTimer == 0 {
			g.hudVisible = false
		}
	}
	if g.statusTimer > 0 {
		g.statusTimer--
		if g.statusTimer == 0 {
			g.statusMsg = ""
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

	// Toolbar clicks are handled before anything else and consume the click.
	clicked := inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft)
	if clicked {
		if id := g.hitToolbar(mx, my); id >= 0 {
			g.toolbarAction(id)
			g.lastClickTick = 0
			clicked = false
		}
	}

	// Aspect mode key works with or without a video.
	if inpututil.IsKeyJustPressed(ebiten.KeyA) {
		g.view = (g.view + 1) % viewModeCount
	}
	// F toggles fullscreen.
	if inpututil.IsKeyJustPressed(ebiten.KeyF) {
		ebiten.SetFullscreen(!ebiten.IsFullscreen())
	}

	if g.video == nil {
		return nil
	}
	player := g.video.Player()

	// Left-click handling (below the toolbar).
	if clicked {
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
					if err := g.switchVideo((g.index - 1 + len(g.paths)) % len(g.paths)); err != nil {
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
					if err := g.switchVideo((g.index + 1) % len(g.paths)); err != nil {
						fmt.Fprintf(os.Stderr, "switch: %v\n", err)
					}
				}
				g.showHUD()
			}
		}
	}

	// Mouse wheel seek.
	_, yoff := ebiten.Wheel()
	if yoff != 0 {
		pos := player.Position()
		if yoff > 0 {
			pos += wheelSeekDelta
		} else {
			pos -= wheelSeekDelta
		}
		if pos < 0 {
			pos = 0
		}
		if dur := player.Duration(); dur > 0 && pos > dur {
			pos = dur
		}
		if err := player.Seek(pos); err != nil {
			fmt.Fprintf(os.Stderr, "seek: %v\n", err)
		}
		g.showHUD()
	}

	// Playlist cycling.
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

	// Seek backward / forward.
	if inpututil.IsKeyJustPressed(ebiten.KeyBracketLeft) {
		pos := player.Position() - seekDelta
		if pos < 0 {
			pos = 0
		}
		if err := player.Seek(pos); err != nil {
			fmt.Fprintf(os.Stderr, "seek: %v\n", err)
		}
	}
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

// drawVideo scales the current frame into the window per the view mode.
func (g *Game) drawVideo(screen *ebiten.Image) {
	img := g.video.Image()
	iw, ih := img.Bounds().Dx(), img.Bounds().Dy()
	sw, sh := screen.Bounds().Dx(), screen.Bounds().Dy()
	if iw == 0 || ih == 0 {
		return
	}

	op := &ebiten.DrawImageOptions{}
	op.Filter = ebiten.FilterLinear
	switch g.view {
	case viewStretch:
		op.GeoM.Scale(float64(sw)/float64(iw), float64(sh)/float64(ih))
	case viewActual:
		op.GeoM.Translate(float64(sw-iw)/2, float64(sh-ih)/2)
	default: // viewFit
		s := float64(sw) / float64(iw)
		if s2 := float64(sh) / float64(ih); s2 < s {
			s = s2
		}
		op.GeoM.Scale(s, s)
		op.GeoM.Translate((float64(sw)-float64(iw)*s)/2, (float64(sh)-float64(ih)*s)/2)
	}
	screen.DrawImage(img, op)
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.Fill(color.RGBA{16, 16, 16, 255})
	if g.video != nil {
		g.drawVideo(screen)
		if g.hudSlide > 0 {
			g.drawHUD(screen)
		}
	} else {
		g.drawEmptyState(screen)
	}
	if g.video == nil || g.hudSlide > 0 {
		g.drawToolbar(screen)
	}
	if g.helpVisible {
		g.drawHelp(screen)
	}
}

func (g *Game) drawEmptyState(screen *ebiten.Image) {
	sw, sh := screen.Bounds().Dx(), screen.Bounds().Dy()
	msg := "Open a video to get started  (O)"
	tw, th := etext.Measure(msg, g.primaryFace, 0)
	op := &etext.DrawOptions{}
	op.GeoM.Translate((float64(sw)-tw)/2, (float64(sh)-th)/2)
	op.ColorScale.ScaleWithColor(color.RGBA{140, 140, 140, 255})
	etext.Draw(screen, msg, g.primaryFace, op)

	if g.statusMsg != "" {
		w, _ := etext.Measure(g.statusMsg, g.secondFace, 0)
		op := &etext.DrawOptions{}
		op.GeoM.Translate((float64(sw)-w)/2, (float64(sh)-th)/2+36)
		op.ColorScale.ScaleWithColor(color.RGBA{255, 120, 120, 255})
		etext.Draw(screen, g.statusMsg, g.secondFace, op)
	}
}

func (g *Game) drawToolbar(screen *ebiten.Image) {
	sw := screen.Bounds().Dx()
	top := g.toolbarYOffset()
	if top <= -toolbarHeight {
		return
	}

	mx, my := ebiten.CursorPosition()

	vector.FillRect(screen, 0, float32(top), float32(sw), toolbarHeight, color.RGBA{0, 0, 0, 200}, false)

	for _, b := range g.toolbarButtons() {
		hovered := my >= top && my < top+toolbarHeight && mx >= b.x0 && mx < b.x1
		if hovered {
			vector.FillRect(screen, float32(b.x0), float32(top+3), float32(b.x1-b.x0), toolbarHeight-6, color.RGBA{80, 80, 80, 255}, false)
		}
		op := &etext.DrawOptions{}
		op.GeoM.Translate(float64(b.x0)+toolbarPad, float64(top)+8)
		op.ColorScale.ScaleWithColor(color.RGBA{225, 225, 225, 255})
		etext.Draw(screen, b.label, g.secondFace, op)
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

	leftText := ""
	if g.index < len(g.paths) {
		leftText = filepath.Base(g.paths[g.index])
	}
	if g.statusMsg != "" {
		leftText = g.statusMsg
	}

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
	rightText := fmt.Sprintf("%s%d/%d  %s/%s", loopTag, g.index+1, len(g.paths), posStr, durStr)

	// Draw left text (primary face, white; red if it is an error).
	{
		c := color.RGBA{255, 255, 255, 255}
		if g.statusMsg != "" {
			c = color.RGBA{255, 120, 120, 255}
		}
		op := &etext.DrawOptions{}
		op.GeoM.Translate(float64(hudPad), float64(hudTop)+8)
		op.ColorScale.ScaleWithColor(c)
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
	if player.State() == govid.StatePlaying {
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
	panelW, panelH := 420, 420
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
		{"O", "Open file (OS dialog)"},
		{"Space", "Play / Pause"},
		{"[ / ]", "Seek -5s / +5s"},
		{"Left / Right", "Prev / Next in folder"},
		{"A", "Aspect: Fit / Stretch / 1:1"},
		{"L", "Toggle loop"},
		{"F", "Toggle fullscreen"},
		{"Scroll", "Seek -2s / +2s"},
		{"Double-click", "Toggle fullscreen"},
		{"F1 / Esc", "Close this help"},
	}

	colKey := float64(px) + 24
	colDesc := float64(px) + 180
	startY := float64(py) + 60

	for i, r := range rows {
		y := startY + float64(i)*34
		{
			op := &etext.DrawOptions{}
			op.GeoM.Translate(colKey, y)
			op.ColorScale.ScaleWithColor(color.RGBA{220, 220, 220, 255})
			etext.Draw(screen, r.key, g.secondFace, op)
		}
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

// Layout uses the window size as the logical resolution: UI stays crisp at
// any window size and the video is scaled in Draw per the view mode.
func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	if outsideWidth < 1 {
		outsideWidth = 1
	}
	if outsideHeight < 1 {
		outsideHeight = 1
	}
	g.screenW, g.screenH = outsideWidth, outsideHeight
	return outsideWidth, outsideHeight
}

func main() {
	g := &Game{
		pickerCh:    make(chan string, 1),
		primaryFace: &etext.GoTextFace{Source: fontSource, Size: primaryFontSz},
		secondFace:  &etext.GoTextFace{Source: fontSource, Size: secondFontSz},
	}

	// Optional argument: a file to open immediately, or a directory the file
	// picker starts in.
	if len(os.Args) > 1 {
		arg := os.Args[1]
		if st, err := os.Stat(arg); err == nil && !st.IsDir() {
			g.openFile(arg)
		} else {
			if abs, err := filepath.Abs(arg); err == nil {
				g.pickerDir = abs
			}
		}
	}

	ebiten.SetWindowTitle("govid")
	ebiten.SetWindowSize(initialWinW, initialWinH)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)

	if err := ebiten.RunGame(g); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

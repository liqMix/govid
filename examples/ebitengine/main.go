// Example: a generic video player built on govid + Ebitengine.
//
// Usage: go run . [file-or-dir]
//
// Starts with a built-in file browser (no video is loaded by default).
// Opening a file builds a playlist from its directory so left/right cycle
// through neighboring videos. Pass a file to open it immediately, or a
// directory to start the browser there.
//
// Keys: O file browser, Space play/pause, [ ] seek, arrows prev/next,
// A aspect mode (Fit / Stretch / 1:1), L loop, F1 help.
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
	browserRowH    = 24
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

// browserEntry is one row of the file browser.
type browserEntry struct {
	name  string
	path  string
	isDir bool
}

type browser struct {
	visible bool
	dir     string
	entries []browserEntry
	sel     int
	scroll  int
	errMsg  string

	// Double-click detection.
	lastClickTick int
	lastClickRow  int
}

func (b *browser) open(dir string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		b.errMsg = err.Error()
		b.visible = true
		return
	}
	b.dir = abs
	b.errMsg = ""
	b.entries = b.entries[:0]
	if parent := filepath.Dir(abs); parent != abs {
		b.entries = append(b.entries, browserEntry{name: "..", path: parent, isDir: true})
	}
	var dirs, files []browserEntry
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, browserEntry{name: name + string(filepath.Separator), path: filepath.Join(abs, name), isDir: true})
		} else if supportedExts[strings.ToLower(filepath.Ext(name))] {
			files = append(files, browserEntry{name: name, path: filepath.Join(abs, name)})
		}
	}
	less := func(s []browserEntry) func(i, j int) bool {
		return func(i, j int) bool { return strings.ToLower(s[i].name) < strings.ToLower(s[j].name) }
	}
	sort.Slice(dirs, less(dirs))
	sort.Slice(files, less(files))
	b.entries = append(append(b.entries, dirs...), files...)
	b.sel = 0
	b.scroll = 0
	b.visible = true
}

type Game struct {
	paths   []string
	index   int
	video   *govidebiten.VideoImage
	closers []io.Closer
	looping bool
	view    viewMode
	browser browser

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

// openFile loads path, replaces the current video, and rebuilds the playlist
// from the file's directory.
func (g *Game) openFile(path string) {
	video, closers, err := loadVideo(path)
	if err != nil {
		g.browser.errMsg = err.Error()
		return
	}
	for _, c := range g.closers {
		c.Close()
	}
	g.video = video
	g.closers = closers
	g.looping = false
	dir := filepath.Dir(path)
	g.paths = discoverVideos(dir)
	g.index = 0
	for i, p := range g.paths {
		if filepath.Clean(p) == filepath.Clean(path) {
			g.index = i
			break
		}
	}
	g.browser.visible = false
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

// browserPanelRect returns the browser panel bounds.
func (g *Game) browserPanelRect() (x0, y0, x1, y1 int) {
	sw, sh := g.screenW, g.screenH
	mx := sw / 12
	my := sh / 12
	return mx, my, sw - mx, sh - my
}

// browserListRect returns the row-list area inside the panel.
func (g *Game) browserListRect() (x0, y0, x1, y1 int) {
	px0, py0, px1, py1 := g.browserPanelRect()
	return px0 + 8, py0 + 40, px1 - 8, py1 - 32
}

func (g *Game) browserVisibleRows() int {
	_, y0, _, y1 := g.browserListRect()
	n := (y1 - y0) / browserRowH
	if n < 1 {
		n = 1
	}
	return n
}

func (g *Game) browserEnsureVisible() {
	rows := g.browserVisibleRows()
	if g.browser.sel < g.browser.scroll {
		g.browser.scroll = g.browser.sel
	}
	if g.browser.sel >= g.browser.scroll+rows {
		g.browser.scroll = g.browser.sel - rows + 1
	}
	if g.browser.scroll < 0 {
		g.browser.scroll = 0
	}
}

func (g *Game) browserActivate(idx int) {
	if idx < 0 || idx >= len(g.browser.entries) {
		return
	}
	e := g.browser.entries[idx]
	if e.isDir {
		g.browser.open(e.path)
	} else {
		g.openFile(e.path)
	}
}

func (g *Game) updateBrowser() {
	b := &g.browser

	// Close (only when something is already playing behind it).
	if g.video != nil &&
		(inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyO)) {
		b.visible = false
		return
	}

	if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) && b.sel > 0 {
		b.sel--
		g.browserEnsureVisible()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) && b.sel < len(b.entries)-1 {
		b.sel++
		g.browserEnsureVisible()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyPageUp) {
		b.sel -= g.browserVisibleRows()
		if b.sel < 0 {
			b.sel = 0
		}
		g.browserEnsureVisible()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyPageDown) {
		b.sel += g.browserVisibleRows()
		if b.sel > len(b.entries)-1 {
			b.sel = len(b.entries) - 1
		}
		g.browserEnsureVisible()
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEnter) {
		g.browserActivate(b.sel)
		return
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
		if parent := filepath.Dir(b.dir); parent != b.dir {
			b.open(parent)
		}
		return
	}

	// Mouse: wheel scrolls, click selects, double-click opens.
	if _, yoff := ebiten.Wheel(); yoff != 0 {
		b.scroll -= int(yoff)
		maxScroll := len(b.entries) - g.browserVisibleRows()
		if maxScroll < 0 {
			maxScroll = 0
		}
		if b.scroll > maxScroll {
			b.scroll = maxScroll
		}
		if b.scroll < 0 {
			b.scroll = 0
		}
	}
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		mx, my := ebiten.CursorPosition()
		x0, y0, x1, _ := g.browserListRect()
		if mx >= x0 && mx < x1 && my >= y0 {
			row := b.scroll + (my-y0)/browserRowH
			if row >= 0 && row < len(b.entries) && (my-y0)/browserRowH < g.browserVisibleRows() {
				if b.lastClickRow == row && g.tickCount-b.lastClickTick <= dblClickTicks {
					g.browserActivate(row)
					b.lastClickTick = 0
				} else {
					b.sel = row
					b.lastClickRow = row
					b.lastClickTick = g.tickCount
				}
			}
		}
	}
}

func (g *Game) Update() error {
	g.tickCount++

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

	// With nothing loaded the browser is the whole UI.
	if g.video == nil {
		g.browser.visible = true
	}
	if g.browser.visible {
		g.updateBrowser()
		if g.video != nil {
			g.video.Update()
		}
		return nil
	}

	// O opens the file browser at the playlist directory.
	if inpututil.IsKeyJustPressed(ebiten.KeyO) {
		dir := "."
		if len(g.paths) > 0 {
			dir = filepath.Dir(g.paths[g.index])
		}
		g.browser.open(dir)
		return nil
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

	sw, sh := g.screenW, g.screenH
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

	// Aspect mode.
	if inpututil.IsKeyJustPressed(ebiten.KeyA) {
		g.view = (g.view + 1) % viewModeCount
		g.showHUD()
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
		if g.hudSlide > 0 && !g.browser.visible {
			g.drawHUD(screen)
		}
	}
	if g.browser.visible {
		g.drawBrowser(screen)
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

	filename := ""
	if g.index < len(g.paths) {
		filename = filepath.Base(g.paths[g.index])
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

	leftText := filename
	rightText := fmt.Sprintf("%s[%s]  %d/%d  %s/%s",
		loopTag, g.view, g.index+1, len(g.paths), posStr, durStr)

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

func (g *Game) drawBrowser(screen *ebiten.Image) {
	sw, sh := screen.Bounds().Dx(), screen.Bounds().Dy()
	px0, py0, px1, py1 := g.browserPanelRect()

	// Dim the background, then the panel.
	vector.FillRect(screen, 0, 0, float32(sw), float32(sh), color.RGBA{0, 0, 0, 140}, false)
	vector.FillRect(screen, float32(px0), float32(py0), float32(px1-px0), float32(py1-py0), color.RGBA{28, 28, 28, 235}, false)

	// Header: current directory.
	{
		op := &etext.DrawOptions{}
		op.GeoM.Translate(float64(px0)+8, float64(py0)+8)
		op.ColorScale.ScaleWithColor(color.White)
		etext.Draw(screen, g.browser.dir, g.primaryFace, op)
	}

	// Rows.
	lx0, ly0, lx1, _ := g.browserListRect()
	rows := g.browserVisibleRows()
	mx, my := ebiten.CursorPosition()
	for i := 0; i < rows; i++ {
		idx := g.browser.scroll + i
		if idx >= len(g.browser.entries) {
			break
		}
		e := g.browser.entries[idx]
		ry := ly0 + i*browserRowH

		hovered := mx >= lx0 && mx < lx1 && my >= ry && my < ry+browserRowH
		if idx == g.browser.sel {
			vector.FillRect(screen, float32(lx0), float32(ry), float32(lx1-lx0), browserRowH, color.RGBA{0, 90, 40, 255}, false)
		} else if hovered {
			vector.FillRect(screen, float32(lx0), float32(ry), float32(lx1-lx0), browserRowH, color.RGBA{55, 55, 55, 255}, false)
		}

		c := color.RGBA{220, 220, 220, 255}
		if e.isDir {
			c = color.RGBA{140, 190, 255, 255}
		}
		op := &etext.DrawOptions{}
		op.GeoM.Translate(float64(lx0)+6, float64(ry)+4)
		op.ColorScale.ScaleWithColor(c)
		etext.Draw(screen, e.name, g.secondFace, op)
	}
	if len(g.browser.entries) == 0 {
		op := &etext.DrawOptions{}
		op.GeoM.Translate(float64(lx0)+6, float64(ly0)+4)
		op.ColorScale.ScaleWithColor(color.RGBA{150, 150, 150, 255})
		etext.Draw(screen, "(no videos or directories here)", g.secondFace, op)
	}

	// Footer: error or hint.
	footer := "Enter open · Backspace up · O/Esc close · F1 help"
	fc := color.RGBA{150, 150, 150, 255}
	if g.browser.errMsg != "" {
		footer = g.browser.errMsg
		fc = color.RGBA{255, 120, 120, 255}
	}
	op := &etext.DrawOptions{}
	op.GeoM.Translate(float64(px0)+8, float64(py1)-24)
	op.ColorScale.ScaleWithColor(fc)
	etext.Draw(screen, footer, g.secondFace, op)
}

func (g *Game) drawHelp(screen *ebiten.Image) {
	sw, sh := screen.Bounds().Dx(), screen.Bounds().Dy()

	// Full-screen semi-transparent overlay.
	vector.FillRect(screen, 0, 0, float32(sw), float32(sh), color.RGBA{0, 0, 0, 160}, false)

	// Centered panel.
	panelW, panelH := 420, 400
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
		{"O", "Open file browser"},
		{"Space", "Play / Pause"},
		{"[ / ]", "Seek -5s / +5s"},
		{"Left / Right", "Prev / Next in folder"},
		{"A", "Aspect: Fit / Stretch / 1:1"},
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
		primaryFace: &etext.GoTextFace{Source: fontSource, Size: primaryFontSz},
		secondFace:  &etext.GoTextFace{Source: fontSource, Size: secondFontSz},
	}

	// Optional argument: a file to open immediately, or a directory to start
	// the browser in. With no argument the browser opens in the CWD.
	browserDir := "."
	if len(os.Args) > 1 {
		arg := os.Args[1]
		if st, err := os.Stat(arg); err == nil && !st.IsDir() {
			g.openFile(arg)
			browserDir = filepath.Dir(arg)
		} else {
			browserDir = arg
		}
	}
	if g.video == nil {
		g.browser.open(browserDir)
	}

	ebiten.SetWindowTitle("govid")
	ebiten.SetWindowSize(initialWinW, initialWinH)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)

	if err := ebiten.RunGame(g); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

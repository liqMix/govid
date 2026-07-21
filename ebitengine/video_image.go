package ebitengine

import (
	govid "github.com/liqmix/govid"
	"github.com/hajimehoshi/ebiten/v2"
)

// VideoImage bridges a govid.Player to an ebiten.Image for rendering.
type VideoImage struct {
	player  *govid.Player
	image   *ebiten.Image
	rgbaBuf []byte
	width   int
	height  int
}

// New creates a VideoImage from a player.
func New(player *govid.Player) *VideoImage {
	f := player.CurrentFrame()
	img := ebiten.NewImage(f.Width, f.Height)
	return &VideoImage{
		player: player,
		image:  img,
		width:  f.Width,
		height: f.Height,
	}
}

// Update advances the player and uploads new pixel data if the frame changed.
func (v *VideoImage) Update() error {
	if v.player.Update() {
		f := v.player.CurrentFrame()
		if f != nil {
			if f.Width != v.width || f.Height != v.height {
				v.image = ebiten.NewImage(f.Width, f.Height)
				v.width = f.Width
				v.height = f.Height
				v.rgbaBuf = nil
			}
			if f.HasRGBA() {
				// Converted on the decode goroutine (govid.WithRGBA); this
				// goroutine only uploads. WritePixels copies into ebiten's
				// atlas, so the buffer may be recycled after this call.
				v.image.WritePixels(f.RGBA())
			} else {
				v.rgbaBuf = f.ConvertRGBA(v.rgbaBuf)
				v.image.WritePixels(v.rgbaBuf)
			}
		}
	}
	return nil
}

// Image returns the ebiten.Image for rendering.
func (v *VideoImage) Image() *ebiten.Image {
	return v.image
}

// Player returns the underlying govid.Player.
func (v *VideoImage) Player() *govid.Player {
	return v.player
}

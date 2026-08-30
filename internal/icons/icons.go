// Package icons procedurally draws DoDaemon's app mark: a white
// hub-and-spoke network glyph (a central node connected to four outer
// nodes — instantly readable as "network/server" at any size) on a solid
// black square, so it reads clearly as a network daemon both in the
// taskbar and at small Explorer thumbnail sizes. Drawing it in code
// instead of shipping a static image file keeps the whole visual identity
// reproducible and lets both the .ico build resource (cmd/icongen) and the
// native window's runtime taskbar icon (internal/nativeui) share one
// source of truth.
package icons

import (
	"image"
	"image/color"
	"math"
)

var (
	bg    = color.RGBA{0x0a, 0x0a, 0x0a, 0xff} // near-black background
	white = color.RGBA{0xff, 0xff, 0xff, 0xff}
)

// Draw renders the mark at size x size pixels.
func Draw(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	c := s / 2

	fillRect(img, bg)

	hubR := s * 0.11
	nodeR := s * 0.075
	lineW := s * 0.05
	dist := s * 0.29

	// Four outer nodes at N/E/S/W, wired back to the hub — a classic
	// hub-and-spoke network topology glyph.
	spokes := []struct{ dx, dy float64 }{
		{0, -dist}, {dist, 0}, {0, dist}, {-dist, 0},
	}
	for _, p := range spokes {
		fillLine(img, c, c, c+p.dx, c+p.dy, lineW, white)
	}
	for _, p := range spokes {
		fillCircle(img, c+p.dx, c+p.dy, nodeR, white)
	}
	fillCircle(img, c, c, hubR, white)

	return img
}

// fillRect paints every pixel col — used for the icon's solid background,
// which needs no antialiasing since it's the full canvas.
func fillRect(img *image.RGBA, col color.RGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetRGBA(x, y, col)
		}
	}
}

func fillCircle(img *image.RGBA, cx, cy, r float64, col color.RGBA) {
	x0, y0 := int(math.Floor(cx-r-1)), int(math.Floor(cy-r-1))
	x1, y1 := int(math.Ceil(cx+r+1)), int(math.Ceil(cy+r+1))
	bounds := img.Bounds()
	for y := y0; y <= y1; y++ {
		if y < bounds.Min.Y || y >= bounds.Max.Y {
			continue
		}
		for x := x0; x <= x1; x++ {
			if x < bounds.Min.X || x >= bounds.Max.X {
				continue
			}
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			d := math.Sqrt(dx*dx + dy*dy)
			blendEdge(img, x, y, r-d, col)
		}
	}
}

// fillLine draws a line segment from (x0,y0) to (x1,y1) with round caps
// and thickness w.
func fillLine(img *image.RGBA, x0, y0, x1, y1, w float64, col color.RGBA) {
	rad := w / 2
	minX, maxX := math.Min(x0, x1)-rad-1, math.Max(x0, x1)+rad+1
	minY, maxY := math.Min(y0, y1)-rad-1, math.Max(y0, y1)+rad+1

	bounds := img.Bounds()
	dx, dy := x1-x0, y1-y0
	lenSq := dx*dx + dy*dy

	for y := int(math.Floor(minY)); y <= int(math.Ceil(maxY)); y++ {
		if y < bounds.Min.Y || y >= bounds.Max.Y {
			continue
		}
		for x := int(math.Floor(minX)); x <= int(math.Ceil(maxX)); x++ {
			if x < bounds.Min.X || x >= bounds.Max.X {
				continue
			}
			px, py := float64(x)+0.5, float64(y)+0.5

			var t float64
			if lenSq > 0 {
				t = ((px-x0)*dx + (py-y0)*dy) / lenSq
				if t < 0 {
					t = 0
				} else if t > 1 {
					t = 1
				}
			}
			cx, cy := x0+t*dx, y0+t*dy
			d := math.Hypot(px-cx, py-cy)
			blendEdge(img, x, y, rad-d, col)
		}
	}
}

// blendEdge alpha-blends col into img at (x,y) based on signed distance
// dist from the shape's edge (>=1 fully inside, <=0 fully outside),
// giving a 1px antialiased edge instead of a jagged one.
func blendEdge(img *image.RGBA, x, y int, dist float64, col color.RGBA) {
	var alpha float64
	switch {
	case dist >= 1:
		alpha = 1
	case dist <= 0:
		return
	default:
		alpha = dist
	}

	dst := img.RGBAAt(x, y)
	a := alpha
	r := uint8(float64(col.R)*a + float64(dst.R)*(1-a))
	g := uint8(float64(col.G)*a + float64(dst.G)*(1-a))
	b := uint8(float64(col.B)*a + float64(dst.B)*(1-a))
	aOut := uint8(255*a + float64(dst.A)*(1-a))
	img.SetRGBA(x, y, color.RGBA{r, g, b, aOut})
}

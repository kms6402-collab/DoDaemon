// Package icons procedurally draws DoDaemon's app mark: an indigo circle
// (matching the web dashboard's accent color, docs/PLAN.md §5.6) holding a
// small broadcast-tower glyph — a base, a mast and a beacon radiating two
// signal arcs — reading as "a daemon serving/broadcasting network
// protocols" (FTP/TFTP/Syslog), with a small green "online" dot echoing
// the dashboard's status badges. Drawing it in code instead of shipping a
// static image file keeps the whole visual identity reproducible and lets
// both the .ico build resource (cmd/icongen) and the native window's
// runtime taskbar icon (internal/nativeui) share one source of truth.
package icons

import (
	"image"
	"image/color"
	"math"
)

var (
	accent = color.RGBA{0x6d, 0x8d, 0xff, 0xff} // matches --accent in webui/static/style.css
	white  = color.RGBA{0xff, 0xff, 0xff, 0xff}
	online = color.RGBA{0x3e, 0xcf, 0x8e, 0xff} // matches --ok
)

// Draw renders the mark at size x size pixels.
func Draw(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	c := s / 2

	fillCircle(img, c, c, c, accent)

	// Base, mast and beacon of a small broadcast tower.
	baseY := c + s*0.22
	beaconY := c - s*0.10
	fillLine(img, c-s*0.15, baseY, c+s*0.15, baseY, s*0.06, white) // foot
	fillLine(img, c, baseY, c, beaconY, s*0.05, white)             // mast
	fillCircle(img, c, beaconY, s*0.075, white)                    // beacon

	// Two concentric signal arcs fanning upward out of the beacon.
	startAngle, endAngle := -140*math.Pi/180, -40*math.Pi/180
	fillArcBand(img, c, beaconY, s*0.12, s*0.155, startAngle, endAngle, white)
	fillArcBand(img, c, beaconY, s*0.19, s*0.225, startAngle, endAngle, white)

	// "online" dot badges the beacon's lower-right edge.
	dotR := s * 0.065
	fillCircle(img, c+s*0.155, beaconY+s*0.135, dotR, online)

	return img
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

// fillArcBand draws a ring segment (like one bar of a WiFi/broadcast icon)
// centered at (cx,cy): the band between innerR and outerR, spanning
// [startAngle, endAngle] radians measured the way math.Atan2 does (0 =
// +X axis, increasing toward +Y — i.e. clockwise in image space since Y
// grows downward; straight up is -math.Pi/2). Radial edges are
// antialiased; angular edges are a hard cutoff, which is fine at icon
// resolutions.
func fillArcBand(img *image.RGBA, cx, cy, innerR, outerR, startAngle, endAngle float64, col color.RGBA) {
	x0, y0 := int(math.Floor(cx-outerR-1)), int(math.Floor(cy-outerR-1))
	x1, y1 := int(math.Ceil(cx+outerR+1)), int(math.Ceil(cy+outerR+1))
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
			d := math.Hypot(dx, dy)
			if d < innerR-1 || d > outerR+1 {
				continue
			}
			angle := math.Atan2(dy, dx)
			if angle < startAngle || angle > endAngle {
				continue
			}
			dist := math.Min(d-innerR, outerR-d)
			blendEdge(img, x, y, dist, col)
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

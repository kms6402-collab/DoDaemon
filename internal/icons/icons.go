// Package icons procedurally draws DoDaemon's app mark in the style of the
// classic Windows network-utility icons (3CDaemon and friends): a blue
// rounded tile holding a white two-unit server with green status LEDs,
// sitting on a small network bus. Shapes are deliberately few and bold so
// the mark stays legible at 16x16 in the taskbar/tray as well as at 256.
// Drawing it in code instead of shipping a static image keeps the visual
// identity reproducible and lets both the .ico build resource
// (cmd/icongen) and the native window's runtime icon (internal/nativeui)
// share one source of truth.
package icons

import (
	"image"
	"image/color"
	"math"
)

var (
	tileTop    = color.RGBA{0x3a, 0x8d, 0xf5, 0xff}
	tileBottom = color.RGBA{0x12, 0x4a, 0xb0, 0xff}
	tileEdge   = color.RGBA{0x0b, 0x33, 0x80, 0xff}
	white      = color.RGBA{0xff, 0xff, 0xff, 0xff}
	slot       = color.RGBA{0x1d, 0x5c, 0xc4, 0xff}
	led        = color.RGBA{0x2e, 0xd1, 0x5a, 0xff}
)

// Draw renders the mark at size x size pixels on a transparent canvas.
func Draw(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size)

	// Tile: a 1px-ish darker rim under a vertical blue gradient.
	pad := math.Max(0.5, s*0.03)
	radius := s * 0.2
	fillRoundRect(img, pad, pad, s-pad, s-pad, radius, func(_, _ float64) color.RGBA { return tileEdge })
	rim := math.Max(1, s*0.035)
	fillRoundRect(img, pad+rim, pad+rim, s-pad-rim, s-pad-rim, radius-rim, func(_, y float64) color.RGBA {
		return lerp(tileTop, tileBottom, (y-pad)/(s-2*pad))
	})

	// Two stacked server units.
	x0, x1 := s*0.22, s*0.78
	unitR := s * 0.05
	units := [][2]float64{{s * 0.20, s * 0.42}, {s * 0.47, s * 0.69}}
	solid := func(c color.RGBA) func(_, _ float64) color.RGBA {
		return func(_, _ float64) color.RGBA { return c }
	}
	for _, u := range units {
		fillRoundRect(img, x0, u[0], x1, u[1], unitR, solid(white))
		mid := (u[0] + u[1]) / 2
		// Drive slot on the left, status LED on the right. Below ~20px the
		// slot would blur into the unit, so only the LED is kept there.
		if size >= 20 {
			fillRoundRect(img, s*0.30, mid-s*0.03, s*0.54, mid+s*0.03, s*0.02, solid(slot))
		}
		fillCircle(img, s*0.67, mid, math.Max(1.1, s*0.05), led)
	}

	// Network bus under the server: a short drop and a horizontal line
	// ending in two nodes.
	busY := s * 0.81
	lw := math.Max(1.2, s*0.05)
	fillLine(img, s*0.5, s*0.69, s*0.5, busY, lw, white)
	fillLine(img, s*0.27, busY, s*0.73, busY, lw, white)
	fillCircle(img, s*0.27, busY, math.Max(1.2, s*0.055), white)
	fillCircle(img, s*0.73, busY, math.Max(1.2, s*0.055), white)

	return img
}

func lerp(a, b color.RGBA, t float64) color.RGBA {
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	mix := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return color.RGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 0xff}
}

// fillRoundRect fills the rounded rectangle (x0,y0)-(x1,y1) with corner
// radius r, antialiased, taking each pixel's colour from col(x, y).
func fillRoundRect(img *image.RGBA, x0, y0, x1, y1, r float64, col func(x, y float64) color.RGBA) {
	if r < 0 {
		r = 0
	}
	cx, cy := (x0+x1)/2, (y0+y1)/2
	hx, hy := (x1-x0)/2-r, (y1-y0)/2-r
	b := img.Bounds()
	for y := int(math.Floor(y0)) - 1; y <= int(math.Ceil(y1)); y++ {
		if y < b.Min.Y || y >= b.Max.Y {
			continue
		}
		for x := int(math.Floor(x0)) - 1; x <= int(math.Ceil(x1)); x++ {
			if x < b.Min.X || x >= b.Max.X {
				continue
			}
			px, py := float64(x)+0.5, float64(y)+0.5
			qx := math.Max(math.Abs(px-cx)-hx, 0)
			qy := math.Max(math.Abs(py-cy)-hy, 0)
			outside := math.Hypot(qx, qy) - r
			inside := math.Min(math.Max(math.Abs(px-cx)-hx, math.Abs(py-cy)-hy), 0)
			d := outside + inside // signed distance, negative inside
			blendEdge(img, x, y, 0.5-d, col(px, py))
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

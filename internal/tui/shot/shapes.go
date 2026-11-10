// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package shot

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/vector"
)

// Line weights of a box-drawing stroke.
const (
	none   = 0
	light  = 1
	heavy  = 2
	double = 3
)

// boxLines are the box-drawing characters as the weight of each arm: up,
// right, down, left.
var boxLines = map[rune][4]uint8{
	'─': {none, light, none, light}, '━': {none, heavy, none, heavy},
	'│': {light, none, light, none}, '┃': {heavy, none, heavy, none},
	'┌': {none, light, light, none}, '┏': {none, heavy, heavy, none},
	'┐': {none, none, light, light}, '┓': {none, none, heavy, heavy},
	'└': {light, light, none, none}, '┗': {heavy, heavy, none, none},
	'┘': {light, none, none, light}, '┛': {heavy, none, none, heavy},
	'├': {light, light, light, none}, '┣': {heavy, heavy, heavy, none},
	'┤': {light, none, light, light}, '┫': {heavy, none, heavy, heavy},
	'┬': {none, light, light, light}, '┳': {none, heavy, heavy, heavy},
	'┴': {light, light, none, light}, '┻': {heavy, heavy, none, heavy},
	'┼': {light, light, light, light}, '╋': {heavy, heavy, heavy, heavy},
	'╴': {none, none, none, light}, '╵': {light, none, none, none},
	'╶': {none, light, none, none}, '╷': {none, none, light, none},
	'═': {none, double, none, double}, '║': {double, none, double, none},
	'╔': {none, double, double, none}, '╗': {none, none, double, double},
	'╚': {double, double, none, none}, '╝': {double, none, none, double},
	'╠': {double, double, double, none}, '╣': {double, none, double, double},
	'╦': {none, double, double, double}, '╩': {double, double, none, double},
	'╬': {double, double, double, double},
}

// drawShape draws r as a shape in cell when it is one this file draws
// (box drawing, block elements, braille, a few symbols) and reports
// whether it did.
func drawShape(img *image.RGBA, cell image.Rectangle, r rune, col color.RGBA, scale int) bool {
	if arms, ok := boxLines[r]; ok {
		boxArms(img, cell, arms, col, scale)
		return true
	}
	switch {
	case r >= 0x256D && r <= 0x2570: // ╭╮╯╰
		arc(cell, r, col, scale, img)
		return true
	case r >= 0x2580 && r <= 0x259F:
		return block(img, cell, r, col)
	case r >= 0x2800 && r <= 0x28FF:
		braille(img, cell, r, col)
		return true
	}
	return symbol(img, cell, r, col, scale)
}

// thickness is the pixel width of a stroke of weight w.
func thickness(w uint8, scale int) int {
	if w == heavy {
		return 2 * scale
	}
	return scale
}

// boxArms draws the arms of a box-drawing character from the cell's
// centre to its edges.
func boxArms(img *image.RGBA, cell image.Rectangle, arms [4]uint8, col color.RGBA, scale int) {
	cx := cell.Min.X + cell.Dx()/2
	cy := cell.Min.Y + cell.Dy()/2
	// The widest crossing arm, so arms meet without a notch.
	hw, vw := max(thickness(arms[1], scale), thickness(arms[3], scale)), max(thickness(arms[0], scale), thickness(arms[2], scale))
	for k, w := range arms {
		if w == none {
			continue
		}
		offsets := []int{0}
		t := thickness(w, scale)
		if w == double {
			offsets = []int{-scale, scale}
		}
		for _, o := range offsets {
			switch k {
			case 0: // up
				x := cx + o - t/2
				fill(img, image.Rect(x, cell.Min.Y, x+t, cy+hw/2+hw%2), col)
			case 2: // down
				x := cx + o - t/2
				fill(img, image.Rect(x, cy-hw/2, x+t, cell.Max.Y), col)
			case 1: // right
				y := cy + o - t/2
				fill(img, image.Rect(cx-vw/2, y, cell.Max.X, y+t), col)
			case 3: // left
				y := cy + o - t/2
				fill(img, image.Rect(cell.Min.X, y, cx+vw/2+vw%2, y+t), col)
			}
		}
	}
}

// arc draws a rounded corner (╭╮╯╰): a quarter circle joining the two
// arms' ends at the cell's edges.
func arc(cell image.Rectangle, r rune, col color.RGBA, scale int, img *image.RGBA) {
	w, h := float32(cell.Dx()), float32(cell.Dy())
	t := float32(scale)
	cx, cy := float32(int(w/2)), float32(int(h/2))
	// Centre line of the strokes: x = cx + t/2 - t/2 ... the stroke spans
	// [cx - t/2, cx + t/2) like boxArms'.
	mx, my := cx-float32(int(t)/2)+t/2, cy-float32(int(t)/2)+t/2
	rad := min(w, h) / 2
	// The corner's direction: where the arms go.
	var dx, dy float32 // +1 right/down, -1 left/up
	switch r {
	case '╭':
		dx, dy = 1, 1
	case '╮':
		dx, dy = -1, 1
	case '╯':
		dx, dy = -1, -1
	case '╰':
		dx, dy = 1, -1
	}
	// Arc centre, then the polyline: vertical arm from the edge to the
	// arc, the arc, horizontal arm to the edge.
	ax, ay := mx+dx*rad, my+dy*rad
	var pts [][2]float32
	edgeY := float32(0)
	if dy > 0 {
		edgeY = h
	}
	edgeX := float32(0)
	if dx > 0 {
		edgeX = w
	}
	pts = append(pts, [2]float32{mx, edgeY}, [2]float32{mx, ay})
	const n = 16
	for k := 0; k <= n; k++ {
		a := float64(k) / n * math.Pi / 2
		// From (mx, ay) to (ax, my) around (ax, ay).
		x := ax - dx*rad*float32(math.Cos(a))
		y := ay - dy*rad*float32(math.Sin(a))
		pts = append(pts, [2]float32{x, y})
	}
	pts = append(pts, [2]float32{edgeX, my})
	stroke(img, cell, pts, t, col)
}

// stroke draws the polyline pts (cell coordinates) w pixels wide.
func stroke(img *image.RGBA, cell image.Rectangle, pts [][2]float32, w float32, col color.RGBA) {
	z := vector.NewRasterizer(cell.Dx(), cell.Dy())
	for k := 1; k < len(pts); k++ {
		x0, y0, x1, y1 := pts[k-1][0], pts[k-1][1], pts[k][0], pts[k][1]
		dx, dy := x1-x0, y1-y0
		l := float32(math.Hypot(float64(dx), float64(dy)))
		if l == 0 {
			continue
		}
		nx, ny := -dy/l*w/2, dx/l*w/2
		// Extend each segment by half the width so the joints close.
		ex, ey := dx/l*w/2, dy/l*w/2
		if k == 1 {
			ex0, ey0 := float32(0), float32(0)
			z.MoveTo(x0+nx-ex0, y0+ny-ey0)
			z.LineTo(x1+nx+ex, y1+ny+ey)
			z.LineTo(x1-nx+ex, y1-ny+ey)
			z.LineTo(x0-nx-ex0, y0-ny-ey0)
			z.ClosePath()
			continue
		}
		z.MoveTo(x0+nx-ex, y0+ny-ey)
		z.LineTo(x1+nx+ex, y1+ny+ey)
		z.LineTo(x1-nx+ex, y1-ny+ey)
		z.LineTo(x0-nx-ex, y0-ny-ey)
		z.ClosePath()
	}
	z.Draw(img, cell, image.NewUniform(col), image.Point{})
}

// polygon fills the polygon pts (cell coordinates).
func polygon(img *image.RGBA, cell image.Rectangle, pts [][2]float32, col color.RGBA) {
	z := vector.NewRasterizer(cell.Dx(), cell.Dy())
	z.MoveTo(pts[0][0], pts[0][1])
	for _, p := range pts[1:] {
		z.LineTo(p[0], p[1])
	}
	z.ClosePath()
	z.Draw(img, cell, image.NewUniform(col), image.Point{})
}

// block draws a block element (U+2580..U+259F).
func block(img *image.RGBA, cell image.Rectangle, r rune, col color.RGBA) bool {
	x0, y0, w, h := cell.Min.X, cell.Min.Y, cell.Dx(), cell.Dy()
	rect := func(fx0, fy0, fx1, fy1 float64) {
		fill(img, image.Rect(x0+int(math.Round(fx0*float64(w))), y0+int(math.Round(fy0*float64(h))),
			x0+int(math.Round(fx1*float64(w))), y0+int(math.Round(fy1*float64(h)))), col)
	}
	// Quadrants: upper left, upper right, lower left, lower right.
	quads := map[rune][4]bool{
		'▖': {false, false, true, false}, '▗': {false, false, false, true},
		'▘': {true, false, false, false}, '▝': {false, true, false, false},
		'▙': {true, false, true, true}, '▛': {true, true, true, false},
		'▜': {true, true, false, true}, '▟': {false, true, true, true},
		'▚': {true, false, false, true}, '▞': {false, true, true, false},
	}
	if q, ok := quads[r]; ok {
		for k, on := range q {
			if on {
				fx, fy := float64(k%2)/2, float64(k/2)/2
				rect(fx, fy, fx+0.5, fy+0.5)
			}
		}
		return true
	}
	switch {
	case r == '▀':
		rect(0, 0, 1, 0.5)
	case r >= '▁' && r <= '█': // lower eighths
		n := float64(r-'▁'+1) / 8
		rect(0, 1-n, 1, 1)
	case r >= '▉' && r <= '▏': // left eighths, 7/8 down to 1/8
		n := float64(8-(r-'▉')-1) / 8
		rect(0, 0, n, 1)
	case r == '▐':
		rect(0.5, 0, 1, 1)
	case r == '▔':
		rect(0, 0, 1, 0.125)
	case r == '▕':
		rect(0.875, 0, 1, 1)
	case r >= '░' && r <= '▓':
		a := []uint8{0x40, 0x80, 0xc0}[r-'░']
		c := col
		c.A = a
		c.R, c.G, c.B = uint8(uint16(c.R)*uint16(a)/255), uint8(uint16(c.G)*uint16(a)/255), uint8(uint16(c.B)*uint16(a)/255)
		z := vector.NewRasterizer(w, h)
		z.MoveTo(0, 0)
		z.LineTo(float32(w), 0)
		z.LineTo(float32(w), float32(h))
		z.LineTo(0, float32(h))
		z.ClosePath()
		z.Draw(img, cell, image.NewUniform(c), image.Point{})
	default:
		return false
	}
	return true
}

// braille draws a braille pattern (U+2800..U+28FF) as dots.
func braille(img *image.RGBA, cell image.Rectangle, r rune, col color.RGBA) {
	bits := int(r - 0x2800)
	// Dot k (bit k) sits at column, row.
	pos := [8][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
	w, h := float32(cell.Dx()), float32(cell.Dy())
	rad := w / 7
	for k, p := range pos {
		if bits&(1<<k) == 0 {
			continue
		}
		cx := w * float32(1+2*p[0]) / 4
		cy := h * float32(1+2*p[1]) / 8
		var pts [][2]float32
		for a := 0; a < 12; a++ {
			t := float64(a) / 12 * 2 * math.Pi
			pts = append(pts, [2]float32{cx + rad*float32(math.Cos(t)), cy + rad*float32(math.Sin(t))})
		}
		polygon(img, cell, pts, col)
	}
}

// symbol draws the few symbols Go Mono lacks that the UI uses.
func symbol(img *image.RGBA, cell image.Rectangle, r rune, col color.RGBA, scale int) bool {
	w, h := float32(cell.Dx()), float32(cell.Dy())
	t := float32(scale) * 1.5
	// The glyph box: a square centred in the cell.
	s := w * 0.8
	x0, y0 := (w-s)/2, (h-s)/2
	p := func(fx, fy float32) [2]float32 { return [2]float32{x0 + fx*s, y0 + fy*s} }
	switch r {
	case '▶', '►':
		polygon(img, cell, [][2]float32{p(0.1, 0.05), p(0.95, 0.5), p(0.1, 0.95)}, col)
	case '◀', '◄':
		polygon(img, cell, [][2]float32{p(0.9, 0.05), p(0.05, 0.5), p(0.9, 0.95)}, col)
	case '✓', '✔':
		stroke(img, cell, [][2]float32{p(0.05, 0.55), p(0.38, 0.9), p(0.95, 0.1)}, t, col)
	case '✗', '✘':
		stroke(img, cell, [][2]float32{p(0.1, 0.1), p(0.9, 0.9)}, t, col)
		stroke(img, cell, [][2]float32{p(0.9, 0.1), p(0.1, 0.9)}, t, col)
	case '⇧':
		stroke(img, cell, [][2]float32{p(0.3, 0.95), p(0.3, 0.55), p(0.05, 0.55), p(0.5, 0.05), p(0.95, 0.55), p(0.7, 0.55), p(0.7, 0.95), p(0.3, 0.95)}, float32(scale), col)
	case '↳':
		stroke(img, cell, [][2]float32{p(0.2, 0.05), p(0.2, 0.7), p(0.95, 0.7)}, float32(scale), col)
		stroke(img, cell, [][2]float32{p(0.72, 0.47), p(0.95, 0.7), p(0.72, 0.93)}, float32(scale), col)
	case '⇄':
		stroke(img, cell, [][2]float32{p(0.05, 0.3), p(0.95, 0.3)}, float32(scale), col)
		stroke(img, cell, [][2]float32{p(0.75, 0.12), p(0.95, 0.3), p(0.75, 0.48)}, float32(scale), col)
		stroke(img, cell, [][2]float32{p(0.95, 0.7), p(0.05, 0.7)}, float32(scale), col)
		stroke(img, cell, [][2]float32{p(0.25, 0.52), p(0.05, 0.7), p(0.25, 0.88)}, float32(scale), col)
	default:
		return false
	}
	return true
}

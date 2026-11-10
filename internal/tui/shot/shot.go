// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

// Package shot turns a rendered `spec ui` frame into a PNG: the frame's
// ANSI text goes through a VT emulator (charmbracelet/x/vt), and each
// cell is drawn with its colours, attributes and underline style in Go
// Mono (embedded, BSD licensed). Box drawing, block elements, braille and
// the few symbols Go Mono lacks are drawn as shapes, so borders join
// without gaps. The output is deterministic: the same frame gives the
// same bytes.
package shot

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/gomonobolditalic"
	"golang.org/x/image/font/gofont/gomonoitalic"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/jwriteclub/spec/internal/tui"
)

// Palette is a terminal's default colours: what a cell without its own
// colour shows.
type Palette struct {
	Fg, Bg, Cursor color.RGBA
}

// The default palettes: a dark and a light terminal profile, near what
// common terminals ship with.
var (
	DarkPalette  = Palette{Fg: color.RGBA{0xd4, 0xd4, 0xd4, 0xff}, Bg: color.RGBA{0x1e, 0x1e, 0x1e, 0xff}, Cursor: color.RGBA{0xd4, 0xd4, 0xd4, 0xff}}
	LightPalette = Palette{Fg: color.RGBA{0x1e, 0x1e, 0x1e, 0xff}, Bg: color.RGBA{0xff, 0xff, 0xff, 0xff}, Cursor: color.RGBA{0x1e, 0x1e, 0x1e, 0xff}}
)

// Options shape the image.
type Options struct {
	// Scale multiplies every pixel size: 2 (the default) for a crisp
	// image on a high-density screen.
	Scale int
	// Palette is the terminal's default colours; the zero value picks
	// DarkPalette or LightPalette by the frame's theme.
	Palette Palette
	// NoCursor leaves the cursor out (it is drawn as a block).
	NoCursor bool
}

// Cell metrics at scale 1: Go Mono at 15 px is 9 px wide (0.6 em); the
// line is 1.2 em.
const (
	baseFontPx = 15
	baseCellW  = 9
	baseCellH  = 18
	basePad    = 12
)

// Missing collects the runes the font and the shapes lacked (drawn as a
// box), for the caller to report.
type Missing map[rune]int

// Render draws frame f as an image.
func Render(f tui.Frame, o Options) (*image.RGBA, Missing, error) {
	scale := o.Scale
	if scale <= 0 {
		scale = 2
	}
	pal := o.Palette
	if pal == (Palette{}) {
		pal = DarkPalette
		if f.Light {
			pal = LightPalette
		}
	}
	faces, err := facesAt(scale)
	if err != nil {
		return nil, nil, err
	}
	em := vt.NewEmulator(f.W, f.H)
	defer func() { _ = em.Close() }()
	for y, line := range strings.Split(f.Content, "\n") {
		if y >= f.H {
			break
		}
		_, _ = fmt.Fprintf(em, "\x1b[%d;1H%s\x1b[0m", y+1, line)
	}

	cw, ch, pad := baseCellW*scale, baseCellH*scale, basePad*scale
	img := image.NewRGBA(image.Rect(0, 0, f.W*cw+2*pad, f.H*ch+2*pad))
	draw.Draw(img, img.Bounds(), image.NewUniform(pal.Bg), image.Point{}, draw.Src)
	r := &renderer{img: img, faces: faces, cw: cw, ch: ch, scale: scale, missing: Missing{}}
	for y := 0; y < f.H; y++ {
		for x := 0; x < f.W; x++ {
			c := em.CellAt(x, y)
			if c == nil {
				continue
			}
			cursor := !o.NoCursor && f.Cursor != nil && f.Cursor[0] == x && f.Cursor[1] == y
			r.cell(pad+x*cw, pad+y*ch, *c, pal, cursor)
		}
	}
	return img, r.missing, nil
}

// Encode renders f and encodes it as PNG.
func Encode(f tui.Frame, o Options) ([]byte, Missing, error) {
	img, missing, err := Render(f, o)
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), missing, nil
}

// WriteFile renders f at the default options and writes it to path as
// PNG.
func WriteFile(path string, f tui.Frame) error {
	b, _, err := Encode(f, Options{})
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// renderer draws cells into img.
type renderer struct {
	img     *image.RGBA
	faces   *faceSet
	cw, ch  int
	scale   int
	missing Missing
}

// cell draws one cell at pixel (px, py).
func (r *renderer) cell(px, py int, c uv.Cell, pal Palette, cursor bool) {
	st := c.Style
	fg, bg := rgba(st.Fg, pal.Fg), rgba(st.Bg, pal.Bg)
	if st.Attrs&uv.AttrReverse != 0 {
		fg, bg = bg, fg
	}
	if st.Attrs&uv.AttrFaint != 0 {
		fg = blend(fg, bg, 0.5)
	}
	if cursor {
		fg, bg = bg, pal.Cursor
	}
	w := max(c.Width, 1)
	cellRect := image.Rect(px, py, px+r.cw*w, py+r.ch)
	if bg != pal.Bg || cursor {
		draw.Draw(r.img, cellRect, image.NewUniform(bg), image.Point{}, draw.Src)
	}
	if c.Content != "" && c.Content != " " && st.Attrs&uv.AttrConceal == 0 {
		r.glyph(px, py, c.Content, w, fg, st.Attrs)
	}
	if st.Underline != uv.UnderlineNone {
		r.underline(cellRect, st.Underline, rgba(st.UnderlineColor, fg))
	}
	if st.Attrs&uv.AttrStrikethrough != 0 {
		t := r.scale
		y := py + r.ch/2
		fill(r.img, image.Rect(cellRect.Min.X, y-t/2, cellRect.Max.X, y-t/2+t), fg)
	}
}

// glyph draws content (one grapheme) w cells wide at (px, py).
func (r *renderer) glyph(px, py int, content string, w int, fg color.RGBA, attrs uint8) {
	runes := []rune(content)
	ru := runes[0]
	if drawShape(r.img, image.Rect(px, py, px+r.cw*w, py+r.ch), ru, fg, r.scale) {
		return
	}
	face := r.faces.regular
	switch {
	case attrs&uv.AttrBold != 0 && attrs&uv.AttrItalic != 0:
		face = r.faces.boldItalic
	case attrs&uv.AttrBold != 0:
		face = r.faces.bold
	case attrs&uv.AttrItalic != 0:
		face = r.faces.italic
	}
	mask, off, ok := r.faces.mask(face, ru)
	if !ok {
		r.missing[ru]++
		tofu(r.img, image.Rect(px, py, px+r.cw*w, py+r.ch), fg, r.scale)
		return
	}
	at := image.Pt(px, py+r.faces.baseline).Add(off)
	draw.DrawMask(r.img, mask.Bounds().Add(at), image.NewUniform(fg), image.Point{}, mask, mask.Bounds().Min, draw.Over)
	// Combining marks after the first rune.
	for _, cm := range runes[1:] {
		if m, o, ok := r.faces.mask(face, cm); ok {
			at := image.Pt(px, py+r.faces.baseline).Add(o)
			draw.DrawMask(r.img, m.Bounds().Add(at), image.NewUniform(fg), image.Point{}, m, m.Bounds().Min, draw.Over)
		}
	}
}

// underline draws an underline of style u under rect in col.
func (r *renderer) underline(rect image.Rectangle, u uv.Underline, col color.RGBA) {
	t := r.scale
	// Every stroke stays in the cell (a curly line would reach the next
	// row's top).
	fill := func(img *image.RGBA, rr image.Rectangle, col color.RGBA) { fill(img, rr.Intersect(rect), col) }
	y := rect.Min.Y + r.faces.baseline + 2*r.scale
	switch u {
	case uv.UnderlineDouble:
		fill(r.img, image.Rect(rect.Min.X, y, rect.Max.X, y+t), col)
		fill(r.img, image.Rect(rect.Min.X, y+2*t, rect.Max.X, y+3*t), col)
	case uv.UnderlineCurly:
		amp := float64(t) * 1.5
		period := float64(r.cw)
		for x := rect.Min.X; x < rect.Max.X; x++ {
			yy := y + t + int(math.Round(amp*math.Sin(2*math.Pi*float64(x-rect.Min.X)/period)))
			fill(r.img, image.Rect(x, yy, x+1, yy+t), col)
		}
	case uv.UnderlineDotted:
		for x := rect.Min.X; x < rect.Max.X; x += 2 * t {
			fill(r.img, image.Rect(x, y, x+t, y+t), col)
		}
	case uv.UnderlineDashed:
		dash := r.cw / 3
		for x := rect.Min.X; x < rect.Max.X; x += 2 * dash {
			fill(r.img, image.Rect(x, y, min(x+dash, rect.Max.X), y+t), col)
		}
	default:
		fill(r.img, image.Rect(rect.Min.X, y, rect.Max.X, y+t), col)
	}
}

// rgba resolves c, nil being def.
func rgba(c color.Color, def color.RGBA) color.RGBA {
	if c == nil {
		return def
	}
	return color.RGBAModel.Convert(c).(color.RGBA)
}

// blend mixes a toward b by f (0: a, 1: b).
func blend(a, b color.RGBA, f float64) color.RGBA {
	mix := func(x, y uint8) uint8 { return uint8(math.Round(float64(x)*(1-f) + float64(y)*f)) }
	return color.RGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 0xff}
}

// fill paints rect in col.
func fill(img *image.RGBA, rect image.Rectangle, col color.RGBA) {
	draw.Draw(img, rect.Intersect(img.Bounds()), image.NewUniform(col), image.Point{}, draw.Src)
}

// tofu draws the box of a missing glyph.
func tofu(img *image.RGBA, cell image.Rectangle, col color.RGBA, t int) {
	in := cell.Inset(2 * t)
	fill(img, image.Rect(in.Min.X, in.Min.Y, in.Max.X, in.Min.Y+t), col)
	fill(img, image.Rect(in.Min.X, in.Max.Y-t, in.Max.X, in.Max.Y), col)
	fill(img, image.Rect(in.Min.X, in.Min.Y, in.Min.X+t, in.Max.Y), col)
	fill(img, image.Rect(in.Max.X-t, in.Min.Y, in.Max.X, in.Max.Y), col)
}

// ---- fonts ----

// faceSet is Go Mono's four faces at one scale, with a cache of glyph
// masks (a face is not safe for concurrent use, so mask locks it).
type faceSet struct {
	mu                                sync.Mutex
	regular, bold, italic, boldItalic font.Face
	baseline                          int
	cache                             map[glyphKey]glyphMask
}

type glyphKey struct {
	face font.Face
	r    rune
}

type glyphMask struct {
	mask *image.Alpha
	off  image.Point
	ok   bool
}

var (
	facesMu sync.Mutex
	facesBy = map[int]*faceSet{}
)

// facesAt returns the faces for scale, parsing the fonts once.
func facesAt(scale int) (*faceSet, error) {
	facesMu.Lock()
	defer facesMu.Unlock()
	if fs, ok := facesBy[scale]; ok {
		return fs, nil
	}
	fs := &faceSet{cache: map[glyphKey]glyphMask{}}
	for _, f := range []struct {
		ttf  []byte
		face *font.Face
	}{{gomono.TTF, &fs.regular}, {gomonobold.TTF, &fs.bold}, {gomonoitalic.TTF, &fs.italic}, {gomonobolditalic.TTF, &fs.boldItalic}} {
		parsed, err := opentype.Parse(f.ttf)
		if err != nil {
			return nil, err
		}
		face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: float64(baseFontPx * scale), DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return nil, err
		}
		*f.face = face
	}
	m := fs.regular.Metrics()
	ascent, descent := m.Ascent.Ceil(), m.Descent.Ceil()
	fs.baseline = (baseCellH*scale-(ascent+descent))/2 + ascent
	facesBy[scale] = fs
	return fs, nil
}

// mask returns r's glyph in face as an alpha mask and its offset from the
// cell's baseline origin; ok is false when the face has no such glyph.
func (fs *faceSet) mask(face font.Face, r rune) (*image.Alpha, image.Point, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	k := glyphKey{face, r}
	if g, ok := fs.cache[k]; ok {
		return g.mask, g.off, g.ok
	}
	dr, m, mp, _, ok := face.Glyph(fixed.P(0, 0), r)
	g := glyphMask{ok: ok}
	if ok && m != nil && !dr.Empty() {
		cp := image.NewAlpha(image.Rect(0, 0, dr.Dx(), dr.Dy()))
		draw.Draw(cp, cp.Bounds(), m, mp, draw.Src)
		g.mask, g.off = cp, dr.Min
	} else if ok {
		g.mask = image.NewAlpha(image.Rect(0, 0, 0, 0))
	}
	fs.cache[k] = g
	return g.mask, g.off, g.ok
}

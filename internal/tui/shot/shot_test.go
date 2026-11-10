// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package shot

import (
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwriteclub/spec/internal/tui"
)

// at returns the colour of pixel (x, y) of cell (cx, cy) at scale 2.
func at(t *testing.T, f tui.Frame, cx, cy, x, y int) color.RGBA {
	t.Helper()
	img, _, err := Render(f, Options{})
	require.NoError(t, err)
	pad := basePad * 2
	return img.RGBAAt(pad+cx*baseCellW*2+x, pad+cy*baseCellH*2+y)
}

// The image is the frame in cells: each theme's background, a cell's own
// background, the underline in its colour, strikethrough, reverse video
// and the cursor block.
func TestRender(t *testing.T) {
	f := tui.Frame{W: 6, H: 2, Content: "\x1b[41mA\x1b[0m\x1b[4:3m\x1b[58;2;0;0;255mB\x1b[0m\x1b[9mC\x1b[0m\x1b[7mD\x1b[0m─┃\nx"}
	img, missing, err := Render(f, Options{})
	require.NoError(t, err)
	assert.Empty(t, missing)
	assert.Equal(t, (6*baseCellW+2*basePad)*2, img.Bounds().Dx())
	assert.Equal(t, (2*baseCellH+2*basePad)*2, img.Bounds().Dy())
	assert.Equal(t, DarkPalette.Bg, img.RGBAAt(0, 0), "the padding is the background")

	assert.Equal(t, uint8(0x80), at(t, f, 0, 0, 1, 1).R&0x80, "A has a red background")
	assert.NotEqual(t, DarkPalette.Bg, at(t, f, 0, 0, 1, 1))

	// B's curly underline is blue somewhere along the cell's bottom.
	img, _, _ = Render(f, Options{})
	pad := basePad * 2
	blue := false
	for y := baseCellH; y < 2*baseCellH; y++ {
		for x := 0; x < 2*baseCellW; x++ {
			c := img.RGBAAt(pad+2*baseCellW+x, pad+y)
			if c.B > 0xc0 && c.R < 0x40 {
				blue = true
			}
		}
	}
	assert.True(t, blue, "the underline is blue")

	// C is struck through at mid-height.
	assert.Equal(t, DarkPalette.Fg, at(t, f, 2, 0, 1, baseCellH), "the strike line")
	// D is reversed: its background is the foreground.
	assert.Equal(t, DarkPalette.Fg, at(t, f, 3, 0, 1, 1))
	// The light line crosses its cell at mid-height; the heavy one runs
	// down the middle.
	assert.Equal(t, DarkPalette.Fg, at(t, f, 4, 0, 0, baseCellH))
	assert.Equal(t, DarkPalette.Fg, at(t, f, 5, 0, baseCellW, 0))

	f.Light = true
	assert.Equal(t, LightPalette.Bg, at(t, f, 1, 1, 1, 1), "the light profile")
	cur := [2]int{1, 1}
	f.Cursor = &cur
	assert.Equal(t, LightPalette.Cursor, at(t, f, 1, 1, 1, 1), "the cursor block")
}

// The same frame gives the same bytes.
func TestEncodeDeterministic(t *testing.T) {
	f := tui.Frame{W: 20, H: 3, Content: "\x1b[1mbold\x1b[0m \x1b[3mitalic\x1b[0m ⠋ ✓ ╭─╮\n▌▐▀▄█ ⇧Tab"}
	a, missing, err := Encode(f, Options{})
	require.NoError(t, err)
	assert.Empty(t, missing)
	b, _, err := Encode(f, Options{})
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

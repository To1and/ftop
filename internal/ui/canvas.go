package ui

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"ftop/internal/theme"
)

// Cell is one terminal cell. A wide rune occupies two cells: the first carries the
// glyph, the second is a continuation marker the writer skips.
type Cell struct {
	Text  string
	FG    theme.RGB
	BG    theme.RGB
	HasBG bool
	Cont  bool
	// Dim marks a meter's unfilled half, which shares the glyph with the filled
	// half and is distinguished by colour alone in ANSI output.
	Dim bool
	// Bold is btop's box-title weight. It is the only attribute ftop uses besides
	// colour, so it is a flag rather than a general attribute set.
	Bold bool
	// Wide marks the lead cell of a two-cell glyph; the next cell is its Cont.
	Wide bool
}

// Canvas is a fixed grid of cells.
//
// Working in cells rather than concatenating strings is what makes the layout safe:
// every write clips to the grid, so a card can never push the rest of the frame
// sideways. Every layout bug in the earlier implementation came from strings whose
// width was guessed rather than measured.
type Canvas struct {
	W, H  int
	cells []Cell
	ansi  []byte // ANSI's buffer, kept between frames
	// last and lastANSI are the previous frame's bytes and the string made from
	// them: a frame identical to the one before returns the same string.
	last     []byte
	lastANSI string
}

// Reset reuses a canvas for a new frame of the same geometry.
//
// Every frame used to allocate a fresh w*h cell slice (~835 KB at 200x58) and
// fill it with one glyph each. Bubble Tea calls View on every message, not once
// per second, so that allocation ran far more often than the data changed.
func (c *Canvas) Reset(w, h int, bg theme.RGB) {
	if w != c.W || h != c.H || len(c.cells) != w*h {
		c.W, c.H = w, h
		c.cells = make([]Cell, w*h)
	}
	blank := Cell{Text: " ", BG: bg, HasBG: true}
	for i := range c.cells {
		c.cells[i] = blank
	}
}

// NewCanvas allocates a blank canvas filled with bg.
func NewCanvas(w, h int, bg theme.RGB) *Canvas {
	c := &Canvas{}
	c.Reset(w, h, bg)
	return c
}

func (c *Canvas) at(x, y int) *Cell {
	if x < 0 || y < 0 || x >= c.W || y >= c.H {
		return nil
	}
	return &c.cells[y*c.W+x]
}

// Put writes ONE glyph (one grapheme cluster), honouring double-width glyphs.
//
// Writing a multi-glyph string here used to drop everything after the first
// rune, which is how the app's own name rendered as "d" on the top line. Use
// Text for strings; this stays strict so the mistake cannot recur.
func (c *Canvas) Put(x, y int, s string, fg theme.RGB) {
	if c.at(x, y) == nil || s == "" {
		return
	}
	var g theme.Glyph
	n := 0
	theme.EachGlyph(s, func(gl theme.Glyph) bool {
		g = gl
		n++
		return n < 2
	})
	switch {
	case n > 1:
		panic(fmt.Sprintf("canvas.Put takes one glyph, got %q at (%d,%d); use Text", s, x, y))
	case n == 0:
		g = theme.Glyph{Text: " ", W: 1} // a zero-width cluster has no cell to fill
	}
	c.put(x, y, g, fg)
}

// clearFootprint empties a cell's share of a wide glyph before it is
// overwritten: the glyph's other half would otherwise be left as half a glyph.
func (c *Canvas) clearFootprint(x, y int) {
	cell := c.at(x, y)
	if cell == nil {
		return
	}
	if cell.Cont {
		if p := c.at(x-1, y); p != nil && p.Wide {
			p.Text, p.Wide = " ", false
		}
		cell.Cont, cell.Text = false, " "
	} else if cell.Wide {
		if n := c.at(x+1, y); n != nil && n.Cont {
			n.Cont, n.Text = false, " "
		}
		cell.Wide, cell.Text = false, " "
	}
}

// put writes a measured, sanitised glyph (see theme.EachGlyph).
func (c *Canvas) put(x, y int, g theme.Glyph, fg theme.RGB) {
	cell := c.at(x, y)
	if cell == nil {
		return
	}
	// Clear the footprint of whatever used to occupy this cell. Overwriting the
	// lead of a wide glyph with a narrow one used to leave its continuation marker
	// behind: Plain() then emits one fewer character than the row is wide, and
	// every column to the right shifts left by one.
	if cell.Cont {
		if p := c.at(x-1, y); p != nil && p.Wide {
			p.Text, p.Wide = " ", false
		}
	} else if cell.Wide {
		if n := c.at(x+1, y); n != nil && n.Cont {
			n.Cont, n.Text = false, " "
		}
	}
	cell.Text = g.Text
	cell.FG = fg
	cell.Cont = false
	cell.Bold = false
	cell.Dim = false
	cell.Wide = g.W == 2
	if cell.Wide {
		if n := c.at(x+1, y); n != nil {
			if n.Wide { // the glyph being covered was wide too: free its tail
				if nn := c.at(x+2, y); nn != nil && nn.Cont {
					nn.Cont, nn.Text = false, " "
				}
			}
			n.Text, n.Cont, n.Wide = "", true, false
			n.FG = fg
		} else {
			// A wide glyph in the final column cannot be represented; rendering it
			// would push the row one cell past the canvas.
			cell.Text, cell.Wide = " ", false
		}
	}
}

// PutCell writes a themed Cell, keeping its Dim marker.
func (c *Canvas) PutCell(x, y int, cl theme.Cell) {
	c.Put(x, y, cl.Text, cl.FG)
	if cell := c.at(x, y); cell != nil {
		cell.Dim = cl.Dim
	}
}

// Text writes a string left to right, clipping at the right edge. Returns the x
// just past the last glyph.
func (c *Canvas) Text(x, y int, s string, fg theme.RGB) int {
	cx := x
	theme.EachGlyph(s, func(g theme.Glyph) bool {
		if cx >= c.W {
			return false
		}
		c.put(cx, y, g, fg)
		cx += g.W
		return true
	})
	return cx
}

// TextBold is Text in bold, for box titles.
func (c *Canvas) TextBold(x, y int, s string, fg theme.RGB) int {
	end := c.Text(x, y, s, fg)
	for i := x; i < end; i++ {
		if cell := c.at(i, y); cell != nil {
			cell.Bold = true
		}
	}
	return end
}

// TextRight writes a string so that it ENDS at column x (exclusive).
//
// A string wider than the space to its left must be clipped, not pushed to column
// zero: Put() stops at the grid edge, so an over-long value silently overwrites
// whatever is at the start of the line.
func (c *Canvas) TextRight(x, y int, s string, fg theme.RGB) {
	start := x - theme.RuneWidth(s)
	if start < 0 {
		s = theme.Clip(s, x)
		start = 0
	}
	c.Text(start, y, s, fg)
}

// Fill paints a run of cells with a background.
func (c *Canvas) Fill(x, y, w int, bg theme.RGB) {
	for i := 0; i < w; i++ {
		if cell := c.at(x+i, y); cell != nil {
			cell.BG = bg
			cell.HasBG = true
		}
	}
}

// FillRect paints a rectangular region.
func (c *Canvas) FillRect(x, y, w, h int, bg theme.RGB) {
	for j := 0; j < h; j++ {
		c.Fill(x, y+j, w, bg)
	}
}

// Box draws a rounded frame, btop's createBox without its title notches: titles
// are tabs (see titledBox).
func (c *Canvas) Box(x, y, w, h int, col theme.RGB) {
	if w < 4 || h < 2 {
		return
	}
	lastX, lastY := x+w-1, y+h-1
	for i := x + 1; i < lastX; i++ {
		c.Put(i, y, "─", col)
		c.Put(i, lastY, "─", col)
	}
	for j := y + 1; j < lastY; j++ {
		c.Put(x, j, "│", col)
		c.Put(lastX, j, "│", col)
	}
	c.Put(x, y, "╭", col)
	c.Put(lastX, y, "╮", col)
	c.Put(x, lastY, "╰", col)
	c.Put(lastX, lastY, "╯", col)
}

// BoxFooter writes text right-aligned into a box's bottom border, as a plain
// label on the line: ── 8s ago ──╯. It is the place for secondary state such as
// a sample's age. Returns the column the label starts at.
func (c *Canvas) BoxFooter(x, y, w, h int, fg theme.RGB, text string) int {
	if w < 8 || h < 2 || text == "" {
		return x + w
	}
	txt := theme.Clip(text, w-8)
	start := x + w - 4 - theme.RuneWidth(txt)
	c.lineLabel(start, y+h-1, txt, fg)
	return start
}

// ANSI renders the canvas as a single string with cell-exact colours.
//
// Runs of identical styling are emitted as one SGR sequence: a 200x58 frame at one
// escape per cell would be ~3.6 MB of output per repaint, while grouping by run
// brings it to ~40 KB. Bubble Tea diffs per cell, so the smaller string is also
// faster to diff.
func (c *Canvas) ANSI() string {
	// The bytes are built in a buffer the canvas keeps from frame to frame, with
	// the numbers appended directly: through a strings.Builder and fmt this was
	// half a megabyte of garbage and a tenth of the CPU at the frame rate, for a
	// string the same size every time.
	b := c.ansi[:0]

	var curFG, curBG theme.RGB
	var curBold, haveStyle bool

	for y := 0; y < c.H; y++ {
		if y > 0 {
			b = append(b, '\n')
		}
		x := 0
		for x < c.W {
			cell := &c.cells[y*c.W+x]
			if cell.Cont {
				x++
				continue
			}
			// Consume the run of identically-styled cells.
			runStart := x
			for x < c.W {
				cl := &c.cells[y*c.W+x]
				if cl.Cont {
					x++
					continue
				}
				if cl.FG != cell.FG || cl.BG != cell.BG || cl.HasBG != cell.HasBG || cl.Bold != cell.Bold {
					break
				}
				x++
			}
			if haveStyle && cell.Bold != curBold {
				if cell.Bold {
					b = append(b, "\x1b[1m"...)
				} else {
					b = append(b, "\x1b[22m"...)
				}
				curBold = cell.Bold
			}
			if !haveStyle || cell.FG != curFG || cell.BG != curBG {
				if !haveStyle && cell.Bold {
					b = append(b, "\x1b[1m"...)
					curBold = true
				}
				// Only what changed: a run that differs from the last in one
				// colour sets that colour alone. The terminal keeps the other.
				if !haveStyle || cell.FG != curFG {
					b = appendSGR(b, "\x1b[38;2;", cell.FG)
				}
				if !haveStyle || cell.BG != curBG {
					b = appendSGR(b, "\x1b[48;2;", cell.BG)
				}
				curFG, curBG, haveStyle = cell.FG, cell.BG, true
			}
			for i := runStart; i < x; i++ {
				cl := &c.cells[y*c.W+i]
				if cl.Cont {
					continue
				}
				if cl.Text == "" {
					b = append(b, ' ')
				} else {
					b = append(b, cl.Text...)
				}
			}
		}
	}
	b = append(b, "\x1b[0m"...)
	// A frame like the last one is the last one's string, not a copy of it. At
	// the frame rate most frames are unchanged between a spinner's steps, and
	// the renderer recognises the very same string as no change without reading
	// it, where a fresh copy would be compared byte by byte, or parsed.
	if c.lastANSI != "" && bytes.Equal(b, c.last) {
		c.ansi = b
		return c.lastANSI
	}
	c.lastANSI = string(b)
	c.ansi, c.last = c.last, b
	return c.lastANSI
}

// appendSGR appends one truecolour escape: lead, then "r;g;bm".
func appendSGR(b []byte, lead string, c theme.RGB) []byte {
	b = append(b, lead...)
	b = strconv.AppendInt(b, int64(c.R), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(c.G), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(c.B), 10)
	return append(b, 'm')
}

// Blit copies a rectangular region of src into dst at (dx,dy).
//
// Continuation cells are copied with their lead rune, so a wide glyph straddling
// the left edge of the region is clipped rather than half-copied into a broken
// column.
func (c *Canvas) Blit(src *Canvas, sx, sy, dx, dy, w, h int) {
	for j := 0; j < h; j++ {
		srow := sy + j
		if srow < 0 || srow >= src.H {
			continue
		}
		for i := 0; i < w; i++ {
			scol := sx + i
			if scol < 0 || scol >= src.W {
				continue
			}
			s := &src.cells[srow*src.W+scol]
			if s.Cont {
				continue
			}
			d := c.at(dx+i, dy+j)
			if d == nil {
				continue
			}
			c.clearFootprint(dx+i, dy+j)
			if s.Wide && i+1 < w {
				c.clearFootprint(dx+i+1, dy+j) // the half it is about to cover
			}
			*d = *s
			// Re-mark the continuation cell from the destination's own geometry; a
			// wide glyph at the region edge must not leave a stale marker behind.
			if s.Wide {
				if n := c.at(dx+i+1, dy+j); n != nil && i+1 < w {
					n.Text = ""
					n.Cont = true
					n.Wide = false
					n.FG = s.FG
					n.BG = s.BG
					n.HasBG = s.HasBG
				} else {
					d.Text, d.Wide = " ", false
				}
			}
		}
	}
}

// Plain renders without colour, for tests and for non-TTY output.
func (c *Canvas) Plain() string {
	var b strings.Builder
	for y := 0; y < c.H; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		for x := 0; x < c.W; x++ {
			cell := &c.cells[y*c.W+x]
			if cell.Cont {
				continue
			}
			// Dim cells are the unfilled half of a meter. With no colour to carry
			// that meaning, substitute the light-shade glyph so a plain-text frame
			// still shows the proportion instead of a solid bar.
			switch {
			case cell.Text == "":
				b.WriteByte(' ')
			case cell.Dim:
				b.WriteString("░")
			default:
				b.WriteString(cell.Text)
			}
		}
	}
	return b.String()
}

package ui

import "ftop/internal/theme"

// Box titles as labels.
//
// btop writes a title into the border inside a notch, ┐title┌, whose strokes
// point down into the box. Here a title sits on the border between two upright
// strokes that the border runs into, and takes no row besides the border's own:
//
//	╭─┤● node-1├──────────┤4× RTX 4090├─╮
//	│ 0   ■■■■■■■■ 42%   ■■■■■■ 68%     │
//
// A label closed above and below as well needed rows of its own, and a frame
// that closes around the text both tightly and with rounded corners has no
// glyphs: the rounded corners ╭╮╰╯ join lines through the middle of a cell, and
// lines against the text run on cell edges (▁▔), which have no rounded corner.
// Labels in a bottom border are plain text between runs of the line, with no
// strokes leaving it: ──── util ────.

// seg is a run of title text in one style.
type seg struct {
	text string
	fg   theme.RGB
	bold bool
}

func segsWidth(ss []seg) int {
	w := 0
	for _, s := range ss {
		w += theme.RuneWidth(s.text)
	}
	return w
}

// clipSegs cuts ss to at most w cells, shortening the last runs first.
func clipSegs(ss []seg, w int) []seg {
	out := make([]seg, 0, len(ss))
	used := 0
	for _, s := range ss {
		sw := theme.RuneWidth(s.text)
		if used+sw <= w {
			out = append(out, s)
			used += sw
			continue
		}
		if room := w - used; room >= 2 {
			s.text = theme.Clip(s.text, room)
			out = append(out, s)
		}
		break
	}
	return out
}

// tabWidth is the cells a label around ss takes: its text and a stroke each
// side.
func tabWidth(ss []seg) int { return segsWidth(ss) + 2 }

// labelInset is how far into a box's top border its labels start: a rule cell
// stays between each corner and its label.
const labelInset = 2

// tab draws a title label whose left stroke is column x, on a box's top border
// at row y.
func (c *Canvas) tab(x, y int, ss []seg, col theme.RGB) {
	w := tabWidth(ss)
	c.Put(x, y, "┤", col)
	c.Put(x+w-1, y, "├", col)
	cx := x + 1
	for _, s := range ss {
		if s.bold {
			cx = c.TextBold(cx, y, s.text, s.fg)
		} else {
			cx = c.Text(cx, y, s.text, s.fg)
		}
	}
}

// titledBox draws a rounded box at (x, y, w, h) with a title label at its left
// and an optional one at its right, both on its top border. The right label is
// shortened, then dropped, before the left one is.
func (c *Canvas) titledBox(x, y, w, h int, col theme.RGB, left, right []seg) {
	c.Box(x, y, w, h, col)
	room := w - 2*labelInset // the rule between each corner and its label
	lw := tabWidth(left)
	if lw > room {
		left = clipSegs(left, room-2)
		lw = tabWidth(left)
	}
	if len(left) > 0 {
		c.tab(x+labelInset, y, left, col)
	}
	if len(right) == 0 {
		return
	}
	// One rule cell must stay between the two tabs.
	if avail := room - lw - 1; tabWidth(right) > avail {
		right = clipSegs(right, avail-2)
	}
	if len(right) > 0 {
		c.tab(x+w-labelInset-tabWidth(right), y, right, col)
	}
}

// lineLabel writes txt into a horizontal border at row y starting at column x,
// with a space on each side, and returns the column after the closing space.
func (c *Canvas) lineLabel(x, y int, txt string, fg theme.RGB) int {
	c.Put(x-1, y, " ", fg)
	end := c.Text(x, y, txt, fg)
	c.Put(end, y, " ", fg)
	return end + 1
}

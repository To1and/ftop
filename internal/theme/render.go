// Package btop render primitives, ported so they match btop's, except that
// multi-row graphs shade by each cell's highest dot (see Graph).
package theme

import (
	"math"
)

// BrailleUp is btop's graph_symbols["braille_up"], indexed result[0]*5+result[1].
//
// Each glyph packs two columns x four rows of dots: the left column is the previous
// sample, the right column the current one. btop reaches 4 sub-rows per cell, so a
// 2-row graph has 8 levels of vertical resolution.
var BrailleUp = [...]string{
	" ", "⢀", "⢠", "⢰", "⢸",
	"⡀", "⣀", "⣠", "⣰", "⣸",
	"⡄", "⣄", "⣤", "⣴", "⣼",
	"⡆", "⣆", "⣦", "⣶", "⣾",
	"⡇", "⣇", "⣧", "⣷", "⣿",
}

// Meter is one btop meter: a filled span whose colour is indexed by CELL POSITION.
//
// Meter(5, 20, "cpu") paints 5 filled cells using gradient entries 5,10,15,20,25 —
// not entry 5 four times. That is the whole trick, and it is why a btop meter looks
// like a compressed rainbow rather than a flat bar.
type Meter struct {
	Width    int
	Gradient string
	Invert   bool
	// Scale draws the meter in one metric's own ramp instead of a theme
	// gradient: each filled cell takes the colour for the reading it stands for,
	// so the bar's hue names the measurement and its length says how much.
	Scale *RGBScale
}

// Render returns one run of (colour, glyph) pairs for the given value 0..100.
func (m Meter) Render(t *Theme, value int) []Cell {
	return m.Append(nil, t, value)
}

// Append renders into buf, which lets a caller reuse one buffer per row instead of
// allocating a slice for every meter on every frame.
func (m Meter) Append(buf []Cell, t *Theme, value int) []Cell {
	if m.Width < 1 {
		return buf[:0]
	}
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	out := buf[:0]
	for i := 1; i <= m.Width; i++ {
		// btop: y = round(i * 100 / width); fill while value >= y.
		y := int(float64(i)*100.0/float64(m.Width) + 0.5)
		if value >= y {
			idx := y
			if m.Invert {
				idx = 100 - y
			}
			if m.Scale != nil {
				out = append(out, Cell{Text: "■", FG: m.Scale.At(float64(idx))})
				continue
			}
			// btop uses "■" for BOTH halves; only the colour differs. Using "█"
			// here made the bar a solid slab instead of btop's row of small blocks.
			out = append(out, Cell{Text: "■", FG: t.G(m.Gradient, idx)})
		} else {
			// btop draws the remainder with the SAME "■" glyph, only coloured with
			// meter_bg. A different glyph is what made the bar read as a chunky
			// two-tone strip instead of btop's row of small blocks. The Dim flag
			// carries "this is the empty part" so plain-text output (which has no
			// colour) can still show the proportion.
			//
			// The track is mixed toward the page: at btop's meter_bg a row of empty
			// cells beside a short bar was the brightest thing in a quiet card.
			track := t.Track()
			for j := 0; j < m.Width+1-i; j++ {
				out = append(out, Cell{Text: "■", FG: track, Dim: true})
			}
			break
		}
	}
	return out
}

// Cell is one glyph plus its foreground colour.
type Cell struct {
	Text string
	FG   RGB
	// Dim marks the unfilled part of a meter. It exists so a colourless renderer
	// can still distinguish full from empty without changing the glyph, which is
	// what btop uses for both halves.
	Dim bool
}

// Graph renders a btop braille history graph.
//
// c.data holds newest-last values in 0..100. btop's algorithm packs two samples per
// cell column (previous and current). The surface of a multi-row graph shades with
// height rather than with value, but finer than btop's: btop gives each ROW of
// cells one gradient entry, so a three-row graph had three colours. A cell has
// one colour, so the four dot rows of a braille glyph cannot differ; each cell
// is coloured by the height of its highest dot instead, which gives a graph four
// colour levels per row, all along its slopes.
func Graph(t *Theme, data []float64, width, height int, gradient string, opts GraphOpts) [][]Cell {
	if width < 1 {
		return nil
	}
	if height < 1 {
		height = 1
	}
	// Each braille cell packs TWO samples: the left sub-column and the right.
	// btop therefore consumes 2*width samples for a width-wide graph, and using
	// only `width` wastes half the horizontal resolution — a 200-column card was
	// showing 100 seconds of history where it could show 100 doubled, at half the
	// apparent density.
	//
	// Missing samples are NaN, not 0. btop leaves the cells before the first sample
	// blank; padding with zeros instead drew a stream that is still starting up as
	// a host that has been idle for a minute, and with NoZero as a baseline.
	series := make([]float64, width*2)
	for i := range series {
		series[i] = math.NaN()
	}
	if len(data) > len(series) {
		copy(series, data[len(data)-len(series):])
	} else {
		copy(series[len(series)-len(data):], data)
	}

	// glyphs[row][col], accumulated then emitted, and the height of each cell's
	// highest dot (0..4).
	glyphs := make([][]string, height)
	tops := make([][]int, height)
	for r := range glyphs {
		glyphs[r] = make([]string, width)
		tops[r] = make([]int, width)
	}

	// btop biases the sub-row rounding upward, more for a single-row graph, so a
	// short spike still lifts the glyph off the floor (Graph::_create `mod`).
	mod := 0.1
	if height == 1 {
		mod = 0.3
	}
	for x := 0; x < width; x++ {
		left, right := series[2*x], series[2*x+1]
		for horizon := 0; horizon < height; horizon++ {
			var curHigh, curLow int
			if height > 1 {
				curHigh = int(float64(100.0*float64(height-horizon)/float64(height)) + 0.5)
				curLow = int(float64(100.0*float64(height-(horizon+1))/float64(height)) + 0.5)
			} else {
				curHigh, curLow = 100, 0
			}
			clampMin := 0
			if opts.NoZero && horizon == height-1 {
				clampMin = 1
			}
			result := [2]int{
				level(left, curHigh, curLow, clampMin, mod),
				level(right, curHigh, curLow, clampMin, mod),
			}
			glyphs[horizon][x] = BrailleUp[result[0]*5+result[1]]
			tops[horizon][x] = max(result[0], result[1])
		}
	}

	out := make([][]Cell, height)
	for r := 0; r < height; r++ {
		row := make([]Cell, width)
		for c := 0; c < width; c++ {
			switch {
			case glyphs[r][c] == " ":
				row[c] = Cell{Text: " ", FG: t.C("main_bg")}
				continue
			case gradient == "" && opts.Scale == nil:
				row[c] = Cell{Text: glyphs[r][c], FG: t.C("main_fg")}
				continue
			}
			// Colour says how HIGH the cell is, in dot rows from the graph's
			// floor, across the whole graph. Two things follow, and both are
			// what makes a graph read as one surface:
			//
			//   - the peak of a busy graph is at the ramp's hot end and an idle
			//     floor is at its cold end, so the shape of the load is the
			//     shape of the colour;
			//   - two cells at the same height in different columns never share a
			//     tone because one column was busier, which is what a
			//     reading-tinted graph did.
			//
			// A cell has one colour, so this is the finest a braille graph can
			// shade: four dot rows per cell cannot differ from each other. One
			// row of cells is therefore at most four colours, and that is a
			// property of the character grid, not of this rule.
			dots := 4*(height-1-r) + tops[r][c]
			full := 4 * height
			if opts.NoZero {
				full = 4*height - 1
				dots--
			}
			idx := 0.0
			if full > 0 {
				idx = float64(dots) * 100 / float64(full)
			}
			idx = min(max(idx, 0), 100)
			if opts.Invert {
				idx = 100 - idx
			}
			if opts.Scale != nil {
				row[c] = Cell{Text: glyphs[r][c], FG: opts.Scale.At(idx)}
			} else {
				row[c] = Cell{Text: glyphs[r][c], FG: t.G(gradient, int(idx+0.5))}
			}
		}
		out[r] = row
	}
	return out
}

// mix blends two colours, a*(1-t) + b*t.
func mix(a, b RGB, t float64) RGB {
	return RGB{
		R: int(float64(a.R) + float64(b.R-a.R)*t + 0.5),
		G: int(float64(a.G) + float64(b.G-a.G)*t + 0.5),
		B: int(float64(a.B) + float64(b.B-a.B)*t + 0.5),
	}
}

// maxKnown is the larger of two samples, ignoring a missing one.
func maxKnown(a, b float64) float64 {
	switch {
	case math.IsNaN(a):
		return b
	case math.IsNaN(b):
		return a
	}
	return max(a, b)
}

// GraphOpts mirrors btop's Graph flags.
type GraphOpts struct {
	NoZero bool // never draw a blank baseline: an idle host still shows a floor
	Invert bool
	// Scale colours the graph by its metric's own ramp instead of a theme
	// gradient: the hue names the measurement, the step says its height. A
	// braille cell holds four dot rows and can take one colour, so a two-row
	// graph is coloured by the height of each column's highest dot across the
	// WHOLE graph, not per row: its body shades like a filled area rather than
	// repeating one flat tone.
	Scale *RGBScale
}

// level ports the per-sample sub-row quantisation inside btop's Graph::_create.
//
// btop rounds to the nearest sub-row after adding `mod`, not toward the floor.
// Flooring loses the low end entirely: a host hovering between 10% and 20% draws
// the same glyph as an idle one, which is exactly the reading a monitor exists to
// show.
func level(v float64, curHigh, curLow int, clampMin int, mod float64) int {
	switch {
	case math.IsNaN(v):
		return 0 // no sample: blank, even under NoZero
	case v >= float64(curHigh):
		return 4
	case v <= float64(curLow):
		return clampMin
	default:
		n := int(math.Round((v-float64(curLow))*4/float64(curHigh-curLow) + mod))
		if n < clampMin {
			return clampMin
		}
		if n > 4 {
			return 4
		}
		return n
	}
}

// Sparkline is the one thing btop does not have: a single-row Unicode
// eighth-block trace, for rows too short to spend two lines on a graph.
var spark = [...]string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

// RuneWidth counts terminal cells, treating CJK as two columns.
//
// This matters more than it looks: labels here are Chinese, and every layout bug in
// the earlier Python version traced back to counting runes where cells were meant.

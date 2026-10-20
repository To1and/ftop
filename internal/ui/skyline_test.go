package ui

import (
	"fmt"
	"strings"
	"testing"

	"ftop/internal/wire"
)

// skyFleet is a board whose cards carry the given utilisation and VRAM fill,
// one host per entry, every card of a host alike.
func skyFleet(width int, hosts []skyHost) *App {
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.name
	}
	a := New(Options{Hosts: names, GraphRows: 1})
	a.width, a.height = width, 50
	for _, h := range hosts {
		s := wire.Sample{}
		s.Cores = 64
		for j := 0; j < h.gpus; j++ {
			u, total := h.util, uint64(24564)
			used := uint64(h.vram * float64(total))
			s.Gpus = append(s.Gpus, wire.Gpu{
				Index: j, UUID: fmt.Sprintf("%s-%d", h.name, j), Name: "NVIDIA GeForce RTX 4090",
				Util: &u, MemUsed: &used, MemTotal: &total,
			})
		}
		a.Deliver(sshxSample(h.name, s))
	}
	return a
}

type skyHost struct {
	name       string
	gpus       int
	util, vram float64
}

// skyAxis is the chart's name row: the one carrying the "gpu" noun.
func skyAxis(t *testing.T, c *Canvas, top int) int {
	t.Helper()
	for y := 1; y < top-1; y++ {
		var b strings.Builder
		for x := 0; x < c.W/2; x++ {
			b.WriteString(c.at(x, y).Text)
		}
		if strings.Contains(b.String(), " gpu ") {
			return y
		}
	}
	t.Fatal("no axis row in the header")
	return -1
}

// A fleet shaped like a real one: two large hosts, two mid-sized, a run of
// single-card machines, and names long enough to be wider than their bars.
var skyMixed = []skyHost{
	{"lab-8a", 8, 0, 0.1}, {"lab-8b", 8, 0, 0.9}, {"lab-4a", 4, 0, 0.7}, {"lab-4b", 4, 0, 0.2},
	{"lab-1a", 1, 0, 0.15}, {"lab-1b", 1, 0, 0.15}, {"lab-1c", 1, 0, 0.15},
	{"lab-deploy", 1, 0, 0.95}, {"lab-dev", 1, 0, 0.12},
}

// TestSkylineAxisRunsThroughTheNames: every host's name sits on its own stretch
// of axis, ─ name ─, with the rule visible past the space on both sides.
//
// The rule used to be as wide as the bars or the name, whichever was wider, and
// the name and its two spaces were drawn over it. A host whose name was wider
// than its bars — every single-card machine, and every host once the chart
// tightened its bars to one cell — covered its whole rule, so the names floated
// on no line at all, and the idle floors in the row above, run together, were
// the only line left to read as the axis.
func TestSkylineAxisRunsThroughTheNames(t *testing.T) {
	for _, w := range []int{190, 200, 240} {
		a := skyFleet(w, skyMixed)
		c := a.fleetCanvas()
		axis := skyAxis(t, c, a.boardTop)
		row := ""
		for x := 0; x < c.W; x++ {
			row += c.at(x, axis).Text
		}
		cells := []rune(row)
		for _, h := range skyMixed {
			i := strings.Index(row, " "+h.name+" ")
			if i < 0 {
				t.Errorf("width %d: %s is not on the axis: %q", w, h.name, row)
				continue
			}
			x := len([]rune(row[:i])) // the space before the name
			end := x + 1 + len(h.name)
			if cells[x-1] != '─' || cells[end+1] != '─' {
				t.Errorf("width %d: %s has no rule on both sides: %q", w, h.name, string(cells[max(x-3, 0):min(end+4, len(cells))]))
			}
		}
	}
}

// TestSkylineHasOneLine: the axis is the only line in the chart. An idle card
// draws nothing above its name.
//
// Idle cards used to keep a one-eighth floor in the track colour. On a host
// whose bars are one cell wide the floors ran together into a rule of their
// own, one row above the names, and that was the line a reader saw as the
// axis — while the real one, on the name row, was hidden under the names.
func TestSkylineHasOneLine(t *testing.T) {
	for _, w := range []int{160, 200, 240} {
		a := skyFleet(w, skyMixed)
		c := a.fleetCanvas()
		axis := skyAxis(t, c, a.boardTop)
		for x := 0; x < c.W/2; x++ {
			if !inSkyHost(a, x, axis) {
				continue
			}
			if txt := c.at(x, axis-1).Text; txt != " " && txt != "" {
				t.Errorf("width %d: an idle fleet draws %q above the names at column %d", w, txt, x)
				break
			}
		}
	}
}

// TestSkylineBarsStartAtTheNameRow: a utilisation bar stands on the name row's
// top edge and a VRAM bar hangs from its bottom edge, at every fill.
//
// A VRAM cell exactly half full was drawn as the upper-half glyph in the page
// colour over a VRAM background: the bottom half was coloured and the top was
// page, so the bar began half a row below the names. Every card under an eighth
// of its memory is lifted to that half-cell floor, so on a quiet fleet nearly
// every bar was detached.
func TestSkylineBarsStartAtTheNameRow(t *testing.T) {
	lower := map[string]bool{"▁": true, "▂": true, "▃": true, "▄": true, "▅": true, "▆": true, "▇": true}
	for _, util := range []float64{4, 12, 30, 50, 100} {
		for _, vram := range []float64{0.005, 0.1, 0.17, 0.5, 1} {
			a := skyFleet(200, []skyHost{{"lab-a", 2, util, vram}})
			c := a.fleetCanvas()
			axis := skyAxis(t, c, a.boardTop)
			found := false
			for x := 0; x < c.W/2; x++ {
				if !inSkyHost(a, x, axis) {
					continue
				}
				up, down := c.at(x, axis-1), c.at(x, axis+1)
				if down.Text == " " && up.Text == " " {
					continue
				}
				found = true
				// The cell above the names is coloured at its bottom edge.
				switch {
				case lower[up.Text] && up.FG == skyUtil:
				case up.Text == "▄" && up.FG == skyUtil && up.HasBG && up.BG == skyUtil:
				default:
					t.Errorf("util %.0f%%: the bar does not stand on the names at column %d: %q fg %s", util, x, up.Text, up.FG.Hex())
				}
				// The cell below them is coloured at its top edge.
				switch {
				case down.Text == "▀" && down.FG == skyVRAM:
				case lower[down.Text] && down.FG == a.skin.Bg && down.HasBG && down.BG == skyVRAM:
				default:
					t.Errorf("vram %.1f%%: the bar does not hang from the names at column %d: %q fg %s bg %s",
						vram*100, x, down.Text, down.FG.Hex(), down.BG.Hex())
				}
			}
			if !found {
				t.Errorf("util %.0f%% vram %.1f%%: no bars drawn", util, vram*100)
			}
		}
	}
}

// inSkyHost reports whether column x of row y is inside a host's stretch of the
// chart, as opposed to the key column or the margins.
func inSkyHost(a *App, x, y int) bool {
	for _, h := range a.hits {
		if h.host != "" && h.y == y && x >= h.x && x < h.x+h.w {
			return true
		}
	}
	return false
}

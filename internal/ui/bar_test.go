package ui

import (
	"testing"

	"ftop/internal/theme"
)

// TestEveryBarIsAGradient: a filled bar walk from the ramp's cold end to the
// reading's own colour, so its cells differ from each other at any load.
//
// The two failure modes this rules out are the ones the palette was rebuilt for:
// indexing by value made a short bar one flat colour, and indexing by position
// (btop) painted a quiet bar in the alarm colours it had not reached.
func TestEveryBarIsAGradient(t *testing.T) {
	s := theme.NewSkin()
	// Every meter on screen, not just util: swap was added to the board after
	// this test was written, and a scale with no ramp behind it renders black
	// rather than failing.
	scales := map[string]theme.RGBScale{
		"util": s.Scale.CPU, "vram": s.Scale.VRAM, "power": s.Scale.Power,
		"host": s.Scale.HostCPU, "mem": s.Scale.Mem, "swap": s.Scale.Swap,
	}
	for name, util := range scales {
		t.Run(name, func(t *testing.T) { checkBarGradient(t, s, util) })
	}
}

func checkBarGradient(t *testing.T, s *theme.Skin, util theme.RGBScale) {
	for _, width := range []int{5, 10, 14, 20} {
		for _, value := range []int{25, 50, 80, 100} {
			cells := meterScale(width, value, util, s.Theme)
			filled := 0
			seen := map[theme.RGB]bool{}
			for _, c := range cells {
				if c.Dim {
					continue
				}
				filled++
				seen[c.FG] = true
			}
			if filled < 3 {
				continue // nothing to interpolate across
			}
			if len(seen) < 2 {
				t.Errorf("width %d, value %d: the bar is one colour %s", width, value, cells[0].FG.Hex())
			}
			// The last filled cell is the reading's own colour.
			last := cells[filled-1].FG
			if want := util.At(float64(value)); last != want {
				t.Errorf("width %d, value %d: the bar ends %s, want the reading's %s",
					width, value, last.Hex(), want.Hex())
			}
			// The first is the ramp's cold end.
			if first := cells[0].FG; first != util.At(0) {
				t.Errorf("width %d, value %d: the bar starts %s, want the cold end %s",
					width, value, first.Hex(), util.At(0).Hex())
			}
		}
	}
}

// TestALongerBarIsNeverDarker: the fill grows with the value, and so does its
// brightest cell. A bar that got dimmer as it filled would contradict the
// lightness rule the palette is built on.
func TestALongerBarIsNeverDarker(t *testing.T) {
	s := theme.NewSkin()
	prevBright, prevFilled := -1.0, -1
	for v := 0; v <= 100; v += 5 {
		cells := meterScale(14, v, s.Scale.VRAM, s.Theme)
		filled, brightest := 0, -1.0
		for _, c := range cells {
			if c.Dim {
				continue
			}
			filled++
			if L, _, _ := theme.OKLCHOf(c.FG); L > brightest {
				brightest = L
			}
		}
		if filled < prevFilled {
			t.Fatalf("%d%% filled %d cells, after %d", v, filled, prevFilled)
		}
		if brightest < prevBright-0.01 {
			t.Fatalf("%d%% is dimmer (%.2f) than the bar below it (%.2f)", v, brightest, prevBright)
		}
		prevFilled, prevBright = filled, brightest
	}
}

// TestNoStaleRampNameReachesTheScreen: a colour name the palette does not know
// comes back magenta. That sentinel exists so a renamed ramp fails loudly instead
// of silently painting a reading in the wrong hue — which is what happened when
// "cached" and "used" were left behind after the palette was replaced, and power
// read in pink.
func TestNoStaleRampNameReachesTheScreen(t *testing.T) {
	a := fixture(200, 50, []int{4, 4})
	check := func(name string, c *Canvas) {
		for y := 0; y < c.H; y++ {
			for x := 0; x < c.W; x++ {
				if cell := c.at(x, y); cell != nil && cell.FG == theme.UnknownRamp {
					t.Fatalf("%s: unknown colour at (%d,%d), glyph %q", name, x, y, cell.Text)
				}
			}
		}
	}
	check("board", a.fleetCanvas())
	a.open("hosta")
	check("host", a.detailCanvas())
}

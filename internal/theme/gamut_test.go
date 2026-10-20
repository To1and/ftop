package theme

import (
	"fmt"
	"testing"
)

// TestGamutBudget prints the chroma the sRGB gamut allows at each lightness and
// hue. The first palette asked for C=0.14 at L=0.90, where blue can hold about
// 0.05, so the ramp was silently desaturated into near-white by oklchRGB's gamut
// reduction — which is why every hot end converged and the skyline looked pale.
// Knowing the ceiling is what keeps a ramp coloured all the way to its top.
func TestGamutBudget(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	hues := []float64{10, 35, 60, 100, 130, 165, 200, 230, 260, 290, 320, 350}
	fmt.Print("        ")
	for _, L := range []float64{.50, .55, .60, .65, .70, .75, .80, .85, .90} {
		fmt.Printf(" L%.2f", L)
	}
	fmt.Println()
	for _, h := range hues {
		fmt.Printf("h%3.0f  ", h)
		for _, L := range []float64{.50, .55, .60, .65, .70, .75, .80, .85, .90} {
			fmt.Printf(" %.3f", maxChroma(L, h))
		}
		fmt.Println()
	}
}

// TestRampsStaySaturated: a ramp is written as a fraction of the gamut ceiling,
// so it is never reduced by oklchRGB and stays as coloured at 100% as at 0%. This
// is the property the first palette lacked: it asked for a fixed chroma, sRGB
// could not give it at high lightness, and the ramp silently de-saturated into
// near-white.
//
// The slack is 0.10 rather than a tidy 0.02 because the ceiling itself is not
// smooth. sRGB's boundary has a genuine fold in the blues: at L .45 the ceiling at
// h 263.5° is 0.260 and at h 265° it is 0.309, a 19% step over 1.5 of hue. A ramp
// whose hue arc crosses that fold gets its fraction measured against two different
// ceilings a few steps apart, so the realised fraction dips even though nothing in
// the ramp changed. What this test is for — catching a ramp that fades out at the
// top — is a collapse toward zero, and 0.10 still catches that.
func TestRampsStaySaturated(t *testing.T) {
	for name, r := range Default.ramps {
		for v := 0; v <= 100; v += 5 {
			L, C, h := OKLCHOf(r.at(float64(v) / 100))
			ceiling := maxChroma(L, h)
			if ceiling <= 0 {
				continue
			}
			frac := C / ceiling
			want := r.S0 + (r.S1-r.S0)*float64(v)/100
			if frac < want-0.10 {
				t.Errorf("%s at %d%%: %.2f of the ceiling, want %.2f", name, v, frac, want)
			}
		}
	}
}

// TestPaletteStaysCalm pins the restraint, which is the thing the previous two
// palettes got wrong and could not measure until they were already on screen.
//
// The bound is not arbitrary. Kanagawa, Nord and Rosé Pine — the three palettes
// this one was chosen to sit beside — put their accent colours between 0.20 and
// 0.81 of the gamut ceiling, most of them around 0.5. The palette before this one
// sat at 0.70-0.98, twice Kanagawa's chroma, and read as gaudy. A ceiling of 0.60
// leaves a little headroom over the solved 0.55 while still failing anything that
// drifts back toward the old saturation.
//
// It is a separate test from TestRampsStaySaturated on purpose: that one asserts
// each ramp keeps the chroma it asked for, this one asserts it did not ask for
// very much.
func TestPaletteStaysCalm(t *testing.T) {
	const ceiling = 0.60
	for name, r := range Default.ramps {
		if r.S1 > ceiling {
			t.Errorf("%s declares saturation %.2f of the gamut ceiling, over the %.2f limit", name, r.S1, ceiling)
		}
		for v := 0; v <= 100; v++ {
			L, C, h := OKLCHOf(r.at(float64(v) / 100))
			if mc := maxChroma(L, h); mc > 0 && C/mc > ceiling {
				t.Errorf("%s at %d%% reaches %.2f of the gamut ceiling", name, v, C/mc)
			}
		}
	}
}

// TestBarGradientsStayVisible pins the reason the lightness bands are as wide as
// they are. A bar is coloured from its own start to its own reading, so the
// gradient it shows is the part of the band it crosses: the first filled cell to
// the last. With a band 0.16 tall a 20% bar spanned ΔE 3.9 and read as one flat
// colour; the bands here are 0.24-0.28 tall and the same bar spans 5.0-7.7, which is
// the difference between a gradient you can see and one you cannot.
//
// The floor is deliberately on the 20% bar rather than the full one: every bar
// looks graded at 90%, and the short bars are where the effect was lost.
func TestBarGradientsStayVisible(t *testing.T) {
	const width, value = 16, 20
	filled := (value*width + 50) / 100
	for name, r := range Default.ramps {
		var first, last RGB
		for i := 0; i < filled; i++ {
			f := float64(i) / float64(filled-1)
			c := r.at(float64(value) / 100 * f)
			if i == 0 {
				first = c
			}
			last = c
		}
		if d := DeltaE(first, last); d < 4.5 {
			t.Errorf("%s: a %d%% bar spans only ΔE %.1f from its first cell to its last", name, value, d)
		}
	}
}

// TestRampTopsStayColoured: the price of a wide band is that its top approaches
// white, where sRGB runs out of chroma. Every top must still carry its hue.
func TestRampTopsStayColoured(t *testing.T) {
	for name, r := range Default.ramps {
		_, C, _ := OKLCHOf(r.at(1))
		if C < 0.045 {
			t.Errorf("%s tops out at chroma %.3f: the top of the ramp has gone white", name, C)
		}
	}
}

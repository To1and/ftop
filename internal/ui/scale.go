package ui

import "ftop/internal/theme"

// mix blends two colours, a*(1-t) + b*t, for the shades no theme key names.
func mix(a, b theme.RGB, t float64) theme.RGB {
	return theme.RGB{
		R: int(float64(a.R) + float64(b.R-a.R)*t + 0.5),
		G: int(float64(a.G) + float64(b.G-a.G)*t + 0.5),
		B: int(float64(a.B) + float64(b.B-a.B)*t + 0.5),
	}
}

// pctWidth is the cells "59%" takes: the percentage sits CENTRED between its
// bar and the next element, so a narrow lane's gap is the same on both sides of
// it whether the reading is one digit or three.
func pctWidth(v int) int {
	n := 1
	if v >= 100 {
		n = 3
	} else if v >= 10 {
		n = 2
	}
	return n + 1
}

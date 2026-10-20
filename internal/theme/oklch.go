package theme

import "math"

// OKLCH colour, and the ramp a measurement walks.
//
// btop interpolates in sRGB, channel by channel, between two or three stops. That
// is why so many of its ramps are one colour getting dimmer: two reds interpolated
// in sRGB stay red, and the eye sees "more red" only as "less contrast". Here a ramp
// is specified in OKLCH — hue, chroma, lightness — which are the three things the
// eye actually separates, so a ramp can move in any of them independently.
//
// The numbers below are the standard Björn Ottosson transforms. They are written
// out rather than pulled from a colour library: the whole point is that every
// colour ftop draws is reproducible from this file.

// oklabToLinear converts an OKLab triple to linear sRGB (not yet encoded).
func oklabToLinear(L, a, b float64) (r, g, bl float64) {
	l_ := L + 0.3963377774*a + 0.2158037573*b
	m_ := L - 0.1055613458*a - 0.0638541728*b
	s_ := L - 0.0894841775*a - 1.2914855480*b
	l, m, s := l_*l_*l_, m_*m_*m_, s_*s_*s_
	return 4.0767416621*l - 3.3077115913*m + 0.2309699292*s,
		-1.2684380046*l + 2.6097574011*m - 0.3413193965*s,
		-0.0041960863*l - 0.7034186147*m + 1.7076147010*s
}

// linearToOKLab is the inverse.
func linearToOKLab(r, g, b float64) (L, a, bb float64) {
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return 0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s
}

// toLinear decodes one sRGB channel (0..1, gamma-encoded) to linear light.
func toLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// fromLinear encodes linear light back to sRGB.
func fromLinear(v float64) float64 {
	if v <= 0.0031308 {
		return 12.92 * v
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

// inGamut reports whether a linear triple is displayable, allowing the rounding
// slack the 8-bit output will swallow anyway.
func inGamut(r, g, b float64) bool {
	const eps = 0.002
	return r >= -eps && r <= 1+eps && g >= -eps && g <= 1+eps && b >= -eps && b <= 1+eps
}

// maxChroma is the most chroma this hue and lightness can hold inside sRGB.
//
// This bound is the reason the ramps are shaped the way they are: at L=0.90 no blue
// can hold more than C≈0.05, so a blue ramp that climbs to L=0.90 ends near white
// whatever its nominal chroma. Bisection rather than a closed form, because the
// gamut boundary is not a simple shape and a table of it would be a second source
// of truth.
func maxChroma(L, h float64) float64 {
	rad := h * math.Pi / 180
	ca, sa := math.Cos(rad), math.Sin(rad)
	lo, hi := 0.0, 0.4
	for i := 0; i < 26; i++ {
		mid := (lo + hi) / 2
		r, g, b := oklabToLinear(L, mid*ca, mid*sa)
		if inGamut(r, g, b) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

// oklchRGB converts an OKLCH triple to 8-bit sRGB, reducing chroma until the
// colour is displayable.
//
// Reducing chroma rather than clipping the channels is what keeps a ramp smooth at
// its bright end: clipping turns a too-saturated light blue into a hard white, while
// desaturating keeps it the right hue and merely paler.
func oklchRGB(L, C, h float64) RGB {
	rad := h * math.Pi / 180
	ca, sa := math.Cos(rad), math.Sin(rad)
	if C > 0 {
		if mc := maxChroma(L, h); C > mc*0.97 {
			C = mc * 0.97
		}
	}
	r, g, b := oklabToLinear(L, C*ca, C*sa)
	enc := func(v float64) int {
		v = fromLinear(math.Max(0, math.Min(1, v)))
		return int(math.Round(math.Max(0, math.Min(1, v)) * 255))
	}
	return RGB{enc(r), enc(g), enc(b)}
}

// HSLOf converts to hue (degrees), saturation and lightness, for tests and for
// comparing colours the way a person does.
func HSLOf(c RGB) (h, s, l float64) {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	hi, lo := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	l = (hi + lo) / 2
	if hi == lo {
		return 0, 0, l
	}
	d := hi - lo
	s = d / (1 - math.Abs(2*l-1))
	switch hi {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return math.Mod(h*60+360, 360), s, l
}

// OKLCHOf reports a colour's OKLCH coordinates. Used by tests to assert that a
// ramp actually moves, and that two ramps stay apart.
func OKLCHOf(c RGB) (L, C, h float64) {
	r, g, b := toLinear(float64(c.R)/255), toLinear(float64(c.G)/255), toLinear(float64(c.B)/255)
	L, a, bb := linearToOKLab(r, g, b)
	C = math.Hypot(a, bb)
	h = math.Mod(math.Atan2(bb, a)*180/math.Pi+360, 360)
	return L, C, h
}

// DeltaE is the perceptual distance between two colours (OKLab, ×100 to read
// like the familiar ΔE scale). Roughly: under 2 is invisible, 5 is a difference
// you can see side by side.
func DeltaE(a, b RGB) float64 {
	la, aa, ba := oklabOf(a)
	lb, ab, bb := oklabOf(b)
	return math.Hypot(math.Hypot(la-lb, aa-ab), ba-bb) * 100
}

// oklabOf reports a colour's Cartesian OKLab coordinates.
func oklabOf(c RGB) (L, a, b float64) {
	return linearToOKLab(
		toLinear(float64(c.R)/255),
		toLinear(float64(c.G)/255),
		toLinear(float64(c.B)/255),
	)
}

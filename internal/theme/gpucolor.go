package theme

import (
	"regexp"
	"strconv"
	"strings"
)

// GPU model colours.
//
// A board of mixed cards should say which cards are which before the names are
// read. Every product line gets a hue of its own and every model in it a shade
// of that hue, so two lines never look alike and two models of one line look
// related but not the same: a 4090 and a 5090 are both GeForce green, the 5090
// a step along from the 4090; an A100, an H200 and a B200 are all data-centre
// amber, one step per architecture.
//
// The hues are fixed (see palette.go). They are deliberately low-chroma and sit
// in the gaps between the measurement ramps' hue ranges, so a model name can
// never be mistaken for a reading, and a mixed board still separates GeForce
// from the data-centre line at a glance.

// productLine is an Nvidia product line, the unit that shares a hue.
type productLine int

const (
	lineOther       productLine = iota // anything not recognised: the text colour
	lineGeForce                        // GeForce RTX / GTX / TITAN: green
	lineDataCentre                     // the compute line, P100 V100 A100 H100/H200 B100/B200 GH/GB: amber
	lineInference                      // the inference and graphics data-centre cards, T4 L4 L20 L40: lime
	lineWorkstation                    // RTX A6000, RTX 6000 Ada, RTX PRO, Quadro: blue
	lineEmbedded                       // Jetson, Orin, Xavier: magenta
)

// A model's place in its line: gen is its generation or architecture, which
// turns the hue a step; variant tells apart models of one generation, which
// changes the shade.
type gpuModel struct {
	line         productLine
	gen, variant int
}

var (
	reGeForce = regexp.MustCompile(`\b(?:RTX|GTX)\s*(\d{4})`)
	reWork    = regexp.MustCompile(`\bRTX\s*(?:PRO\s*)?(?:A(\d{3,4})|(\d{4})\s*(?:ADA|BLACKWELL)?)\b`)
	reDC      = regexp.MustCompile(`\b(GB|GH|B|H|A|V|P)(\d{1,3})\b`)
	reInfer   = regexp.MustCompile(`\b(L|T)(\d{1,2})S?\b`)
)

// dcArch orders the data-centre architectures, oldest first.
var dcArch = map[string]int{"P": 0, "V": 1, "A": 2, "H": 3, "GH": 3, "B": 4, "GB": 4}

// classify places a card name, as NVML reports it, in its product line.
func classify(name string) gpuModel {
	n := strings.ToUpper(name)
	switch {
	case strings.Contains(n, "JETSON"), strings.Contains(n, "ORIN"), strings.Contains(n, "XAVIER"):
		return gpuModel{line: lineEmbedded}
	case strings.Contains(n, "QUADRO"):
		return gpuModel{line: lineWorkstation, variant: digits(n)}
	}
	if m := reWork.FindStringSubmatch(n); m != nil && !strings.Contains(n, "GEFORCE") {
		// "RTX A6000", "RTX 6000 Ada", "RTX PRO 6000": a workstation card, never a
		// GeForce, though both say RTX.
		if m[1] != "" {
			return gpuModel{line: lineWorkstation, gen: 0, variant: atoi(m[1])}
		}
		if strings.Contains(n, "ADA") || strings.Contains(n, "PRO") || strings.Contains(n, "BLACKWELL") || atoi(m[2])%1000 == 0 {
			gen := 1
			if strings.Contains(n, "BLACKWELL") || strings.Contains(n, "PRO") {
				gen = 2
			}
			return gpuModel{line: lineWorkstation, gen: gen, variant: atoi(m[2])}
		}
	}
	if m := reGeForce.FindStringSubmatch(n); m != nil {
		num := atoi(m[1]) // 4090: generation 4, tier 9; 1660: generation 1
		gen := num / 1000
		if num/100 == 16 {
			gen = 1
		}
		return gpuModel{line: lineGeForce, gen: gen, variant: (num % 1000) / 10}
	}
	if strings.Contains(n, "TITAN") {
		return gpuModel{line: lineGeForce, gen: 0, variant: digits(n)}
	}
	if m := reDC.FindStringSubmatch(n); m != nil {
		// A100, H200, B200: the leading digit is the variant; A30, A40, H20: the
		// tens digit is.
		v := atoi(m[2])
		if v >= 100 {
			v /= 100
		} else if v >= 10 {
			v /= 10
		}
		return gpuModel{line: lineDataCentre, gen: dcArch[m[1]], variant: v}
	}
	if m := reInfer.FindStringSubmatch(n); m != nil {
		gen := 0 // T: Turing
		if m[1] == "L" {
			gen = 1 // L: Ada
		}
		return gpuModel{line: lineInference, gen: gen, variant: atoi(m[2])}
	}
	return gpuModel{line: lineOther}
}

func atoi(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}

// digits is the first run of digits in s, as a number.
func digits(s string) int {
	start := strings.IndexAny(s, "0123456789")
	if start < 0 {
		return 0
	}
	end := start
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return atoi(s[start:end])
}

// modelColors caches each model's colour for one skin.
type modelColors struct {
	skin  *Skin
	cache map[string]RGB
}

func newModelColors(s *Skin) *modelColors {
	return &modelColors{skin: s, cache: map[string]RGB{}}
}

// lineHue is a product line's hue. Fixed per line, not borrowed from a theme:
// "which card is this" must not change when the palette does.
func (m *modelColors) lineHue(l productLine) float64 {
	if h, ok := modelHue[l]; ok {
		return h
	}
	return 0
}

// modelL and modelC are the lightness and chroma every product line starts from.
// Low chroma on purpose: this is a label, not a reading, and a saturated label
// competes with the meters it sits above.
const (
	modelL = 0.80
	modelC = 0.11
)

// of is a card name's colour: its line's hue, turned a step per generation and a
// step of lightness per variant.
func (m *modelColors) of(name string) RGB {
	if c, ok := m.cache[name]; ok {
		return c
	}
	g := classify(name)
	h := m.lineHue(g.line)
	l := modelL
	if g.line != lineOther {
		dh, dl := shade(g)
		h += dh
		l += dl
	}
	if l < 0.62 {
		l = 0.62
	}
	if l > 0.90 {
		l = 0.90
	}
	c := oklchRGB(l, modelC, h)
	m.cache[name] = c
	return c
}

// HSL is a colour's hue (degrees), saturation and lightness, for tests and for
// callers that need to compare colours as a person would.
func HSL(c RGB) (float64, float64, float64) { return HSLOf(c) }

// variantSteps are the lightness steps variants of one generation take, in an
// order that keeps the commonest neighbours (100 and 200, 80 and 90) apart.
var variantSteps = [4]float64{-0.06, 0.06, -0.12, 0.12}

// shade is a model's turn of its line's hue (degrees) and change of lightness.
// A generation is a hue step, small enough that the line still reads as one
// colour; a variant within a generation is a lightness step.
func shade(g gpuModel) (float64, float64) {
	switch g.line {
	case lineGeForce:
		// 3090, 4090, 5090: a hue step each; 4060 to 4090: lighter with the tier.
		return 22 * float64(g.gen-4), 0.05 * float64(g.variant-7)
	case lineDataCentre:
		// P, V, A, H, B: a hue step per architecture; 100, 200, 800 a shade each.
		return 10 * float64(g.gen-2), variantSteps[g.variant%4]
	case lineInference:
		return 14 * float64(g.gen), variantSteps[(g.variant/10+g.variant)%4]
	case lineWorkstation:
		return 12 * float64(g.gen-1), variantSteps[(g.variant/1000)%4]
	}
	return 0, 0
}

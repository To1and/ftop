package theme

import "strings"

// Skin is the palette the UI draws with, plus one scale per measurement.
//
// There is exactly one skin now. It stays a struct rather than a pile of package
// globals because the scales are cheap to hold and the tests build one per case;
// NewSkin no longer takes a theme name, because there is nothing to choose.
type Skin struct {
	Theme *Theme
	Bg    RGB
	Hue   struct {
		Frame, Warn, Dead, Focus RGB
	}
	// Scale shades one metric: every gradient from its darkest step to its
	// brightest, so a value drawn in it says its own size by its own colour.
	Scale struct {
		CPU, Mem, VRAM, Power, HostCPU, Swap RGBScale
	}
	// Model is a GPU model's colour: one hue per product line, a shade of it per
	// model (see gpucolor.go).
	Model func(gpuName string) RGB
	// Agent is an agent's colour, by the kind recorded on its claim
	// ("claude-code", "codex", "human", …). An agent ftop does not recognise gets
	// the label grey: this answers "who", and inventing a brand for "someone"
	// would make the anonymous case the loudest one on the board.
	Agent func(kind string) RGB
}

// NewSkin resolves the one theme.
//
// btop's theme files are gone. Their four panel hues had to be re-derived into
// ftop's states, and several themes defined all four identically, so the code
// carried a fallback ladder for "the theme has no distinguishable colours at
// all". A palette that is specified by measurement removes the question.
func NewSkin() *Skin {
	t := Default
	s := &Skin{Theme: t, Bg: t.bg}
	s.Hue.Frame = t.frame
	s.Hue.Warn = t.warn
	s.Hue.Dead = t.dead
	s.Hue.Focus = t.focus

	//   util      green to yellow-green: the device's own utilisation
	//   vram      cyan-blue to indigo: the device's memory
	//   power     amber to orange
	//   host cpu  violet to magenta
	//   host mem  blue to periwinkle
	//   swap      indigo, and the only ramp that starts below the others: it is
	//             the host's least important reading, so it recedes rather than
	//             competing with the two device meters it sits between
	//
	// util and vram are the pair that shares a row on every card, so they are the
	// furthest apart in hue and identical in lightness: at any reading, a full
	// util and a full vram look equally full and never the same colour.
	s.Scale.CPU = newScale(t, "util")
	s.Scale.VRAM = newScale(t, "vram")
	s.Scale.Power = newScale(t, "power")
	s.Scale.HostCPU = newScale(t, "host")
	s.Scale.Mem = newScale(t, "mem")
	s.Scale.Swap = newScale(t, "swap")

	s.Model = newModelColors(s).of
	s.Agent = agentColour
	return s
}

// agentColour is an agent kind's brand colour.
//
// The kinds are the ones the agent-facing CLI writes: the runtime names it
// detects for itself, plus "human" for a person at a terminal. The table is open
// on purpose. It is keyed by the string on the claim, so an agent that declared
// its own name through AI_AGENT, or a runtime that does not exist yet, still
// works — it just gets the unknown grey until someone adds a colour for it, which
// is the right default: a claim ftop has never seen should not look like the two
// it knows best.
func agentColour(kind string) RGB {
	switch kind {
	case "claude-code":
		return colClaude
	case "codex":
		return colCodex
	}
	return colAgentUnknown
}

// ShortLabel is a host's display name when the configuration gave it none.
//
// It is the name itself. The fleet's shared prefix is removed where the fleet is
// known (config.FleetPrefix); a fixed list of prefixes here would be one
// installation's naming scheme compiled into everyone's binary.
func ShortLabel(name string) string { return name }

// ShortGPU strips vendor and marketing words: "NVIDIA GeForce RTX 4090" -> "RTX 4090".
func ShortGPU(name string) string {
	n := name
	for _, drop := range []string{"NVIDIA ", "GeForce ", "Tesla ", "NVIDIA"} {
		n = strings.ReplaceAll(n, drop, "")
	}
	n = strings.TrimSpace(n)
	if n == "" {
		return name
	}
	return n
}

// MiB renders a MiB count the way btop's humanizer does at this scale.
func MiB(v uint64) string {
	switch {
	case v >= 1024*1024:
		return fmtF(float64(v)/1024/1024, 1) + "T"
	case v >= 1024:
		return fmtF(float64(v)/1024, 1) + "G"
	}
	return fmtF(float64(v), 0) + "M"
}

func fmtF(v float64, dec int) string {
	if dec == 0 {
		return strconvInt(int(v + 0.5))
	}
	whole := int(v)
	frac := int((v-float64(whole))*10 + 0.5)
	if frac >= 10 {
		return strconvInt(whole+1) + ".0"
	}
	return strconvInt(whole) + "." + strconvInt(frac)
}

// strconvInt avoids pulling strconv into this file's hot path for one call site.
func strconvInt(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// Clip truncates a string to w columns, appending an ellipsis when it had to cut.
func Clip(s string, w int) string {
	if RuneWidth(s) <= w {
		return s
	}
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	EachGlyph(s, func(g Glyph) bool {
		if used+g.W > w-1 {
			return false
		}
		b.WriteString(g.Text)
		used += g.W
		return true
	})
	return b.String() + "…"
}

// PadLeft left-pads to w columns.
func PadLeft(s string, w int) string {
	if n := w - RuneWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// RGBScale is a measurement's colour walk, sampled at one-step resolution.
//
// It used to be eleven samples taken off a btop gradient with a "floor" that
// skipped the unreadably dark opening entries. There is no floor any more: the
// ramps are specified in OKLCH with a lightness that is already chosen for a dark
// background, so the whole range is usable and the scale is a straight sample of
// the ramp.
type RGBScale struct {
	ramp  ramp
	steps [101]RGB
}

// newScale samples a measurement's ramp.
func newScale(t *Theme, name string) RGBScale {
	s := RGBScale{ramp: t.Ramp(name)}
	for i := range s.steps {
		s.steps[i] = s.ramp.at(float64(i) / 100)
	}
	return s
}

// At is the scale's colour for v in 0..100.
func (s RGBScale) At(v float64) RGB {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return s.ramp.at(v / 100)
}

// Top is the scale's brightest colour.
func (s RGBScale) Top() RGB { return s.steps[len(s.steps)-1] }

// AtFraction is a ramp's colour for a fraction of its span, which is what a
// progress bar needs: the cells of a bar interpolate from the ramp's start to the
// reading, not from 0 to 100.
func (s RGBScale) AtFraction(t float64) RGB { return s.ramp.at(t) }

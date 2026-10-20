package theme

// Theme is the named colours the UI asks for, plus the measurement ramps.
//
// The names are ftop's own. btop's keys (cpu_start, used_mid, proc_box…) described
// btop's four panels; ftop has one kind of box and a fixed set of readings, so a
// palette keyed by measurement says what it means and leaves no room for a
// "fall back to the default theme" path that silently hides a typo.
type Theme struct {
	bg, fg, muted, frame, focus, warn, dead, ok, track RGB
	title, accent                                      RGB
	div                                                RGB
	ramps                                              map[string]ramp
}

// Default is the one theme. Built once; every Skin shares it because none of it
// changes at run time any more.
var Default = &Theme{
	bg:     colBg,
	fg:     colFg,
	muted:  colMuted,
	frame:  colFrame,
	focus:  colFocus,
	warn:   colWarn,
	dead:   colDead,
	ok:     colOK,
	track:  colTrack,
	title:  colFocus,
	accent: colWarn,
	div:    colFrame,
	ramps: map[string]ramp{
		"util":  rampUtil,
		"vram":  rampVRAM,
		"power": rampPower,
		"host":  rampHost,
		"mem":   rampMem,
		"swap":  rampSwap,
	},
}

// Bg is the page colour every cell is cleared to.
func (t *Theme) Bg() RGB { return t.bg }

// Band is the background of a card's alternating rows.
//
// A card alternates its blocks onto a faint lift so two lines of readings do not
// read as one run. The lift is small — this is a distinction between rows, not a
// second surface — but it has to be a lift at all: at ΔE 7 from the page it is
// visible without competing with anything drawn on it.
func (t *Theme) Band() RGB { return mix(t.bg, t.fg, 0.08) }

// Track is the unfilled part of a meter and of a lamp.
//
// It is mixed from the BAND, not from the page, and this is the whole of a bug
// that made empty cells disappear. The track used to be meter_bg mixed 45%
// toward the page, which landed at ΔE 2.6 from a card's alternating rows: on
// every second block the unfilled half of a bar was not a dim bar, it was
// invisible, and the row read as a bar of a different length than it had. A
// track has to clear the page by enough to be a surface AND clear the band by
// enough to survive sitting on it; measured against both, 0.11 toward the text
// is where those two costs meet (ΔE 8.2 from the band, 15.2 from the page). Going
// further is not free: past 0.12 the track approaches the label grey and a row of
// empty cells starts to read as text.
func (t *Theme) Track() RGB { return mix(t.Band(), t.fg, 0.11) }

// C returns a named colour.
//
// An unknown name is a programming error, not a theme to fall back to: the old
// loader answered every unrecognised key with btop's default, which is why a
// parser reading the wrong syntax still produced a fully coloured board. A
// missing name now comes back as magenta, which is impossible to miss on screen
// and impossible to mistake for a themed colour.
func (t *Theme) C(name string) RGB {
	switch name {
	case "main_bg", "bg":
		return t.bg
	case "main_fg", "fg":
		return t.fg
	case "graph_text", "muted", "inactive_fg":
		return t.muted
	case "title":
		return t.title
	case "hi_fg", "accent":
		return t.accent
	case "div_line", "frame":
		return t.div
	case "meter_bg", "track":
		return t.track
	case "proc_misc", "ok":
		return t.ok
	case "warn":
		return t.warn
	case "dead":
		return t.dead
	case "focus":
		return t.focus
	}
	return UnknownRamp
}

// AgentColour is an agent kind's brand colour.
//
// It lives on the Theme because the lamp and the footer both need it and neither
// takes a Skin: the footer is drawn from the ledger, which has no opinion about
// appearance. See agentColour for the table.
func (t *Theme) AgentColour(kind string) RGB { return agentColour(kind) }

// Ramp returns a measurement's ramp by name.
//
// The name is the measurement — "util", "vram", "power", "host", "mem", "swap" —
// so a caller that draws a number also names what the number is. btop's names
// ("cpu", "used", "cached") named the theme key instead, which is how the skyline
// ended up labelling utilisation with the process ramp.
func (t *Theme) Ramp(name string) ramp { return t.ramps[name] }

// UnknownRamp is the colour of a name the palette does not have. Magenta, because
// it is impossible to mistake for a themed colour and impossible to miss.
var UnknownRamp = RGB{0xff, 0x00, 0xff}

// G shades a ramp at v (0..100). It exists for the few places that draw a single
// reading rather than a series; everything else uses RGBScale.
func (t *Theme) G(name string, v int) RGB {
	r, ok := t.ramps[name]
	if !ok {
		return UnknownRamp
	}
	return r.at(float64(v) / 100)
}

// Temp is a temperature reading's colour: three fixed bands.
//
// 60°C is where a modern card starts stepping clocks and 85°C is where it is
// throttling hard, so those are the boundaries. A gradient here would say
// "62°C is a bit more than 58°C" in a colour that also means "utilisation", and
// the two rows sit one above the other on the detail page.
func (t *Theme) Temp(celsius float64) RGB {
	switch {
	case celsius >= 85:
		return tempHot
	case celsius >= 60:
		return tempWarm
	}
	return tempOK
}

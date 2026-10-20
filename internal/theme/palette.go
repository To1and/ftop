package theme

// The ftop theme: one palette, specified in OKLCH.
//
// There used to be 41 btop themes here, and the board borrowed its colours from
// whichever one was loaded. That had two problems the eye kept finding:
//
//   - btop's ramps interpolate in sRGB between two or three stops, so most of them
//     are one hue getting dimmer. Measured across the 41 themes, 102 of 210 ramps
//     got DARKER as the reading rose, and 118 held a hue arc under 25°.
//   - ftop has one kind of box and a fixed set of measurements, not btop's four
//     panels. Reusing box hues by name (cpu_box, mem_box) and then re-deriving
//     state colours from them meant half the palette was a fallback ladder.
//
// So the palette is explicit now, and every ramp is a walk through OKLCH. A ramp
// names three things — where its hue starts and ends, and the lightness band it
// crosses — and the reading decides how far along it sits. Two rules hold for all
// of them, and the tests assert both:
//
//   - lightness never falls as the reading rises. A fuller bar is never darker.
//   - the six ramps stay apart: no two of them share a colour at the same reading.

// ramp is one measurement's colour walk.
//
// Lightness carries the reading; hue carries the identity. That is deliberate:
// lightness is the one channel that survives a colour-blind viewer, a 16-colour
// terminal and a cheap panel, so it is the channel that has to mean the number.
type ramp struct {
	H0, H1 float64 // hue at 0 and at 100, degrees
	L0, L1 float64 // OKLab lightness at 0 and at 100
	// S0 and S1 are saturation at 0 and at 100, as a fraction of the most
	// chromatic colour sRGB can show at that hue and lightness.
	//
	// Absolute chroma does not survive a lightness ramp: at L .50 blue can hold
	// C 0.21, and at L .90 it holds C 0.05. A ramp written in absolute chroma
	// therefore gets clipped somewhere along its length by the gamut reduction in
	// oklchRGB, and the clipped stretch silently turns pale — which is exactly how
	// every ramp in the first version ended in near-white. A fraction is measured
	// against the ceiling at each point, so a ramp keeps the same place in the
	// gamut all the way up: as saturated as that lightness allows, never reduced.
	S0, S1 float64
}

// at is the colour this ramp shows for a reading of t (0..1).
//
// The hue takes the short way round, so a ramp written as 120→165 stays in the
// yellow-greens rather than sweeping the long way through blue.
func (r ramp) at(t float64) RGB {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	d := r.H1 - r.H0
	for d > 180 {
		d -= 360
	}
	for d < -180 {
		d += 360
	}
	L := r.L0 + (r.L1-r.L0)*t
	h := r.H0 + d*t
	// Saturation against the ceiling at this point, so the gamut never has to
	// reduce the request.
	s := r.S0 + (r.S1-r.S0)*t
	return oklchRGB(L, s*maxChroma(L, h), h)
}

// The ramps.
//
// The six split into two families by scope, and the split is by hue before it is
// by anything else:
//
//   device  util green, vram blue
//   host    cpu violet, mem rose
//   other   power orange, swap teal
//
// A card shows all six at once — its own cpu and memory above, its devices
// below — so scope has to read before magnitude does. Green against blue and
// violet against rose are two separate axes, and the ranges do not overlap:
// memory is not a shade of blue that could be mistaken for VRAM, which is the
// confusion that started this redesign. Tests assert that the two hue ranges stay
// disjoint.
//
// Within a ramp, lightness carries the reading and is monotonic: a fuller bar is
// never darker. The device pair share a lightness band exactly, so a full util
// and a full vram look equally full; the host pair and the two standalone
// readings sit progressively lower, and swap is the lowest of all.
//
// # Restraint is a number, not a taste
//
// Chroma is specified as a fraction of the gamut ceiling at each point (see
// ramp.S0/S1) rather than as an absolute value. That is what keeps a ramp
// coloured at 100%: a fixed chroma that sRGB cannot reach at high lightness gets
// silently reduced by the gamut mapping, and the top of every ramp turns pale.
//
// The S values also decide how loud the board is, and they were the whole problem
// with the first two palettes. Measured the same way, three palettes that are
// generally called restrained sit at:
//
//	Kanagawa   0.37 - 0.73 of the ceiling
//	Nord       0.20 - 0.57
//	Rosé Pine  0.22 - 0.81
//
// and the previous ftop palette sat at 0.70 - 0.98: roughly twice Kanagawa's
// chroma, which is why it read as gaudy next to any of them. The warmest ramp
// here declares 0.58 and the rest sit under it, and TestPaletteStaysCalm holds
// the whole palette under 0.60, so "calm enough" cannot quietly drift back out.
//
// Lowering it costs separation, and the cost is measurable rather than a matter
// of opinion: solving the six ramps at each ceiling gives a worst-pair ΔE of
// 10.6 at 0.50, 11.5 at 0.55, 12.2 at 0.60 and 12.5 at 0.70. Going from 0.55 to
// 0.70 buys 1.0 of separation and doubles the loudness; 0.55 is the knee.
//
// # Saturation and lightness are separate dials
//
// Muting a palette and darkening it are two different things, and the first pass
// at this did both at once: it clamped chroma *and* lowered the lightness bands,
// and the result was not calm but dim. A bar at L .26 on a page at L .17 has
// almost no contrast left whatever its hue is.
//
// The bands here sit high — a ramp runs from about L .44 to L .88 — because the
// lightness band does two jobs at once. It makes a coloured bar read as bright
// against a near-black page (low saturation at high lightness is soft; low
// saturation at low lightness is only dark), and it is what the gradient inside a
// bar is made of: a bar is coloured from its own start to its own reading, so the
// gradient a 20% bar shows is the part of the band it crosses, and a narrow band
// made every short bar almost flat. Measured first-cell to last, the bars here
// span ΔE 5.0 - 7.7 at 20%, 12.3 - 14.4 at 50%. A band that reached L .96 would
// roughly double that and take the top of every ramp to near-white, which is the
// failure the first palette had; .84-.88 is the last lightness that keeps a
// colour at the top. TestBarGradientsStayVisible asserts the floor.

var (
	// Device meters: green and blue. These are the two that share a row on every
	// card, so they are the two the eye must never confuse.
	//
	// util is a soft green rather than a yellow-green: yellow-green is where power
	// lives once a ramp arcs, and the two came within ΔE 8 at the cold end when
	// util started at h 115. Starting at 136 puts the cold end in green proper;
	// the arc still reaches 158 by the top, which is what keeps the ramp a ramp
	// rather than one colour dimming.
	rampUtil = ramp{H0: 136, H1: 158, L0: .56, L1: .84, S0: .56, S1: .56}
	rampVRAM = ramp{H0: 260, H1: 283, L0: .44, L1: .72, S0: .52, S1: .54}

	// Host resources: violet and rose. A host reading is never a device reading.
	// Rose is deliberately not a blue: memory reading as VRAM was the confusion
	// this palette was rebuilt to remove.
	//
	// host is a blue-violet, not a pink-violet. An arc that ran to 322 crossed into
	// pink by its top and read as rose, which is memory's family; holding it to
	// 290-306 and lifting the band to L .54-.82 keeps it unmistakably violet. The
	// narrow arc is why the band is the taller of the two: hue does less work, so
	// lightness does more.
	rampHost = ramp{H0: 290, H1: 306, L0: .54, L1: .82, S0: .44, S1: .58}
	rampMem  = ramp{H0: 334, H1: 361, L0: .46, L1: .72, S0: .36, S1: .52}

	// Standalone readings. Power is the only warm ramp, so a warm cell on a card
	// is a wattage and nothing else. Swap is the lowest of the six: it is the
	// host's third resource and the last reading on that page, so it recedes
	// rather than competing with the two meters either side of it. It sits a step
	// lower than the rest rather than far lower, so it still reads as a bar.
	//
	// swap starts at 204 rather than 197 because util now starts at 136: the two
	// were the closest pair on the board at ΔE 8.6, and the two degrees of
	// separation bought here are what let util be green at all.
	rampPower = ramp{H0: 62, H1: 94, L0: .56, L1: .80, S0: .41, S1: .42}
	rampSwap  = ramp{H0: 204, H1: 226, L0: .54, L1: .80, S0: .55, S1: .55}
)

// Temperature is not a ramp. It answers "will this throttle", a threshold
// question, not "how much", a proportion — so it gets three fixed bands, and its
// hues are deliberately outside every ramp's range (a warm reading that also
// graded through amber would be read as a high utilisation).
var (
	tempOK   = RGB{0x8a, 0xb8, 0x8a}
	tempWarm = RGB{0xd2, 0xa8, 0x6a}
	tempHot  = RGB{0xe0, 0x6a, 0x70}
)

// State colours. These are not ramps: a host is online, stale, or down, and a
// frame is none of those. They are fixed so that a healthy board stays grey and
// only a host that is news is coloured.
//
// The neutrals are graded rather than flat: the page is the darkest cell on
// screen, the frame one step up, the track between them, and the label grey a
// long way above the page so it never competes with a reading. The text is a
// warm off-white rather than pure grey — on a background this dark a neutral
// #ccd0d4 reads slightly blue and cold.
var (
	colBg    = RGB{0x0d, 0x0f, 0x12}
	colFg    = RGB{0xd6, 0xd5, 0xd0}
	colMuted = RGB{0x44, 0x48, 0x50}
	colFrame = RGB{0x32, 0x37, 0x3e}
	colFocus = RGB{0xec, 0xea, 0xe4}
	colWarn  = RGB{0xc9, 0x9a, 0x55}
	colDead  = RGB{0xc2, 0x62, 0x69}
	colOK    = RGB{0x6d, 0xb1, 0x86}
	colTrack = RGB{0x1e, 0x22, 0x27}
)

// Agent brands.
//
// An agent's colour is not a reading, so it may not come from a ramp: a ramp
// answers "how much", and these answer "who". They are the runtimes' own colours,
// held as given rather than re-derived, because an agent's colour is the one
// place on this board where someone else already made the choice.
//
// They are also the tightest colours in the palette, and that is deliberate on
// our side rather than theirs. Claude's orange sits at h39, in the middle of
// power's hue arc, so power was moved to h62-94 to get out of its way (see
// rampPower); codex's blue sits at h256 against vram's h260-283 and clears it by
// ΔE 8.8 at its closest reading, which is the separation floor the ramps already
// use among themselves. TestAgentColoursStayOffTheRamps holds both numbers.
var (
	colClaude = RGB{0xd9, 0x77, 0x57}
	colCodex  = RGB{0x4c, 0x9a, 0xff}
)

// colAgentUnknown is an agent ftop cannot name. It is the label grey, not a
// brand: inventing a hue for "unknown" would give an unidentified process the
// strongest identity on the board.
var colAgentUnknown = colMuted

// Model hues: one per product line. Fixed hue, low chroma — these answer "which
// card is this", not "how busy", so they must never read as a measurement. They
// live in the gaps of the ramps' hue ranges.
var modelHue = map[productLine]float64{
	lineGeForce:     142,
	lineDataCentre:  32,
	lineInference:   196,
	lineWorkstation: 262,
	lineEmbedded:    312,
}

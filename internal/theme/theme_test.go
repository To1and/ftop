package theme

import (
	"math"
	"testing"
)

// TestRampsMoveTogether: every ramp must actually change as the reading rises, in
// both lightness and hue. A ramp that only moves one of them is the btop bug this
// palette exists to fix — measured on btop's 41 themes, 102 of 210 ramps got
// darker as the reading rose.
func TestRampsMoveTogether(t *testing.T) {
	for name := range Default.ramps {
		lo, hi := Default.G(name, 0), Default.G(name, 100)
		dL, dC, dH := hueLightDelta(lo, hi)
		if dL < 0.15 {
			t.Errorf("%s: lightness moves only %.2f from 0 to 100", name, dL)
		}
		if dH < 20 && dC < 0.04 {
			t.Errorf("%s: neither hue (%.0f°) nor chroma (%.2f) moves; the ramp is one colour dimming",
				name, dH, dC)
		}
	}
}

// TestRampLightnessIsMonotonic: a fuller bar is never darker. This is the rule
// that makes lightness readable as the number.
func TestRampLightnessIsMonotonic(t *testing.T) {
	for name := range Default.ramps {
		prev := -1.0
		for v := 0; v <= 100; v++ {
			L, _, _ := OKLCHOf(Default.G(name, v))
			if L < prev-0.004 { // 8-bit quantisation slack
				t.Fatalf("%s: L falls from %.3f to %.3f at %d%%", name, prev, L, v)
			}
			prev = L
		}
	}
}

// TestUtilAndVRAMStayApart: these two share a row on every card, so at any
// reading they must be distinguishable. ΔE under 5 is not.
func TestUtilAndVRAMStayApart(t *testing.T) {
	worst, at := math.Inf(1), 0.0
	for v := 0; v <= 100; v++ {
		d := DeltaE(Default.G("util", v), Default.G("vram", v))
		if d < worst {
			worst, at = d, float64(v)
		}
	}
	if worst < 8 {
		t.Errorf("util and vram come within ΔE %.1f at %.0f%%", worst, at)
	}
}

// TestEveryRampStaysApart: no two measurement ramps may meet. Every ramp is
// visible somewhere on the same screen, so a collision means two different facts
// drawn in the same colour.
//
// The floor is 8, and it used to be 11. The two numbers are the two ends of one
// trade and the choice between them was made on screen rather than here: a bar's
// gradient is the part of its lightness band it crosses, so a narrow band made
// every short bar almost flat (ΔE 3.9 from a 20% bar's first cell to its last,
// which is invisible). Widening the bands to ΔE 5.0-7.7 buys a visible gradient
// and costs separation, because six bands of 0.24 can no longer be stacked
// without overlapping. 8 is where they trade now. The pair that started this —
// memory against VRAM — is still told apart by hue range, which does not move.
func TestEveryRampStaysApart(t *testing.T) {
	names := []string{"util", "vram", "power", "host", "mem", "swap"}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			worst, at := math.Inf(1), 0
			for v := 0; v <= 100; v++ {
				if d := DeltaE(Default.G(names[i], v), Default.G(names[j], v)); d < worst {
					worst, at = d, v
				}
			}
			if worst < 8 {
				t.Errorf("%s and %s come within ΔE %.1f at %d%%", names[i], names[j], worst, at)
			}
		}
	}
}

// TestUtilAndVRAMShareLightness: "how full" must mean the same thing on both
// bars of a row. A full util and a full vram are equally full, so they are
// equally light.
//
// The bands do not coincide any more, and that is deliberate: saturation is held
// down, so lightness has to carry more of the difference between metrics, and
// six ramps cannot all share one band. util sits above vram by design, which is
// why the two are told apart by hue first (TestUtilAndVRAMStayApart) and by this
// offset second. What must not happen is the offset growing until the two bars
// report different amounts: the gap is capped at one visible step.
func TestUtilAndVRAMShareLightness(t *testing.T) {
	const maxStep = 0.16
	for v := 0; v <= 100; v += 10 {
		lu, _, _ := OKLCHOf(Default.G("util", v))
		lv, _, _ := OKLCHOf(Default.G("vram", v))
		if math.Abs(lu-lv) > maxStep {
			t.Errorf("at %d%%: util L=%.2f, vram L=%.2f, a gap of %.2f (max %.2f)",
				v, lu, lv, math.Abs(lu-lv), maxStep)
		}
	}
}

// TestVRAMStaysBlue: the memory bar must read as blue at every point, including
// full. A ramp that climbs too far in lightness runs out of sRGB chroma and
// turns white, which is the failure this palette was chosen against.
func TestVRAMStaysBlue(t *testing.T) {
	for v := 0; v <= 100; v += 5 {
		_, C, H := OKLCHOf(Default.G("vram", v))
		if C < 0.045 {
			t.Errorf("vram at %d%% has chroma %.3f; it is washing out", v, C)
		}
		if H < 180 || H > 290 {
			t.Errorf("vram at %d%% is hue %.0f°, outside blue", v, H)
		}
	}
}

// TestModelColoursDiffer: one hue per product line, and the two the fleet is
// actually made of — GeForce and the data-centre line — must be far apart.
func TestModelColoursDiffer(t *testing.T) {
	s := NewSkin()
	g := s.Model("NVIDIA GeForce RTX 4090")
	d := s.Model("NVIDIA H200")
	if got := DeltaE(g, d); got < 20 {
		t.Errorf("GeForce and data-centre are ΔE %.1f apart", got)
	}
	// Per-generation steps stay small enough to read as one family.
	g5 := s.Model("NVIDIA GeForce RTX 5090")
	if got := DeltaE(g, g5); got < 2 || got > 12 {
		t.Errorf("4090 vs 5090: ΔE %.1f, want a visible but small step", got)
	}
}

// TestRuneWidth pins the CJK width rule, which every layout bug in the earlier
// implementation traced back to.
func TestRuneWidth(t *testing.T) {
	cases := map[string]int{
		"abc": 3, "主机": 4, "GPU 均值": 8, "●": 1, "█": 1,
	}
	for s, want := range cases {
		if got := RuneWidth(s); got != want {
			t.Errorf("RuneWidth(%q) = %d, want %d", s, got, want)
		}
	}
}

// TestUnknownColourIsObvious: a name the palette does not have must not silently
// become a themed grey. btop's loader answered every unrecognised key with the
// default theme, which is how a parser reading the wrong syntax still produced a
// fully coloured board.
func TestUnknownColourIsObvious(t *testing.T) {
	if got := Default.C("cpu_start"); got != (RGB{0xff, 0x00, 0xff}) {
		t.Errorf("unknown colour = %s, want magenta", got.Hex())
	}
}

// TestGraphPadsShortSeries makes sure a fresh stream grows in from the right
// instead of being stretched across the whole width.
func TestGraphPadsShortSeries(t *testing.T) {
	tm := Default
	rows := Graph(tm, []float64{100}, 20, 1, "", GraphOpts{})
	blank := 0
	for _, c := range rows[0] {
		if c.Text == " " {
			blank++
		}
	}
	if blank < 15 {
		t.Errorf("only %d blank cells; short series was stretched, not padded", blank)
	}
}

// TestGraphShadesByHeight: a graph's row of cells takes its colour from how high
// its dots reach, so a taller column is brighter than a shorter one.
func TestGraphShadesByHeight(t *testing.T) {
	tm := Default
	data := make([]float64, 80)
	for i := range data {
		data[i] = 100
	}
	rows := Graph(tm, data, 40, 3, "", GraphOpts{NoZero: true, Scale: ptrScale(tm.Ramp("util"))})
	top, bot := rows[0][10].FG, rows[2][10].FG
	if top == bot {
		t.Errorf("a full column is one colour %s from floor to top", top.Hex())
	}
	lt, _, _ := OKLCHOf(top)
	lb, _, _ := OKLCHOf(bot)
	if lt <= lb {
		t.Errorf("the top of a full column (L=%.2f) is not brighter than its floor (L=%.2f)", lt, lb)
	}
}

// TestGraphColourFollowsHeight: a cell's colour is decided by how high its dots
// reach, so two cells at different heights differ and two at the same height do
// not. The reading does not tint the graph — only the height does.
func TestGraphColourFollowsHeight(t *testing.T) {
	tm := Default
	sc := ptrScale(tm.Ramp("util"))
	// A 100% column beside a 60% column: the tall column's lower cell sits at the
	// same height as the short column's only cell, and low columns are darker
	// than the tall column's surface.
	rows := Graph(tm, []float64{100, 100, 60, 60}, 2, 2, "", GraphOpts{Scale: sc})
	if rows[0][0].FG == rows[1][0].FG {
		t.Errorf("one column is one colour top to bottom: %s", rows[0][0].FG.Hex())
	}
	lTop, _, _ := OKLCHOf(rows[0][0].FG)
	lBot, _, _ := OKLCHOf(rows[1][0].FG)
	if lTop <= lBot {
		t.Errorf("the top of a full column (L=%.2f) is not above its floor (L=%.2f)", lTop, lBot)
	}
}

func ptrScale(r ramp) *RGBScale {
	s := RGBScale{ramp: r}
	return &s
}

// hueLightDelta is the lightness and (short-way) hue difference between two
// colours, plus their chroma difference.
func hueLightDelta(a, b RGB) (dL, dC, dH float64) {
	la, ca, ha := OKLCHOf(a)
	lb, cb, hb := OKLCHOf(b)
	d := math.Abs(ha - hb)
	if d > 180 {
		d = 360 - d
	}
	return math.Abs(la - lb), math.Abs(ca - cb), d
}

// TestSwapRecedes: swap is the host's third resource and the least important of
// the six, so it must start below every other ramp. A reading that graded through
// the same lightness band as host cpu would compete with it on a page where memory
// has already said which is the bigger number.
func TestSwapRecedes(t *testing.T) {
	// Swap must be the quietest of the six, but "quietest" is not "darkest". The
	// bands are wide enough to carry a visible gradient, so they overlap on the
	// lightness axis and swap cannot also sit below every other ramp's top. What
	// has to hold is that swap is dim *for its lightness*: hue is what makes it
	// recede. It sits in the blue-greens, out of every other ramp's range, and its
	// chroma stays under the rest at the same reading.
	for _, name := range []string{"util", "vram", "power", "host", "mem"} {
		for v := 25; v <= 100; v += 25 {
			_, swC, _ := OKLCHOf(Default.G("swap", v))
			_, c, _ := OKLCHOf(Default.G(name, v))
			if swC > c+0.03 {
				t.Errorf("at %d%%: swap chroma %.3f exceeds %s's %.3f", v, swC, name, c)
			}
		}
	}
	// And it is not the loudest by chroma either: it is a dim reading, so its
	// most chromatic point stays under every other ramp's.
	_, swC, _ := OKLCHOf(Default.G("swap", 100))
	for _, name := range []string{"util", "vram", "power", "host", "mem"} {
		_, c, _ := OKLCHOf(Default.G(name, 100))
		if swC > c+0.02 {
			t.Errorf("swap reaches chroma %.3f, more than %s's %.3f", swC, name, c)
		}
	}
}

// TestEveryScaleHasARamp: a Skin field wired to a ramp name the theme does not
// have renders black, not magenta: Ramp returns the zero value, and the zero
// ramp's lightness is 0. That is how swap shipped for one merge — a black bar on
// a black background, invisible rather than wrong, which is the failure mode no
// screenshot catches.
func TestEveryScaleHasARamp(t *testing.T) {
	s := NewSkin()
	scales := map[string]RGBScale{
		"util": s.Scale.CPU, "vram": s.Scale.VRAM, "power": s.Scale.Power,
		"host": s.Scale.HostCPU, "mem": s.Scale.Mem, "swap": s.Scale.Swap,
	}
	for name, sc := range scales {
		if L, _, _ := OKLCHOf(sc.At(50)); L < 0.2 {
			t.Errorf("%s at 50%% is L=%.2f: the scale has no ramp behind it", name, L)
		}
	}
}

// TestMemoryAndVRAMAreDifferentFamilies: these two were the pair the whole
// palette was rebuilt for. Memory reading as VRAM is not a near miss — a host
// memory bar and a device memory bar are different facts about different things,
// and one of them is blue by convention while the other was blue by accident.
// They must not merely be far apart at a matched reading; their hue ranges must
// not overlap at all.
// hueArc is a ramp's hue range as an interval that starts at its first reading
// and sweeps forward. Hue is an angle, so a ramp crossing 0° (mem runs 341→6°)
// cannot be described by min and max of the raw numbers: those come out as 0-360,
// which reads as "covers every hue there is".
type hueArc struct{ start, sweep float64 }

func arcOf(name string) hueArc {
	_, _, h0 := OKLCHOf(Default.G(name, 0))
	lo, hi := 0.0, 0.0
	for v := 0; v <= 100; v++ {
		_, _, h := OKLCHOf(Default.G(name, v))
		d := h - h0
		for d > 180 {
			d -= 360
		}
		for d < -180 {
			d += 360
		}
		lo, hi = math.Min(lo, d), math.Max(hi, d)
	}
	return hueArc{start: h0 + lo, sweep: hi - lo}
}

// ahead reports how far b's start lies beyond a's arc, going forward.
func (a hueArc) ahead(b hueArc) float64 {
	d := b.start - a.start
	for d < 0 {
		d += 360
	}
	if d <= a.sweep {
		return 0
	}
	return d - a.sweep
}

// TestMemoryAndVRAMAreDifferentFamilies: these two were the pair the whole
// palette was rebuilt for. Memory reading as VRAM is not a near miss — a host
// memory bar and a device memory bar are different facts about different things,
// and one of them is blue by convention while the other was blue by accident.
// They must not merely be far apart at a matched reading; their hue ranges must
// not overlap at all.
func TestMemoryAndVRAMAreDifferentFamilies(t *testing.T) {
	v, m := arcOf("vram"), arcOf("mem")
	if v.ahead(m) == 0 || m.ahead(v) == 0 {
		t.Errorf("vram spans %.0f..%.0f° and mem spans %.0f..%.0f°: they share a hue range",
			v.start, v.start+v.sweep, m.start, m.start+m.sweep)
	}
}

// TestLabelsStayOutOfTheRamps: the label grey is text, not a measurement. A ramp
// whose cold end sits ΔE 6 from it makes column names read as values.
func TestLabelsStayOutOfTheRamps(t *testing.T) {
	label := Default.C("graph_text")
	for name := range Default.ramps {
		worst, at := math.Inf(1), 0
		for v := 0; v <= 100; v++ {
			if d := DeltaE(label, Default.G(name, v)); d < worst {
				worst, at = d, v
			}
		}
		if worst < 8 {
			t.Errorf("%s comes within ΔE %.1f of the label grey at %d%%", name, worst, at)
		}
	}
}

// TestAgentColoursStayOffTheRamps: an agent's colour answers "who", a ramp
// answers "how much", and the two are drawn on the same card. They are held to
// the same separation floor the ramps already use among themselves.
//
// This is the test that keeps two brands out of the measurement palette. Claude's
// orange is h39, inside power's old h38-70 arc, and cleared it by ΔE 6.3 — the
// same failure as the `held:` footer that used to be drawn in the util green.
// Power was moved to h62-94 to make room. Codex's blue is h256 against vram's
// h260-283 and clears it by 8.8, which passes but is the tightest pair in the
// palette: a future vram change that pushes its cold end toward h250 will fail
// here rather than quietly put a claim and a memory bar in one colour.
func TestAgentColoursStayOffTheRamps(t *testing.T) {
	s := NewSkin()
	names := []string{"util", "vram", "power", "host", "mem", "swap"}
	for _, kind := range []string{"claude-code", "codex"} {
		c := s.Agent(kind)
		worst, at, which := math.Inf(1), 0, ""
		for _, n := range names {
			for v := 0; v <= 100; v++ {
				if d := DeltaE(c, Default.G(n, v)); d < worst {
					worst, at, which = d, v, n
				}
			}
		}
		if worst < 8 {
			t.Errorf("%s (%s) comes within ΔE %.1f of %s at %d%%", kind, c.Hex(), worst, which, at)
		}
	}
}

// TestAgentColoursDiffer: two agents on one host must not read as one. This is
// the property the footer needs, since that is where several of them appear on
// one line.
func TestAgentColoursDiffer(t *testing.T) {
	s := NewSkin()
	if d := DeltaE(s.Agent("claude-code"), s.Agent("codex")); d < 20 {
		t.Errorf("claude-code and codex are ΔE %.1f apart", d)
	}
	// An agent ftop does not know is the label grey, and must be dimmer than
	// either brand so an unidentified claim never outshines a known one.
	unknown := s.Agent("something-else")
	for _, kind := range []string{"claude-code", "codex"} {
		Lk, _, _ := OKLCHOf(s.Agent(kind))
		Lu, _, _ := OKLCHOf(unknown)
		if Lu >= Lk {
			t.Errorf("unknown agent (L=%.2f) is not dimmer than %s (L=%.2f)", Lu, kind, Lk)
		}
	}
}

// TestTrackSurvivesTheBand: a card alternates its blocks onto Band(), and the
// unfilled half of every meter is drawn on whichever block it lands on. The track
// must therefore clear BAND, not just the page.
//
// It cleared only the page, and the difference is what a reader saw: the track sat
// ΔE 2.6 from the band, so on every second block the empty half of a bar was the
// same colour as the row behind it and the row read as a bar of the wrong length.
// Two floors, both at the palette's usual 8: far enough from the band to be a
// surface, and far enough from the page that an all-empty bar still reads as a bar
// rather than as blank.
func TestTrackSurvivesTheBand(t *testing.T) {
	tr, band, bg := Default.Track(), Default.Band(), Default.Bg()
	if d := DeltaE(band, tr); d < 8 {
		t.Errorf("track is ΔE %.1f from the band it is often drawn on", d)
	}
	if d := DeltaE(bg, tr); d < 8 {
		t.Errorf("track is ΔE %.1f from the page", d)
	}
	// And it may not go so far that a row of empty cells reads as text.
	if d := DeltaE(tr, Default.C("graph_text")); d < 6 {
		t.Errorf("track is ΔE %.1f from the label grey; empty cells will read as characters", d)
	}
}

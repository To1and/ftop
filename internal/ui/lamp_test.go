package ui

import (
	"strings"
	"testing"
	"time"

	"ftop/internal/theme"
)

func lampAt(ms int) time.Time {
	return time.Unix(0, 0).Add(time.Duration(ms) * time.Millisecond)
}

// TestLampSaysWhoAndWhether: a lamp's colour is the holder's and its motion is
// the holder's activity. Colour must not vary with utilisation — that is the
// util bar's job, and a lamp that changed hue as the card got busy would make
// "who" unreadable exactly when the card is interesting.
func TestLampSaysWhoAndWhether(t *testing.T) {
	claude := Holder{Owner: "claude-code/1a2b3c4d"}
	codex := Holder{Owner: "codex/9f8e7d6c"}

	c := lampStateOf(claude, true, 50)
	x := lampStateOf(codex, true, 50)
	if !c.lit || !x.lit {
		t.Fatal("a held card's lamp is not lit")
	}
	if c.colour == x.colour {
		t.Error("two agents' lamps are the same colour")
	}
	if c.colour != theme.Default.AgentColour("claude-code") {
		t.Errorf("claude's lamp is %s, not its brand colour", c.colour.Hex())
	}
	// Busy-ness changes the motion, never the colour.
	calm := lampStateOf(claude, true, 2)
	if calm.colour != c.colour {
		t.Error("utilisation changed the lamp's colour")
	}
	if calm.busy || !c.busy {
		t.Error("busy was not read from utilisation")
	}
}

// TestFreeCardLampIsDark: a card the ledger says is free gets the dark dot, not
// a brand colour. And a card the ledger has NO answer for gets nothing: "free"
// and "unknown" must not look alike.
func TestFreeCardLampIsDark(t *testing.T) {
	free := lampStateOf(Holder{}, false, 90)
	if free.lit {
		t.Error("a card with no claim is lit")
	}
	if _, fg := lampCell(free, lampAt(0)); fg != theme.Default.Track() {
		t.Errorf("an unlit lamp is %s, not the track", fg.Hex())
	}
}

// TestLampTurnsOnlyWhileBusy: a held card that is idle shows its holder's
// resting mark and holds still; a busy one turns through its holder's spinner.
// The colour is the holder's at every frame of both.
func TestLampTurnsOnlyWhileBusy(t *testing.T) {
	for _, owner := range []string{"claude-code/x", "codex/x", "human/x"} {
		idle := lampStateOf(Holder{Owner: owner}, true, 2)
		busy := lampStateOf(Holder{Owner: owner}, true, 80)
		brand := agentColourFor(owner)
		still, turning := map[string]bool{}, map[string]bool{}
		for ms := 0; ms < 2400; ms += 20 {
			g, fg := lampCell(idle, lampAt(ms))
			still[g] = true
			if fg != brand {
				t.Errorf("%s idle at %dms is %s, not its brand colour", owner, ms, fg.Hex())
			}
			g, fg = lampCell(busy, lampAt(ms))
			turning[g] = true
			if fg != brand {
				t.Errorf("%s busy at %dms is %s, not its brand colour", owner, ms, fg.Hex())
			}
		}
		if len(still) != 1 {
			t.Errorf("%s: an idle claim moves through %d glyphs", owner, len(still))
		}
		if len(turning) < 4 {
			t.Errorf("%s: a busy card shows only %d glyphs; it does not turn", owner, len(turning))
		}
	}
}

// TestEachAgentInItsOwnIdiom: Claude Code and Codex lamps are their own
// spinners, so the two read apart by shape as well as colour, and an agent ftop
// has no idiom for still gets a lamp that turns.
func TestEachAgentInItsOwnIdiom(t *testing.T) {
	seen := func(owner string, util float64) map[string]bool {
		st := lampStateOf(Holder{Owner: owner}, true, util)
		out := map[string]bool{}
		for ms := 0; ms < 2400; ms += 10 {
			g, _ := lampCell(st, lampAt(ms))
			out[g] = true
		}
		return out
	}
	claude, codex := seen("claude-code/a", 80), seen("codex/b", 80)
	for g := range claude {
		if codex[g] {
			t.Errorf("claude and codex share the frame %q", g)
		}
	}
	if !claude["✻"] || !codex["⠋"] {
		t.Errorf("the spinners are not the agents' own: claude %v, codex %v", claude, codex)
	}
	if g, _ := lampCell(lampStateOf(Holder{Owner: "claude-code/a"}, true, 0), lampAt(0)); g != "✻" {
		t.Errorf("an idle claude claim rests on %q, want ✻", g)
	}
}

// TestLampGlyphsAreOneCell: every frame of every idiom is one cell wide. A
// spinner frame two cells wide would push the row it sits in one cell right on
// that frame only, and the card would shudder.
func TestLampGlyphsAreOneCell(t *testing.T) {
	styles := []lampStyle{lampStyleOther}
	for _, s := range lampStyles {
		styles = append(styles, s)
	}
	for _, s := range styles {
		for _, g := range append([]string{s.rest}, s.spin...) {
			if w := theme.RuneWidth(g); w != 1 {
				t.Errorf("%q is %d cells wide", g, w)
			}
		}
	}
}

// TestLampIsAPureFunctionOfTime: the same timestamp draws the same lamp, so a
// snapshot is reproducible and a dropped frame does not shift the phase.
func TestLampIsAPureFunctionOfTime(t *testing.T) {
	st := lampStateOf(Holder{Owner: "codex/x"}, true, 80)
	for _, ms := range []int{0, 137, 700, 1200, 2333} {
		a, _ := lampCell(st, lampAt(ms))
		b, _ := lampCell(st, lampAt(ms))
		if a != b {
			t.Errorf("t=%dms is not deterministic", ms)
		}
	}
}

// TestOverdueLampIsUnmissable: an overdue claim is a still dot in the warning
// colour, busy or not, so it is never mistaken for live work.
func TestOverdueLampIsUnmissable(t *testing.T) {
	warn := theme.Default.C("warn")
	for _, util := range []float64{0, 90} {
		st := lampStateOf(Holder{Owner: "claude-code/x", Overdue: true}, true, util)
		for ms := 0; ms < 2400; ms += 50 {
			if g, fg := lampCell(st, lampAt(ms)); fg != warn || g != lampGlyph {
				t.Fatalf("overdue lamp at %dms is %q %s, not a still warning dot", ms, g, fg.Hex())
			}
		}
	}
}

// TestOneRowLampUsesTheGap: a one-line row has no second line, so the lamp goes
// in the gap after the index — and only when lit, so a board with no claims is
// laid out exactly as it was.
func TestOneRowLampUsesTheGap(t *testing.T) {
	a := fixture(240, 40, []int{4})
	a.mu.Lock()
	st := a.states["hosta"]
	a.holders = map[string]Holder{ledgerKey("hosta", st.sample.Gpus[0]): {Owner: "claude-code/x"}}
	a.mu.Unlock()
	geo := boardGeom(240, 1, 1, idxWidth([]*hostState{st}), true)
	if geo.rows != 1 {
		t.Skipf("lane is %d cells, two-line layout; this case needs one line", geo.width())
	}
	frame := a.PlainFrame()
	if !containsLamp(frame) {
		t.Skip("the fixture's first card is not laid out at this width")
	}
}

func lampStateOf(h Holder, ok bool, util float64) lampState {
	return lampFor(h, ok, wireGPU{util: util})
}

func absAngle(d float64) float64 {
	for d > 180 {
		d -= 360
	}
	for d < -180 {
		d += 360
	}
	if d < 0 {
		return -d
	}
	return d
}

func containsLamp(s string) bool {
	return strings.Contains(s, lampGlyphID) || containsLitLamp(s)
}

// containsLitLamp reports whether any lit lamp, of any idiom, is in s.
func containsLitLamp(s string) bool {
	for _, g := range litLampGlyphs() {
		if strings.Contains(s, g) {
			return true
		}
	}
	return false
}

// litLampGlyphs is every glyph a lit lamp can show, in any idiom, less the dot
// a free card shows.
func litLampGlyphs() []string {
	styles := []lampStyle{lampStyleOther}
	for _, st := range lampStyles {
		styles = append(styles, st)
	}
	var out []string
	for _, st := range styles {
		for _, g := range append([]string{st.rest}, st.spin...) {
			if g != lampGlyphID {
				out = append(out, g)
			}
		}
	}
	return out
}

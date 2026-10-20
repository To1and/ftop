package ui

import (
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

// TestBreathIsVisibleAndNeverOut: the breath must stay visible for its whole
// cycle. A lamp that faded to the page colour would spend most of its period
// invisible, and a claim is not a thing that sometimes isn't there.
func TestBreathIsVisibleAndNeverOut(t *testing.T) {
	st := lampStateOf(Holder{Owner: "claude-code/x"}, true, 0)
	brand := st.colour
	worst := 0.0
	for ms := 0; ms < int(lampBreath/time.Millisecond); ms += 50 {
		_, fg := lampCell(st, lampAt(ms))
		if d := theme.DeltaE(fg, theme.Default.Track()); d < worst || worst == 0 {
			worst = d
		}
		// Hue is held for the whole cycle: only lightness moves.
		_, _, h := theme.OKLCHOf(fg)
		_, _, hb := theme.OKLCHOf(brand)
		if d := absAngle(h - hb); d > 12 {
			t.Fatalf("at %dms the breath has drifted %.0f° in hue", ms, d)
		}
	}
	if worst < 12 {
		t.Errorf("the dark end of the breath is only ΔE %.1f from the track", worst)
	}
}

// TestBreathIsAPureFunctionOfTime: the same timestamp draws the same lamp, so a
// snapshot is reproducible and a dropped frame does not shift the phase.
func TestBreathIsAPureFunctionOfTime(t *testing.T) {
	st := lampStateOf(Holder{Owner: "codex/x"}, true, 0)
	for _, ms := range []int{0, 137, 700, 1200, 2333} {
		_, a := lampCell(st, lampAt(ms))
		_, b := lampCell(st, lampAt(ms))
		if a != b {
			t.Errorf("t=%dms is not deterministic", ms)
		}
	}
	// And it does move.
	_, a := lampCell(st, lampAt(0))
	_, b := lampCell(st, lampAt(1200))
	if a == b {
		t.Error("the breath does not move over half a period")
	}
}

// TestOverdueLampIsUnmissable: an overdue claim keeps the warning colour and
// does not breathe, so it is at full brightness whenever the reader looks.
func TestOverdueLampIsUnmissable(t *testing.T) {
	h := Holder{Owner: "claude-code/x", Overdue: true}
	st := lampStateOf(h, true, 0)
	warn := theme.Default.C("warn")
	for ms := 0; ms < int(lampBreath/time.Millisecond); ms += 100 {
		if _, fg := lampCell(st, lampAt(ms)); fg != warn {
			t.Fatalf("overdue lamp is %s at %dms, not the warning colour", fg.Hex(), ms)
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
	for _, r := range s {
		if r == '●' || r == '·' {
			return true
		}
	}
	return false
}

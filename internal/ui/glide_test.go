package ui

import (
	"strings"
	"testing"
	"time"

	"ftop/internal/theme"
	"ftop/internal/wire"
)

// glideFleet is a running board (Init has been called) with one two-card host
// at the given utilisation.
func glideFleet(t *testing.T, util float64) *App {
	t.Helper()
	a := fixture(200, 40, []int{2})
	a.Init()
	a.now = time.Unix(1000, 0)
	setUtil(a, util)
	a.fleetCanvas() // the first frame: every bar starts at its reading
	return a
}

func setUtil(a *App, util float64) {
	st := a.states["hosta"]
	s := st.sample
	s.Gpus = append([]wire.Gpu(nil), s.Gpus...)
	for i := range s.Gpus {
		u := util
		s.Gpus[i].Util = &u
	}
	a.applySample(sshxSample("hosta", s))
}

// cardMeter is GPU 0's utilisation meter on the card: its filled cells and the
// percentage written after it.
func cardMeter(t *testing.T, a *App) (filled int, pct string) {
	t.Helper()
	c := a.fleetCanvas()
	p := a.placed[0]
	for y := a.boardTop + p.y; y < a.boardTop+p.y+p.h; y++ {
		if c.at(p.x+2, y).Text != "0" {
			continue
		}
		track := a.skin.Theme.Track()
		var b strings.Builder
		for x := p.x + 3; x < p.x+p.w-1; x++ {
			cl := c.at(x, y)
			if cl.Text == "■" && cl.FG != track {
				filled++
			}
			b.WriteString(cl.Text)
		}
		f := strings.Fields(b.String())
		for _, w := range f {
			if strings.HasSuffix(w, "%") {
				return filled, w
			}
		}
		t.Fatalf("no percentage on GPU 0's row: %q", b.String())
	}
	t.Fatal("no GPU 0 row")
	return 0, ""
}

// TestBarsGlideNumbersDoNot: a new reading moves the bar over glideFor and
// changes the number at once.
func TestBarsGlideNumbersDoNot(t *testing.T) {
	a := glideFleet(t, 10)
	before, _ := cardMeter(t, a)
	setUtil(a, 90)
	a.fleetCanvas() // the frame the reading arrives in starts the move
	a.now = a.now.Add(glideFor / 8)
	mid, pct := cardMeter(t, a)
	if pct != "90%" {
		t.Errorf("mid-glide the number reads %s; it is the reading, 90%%", pct)
	}
	a.now = a.now.Add(glideFor)
	after, _ := cardMeter(t, a)
	still := fixture(200, 40, []int{2})
	setUtil(still, 90)
	if want, _ := cardMeter(t, still); after != want {
		t.Errorf("after the glide the bar has %d cells, want the reading's %d", after, want)
	}
	if !(before < mid && mid < after) {
		t.Errorf("mid-glide the bar has %d cells, want strictly between %d and %d", mid, before, after)
	}
}

// TestSnapshotsDoNotGlide: before the program runs (-once, the demo, tests) a
// frame draws every bar at its reading.
func TestSnapshotsDoNotGlide(t *testing.T) {
	a := fixture(200, 40, []int{2})
	a.now = time.Unix(1000, 0)
	setUtil(a, 10)
	a.fleetCanvas()
	setUtil(a, 90)
	got, _ := cardMeter(t, a)
	b := fixture(200, 40, []int{2})
	setUtil(b, 90)
	want, _ := cardMeter(t, b)
	if got != want {
		t.Errorf("a snapshot drew %d cells for 90%%, a fresh board %d", got, want)
	}
}

// TestASampleHurriesTheClock: a sample landing on a board at rest brings the tick
// up to the frame rate at once, and the one-second chain it replaces dies at its
// next tick instead of running alongside.
func TestASampleHurriesTheClock(t *testing.T) {
	a := fixture(200, 40, []int{2})
	a.changed.Store(0)
	a.Init()
	if a.chainRate != rateRest {
		t.Fatal("a board at rest starts faster than once a second")
	}
	old := a.tickGen
	st := a.states["hosta"]
	if _, cmd := a.Update(sshxSample("hosta", st.sample)); cmd == nil {
		t.Fatal("a sample on a resting board did not hurry the clock")
	}
	if a.chainRate != rateFrame || a.tickGen == old {
		t.Fatal("the hurried chain is not the live one")
	}
	if _, cmd := a.Update(tickMsg{at: time.Now(), gen: old}); cmd != nil {
		t.Error("the replaced one-second chain scheduled another tick")
	}
	if _, cmd := a.Update(sshxSample("hosta", st.sample)); cmd != nil {
		t.Error("a second sample started a second fast chain")
	}
}

// TestHostViewStaysSlow: a host view has no lamps and no gliding bars, so a lamp
// turning on the board behind it does not keep it fast.
func TestHostViewStaysSlow(t *testing.T) {
	a := fixture(200, 40, []int{2})
	a.changed.Store(0)
	st := a.states["hosta"]
	a.Update(LedgerMsg{At: time.Now(), Holders: map[string]Holder{ledgerKey("hosta", st.sample.Gpus[1]): {Owner: "codex/x"}}})
	if got := a.tickPeriod(); got >= time.Second {
		t.Fatalf("the board with a turning lamp repaints every %v", got)
	}
	a.detail = true
	if got := a.tickPeriod(); got != time.Second {
		t.Errorf("a host view repaints every %v, want one second", got)
	}
	a.help = true
	if got := a.tickPeriod(); got >= time.Second {
		t.Errorf("the help over the board repaints every %v; the board under it is still turning", got)
	}
}

// TestGraphsAreDrawnOncePerSample: a card's graph is reused frame to frame and
// drawn again when a sample lands.
func TestGraphsAreDrawnOncePerSample(t *testing.T) {
	a := fixture(200, 40, []int{2})
	st := a.states["hosta"]
	k := gpuKey(st.sample.Gpus[0])
	a.fleetCanvas()
	first := st.graphs[k]
	if first == nil {
		t.Fatal("no graph drawn for GPU 0")
	}
	a.fleetCanvas()
	if st.graphs[k] != first {
		t.Error("the graph was drawn again with no new sample")
	}
	setUtil(a, 50)
	a.fleetCanvas()
	if st.graphs[k] == first {
		t.Error("a new sample did not redraw the graph")
	}
}

// TestASampleWaitsForTheTick: while the clock is fast, the frame for a sample's
// arrival is the one already on screen; the next tick draws the sample.
func TestASampleWaitsForTheTick(t *testing.T) {
	a := glideFleet(t, 10)
	a.width, a.height = 200, 40
	a.Update(tickMsg{at: time.Now(), gen: a.tickGen})
	before := a.View().Content
	st := a.states["hosta"]
	s := st.sample
	s.Gpus = append([]wire.Gpu(nil), s.Gpus...)
	u := 95.0
	s.Gpus[0].Util = &u
	a.Update(sshxSample("hosta", s))
	if a.chainRate != rateFrame {
		t.Fatal("a sample did not bring the clock up to the frame rate")
	}
	if got := a.View().Content; got != before {
		t.Error("a sample was drawn on arrival rather than by the next tick")
	}
	a.Update(tickMsg{at: time.Now().Add(glideFor), gen: a.tickGen})
	if got := a.View().Content; got == before {
		t.Error("the next tick did not draw the sample")
	}
}

// TestAGlideMovesOnlyTheEnd: from one frame of a glide to the next, the cells
// the bar already covered keep their colour; only its end moves.
func TestAGlideMovesOnlyTheEnd(t *testing.T) {
	a := glideFleet(t, 10)
	setUtil(a, 90)
	a.fleetCanvas()
	row := func() []theme.RGB {
		c := a.fleetCanvas()
		p := a.placed[0]
		track := a.skin.Theme.Track()
		for y := a.boardTop + p.y; y < a.boardTop+p.y+p.h; y++ {
			if c.at(p.x+2, y).Text != "0" {
				continue
			}
			var out []theme.RGB
			for x := p.x + 3; x < p.x+p.w-1; x++ {
				if cl := c.at(x, y); cl.Text == "■" && cl.FG != track {
					out = append(out, cl.FG)
				} else if len(out) > 0 {
					break
				}
			}
			return out
		}
		t.Fatal("no GPU 0 row")
		return nil
	}
	a.now = a.now.Add(glideFor / 6)
	first := row()
	a.now = a.now.Add(glideFor / 6)
	second := row()
	if len(second) <= len(first) {
		t.Fatalf("the bar did not grow between frames: %d then %d cells", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("cell %d changed colour mid-glide: %s then %s", i, first[i].Hex(), second[i].Hex())
		}
	}
}

// TestSmallChangesDoNotGlide: a change under glideMin takes the reading at once
// and does not keep the clock at the frame rate.
func TestSmallChangesDoNotGlide(t *testing.T) {
	a := glideFleet(t, 50)
	a.glideUntil.Store(0)
	setUtil(a, 51)
	a.fleetCanvas()
	if a.glideUntil.Load() != 0 {
		t.Error("a one-point change started a glide")
	}
	if g := a.glides[barKey("hosta", a.states["hosta"].sample.Gpus[0], "util")]; g == nil || g.at(a.now) != 51 {
		t.Errorf("a one-point change is not drawn at once: %+v", g)
	}
	setUtil(a, 80)
	a.fleetCanvas()
	if a.glideUntil.Load() == 0 {
		t.Error("a thirty-point change did not glide")
	}
}

// TestASampleHurriesPastTheLampsStep: with a lamp turning the clock runs on the
// lamp's step; a sample that starts a glide takes it to the frame rate at once
// rather than leaving the glide drawn at the lamp's 100 ms.
func TestASampleHurriesPastTheLampsStep(t *testing.T) {
	a := fixture(200, 40, []int{2})
	a.changed.Store(0)
	a.glideUntil.Store(0)
	a.Init()
	st := a.states["hosta"]
	a.Update(LedgerMsg{At: time.Now(), Holders: map[string]Holder{ledgerKey("hosta", st.sample.Gpus[1]): {Owner: "claude-code/x"}}})
	if a.chainRate != rateSpin {
		t.Fatalf("a turning lamp left the clock at rate %d", a.chainRate)
	}
	if _, cmd := a.Update(sshxSample("hosta", st.sample)); cmd == nil || a.chainRate != rateFrame {
		t.Errorf("a sample under a turning lamp left the clock at rate %d", a.chainRate)
	}
}

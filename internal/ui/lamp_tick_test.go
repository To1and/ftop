package ui

import (
	"testing"
	"time"
)

// TestTickFollowsTheHolders: the board repaints at a lamp's step while a held
// card is busy, once a second while its holder only sits on it, and the decision
// has to be readable without the render lock.
//
// It is tested through Update rather than by poking the state, because the step
// is derived: what matters is that a ledger read with a busy holder in it changes
// the tick, and a ledger read that empties the board changes it back.
func TestTickFollowsTheHolders(t *testing.T) {
	a := fixture(200, 40, []int{2})
	a.changed.Store(0) // no bar is moving: the fixture's sample is long settled
	if got := a.tickPeriod(); got != time.Second {
		t.Fatalf("an empty board repaints every %v", got)
	}

	// GPU 0 reads 0% and GPU 1 10%: an idle claim and a busy one.
	st := a.states["hosta"]
	idle := map[string]Holder{ledgerKey("hosta", st.sample.Gpus[0]): {Owner: "claude-code/x"}}
	a.Update(LedgerMsg{Holders: idle, At: time.Now()})
	if got := a.tickPeriod(); got != time.Second {
		t.Errorf("an idle claim, which holds still, repaints every %v", got)
	}

	busy := map[string]Holder{ledgerKey("hosta", st.sample.Gpus[1]): {Owner: "claude-code/x"}}
	a.Update(LedgerMsg{Holders: busy, At: time.Now()})
	step := lampStyles["claude-code"].step
	if got := a.tickPeriod(); got <= 0 || got > step+step/4 {
		t.Errorf("a turning lamp repaints after %v, want within one step (%v)", got, step)
	}

	a.Update(LedgerMsg{Holders: map[string]Holder{}, At: time.Now()})
	if got := a.tickPeriod(); got != time.Second {
		t.Errorf("after release the board repaints every %v", got)
	}
}

// TestTwoSpinnersShareOneClock: Claude Code and Codex lamps step together, and a
// lamp of another holder on a coarser step does not make the clock wake more.
func TestTwoSpinnersShareOneClock(t *testing.T) {
	a := fixture(200, 40, []int{4})
	a.changed.Store(0)
	st := a.states["hosta"]
	a.Update(LedgerMsg{At: time.Now(), Holders: map[string]Holder{
		ledgerKey("hosta", st.sample.Gpus[1]): {Owner: "claude-code/x"},
		ledgerKey("hosta", st.sample.Gpus[2]): {Owner: "codex/y"},
		ledgerKey("hosta", st.sample.Gpus[3]): {Owner: "human/z"},
	}})
	if got := time.Duration(a.spinStep.Load()); got != 100*time.Millisecond {
		t.Errorf("the shared step is %v, want 100ms", got)
	}
}

// TestLampRepaintCostIsSmall: at the frame rate the board must stay well inside a
// frame budget. It is a guard on the number, not a benchmark.
func TestLampRepaintCostIsSmall(t *testing.T) {
	a := fixture(200, 58, []int{1, 2, 4, 8, 2, 4, 1, 8, 4, 2, 1, 2, 4})
	a.now = time.Now()
	frameStart := time.Now()
	for i := 0; i < 8; i++ {
		_ = a.PlainFrame()
	}
	per := time.Since(frameStart) / 8
	// A second of lamp frames may not cost a whole core's worth of a second: the
	// bound is generous (25%) only so the test is not flaky on a loaded machine.
	if budget := time.Second / 4; per*time.Duration(frameRate) > budget {
		t.Errorf("%d frames/s cost %v of CPU, over the %v budget", frameRate, per*time.Duration(frameRate), budget)
	}
	t.Logf("%.2f ms/frame, %.1f%% of a core at %d fps", float64(per.Microseconds())/1000,
		100*float64(per*time.Duration(frameRate))/float64(time.Second), frameRate)
}

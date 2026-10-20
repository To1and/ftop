package ui

import (
	"testing"
	"time"
)

// TestTickFollowsTheHolders: the board repaints at the lamp's rate only while a
// lamp is lit, and the decision has to be readable without the render lock.
//
// It is tested through Update rather than by poking the flag, because the flag is
// derived state: what matters is that a ledger read with holders in it changes the
// tick, and a ledger read that empties the board changes it back.
func TestTickFollowsTheHolders(t *testing.T) {
	a := fixture(200, 40, []int{2})
	if got := a.tickPeriod(); got != time.Second {
		t.Fatalf("an empty board repaints every %v", got)
	}

	st := a.states["hosta"]
	held := map[string]Holder{ledgerKey("hosta", st.sample.Gpus[0]): {Owner: "claude-code/x"}}
	a.Update(LedgerMsg{Holders: held, At: time.Now()})
	if got := a.tickPeriod(); got != time.Second/lampFPS {
		t.Errorf("a held board repaints every %v, want %v", got, time.Second/lampFPS)
	}

	a.Update(LedgerMsg{Holders: map[string]Holder{}, At: time.Now()})
	if got := a.tickPeriod(); got != time.Second {
		t.Errorf("after release the board repaints every %v", got)
	}
}

// TestLampRepaintCostIsSmall: at the lamp's rate the board must stay well inside a
// frame budget. This is why the rate is 8 and not 30 — it is a guard on the number,
// not a benchmark.
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
	if budget := time.Second / 4; per*time.Duration(lampFPS) > budget {
		t.Errorf("%d frames/s cost %v of CPU, over the %v budget", lampFPS, per*time.Duration(lampFPS), budget)
	}
	t.Logf("%.2f ms/frame, %.1f%% of a core at %d fps", float64(per.Microseconds())/1000,
		100*float64(per*time.Duration(lampFPS))/float64(time.Second), lampFPS)
}

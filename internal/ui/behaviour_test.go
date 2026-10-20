package ui

import (
	"strings"
	"testing"

	"ftop/internal/wire"
)

// TestUnknownGaugeIsNotZero is the invariant behind the pointer fields: a driver
// that refused a query must render as "unknown", never as an idle card.
func TestUnknownGaugeIsNotZero(t *testing.T) {
	a := fixture(200, 60, []int{1})
	a.mu.Lock()
	st := a.states["hosta"]
	st.sample.Gpus[0].Util = nil
	st.sample.Gpus[0].MemUsed = nil
	st.sample.Gpus[0].MemTotal = nil
	a.mu.Unlock()

	frame := a.PlainFrame()
	if !strings.Contains(frame, "—") {
		t.Error("a device with no reading does not show dashes")
	}
	// The mean must exclude the unknown card rather than average in a zero.
	if got := a.states["hosta"].sample.UtilAvg(); got != 0 {
		t.Errorf("UtilAvg over zero known cards = %v, want 0", got)
	}
}

// TestUtilAvgSkipsUnknown: one known card at 80% beside one unreadable card is a
// mean of 80, not 40.
func TestUtilAvgSkipsUnknown(t *testing.T) {
	u := 80.0
	s := wire.Sample{Gpus: []wire.Gpu{{Util: &u}, {Util: nil}}}
	if got := s.UtilAvg(); got != 80 {
		t.Errorf("UtilAvg = %v, want 80 (unknown excluded)", got)
	}
	if got := s.BusyGPUs(); got != 1 {
		t.Errorf("BusyGPUs = %v, want 1", got)
	}
	if got := s.KnownGPUs(); got != 1 {
		t.Errorf("KnownGPUs = %v, want 1", got)
	}
}

// TestHelpScreenDocumentsRealKeys: the status line advertises "?", so the help must
// open and must not claim keys the app does not handle.
func TestHelpScreenDocumentsRealKeys(t *testing.T) {
	a := fixture(120, 30, []int{2})

	_, _ = a.Update(keyMsg("?"))
	if !a.help {
		t.Fatal("? did not open the help screen")
	}
	help := a.helpCanvas().Plain()
	for _, want := range []string{"quit", "open", "util", "history"} {
		if !strings.Contains(help, want) {
			t.Errorf("help screen does not mention %q", want)
		}
	}

	_, _ = a.Update(keyMsg("?"))
	if a.help {
		t.Error("? did not close the help screen")
	}
}

// TestEnterOpensTheHighlightedHost: Enter must open the card under the highlight,
// wherever arrow keys have moved it, not whatever host an index points at.
func TestEnterOpensTheHighlightedHost(t *testing.T) {
	a := New(Options{Hosts: []string{"aaa-idle", "zzz-busy"}, GraphRows: 1})
	a.width, a.height = 200, 40

	idle := 0.0
	busy := 95.0
	a.Deliver(sshxSample("aaa-idle", wire.Sample{
		Gpus: []wire.Gpu{{Index: 0, Util: &idle, Name: "RTX 4090"}},
	}))
	a.Deliver(sshxSample("zzz-busy", wire.Sample{
		Gpus: []wire.Gpu{{Index: 0, Util: &busy, Name: "RTX 4090"}},
	}))
	_ = a.fleetCanvas() // arrow keys navigate the drawn geometry

	_, _ = a.Update(keyMsg("right"))
	_, _ = a.Update(keyMsg("enter"))
	if !a.detail {
		t.Fatal("enter did not open detail")
	}
	if got := a.order[a.cursor]; got != "zzz-busy" {
		t.Errorf("detail opened %q, want the highlighted host \"zzz-busy\"", got)
	}
}

// TestDetailScrollReachesLaterDevices: a 16-GPU host cannot fit, so the view must
// be scrollable and must say when more is below.
func TestDetailScrollReachesLaterDevices(t *testing.T) {
	n := 16
	a := fixture(160, 24, []int{n})
	a.detail = true
	a.cursor = 0

	frame := a.detailCanvas().Plain()
	if !strings.Contains(frame, "more") {
		t.Error("a clipped detail view does not indicate there is more")
	}

	a.detailScroll = 1000 // past the end; the view clamps to the real content height
	scrolled := a.detailCanvas().Plain()
	if scrolled == frame {
		t.Error("scrolling changed nothing")
	}
	if !strings.Contains(scrolled, "15 · RTX 4090") {
		t.Error("scrolling did not reach the last GPU")
	}
}

// TestConfiguredColumnsAreACeiling: asking for one column on a wide terminal must
// stack every card in a single lane.
func TestConfiguredColumnsAreACeiling(t *testing.T) {
	a := New(Options{Hosts: []string{"h1", "h2", "h3"}, GraphRows: 1, MaxCols: 1})
	a.width, a.height = 240, 60
	frame := a.PlainFrame()
	tops := 0
	for _, line := range strings.Split(frame, "\n") {
		// A box's top border carries its left label: ╭─┤ name ├.
		if n := strings.Count(line, "╭─┤"); n > 1 {
			t.Errorf("two boxes side by side with columns = 1: %q", line)
		}
		if strings.HasPrefix(line, "╭─┤") {
			tops++
		}
	}
	// Three cards; the fleet header has no left label.
	if tops != 3 {
		t.Errorf("%d card tops, want 3", tops)
	}
}

package ui

import (
	"math"
	"strings"
	"testing"

	"ftop/internal/wire"
)

// Regression tests for review round 2. Each reproduces a finding against the
// code as it was, and fails if the fix is reverted.

func gpus(n int, util *float64) []wire.Gpu {
	out := make([]wire.Gpu, n)
	for i := range out {
		out[i] = wire.Gpu{Index: i, Name: "RTX 4090", Util: util}
	}
	return out
}

// A sample that omits devices must not shrink the card or re-sort the board.
func TestMissingDevicesKeepTheirSlot(t *testing.T) {
	a := New(Options{Hosts: []string{"hosta", "hostb", "hostc"}, GraphRows: 1})
	a.width, a.height = 160, 40
	u := 30.0
	for i, n := range []int{8, 4, 2} {
		a.Deliver(sshxSample(a.order[i], wire.Sample{Gpus: gpus(n, &u)}))
	}
	before := strings.Join(a.sortedNames(), ",")
	a.Deliver(sshxSample("hosta", wire.Sample{Gpus: gpus(1, &u)}))
	if got := strings.Join(a.sortedNames(), ","); got != before {
		t.Errorf("a partial sample re-sorted the board: %s -> %s", before, got)
	}
	st := a.states["hosta"]
	if len(st.sample.Gpus) != 8 {
		t.Fatalf("card shrank to %d devices", len(st.sample.Gpus))
	}
	if st.sample.Gpus[5].Util != nil {
		t.Error("an omitted device kept a stale reading instead of becoming unknown")
	}
	// A new session (the host failed and came back) starts a fresh inventory.
	a.Deliver(sshxErr("hosta", "connection timed out"))
	a.Deliver(sshxSample("hosta", wire.Sample{Gpus: gpus(1, &u)}))
	if n := len(a.states["hosta"].sample.Gpus); n != 1 {
		t.Errorf("inventory survived a reconnect: %d devices", n)
	}
}

// Unknown samples are gaps in the history, so the graph keeps moving.
func TestUnknownSamplesAdvanceHistory(t *testing.T) {
	a := New(Options{Hosts: []string{"h"}, GraphRows: 1})
	full := 100.0
	a.Deliver(sshxSample("h", wire.Sample{Gpus: gpus(1, &full)}))
	for i := 0; i < 60; i++ {
		a.Deliver(sshxSample("h", wire.Sample{Gpus: gpus(1, nil)}))
	}
	h := a.states["h"].gpuHist["#0"]
	if len(h) != 61 || !math.IsNaN(h[60]) {
		t.Errorf("history did not advance through unknown samples: len %d, last %v", len(h), h[len(h)-1])
	}
	if _, err := a.ExportJSON(); err != nil {
		t.Errorf("snapshot with gaps does not serialise: %v", err)
	}
}

// A fleet with no readable card shows "—" for its mean, not 0%; a power
// reading above the cap is shown as measured.
func TestHeaderAggregatesAreHonest(t *testing.T) {
	a := New(Options{Hosts: []string{"h"}, GraphRows: 1})
	a.width, a.height = 180, 30
	p, capw := 1200.0, 1000.0
	g := wire.Gpu{Index: 0, Name: "RTX 4090", Power: &p, PowerCap: &capw}
	a.Deliver(sshxSample("h", wire.Sample{Gpus: []wire.Gpu{g}}))
	frame := strings.Split(a.PlainFrame(), "\n")
	head := strings.Join(frame[:a.boardTop], "\n")
	if strings.Contains(head, "util") && strings.Contains(head, " 0%") {
		t.Errorf("unknown mean shown as 0%%: %q", head)
	}
	if !strings.Contains(head, "1.2/1.0kW") {
		t.Errorf("above-cap power was altered: %q", head)
	}
}

// Optional readings vanishing (power caps going nil) must not move the board.
func TestBoardDoesNotMoveWhenReadingsVanish(t *testing.T) {
	counts := []int{8, 8, 4, 4, 4, 2, 2, 1, 1}
	feed := func(a *App, withPower bool) {
		for i, n := range counts {
			var gs []wire.Gpu
			for j := 0; j < n; j++ {
				u, p, c := 50.0, 200.0, 450.0
				g := wire.Gpu{Index: j, Name: "RTX 4090", Util: &u}
				if withPower {
					g.Power, g.PowerCap = &p, &c
				}
				gs = append(gs, g)
			}
			a.Deliver(sshxSample(a.order[i], wire.Sample{Gpus: gs}))
		}
	}
	top := func(a *App) int {
		for y, line := range strings.Split(a.PlainFrame(), "\n") {
			if strings.Contains(line, "util") {
				return y
			}
		}
		return -1
	}
	for w := 40; w <= 300; w++ {
		a := fixture(w, 60, counts)
		feed(a, true)
		want := top(a)
		feed(a, false)
		if got := top(a); got != want {
			t.Fatalf("width %d: legend row moved %d -> %d when power readings vanished", w, want, got)
		}
	}
}

// TestHelpStaysInsideItsBox: the help panel is a closed box at every size, and
// its text stays between its own two strokes. It sits over the board, so the
// check is the panel's box rather than the whole frame.
func TestHelpStaysInsideItsBox(t *testing.T) {
	for _, sz := range [][2]int{{40, 12}, {60, 16}, {80, 20}, {120, 30}} {
		a := fixture(sz[0], sz[1], []int{1})
		a.help = true
		rows := make([][]rune, 0, sz[1])
		for _, line := range strings.Split(a.helpCanvas().Plain(), "\n") {
			rows = append(rows, []rune(line))
		}
		top := -1
		for y, r := range rows {
			// In runes: the border and the title are both multi-byte.
			for x := 0; x+1 < len(r); x++ {
				if r[x] == '╭' && strings.HasPrefix(string(r[x:]), "╭─┤ftop help├") {
					top = y
					break
				}
			}
		}
		if top < 0 {
			t.Fatalf("%dx%d: no help panel", sz[0], sz[1])
		}
		// The panel's strokes: the first and last │ on its first content row.
		lo, hi := -1, -1
		for _, r := range rows[top+1:] {
			first, last := -1, -1
			for x, c := range r {
				if c == '│' {
					if first < 0 {
						first = x
					}
					last = x
				}
			}
			if first >= 0 && last > first {
				lo, hi = first, last
				break
			}
		}
		if lo < 0 {
			t.Fatalf("%dx%d: the panel has no upright strokes", sz[0], sz[1])
		}
		for y := top + 1; y < len(rows); y++ {
			r := rows[y]
			if len(r) <= hi || r[lo] != '│' {
				break // below the panel
			}
			if r[hi] != '│' {
				t.Errorf("%dx%d line %d: the panel's right stroke is missing: %q", sz[0], sz[1], y, string(r))
				continue
			}
			if strings.ContainsRune(string(r[lo+1:hi]), '│') {
				t.Errorf("%dx%d line %d: text crosses the panel's stroke: %q", sz[0], sz[1], y, string(r))
			}
		}
	}
}

// A stale, empty process list's message fits a narrow detail box.
func TestStaleProcessNoteFitsNarrowDetail(t *testing.T) {
	for w := 40; w <= 50; w++ {
		a := fixture(w, 16, []int{1})
		a.mu.Lock()
		a.states["hosta"].sample.Gpus[0].ProcsStale = true
		a.detail = true
		frame := a.detailCanvas().Plain()
		a.mu.Unlock()
		for i, line := range strings.Split(frame, "\n") {
			r := []rune(strings.TrimRight(line, " "))
			if len(r) > 0 && r[0] == '│' && r[len(r)-1] != '│' {
				t.Errorf("width %d line %d: %q", w, i, line)
			}
		}
	}
}

// History is long enough for the widest detail graph.
func TestHistoryCoversWideDetailGraphs(t *testing.T) {
	a := New(Options{Hosts: []string{"h"}, GraphRows: 1})
	u := 50.0
	for i := 0; i < 1000; i++ {
		a.Deliver(sshxSample("h", wire.Sample{Gpus: gpus(1, &u)}))
	}
	if n := len(a.states["h"].gpuHist["#0"]); n != historyCap {
		t.Errorf("history length %d, want the cap %d", n, historyCap)
	}
	// A one-column detail on a 300-column terminal draws ~240 cells = 480 samples.
	if historyCap < 2*240 {
		t.Errorf("historyCap %d cannot fill a 240-cell detail graph", historyCap)
	}
}

// Once every host has answered, the default highlight is pinned to its host: a
// host failing afterwards does not slide it onto another machine.
func TestDefaultFocusPinsOnceSettled(t *testing.T) {
	a := New(Options{Hosts: []string{"big", "small"}, GraphRows: 1})
	a.width, a.height = 200, 40
	u := 10.0
	a.Deliver(sshxSample("small", wire.Sample{Gpus: gpus(1, &u)}))
	_ = a.fleetCanvas()
	a.Deliver(sshxSample("big", wire.Sample{Gpus: gpus(8, &u)}))
	_ = a.fleetCanvas()
	if a.focused() != "big" {
		t.Fatalf("settled default focus = %q, want the top-left card", a.focused())
	}
	a.Deliver(sshxErr("big", "connection timed out"))
	_ = a.fleetCanvas()
	if a.focused() != "big" {
		t.Errorf("highlight left its host when the host failed: now on %q", a.focused())
	}
}

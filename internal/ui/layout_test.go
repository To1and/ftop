package ui

import (
	"strings"
	"testing"
	"time"

	"ftop/internal/theme"
	"ftop/internal/wire"
)

// fixture builds an app with N hosts of varying GPU counts.
func fixture(width, height int, gpuCounts []int) *App {
	hosts := make([]string, len(gpuCounts))
	for i := range gpuCounts {
		hosts[i] = "host" + string(rune('a'+i))
	}
	a := New(Options{Hosts: hosts, GraphRows: 1})
	a.width, a.height = width, height
	for i, n := range gpuCounts {
		s := wire.Sample{}
		s.Cores = 64
		for j := 0; j < n; j++ {
			u, used, total := float64(10*(j%9)), uint64(8000), uint64(24564)
			g := wire.Gpu{Index: j, Name: "NVIDIA GeForce RTX 4090", Util: &u, MemUsed: &used, MemTotal: &total}
			temp := 45.0
			pw := 120.0
			cap := 450.0
			clk := 2500.0
			g.Temp, g.Power, g.PowerCap, g.ClockSM = &temp, &pw, &cap, &clk
			s.Gpus = append(s.Gpus, g)
		}
		a.Deliver(sampleMsg(hosts[i], s))
	}
	return a
}

func sampleMsg(host string, s wire.Sample) any {
	return sshxSample(host, s)
}

// TestNoLineExceedsWidth is the invariant every layout bug violated: a line wider
// than the canvas is a line whose right-hand content is simply gone.
func TestNoLineExceedsWidth(t *testing.T) {
	for _, width := range []int{60, 80, 100, 120, 160, 200, 240} {
		a := fixture(width, 60, []int{1, 2, 4, 8, 2, 4})
		frame := a.PlainFrame()
		for i, line := range strings.Split(frame, "\n") {
			if w := theme.RuneWidth(line); w > width {
				t.Errorf("width %d: line %d is %d cells wide", width, i, w)
			}
		}
	}
}

// TestHeightIsRespected: a frame taller than the terminal scrolls the status line
// off and leaves the alt screen dirty.
func TestHeightIsRespected(t *testing.T) {
	for _, h := range []int{20, 30, 40, 58, 80} {
		a := fixture(200, h, []int{1, 2, 4, 8, 2, 4})
		lines := strings.Count(a.PlainFrame(), "\n") + 1
		if lines > h {
			t.Errorf("height %d produced %d lines", h, lines)
		}
	}
}

// TestEveryGpuGetsTwoBars: the requirement is two bars per GPU, utilisation and
// VRAM, each with its percentage. Rows are one line per GPU, so every GPU row
// must carry exactly two separate meter runs and two percentages.
//
// Counting a glyph run does not establish that, and it silently rotted when the
// meter glyph changed from █ to ■, so this counts runs per row.
func TestEveryGpuGetsTwoBars(t *testing.T) {
	a := fixture(160, 40, []int{4})
	rows := 0
	frame := a.PlainFrame()
	for y, line := range strings.Split(frame, "\n") {
		if y < a.boardTop {
			continue // the header's fleet meters
		}
		runs, in := 0, false
		for _, r := range line {
			isMeter := r == '■' || r == '░'
			if isMeter && !in {
				runs++
			}
			in = isMeter
		}
		if runs == 0 {
			continue
		}
		rows++
		if runs != 2 {
			t.Errorf("GPU row has %d meter runs, want 2: %q", runs, line)
		}
		if n := strings.Count(line, "%"); n != 2 {
			t.Errorf("GPU row has %d percentages, want 2: %q", n, line)
		}
	}
	if rows != 4 {
		t.Errorf("%d GPU rows, want 4", rows)
	}
}

// TestRowPlanFitsItsLane: a row plan wider than the card is a row whose right
// end is drawn over the border, at every width a lane can take.
func TestRowPlanFitsItsLane(t *testing.T) {
	for inner := 1 + 2*2 + 2*(minBar+1+pctW); inner <= 200; inner++ {
		for _, graph := range []bool{true, false} {
			g := planRow(inner, 1, graph)
			if g.width() > inner {
				t.Fatalf("inner %d graph=%v: plan is %d wide", inner, graph, g.width())
			}
			if g.meterW > maxBar {
				t.Fatalf("inner %d: bar %d exceeds the cap %d", inner, g.meterW, maxBar)
			}
		}
	}
}

// TestBarsStayShortOnWideTerminals: the bars stop at maxBar; the width goes to
// more lanes and the history graph instead of two long slabs.
func TestBarsStayShortOnWideTerminals(t *testing.T) {
	a := fixture(300, 60, []int{8, 8, 4, 2})
	frame := a.PlainFrame()
	longest := 0
	for y, line := range strings.Split(frame, "\n") {
		if y < a.boardTop {
			continue // the header's fleet meters are a separate, fixed size
		}
		run := 0
		for _, r := range line {
			if r == '■' || r == '░' {
				run++
				longest = max(longest, run)
			} else {
				run = 0
			}
		}
	}
	if longest > maxBar {
		t.Errorf("a %d-cell bar at 300 columns; bars are capped at %d", longest, maxBar)
	}
	if a.lanes < 2 {
		t.Errorf("300 columns drew %d lane(s); the width is going unused", a.lanes)
	}
}

// TestOrderIsStableUnderLoad: the board must not reshuffle as load moves between
// hosts. Only a host coming online or dropping off may change the order.
func TestOrderIsStableUnderLoad(t *testing.T) {
	a := New(Options{Hosts: []string{"b-host", "a-host", "c-host"}, GraphRows: 1})
	a.width, a.height = 200, 40
	feed := func(host string, util float64) {
		u := util
		a.Deliver(sshxSample(host, wire.Sample{Gpus: []wire.Gpu{{Index: 0, Util: &u, Name: "RTX 4090"}}}))
	}
	for _, h := range []string{"a-host", "b-host", "c-host"} {
		feed(h, 10)
	}
	want := strings.Join(a.sortedNames(), ",")
	for round := 0; round < 20; round++ {
		feed("a-host", float64(round*5%100))
		feed("b-host", float64(100-round*5%100))
		feed("c-host", float64(round*37%100))
		if got := strings.Join(a.sortedNames(), ","); got != want {
			t.Fatalf("round %d: order changed with load: %s -> %s", round, want, got)
		}
	}

	a.Deliver(sshxErr("a-host", "connection timed out"))
	if got := a.sortedNames(); got[len(got)-1] != "a-host" {
		t.Errorf("a host that dropped off did not move to the end: %v", got)
	}
	feed("a-host", 50)
	if got := strings.Join(a.sortedNames(), ","); got != want {
		t.Errorf("a host that came back did not return to its place: %s, want %s", got, want)
	}
}

// TestFocusFollowsTheHostAcrossResort: a host coming online re-sorts the board,
// and the highlight must stay on the machine it was on, not on a position.
func TestFocusFollowsTheHostAcrossResort(t *testing.T) {
	a := New(Options{Hosts: []string{"small", "big"}, GraphRows: 1})
	a.width, a.height = 200, 40
	u := 10.0
	a.Deliver(sshxSample("small", wire.Sample{Gpus: []wire.Gpu{{Index: 0, Util: &u}}}))
	_ = a.fleetCanvas()
	a.focusName = "small"

	// An 8-GPU host arriving sorts ahead of the 1-GPU one.
	var gs []wire.Gpu
	for i := 0; i < 8; i++ {
		gs = append(gs, wire.Gpu{Index: i, Util: &u})
	}
	a.Deliver(sshxSample("big", wire.Sample{Gpus: gs}))
	_ = a.fleetCanvas()
	if a.sortedNames()[0] != "big" {
		t.Fatalf("expected the larger host first, got %v", a.sortedNames())
	}
	if a.focused() != "small" {
		t.Errorf("highlight moved to %q when another host came online", a.focused())
	}
}

// TestNaturalOrder: box-10 sorts after box-9, not between box-1 and box-2.
func TestNaturalOrder(t *testing.T) {
	in := []string{"box-10", "box-2", "box-1", "box-9"}
	want := []string{"box-1", "box-2", "box-9", "box-10"}
	for i := 0; i < len(in); i++ {
		for j := i + 1; j < len(in); j++ {
			if naturalLess(in[j], in[i]) {
				in[i], in[j] = in[j], in[i]
			}
		}
	}
	if strings.Join(in, ",") != strings.Join(want, ",") {
		t.Errorf("natural order = %v, want %v", in, want)
	}
}

// TestEachGpuPlotsItsOwnHistory: eight rows repeating one host-wide curve would
// draw the same line eight times.
func TestEachGpuPlotsItsOwnHistory(t *testing.T) {
	a := New(Options{Hosts: []string{"h"}, GraphRows: 1})
	for i := 0; i < 10; i++ {
		lo, hi := 5.0, 95.0
		a.Deliver(sshxSample("h", wire.Sample{Gpus: []wire.Gpu{{Index: 0, Util: &lo}, {Index: 1, Util: &hi}}}))
	}
	h := a.states["h"].gpuHist
	if len(h["#0"]) != 10 || len(h["#1"]) != 10 || h["#0"][9] == h["#1"][9] {
		t.Errorf("per-GPU history not kept separately: %v / %v", h["#0"], h["#1"])
	}
}

// TestFailedHostShowsReason: "offline" without a reason makes you ssh in to find out.
func TestFailedHostShowsReason(t *testing.T) {
	a := New(Options{Hosts: []string{"deadhost", "live"}, GraphRows: 2})
	a.width, a.height = 200, 40
	a.Deliver(sshxErr("deadhost", "connection timed out"))
	frame := a.PlainFrame()
	if !strings.Contains(frame, "connection timed out") {
		t.Error("failure reason is not rendered")
	}
}

// TestStaleHostIsMarked: an old sample presented as current is a lie.
func TestStaleHostIsMarked(t *testing.T) {
	a := fixture(200, 40, []int{2})
	a.mu.Lock()
	a.states["hosta"].age = time.Now().Add(-30 * time.Second)
	a.mu.Unlock()
	if !strings.Contains(a.PlainFrame(), "ago") {
		t.Error("stale host is not marked with its age")
	}
}

// TestDetailViewListsProcesses: the overview hides processes, the detail must show them.
func TestDetailViewListsProcesses(t *testing.T) {
	a := fixture(200, 40, []int{2})
	a.mu.Lock()
	st := a.states["hosta"]
	st.sample.Gpus[0].Procs = []wire.Proc{
		{PID: 4242, Name: "worker.py:pick_place", User: "alice", MemMB: 8192},
		{PID: 4242, Name: "worker.py:pick_place", User: "alice", MemMB: 8192},
	}
	a.mu.Unlock()
	a.detail = true
	frame := a.detailCanvas().Plain()
	// The command stops a gap short of the graph, so a long one is cut.
	if !strings.Contains(frame, "worker.py") {
		t.Error("process label missing from detail view")
	}
	if strings.Count(frame, "4242") != 1 {
		t.Error("duplicate pid was listed twice")
	}
}

// TestBandsCloseFlat uses the real fleet's shape: two 8-GPU, four 4-GPU, three
// 2-GPU and five 1-GPU hosts in three lanes. Every band must start on the same
// row in every lane; a waterfall layout staggered exactly this shape.
func TestBandsCloseFlat(t *testing.T) {
	a := fixture(200, 60, []int{8, 8, 4, 4, 4, 4, 2, 2, 2, 1, 1, 1, 1, 1})
	cards, _ := pack(a.sortedHosts(), 200, 3, spacing{})

	tops := map[int]map[int]bool{} // y -> lanes with a card starting there
	for _, p := range cards {
		if tops[p.y] == nil {
			tops[p.y] = map[int]bool{}
		}
		tops[p.y][p.lane] = true
	}
	// Band starts are the rows where lane 0 starts a card.
	var starts []int
	for y, lanes := range tops {
		if lanes[0] {
			starts = append(starts, y)
		}
	}
	for _, y := range starts {
		if len(tops[y]) != 3 && y != maxInt(starts) {
			t.Errorf("band at row %d starts in lanes %v only; the row is staggered", y, tops[y])
		}
	}
	// The largest host still leads the board.
	if first := cards[0]; first.lane != 0 || first.y != 0 || len(first.host.sample.Gpus) != 8 {
		t.Errorf("first card is %s, want an 8-GPU host at the top left", first.host.name)
	}
}

func maxInt(v []int) int {
	m := v[0]
	for _, x := range v {
		m = max(m, x)
	}
	return m
}

// TestBoardDoesNotMoveWithLoad: only a host coming or going may move a card.
// Load crossing a digit boundary (9 -> 10 busy cards, 9.9 -> 10.0 kW) used to
// change the header's height and shift the whole board down a line.
func TestBoardDoesNotMoveWithLoad(t *testing.T) {
	counts := []int{8, 8, 4, 4, 4, 2, 2, 1, 1}
	positions := func(a *App) map[string][2]int {
		out := map[string][2]int{}
		for y, line := range strings.Split(a.PlainFrame(), "\n") {
			for _, h := range a.sortedHosts() {
				if x := strings.Index(line, "● "+h.label+"├"); x >= 0 {
					out[h.name] = [2]int{y, theme.RuneWidth(line[:x])}
				}
			}
		}
		return out
	}
	load := func(a *App, util, power float64) {
		for i, n := range counts {
			s := wire.Sample{}
			for j := 0; j < n; j++ {
				u, used, total := util, uint64(8000), uint64(24564)
				p, capw, temp := power, 450.0, 40.0
				s.Gpus = append(s.Gpus, wire.Gpu{Index: j, Name: "RTX 4090", Util: &u, MemUsed: &used, MemTotal: &total, Power: &p, PowerCap: &capw, Temp: &temp})
			}
			a.Deliver(sshxSample(a.order[i], s))
		}
	}
	// Every width: the header only changed height at the few widths where the
	// totals and the strip were within a digit of filling the row.
	for w := 80; w <= 240; w++ {
		// Tall enough that the whole board is on screen at every width.
		a := fixture(w, 120, counts)
		load(a, 0, 5)
		want := positions(a)
		if len(want) != len(counts) {
			t.Fatalf("width %d: found %d card titles, want %d", w, len(want), len(counts))
		}
		for _, l := range [][2]float64{{4, 20}, {9, 30}, {50, 280}, {100, 449}, {100, 700}, {0, 0}} {
			load(a, l[0], l[1])
			got := positions(a)
			for name, p := range want {
				if got[name] != p {
					t.Errorf("width %d, load %v: %s moved from %v to %v", w, l, name, p, got[name])
				}
			}
		}
	}
}

// TestBoxesStayClosed: at every width a one-column layout uses, each row that
// opens with a border must also close with one. A value drawn past the inner
// edge overwrites the right border, which is how the detail header lost it.
func TestBoxesStayClosed(t *testing.T) {
	for w := 40; w <= 110; w += 7 {
		a := fixture(w, 40, []int{8, 2, 1})
		a.mu.Lock()
		a.states["hosta"].sample.Gpus[0].Procs = []wire.Proc{{PID: 1, Name: "a-rather-long-command-line --with --flags", User: "someone-long", MemMB: 4096}}
		a.mu.Unlock()
		views := map[string]string{"board": a.PlainFrame()}
		a.mu.Lock()
		a.detail = true
		views["detail"] = a.detailCanvas().Plain()
		a.mu.Unlock()
		for view, frame := range views {
			for i, line := range strings.Split(frame, "\n") {
				r := []rune(strings.TrimRight(line, " "))
				if len(r) > 0 && r[0] == '│' && r[len(r)-1] != '│' {
					t.Errorf("%s at width %d, line %d: box row not closed: %q", view, w, i, line)
				}
			}
		}
	}
}

// TestRowPlanRhythm: in every plan the gap between elements is the same
// everywhere, a bar's percentage follows it after one cell, and with the graph
// on the row ends at the content's right edge, so a card's margins match.
func TestRowPlanRhythm(t *testing.T) {
	for inner := 40; inner <= 200; inner++ {
		for _, idxW := range []int{1, 2} {
			g := planRow(inner, idxW, true)
			if g.gap < 2 || g.gap > 3 {
				t.Fatalf("inner %d: gap %d", inner, g.gap)
			}
			if g.utilX() != idxW+g.gap || g.vramX() != g.utilX()+g.meterW+1+pctW+g.gap {
				t.Fatalf("inner %d: columns out of rhythm: %+v", inner, g)
			}
			if g.graphW > 0 && g.width() != inner {
				t.Fatalf("inner %d: a row with a graph is %d wide; its right margin differs from its left", inner, g.width())
			}
		}
	}
}

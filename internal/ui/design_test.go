package ui

import (
	"math"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"ftop/internal/wire"
)

func grid(frame string) [][]rune {
	lines := strings.Split(frame, "\n")
	out := make([][]rune, len(lines))
	for i, l := range lines {
		out[i] = []rune(l)
	}
	return out
}

func at(g [][]rune, x, y int) rune {
	if y < 0 || y >= len(g) || x < 0 || x >= len(g[y]) {
		return 0
	}
	return g[y][x]
}

// TestLabelsSitOnTheBorder: a title is its text between two upright strokes on
// its box's top border, ─┤title├─, with the border running into both strokes
// and no rows of its own above or below.
func TestLabelsSitOnTheBorder(t *testing.T) {
	widths := []int{90, 120, 160, 200, 240}
	for w := minWidth; w < 90; w++ {
		widths = append(widths, w)
	}
	for _, w := range widths {
		a := fixture(w, 80, []int{8, 4, 2, 1})
		a.Deliver(sshxErr("hoste", "connection timed out"))
		views := map[string]string{"board": a.PlainFrame()}
		a.open(a.sortedNames()[0])
		views["detail"] = a.detailCanvas().Plain()
		for view, frame := range views {
			g := grid(frame)
			labels := 0
			for y := range g {
				open, lastClose := -1, -2
				for x, r := range g[y] {
					switch r {
					case '┤':
						if open >= 0 {
							t.Errorf("%s at width %d: label opened at (%d,%d) inside another", view, w, x, y)
						}
						if x-lastClose < 2 {
							t.Errorf("%s at width %d: labels touch at (%d,%d)", view, w, x, y)
						}
						if at(g, x-1, y) != '─' {
							t.Errorf("%s at width %d: the border does not run into the label at (%d,%d): %q", view, w, x, y, string(g[y]))
						}
						open = x
					case '├':
						if open < 0 {
							t.Errorf("%s at width %d: label stroke at (%d,%d) has no opening stroke: %q", view, w, x, y, string(g[y]))
							continue
						}
						labels++
						if at(g, x+1, y) != '─' {
							t.Errorf("%s at width %d: the border does not leave the label at (%d,%d): %q", view, w, x, y, string(g[y]))
						}
						// No label box above or below it any more.
						for k := open; k <= x; k++ {
							if strings.ContainsRune("╭╮╰╯", at(g, k, y-1)) || strings.ContainsRune("╭╮╰╯", at(g, k, y+1)) {
								t.Errorf("%s at width %d: label %d..%d on row %d has a frame above it", view, w, open, x, y)
								break
							}
						}
						open, lastClose = -1, x
					}
				}
				if open >= 0 {
					t.Errorf("%s at width %d: label opened at (%d,%d) never closes: %q", view, w, open, y, string(g[y]))
				}
			}
			if labels == 0 {
				t.Errorf("%s at width %d: no labels drawn", view, w)
			}
		}
	}
}

// TestLabelsHugTheirText: a label's strokes are the cells either side of its
// text, with no padding inside.
func TestLabelsHugTheirText(t *testing.T) {
	a := fixture(200, 50, []int{8, 4})
	frame := a.PlainFrame()
	for _, want := range []string{"┤all online├", "┤● hosta├", "┤8× RTX 4090 · 24.0G├"} {
		if !strings.Contains(frame, want) {
			t.Errorf("no %q in the board:\n%s", want, frame)
		}
	}
}

// TestNoNotchStrokes: btop's notches (┐title┌, ┘label└) are gone everywhere;
// titles are closed labels and bottom-border labels sit on the bare line.
func TestNoNotchStrokes(t *testing.T) {
	a := fixture(200, 50, []int{8, 4, 2})
	a.states["hostb"].age = a.states["hostb"].age.Add(-30e9) // stale: an age in its border
	frames := []string{a.PlainFrame()}
	a.open("hosta")
	frames = append(frames, a.detailCanvas().Plain())
	for _, f := range frames {
		if strings.ContainsAny(f, "┐┌┘└") {
			for _, line := range strings.Split(f, "\n") {
				if strings.ContainsAny(line, "┐┌┘└") {
					t.Errorf("notch stroke in %q", line)
				}
			}
		}
	}
	if !strings.Contains(frames[0], "── util ──") && !strings.Contains(frames[0], "── util · vram") {
		t.Error("the column names are not on the cards' bottom borders")
	}
}

// TestClockClearOfDivider: the clock label never covers the header's divider
// joint, and both stay intact, at every width.
func TestClockClearOfDivider(t *testing.T) {
	clock := regexp.MustCompile(`─┤\d\d:\d\d:\d\d├─`)
	for w := 60; w <= 240; w++ {
		a := fixture(w, 50, []int{8, 4})
		border := strings.Split(a.PlainFrame(), "\n")[0]
		hasDivider := strings.Contains(border, "┬")
		if w >= 120 && !hasDivider {
			t.Errorf("width %d: no divider between the chart and the panel: %q", w, border)
		}
		if hasDivider && !clock.MatchString(border) {
			t.Errorf("width %d: clock missing or broken: %q", w, border)
		}
	}
}

// TestSkylineClickSelectsHost: each host's bars in the header are a click target
// for that host, and a second click opens it.
func TestSkylineClickSelectsHost(t *testing.T) {
	a := fixture(200, 50, []int{8, 4, 2})
	a.View()
	var target *hit
	for i := range a.hits {
		if a.hits[i].host != "" && a.hits[i].host != a.focused() {
			target = &a.hits[i]
			break
		}
	}
	if target == nil {
		t.Fatal("the skyline has no host targets")
	}
	if target.y >= a.boardTop {
		t.Fatalf("host target at row %d is not in the header (board starts at %d)", target.y, a.boardTop)
	}
	a.Update(tea.MouseClickMsg{X: target.x, Y: target.y, Button: tea.MouseLeft})
	a.View()
	if a.focused() != target.host {
		t.Fatalf("clicking %s in the skyline selected %s", target.host, a.focused())
	}
	a.Update(tea.MouseClickMsg{X: target.x, Y: target.y, Button: tea.MouseLeft})
	if !a.detail {
		t.Fatal("a second click on the selected host did not open it")
	}
}

// TestSkylineMarksUnknownNotIdle: a card that reports no utilisation is a mark
// of its own in the skyline, never the idle floor.
func TestSkylineMarksUnknownNotIdle(t *testing.T) {
	a := fixture(200, 50, []int{2})
	a.mu.Lock()
	for i := range a.states["hosta"].sample.Gpus {
		a.states["hosta"].sample.Gpus[i].Util = nil
	}
	a.mu.Unlock()
	head := strings.Join(strings.Split(a.PlainFrame(), "\n")[:7], "\n")
	if !strings.Contains(head, "·") {
		t.Errorf("unknown utilisation not marked in the skyline:\n%s", head)
	}
}

// TestEveryViewFitsEveryTerminal: from the smallest supported terminal up, no
// view panics, overflows its rows or columns, or leaves a box row open.
func TestEveryViewFitsEveryTerminal(t *testing.T) {
	for _, h := range []int{minHeight, 16, 24, 40} {
		for w := minWidth; w <= 240; w++ {
			a := fixture(w, h, []int{8, 4, 2, 1})
			a.Deliver(sshxErr("hoste", "connection timed out: a long reason that will not fit anywhere"))
			views := map[string]string{"board": a.PlainFrame()}
			a.mu.Lock()
			a.open(a.sortedNames()[0])
			views["detail"] = a.detailCanvas().Plain()
			views["help"] = a.helpCanvas().Plain()
			a.mu.Unlock()
			for view, frame := range views {
				lines := strings.Split(frame, "\n")
				if len(lines) > h {
					t.Fatalf("%s %dx%d: %d lines", view, w, h, len(lines))
				}
				for i, line := range lines {
					if lw := len([]rune(line)); lw > w {
						t.Fatalf("%s %dx%d: line %d is %d cells", view, w, h, i, lw)
					}
					r := []rune(strings.TrimRight(line, " "))
					if len(r) > 1 && r[0] == '│' && !strings.ContainsRune("│╯╮", r[len(r)-1]) && i < h-1 {
						t.Fatalf("%s %dx%d: line %d not closed: %q", view, w, h, i, line)
					}
				}
			}
		}
	}
}

// TestUnknownReadingsInTheHeader: unknown VRAM has its own mark under the axis,
// a NaN utilisation is unknown rather than the idle floor, and a measured 0 W
// without a cap is shown as 0, not as missing.
func TestUnknownReadingsInTheHeader(t *testing.T) {
	frame := func(gs []wire.Gpu) *App {
		a := New(Options{Hosts: []string{"h"}, GraphRows: 1})
		a.width, a.height = 200, 40
		a.Deliver(sshxSample("h", wire.Sample{Gpus: gs}))
		a.PlainFrame()
		return a
	}
	head := func(gs []wire.Gpu) string {
		a := New(Options{Hosts: []string{"h"}, GraphRows: 1})
		a.width, a.height = 200, 40
		a.Deliver(sshxSample("h", wire.Sample{Gpus: gs}))
		// Only the chart, left of the divider: the panel beside it shows "—"
		// for unknown VRAM anyway.
		lines := strings.Split(a.PlainFrame(), "\n")[:a.boardTop]
		div := strings.Index(string([]rune(lines[0])), "┬")
		var b strings.Builder
		for _, l := range lines {
			r := []rune(l)
			if cut := len([]rune(lines[0][:max(div, 0)])); div > 0 && cut < len(r) {
				r = r[:cut]
			}
			b.WriteString(string(r) + "\n")
		}
		return b.String()
	}
	zero, total, u := uint64(0), uint64(100), 10.0
	measured := head([]wire.Gpu{{Index: 0, Name: "RTX 4090", Util: &u, MemUsed: &zero, MemTotal: &total}})
	unknown := head([]wire.Gpu{{Index: 0, Name: "RTX 4090", Util: &u}})
	if measured == unknown {
		t.Error("unknown VRAM draws exactly like 0% VRAM")
	}
	// Known VRAM, so the only possible mark is the utilisation's, above the axis.
	nan, used := math.NaN(), uint64(50)
	h := head([]wire.Gpu{{Index: 0, Name: "RTX 4090", Util: &nan, MemUsed: &used, MemTotal: &total}})
	lines := strings.Split(h, "\n")
	// The axis is per host now, so the row is found by the noun on it rather
	// than by a rule running under the name.
	axis := -1
	for i, l := range lines {
		if strings.Contains(l, " gpu ") {
			axis = i
		}
	}
	if axis < 0 || !strings.Contains(strings.Join(lines[axis-1:axis], "\n"), "·") {
		t.Errorf("a NaN utilisation is not marked unknown above the axis:\n%s", h)
	}
	p := 0.0
	a := frame([]wire.Gpu{{Index: 0, Name: "RTX 4090", Power: &p}})
	if h := strings.Join(strings.Split(a.PlainFrame(), "\n")[:a.boardTop], "\n"); !strings.Contains(h, "0.0kW") {
		t.Errorf("a measured 0 W without a cap is not shown:\n%s", h)
	}
}

// TestTooSmallMessageFits: the message for a terminal below the minimum is cut
// to that terminal, or it wraps and scrolls.
func TestTooSmallMessageFits(t *testing.T) {
	for w := 1; w < minWidth; w++ {
		for h := 1; h < minHeight; h++ {
			lines := strings.Split(tooSmall(w, h), "\n")
			if len(lines) > h {
				t.Fatalf("%dx%d: %d lines", w, h, len(lines))
			}
			for _, l := range lines {
				if n := len([]rune(l)); n > w {
					t.Fatalf("%dx%d: a %d-cell line: %q", w, h, n, l)
				}
			}
		}
	}
}

// TestClockClearOfLongStatus: a long status ("connecting 1") on a narrow
// terminal keeps its label whole; the clock gives way.
func TestClockClearOfLongStatus(t *testing.T) {
	for w := minWidth; w < 90; w++ {
		a := New(Options{Hosts: []string{"hosta", "hostb"}, GraphRows: 1})
		a.width, a.height = w, 30
		u := 10.0
		a.Deliver(sshxSample("hosta", wire.Sample{Gpus: []wire.Gpu{{Index: 0, Name: "RTX 4090", Util: &u}}}))
		border := strings.Split(a.PlainFrame(), "\n")[0]
		if !strings.Contains(border, "┤connecting 1├") {
			t.Errorf("width %d: status label broken: %q", w, border)
		}
	}
}

// TestPowerFractionIsOneSubset: when some cards report no power cap the panel
// shows total draw alone; dividing every card's draw by some cards' caps read
// as a fleet at its limit.
func TestPowerFractionIsOneSubset(t *testing.T) {
	a := New(Options{Hosts: []string{"h"}, GraphRows: 1})
	a.width, a.height = 200, 40
	pa, ca, pb := 100.0, 200.0, 100.0
	a.Deliver(sshxSample("h", wire.Sample{Gpus: []wire.Gpu{
		{Index: 0, Name: "RTX 4090", Power: &pa, PowerCap: &ca},
		{Index: 1, Name: "RTX 4090", Power: &pb},
	}}))
	head := strings.Join(strings.Split(a.PlainFrame(), "\n")[:a.boardTop], "\n")
	if strings.Contains(head, "0.2/0.2kW") || strings.Contains(head, "50%") {
		t.Errorf("power fraction mixes capped and uncapped cards:\n%s", head)
	}
	if !strings.Contains(head, "0.2kW") {
		t.Errorf("total draw missing:\n%s", head)
	}
}

// TestCardsKeepTheirSpace: cards stacked in one lane are a row apart, like
// cards in different bands.
func TestCardsKeepTheirSpace(t *testing.T) {
	for _, w := range []int{120, 160, 200, 240} {
		for _, counts := range [][]int{{8, 2, 1}, {8, 8, 4, 2, 2, 1, 1}, {4, 1, 1, 1}} {
			a := fixture(w, 200, counts)
			a.PlainFrame()
			for i, p := range a.placed {
				for _, q := range a.placed[i+1:] {
					if p.lane != q.lane {
						continue
					}
					upper, lower := p, q
					if q.y < p.y {
						upper, lower = q, p
					}
					if lower.y < upper.y+upper.h+1 {
						t.Errorf("width %d %v: %s (rows %d..%d) and %s (from %d) have no row between them",
							w, counts, upper.host.name, upper.y, upper.y+upper.h, lower.host.name, lower.y)
					}
				}
			}
		}
	}
}

// TestZeroPowerCapIsUnknown: a cap of 0 is no cap; 100W against it is not 0%.
func TestZeroPowerCapIsUnknown(t *testing.T) {
	a := fixture(200, 50, []int{1})
	p, zero := 100.0, 0.0
	a.mu.Lock()
	a.states["hosta"].sample.Gpus[0].Power = &p
	a.states["hosta"].sample.Gpus[0].PowerCap = &zero
	a.mu.Unlock()
	a.open("hosta")
	for _, line := range strings.Split(a.detailCanvas().Plain(), "\n") {
		if strings.Contains(line, "power ") && (strings.Contains(line, "/0W") || strings.Contains(line, " 0%")) {
			t.Errorf("a zero cap was used as a denominator: %q", line)
		}
	}
}

// TestPagingIsContinuous: a page key moves exactly one visible window, so no
// row is ever skipped, on the board and in a host.
func TestPagingIsContinuous(t *testing.T) {
	for _, h := range []int{minHeight, 20, 30} {
		a := fixture(80, h, []int{8, 8, 8, 8})
		a.View()
		for i := 0; i < 3; i++ {
			before := a.boardScroll
			a.Update(keyMsg("pgdown"))
			a.View()
			if step := a.boardScroll - before; step > a.boardView || (step == 0 && i == 0) {
				t.Errorf("80x%d board: page down moved %d rows through a %d-row window", h, step, a.boardView)
			}
		}
		before := a.boardScroll
		a.Update(keyMsg("pgup"))
		a.View()
		if step := before - a.boardScroll; step > a.boardView {
			t.Errorf("80x%d board: page up moved %d rows through a %d-row window", h, step, a.boardView)
		}

		b := fixture(80, h, []int{8})
		b.mu.Lock()
		for i := range b.states["hosta"].sample.Gpus {
			b.states["hosta"].sample.Gpus[i].Procs = []wire.Proc{{PID: 1, Name: "p"}, {PID: 2, Name: "q"}}
		}
		b.mu.Unlock()
		b.open("hosta")
		b.View()
		for i := 0; i < 3; i++ {
			before := b.detailScroll
			b.Update(keyMsg("pgdown"))
			b.View()
			if step := b.detailScroll - before; step > b.detailView || (step == 0 && i == 0) {
				t.Errorf("80x%d detail: page down moved %d rows through a %d-row window", h, step, b.detailView)
			}
		}
	}
}

// TestSkylineKeepsItsDivider: the chart, its truncation mark included, stays
// left of the divider at every width and fleet shape.
func TestSkylineKeepsItsDivider(t *testing.T) {
	fleets := [][]int{{1, 1, 1, 1, 1}, {8, 8, 4, 4, 2, 2, 1, 1, 1, 1, 1, 1}, {2, 2, 2, 2, 2, 2, 2, 2}}
	for _, counts := range fleets {
		for w := 87; w <= 240; w++ {
			a := fixture(w, 50, counts)
			g := grid(a.PlainFrame())
			div := -1
			for x, r := range g[0] {
				if r == '┬' {
					div = x
				}
			}
			if div < 0 {
				continue
			}
			for y := 1; y < a.boardTop-1; y++ {
				if at(g, div, y) != '│' {
					t.Fatalf("width %d %v: divider broken at row %d: %q", w, counts, y, string(g[y]))
				}
			}
		}
	}
}

// TestWaitingHostOpensAtTheTop: page keys pressed while a host has no sample
// yet must not scroll its first sample part-way down.
func TestWaitingHostOpensAtTheTop(t *testing.T) {
	a := fixture(80, 30, []int{8})
	a.mu.Lock()
	a.states["hostb"] = &hostState{name: "hostb", label: "hostb"}
	a.order = append(a.order, "hostb")
	a.mu.Unlock()
	a.open("hostb")
	a.View()
	for i := 0; i < 3; i++ {
		a.Update(keyMsg("pgdown"))
		a.View()
	}
	u := 10.0
	var gs []wire.Gpu
	for i := 0; i < 8; i++ {
		gs = append(gs, wire.Gpu{Index: i, Name: "RTX 4090", Util: &u})
	}
	a.Deliver(sshxSample("hostb", wire.Sample{Gpus: gs}))
	a.View()
	if a.detailScroll != 0 {
		t.Fatalf("the first sample opened scrolled %d rows down", a.detailScroll)
	}
}

// TestBarsSitAgainstTheirPercentages: everywhere a meter is drawn, in the
// header panel, on the cards and in a host's view, its percentage starts after
// exactly one blank cell: a bar and its number read as one element.
func TestBarsSitAgainstTheirPercentages(t *testing.T) {
	isBar := func(r rune) bool { return r == '■' || r == '░' }
	// meters checks every meter that ends in lines and returns how many there
	// were.
	meters := func(w int, where string, lines []string) int {
		n := 0
		for _, line := range lines {
			r := []rune(line)
			for i := 1; i+1 < len(r); i++ {
				if !isBar(r[i-1]) || isBar(r[i]) {
					continue
				}
				n++
				if r[i] != ' ' || !(r[i+1] >= '0' && r[i+1] <= '9' || r[i+1] == '—') {
					t.Errorf("width %d, %s: a bar's percentage is not one cell after it: %q", w, where, line)
				}
			}
		}
		return n
	}
	for _, w := range []int{80, 120, 200} {
		a := fixture(w, 60, []int{8, 4, 2})
		lines := strings.Split(a.PlainFrame(), "\n")
		// Each region is counted on its own: a check that finds no meters in
		// one of them proves nothing about it.
		if n := meters(w, "header", lines[:a.boardTop]); n != 3 {
			t.Errorf("width %d: %d meters in the header, want the panel's 3", w, n)
		}
		if n := meters(w, "board", lines[a.boardTop:]); n != 2*(8+4+2) {
			t.Errorf("width %d: %d meters on the board, want 2 per GPU", w, n)
		}
		a.open("hosta")
		if n := meters(w, "host", strings.Split(a.detailCanvas().Plain(), "\n")); n < 2+3 {
			t.Errorf("width %d: %d meters in the host view, want the host's 2 and 3 per device", w, n)
		}
	}
}

// TestHostReadingsKeepTheirColumns: a reading too wide for its column is cut
// rather than run into the next one, and each gauge carries its own readings —
// the link beside utilisation, the heat beside power.
func TestHostReadingsKeepTheirColumns(t *testing.T) {
	for _, w := range []int{76, 100, 160} {
		a := fixture(w, 40, []int{1})
		a.mu.Lock()
		g := &a.states["hosta"].sample.Gpus[0]
		p, c, gen, lanes := 1e20, 1.0, 4, 16
		g.Power, g.PowerCap, g.PcieGen, g.PcieWide = &p, &c, &gen, &lanes
		a.mu.Unlock()
		a.open("hosta")
		var util, power string
		for _, line := range strings.Split(a.detailCanvas().Plain(), "\n") {
			switch {
			case strings.HasPrefix(line, "│ util "):
				util = line
			case strings.HasPrefix(line, "│ power "):
				power = line
			}
		}
		if !strings.Contains(util, "PCIe") {
			t.Errorf("width %d: the PCIe reading is not beside utilisation: %q", w, util)
		}
		i := strings.Index(power, "°C")
		if i < 0 {
			t.Fatalf("width %d: the temperature is not beside power: %q", w, power)
		}
		before := strings.TrimRight(power[:i], "0123456789")
		if !strings.HasSuffix(before, "…"+strings.Repeat(" ", detailGap)) {
			t.Errorf("width %d: the power reading is not cut a gap short of the temperature: %q", w, power)
		}
	}
}

// TestGraphEndsAtTheMargin: on every lane, the narrowest included, a GPU row's
// graph ends at the card's inner margin, so the row's margins match.
func TestGraphEndsAtTheMargin(t *testing.T) {
	checked := 0
	for w := 120; w <= 240; w++ {
		a := fixture(w, 80, []int{4, 4, 4, 4})
		g := grid(a.PlainFrame())
		if boardGeom(a.width, a.lanes, 1, idxWidth(a.sortedHosts()), true).graphW == 0 {
			continue // no graph: the row ends at its last reading
		}
		checked++
		rows := laneRows(a.width, a.lanes, 1)
		for _, p := range a.placed {
			for i := range p.host.sample.Gpus {
				// The graph's floor: a GPU's last line, below the host's row.
				y := a.boardTop + p.y + 1 + hostRows + i*rows + rows - 1
				if r := at(g, p.x+p.w-3, y); r == ' ' || r == 0 {
					t.Errorf("width %d: %s GPU %d ends short of the margin: %q", w, p.host.name, i, string(g[y]))
				}
			}
		}
	}
	if checked < 40 {
		t.Fatalf("only %d widths had a graph to check", checked)
	}
}

// TestIndexIsAGapFromItsBar: the GPU index is as far from the utilisation bar
// as any two other elements are from each other, not jammed against it.
func TestIndexIsAGapFromItsBar(t *testing.T) {
	rows := 0
	for _, w := range []int{80, 120, 200, 240} {
		a := fixture(w, 60, []int{8, 4, 2})
		for y, line := range strings.Split(a.PlainFrame(), "\n") {
			r := []rune(line)
			if y < a.boardTop || len(r) < 6 || r[0] != '│' || !strings.ContainsAny(line, "■░") {
				continue
			}
			i := 1
			for i < len(r) && r[i] == ' ' {
				i++
			}
			if i == len(r) || r[i] < '0' || r[i] > '9' {
				continue
			}
			for i < len(r) && r[i] >= '0' && r[i] <= '9' {
				i++
			}
			gap := 0
			for ; i < len(r) && r[i] == ' '; i++ {
				gap++
			}
			rows++
			if gap < 2 {
				t.Errorf("width %d: the index is %d cell(s) from its bar: %q", w, gap, line)
			}
		}
	}
	if rows == 0 {
		t.Fatal("no GPU rows found")
	}
}

// TestHostHeaderKeepsItsRhythm: in the host's own box, the gap before a fact is
// the same as the gap after it, like every other pair on the board. Both meters
// are at 100% so their percentages fill their columns exactly, and the space
// measured is the gap alone.
func TestHostHeaderKeepsItsRhythm(t *testing.T) {
	gapIn := regexp.MustCompile(`100%( +)64\.0G/64\.0G( +)cache`)
	checked := 0
	for _, w := range []int{120, 160, 200} {
		a := fixture(w, 40, []int{1})
		a.mu.Lock()
		cpu := 100.0
		st := a.states["hosta"]
		st.sample.CPUPct, st.sample.TotalKB, st.sample.MemKB = &cpu, 64*1024*1024, 64*1024*1024
		st.sample.MemLimited = true
		a.mu.Unlock()
		a.open("hosta")
		line := strings.Split(a.detailCanvas().Plain(), "\n")[2]
		m := gapIn.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("width %d: the memory row is not readable: %q", w, line)
		}
		checked++
		if len(m[1]) != detailGap || len(m[2]) != detailGap {
			t.Errorf("width %d: gaps of %d and %d cells, want %d: %q", w, len(m[1]), len(m[2]), detailGap, line)
		}
	}
	if checked != 3 {
		t.Fatalf("%d rows checked", checked)
	}
}

// TestHugePIDStaysInItsColumn: a PID wider than its column is cut, never
// written over the box's left border.
func TestHugePIDStaysInItsColumn(t *testing.T) {
	a := fixture(100, 40, []int{1})
	a.mu.Lock()
	a.states["hosta"].sample.Gpus[0].Procs = []wire.Proc{{PID: 1<<63 - 1, Name: "p", User: "z"}}
	a.mu.Unlock()
	a.open("hosta")
	seen := false
	for _, line := range strings.Split(a.detailCanvas().Plain(), "\n") {
		if !strings.Contains(line, " z ") {
			continue
		}
		seen = true
		if !strings.HasPrefix(line, "\u2502 ") {
			t.Errorf("the process row starts outside its box: %q", line)
		}
	}
	if !seen {
		t.Fatal("no process row found")
	}
}

// TestHostGraphReachesTheBoxBottom: a device's history runs from its box's first
// row to its last, so its floor is the box's bottom, and the process table's
// command column stops a gap short of it.
func TestHostGraphReachesTheBoxBottom(t *testing.T) {
	a := fixture(160, 40, []int{1})
	a.mu.Lock()
	st := a.states["hosta"]
	// The box keeps two process rows whatever is on the card; three processes
	// fill them, so the last row has a command to collide with the graph.
	long := strings.Repeat("a-very-long-command-line ", 8)
	st.sample.Gpus[0].Procs = []wire.Proc{{PID: 7, User: "z", Name: long, MemMB: 300}, {PID: 8, User: "z", Name: long, MemMB: 200}, {PID: 9, User: "z", Name: long, MemMB: 100}}
	for i := 0; i < 200; i++ {
		st.gpuHist[gpuKey(st.sample.Gpus[0])] = appendCapped(st.gpuHist[gpuKey(st.sample.Gpus[0])], 60)
	}
	a.mu.Unlock()
	a.open("hosta")
	g := grid(a.detailCanvas().Plain())
	isDot := func(r rune) bool { return r >= 0x2800 && r <= 0x28ff }
	top, bottom := -1, -1
	for y := range g {
		if strings.Contains(string(g[y]), "0 · RTX 4090") {
			top = y
		}
		if top >= 0 && y > top && at(g, 0, y) == '╰' {
			bottom = y
			break
		}
	}
	if top < 0 || bottom < 0 {
		t.Fatal("no device box")
	}
	last := g[bottom-1]
	gx := -1
	for x, r := range last {
		if isDot(r) {
			gx = x
			break
		}
	}
	if gx < 0 {
		t.Fatalf("the graph stops above the box's last row: %q", string(last))
	}
	for x := gx - detailGap; x < gx; x++ {
		if at(g, x, bottom-1) != ' ' {
			t.Errorf("the command runs into the graph: %q", string(last))
			break
		}
	}
	if !strings.Contains(string(last), "a-very-long") {
		t.Errorf("the command is gone: %q", string(last))
	}
}

// TestSkylineSpacesHosts: in the header chart hosts are three cells apart,
// with no line drawn between them.
func TestSkylineSpacesHosts(t *testing.T) {
	for _, w := range []int{100, 160, 200, 240} {
		a := fixture(w, 50, []int{8, 4, 2, 1})
		frame := a.PlainFrame()
		for _, line := range strings.Split(frame, "\n")[:a.boardTop] {
			if strings.ContainsAny(line, "╎┼") {
				t.Errorf("width %d: a line between hosts: %q", w, line)
			}
		}
		var spans [][2]int
		for _, h := range a.hits {
			if h.host != "" && h.y == 1 {
				spans = append(spans, [2]int{h.x, h.x + h.w})
			}
		}
		if len(spans) != 4 {
			t.Fatalf("width %d: %d hosts in the chart, want 4", w, len(spans))
		}
		for i := 1; i < len(spans); i++ {
			if gap := spans[i][0] - spans[i-1][1]; gap < 3 {
				t.Errorf("width %d: hosts %d and %d are %d cells apart", w, i-1, i, gap)
			}
		}
	}
}

// TestSkylineKeysBesideTheAxis: the util and vram keys sit one each side of
// the axis, each a row clear of the "gpu" between them, a gap from the first
// host like the hosts from each other, and the header has no title of its own.
func TestSkylineKeysBesideTheAxis(t *testing.T) {
	for _, w := range []int{100, 160, 200, 240} {
		a := fixture(w, 50, []int{8, 4, 2, 1})
		lines := strings.Split(a.PlainFrame(), "\n")
		if strings.Contains(lines[0], "ftop") {
			t.Errorf("width %d: the header still has a title: %q", w, lines[0])
		}
		axis := -1
		for y := 1; y < a.boardTop-1; y++ {
			if strings.Contains(lines[y], " hosta ") {
				axis = y
			}
		}
		if axis < 0 {
			t.Fatalf("width %d: no axis", w)
		}
		first := -1
		for _, h := range a.hits {
			if h.host != "" && (first < 0 || h.x < first) {
				first = h.x
			}
		}
		for _, k := range []struct {
			row  int
			text string
		}{{axis - 2, "util"}, {axis + 2, "vram"}} {
			i := strings.Index(lines[k.row], k.text)
			if i < 0 {
				t.Errorf("width %d: %q is not beside the axis: %q", w, k.text, lines[k.row])
				continue
			}
			end := len([]rune(lines[k.row][:i])) + len(k.text)
			if first-end != 3 {
				t.Errorf("width %d: %q is %d cells from the first host, want 3: %q", w, k.text, first-end, lines[k.row])
			}
		}
		// The rows between the keys and the noun are blank in the key column.
		for _, row := range []int{axis - 1, axis + 1} {
			if key := string([]rune(lines[row])[:first]); strings.Contains(key, "util") || strings.Contains(key, "vram") {
				t.Errorf("width %d: a key sits right against the axis: %q", w, lines[row])
			}
		}
	}
}

// TestNarrowLanesStackAGPU: on lanes too narrow for one comfortable line (three
// lanes at 200 columns) a GPU takes two, its VRAM bar under its utilisation bar
// and its power under its temperature; wider lanes keep one line.
func TestNarrowLanesStackAGPU(t *testing.T) {
	isBar := func(r rune) bool { return r == '■' || r == '░' }
	for _, tc := range []struct {
		w, rows int
	}{{200, 2}, {160, 1}, {240, 1}} {
		a := fixture(tc.w, 120, []int{8, 4, 2})
		g := grid(a.PlainFrame())
		if got := laneRows(a.width, a.lanes, 1); got != tc.rows {
			t.Fatalf("width %d (%d lanes): %d lines per GPU, want %d", tc.w, a.lanes, got, tc.rows)
		}
		for _, p := range a.placed {
			n := len(p.host.sample.Gpus)
			// Border, the host's own block, then the GPUs' lines.
			if p.h != tc.rows*n+2+hostRows {
				t.Errorf("width %d: %s is %d rows for %d GPUs, want %d", tc.w, p.host.name, p.h, n, tc.rows*n+2+hostRows)
			}
			if tc.rows != 2 {
				continue
			}
			top, bottom := a.boardTop+p.y+1+hostRows, a.boardTop+p.y+2+hostRows
			firstBar := func(y int) int {
				for x := p.x; x < p.x+p.w; x++ {
					if isBar(at(g, x, y)) {
						return x
					}
				}
				return -1
			}
			if bx := firstBar(top); bx < 0 || firstBar(bottom) != bx {
				t.Errorf("width %d: %s's VRAM bar is not under its utilisation bar:\n%s\n%s", tc.w, p.host.name, string(g[top]), string(g[bottom]))
			}
			row := func(y int) string { return string(g[y][p.x : p.x+p.w]) }
			// One line per kind of fact: the utilisation line carries temperature
			// and power side by side, the memory line carries the used/total.
			if !strings.Contains(row(top), "°C") || !strings.Contains(row(top), "W ") {
				t.Errorf("width %d: %s's temperature and power are not on its utilisation line:\n%s", tc.w, p.host.name, row(top))
			}
			if !strings.Contains(row(bottom), "/") {
				t.Errorf("width %d: %s's memory line has no used/total reading:\n%s", tc.w, p.host.name, row(bottom))
			}
		}
	}
}

// TestHostGraphFollowsTheReadings: a host's reading columns are as wide as the
// readings its devices report, and a field no device reports takes no column at
// all, so the graph starts a gap after the last column rather than past cells
// reserved for a measurement this host never gives — or, when the readings are
// narrow, a gap after the room a process's command needs.
func TestHostGraphFollowsTheReadings(t *testing.T) {
	a := fixture(200, 40, []int{1})
	a.mu.Lock()
	st := a.states["hosta"]
	k := gpuKey(st.sample.Gpus[0])
	for i := 0; i < 400; i++ {
		st.gpuHist[k] = appendCapped(st.gpuHist[k], 100)
	}
	gpus := st.sample.Gpus
	a.mu.Unlock()
	if gpus[0].ClockMem != nil || gpus[0].PcieGen != nil {
		t.Fatal("the fixture reports a memory clock or PCIe link; this test needs a host without them")
	}
	cols := detailCols(gpus)
	// The plan: label, bar, its percentage, a gap, then each column and a gap,
	// then the graph.
	gx := 2 + detailLabelW + detailBarW + 1 + pctW + detailGap
	for _, c := range cols {
		gx += c + detailGap
	}
	gx = max(gx, 2+procCmdDX+minCmdW+detailGap)
	a.open("hosta")
	for _, line := range strings.Split(a.detailCanvas().Plain(), "\n") {
		if !strings.Contains(line, "│ vram ") {
			continue
		}
		if strings.Contains(line, "MEM") {
			t.Errorf("a column is printed for a reading the card never gave: %q", line)
		}
		run := []rune(line)
		first := -1
		for i, r := range run {
			if r >= 0x2800 && r <= 0x28ff {
				first = i
				break
			}
		}
		if first != gx {
			t.Errorf("the graph starts at %d, want %d (columns %v): %q", first, gx, cols, line)
		}
		return
	}
	t.Fatal("no vram row")
}

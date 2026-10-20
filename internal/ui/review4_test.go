package ui

import (
	"strings"
	"testing"

	"ftop/internal/theme"
	"ftop/internal/wire"
)

// The review of 2026-10-19: each test pins one decision, and each fails on the
// design before it.

// cardOf is the placed card of a host in the last frame, in screen rows.
func cardOf(t *testing.T, a *App, host string) placed {
	t.Helper()
	for _, p := range a.placed {
		if p.host.name == host {
			p.y += a.boardTop - a.boardScroll
			return p
		}
	}
	t.Fatalf("no card for %s", host)
	return placed{}
}

// TestGPUsAlternateBands: every other GPU of a card sits on a faint band across
// the card's inner width, so two lines of one GPU read as a pair.
func TestGPUsAlternateBands(t *testing.T) {
	a := fixture(200, 60, []int{4})
	c := a.fleetCanvas()
	p := cardOf(t, a, "hosta")
	if laneRows(a.width, a.lanes, 1) != 2 {
		t.Fatal("this test needs the two-line plan")
	}
	bg := a.skin.Bg
	for gpu := 0; gpu < 4; gpu++ {
		for line := 0; line < 2; line++ {
			// The host's own block comes first.
			y := p.y + 1 + hostRows + gpu*2 + line
			// Inside the box, left padding to right padding.
			for x := p.x + 1; x < p.x+p.w-1; x++ {
				banded := c.at(x, y).BG != bg
				if banded != (gpu%2 == 1) {
					t.Fatalf("GPU %d line %d cell %d: banded=%v", gpu, line, x-p.x, banded)
				}
			}
		}
	}
	// The border is not part of the band.
	if c.at(p.x, p.y+4).BG != bg {
		t.Error("the band covers the card's border")
	}
}

// TestGraphLabelNamesTheMetric: the history's label says what it plots.
func TestGraphLabelNamesTheMetric(t *testing.T) {
	frame := fixture(200, 60, []int{4, 2}).PlainFrame()
	if !strings.Contains(frame, "util history") {
		t.Errorf("no \"util history\" label:\n%s", frame)
	}
	if strings.Contains(frame, "last ") || strings.Contains(frame, " now ") {
		t.Errorf("the graph is still labelled by span or by \"now\":\n%s", frame)
	}
}

// TestLegendsInTheirMetricsColours: on a card's bottom border, "util" and
// "history" are in the util colour and "vram" in the VRAM colour, the same
// colours as the skyline's keys.
func TestLegendsInTheirMetricsColours(t *testing.T) {
	a := fixture(200, 60, []int{4})
	c := a.fleetCanvas()
	p := cardOf(t, a, "hosta")
	bottom := p.y + p.h - 1
	row := []rune(strings.Split(c.Plain(), "\n")[bottom])
	want := map[string]theme.RGB{
		"util":    a.skin.Scale.CPU.At(72),
		"vram":    a.skin.Scale.VRAM.At(78),
		"history": a.skin.Scale.CPU.At(72),
	}
	for word, fg := range want {
		i := strings.Index(string(row), word)
		if i < 0 {
			t.Fatalf("no %q on the bottom border: %q", word, string(row))
		}
		x := len([]rune(string(row)[:i]))
		if got := c.at(x, bottom).FG; got != fg {
			t.Errorf("%q is %s, want %s", word, got.Hex(), fg.Hex())
		}
	}
}

// TestTwoLineRowsKeepOneKindPerLine: on two lines, the util line carries
// temperature and power side by side, and the VRAM line the used/total size in
// the VRAM colour.
func TestTwoLineRowsKeepOneKindPerLine(t *testing.T) {
	a := fixture(200, 60, []int{2})
	c := a.fleetCanvas()
	p := cardOf(t, a, "hosta")
	lines := strings.Split(c.Plain(), "\n")
	util, vram := []rune(lines[p.y+1+hostRows]), []rune(lines[p.y+2+hostRows])
	if !strings.Contains(string(util), "°C") || !strings.Contains(string(util), "W ") {
		t.Errorf("the util line lacks temperature and power: %q", string(util))
	}
	i := strings.Index(string(vram), "G/")
	if i < 0 {
		t.Fatalf("the VRAM line has no used/total: %q", string(vram))
	}
	x := len([]rune(string(vram)[:i])) - 1 // a digit of the used size
	g := a.states["hosta"].sample.Gpus[0]
	if got, want := c.at(x, p.y+2+hostRows).FG, a.skin.Scale.VRAM.At(g.MemPct()*100); got != want {
		t.Errorf("the used size is %s, want the VRAM colour %s", got.Hex(), want.Hex())
	}
}

// TestUnitsAreSecondary: a reading's unit is dimmer than its number.
func TestUnitsAreSecondary(t *testing.T) {
	a := fixture(200, 60, []int{2})
	c := a.fleetCanvas()
	p := cardOf(t, a, "hosta")
	row := []rune(strings.Split(c.Plain(), "\n")[p.y+1+hostRows])
	i := strings.Index(string(row), "°C")
	if i < 0 {
		t.Fatal("no temperature")
	}
	x := len([]rune(string(row)[:i]))
	if unit, digit := c.at(x, p.y+1+hostRows).FG, c.at(x-1, p.y+1+hostRows).FG; unit != a.skin.Theme.C("graph_text") || digit == unit {
		t.Errorf("°C is %s and its digit %s: the unit should be the secondary colour", unit.Hex(), digit.Hex())
	}
}

// TestLabelsAreNotTheReadingColour: a label is grey text, never a measurement's
// colour, so a column name cannot be read as a value.
func TestLabelsAreNotTheReadingColour(t *testing.T) {
	s := theme.NewSkin()
	label := s.Theme.C("graph_text")
	if label == s.Theme.C("main_fg") {
		t.Error("labels are the same colour as the readings")
	}
	for _, ramp := range []string{"util", "vram", "power"} {
		for v := 0; v <= 100; v += 25 {
			col := s.Theme.G(ramp, v)
			if theme.DeltaE(label, col) < 8 {
				t.Errorf("the label grey is ΔE %.1f from %s at %d%%", theme.DeltaE(label, col), ramp, v)
			}
		}
	}
}

// TestFramesAreGrey: a healthy box's frame is a light grey, not green, and a
// failed host's is still coloured.
func TestFramesAreGrey(t *testing.T) {
	a := fixture(200, 60, []int{2, 2})
	a.focusName = "hostb"
	c := a.fleetCanvas()
	p := cardOf(t, a, "hosta")
	fg := c.at(p.x, p.y).FG
	if fg != a.skin.Hue.Frame {
		t.Errorf("a healthy card's frame is %s, want %s", fg.Hex(), a.skin.Hue.Frame.Hex())
	}
	// A frame is a desaturated slate: close to grey, and never one of the ramps.
	if _, c, _ := theme.OKLCHOf(fg); c > 0.05 {
		t.Errorf("the frame %s is too saturated to read as a frame (C=%.2f)", fg.Hex(), c)
	}
	if lum := (fg.R + fg.G + fg.B) / 3; lum < 0x30 || lum > 0x90 {
		t.Errorf("the frame %s is outside the quiet band", fg.Hex())
	}
}

// TestSkylineAxisIsPerHost: no rule joins two hosts, or the first host to the
// keys: each host's stretch of axis is under its own bars and stops there.
func TestSkylineAxisIsPerHost(t *testing.T) {
	a := fixture(200, 50, []int{8, 8, 2})
	lines := strings.Split(a.PlainFrame(), "\n")
	axis := -1
	for i, l := range lines[:a.boardTop] {
		if strings.Contains(l, "hosta") {
			axis = i
		}
	}
	if axis < 0 {
		t.Fatal("no axis row")
	}
	row := lines[axis]
	if div := strings.IndexRune(row, '│'); div >= 0 {
		row = row[:strings.LastIndex(row, "│")]
	}
	// Left of the first host: the key column and the "gpu" noun, and no rule. The
	// axis must not run through that margin: it is the keys' column, and a rule
	// there joined the chart to its own legend.
	first := strings.Index(row, "─")
	if name := strings.Index(row, "hosta"); first < 0 || first > name {
		first = name
	}
	lead := string([]rune(row[:first])[1:])
	if trimmed := strings.TrimSpace(strings.NewReplacer("gpu", "", " ", "").Replace(lead)); trimmed != "" {
		t.Errorf("the axis runs left of the first host: %q", row)
	}
	// Between two hosts: a gap of space, no rule across it.
	for _, pair := range [][2]string{{"hosta", "hostb"}, {"hostb", "hostc"}} {
		i, j := strings.Index(row, pair[0])+len(pair[0]), strings.Index(row, pair[1])
		if !strings.Contains(row[i:j], "   ") {
			t.Errorf("no gap between %s's and %s's axes: %q", pair[0], pair[1], row[i:j])
		}
	}
	// The hosts with more bars than name width do have an axis under them.
	if !strings.Contains(row, "─") {
		t.Errorf("no axis at all: %q", row)
	}
}

// TestSkylineBarsWalkTheirRamp: a full bar crosses its metric's ramp from the
// axis outward, half a cell at a time, and the two metrics stay apart.
//
// The bars were once one colour per row, which read as three stripes, and then
// one flat colour per metric, which read as no gradient at all. A full bar now
// shows twice as many shades as it has rows, each lighter than the one nearer
// the names, and its tip is the top of the ramp.
func TestSkylineBarsWalkTheirRamp(t *testing.T) {
	a := skyFleet(200, []skyHost{{"lab-a", 2, 100, 1}})
	c := a.fleetCanvas()
	axis := skyAxis(t, c, a.boardTop)
	col := -1
	for x := 0; x < c.W/2 && col < 0; x++ {
		if inSkyHost(a, x, axis) && c.at(x, axis-1).Text == "▄" {
			col = x
		}
	}
	if col < 0 {
		t.Fatal("no full utilisation bar drawn")
	}
	// Walk away from the axis, half a cell at a time: lower half then upper.
	var util, vram []theme.RGB
	for y := axis - 1; y > 0 && c.at(col, y).Text == "▄"; y-- {
		util = append(util, c.at(col, y).FG, c.at(col, y).BG)
	}
	for y := axis + 1; y < a.boardTop && c.at(col, y).Text == "▀"; y++ {
		vram = append(vram, c.at(col, y).FG, c.at(col, y).BG)
	}
	for name, run := range map[string][]theme.RGB{"util": util, "vram": vram} {
		if len(run) < 4 {
			t.Fatalf("%s: a full bar is only %d half-cells", name, len(run))
		}
		for i := 1; i < len(run); i++ {
			l0, _, _ := theme.OKLCHOf(run[i-1])
			l1, _, _ := theme.OKLCHOf(run[i])
			if l1 <= l0 {
				t.Errorf("%s: half-cell %d (%s) is not lighter than the one nearer the axis (%s)", name, i, run[i].Hex(), run[i-1].Hex())
			}
		}
		if d := theme.DeltaE(run[0], run[len(run)-1]); d < 10 {
			t.Errorf("%s: the bar's two ends are only ΔE %.1f apart; the gradient does not show", name, d)
		}
	}
	if tip := util[len(util)-1]; tip != a.skin.Scale.CPU.AtFraction(1) {
		t.Errorf("a full util bar ends at %s, want the ramp's top %s", tip.Hex(), a.skin.Scale.CPU.AtFraction(1).Hex())
	}
	// The two metrics are told apart at every height.
	for i := range min(len(util), len(vram)) {
		if theme.DeltaE(util[i], vram[i]) < 10 {
			t.Errorf("half-cell %d: util %s and vram %s are too close", i, util[i].Hex(), vram[i].Hex())
		}
	}
}

// TestPanelCountsBands: below the totals the cards are counted by utilisation
// band from idle up; "busy" is gone, and the bands with idle cover every
// readable card once ("≥" bands are cumulative, so the last band is a subset).
func TestPanelCountsBands(t *testing.T) {
	a := fixture(200, 50, []int{8, 4})
	head := strings.Join(strings.Split(a.PlainFrame(), "\n")[:a.boardTop], "\n")
	for _, want := range []string{"idle", "≥30%", "≥60%", "≥85%", "read"} {
		if !strings.Contains(head, want) {
			t.Errorf("the panel has no %q:\n%s", want, head)
		}
	}
	if strings.Contains(head, "busy") || strings.Contains(head, "full") {
		t.Errorf("the panel still has busy/full:\n%s", head)
	}
	fs := a.fleetStats()
	if fs.idle()+fs.band(idleBand) != fs.known {
		t.Errorf("idle %d + busy %d != read %d", fs.idle(), fs.band(idleBand), fs.known)
	}
	if !(fs.band(30) >= fs.band(60) && fs.band(60) >= fs.band(85)) {
		t.Error("the bands are not nested")
	}
}

// TestFailedCardLeadsWithTheReason: a failed host's line is the reason in the
// alert colour, then the message's detail a gap later in the secondary colour.
func TestFailedCardLeadsWithTheReason(t *testing.T) {
	a := fixture(200, 50, []int{4, 2})
	a.Deliver(sshxErr("hostb", "connection timed out: ssh: connect to host 192.0.2.9 port 22"))
	c := a.fleetCanvas()
	p := cardOf(t, a, "hostb")
	row := []rune(strings.Split(c.Plain(), "\n")[p.y+1])
	why := strings.Index(string(row), "connection timed out")
	detail := strings.Index(string(row), "ssh: connect")
	if why < 0 || detail < 0 {
		t.Fatalf("the failure is not on the card: %q", string(row))
	}
	wx, dx := len([]rune(string(row)[:why])), len([]rune(string(row)[:detail]))
	if strings.Contains(string(row), "out: ssh") {
		t.Errorf("the reason and the detail run together: %q", string(row))
	}
	tm := a.skin.Theme
	if got := c.at(wx, p.y+1).FG; got != tm.C("hi_fg") {
		t.Errorf("the reason is %s, want the alert colour", got.Hex())
	}
	if got := c.at(dx, p.y+1).FG; got != tm.C("graph_text") {
		t.Errorf("the detail is %s, want the secondary colour", got.Hex())
	}
}

// TestHostViewListsTheTopThree: a device box lists its three biggest processes
// and counts the rest, in its bottom border.
func TestHostViewListsTheTopThree(t *testing.T) {
	a := fixture(200, 50, []int{1})
	a.mu.Lock()
	g := &a.states["hosta"].sample.Gpus[0]
	for i, mb := range []uint64{100, 900, 300, 700, 500, 200} {
		g.Procs = append(g.Procs, wire.Proc{PID: 1000 + i, Name: "job", User: "u", MemMB: mb})
	}
	a.mu.Unlock()
	a.open("hosta")
	frame := a.detailCanvas().Plain()
	order := []string{"1001", "1003", "1004"} // 900, 700, 500
	last := -1
	for _, pid := range order {
		i := strings.Index(frame, pid)
		if i < 0 || i < last {
			t.Fatalf("the top three are not listed largest first (%s):\n%s", pid, frame)
		}
		last = i
	}
	for _, pid := range []string{"1000", "1002", "1005"} {
		if strings.Contains(frame, pid) {
			t.Errorf("process %s is listed beyond the top three", pid)
		}
	}
	if !strings.Contains(frame, "+3 more") {
		t.Errorf("the hidden processes are not counted:\n%s", frame)
	}
}

// TestHostResourcesHaveTheirOwnHues: the host's cpu and memory meters use
// neither the util, VRAM nor power colours.
func TestHostResourcesHaveTheirOwnHues(t *testing.T) {
	a := fixture(200, 50, []int{1})
	cpu, total := 60.0, uint64(64<<20)
	a.mu.Lock()
	a.states["hosta"].sample.CPUPct = &cpu
	a.states["hosta"].sample.MemKB, a.states["hosta"].sample.TotalKB = total/2, total
	a.mu.Unlock()
	a.open("hosta")
	c := a.detailCanvas()
	s := a.skin
	device := map[theme.RGB]bool{}
	for v := 0; v <= 100; v++ {
		device[s.Scale.CPU.At(float64(v))] = true
		device[s.Scale.VRAM.At(float64(v))] = true
		device[s.Scale.Power.At(float64(v))] = true
	}
	found := 0
	for x := 0; x < a.width; x++ {
		cl := c.at(x, 1)
		if cl.Text == "■" && !cl.Dim {
			found++
			if device[cl.FG] {
				t.Errorf("a host meter cell at %d is in a device meter's colour %s", x, cl.FG.Hex())
			}
		}
	}
	if found == 0 {
		t.Fatal("no host meter drawn")
	}
}

// TestStatusBarLeadsWithOpen: the status line starts with opening a host and
// names the history toggle for what it toggles.
func TestStatusBarLeadsWithOpen(t *testing.T) {
	lines := strings.Split(fixture(200, 50, []int{2}).PlainFrame(), "\n")
	status := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(status, "↵") || !strings.Contains(status, "open") {
		t.Errorf("the status line does not lead with open: %q", status)
	}
	if !strings.Contains(status, "history") {
		t.Errorf("the status line does not say history: %q", status)
	}
}

// TestScrollHintCountsHosts: "more" says how many hosts are below.
func TestScrollHintCountsHosts(t *testing.T) {
	a := fixture(200, 24, []int{8, 8, 8, 8, 8, 8})
	lines := strings.Split(a.PlainFrame(), "\n")
	status := lines[len(lines)-1]
	if !strings.Contains(status, "below") {
		t.Errorf("no count of hosts below: %q", status)
	}
}

// TestHelpExplainsTheReadings: the help panel says what the readings mean.
func TestHelpExplainsTheReadings(t *testing.T) {
	a := fixture(200, 50, []int{2})
	a.help = true
	help := a.helpCanvas().Plain()
	for _, want := range []string{"util history", "never a zero", "≥30%", "board", "host view", "mouse"} {
		if !strings.Contains(help, want) {
			t.Errorf("the help does not explain %q", want)
		}
	}
}

// TestModelColours: one hue per product line, a shade of it per model.
func TestModelColours(t *testing.T) {
	s := theme.NewSkin()
	models := []string{
		"NVIDIA GeForce RTX 4090", "NVIDIA GeForce RTX 5090", "NVIDIA GeForce RTX 3090",
		"NVIDIA A100-SXM4-80GB", "NVIDIA H100 80GB HBM3", "NVIDIA H200", "NVIDIA B200",
		"NVIDIA L40S", "Tesla T4", "NVIDIA RTX A6000",
	}
	seen := map[theme.RGB]string{}
	for _, m := range models {
		c := s.Model(m)
		if other, dup := seen[c]; dup {
			t.Errorf("%s and %s share %s", m, other, c.Hex())
		}
		seen[c] = m
	}
	hue := func(c theme.RGB) float64 { h, _, _ := theme.HSL(c); return h }
	dist := func(a, b float64) float64 {
		d := a - b
		if d < 0 {
			d = -d
		}
		if d > 180 {
			d = 360 - d
		}
		return d
	}
	g4, g5 := hue(s.Model(models[0])), hue(s.Model(models[1]))
	h2, b2 := hue(s.Model(models[5])), hue(s.Model(models[6]))
	if dist(g4, g5) >= dist(g4, h2) {
		t.Errorf("a 4090 is no closer to a 5090 (%.0f°) than to an H200 (%.0f°)", dist(g4, g5), dist(g4, h2))
	}
	if dist(h2, b2) >= dist(h2, g4) {
		t.Errorf("an H200 is no closer to a B200 (%.0f°) than to a 4090 (%.0f°)", dist(h2, b2), dist(h2, g4))
	}
}

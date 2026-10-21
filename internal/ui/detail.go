package ui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"ftop/internal/theme"
	"ftop/internal/wire"
)

// The detail view answers "what is this machine doing": per device, three gauges
// with their absolute values, the device's own history at full height, and every
// process with its owner. It is the only place processes appear.

// Detail geometry. The gauges are as short as the board's: a bar past ~14 cells
// adds no precision, and the width is worth more as history.
//
// A device box is the same height whatever is on the card: its border, the claim
// line, three gauges, the process table's rule and header, and two process rows. A
// box that grew with its process list moved every box below it when a job started,
// and two boxes side by side stopped lining up.
const (
	detailLabelW = 6  // "power "
	detailBarW   = 14 // gauge length
	detailGauges = 3  // util, vram, power
	minDetailCol = 90 // narrowest a device box may be in a two-column layout
	detailGap    = 3  // between elements, as on the board
	// minCmdW is the narrowest a process's command may be before the history
	// gives way: "python3 -u eval_harness.py" whole.
	minCmdW = 28
)

// The process table's columns, from the box's inner edge: the pid and the VRAM
// right-aligned, the user, and the command after them, which takes the rest.
const (
	procPidW  = 7
	procUserW = 10
	procMemW  = 7
	procCmdDX = procPidW + detailGap + procUserW + detailGap + procMemW + detailGap
)

// detailCols are the widths of the reading columns beside the gauges, so the
// readings of the three rows line up. Readings start at their column's left
// edge, so each is the same gap from the percentage before it.
//
// A column is as wide as the widest reading the host's devices can show in it:
// the widest value of each field they report ("140.4G/141.0G", "10.50GHz MEM"),
// or "—" with its unit for one they do not. Sized by what is reported rather
// than by every field there could be, the graph after them sits a gap from the
// last reading instead of past a reserve of empty cells; sized by each field's
// widest value rather than its current one, it does not move with the load.
// A column nothing fills is left out.
func detailCols(gpus []wire.Gpu) []int {
	known := func(v *float64, w, unknown int) int {
		if v == nil {
			return unknown
		}
		return w
	}
	cols := []int{0, 0}
	for _, g := range gpus {
		mem := 1 // "—"
		switch {
		case g.MemUsed != nil && g.MemTotal != nil:
			mem = 13 // "140.4G/141.0G"
		case g.MemUsed != nil:
			mem = 6
		}
		pwr := 1
		switch {
		case g.Power != nil && g.PowerCap != nil && *g.PowerCap > 0:
			pwr = 11 // "1000W/1000W"
		case g.Power != nil:
			pwr = 5
		}
		pcie := 0
		if g.PcieGen != nil && g.PcieWide != nil {
			pcie = 12 // "PCIe 5.0 ×16"
		}
		// The temperature and the fan are one reading, the heat and what is done
		// about it: "105°C fan 100%". As two columns the fan sat a whole memory
		// clock's width away from the temperature it answers.
		heat := known(g.Temp, 5, 0)
		if g.Fan != nil {
			heat = max(heat, 1) + 1 + 8 // "— fan 100%" when only the fan reports
		}
		// A field the card never reports takes no column: "—" in every box is a
		// measurement this host has none of, and its cells are the graph's.
		cols[0] = max(cols[0], known(g.ClockSM, 10, 0), mem, pwr)    // "9.99GHz SM"
		cols[1] = max(cols[1], heat, known(g.ClockMem, 12, 0), pcie) // "10.50GHz MEM"
	}
	for len(cols) > 0 && cols[len(cols)-1] == 0 {
		cols = cols[:len(cols)-1]
	}
	return cols
}

// maxProcs is how many of a device's processes a box lists, and how many rows it
// keeps for them. A card belongs to a device, not to a job, so the list is what is
// ON it: the top two by memory, the rest counted rather than listed. A list that
// grew with the job turned one box into half the screen and squeezed every other
// card off it; two rows hold the common cases, one job or a job and its helper.
const maxProcs = 2

// detailBoxHeight is a device box's height: border, the claim line, the gauges,
// the rule and header over the processes, and the process rows.
const detailBoxHeight = 2 + 1 + detailGauges + 2 + maxProcs

// detailCanvas draws one host's devices.
//
// The content is laid out on a canvas as tall as it needs to be, then blitted
// into the visible window at a scroll offset. Trying to scroll by shifting the
// y coordinate instead made every box's interior fall out of step with its
// border, because a box drawn from a negative row is clipped at the grid edge
// rather than from the middle.
func (a *App) detailCanvas() *Canvas {
	s := a.skin
	c := a.canvas(a.width, a.height, s.Bg)
	a.hits = a.hits[:0]

	a.cursor = min(max(a.cursor, 0), len(a.order)-1)
	h := a.states[a.order[a.cursor]]

	if a.procView {
		keys := []keyHint{{"p/esc", "close", "p"}, {"↑↓", "scroll", ""}, {"?", "help", "?"}, {"t", "theme", "t"}}
		procCanvas(c, a, h, s)
		a.drawStatusKeys(c, keys)
		return c
	}

	top := a.detailHeader(c, h)
	keys := []keyHint{{"esc", "back", "esc"}, {"?", "help", "?"}, {"←", "prev", "left"}, {"→", "next", "right"}, {"↑↓", "scroll", ""}, {"p", "processes", "p"}}
	if h.err != "" || !h.hasData {
		// Nothing to scroll: a page key pressed now must not leave the host's
		// first sample opening part-way down.
		a.detailScroll, a.detailView = 0, 0
		a.drawStatusKeys(c, keys)
		return c
	}

	gpus := h.sample.Gpus
	procs := make([][]wire.Proc, len(gpus))
	heights := make([]int, len(gpus))
	for i, g := range gpus {
		procs[i] = dedupeProcs(g.Procs)
		heights[i] = detailBoxHeight
	}
	cols := 1
	if a.width >= 2*minDetailCol+1 && len(gpus) > 1 {
		cols = 2
	}
	// The same spacing as the board: a column between the two columns, a row
	// between stacked boxes.
	lane, ys, total := fixedColumns(heights, cols, stackGap)
	xs, ws := gutterSpans(a.width, cols, 1)

	body := a.bodyCanvas(a.width, max(total, 1), s.Bg)
	buf := make([]theme.Cell, 0, detailBarW+1)
	readCols := detailCols(gpus)
	now := a.clock()
	for i, g := range gpus {
		hv, held := a.holders[ledgerKey(h.name, g)]
		cl := claim{h: hv, held: held, lamp: lampFor(hv, held, wireGPU{util: g.UtilPct()}), now: now, read: a.ledgerAt}
		detailBox(body, xs[lane[i]], ys[i], ws[lane[i]], heights[i], g, h.gpuHist[gpuKey(g)], procs[i], h.missing[gpuKey(g)], readCols, s, buf, cl)
	}

	top += stackGap
	viewH := max(a.height-1-top, 0)
	a.detailView = viewH
	maxScroll := max(total-viewH, 0)
	a.detailScroll = min(max(a.detailScroll, 0), maxScroll)
	c.Blit(body, 0, a.detailScroll, 0, top, a.width, viewH)

	a.drawStatusKeys(c, keys)
	a.drawScrollHint(c, a.detailScroll, maxScroll)
	return c
}

// detailHeader draws the host box: name and GPU summary in the border, then the
// machine's own resources — CPU, memory and swap meters with their facts, the
// three families of history beside them, and the busiest processes by memory.
// Returns the first free row.
//
// This box is the one place host facts are answered at length. The board's host
// row carries two percentages; here there is room for what explains them: cores,
// load, the memory limit and its reclaimable cache, swap, uptime, and who is
// holding the memory.
func (a *App) detailHeader(c *Canvas, h *hostState) int {
	s := a.skin
	t := s.Theme
	glyph, failed := hostStateGlyph(h)
	titleFG := t.C("title")
	if failed {
		titleFG = t.C("hi_fg")
	}
	sub := ""
	if h.hasData && h.err == "" {
		sub = gpuNameShort(h)
		if h.sample.Driver != "" {
			sub += " · driver " + h.sample.Driver
		}
	}
	const y0 = 0
	var right []seg
	if sub != "" {
		right = []seg{{sub, t.C("main_fg"), false}}
	}
	// Border, four rows of facts, border. The histories' names go in the bottom
	// border under their own graphs, the way every column name on the board does,
	// rather than in a row of their own.
	const boxH = 6
	c.titledBox(0, y0, a.width, boxH, cardHue(s, h),
		[]seg{{glyph, glyphColor(t, h), false}, {" " + h.label, titleFG, true}}, right)

	snap := h.sample
	if h.err != "" {
		c.errorLine(2, y0+1, a.width-4, h.err, t)
		return y0 + boxH
	}
	if !h.hasData {
		c.Text(2, y0+1, "waiting for first sample…", t.C("graph_text"))
		return y0 + boxH
	}

	const (
		hostLabelW = 5 // "swap "
		hostMeterW = 12
	)
	x0, xr := 2, a.width-2
	factsX := x0 + hostLabelW + hostMeterW + 1 + pctW + detailGap
	label, value, unit := t.C("graph_text"), t.C("main_fg"), t.C("graph_text")

	// The graphs take the right end, three of them equal width, and only once the
	// facts beside them keep a readable column. Below that two are shown, and
	// below that none: a two-cell graph is a decoration, not a history.
	// A host with no swap has no swap history to draw: two graphs, not three
	// empty ones, and the box says "none" on the meter's row instead.
	want := 3
	if !snap.SwapKnown {
		want = 2
	}
	// factsNeed is the widest fact row: cores, the cgroup note and the three
	// load averages. The graphs take what is left, within a cap — a strip wider
	// than ~30 cells shows the same ten minutes at a size no reader scans — so a
	// wide host box does not leave a field of empty cells between its facts and
	// its history.
	const factsNeed, maxGraph = 52, 30
	graphW, graphs := 0, 0
	for _, n := range []int{want, 2} {
		if w := (xr - factsX - factsNeed - (n-1)*detailGap) / n; w >= 14 {
			graphW, graphs = min(w, maxGraph), n
			break
		}
	}
	// A narrower terminal still takes two graphs when they can be short.
	if graphs == 0 && factsX+28+2*(minSpark+detailGap) <= xr {
		graphW, graphs = minSpark, 2
	}
	graphsX := xr
	if graphs > 0 {
		graphsX = xr - (graphs*graphW + (graphs-1)*detailGap)
	}

	// meter draws one host resource: its name, its meter, its percentage. A
	// resource with no reading keeps its name and shows a dash, so a row never
	// disappears and the box does not change height with the load.
	meter := func(row int, name string, pct float64, sc theme.RGBScale) {
		y := y0 + row
		c.Text(x0, y, name, sc.At(65))
		bx := x0 + hostLabelW
		px := bx + hostMeterW + 1
		if pct < 0 {
			c.Text(px, y, "—", t.C("inactive_fg"))
			return
		}
		v := int(math.Round(pct))
		for i, cell := range (theme.Meter{Width: hostMeterW, Scale: &sc}).Render(t, v) {
			c.PutCell(bx+i, y, cell)
		}
		writePct(c, px, y, v, sc.At(float64(v)), t)
	}
	// fact writes a value and its unit, and returns the column after it. Nothing
	// is written past the graphs: a fact that would overlap them is left out whole.
	fact := func(cx, row int, v, u string, fg theme.RGB) int {
		if v == "" {
			return cx
		}
		w := theme.RuneWidth(v) + theme.RuneWidth(u)
		if cx+w > graphsX-detailGap {
			return cx
		}
		end := c.Text(cx, y0+row, v, fg)
		end = c.Text(end, y0+row, u, unit)
		return end
	}

	// cpu: the percentage, the denominator it is a percentage OF, and the load,
	// which is the machine's and not the cgroup's.
	cpuPct := snap.CPUPctValue()
	meter(1, "cpu", cpuPct, s.Scale.HostCPU)
	cx := fact(factsX, 1, fmt.Sprintf("%d", snap.Cores), " cores", value)
	if snap.CPUQuota {
		// Which scope this percentage is over. Without it a 100% on a 32-core
		// quota and a 100% on 128 physical cores look like the same fact.
		cx = fact(cx+detailGap, 1, "cgroup quota", "", label)
	}
	// "load 32.00 29.44 27.20": the word names the three numbers that follow,
	// which is the order they are read in.
	if cx+detailGap+len("load 32.00 29.44 27.20") <= graphsX-detailGap {
		cx = c.Text(cx+detailGap, y0+1, "load", label)
		cx = c.Text(cx+1, y0+1, fmt.Sprintf("%.2f %.2f %.2f", snap.Load1, snap.Load5, snap.Load15), value)
	}

	// mem: used of the limit, and the cache the used figure already excludes, so
	// "why does the machine look full but the job not?" has an answer on screen.
	memPct := snap.MemPct()
	meter(2, "mem", memPct, s.Scale.Mem)
	if snap.TotalKB > 0 {
		cx = fact(factsX, 2, kibText(snap.MemKB), "/"+kibText(snap.TotalKB), s.Scale.Mem.At(memPct))
		cx = fact(cx+detailGap, 2, "cache "+kibText(snap.CacheKB), "", label)
	} else {
		cx = fact(factsX, 2, "—", "", t.C("inactive_fg"))
	}
	if !snap.MemLimited {
		cx = fact(cx+detailGap, 2, "whole machine", "", label)
	}

	// swap: a host with none says so rather than drawing a meter of zeroes.
	swapPct := snap.SwapPct()
	if snap.SwapKnown {
		meter(3, "swap", swapPct, s.Scale.Swap)
		cx = fact(factsX, 3, kibText(snap.SwapKB), "/"+kibText(snap.SwapTotalKB), value)
	} else {
		c.Text(x0, y0+3, "swap", label)
		cx = fact(factsX, 3, "none", "", label)
	}
	cx = fact(cx+detailGap, 3, fmt.Sprint(snap.Procs), " procs", value)
	if snap.Uptime > 0 {
		cx = fact(cx+detailGap, 3, uptimeText(snap.Uptime), " up", value)
	}

	// The busiest processes by resident memory: a GPU list says who is on a card,
	// never who is holding the machine's RAM. The whole row opens the full table.
	tops := topByRSS(h.hostProcs, 3)
	c.Text(x0, y0+4, "top", label)
	// The histories occupy rows 1..3 only, so this row runs the full width: the
	// limitation that keeps facts clear of the graphs does not apply to it.
	cx = factsX
	for i, p := range tops {
		if cx >= xr-detailGap {
			break
		}
		if i > 0 {
			cx = c.Text(cx, y0+4, " · ", label)
		}
		cx = fact(cx, 4, theme.Clip(p.Name, 28), "", t.C("main_fg"))
		cx = fact(cx+1, 4, theme.MiB(p.RSSKB/1024), "", s.Scale.Mem.At(55))
	}
	if len(tops) == 0 {
		msg := "process list not sampled"
		if !h.hostProcsStale && len(h.hostProcs) == 0 {
			msg = "—"
		}
		c.Text(factsX, y0+4, msg, label)
	}
	if len(h.hostProcs) > 0 {
		// The hint follows the list it extends, not the graphs at the far right:
		// it names the same fact as "top" and reads as part of that row.
		hint := "p all"
		if snap.Procs > 0 {
			hint = fmt.Sprintf("p all %d", snap.Procs)
		}
		// Right after the list when it fits, otherwise at the row's right end: the
		// way into the full table must not disappear on a host with wide names.
		x := cx + detailGap
		if x+theme.RuneWidth(hint) > xr-detailGap {
			x = xr - detailGap - theme.RuneWidth(hint)
		}
		if x > cx && x+theme.RuneWidth(hint) <= xr-detailGap {
			c.Text(x, y0+4, hint, t.C("title"))
			a.hits = append(a.hits, hit{x: x, y: y0 + 4, w: theme.RuneWidth(hint), key: "p"})
		}
		a.hits = append(a.hits, hit{x: x0, y: y0 + 4, w: max(xr-x0, 1), key: "p"})
	}

	// The histories, one per resource, each in that resource's ramp: the graph
	// beside a meter is that meter extended back in time. Each is named under
	// itself — three unlabelled strips leave the reader guessing which is which,
	// and the order of the meters is a convention, not a label.
	if graphs > 0 {
		type series struct {
			name string
			data []float64
			sc   theme.RGBScale
		}
		list := []series{
			{"cpu", h.cpuHist, s.Scale.HostCPU},
			{"mem", h.memHist, s.Scale.Mem},
		}
		if graphs == 3 {
			list = append(list, series{"swap", h.swapHist, s.Scale.Swap})
		}
		for i := 0; i < graphs; i++ {
			gx := graphsX + i*(graphW+detailGap)
			// Three rows: the four facts rows are the box's interior, and a graph
			// that reached the fourth would take width from the "top" row, which
			// runs the full way across.
			rows := theme.Graph(t, list[i].data, graphW, 3, "", theme.GraphOpts{NoZero: true, Scale: &list[i].sc})
			for r := range rows {
				for j, cell := range rows[r] {
					c.Put(gx+j, y0+1+r, cell.Text, cell.FG)
				}
			}
			// The name sits in the border under its own graph: three strips of
			// braille with no label leave the reader guessing which is which, and
			// the meters' order above is a convention, not a label.
			c.lineLabel(gx, y0+boxH-1, list[i].name, list[i].sc.At(70))
		}
	}

	// A stale sample shown as current is the lie this whole view exists to avoid.
	if h.fresh() > 5*time.Second {
		c.BoxFooter(0, y0, a.width, boxH, t.C("hi_fg"), fmt.Sprintf("sample %.0fs old", h.fresh().Seconds()))
	}
	return y0 + boxH
}

// kibText formats a KiB reading (the wire's unit for host memory) as "64.0G".
func kibText(kb uint64) string { return theme.MiB(kb / 1024) }

// uptimeText is "12d", "4h", "17m".
func uptimeText(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// topByRSS returns up to n processes, biggest resident set first. Ties go to the
// lower pid so the list does not shuffle between walks.
func topByRSS(ps []wire.HostProc, n int) []wire.HostProc {
	out := append([]wire.HostProc(nil), ps...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RSSKB != out[j].RSSKB {
			return out[i].RSSKB > out[j].RSSKB
		}
		return out[i].PID < out[j].PID
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// claim is what the ledger says about one card, ready to draw: the holder if
// there is one, the card's lamp at the frame's moment, and when the ledger was
// read, which is the "today" a claim's time is told against. The frame's own
// clock is a fixed moment in a snapshot, and would date every claim as long ago.
type claim struct {
	h    Holder
	held bool
	lamp lampState
	now  time.Time
	read time.Time
}

// threadIDW is how much of a thread's id the claim line prints: the first eight
// characters, which is how both runtimes' own lists abbreviate a conversation.
const threadIDW = 8

// threadID is the conversation part of a claim's "kind/thread" owner, cut to
// threadIDW cells without an ellipsis — a prefix of an id, the way a short commit
// hash is. An owner with no thread (an agent whose runtime names none) is shown
// as recorded.
func threadID(owner string) string {
	id := owner
	if i := strings.IndexByte(owner, '/'); i >= 0 && i+1 < len(owner) {
		id = owner[i+1:]
	}
	var b strings.Builder
	used := 0
	theme.EachGlyph(id, func(g theme.Glyph) bool {
		if used+g.W > threadIDW {
			return false
		}
		b.WriteString(g.Text)
		used += g.W
		return true
	})
	return b.String()
}

// sinceText is when a claim was taken, as a reader's clock shows it: "14:02" the
// same day, "Oct 19" before. A duration ("2h") read as a countdown as often as an
// age; a time of day cannot be misread.
func sinceText(since, now time.Time) string {
	since, now = since.Local(), now.Local()
	y1, m1, d1 := since.Date()
	y2, m2, d2 := now.Date()
	if y1 == y2 && m1 == m2 && d1 == d2 {
		return since.Format("15:04")
	}
	return since.Format("Jan 2")
}

// claimLine is a device box's first line: who holds the card, as the ledger
// records it, or that nobody does.
//
// A held card shows its lamp, the holding thread's id in its runtime's brand
// colour, why it was claimed, and since when. The id is what a reader takes to
// their agent's list to find the conversation; the runtime's name the line used
// to show ("held · claude-code") only said which kind of agent to look among, and
// on a fleet run by one kind it said the same thing on every card.
//
// A card nobody holds says which of two things that is: "free" when nothing runs
// on it, "unclaimed" when something does — a job the ledger knows nothing about,
// which is the warning the next agent needs before it takes the card.
func claimLine(c *Canvas, x, y, w int, cl claim, running bool, t *theme.Theme) {
	glyph, fg := lampCell(cl.lamp, cl.now)
	c.Put(x, y, glyph, fg)
	tx, end := x+2, x+w
	if !cl.held {
		if running {
			c.Text(tx, y, "unclaimed", t.C("warn"))
		} else {
			c.Text(tx, y, "free", t.C("graph_text"))
		}
		return
	}
	// The time takes the line's right end, so the reasons of a column of boxes
	// start and stop in the same places. An overdue claim says so before it: the
	// lamp has already turned to the warning colour, and the word says why.
	var right []seg
	if !cl.h.Since.IsZero() {
		right = []seg{{"since " + sinceText(cl.h.Since, cl.read), t.C("graph_text"), false}}
	}
	if cl.h.Overdue {
		if len(right) > 0 {
			right = append([]seg{{" · ", t.C("graph_text"), false}}, right...)
		}
		right = append([]seg{{"overdue", t.C("warn"), false}}, right...)
	}
	rx := end - segsWidth(right)
	cx := c.TextBold(tx, y, threadID(cl.h.Owner), theme.Default.AgentColour(kindOf(cl.h.Owner)))
	// The reason is another agent's text: drawn, clipped, never parsed. It is the
	// line's longest part and the first to give way.
	if why := cl.h.Why; why != "" && rx-detailGap-(cx+detailGap) >= 8 {
		c.Text(cx+detailGap, y, theme.Clip(why, rx-detailGap-(cx+detailGap)), t.C("main_fg"))
	}
	if rx > cx+detailGap {
		for _, sg := range right {
			rx = c.Text(rx, y, sg.text, sg.fg)
		}
	}
}

// detailBox draws one device: who holds it on the first line, three gauges with
// their readings under it, the process table below them, and the device's history
// down the right side beside both.
func detailBox(c *Canvas, x, y, w, h int, g wire.Gpu, hist []float64, procs []wire.Proc, missing bool, cols []int, s *theme.Skin, buf []theme.Cell, cl claim) {
	t := s.Theme
	label, value, unit := t.C("graph_text"), t.C("main_fg"), t.C("graph_text")
	// A device the latest sample omitted keeps its last process list; say so, or
	// the list reads as current.
	border := s.Hue.Frame
	var right []seg
	if missing {
		border = s.Hue.Warn
		right = []seg{{"not reported · processes last seen", t.C("hi_fg"), false}}
	}
	c.titledBox(x, y, w, h, border,
		[]seg{{fmt.Sprintf("%d · %s", g.Index, theme.ShortGPU(g.Name)), t.C("title"), true}},
		right)

	x0, xr := x+2, x+w-2 // the content's edges
	claimLine(c, x0, y+1, xr-x0, cl, len(procs) > 0, t)

	// readingX is where the reading columns start: after the label, the gauge
	// and its percentage.
	readingX := x0 + detailLabelW + detailBarW + 1 + pctW + detailGap
	// row draws a gauge and its readings in the reading columns, each a value in
	// its own colour and a dimmer unit. A reading that would reach the border is
	// left out whole.
	row := func(r int, name string, pct float64, sc theme.RGBScale, readings ...reading) {
		yy := y + 2 + r
		c.Text(x0, yy, name, label)
		cx := x0 + detailLabelW
		px := cx + detailBarW + 1
		if pct < 0 {
			c.Text(px, yy, "—", t.C("inactive_fg"))
		} else {
			v := int(math.Round(pct))
			for i, cell := range (theme.Meter{Width: detailBarW, Scale: &sc}).Append(buf, t, v) {
				c.PutCell(cx+i, yy, cell)
			}
			writePct(c, px, yy, v, sc.At(float64(v)), t)
		}
		vx, col, end := readingX, 0, readingX
		for _, rd := range readings {
			v, u, fg := rd.v, rd.u, rd.fg
			if v == "—" {
				fg, u = t.C("inactive_fg"), ""
			}
			if rd.follow {
				// Part of the reading before it, a space after it, inside the
				// same column.
				if v != "" && col > 0 && end+1+theme.RuneWidth(v)+theme.RuneWidth(u) <= xr {
					ux := c.Text(end+1, yy, v, fg)
					end = c.Text(ux, yy, u, unit)
				}
				continue
			}
			if col >= len(cols) {
				break
			}
			if theme.RuneWidth(v)+theme.RuneWidth(u) > cols[col] {
				// Only an absurd reading is wider than its column. Cut it, unit
				// first, rather than run into the next one.
				v, u = theme.Clip(v, cols[col]), ""
			}
			end = vx
			if v != "" && vx+theme.RuneWidth(v)+theme.RuneWidth(u) <= xr {
				ux := c.Text(vx, yy, v, fg)
				end = c.Text(ux, yy, u, unit)
			}
			vx += cols[col] + detailGap
			col++
		}
	}

	mem := g.MemPct()
	if mem >= 0 {
		mem *= 100
	}
	pwr := g.PowerPct()
	if pwr >= 0 {
		pwr *= 100
	}
	if g.Power == nil || g.PowerCap == nil || *g.PowerCap <= 0 {
		pwr = -1
	}
	tempFG := value
	if g.Temp != nil {
		tempFG = t.Temp(*g.Temp)
	}
	// Each gauge carries the readings about the same thing, so a row reads as one
	// fact: the compute clock and the link beside utilisation, used of total and
	// the memory clock beside VRAM, and the watts with the heat they make — the
	// temperature and the fan — beside power. The first column is each gauge's own
	// absolute reading, in the gauge's colour where it has one.
	//
	// A reading the card does not report is left out rather than printed as a
	// dash in every box: a column of "— MEM" says nothing about this host, and
	// the width it takes is the history's.
	row(0, "util", g.UtilPct(), s.Scale.CPU,
		reading{v: fmtMHz(g.ClockSM), u: " SM", fg: value},
		reading{v: fmtPCIe(g.PcieGen, g.PcieWide), fg: value})
	row(1, "vram", mem, s.Scale.VRAM,
		reading{v: fmtMem(g.MemUsed, g.MemTotal), fg: s.Scale.VRAM.At(max(mem, 0))},
		dropUnknownReading(reading{v: fmtMHz(g.ClockMem), u: " MEM", fg: value}))
	row(2, "power", pwr, s.Scale.Power,
		reading{v: fmtPower(g.Power, g.PowerCap), fg: s.Scale.Power.At(max(pwr, 0))},
		reading{v: fmtTemp(g.Temp), fg: tempFG},
		reading{v: fmtFan(g.Fan), fg: value, follow: true})

	// Process table columns, named in a header row over them. A dashed rule above
	// the header ends the gauges: without it the header read as a fourth gauge
	// with no bar. The names were in the bottom border once, the way the board
	// names its columns, but there they sat far below rows they named.
	pidX := x0
	userX := pidX + procPidW + detailGap
	memX := userX + procUserW + detailGap
	cmdX := x0 + procCmdDX

	// History down the right side, beside the gauges and the process table, so its
	// floor is the box's bottom rather than the middle of it. The claim line runs
	// above it: the reason is the line's longest part and needs the whole width.
	// It starts after the last reading, and never before a command has room to be
	// read: a graph that took the cells cut every job name to a stub.
	gx := readingX
	for _, cw := range cols {
		gx += cw + detailGap
	}
	gx = max(gx, cmdX+minCmdW+detailGap)
	bottom := y + h - 1
	cmdEnd := xr
	graphed := false
	if gw := xr - gx; gw >= 12 {
		rows := theme.Graph(t, hist, gw, h-3, "", theme.GraphOpts{NoZero: true, Scale: &s.Scale.CPU})
		for r := range rows {
			for i, cell := range rows[r] {
				c.Put(gx+i, y+2+r, cell.Text, cell.FG)
			}
		}
		cmdEnd, graphed = gx-detailGap, true
	}

	py := y + 2 + detailGauges
	if graphed {
		c.legendLabel(gx, bottom, "util history", s)
	}
	for xx := x0; xx < cmdEnd; xx++ {
		c.Put(xx, py, "┄", s.Hue.Frame)
	}
	py++
	c.TextRight(pidX+procPidW, py, "Pid", label)
	c.Text(userX, py, "User", label)
	c.TextRight(memX+procMemW, py, "VRAM", label)
	c.Text(cmdX, py, theme.Clip("Command", cmdEnd-cmdX), label)
	py++
	if len(procs) == 0 {
		msg := "no processes"
		if g.ProcsStale {
			msg = "process list not sampled"
		}
		c.Text(cmdX, py, theme.Clip(msg, cmdEnd-cmdX), t.C("graph_text"))
		return
	}
	list := procs
	if len(list) > maxProcs {
		list = list[:maxProcs]
	}
	for _, p := range list {
		c.TextRight(pidX+procPidW, py, fitExact(fmt.Sprint(p.PID), procPidW), t.C("graph_text"))
		// The owner is text, not a state: saturated green said "healthy" about a
		// user name.
		c.Text(userX, py, theme.Clip(p.User, procUserW), t.C("main_fg"))
		if p.MemMB > 0 {
			pct := 0
			if g.MemTotal != nil && *g.MemTotal > 0 {
				pct = int(float64(p.MemMB) / float64(*g.MemTotal) * 100)
			}
			c.TextRight(memX+procMemW, py, fitExact(theme.MiB(p.MemMB), procMemW), s.Scale.VRAM.At(float64(50+pct/2)))
		} else {
			// Some drivers report per-process VRAM as 0 while the device is full.
			// Printing "0M" would state a measurement never taken.
			c.TextRight(memX+procMemW, py, "—", t.C("inactive_fg"))
		}
		c.Text(cmdX, py, theme.Clip(p.Name, cmdEnd-cmdX), t.C("main_fg"))
		py++
	}
	if rest := len(procs) - len(list); rest > 0 {
		// Say what is not shown, in the bottom border under the commands: a
		// silently truncated list reads as a card with two jobs on it.
		more := fmt.Sprintf("+%d more", rest)
		if !graphed || cmdX+theme.RuneWidth(more) < gx-1 {
			c.lineLabel(cmdX, bottom, more, t.C("graph_text"))
		}
	}
}

// fixedColumns places item i in column i%n, stacked with gap rows between
// items. It returns each item's column and top, and the height of the tallest
// column.
//
// The column is fixed by the device index, not by which column is shorter. Box
// heights follow process counts, so a shortest-column placement moved GPU 2 from
// one side to the other whenever a job started on GPU 1.
func fixedColumns(heights []int, n, gap int) (lane, y []int, total int) {
	cols := make([]int, n)
	lane = make([]int, len(heights))
	y = make([]int, len(heights))
	for i, h := range heights {
		col := i % n
		if cols[col] > 0 {
			cols[col] += gap
		}
		lane[i], y[i] = col, cols[col]
		cols[col] += h
	}
	for _, v := range cols {
		total = max(total, v)
	}
	return lane, y, total
}

func fmtMHz(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.2fGHz", *v/1000)
}

func fmtTemp(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f°C", *v)
}

func fmtFan(v *float64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("fan %.0f%%", *v)
}

func fmtMem(used, total *uint64) string {
	switch {
	case used == nil:
		return "—"
	case total == nil:
		return theme.MiB(*used)
	}
	return theme.MiB(*used) + "/" + theme.MiB(*total)
}

func fmtPower(p, capw *float64) string {
	switch {
	case p == nil:
		return "—"
	case capw == nil || *capw <= 0:
		return fmt.Sprintf("%.0fW", *p)
	}
	return fmt.Sprintf("%.0fW/%.0fW", *p, *capw)
}

func fmtPCIe(gen, width *int) string {
	if gen == nil || width == nil {
		return ""
	}
	return fmt.Sprintf("PCIe %d.0 ×%d", *gen, *width)
}

// plural is "1 proc", "3 procs".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// dedupeProcs collapses the same pid listed once per CUDA context.
func dedupeProcs(ps []wire.Proc) []wire.Proc {
	seen := make(map[int]struct{}, len(ps))
	out := make([]wire.Proc, 0, len(ps))
	for _, p := range ps {
		if _, dup := seen[p.PID]; dup {
			continue
		}
		seen[p.PID] = struct{}{}
		out = append(out, p)
	}
	// Biggest first, so the few a box lists are the ones that matter. Ties go
	// to the lower pid, so a list of equal users does not reorder between samples.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MemMB != out[j].MemMB {
			return out[i].MemMB > out[j].MemMB
		}
		return out[i].PID < out[j].PID
	})
	return out
}

// reading is one value in a device box's reading columns: the number, its unit,
// and the number's colour. A reading that follows is part of the one before it
// and shares its column ("40°C fan 30%").
type reading struct {
	v, u   string
	fg     theme.RGB
	follow bool
}

// dropUnknownReading is a reading with itself cleared when the card did not
// report it, so the column it would sit in collapses.
func dropUnknownReading(r reading) reading {
	if r.v == "—" {
		return reading{}
	}
	return r
}

// ---- the host process view -------------------------------------------------

// procCanvas is the full process table, as a screen of its own in the help
// screen's idiom: the host view behind recedes and one panel answers who is
// using the machine.
//
// Two tables side by side, by memory and by cpu, because those are two different
// answers and a train job is usually first in one and not the other. The GPU
// column is joined from the card process lists by pid, which is the one place
// "who holds the RAM" and "who holds the cards" meet.
func procCanvas(c *Canvas, a *App, h *hostState, s *theme.Skin) {
	t := s.Theme
	x0, y0 := 0, 0
	// The panel is the whole screen below the top border: this is a table, and a
	// table with a box inside a box loses a column to every border.
	w, hgt := a.width, a.height
	title := h.label + " · processes"
	sub := fmt.Sprintf("%d on the host · %d listed", h.sample.Procs, len(h.hostProcs))
	if h.hostProcsStale {
		sub += " · not sampled this round"
	}
	c.titledBox(x0, y0, w, hgt, s.Hue.Frame,
		[]seg{{title, t.C("title"), true}},
		[]seg{{sub, t.C("graph_text"), false}})
	if h.err != "" || !h.hasData {
		c.Text(2, 1, "no process data", t.C("graph_text"))
		return
	}

	// The two tables split the width; each gives command the rest.
	inner := w - 2
	colGap := 3
	leftW := (inner - colGap) / 2
	rightW := inner - colGap - leftW
	rows := topByRSS(h.hostProcs, len(h.hostProcs))
	byCPU := append([]wire.HostProc(nil), h.hostProcs...)
	sort.SliceStable(byCPU, func(i, j int) bool {
		ci, cj := 0.0, 0.0
		if byCPU[i].CPUPct != nil {
			ci = *byCPU[i].CPUPct
		}
		if byCPU[j].CPUPct != nil {
			cj = *byCPU[j].CPUPct
		}
		if ci != cj {
			return ci > cj
		}
		return byCPU[i].PID < byCPU[j].PID
	})

	gpuOf := make(map[int][]int, len(rows))
	for _, g := range h.sample.Gpus {
		for _, p := range g.Procs {
			gpuOf[p.PID] = append(gpuOf[p.PID], g.Index)
		}
	}

	visible := max(hgt-4, 1)
	// One scroll offset for both tables: they are two orders of the same list,
	// and scrolling them apart would put row n of one beside row m of the other.
	maxScroll := max(max(len(rows), len(byCPU))-visible, 0)
	a.procScroll = min(max(a.procScroll, 0), maxScroll)

	table := func(x, w int, head string, list []wire.HostProc) {
		label, value := t.C("graph_text"), t.C("main_fg")
		c.Text(x, 1, head, t.C("title"))
		// Columns: Pid, User, RSS, CPU%, GPU, Command.
		const pidW, userW, rssW, cpuW, gpuW = 7, 12, 7, 5, 4
		px := x
		ux := px + pidW + 1
		rx := ux + userW + 1
		cx := rx + rssW + 1
		cpuX := cx + cpuW + 1
		gpuX := cpuX + cpuW + 1
		cmdX := gpuX + gpuW + 1
		// One cell short of the table's edge: a name that ends against the panel's
		// border reads as cut off even when it is whole.
		cmdW := max(x+w-cmdX-1, 0)
		c.TextRight(px+pidW, 2, "Pid", label)
		c.Text(ux, 2, "User", label)
		c.TextRight(rx+rssW, 2, "RSS", label)
		// Both orders show the CPU column, so both name it: an unlabelled column
		// of percentages is a reading the reader has to guess at.
		c.TextRight(cpuX+cpuW, 2, "CPU", label)
		c.TextRight(gpuX+gpuW, 2, "GPU", label)
		c.Text(cmdX, 2, theme.Clip("Command", cmdW), label)
		for i := 0; i < visible; i++ {
			j := a.procScroll + i
			if j >= len(list) {
				break
			}
			p := list[j]
			y := 3 + i
			c.TextRight(px+pidW, y, fitExact(fmt.Sprint(p.PID), procPidW), label)
			c.Text(ux, y, theme.Clip(p.User, procUserW), value)
			c.TextRight(rx+rssW, y, fitExact(theme.MiB(p.RSSKB/1024), rssW), t.C("main_fg"))
			if p.CPUPct == nil {
				c.TextRight(cpuX+cpuW, y, "—", t.C("inactive_fg"))
			} else {
				// Percent of one core, as top reports it: 400% is four cores.
				v := int(*p.CPUPct + 0.5)
				c.TextRight(cpuX+cpuW, y, fmt.Sprintf("%d%%", v), s.Scale.CPU.At(min(float64(v), 100)))
			}
			if gs := gpuOf[p.PID]; len(gs) > 0 {
				txt := fmt.Sprint(gs[0])
				for _, g := range gs[1:] {
					txt += "," + fmt.Sprint(g)
				}
				c.TextRight(gpuX+gpuW, y, theme.Clip(txt, gpuW), t.C("title"))
			} else {
				c.TextRight(gpuX+gpuW, y, "·", t.C("inactive_fg"))
			}
			c.Text(cmdX, y, theme.Clip(p.Name, cmdW), value)
		}
	}
	table(x0+2, leftW, "by memory", rows)
	// The divider is the border colour, not a box: the two tables are one panel.
	for y := 1; y < hgt-1; y++ {
		c.Put(x0+2+leftW+colGap/2, y, "│", t.C("div_line"))
	}
	table(x0+2+leftW+colGap, rightW, "by cpu", byCPU)

	if maxScroll > 0 {
		hint := fmt.Sprintf("↕ %d-%d of %d", a.procScroll+1, min(a.procScroll+visible, len(rows)), len(rows))
		c.TextRight(w-2, hgt-1, hint, t.C("graph_text"))
	}
}

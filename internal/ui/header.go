package ui

import (
	"fmt"
	"math"
	"time"

	"ftop/internal/theme"
)

// fleetStats is the fleet summary every header draws from.
//
// Unknown readings are excluded rather than counted as zero: a fleet of
// unreadable cards must not read as idle in the one place that summarises it.
type fleetStats struct {
	hosts, online, dead, pending int
	gpus, busy, known            int
	utils                        []float64 // every readable card's utilisation
	util                         float64   // mean over readable cards; -1 unknown
	memUsed, memTotal            float64   // MiB, over cards reporting both
	pwr, capw                    float64   // W, over cards reporting both
	pwrOnly                      float64   // W drawn by cards with no cap reported
	pwrOnlyN                     int       // how many such cards; 0 W is a reading

	// The hosts' own two readings. CPU is a ratio of busy cores to cores, never
	// a mean of percentages: a 64-core host at 5% and an 8-core host at 95% do
	// not average to anything a reader could act on.
	busyCores, cores float64
	hostsCPU         int
	memKB, totalKB   float64
	hostsMem         int
}

// fleetStats is taken over the hosts the board draws: a laptop the settings hide
// must not be the "1 down" in a header whose chart does not show it.
func (a *App) fleetStats() fleetStats {
	shown := a.sortedHosts()
	fs := fleetStats{hosts: len(shown), util: -1}
	var utilSum float64
	for _, st := range shown {
		switch {
		case st.err != "":
			fs.dead++
			continue
		case !st.hasData:
			fs.pending++
			continue
		}
		fs.online++
		if cpu := st.sample.CPUPctValue(); cpu >= 0 && st.sample.Cores > 0 {
			fs.busyCores += cpu / 100 * float64(st.sample.Cores)
			fs.cores += float64(st.sample.Cores)
			fs.hostsCPU++
		}
		if st.sample.TotalKB > 0 {
			fs.memKB += float64(st.sample.MemKB)
			fs.totalKB += float64(st.sample.TotalKB)
			fs.hostsMem++
		}
		for _, g := range st.sample.Gpus {
			fs.gpus++
			if u := g.UtilPct(); u >= 0 {
				utilSum += u
				fs.known++
				fs.utils = append(fs.utils, u)
				if u >= idleBand {
					fs.busy++
				}
			}
			if g.MemUsed != nil && g.MemTotal != nil && *g.MemTotal > 0 {
				fs.memUsed += float64(*g.MemUsed)
				fs.memTotal += float64(*g.MemTotal)
			}
			switch {
			case g.Power != nil && g.PowerCap != nil && *g.PowerCap > 0:
				fs.pwr += *g.Power
				fs.capw += *g.PowerCap
			case g.Power != nil:
				fs.pwrOnly += *g.Power
				fs.pwrOnlyN++
			}
		}
	}
	if fs.known > 0 {
		fs.util = utilSum / float64(fs.known)
	}
	return fs
}

// memPct and pwrPct are -1 when no card reported the reading.
func (fs fleetStats) memPct() float64 {
	if fs.memTotal <= 0 {
		return -1
	}
	return fs.memUsed / fs.memTotal * 100
}

// pwrPct is draw against capacity, unknown unless every card that reports its
// draw also reports its cap: a percentage over some of the cards would be read
// as the fleet's.
func (fs fleetStats) pwrPct() float64 {
	if fs.capw <= 0 || fs.pwrOnlyN > 0 {
		return -1
	}
	return fs.pwr / fs.capw * 100
}

// hostCPUPct is busy cores over cores, or -1 when no host reported both.
func (fs fleetStats) hostCPUPct() float64 {
	if fs.cores <= 0 {
		return -1
	}
	return fs.busyCores / fs.cores * 100
}

// hostMemPct is used memory over the scopes' limits, or -1 when none reported.
func (fs fleetStats) hostMemPct() float64 {
	if fs.totalKB <= 0 {
		return -1
	}
	return fs.memKB / fs.totalKB * 100
}

// memText is "880G/1.4T"; "—" when unknown.
func (fs fleetStats) memText() string {
	if fs.memTotal <= 0 {
		return "—"
	}
	return theme.MiB(uint64(fs.memUsed)) + "/" + theme.MiB(uint64(fs.memTotal))
}

// hostsCPUText is the fleet's core count, "186/512 cores", which is the absolute
// reading behind hostCPUPct: a percentage of a fleet this size says little until
// the cores it is a percentage of are beside it. "—" when no host reported both.
func hostsCPUText(fs fleetStats) string {
	if fs.cores <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d/%d cores", int(math.Round(fs.busyCores)), int(math.Round(fs.cores)))
}

// hostMemText is "880G/1.4T" over the hosts' own memory, not the cards'.
func (fs fleetStats) hostMemText() string {
	if fs.totalKB <= 0 {
		return "—"
	}
	return kibText(uint64(fs.memKB)) + "/" + kibText(uint64(fs.totalKB))
}

// pwrText is "11.6/22.4kW", or the total draw alone when some card reports no
// cap: dividing every card's draw by some cards' caps overstated the load.
// Measured power is never clamped to the cap: a card drawing above its limit is
// a fact worth showing.
func (fs fleetStats) pwrText() string {
	switch {
	case fs.capw > 0 && fs.pwrOnlyN == 0:
		return fmt.Sprintf("%.1f/%.1fkW", fs.pwr/1000, fs.capw/1000)
	case fs.capw > 0 || fs.pwrOnlyN > 0:
		return fmt.Sprintf("%.1fkW", (fs.pwr+fs.pwrOnly)/1000)
	}
	return "—"
}

// status is the connection word for the header, coloured by meaning. It reflects
// what HAS answered: "all online" while nothing has connected is the app lying
// about the fleet on its first frame, which is when it is most likely believed.
func (a *App) status(fs fleetStats) (string, theme.RGB) {
	t := a.skin.Theme
	switch {
	case fs.online == 0 && fs.pending > 0:
		return fmt.Sprintf("connecting %d", fs.pending), t.C("graph_text")
	case fs.dead > 0:
		return fmt.Sprintf("✕ %d down", fs.dead), t.C("hi_fg")
	case fs.pending > 0:
		return fmt.Sprintf("connecting %d", fs.pending), t.C("graph_text")
	}
	return "all online", t.C("proc_misc")
}

// drawHeader draws the fleet header and returns the first row below it. Its
// height is fixed: a header that grew with the readings moved the whole board
// by a line with the load.
func (a *App) drawHeader(c *Canvas, x, y, w int) int {
	return a.headerSkyline(c, x, y, w, a.fleetStats())
}

// meterRow is "label ■■■■■■ 49%  abs": a labelled meter in its metric's scale,
// its percentage a cell after it, then an optional absolute reading a gap later.
// Unknown leaves the meter blank and shows a dash: an empty meter would read as
// 0%.
func (a *App) meterRow(c *Canvas, x, y int, label string, labelW, meterW int, pct float64, scale theme.RGBScale, abs string, absW int, labelFG theme.RGB) int {
	t := a.skin.Theme
	if labelFG.R < 0 {
		labelFG = t.C("graph_text")
	}
	c.Text(x, y, label, labelFG)
	cx := x + labelW
	px := cx + meterW + 1
	if pct < 0 {
		c.Text(px, y, "—", t.C("inactive_fg"))
	} else {
		// The bar glides to the reading; the number is the reading.
		shown := int(math.Round(a.glideTo(barID{metric: label}, pct)))
		for i, cell := range (theme.Meter{Width: meterW, Scale: &scale}).Render(t, shown) {
			c.PutCell(cx+i, y, cell)
		}
		v := int(math.Round(pct))
		writePct(c, px, y, v, scale.At(float64(v)), t)
	}
	cx = px + pctW
	if absW > 0 {
		c.Text(cx+panelGap, y, theme.Clip(abs, absW), t.C("main_fg"))
		cx += panelGap + absW
	}
	return cx
}

// idle is the readable cards doing nothing: the number a user looking for a
// free GPU wants.
func (fs fleetStats) idle() int { return fs.known - fs.busy }

// The utilisation bands the panel counts. They are the same thresholds the
// counts have always used (idle below 5%), continued upward in equal steps, so
// the three numbers plus idle partition the readable cards exactly.
var bands = []struct {
	lo   int
	name string
}{
	{30, "≥30%"},
	{60, "≥60%"},
	{85, "≥85%"},
}

const idleBand = 5

// band counts the readable cards whose utilisation is at least lo.
func (fs fleetStats) band(lo int) int {
	n := 0
	for _, v := range fs.utils {
		if v >= float64(lo) {
			n++
		}
	}
	return n
}

// The panel's columns: a label, its meter and percentage, the reading. Counts
// above use the same gap.
const (
	panelLabelW = 6  // "power "
	panelAbsW   = 13 // "140.4G/141.0G"
	panelGap    = 2
	panelMeterW = 16
	panelW      = panelLabelW + panelMeterW + 1 + pctW + panelGap + panelAbsW
)

// panel draws the totals beside a header chart: two lines of counts, then one row
// per meter — util, vram, power, cpu, mem. It needs seven rows.
//
// cpu and mem used to share the last row to keep the panel six rows tall. That
// saved a line and broke the rhythm: five readings were drawn as three full-width
// meters and then two half-width ones crammed together, so the column of meters
// stopped lining up exactly where a reader had learned to scan down it. Every
// reading now gets its own row and its own label column.
func (a *App) panel(c *Canvas, x, y, w int, fs fleetStats) {
	s := a.skin
	t := s.Theme
	value, noun := t.C("title"), t.C("graph_text")
	// count writes "n noun" with n right-aligned in nw cells, and returns the
	// column after it.
	count := func(cx, cy int, n string, nw int, word string, fg theme.RGB) int {
		cx = c.Text(cx, cy, theme.PadLeft(n, nw), fg)
		return c.Text(cx+1, cy, word, noun)
	}
	hostsW := len(fmt.Sprint(fs.hosts))
	numW := 2*hostsW + 1
	col2 := x + numW + 1 + len("hosts") + panelGap + 1
	count(x, y, fmt.Sprintf("%d/%d", fs.online, fs.hosts), numW, "hosts", value)
	cxg := count(col2, y, fmt.Sprint(fs.gpus), 3, "gpus", value)
	// How many cards the summary is over: a mean over 12 of 44 cards and a mean
	// over all of them look the same until the coverage says which it is.
	if cxg+panelGap+1+len("44/44 read") <= x+w {
		c.Text(cxg+panelGap+1, y, fmt.Sprintf("%d/%d read", fs.known, fs.gpus), noun)
	}

	// Below the counts the cards are counted by utilisation band, from free to
	// saturated, each in the colour of its place on the same ramp the bars use:
	// a quiet fleet says so in green, a contended one in red, and the words are
	// just the thresholds. "busy" is gone: it was one of these bands under
	// another name, so it could be neither compared with the others nor summed
	// with them.
	//
	// Bands appear as the panel's width allows, the one nearest saturation
	// first: on a narrow terminal the count that says whether the fleet is
	// contended is the one worth its cells.
	type bandCell struct {
		n, word string
		at      int
	}
	rowW := func(cl []bandCell) int {
		w := 0
		for i, c := range cl {
			if i > 0 {
				w += panelGap + 1
			}
			w += 2 + 1 + theme.RuneWidth(c.word)
		}
		return w
	}
	idleCell := bandCell{fmt.Sprint(fs.idle()), "idle", idleBand}
	bandCells := make([]bandCell, 0, len(bands))
	for _, b := range bands {
		bandCells = append(bandCells, bandCell{fmt.Sprint(fs.band(b.lo)), b.name, b.lo})
	}
	// Keep the bands nearest saturation when room runs out.
	show := append([]bandCell{idleCell}, bandCells...)
	for len(show) > 1 && rowW(show) > w {
		show = append(show[:1], show[2:]...)
	}
	cx := x
	for i, cl := range show {
		lead := panelGap + 1
		if i == 0 {
			lead = 0
		}
		cx = c.Text(cx+lead, y+1, theme.PadLeft(cl.n, 2), value)
		cx = c.Text(cx+1, y+1, cl.word, s.Scale.CPU.At(float64(cl.at)))
	}

	// The bars are capped like the board's, and the space they give up goes to
	// the counts' new fan of bands.
	meterW := min(max(w-(panelW-panelMeterW), 4), 14)
	a.meterRow(c, x, y+2, "util", panelLabelW, meterW, fs.util, s.Scale.CPU, "", panelAbsW, s.Scale.CPU.At(72))
	a.meterRow(c, x, y+3, "vram", panelLabelW, meterW, fs.memPct(), s.Scale.VRAM, fs.memText(), panelAbsW, s.Scale.VRAM.At(78))
	a.meterRow(c, x, y+4, "power", panelLabelW, meterW, fs.pwrPct(), s.Scale.Power, fs.pwrText(), panelAbsW, s.Scale.Power.At(72))
	// The hosts' own resources take the last two rows, in the same columns as the
	// three fleet meters above them. They have no absolute reading: the host memory
	// total would need eleven cells the panel does not have, and the percentage is
	// the whole point at this zoom.
	a.meterRow(c, x, y+5, "cpu", panelLabelW, meterW, fs.hostCPUPct(), s.Scale.HostCPU, hostsCPUText(fs), panelAbsW, s.Scale.HostCPU.At(65))
	a.meterRow(c, x, y+6, "mem", panelLabelW, meterW, fs.hostMemPct(), s.Scale.Mem, fs.hostMemText(), panelAbsW, s.Scale.Mem.At(80))
}

// headerSkyline draws every GPU in the fleet as a bar, utilisation rising above
// an axis and VRAM hanging below it, with each host's name on the axis under its
// bars; the totals panel sits to the right. Box: 7 rows.
//
// The chart is the shape of the fleet's load: which hosts are full, which have
// free cards, where memory is tight. A fleet mean, by contrast, barely moves and
// drew a flat band.
func (a *App) headerSkyline(c *Canvas, x, y, w int, fs fleetStats) int {
	s := a.skin
	t := s.Theme
	// Nine rows: the panel needs seven (counts, bands, five meters) and the chart
	// splits the remaining seven between utilisation and VRAM four/three.
	const h = 9
	by := y
	border := s.Hue.Frame

	dx := x + w - 4 - panelW
	if dx-x < 40 {
		dx = -1
	}
	// No title of its own: the program's name says nothing the terminal's tab
	// does not, and its label crowded the clock.
	st, stCol := a.status(fs)
	var left []seg
	right := []seg{{st, stCol, false}}
	c.titledBox(x, by, w, h, border, left, right)
	// The clock is centred over the chart, clear of the divider's joint, and
	// only when it clears both other labels with a rule cell to spare.
	clock := []seg{{time.Now().Format("15:04:05"), t.C("title"), true}}
	span := w
	if dx >= 0 {
		span = dx - x
	}
	cx := x + (span-tabWidth(clock))/2
	leftEnd := x + 2
	rightStart := x + w - 2 - tabWidth(right)
	if cx > leftEnd && cx+tabWidth(clock) < rightStart {
		c.tab(cx, by, clock, border)
	}
	px := x + 2
	if dx >= 0 {
		c.Put(dx, by, "┬", border)
		for r := 1; r < h-1; r++ {
			c.Put(dx, by+r, "│", border)
		}
		c.Put(dx, by+h-1, "┴", border)
		a.skyline(c, x+1, by+1, dx-x-1, h-2)
		px = dx + 2
	}
	a.panel(c, px, by+1, x+w-2-px, fs)
	return by + h
}

// skyShade is a skyline bar's colour at a height: the metric's ramp at the
// fraction of the chart's half that lies between the axis and that edge.
//
// The colour is a function of the position, not of the bar: every bar of a metric
// is the same colour at the same height, and a bar's tip is the colour of its own
// reading. That is the gradient the card meters draw — from the ramp's start to
// the reading — stood on end. A tall bar walks the whole ramp, a short one stays
// in its dark end, and the comparison between hosts is in the colour as well as
// in the height.
//
// Sampled at the edge furthest from the axis, so the last half-cell of a full
// bar is the ramp's top and the first is one half-cell's step above its start.
func skyShade(sc theme.RGBScale, edge, full int) theme.RGB {
	return sc.AtFraction(float64(edge) / float64(full))
}

// skyline draws the bar chart into the w×h area at (x, y): h-1 rows split evenly
// above and below an axis row.
func (a *App) skyline(c *Canvas, x, y, w, h int) {
	s := a.skin
	t := s.Theme
	// Utilisation is the reading the chart is for, so it takes the larger half
	// when the split is uneven: three rows above and two below, not the reverse.
	up := h / 2
	down := h - 1 - up
	// The axis is drawn per host, under that host's own bars, not as one line
	// through the whole chart: a single rule joined the hosts to each other and
	// to the util/vram keys, and read as one long bar rather than as the ground
	// each host's bars stand on.
	axis := y + up
	// The keys sit just left of the chart, one each side of the axis.
	const keyW = 4 // "util", "vram"
	x0, x1 := x+1+keyW+1, x+w-1

	hosts := a.sortedHosts()
	type group struct {
		st    *hostState
		n     int // bar slots
		label string
	}
	groups := make([]group, len(hosts))
	for i, st := range hosts {
		n := len(st.sample.Gpus)
		if st.rank() != 0 || n == 0 {
			n = 1
		}
		groups[i] = group{st, n, theme.Clip(st.label, 12)}
	}
	// Hosts are three cells apart, and the chart keeps two cells at each end.
	// (Faint dashed lines in the gaps were tried; the space alone reads cleaner.)
	//
	// A host's name sits in its own stretch of axis, ─ name ─: a space each side
	// of the name, and at least `stub` cells of rule beyond each space. The rule
	// used to be only as wide as the bars or the name, whichever was wider, with
	// the name and its spaces drawn over it — so a host whose name was wider than
	// its bars covered all of its rule. That is every single-card machine, and
	// every host once the bars tighten to one cell, and the names then floated on
	// no line at all.
	//
	// The chart gives things up in order of what they carry. The rule beyond a
	// name shortens from two cells to one first; then the bars narrow, down to
	// one cell with no space between them, while every name keeps its rule; then
	// the rule goes; and only then do hosts close up to one cell apart. A rule
	// beside a one-cell gap is never drawn: two hosts' rules a cell apart read as
	// one dashed line through both.
	slot, bar, gap, stub := 1, 1, 1, 0
	groupW := func(g group, slot, bar, gap, stub int) int {
		name := theme.RuneWidth(g.label)
		switch {
		case stub > 0:
			name += 2 + 2*stub
		case gap < 3:
			// The name's two spaces cannot spill into a one-cell gap.
			name += 2
		}
		return max(g.n*slot-(slot-bar), name)
	}
	widthAt := func(slot, bar, gap, stub int) int {
		total := 2 * (gap/2 + 1) // the margins at the two ends
		for i, g := range groups {
			if i > 0 {
				total += gap
			}
			total += groupW(g, slot, bar, gap, stub)
		}
		return total
	}
	type fitting struct{ slot, bar, gap, stub int }
	var tries []fitting
	bars := func(gap, stub int, solid bool) {
		for _, s := range []int{4, 3, 2} {
			tries = append(tries, fitting{s, s - 1, gap, stub})
		}
		if solid {
			tries = append(tries, fitting{1, 1, gap, stub})
		}
	}
	bars(3, 2, false)
	bars(3, 1, true)
	bars(3, 0, true)
	bars(1, 0, true)
	for _, f := range tries {
		if widthAt(f.slot, f.bar, f.gap, f.stub) <= x1-x0 {
			slot, bar, gap, stub = f.slot, f.bar, f.gap, f.stub
			break
		}
	}
	total := widthAt(slot, bar, gap, stub)
	start := x0 + max((x1-x0-total)/2, 0) // the chart's left margin
	// The two keys, one each side of the axis, with the noun they are measured on
	// between them. "util" and "vram" alone were ambiguous at the top of a page
	// that also reports a host's own cpu and memory a few rows down: the pairs
	// read as the same two quantities at two scopes, when this chart is devices
	// and the host's figures are the machine's.
	//
	// Each key sits a row clear of the noun, in the middle of the half it names:
	// two rows up for utilisation and two down for VRAM. Stacked on three
	// consecutive rows the three words read as one block of text, a list, rather
	// than as a heading over the bars and a heading under them.
	c.Text(start-1-keyW, axis-min(2, up), "util", s.Scale.CPU.At(60))
	c.Text(start-1-keyW, axis+min(2, down), "vram", s.Scale.VRAM.At(75))
	// "gpu" sits between the two keys, on the axis row, in the margin to the left
	// of the chart rather than over it: the host names start at `start` and the
	// three cells before them are empty on every chart wide enough to draw. The
	// keys are a two-word heading and this is the noun they are both measured on,
	// so it belongs between them and not beside the first host's name.
	if kx := start - 1 - 3; kx > 0 {
		c.Text(kx, axis, "gpu", t.C("graph_text"))
	}
	gx := start + gap/2 + 1

	blocks := []string{" ", "▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	focus := a.focused()
	for _, g := range groups {
		barsW := g.n*slot - (slot - bar)
		gw := groupW(g, slot, bar, gap, stub)
		if gx+gw+gap/2 >= x1 {
			// The last chart column is always free: every group drawn, and the
			// margin after it, ends before it. Past it is the divider.
			c.Text(min(gx, x1), axis, "…", t.C("graph_text"))
			break
		}
		for r := y; r < y+h; r++ {
			a.hits = append(a.hits, hit{x: gx, y: r, w: gw, host: g.st.name})
		}
		for i := gx; i < gx+gw; i++ {
			c.Put(i, axis, "─", t.C("div_line"))
		}
		bx := gx + (gw-barsW)/2
		st := g.st
		switch {
		case st.err != "":
			c.Put(bx, axis-1, "✕", t.C("hi_fg"))
		case !st.hasData:
			c.Put(bx, axis-1, "◌", t.C("graph_text"))
		default:
			for j, gpu := range st.sample.Gpus {
				cx := bx + j*slot
				// The bars glide, under the same keys as the card's meters.
				u := a.glideTo(barKey(st.name, gpu, "util"), gpu.UtilPct())
				if u < 0 {
					// Unknown is a mark of its own, never an empty bar.
					for k := 0; k < bar; k++ {
						c.Put(cx+k, axis-1, "·", t.C("inactive_fg"))
					}
				} else {
					// An idle card draws nothing above its name: the axis is its
					// ground, and a skyline at rest is the ground with nothing on
					// it. Idle cards used to keep a one-eighth floor in the track
					// colour, and on a host whose bars are one cell wide the floors
					// ran together into a rule of their own a row above the names —
					// which is the line a reader took for the axis. A card's
					// presence is still on the board: its VRAM bar always hangs at
					// least half a cell.
					eighths := int(math.Round(u / 100 * float64(up*8)))
					// The bar walks its ramp from the axis up (skyShade), in half
					// cells: a full cell is two colours, the lower half in the
					// glyph and the upper half behind it, so a three-row bar
					// crosses six shades rather than three. Three was what an
					// earlier pass drew, one colour per row, and it read as
					// stripes; it was replaced by a single flat colour, which
					// read as no gradient at all.
					for r := 0; r < up; r++ {
						base := (up - 1 - r) * 8 // eighths between the axis and this cell
						n := min(max(eighths-base, 0), 8)
						if n == 0 {
							continue
						}
						// A partial cell has one colour: the lower block glyph is
						// the bar and the rest of the cell is page.
						lo := skyShade(s.Scale.CPU, base+n, up*8)
						hi := lo
						glyph, two := blocks[n], false
						if n == 8 {
							// Bars grow upward: ▄ puts the lower half's colour in
							// the glyph and the upper half's behind it.
							glyph, two = "▄", true
							lo = skyShade(s.Scale.CPU, base+4, up*8)
						}
						for k := 0; k < bar; k++ {
							c.Put(cx+k, y+r, glyph, lo)
							if two {
								c.Fill(cx+k, y+r, 1, hi)
							}
						}
					}
				}
				m := gpu.MemPct()
				if m >= 0 {
					m = a.glideTo(barKey(st.name, gpu, "vram"), m*100) / 100
				}
				if m < 0 {
					// Unknown VRAM has a mark of its own under the axis: drawing
					// nothing would read as a card using none.
					for k := 0; k < bar; k++ {
						c.Put(cx+k, axis+1, "·", t.C("inactive_fg"))
					}
					continue
				}
				// Hanging bars fill from the top. Only lower partial blocks exist
				// in the text repertoire, so a partial cell is the bar's colour as
				// background with the unfilled lower part drawn over it in the
				// page colour; a full cell is the upper-half glyph, which needs no
				// background at all and so still reads as solid in plain text.
				//
				// Both are required. Drawing every cell as a lower block over a
				// coloured background makes the band look uniform in a terminal,
				// but plain output (-once, or a pipe) has no background, so every
				// cell would come out as the same glyph and the band would lose
				// the height that is its whole content.
				eighths := int(math.Round(m * float64(down*8)))
				// A measured card keeps a half-cell floor, for the same reason the
				// utilisation bar does: at 0.4% the rounding lands on one eighth of
				// a cell, which draws as a hairline spike and makes the bottom of
				// the band ragged. A card that has reported a VRAM figure is
				// present, and a half cell says so without claiming it holds
				// anything.
				if eighths < 4 {
					eighths = 4
				}
				// The same walk as utilisation, mirrored: the ramp runs from the
				// axis down, so a bar's lowest edge is its reading's colour.
				for r := 0; r < down; r++ {
					n := min(max(eighths-r*8, 0), 8)
					vcol := skyShade(s.Scale.VRAM, r*8+n, down*8)
					for k := 0; k < bar && n > 0; k++ {
						if n == 8 {
							// ▀ puts the upper half in the glyph and the lower half
							// behind it, each in its own shade.
							c.Put(cx+k, axis+1+r, "▀", skyShade(s.Scale.VRAM, r*8+4, down*8))
							c.Fill(cx+k, axis+1+r, 1, vcol)
							continue
						}
						if n == 4 {
							// Exactly half is the same upper-half glyph as the full
							// cells, in the bar's colour on the page, so a half cell
							// has the same shape and the same top edge as a full one.
							// It was drawn the other way round — the glyph in the page
							// colour over a VRAM background — which coloured the
							// LOWER half: the bar began half a row below the names,
							// and since every card under an eighth of its memory is
							// lifted to this floor, nearly every bar on a quiet fleet
							// hung detached.
							c.Put(cx+k, axis+1+r, "▀", vcol)
							continue
						}
						c.Put(cx+k, axis+1+r, blocks[8-n], a.skin.Bg)
						c.Fill(cx+k, axis+1+r, 1, vcol)
					}
				}
			}
		}
		nameFG := t.C("main_fg")
		switch {
		case st.err != "":
			nameFG = t.C("hi_fg")
		case st.name == focus:
			nameFG = t.C("title")
		}
		lx := gx + (gw-theme.RuneWidth(g.label))/2
		c.Put(lx-1, axis, " ", t.C("div_line"))
		end := c.Text(lx, axis, g.label, nameFG)
		if st.name == focus {
			c.TextBold(lx, axis, g.label, nameFG)
		}
		c.Put(end, axis, " ", t.C("div_line"))
		gx += gw + gap
	}
}

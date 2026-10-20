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

// hostStateGlyph is the freshness indicator.
//
// Staleness is a distinct state from failure: a host whose stream died shows the
// last good sample and says so, because "no data" and "here is 8-second-old data"
// are different facts and conflating them hides an outage.
func hostStateGlyph(h *hostState) (string, bool) {
	switch {
	case h.err != "":
		return "✕", true
	case !h.hasData:
		return "◌", true
	case h.fresh() > 5*time.Second:
		return "◐", true
	}
	return "●", false
}

// cardHue picks the border colour for a host's state.
//
// Load is deliberately not a state here. A border that turned "busy" above 60%
// flickered every time a host's mean crossed the line, and the bars already say
// how busy it is.
func cardHue(s *theme.Skin, h *hostState) theme.RGB {
	switch {
	case h.err != "":
		return s.Hue.Dead
	case !h.hasData:
		return s.Hue.Warn
	case h.fresh() > 5*time.Second:
		return s.Hue.Warn
	}
	// A healthy box is a quiet grey, not green: a whole screen of green frames
	// competed with the readings, and green is the state that is never news.
	return s.Hue.Frame
}

// gpuNameShort is the card subtitle: "8× RTX 4090 · 24G".
//
// The VRAM size is stated once here rather than on every row: it is the same for
// every card of a model, and a per-row "/24.0G" spent seven columns repeating it.
func gpuNameShort(h *hostState) string {
	if len(h.sample.Gpus) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, g := range h.sample.Gpus {
		counts[theme.ShortGPU(g.Name)]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d× %s", counts[k], k))
	}
	out := strings.Join(parts, " + ")
	if total, ok := uniformVRAM(h.sample.Gpus); ok {
		out += " · " + theme.MiB(total)
	}
	return out
}

// uniformVRAM reports the VRAM size when every readable card shares it.
func uniformVRAM(gs []wire.Gpu) (uint64, bool) {
	var total uint64
	for _, g := range gs {
		if g.MemTotal == nil {
			continue
		}
		// Within 2%: the same model reports slightly different usable sizes.
		switch {
		case total == 0:
			total = *g.MemTotal
		case math.Abs(float64(*g.MemTotal)-float64(total)) > float64(total)/50:
			return 0, false
		}
	}
	return total, total > 0
}

// ---- one-line GPU rows -----------------------------------------------------

// The rhythm of a row. A bar and its percentage read as one element and sit a
// cell apart; elements of different kinds are a gap apart, the same gap between
// every pair, so the row reads as evenly spaced columns rather than as runs of
// text. The header and a host's view use the same two distances.
const (
	pctW   = 4 // "100%", written straight after its bar
	usedW  = 6 // "140.4G"
	tempW  = 5 // "105°C"
	powerW = 5 // "1000W"

	minBar   = 5
	maxBar   = 14
	minSpark = 8
)

// rowGeom is the column plan for one-line GPU rows at a given content width.
//
// The bars are capped. A meter that grows with the terminal turned a 200-column
// card into two long slabs and nothing else; past ~14 cells a bar adds no
// precision (each cell is already 7%), so the width goes to more columns of
// cards and to the history graph instead.
type rowGeom struct {
	rows   int // lines per GPU: 1, or 2 on a narrow lane (see planGPU)
	idxW   int // the widest GPU index on the board
	gap    int // between elements of different kinds
	meterW int
	graphW int // 0 = no graph
	used   bool
	temp   bool
	power  bool
}

// barW is a bar with its percentage.
func (g rowGeom) barW() int { return g.meterW + 1 + pctW }

// utilX and vramX are where the two bars start, relative to the row.
func (g rowGeom) utilX() int { return g.idxW + g.gap }
func (g rowGeom) vramX() int {
	if g.rows == 2 {
		return g.utilX() // under the utilisation bar
	}
	return g.utilX() + g.barW() + g.gap
}

// readCells is the cells the two-line plan's readings take on the row under the
// utilisation bar: temperature and power side by side, the percentages of the
// narrower lane closing their gap when they must.
func (g rowGeom) readCells() int { return tempW + g.gap + powerW }

// readX is where a two-line plan's readings start, a full gap after the
// percentage.
func (g rowGeom) readX() int { return g.utilX() + g.barW() + g.gap }

// graphX is where the history graph starts.
func (g rowGeom) graphX() int { return g.width() - g.graphW }

// planRow picks the row's gap, then its fields. The wide gap is kept only while
// it costs nothing that matters, the gauges, ten-cell bars and the graph; past
// that a narrower lane closes its gaps before it gives up a field.
func planRow(inner, idxW int, graph bool) rowGeom {
	for _, gap := range []int{3, 2} {
		g := planFields(inner, idxW, gap, graph)
		if g.temp && g.power && g.meterW >= 10 && (g.graphW > 0 || !graph) {
			return g
		}
	}
	return planFields(inner, idxW, 2, graph)
}

// planFields admits fields in priority order. Both bars and both percentages are
// mandatory; temperature and power come next, then the graph, then the absolute
// VRAM reading. Bars widen in steps between those admissions so a narrow lane
// trades away the graph before its bars shrink to stubs. The graph takes what is
// left, so a row ends at its content's right edge as it starts at the left.
func planFields(inner, idxW, gap int, graph bool) rowGeom {
	g := rowGeom{idxW: idxW, gap: gap, meterW: minBar}
	left := inner - g.width()
	if left < 0 {
		// Too narrow for two proper bars. Shrink both rather than drop one: two
		// bars per GPU is the requirement, and a missing bar is a missing fact.
		g.meterW = max(1, minBar+(left-1)/2)
		return g
	}
	take := func(n int) bool {
		if left < gap+n {
			return false
		}
		left -= gap + n
		return true
	}
	grow := func(to int) {
		for g.meterW < to && left >= 2 {
			g.meterW++
			left -= 2
		}
	}
	widen := func(to int) {
		if g.graphW > 0 {
			n := min(to-g.graphW, left)
			g.graphW += n
			left -= n
		}
	}
	g.temp = take(tempW)
	g.power = take(powerW)
	grow(10)
	if graph && take(minSpark) {
		g.graphW = minSpark
	}
	grow(12)
	widen(16)
	g.used = take(usedW)
	grow(maxBar)
	widen(inner)
	return g
}

// twoRowsBelow is the content width under which a GPU takes two lines. One
// line holds both bars, both percentages, temperature, power and the graph, and
// on a narrow lane (three lanes on a 200-column terminal) that left short bars,
// two-cell gaps and an eight-cell graph. Two lines stack the bars, put
// temperature over power, and give the graph both lines and the rest of the
// width.
const twoRowsBelow = 70

// planGPU plans one GPU's lines for a content width: one line, or two below
// twoRowsBelow.
func planGPU(inner, idxW int, graph bool) rowGeom {
	if inner >= twoRowsBelow {
		g := planRow(inner, idxW, graph)
		g.rows = 1
		return g
	}
	return planTwoRows(inner, idxW, graph)
}

// planTwoRows is the two-line plan: the bars at full length and a full gap
// first, then the readings, then the graph with the rest. A narrower lane gives
// up bar length, then gap, then the readings.
func planTwoRows(inner, idxW int, graph bool) rowGeom {
	g := rowGeom{rows: 2, idxW: idxW, gap: 3, meterW: maxBar, temp: true, power: true}
	for g.width() > inner && g.meterW > 10 {
		g.meterW--
	}
	if g.width() > inner {
		g.gap = 2
	}
	for g.width() > inner && g.meterW > minBar {
		g.meterW--
	}
	if g.width() > inner {
		g.temp, g.power = false, false
	}
	for g.width() > inner && g.meterW > 1 {
		g.meterW--
	}
	if rest := inner - g.width() - g.gap; graph && rest >= minSpark {
		g.graphW = rest
	}
	return g
}

// width is the number of cells the plan occupies.
func (g rowGeom) width() int {
	if g.rows == 2 {
		w := g.utilX() + g.barW()
		if g.temp {
			w += g.gap + g.readCells()
		}
		if g.graphW > 0 {
			w += g.gap + g.graphW
		}
		return w
	}
	w := g.vramX() + g.barW()
	for _, f := range []struct {
		on bool
		w  int
	}{{g.used, usedW}, {g.temp, tempW}, {g.power, powerW}, {g.graphW > 0, g.graphW}} {
		if f.on {
			w += g.gap + f.w
		}
	}
	return w
}

// ---- the host's own block ---------------------------------------------------

// hostRows is the lines the machine's own block takes on every card: its cpu,
// then its memory.
//
// It is two whatever the GPUs below it take. The block reads as a card of its
// own, one resource per line with a meter and its reading, the way a GPU's two
// lines are utilisation and VRAM; on a lane where a GPU takes one line, the
// machine's two resources still do not share one.
const hostRows = 2

// hostLabW is the cells before a host meter: "cpu " and "mem ".
const hostLabW = 4

// hostGeom places the host block's two lines in the GPU rows' own columns.
//
// The meters end where a GPU's utilisation bar ends, so the host's percentages
// stand in the cards' percentage column; each reading starts where a GPU's next
// element starts (its VRAM bar on one line, its temperature on two). The block
// draws no history: it states capacity, used of what the host has, and the
// graphs on a card are its devices'.
type hostGeom struct {
	barX   int // where both meters start
	meterW int
	gap    int // between elements, as the GPU rows use it
	factX  int // where each line's reading starts
	endX   int // the column after the last cell a reading may use
	// graphX and graphW are the host's own utilisation history. They take the
	// same columns the GPU rows use for theirs, so a card's graphs form one
	// right-hand column from the cpu line down; 0 means the lane is too narrow
	// and the readings keep the width.
	graphX, graphW int
}

// hostPlan derives the host block's columns from the board's GPU plan.
func hostPlan(geo rowGeom, inner int) hostGeom {
	pctX := geo.utilX() + geo.meterW + 1
	// A label needs its cell of space before its bar: a bar that starts against
	// its own name reads as part of the name. When the index column and its gap
	// are narrower than that, the bar starts later and is that much shorter, so
	// its percentage still lands in the cards' percentage column.
	barX := max(geo.utilX(), hostLabW)
	meterW := max(pctX-1-barX, 3)
	pctX = barX + meterW + 1
	g := hostGeom{barX: barX, meterW: meterW, gap: geo.gap, factX: pctX + pctW + geo.gap, endX: inner}
	// The rest of the block was empty: the two lines stopped after the memory
	// total, which on a three-lane card is most of the width. It carries the
	// machine's own utilisation history instead — the same graph the GPU rows
	// draw, two rows tall and in the cpu ramp, in the same columns as theirs so
	// the right-hand side of a card is one column of graphs.
	//
	// The width is taken from what the lane has left rather than from the GPU
	// rows' plan: on a narrow lane those rows drop their graph entirely to keep
	// two bars and two readings, and the host block has no such pressure — its
	// readings are shorter and its graph is the only one on the card until the
	// devices' own appear. Leaving it out because the GPUs did made the first lane
	// of a narrow board the only one with a graph.
	//
	// The readings keep their room first: 12 cells is "339.2G/512.0G", the widest
	// either line gets, and a graph that pushed a reading into an ellipsis would
	// be trading a fact for a shape.
	if spare := inner - (g.factX + 12 + geo.gap); spare >= 8 {
		w := spare
		if geo.graphW > 0 {
			w = geo.graphW // line up with the devices' graphs when they exist
		}
		g.graphW = min(w, spare)
		g.graphX = inner - g.graphW
	}
	return g
}

// hostBlock draws the machine's own two lines above its cards.
//
// cpu is the meter, then the cores' worth busy of the cores this scope may use
// ("73/128 cores"); mem is the meter, then used of the scope's limit
// ("339.2G/512.0G"). The two readings have one shape on purpose, used over what
// there is, so the block reads as two lines of one table, as a GPU's VRAM line
// does. Both denominators are the scope's: a cgroup's quota and limit, or the
// whole machine's cores and memory when nothing limits it.
//
// "cpu" and "mem" are written on the lines themselves, each in its own ramp.
// In the bottom border, one word would name a host reading and a card reading
// at once ("util·cpu"), two scopes in one noun.
func hostBlock(c *Canvas, x, y int, h *hostState, s *theme.Skin, g hostGeom, gl glider) {
	t := s.Theme
	snap := h.sample
	meter := func(row int, name string, labFG theme.RGB, pct float64, sc theme.RGBScale) {
		ly := y + row
		c.Text(x, ly, name, labFG)
		px := x + g.barX + g.meterW + 1
		if pct < 0 {
			// Unknown is a dash, never an empty bar: an empty bar reads as 0%, a
			// measurement the probe never gave us.
			c.Text(px, ly, "—", t.C("inactive_fg"))
			return
		}
		shown := int(math.Round(gl.to(barID{host: h.name, metric: name}, pct)))
		v := int(math.Round(pct))
		putMeter(c, x+g.barX, ly, g.meterW, shown, v, sc, t)
		writePct(c, px, ly, v, sc.At(float64(v)), t)
	}
	// reading writes the first of its forms that fits the line whole, the number
	// in fg and its unit dimmed. A number that is cut ("339.2G/5…") states
	// something false about the limit, so a form that does not fit is skipped.
	reading := func(row int, fg theme.RGB, forms ...[2]string) {
		for _, f := range forms {
			if g.factX+theme.RuneWidth(f[0])+theme.RuneWidth(f[1]) > g.endX {
				continue
			}
			end := c.Text(x+g.factX, y+row, f[0], fg)
			c.Text(end, y+row, f[1], t.C("graph_text"))
			return
		}
	}

	cpu := snap.CPUPctValue()
	meter(0, "cpu", s.Scale.HostCPU.At(65), cpu, s.Scale.HostCPU)
	if snap.Cores > 0 {
		unit := " cores"
		if snap.Cores == 1 {
			unit = " core"
		}
		total := fmt.Sprint(snap.Cores)
		if cpu >= 0 {
			n := busyCores(cpu, snap.Cores) + "/" + total
			reading(0, s.Scale.HostCPU.At(cpu), [2]string{n, unit}, [2]string{n, ""})
		} else {
			// No baseline yet: how many cores there are is known, how many are
			// busy is not, and "0/128" would say they are idle.
			reading(0, t.C("main_fg"), [2]string{total, unit}, [2]string{total, ""})
		}
	}

	mem := snap.MemPct()
	meter(1, "mem", s.Scale.Mem.At(80), mem, s.Scale.Mem)
	if snap.TotalKB > 0 {
		// The percentage already says "—" when memory is unreadable; a second
		// dash here would be the same fact twice.
		reading(1, s.Scale.Mem.At(mem), [2]string{kibText(snap.MemKB) + "/" + kibText(snap.TotalKB), ""})
	}

	// The machine's own utilisation history, in the columns the GPU rows use for
	// theirs. Two rows tall, the height of the block, so the card's right-hand
	// side is one column of graphs from the cpu line to the last device.
	//
	// It is drawn in the host cpu ramp, not the device one: the graph is the
	// machine's, and a green line here would say "GPU utilisation" in the one
	// place on the card where that is not what it is. NoZero keeps a floor under a
	// machine that has reported 0%, so an idle host draws a line rather than
	// nothing.
	if g.graphW > 0 {
		rows := h.graphOf("\x00cpu", h.cpuHist, g.graphW, hostRows, &s.Scale.HostCPU, t)
		for r := range rows {
			for i, cell := range rows[r] {
				c.Put(x+g.graphX+i, y+r, cell.Text, cell.FG)
			}
		}
	}
}

// busyCores is a CPU percentage as the cores' worth it keeps busy: "73", or
// "3.6" below ten, where a whole number would round a light load to nothing.
func busyCores(pct float64, cores int) string {
	b := pct / 100 * float64(cores)
	if b < 9.95 {
		return fmt.Sprintf("%.1f", b)
	}
	return fmt.Sprintf("%.0f", b)
}

// idxWidth is the cells the widest GPU index on the board needs.
func idxWidth(hosts []*hostState) int {
	w := 1
	for _, h := range hosts {
		for _, g := range h.sample.Gpus {
			w = max(w, len(fmt.Sprint(g.Index)))
		}
	}
	return w
}

// gpuLine draws one GPU as one row: index, utilisation bar and percentage, VRAM
// bar and percentage, then the gauges and the device's own history.
//
// Both bars share the row instead of stacking. Stacking cost two lines per GPU,
// which is why a 50-row terminal used to hold six hosts; side by side, with each
// bar in its own gradient, "busy but VRAM-light" is still a visible shape.
func gpuLine(c *Canvas, x, y int, g wire.Gpu, graph func(w, h int) [][]theme.Cell, s *theme.Skin, geo rowGeom, buf []theme.Cell, st lampState, now time.Time, host string, gl glider) {
	t := s.Theme
	c.Text(x, y, fitExact(fmt.Sprint(g.Index), geo.idxW), t.C("graph_text"))
	// The lamp takes a cell that is blank on the layout in use, and never one that
	// is somebody else's. On a two-line row that is the cell under the index, which
	// the second line leaves empty on every card — the place the reader's eye is
	// already going for the row's identity. A one-line row has no such second
	// line, so the lamp takes the first cell of the gap after the index, which is
	// spacing by definition. Neither choice costs a column of data.
	glyph, fg := lampCell(st, now)
	switch {
	case geo.rows == 2:
		// The cell under the index, which the second line leaves empty on every
		// card and which the reader's eye is already going to for the row's
		// identity. Free cards keep a dark dot there so the lamp's presence is
		// stable and "free" is a state rather than an absence.
		c.Put(x, y+geo.rows-1, glyph, fg)
	case st.lit:
		// A one-line row has no second line, so there is no cell of its own to
		// give the lamp. Rather than widen every row for a mark that is usually
		// out, it takes the last cell of the gap after the index — spacing by
		// definition — and only when it is lit. An unlit lamp draws nothing here,
		// so a board with no claims is laid out exactly as it was before.
		c.Put(x+geo.idxW+geo.gap-1, y, glyph, fg)
	}
	// Every reading is drawn in its metric's scale, the percentage included: the
	// metre's cells and the number beside it are the same colour for the same
	// value, so the row reads as two facts, not two colour systems.
	bar := func(bx, by int, pct float64, scale theme.RGBScale, name string) {
		px := bx + geo.meterW + 1
		if pct < 0 {
			// Unknown is a dash, never an empty bar: an empty bar reads as 0%, a
			// measurement the driver never gave us.
			c.Text(px, by, "—", t.C("inactive_fg"))
			return
		}
		// The bar glides to the reading; the number is the reading.
		shown := int(math.Round(gl.to(barKey(host, g, name), pct)))
		v := int(math.Round(pct))
		putMeter(c, bx, by, geo.meterW, shown, v, scale, t)
		writePct(c, px, by, v, scale.At(float64(v)), t)
	}
	mem := g.MemPct()
	if mem >= 0 {
		mem *= 100
	}
	// The second bar is beside the first on one line, under it on two.
	vy := y + geo.rows - 1
	bar(x+geo.utilX(), y, g.UtilPct(), s.Scale.CPU, "util")
	bar(x+geo.vramX(), vy, mem, s.Scale.VRAM, "vram")

	// Readings are right-aligned in their columns, so digits line up down a card,
	// and each is split at its unit: the number in the reading's colour, the unit
	// dimmed, so a column of numbers reads as numbers.
	gauge := func(gx, gy int, ok bool, w int, txt string, fg theme.RGB) {
		if !ok {
			c.Text(gx, gy, theme.PadLeft("—", w), t.C("inactive_fg"))
			return
		}
		num, unit := splitUnit(txt)
		start := gx + w - theme.RuneWidth(txt)
		end := c.Text(start, gy, num, fg)
		c.Text(end, gy, unit, t.C("graph_text"))
	}
	var tempTxt, powerTxt string
	var tempFG, powerFG theme.RGB
	if g.Temp != nil {
		tempTxt, tempFG = fmt.Sprintf("%.0f°C", *g.Temp), t.Temp(*g.Temp)
	}
	if g.Power != nil {
		powerTxt, powerFG = fmt.Sprintf("%.0fW", *g.Power), s.Scale.Power.At(g.PowerPct()*100)
	}
	if geo.rows == 2 {
		// One line per kind of fact. The utilisation line carries the compute
		// readings, temperature and power side by side: two rows of a column that
		// alternated °C and W made the reader switch units every line. The memory
		// line carries the memory reading, in the VRAM colour like its bar.
		if geo.temp {
			gauge(x+geo.readX(), y, g.Temp != nil, tempW, tempTxt, tempFG)
			gauge(x+geo.readX()+tempW+geo.gap, y, g.Power != nil, powerW, powerTxt, powerFG)
		}
		if g.MemUsed != nil {
			used := theme.MiB(*g.MemUsed)
			if g.MemTotal != nil {
				used += "/" + theme.MiB(*g.MemTotal)
			}
			// Right-aligned under the two readings, so the memory line's one
			// number sits where the eye already looks.
			c.TextRight(x+geo.readX()+geo.readCells(), vy, used, s.Scale.VRAM.At(max(mem, 0)))
		}
	} else {
		cx := x + geo.vramX() + geo.barW() + geo.gap
		if geo.used {
			var used string
			if g.MemUsed != nil {
				used = theme.MiB(*g.MemUsed)
			}
			gauge(cx, y, g.MemUsed != nil, usedW, used, t.C("main_fg"))
			cx += usedW + geo.gap
		}
		if geo.temp {
			gauge(cx, y, g.Temp != nil, tempW, tempTxt, tempFG)
			cx += tempW + geo.gap
		}
		if geo.power {
			gauge(cx, y, g.Power != nil, powerW, powerTxt, powerFG)
		}
	}
	if geo.graphW > 0 {
		// NoZero keeps a floor under an idle card, so "measured 0%" draws a line
		// and only "no samples yet" is blank. On two lines the graph is two rows
		// tall and shades by height like a host's own.
		rows := graph(geo.graphW, geo.rows)
		for r := range rows {
			for i, cell := range rows[r] {
				c.Put(x+geo.graphX()+i, y+r, cell.Text, cell.FG)
			}
		}
	}
}

// ---- cards and lanes --------------------------------------------------------

// stackGap is the empty rows between cards stacked in a lane, and between
// bands.
const stackGap = 1

// Lane widths. A lane near laneTarget holds both bars, temperature, power and a
// readable graph; below minLaneW a row starts losing fields.
const (
	minLaneW   = 60
	laneTarget = 80
	// cardChrome is the border and one cell of padding on each side.
	cardChrome = 4
)

// placed is one card's position on the board.
type placed struct {
	host       *hostState
	lane, x, y int
	w, h       int
}

// cardHeight is a card's height: the host's own block and its GPUs' lines,
// plus the border. A host with nothing to show (failed, connecting) gets one
// line for its reason; one that answers with no GPUs still has a cpu and a
// memory to report, so it keeps its block.
func cardHeight(h *hostState, rows int) int {
	if h.err != "" || !h.hasData {
		return 3
	}
	// The machine's own block is two lines whatever the GPUs take: one line per
	// resource, as a GPU's two lines are.
	return 2 + hostRows + rows*len(h.sample.Gpus)
}

// laneRows is the lines per GPU for n lanes across width: the narrowest lane's,
// which every card shares.
func laneRows(width, n, gap int) int {
	_, ws := gutterSpans(width, max(n, 1), gap)
	if ws[len(ws)-1]-cardChrome < twoRowsBelow {
		return 2
	}
	return 1
}

// boardGeom is the row plan every card uses: the narrowest lane's.
func boardGeom(width, lanes, gap, idxW int, graph bool) rowGeom {
	_, ws := gutterSpans(width, max(lanes, 1), gap)
	return planGPU(ws[len(ws)-1]-cardChrome, idxW, graph)
}

// ownerW is the widest holder name the card's footer line prints before clipping.
//
// The footer is a whole line, so it does not resize the row plan; the cap only keeps
// one long agent name from crowding out the reason beside it.
const ownerW = 18

// notch is a label written into a card's bottom border, dx cells into its row.
type notch struct {
	dx   int
	text string
}

// cardStyle is what every card on a board shares besides its row plan.
type cardStyle struct {
	interval time.Duration // between samples, for the graph's span
	glide    glider        // a bar's drawn length for its reading
}

// borderLegend names the row's columns in each card's bottom border, under the
// columns themselves: the two bars and the graph. Temperature, power and VRAM
// carry their units and need no name.
func borderLegend(geo rowGeom, interval time.Duration) []notch {
	out := []notch{{geo.utilX(), "util"}, {geo.vramX(), "vram"}}
	if geo.rows == 2 {
		out = []notch{{geo.utilX(), "util · vram"}}
	}
	if geo.graphW > 0 {
		// "last 56s" named neither the measurement nor the direction of time: it
		// is this card's own utilisation history, newest at the right. The span is
		// left to the help screen, where there is room to explain it.
		if theme.RuneWidth("util history") <= geo.graphW-1 {
			out = append(out, notch{geo.graphX(), "util history"})
		} else if theme.RuneWidth("util") <= geo.graphW-1 {
			out = append(out, notch{geo.graphX(), "util"})
		}
	}
	return out
}

// gutterSpans splits the width into n lanes with gap empty columns between
// them, giving the remainder to the leftmost lanes so widths differ by at most a
// cell.
func gutterSpans(width, n, gap int) (xs, ws []int) {
	usable := width - (n-1)*gap
	base, extra := usable/n, usable%n
	x := 0
	for i := 0; i < n; i++ {
		w := base
		if i < extra {
			w++
		}
		xs = append(xs, x)
		ws = append(ws, w)
		x += w + gap
	}
	return xs, ws
}

// spacing is the space between cards: columns between lanes, rows between
// cards.
type spacing struct{ col, row int }

// pack places cards in bands. Each band is as tall as the first unplaced card;
// every lane of the band is then filled, in order, with the next cards that
// still fit in the band's remaining height.
//
// This is what keeps the board in aligned rows. A plain "shortest lane next"
// waterfall staggered any fleet whose GPU counts do not divide evenly into the
// lanes: two 8-GPU hosts and four 4-GPU hosts in three lanes left every row with
// a tall card beside a short one. Banding puts a 4-GPU and a 2-GPU host together
// beside each 8-GPU one, so the band closes flat.
//
// Gaps are filled only with hosts of the same state: a failed host stays at the
// end of the board rather than being used to plug a hole between busy ones. The
// placement is a pure function of the order and the GPU counts, so it is exactly
// as stable as the order is.
func pack(hosts []*hostState, width, n int, sp spacing) ([]placed, int) {
	xs, ws := gutterSpans(width, n, sp.col)
	rows := laneRows(width, n, sp.col)
	out := make([]placed, 0, len(hosts))
	used := make([]bool, len(hosts))
	y := 0
	for i := range hosts {
		if used[i] {
			continue
		}
		band, rank := cardHeight(hosts[i], rows), hosts[i].rank()
		for lane := 0; lane < n; lane++ {
			ly := y
			for j := i; j < len(hosts) && ly < y+band; j++ {
				if used[j] || hosts[j].rank() != rank {
					continue
				}
				h := cardHeight(hosts[j], rows)
				if ly+h > y+band {
					continue
				}
				out = append(out, placed{host: hosts[j], lane: lane, x: xs[lane], y: ly, w: ws[lane], h: h})
				used[j] = true
				// The same space between cards stacked in a lane as between bands.
				ly += h + sp.row
			}
		}
		y += band + sp.row
	}
	return out, max(y-sp.row, 0)
}

// chooseLanes picks the lane count: the one nearest to laneTarget-wide lanes,
// then more lanes (down to minLaneW) if the board would not fit on one screen.
//
// Lanes of equal width are what make equal-sized cards line up into rows. Two
// 100-column lanes at 200 columns fit the fleet too, but every row then has a
// tall card beside a short one and the board reads as a staggered pile.
func chooseLanes(width, viewH, maxCols int, pack func(n int) ([]placed, int)) (int, []placed, int) {
	most := max(width/minLaneW, 1)
	if maxCols > 0 {
		most = min(most, maxCols)
	}
	n := min(max(int(math.Round(float64(width)/laneTarget)), 1), most)
	out, h := pack(n)
	for h > viewH && n < most {
		n++
		out, h = pack(n)
	}
	return n, out, h
}

// hostCard draws one machine: a box with its state and name in a label at the
// left of the top border, its GPUs in a label at the right, one row per GPU
// inside, and the column names in its bottom border.
//
// geo is shared by every card on the board, planned for the narrowest lane, so
// columns line up across lanes and the labels are true for all of them.
func hostCard(c *Canvas, p placed, s *theme.Skin, geo rowGeom, cs cardStyle, focused bool, buf []theme.Cell, holders map[string]Holder, now time.Time) {
	t := s.Theme
	h := p.host
	glyph, failed := hostStateGlyph(h)
	titleFG := t.C("title")
	if failed {
		titleFG = t.C("hi_fg")
	}
	border := cardHue(s, h)
	if focused {
		// Only the two strokes around the title light: the frame goes on saying
		// the host's state, so focus cannot hide that a host has gone stale.
		border = s.Hue.Focus
	}
	y, bh := p.y, p.h
	left := []seg{{glyph, glyphColor(t, h), false}, {" " + h.label, titleFG, true}}
	var right []seg
	if h.hasData && h.err == "" {
		// The model in its product line's colour, the count as bright as the
		// text, the VRAM size secondary.
		right = subtitleSegs(h, s)
	}
	c.titledBox(p.x, y, p.w, bh, border, left, right)

	x0 := p.x + 2
	inner := p.w - cardChrome
	first := y + 1
	// Every lane uses the narrowest lane's plan, so columns line up across
	// lanes; a wider lane gives its spare cells to the graph, which ends the row,
	// so the row still ends at the card's margin.
	if geo.graphW > 0 && geo.width() < inner {
		geo.graphW += inner - geo.width()
	}
	hp := hostPlan(geo, inner)
	// The readings take what they need on the row above the VRAM bar; when a lane
	// has nothing left to spare, the plan has already closed their gap.

	switch {
	case h.err != "":
		c.errorLine(x0, first, inner, h.err, t)
		return
	case !h.hasData:
		c.Text(x0, first, "waiting for first sample…", t.C("graph_text"))
		return
	}
	// The sample's age claims the bottom border's right end; a column label it
	// would cut into is left out.
	bottom := y + bh - 1
	footEnd := p.x + p.w - 2
	if h.fresh() > 5*time.Second {
		age := fmt.Sprintf("%.0fs ago", h.fresh().Seconds())
		footEnd = p.x + p.w - 4 - theme.RuneWidth(age)
		c.lineLabel(footEnd+1, bottom, age, t.C("hi_fg"))
	}
	last := p.x + 1
	// The ledger takes the footer's right end, before the sample's age: it is a fact
	// about the machine that changes rarely, and the legend on the left names columns
	// that are always there. A card with no claims gives the space back to the legend.
	if strip := ledgerStrip(h, holders); len(strip) > 0 {
		w := ledgerSegsWidth(strip)
		if p.x+p.w-2-w > last+8 {
			x := p.x + p.w - 2 - w
			// lineLabel's padding cells are drawn in the segment's own first
			// colour so the notch in the border is one run rather than a hole.
			c.Put(x-1, bottom, " ", strip[0].fg)
			cx := x
			for _, sg := range strip {
				cx = c.Text(cx, bottom, sg.text, sg.fg)
			}
			c.Put(cx, bottom, " ", strip[len(strip)-1].fg)
			footEnd = p.x + p.w - 3 - w
		}
	}
	for _, n := range borderLegend(geo, cs.interval) {
		// A label of two colours ("util history") is one string; the colouring
		// happens per word inside it.
		tx := x0 + n.dx
		end := tx + theme.RuneWidth(n.text)
		if tx-1 <= last || end >= footEnd {
			continue
		}
		last = c.legendLabel(tx, bottom, n.text, s)
	}
	// Every other block on a faint band across the whole inner width: two lines
	// of readings with no other mark between them read as one run, and on a busy
	// card the graphs of neighbouring GPUs touched.
	band := t.Band()
	// The machine's own block comes first, what the host has before what is on
	// it, and takes its place in the alternation: it sits on a band, so the first
	// GPU, plain, does not run on from it as one block of four lines, and every
	// GPU keeps the band it had.
	c.FillRect(p.x+1, first, p.w-2, hostRows, band)
	hostBlock(c, x0, first, h, s, hp, cs.glide)
	for i, g := range h.sample.Gpus {
		gy := first + hostRows + i*geo.rows
		if i%2 == 1 {
			c.FillRect(p.x+1, gy, p.w-2, geo.rows, band)
		}
		// The lamp is about the CARD, so its inputs are the card's own two facts:
		// what the ledger says about it and what it is doing. The ledger is keyed
		// by UUID, which is the identity that survives a renumbering; a card the
		// inventory remembers but this sample omitted has no reading, which the
		// lamp treats as "not busy" rather than as idle.
		hv, held := holders[ledgerKey(h.name, g)]
		util := g.UtilPct()
		key := gpuKey(g)
		graph := func(w, rows int) [][]theme.Cell { return h.graphOf(key, h.gpuHist[key], w, rows, &s.Scale.CPU, t) }
		gpuLine(c, x0, gy, g, graph, s, geo, buf,
			lampFor(hv, held, wireGPU{util: util}), now, h.name, cs.glide)
	}
}

// glyphColor is the title glyph's colour: green answering, amber stale or
// connecting, the accent red for a failure.
func glyphColor(t *theme.Theme, h *hostState) theme.RGB {
	switch {
	case h.err != "":
		return t.C("hi_fg")
	case !h.hasData:
		return t.C("graph_text")
	case h.fresh() > 5*time.Second:
		return t.C("warn")
	}
	return t.C("ok")
}

// fitExact right-aligns txt in exactly w cells, cutting it with an ellipsis only
// when it is wider than w.
func fitExact(txt string, w int) string {
	if theme.RuneWidth(txt) > w {
		txt = theme.Clip(txt, w)
	}
	return theme.PadLeft(txt, w)
}

// meterScale draws a meter in a metric's own scale.
//
// The filled cells interpolate from the ramp's start to THE READING, not from 0
// to 100: a 20% bar walks the first fifth of the ramp across its four cells, and
// a 95% bar walks almost the whole thing. That is the only rule that gives both
// properties at once —
//
//   - every bar is a gradient, however short (btop's cell-position indexing gets
//     this, but paints a quiet card in the alarm end of the scale);
//   - the bar's last cell is the reading's own colour, so the number beside it and
//     the bar agree (value indexing gets this, but a 5% bar is one flat cell).
//
// A one-cell bar is necessarily one colour: there is nowhere to interpolate.
func meterScale(width, value int, sc theme.RGBScale, t *theme.Theme) []theme.Cell {
	out := make([]theme.Cell, width)
	for i := range out {
		out[i] = meterCell(i, width, value, value, sc, t)
	}
	return out
}

// putMeter draws a meter straight onto the canvas: shown cells' worth of length,
// coloured for the reading. At the frame rate the slice meterScale returns was a
// quarter of everything a frame allocated.
//
// The two differ only while the bar glides, and they are separate so that a
// glide moves the bar's end and nothing else. Coloured for the length drawn, every
// cell of the bar took a new shade in every frame of the move — the whole bar
// rewritten thirty times a second, which was most of what the terminal was sent.
func putMeter(c *Canvas, x, y, width, shown, value int, sc theme.RGBScale, t *theme.Theme) {
	for i := 0; i < width; i++ {
		c.PutCell(x+i, y, meterCell(i, width, shown, value, sc, t))
	}
}

// meterCell is cell i of a width-cell meter drawn shown long and coloured for
// value: the cells up to the reading walk the ramp from its start to the
// reading's colour, and any drawn past it (a bar still shrinking to the reading)
// take the colour of their own position, which carries on lighter.
func meterCell(i, width, shown, value int, sc theme.RGBScale, t *theme.Theme) theme.Cell {
	if i < (shown*width+50)/100 {
		on := (value*width + 50) / 100 // the reading's filled cells, rounded
		var f float64
		switch {
		case i >= on && width > 1:
			f = float64(i) / float64(width-1)
		case on > 1:
			// Where this cell sits between the ramp's start and the reading.
			f = float64(value) / 100 * float64(i) / float64(on-1)
		default:
			f = float64(value) / 100
		}
		return theme.Cell{Text: "■", FG: sc.AtFraction(f)}
	}
	// The rest of the bar is a track, not a colour: darker than the meter
	// background, so a row of empty cells does not read as a grey bar of its
	// own beside the values.
	return theme.Cell{Text: "■", FG: t.Track(), Dim: true}
}

// writePct writes "59%" in fg, the sign dimmer than the digits. The sign is part
// of the reserved width: a value wide enough to fill it loses the sign rather
// than push the row's next element sideways.
func writePct(c *Canvas, x, y, v int, fg theme.RGB, t *theme.Theme) {
	num := fmt.Sprint(v)
	if theme.RuneWidth(num) >= pctW {
		c.Text(x, y, theme.Clip(num, pctW), fg)
		return
	}
	end := c.Text(x, y, num, fg)
	c.Text(end, y, "%", mix(fg, t.C("main_bg"), 0.45))
}

// splitUnit splits a reading at its unit: "59°C" -> "59", "°C"; "19.0G" ->
// "19.0", "G"; "125.8G/140.4G" keeps its total, which is the same kind of
// number.
func splitUnit(txt string) (string, string) {
	cut := len(txt)
	for cut > 0 && (txt[cut-1] < '0' || txt[cut-1] > '9') && txt[cut-1] != 'G' && txt[cut-1] != 'M' {
		cut--
	}
	return txt[:cut], txt[cut:]
}

// digitRight writes a reading so its last DIGIT ends at column end, with its
// unit after it: two rows of such readings line up on the ones place.
func digitRight(c *Canvas, end, y int, txt string, known bool, fg theme.RGB, t *theme.Theme) {
	if !known {
		c.Text(end, y, "—", t.C("inactive_fg"))
		return
	}
	num, unit := splitUnit(txt)
	ux := c.Text(end-theme.RuneWidth(num)+1, y, num, fg)
	c.Text(ux, y, unit, t.C("graph_text"))
}

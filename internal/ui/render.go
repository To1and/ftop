package ui

import (
	"fmt"
	"time"

	"ftop/internal/theme"
)

// fleetCanvas draws the overview: the fleet header, a one-line column legend, and
// the cards packed into lanes.
//
// The board is laid out at its full height first. When it is taller than the
// terminal it is drawn offscreen and the visible window blitted in, the same way
// the detail view scrolls, so a scrolled card never loses its top border.
func (a *App) fleetCanvas() *Canvas {
	s := a.skin
	c := a.canvas(a.width, a.height, s.Bg)
	a.hits = a.hits[:0]

	top := a.drawHeader(c, 0, 0, a.width)
	hosts := a.sortedHosts()
	graph := a.graphRows > 0
	maxScroll := a.drawCards(c, top, hosts, graph)
	if len(hosts) == 0 && a.hiddenCount() > 0 {
		// Every host is filtered out. An empty board would read as a fleet that
		// has nothing in it, so it says what is hidden and how to show it.
		msg := fmt.Sprintf("%s hidden: no GPU, or a platform the probe cannot run on · o to show", hostsText(a.hiddenCount()))
		c.Text(2, top+1, theme.Clip(msg, a.width-4), s.Theme.C("graph_text"))
	}
	a.drawStatus(c, a.height-1)
	a.drawScrollHint(c, a.boardScroll, maxScroll)
	return c
}

// follow scrolls the board so the focused host is in view, once, after a move.
func (a *App) followFocus(cards []placed, viewH int) {
	if !a.follow {
		return
	}
	focus := a.focused()
	for _, p := range cards {
		if p.host.name != focus {
			continue
		}
		if p.y < a.boardScroll {
			a.boardScroll = p.y
		}
		if p.y+p.h > a.boardScroll+viewH {
			a.boardScroll = p.y + p.h - viewH
		}
	}
	a.follow = false
}

// drawCards lays the hosts out as boxed cards in lanes below row top and returns
// how far the board can scroll.
//
// Cards keep a column of space between lanes and a row between bands: boxes
// whose borders touched drew more lines than data, and put each card's column
// labels right against the next card's title.
func (a *App) drawCards(c *Canvas, top int, hosts []*hostState, graph bool) int {
	s := a.skin
	viewH := max(a.height-1-top, 0)
	sp := spacing{col: 1, row: stackGap}
	lanes, cards, boardH := chooseLanes(a.width, viewH, a.maxCols, func(n int) ([]placed, int) {
		return pack(hosts, a.width, n, sp)
	})
	a.lanes, a.placed = lanes, cards
	geo := boardGeom(a.width, lanes, sp.col, idxWidth(hosts), graph)
	// Read without taking a.mu: every caller of drawCards already holds it (View and
	// PlainFrame both lock around the whole render), and a second Lock here deadlocks
	// the first frame.
	holders := a.holders
	cs := cardStyle{interval: a.interval, glide: a.glideTo}
	// One timestamp for the whole frame: every lamp on the board turns in step,
	// which is what makes them read as lamps on one machine rather than as eight
	// independent animations.
	now := a.clock()

	a.followFocus(cards, viewH)
	maxScroll := max(boardH-viewH, 0)
	a.boardScroll = min(max(a.boardScroll, 0), maxScroll)
	a.boardTop, a.boardView = top, viewH

	// A single card has nothing to move between; a permanent highlight on it is
	// noise rather than information.
	focus := a.focused()
	highlight := len(cards) > 1
	buf := make([]theme.Cell, 0, maxBar+1)
	if maxScroll == 0 {
		for _, p := range cards {
			p.y += top
			hostCard(c, p, s, geo, cs, highlight && p.host.name == focus, buf, holders, now)
		}
	} else {
		body := a.bodyCanvas(a.width, boardH, s.Bg)
		for _, p := range cards {
			hostCard(body, p, s, geo, cs, highlight && p.host.name == focus, buf, holders, now)
		}
		c.Blit(body, 0, a.boardScroll, 0, top, a.width, viewH)
	}
	return maxScroll
}

// drawScrollHint says, beside the status summary, that the view scrolls and
// where it is. Clicking it pages on, back to the top once at the end.
func (a *App) drawScrollHint(c *Canvas, scroll, maxScroll int) {
	if maxScroll <= 0 {
		return
	}
	t := a.skin.Theme
	hint, col, key := "↕ scrolled ", t.C("graph_text"), "pgdown"
	switch {
	case scroll == 0:
		hint, col = "↓ more ", t.C("hi_fg")
		if n := a.hostsBelow(scroll); n > 0 {
			// "more" said there is something below; how many says whether one
			// page shows the fleet.
			hint = fmt.Sprintf("↓ %d below ", n)
			if n == 1 {
				hint = "↓ 1 below "
			}
		}
	case scroll >= maxScroll:
		hint, key = "↑ end ", "home"
	}
	right := a.width - 2 - theme.RuneWidth(a.statusRight())
	x := right - theme.RuneWidth(hint)
	c.TextRight(right, a.height-1, hint, col)
	a.hits = append(a.hits, hit{x: x, y: a.height - 1, w: theme.RuneWidth(hint) - 1, key: key})
}

// spanLabel renders a graph's time span compactly: "44s", "3m".
func spanLabel(d time.Duration) string {
	if d < 2*time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
	}
	return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
}

// statusRight is the right-hand side of the status line.
func (a *App) statusRight() string {
	graph := "history on"
	if a.graphRows == 0 {
		graph = "history off"
	}
	right := fmt.Sprintf("%d lanes · %s ", max(a.lanes, 1), graph)
	// Hosts the settings leave out are counted where the reader looks for what
	// the board is showing: a fleet that is smaller than expected says why.
	if n := a.hiddenCount(); n > 0 {
		right = fmt.Sprintf("%d hidden · ", n) + right
	}
	// A ledger that could not be read is the most serious thing this line can say:
	// the holders drawn on the board are then the last ones ftop managed to read,
	// and a reader who is not told will take them for current. It outranks the
	// appearance summary for the same reason the skill note does.
	if a.ledgerErr != "" {
		return "ledger unreadable: holders may be stale · " + right
	}
	if a.skillNote != "" {
		// The manual's state is the one thing on this line that is not about the
		// board's appearance: an agent whose instructions are stale is a real
		// problem, so it comes first and the appearance summary yields to it.
		return a.skillNote + " · " + right
	}
	return right
}

// drawStatus pins the board's key hints to the last line.
func (a *App) drawStatus(c *Canvas, _ int) {
	// "?" comes second: on a narrow terminal the hints that do not fit are
	// dropped from the end, and the help screen is where the rest are listed.
	// Opening a host is what a board is for, so it comes first; quitting is the
	// last thing a reader wants to be shown.
	hints := []keyHint{{"↵", "open", "enter"}, {"↑↓←→", "select", ""}, {"?", "help", "?"}, {"o", "settings", "o"}, {"c", "history", "c"}}
	if a.onSkill != nil {
		hints = append(hints, keyHint{"s", "skill", "s"})
	}
	hints = append(hints, keyHint{"q", "quit", "q"})
	a.drawStatusKeys(c, hints)
}

// statusReserve is the room the right end of the status line needs: the lane
// summary plus the longest scroll hint.
func (a *App) statusReserve() int {
	return theme.RuneWidth(a.statusRight()) + theme.RuneWidth("↕ scrolled ") + 3
}

// keyHint is one status-line hint: the key as shown, what it does, and the key
// name a click on it presses ("" for a hint that is not clickable).
type keyHint struct {
	label, desc, key string
}

// drawStatusKeys draws a status line with the given key hints, btop-style: the
// key in the accent colour, its action dimmed. A hint that would run into the
// right-hand summary is left out whole, never overprinted. Each drawn hint is a
// click target for its key.
func (a *App) drawStatusKeys(c *Canvas, keys []keyHint) {
	t := a.skin.Theme
	line := a.height - 1
	// Clear the row first: content drawn before the footer (a box that ran long, a
	// horizon line) must not show through the gaps between the hints.
	for i := 0; i < a.width; i++ {
		c.Put(i, line, " ", t.C("inactive_fg"))
	}
	limit := a.width - a.statusReserve()
	x := 1
	for _, k := range keys {
		// Each key on a faint cap, its name in the title colour: a status line of
		// six red words read as six warnings.
		cap := theme.RuneWidth(k.label) + 2
		w := cap + 1 + theme.RuneWidth(k.desc)
		if x+w > limit {
			break
		}
		if k.key != "" {
			a.hits = append(a.hits, hit{x: x, y: line, w: w, key: k.key})
		}
		c.Fill(x, line, cap, mix(a.skin.Bg, t.C("main_fg"), 0.16))
		c.Text(x+1, line, k.label, t.C("title"))
		x = c.Text(x+cap+1, line, k.desc, t.C("graph_text"))
		x += 2
	}
	if a.width > theme.RuneWidth(a.statusRight())+2 {
		c.TextRight(a.width, line, a.statusRight(), t.C("graph_text"))
	}
}

// helpCanvas documents the keys that exist, and how to read the board.
//
// It is a panel over the board, not a page in place of it: the mouse and the
// keys still address the help, and the board showing through behind it says
// which screen the explanation is for. The "reading the board" group exists
// because a reader who could not tell what the history's span meant needed the
// readings explained, and a list of keys never did that.
func (a *App) helpCanvas() *Canvas {
	s := a.skin
	t := s.Theme
	c := a.fleetCanvas()
	// The board behind recedes: every cell shaded toward the page colour, so the
	// panel is the one thing in focus.
	for i := range c.cells {
		cl := &c.cells[i]
		cl.FG = mix(cl.FG, s.Bg, 0.72)
		cl.BG = mix(cl.BG, s.Bg, 0.86)
		cl.Bold = false
	}

	type row struct{ key, desc string }
	groups := []struct {
		name string
		rows []row
	}{
		{"board", []row{
			{"↑↓←→  hjkl", "move between cards"},
			{"tab", "next host in board order"},
			{"enter", "open the selected host"},
			{"pgup  pgdn", "page the board"},
			{"o", "settings: hosts without a GPU, other platforms"},
		}},
		{"host view", []row{
			{"← →  k j", "previous / next host"},
			{"g  G", "first / last host"},
			{"↑  ↓", "scroll"},
			{"p", "the host's processes, by memory and by cpu"},
			{"esc", "back to the board"},
		}},
		{"mouse", []row{
			{"click", "select a card; click again to open it"},
			{"wheel", "scroll the board or a host"},
		}},
		{"reading the board", []row{
			{"util", "a device's utilisation, 0-100%"},
			{"vram", "its memory, used of total"},
			{"util history", "that device's own utilisation, newest at the right"},
			{"cpu  mem", "the host's own: busy of its cores, used of its memory"},
			{"—", "not reported; never a zero"},
			{"idle  ≥30%", "cards under 5%  /  at least 30%, and so on up"},
		}},
		{"cgroup and machine", []row{
			{"cgroup", "a numeric limit: every host figure is read inside it"},
			{"whole machine", "no numeric limit: cpu and memory are the host's own"},
			{"cgroup quota", "the cpu percentage is of the quota, not of every core"},
			{"cache", "page cache, already left out of the memory used"},
		}},
	}
	height := 1
	for _, g := range groups {
		height += 1 + len(g.rows) + 1
	}
	height += 3 // the closing notes
	w := min(78, a.width-4)
	if w < 42 {
		w = a.width - 2
	}
	h := min(height+2, a.height-1)
	x := max((a.width-w)/2, 0)
	y := max((a.height-1-h)/2, 0)
	// The panel's rows are cleared full width: a card's title left standing on
	// the panel's own border row read as part of it.
	for j := y; j < y+h; j++ {
		for i := 0; i < a.width; i++ {
			c.Put(i, j, " ", s.Bg)
			c.Fill(i, j, 1, s.Bg)
		}
	}
	// On a narrow panel the closing hint is dropped before the title is cut: the
	// title says what this is, the hint only repeats a key already on the status
	// line.
	var close []seg
	if tabWidth([]seg{{"? or esc to close", t.C("graph_text"), false}}) < w-2*labelInset-tabWidth([]seg{{"ftop help", t.C("title"), true}})-1 {
		close = []seg{{"? or esc to close", t.C("graph_text"), false}}
	}
	c.titledBox(x, y, w, h, s.Hue.Frame,
		[]seg{{"ftop help", t.C("title"), true}}, close)

	const keyW = 13
	cy := y + 1
	fits := func() bool { return cy < y+h-2 }
	for _, g := range groups {
		if !fits() {
			break
		}
		c.TextBold(x+2, cy, g.name, t.C("title"))
		cy++
		for _, r := range g.rows {
			if !fits() {
				break
			}
			c.Text(x+3, cy, theme.Clip(r.key, keyW), t.C("hi_fg"))
			c.Text(x+3+keyW+2, cy, theme.Clip(r.desc, w-3-keyW-2-1), t.C("main_fg"))
			cy++
		}
		cy++
	}
	for _, note := range []string{
		"Devices only on the board; processes are in a host's view.",
		"The order changes only when a host goes online or offline.",
		"Mouse on: hold Shift (Option in iTerm2) to select text.",
	} {
		if !fits() {
			break
		}
		c.Text(x+2, cy, theme.Clip(note, w-3), t.C("graph_text"))
		cy++
	}
	return c
}

// hostsBelow counts the hosts whose cards are not fully visible at this scroll
// offset: the number the scroll hint gives, so "more" becomes how much more.
func (a *App) hostsBelow(scroll int) int {
	n := 0
	for _, p := range a.placed {
		if p.y+p.h > scroll+a.boardView {
			n++
		}
	}
	return n
}

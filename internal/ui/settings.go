package ui

import (
	"fmt"
	"strconv"
	"strings"

	"ftop/internal/theme"
)

// The settings panel: the board's display options, drawn over the board the
// way the help is, so the board showing through says which screen it changes.
//
// Every row is a switch and nothing here is saved. A choice that outlives the
// session belongs in config.toml, where it is written down beside the user's
// other choices, rather than in a file ftop rewrites behind them; the panel's
// last line names the keys.

// setting is one row of the panel.
type setting struct {
	name string
	// on reads the switch, flip turns it over.
	on   func(a *App) bool
	flip func(a *App)
	// words are what the state is called, off then on: a host filter hides or
	// shows hosts, the history is off or on.
	words [2]string
	// covers is how many hosts the row is about, or -1 when it is not about
	// hosts. A filter that covers nothing still has a row: it says the fleet has
	// no such machine, which is worth knowing when one was expected.
	covers func(a *App) int
}

// historyRow is the history's row, which its own key also flips.
const historyRow = 2

var settingRows = []setting{
	{
		name:   "hosts without a GPU",
		on:     func(a *App) bool { return a.showCPUOnly },
		flip:   func(a *App) { a.showCPUOnly = !a.showCPUOnly },
		words:  [2]string{"hidden", "shown"},
		covers: func(a *App) int { n, _ := a.filtered(); return n },
	},
	{
		name:   "unsupported platforms",
		on:     func(a *App) bool { return a.showUnsupported },
		flip:   func(a *App) { a.showUnsupported = !a.showUnsupported },
		words:  [2]string{"hidden", "shown"},
		covers: func(a *App) int { _, n := a.filtered(); return n },
	},
	{
		name: "util history",
		on:   func(a *App) bool { return a.graphRows > 0 },
		flip: func(a *App) {
			if a.graphRows == 0 {
				a.graphRows = 1
			} else {
				a.graphRows = 0
			}
		},
		words:  [2]string{"off", "on"},
		covers: func(*App) int { return -1 },
	},
}

// settingHit is the click target key for row i.
func settingHit(i int) string { return "setting:" + strconv.Itoa(i) }

// settingsKey acts on a key while the panel is open. Every key but quit goes
// here: the board under the panel does not move while it is being configured.
func (a *App) settingsKey(key string) {
	switch key {
	case "esc", "o":
		a.settings = false
	case "?":
		a.settings, a.help = false, true
	case "down", "j", "tab":
		a.setRow = (a.setRow + 1) % len(settingRows)
	case "up", "k", "shift+tab":
		a.setRow = (a.setRow + len(settingRows) - 1) % len(settingRows)
	case "space", " ", "enter":
		a.flipSetting(a.setRow)
	case "c":
		// The history's own key still works with the panel open, and the row
		// shows it change.
		a.flipSetting(historyRow)
	}
}

// flipSetting turns row i over. Hiding a host can leave nothing focused, so the
// board is asked to bring whatever is focused next into view.
func (a *App) flipSetting(i int) {
	if i < 0 || i >= len(settingRows) {
		return
	}
	a.setRow = i
	settingRows[i].flip(a)
	a.follow = true
}

// settingsCanvas is the panel over the board.
func (a *App) settingsCanvas() *Canvas {
	s := a.skin
	t := s.Theme
	c := a.fleetCanvas()
	for i := range c.cells {
		cl := &c.cells[i]
		cl.FG = mix(cl.FG, s.Bg, 0.72)
		cl.BG = mix(cl.BG, s.Bg, 0.86)
		cl.Bold = false
	}
	// The board's own targets are under the panel now; only the rows answer.
	a.hits = a.hits[:0]

	const nameW, stateW = 24, 8
	notes := []string{
		"↑↓ select   space toggle",
		"For every session: show_cpu_only, show_unsupported in config.toml.",
	}
	h := 1 + 1 + len(settingRows) + 1 + len(notes) + 1
	w := min(70, a.width-4)
	if w < 42 {
		w = a.width - 2
	}
	h = min(h, a.height-1)
	x := max((a.width-w)/2, 0)
	y := max((a.height-1-h)/2, 0)
	for j := y; j < y+h; j++ {
		for i := 0; i < a.width; i++ {
			c.Put(i, j, " ", s.Bg)
			c.Fill(i, j, 1, s.Bg)
		}
	}
	var close []seg
	if tabWidth([]seg{{"o or esc to close", t.C("graph_text"), false}}) < w-2*labelInset-tabWidth([]seg{{"ftop settings", t.C("title"), true}})-1 {
		close = []seg{{"o or esc to close", t.C("graph_text"), false}}
	}
	c.titledBox(x, y, w, h, s.Hue.Frame, []seg{{"ftop settings", t.C("title"), true}}, close)

	cy := y + 1
	fits := func() bool { return cy < y+h-1 }
	if fits() {
		c.TextBold(x+2, cy, "board", t.C("title"))
		cy++
	}
	for i, r := range settingRows {
		if !fits() {
			break
		}
		on := r.on(a)
		if i == a.setRow {
			// The highlighted row sits on the card band, the board's own mark
			// for "this one", rather than a colour of its own.
			c.Fill(x+1, cy, w-2, t.Band())
		}
		cx := x + 3
		c.Text(cx, cy, theme.Clip(r.name, nameW), t.C("main_fg"))
		cx += nameW
		word, fg := r.words[0], t.C("graph_text")
		if on {
			word, fg = r.words[1], t.C("title")
		}
		if cx+stateW <= x+w-1 {
			c.Text(cx, cy, word, fg)
		}
		cx += stateW
		if n := r.covers(a); n >= 0 && cx < x+w-2 {
			c.Text(cx, cy, theme.Clip(hostsText(n), x+w-2-cx), t.C("graph_text"))
		}
		a.hits = append(a.hits, hit{x: x + 1, y: cy, w: w - 2, key: settingHit(i)})
		cy++
	}
	cy++
	for _, n := range notes {
		if !fits() {
			break
		}
		c.Text(x+2, cy, theme.Clip(n, w-3), t.C("graph_text"))
		cy++
	}
	return c
}

// hostsText is "no hosts", "1 host", "3 hosts".
func hostsText(n int) string {
	switch n {
	case 0:
		return "no hosts"
	case 1:
		return "1 host"
	}
	return fmt.Sprintf("%d hosts", n)
}

// settingClicked reports which row a hit key names, if any.
func settingClicked(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, "setting:")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	return i, err == nil
}

package ui

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"ftop/internal/wire"
)

// FleetJSON is the snapshot shape the design prototypes render from.
//
// It exists so a layout experiment can be driven by real fleet data instead of
// invented numbers: every design bug worth finding (16 processes per card, a card
// with no reading, a host that never answered) only shows up in real data.
type FleetJSON struct {
	At      time.Time  `json:"at"`
	Hosts   []HostJSON `json:"hosts"`
	Columns int        `json:"columns"`
	Graph   int        `json:"graphRows"`
}

// HostJSON is one machine as the board sees it.
type HostJSON struct {
	Name       string          `json:"name"`
	Label      string          `json:"label"`
	Err        string          `json:"err,omitempty"`
	HasData    bool            `json:"hasData"`
	Cores      int             `json:"cores"`
	CPUQuota   bool            `json:"cpuQuota"`
	CPU        *float64        `json:"cpu"`
	Load1      float64         `json:"load1"`
	Load5      float64         `json:"load5"`
	Load15     float64         `json:"load15"`
	MemKB      uint64          `json:"memKB"`
	TotalKB    uint64          `json:"totalKB"`
	MemLimited bool            `json:"memLimited"`
	CacheKB    uint64          `json:"cacheKB"`
	SwapKB     uint64          `json:"swapKB"`
	SwapTotal  uint64          `json:"swapTotalKB"`
	SwapKnown  bool            `json:"swapKnown"`
	Procs      int             `json:"procs"`
	UptimeSec  float64         `json:"uptimeSec"`
	Driver     string          `json:"driver"`
	HostProcs  []wire.HostProc `json:"hostProcs,omitempty"`
	ProcsStale bool            `json:"hostProcsStale"`
	Gpus       []GpuJSON       `json:"gpus"`
	// History holds null for seconds with no readable card; JSON has no NaN.
	History []*float64 `json:"history"`
	AgeSec  float64    `json:"ageSec"`
}

// GpuJSON is one device. Nil-able measurements stay nil so the prototype can
// render "unknown" the same way the TUI does.
type GpuJSON struct {
	Index    int         `json:"index"`
	Name     string      `json:"name"`
	UUID     string      `json:"uuid"`
	Util     *float64    `json:"util"`
	MemUsed  *uint64     `json:"memUsed"`
	MemTotal *uint64     `json:"memTotal"`
	Temp     *float64    `json:"temp"`
	Power    *float64    `json:"power"`
	PowerCap *float64    `json:"powerCap"`
	ClockSM  *float64    `json:"clockSM"`
	Fan      *float64    `json:"fan"`
	Procs    []wire.Proc `json:"procs"`
	Stale    bool        `json:"staleProc"`
}

// ExportJSON serialises the current board. It takes the same lock the renderer
// does, so a snapshot never straddles an update.
func (a *App) ExportJSON() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := FleetJSON{At: time.Now(), Columns: a.maxCols, Graph: a.graphRows}
	for _, n := range a.order {
		st, ok := a.states[n]
		if !ok {
			continue
		}
		h := HostJSON{
			Name:    st.name,
			Label:   st.label,
			Err:     st.err,
			HasData: st.hasData,
			AgeSec:  st.fresh().Seconds(),
		}
		if st.hasData {
			s := st.sample
			h.Cores = s.Cores
			h.CPUQuota = s.CPUQuota
			h.CPU = s.CPUPct
			h.Load1, h.Load5, h.Load15 = s.Load1, s.Load5, s.Load15
			h.MemKB, h.TotalKB, h.MemLimited, h.CacheKB = s.MemKB, s.TotalKB, s.MemLimited, s.CacheKB
			h.SwapKB, h.SwapTotal, h.SwapKnown = s.SwapKB, s.SwapTotalKB, s.SwapKnown
			h.Procs = s.Procs
			h.UptimeSec = s.Uptime.Seconds()
			h.Driver = s.Driver
			h.HostProcs, h.ProcsStale = st.hostProcs, st.hostProcsStale
			for _, v := range st.history {
				if math.IsNaN(v) {
					h.History = append(h.History, nil)
				} else {
					h.History = append(h.History, &v)
				}
			}
			for _, g := range s.Gpus {
				h.Gpus = append(h.Gpus, GpuJSON{
					Index: g.Index, Name: g.Name, UUID: g.UUID,
					Util: g.Util, MemUsed: g.MemUsed, MemTotal: g.MemTotal,
					Temp: g.Temp, Power: g.Power, PowerCap: g.PowerCap,
					ClockSM: g.ClockSM, Fan: g.Fan,
					Procs: g.Procs, Stale: g.ProcsStale,
				})
			}
		}
		out.Hosts = append(out.Hosts, h)
	}
	return json.MarshalIndent(out, "", "  ")
}

// HTML renders the canvas as a <pre> with inline colours, for design review: the
// same cells the terminal receives, so a screenshot of it is a screenshot of the
// board rather than of a mock-up.
func (c *Canvas) HTML() string {
	var b strings.Builder
	b.Grow(c.W * c.H * 4)
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	for y := 0; y < c.H; y++ {
		x := 0
		for x < c.W {
			cell := &c.cells[y*c.W+x]
			if cell.Cont {
				x++
				continue
			}
			weight := ""
			if cell.Bold {
				weight = ";font-weight:700"
			}
			fmt.Fprintf(&b, `<span style="color:%s;background:%s%s">`, cell.FG.Hex(), cell.BG.Hex(), weight)
			for x < c.W {
				cl := &c.cells[y*c.W+x]
				if cl.Cont {
					x++
					continue
				}
				if cl.FG != cell.FG || cl.BG != cell.BG || cl.Bold != cell.Bold {
					break
				}
				if cl.Text == "" {
					b.WriteByte(' ')
				} else {
					b.WriteString(esc.Replace(cl.Text))
				}
				x++
			}
			b.WriteString("</span>")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// RenderHTML renders the overview at a given terminal size.
func (a *App) RenderHTML(w, h int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	a.procView = false
	return a.fleetCanvas().HTML()
}

// RenderDetailHTML renders one host's detail view at a given terminal size.
func (a *App) RenderDetailHTML(host string, w, h int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	a.procView = false
	a.cursor = a.indexOf(host)
	return a.detailCanvas().HTML()
}

// RenderPlain renders the overview as text with no colour, for tests and for
// reading a layout in a terminal that is not the real one.
func (a *App) RenderPlain(w, h int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	a.procView = false
	return a.fleetCanvas().Plain()
}

// RenderDetailPlain is RenderPlain for one host's view.
func (a *App) RenderDetailPlain(host string, w, h int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	a.procView = false
	a.cursor = a.indexOf(host)
	return a.detailCanvas().Plain()
}

// RenderProcHTML renders one host's process table as coloured cells.
func (a *App) RenderProcHTML(host string, w, h int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	a.cursor = a.indexOf(host)
	a.detail, a.procScroll = true, 0
	a.procView = true
	return a.detailCanvas().HTML()
}

// RenderProcPlain renders one host's process table as text.
func (a *App) RenderProcPlain(host string, w, h int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	a.cursor = a.indexOf(host)
	// The view is a state, not an argument: set it for the render, and leave it
	// set so a caller can page through the same table.
	a.detail, a.procScroll = true, 0
	a.procView = true
	return a.detailCanvas().Plain()
}

// RenderProcFrame is RenderProcPlain with braille cells blanked and returned as
// Dots, for the prototype page's own dot renderer.
func (a *App) RenderProcFrame(host string, w, h int) (string, []Dot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	a.cursor = a.indexOf(host)
	a.detail, a.procScroll = true, 0
	a.procView = true
	return a.detailCanvas().liftDots()
}

// RenderANSI renders the overview at a given terminal size, exactly as View does.
func (a *App) RenderANSI(w, h int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	a.procView = false
	return a.fleetCanvas().ANSI()
}

// RenderDetailANSI renders one host's detail view at a given terminal size.
func (a *App) RenderDetailANSI(host string, w, h int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	a.procView = false
	a.cursor = a.indexOf(host)
	return a.detailCanvas().ANSI()
}

// Dot is a braille cell lifted out of a frame for the prototype page, which
// draws the dots itself the way Ghostty and kitty do: evenly over the whole
// cell, whatever its height. xterm.js draws braille from the font, whose dots
// sit in the glyph's box, so a taller cell left gaps between rows that a
// terminal drawing its own dots does not have.
type Dot struct {
	X, Y int
	Bits int    // the glyph's dot pattern: codepoint - U+2800
	FG   string // "#rrggbb"
}

// RenderFrame is RenderANSI with braille cells blanked and returned as Dots.
func (a *App) RenderFrame(w, h int) (string, []Dot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	a.procView = false
	return a.fleetCanvas().liftDots()
}

// RenderDetailFrame is RenderDetailANSI with braille cells blanked and returned
// as Dots.
func (a *App) RenderDetailFrame(host string, w, h int) (string, []Dot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	a.procView = false
	a.cursor = a.indexOf(host)
	return a.detailCanvas().liftDots()
}

// liftDots returns the canvas's ANSI with every braille cell blank, and the
// cells it blanked.
func (c *Canvas) liftDots() (string, []Dot) {
	var dots []Dot
	saved := make([]Cell, len(c.cells))
	copy(saved, c.cells)
	for i := range c.cells {
		cl := &c.cells[i]
		r := []rune(cl.Text)
		if len(r) == 1 && r[0] >= 0x2800 && r[0] <= 0x28ff {
			if r[0] != 0x2800 {
				dots = append(dots, Dot{X: i % c.W, Y: i / c.W, Bits: int(r[0] - 0x2800), FG: cl.FG.Hex()})
			}
			cl.Text = " "
		}
	}
	out := c.ANSI()
	copy(c.cells, saved)
	return out, dots
}

// DebugPlan reports a two-row plan for a content width, for layout tests.
func DebugPlan(inner int) string {
	g := planGPU(inner, 1, true)
	return fmt.Sprintf("rows=%d idx=%d gap=%d meter=%d graph=%d used=%v temp=%v power=%v utilX=%d vramX=%d readX=%d width=%d graphX=%d",
		g.rows, g.idxW, g.gap, g.meterW, g.graphW, g.used, g.temp, g.power, g.utilX(), g.vramX(), g.readX(), g.width(), g.graphX())
}

// RenderHelpPlain renders the help screen as text, for tests and for reading
// the panel in a terminal.
func (a *App) RenderHelpPlain(w, h int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	return a.helpCanvas().Plain()
}

// RenderHelpFrame is RenderFrame for the help panel.
func (a *App) RenderHelpFrame(w, h int) (string, []Dot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	return a.helpCanvas().liftDots()
}

// RenderSettingsFrame is RenderFrame for the settings panel.
func (a *App) RenderSettingsFrame(w, h int) (string, []Dot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.width, a.height = w, h
	return a.settingsCanvas().liftDots()
}

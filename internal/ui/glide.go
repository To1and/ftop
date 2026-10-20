package ui

import (
	"math"
	"time"

	"ftop/internal/theme"
	"ftop/internal/wire"
)

// Glides: a bar moves to a new reading instead of jumping to it.
//
// A host's sample lands once an interval, and every bar on its card used to take
// its new length in the frame the sample arrived. With a dozen hosts sampled out
// of step, that is a board that twitches somewhere every few hundred
// milliseconds, a cell or a block at a time, and it reads as a display that is
// struggling rather than as one that is live.
//
// So the bars slide: from wherever they are drawn now to the new reading, over
// glideFor, easing out. Only the bars. The number beside a bar is the reading,
// and it changes in the frame the reading does: a percentage that counted up
// through values nobody measured would be the board making figures up.

// glideMin is the smallest change a bar glides for, in percentage points. Below
// it the bar takes the reading at once: on a fourteen-cell meter a cell is seven
// points and an eighth of a skyline row about four, so a smaller change moves a
// bar's end by a cell at most, which no one sees as a jump, and gliding it kept
// the board at the frame rate for every busy card's second-to-second wobble.
const glideMin = 2.0

// glideFor is how long a bar takes to reach a new reading.
//
// Long enough to be seen as motion at the frame rate (a dozen frames), and well
// under any sampling interval, so a bar is at rest on the true reading for most of
// every interval and a reader who glances never catches it in between.
const glideFor = 400 * time.Millisecond

// frameRate is the board's repaint rate while anything on it moves.
//
// 30 is the rate at which motion reads as continuous rather than stepped: a bar
// gliding at 8 visibly clicked through its lengths. A full frame costs about a
// millisecond, so 30 is ~3% of one core, and only while something is moving; a
// board at rest repaints once a second.
const frameRate = 30

// glide is one bar's move from one reading to the next.
type glide struct {
	from, to float64
	start    time.Time
}

// at is the bar's drawn value at a moment.
func (g *glide) at(now time.Time) float64 {
	t := float64(now.Sub(g.start)) / float64(glideFor)
	if t >= 1 {
		return g.to
	}
	if t <= 0 {
		return g.from
	}
	e := 1 - math.Pow(1-t, 3) // ease out: fast off the mark, settling onto the reading
	return g.from + (g.to-g.from)*e
}

// barID names one bar: a host's GPU's metric, a host's own metric (gpu empty),
// or the fleet's (host and gpu empty). A struct rather than a joined string, so
// asking for a bar's length allocates nothing.
type barID struct{ host, gpu, metric string }

// glider is how draw code asks for a bar's drawn value: the key names the bar,
// v is its reading. A nil glider draws every bar at its reading.
type glider func(key barID, v float64) float64

func (g glider) to(key barID, v float64) float64 {
	if g == nil {
		return v
	}
	return g(key, v)
}

// glideTo is a bar's drawn value this frame.
//
// The move starts in the first frame that sees a new reading, from wherever the
// bar was drawn in the frame before, so a reading that changes again mid-move
// turns the bar around smoothly instead of snapping it back to the old value.
//
// Unknown readings are not moved: they draw as a dash, and a bar that slid out
// of a dash would invent a value for the gap. Nor is anything moved before the
// program is running — -once, the demo snapshot and the tests all draw a single
// frame, which must show the readings themselves.
func (a *App) glideTo(key barID, v float64) float64 {
	if !a.animate || v < 0 || math.IsNaN(v) {
		delete(a.glides, key)
		return v
	}
	now := a.clock()
	g := a.glides[key]
	if g == nil {
		if a.glides == nil {
			a.glides = map[barID]*glide{}
		}
		a.glides[key] = &glide{from: v, to: v, start: now}
		return v
	}
	switch d := math.Abs(v - g.to); {
	case d == 0:
	case d < glideMin && g.at(now) == g.to:
		g.from, g.to = v, v // at rest, and too small a change to move for
	default:
		g.from, g.to, g.start = g.at(now), v, now
		if end := now.Add(glideFor).UnixNano(); end > a.glideUntil.Load() {
			a.glideUntil.Store(end)
		}
	}
	return g.at(now)
}

// barKey names one GPU bar: the same key in the skyline and on the card, so the
// two draw one move and agree in every frame.
func barKey(host string, g wire.Gpu, metric string) barID {
	return barID{host, gpuKey(g), metric}
}

// moving reports whether a bar is gliding, or a sample has just landed and the
// frames that will start its glides have yet to be drawn. It reads atomics, for
// the same reason tickPeriod's other inputs do: the tick must never wait on the
// render lock.
//
// The second half matters because a glide starts in the frame that draws its
// reading, after the tick that scheduled that frame has already chosen its
// period; a sample buys a couple of frames at the frame rate, and the glides
// they start keep it there. A sample that moves no bar — an idle card reading
// 0% again — costs those frames and no more.
func (a *App) moving() bool {
	now := time.Now().UnixNano()
	return now-a.changed.Load() < int64(3*time.Second/frameRate) || now < a.glideUntil.Load()
}

// graphMemo is one history's graph as last drawn, and what it was drawn from.
type graphMemo struct {
	seq, w, h int
	sc        *theme.RGBScale
	rows      [][]theme.Cell
}

// graphOf is a history's braille graph, drawn again only when the history or the
// space for it has changed.
//
// A history moves once a sample, and the board repaints thirty times a second
// while a bar glides; recomputing every card's graph each frame was two thirds of
// what a frame allocated, for a picture that had not changed.
func (h *hostState) graphOf(key string, hist []float64, w, rows int, sc *theme.RGBScale, t *theme.Theme) [][]theme.Cell {
	m := h.graphs[key]
	if m != nil && m.seq == h.seq && m.w == w && m.h == rows && m.sc == sc {
		return m.rows
	}
	if h.graphs == nil {
		h.graphs = map[string]*graphMemo{}
	}
	out := theme.Graph(t, hist, w, rows, "", theme.GraphOpts{NoZero: true, Scale: sc})
	h.graphs[key] = &graphMemo{seq: h.seq, w: w, h: rows, sc: sc, rows: out}
	return out
}

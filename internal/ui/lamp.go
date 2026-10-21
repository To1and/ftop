package ui

import (
	"time"

	"ftop/internal/theme"
)

// The lamp: one cell per GPU, under its index, saying whether an agent holds the
// card.
//
// It exists because the footer's "held:" line is a summary of the MACHINE and a
// card is a list of DEVICES. "held: claude-code×4" on a eight-card host leaves the
// reader counting rows to find which four, and it says nothing at all about the
// four that are free. The index column already has a cell beside every GPU that no
// other field uses, so the answer can sit next to the row it is about.
//
// A lamp has three things to say:
//
//	free            a dark dot, still
//	held            the holder's resting mark, in its brand colour, still
//	held and busy   the holder's own spinner, in its brand colour, turning
//
// Each agent is drawn in its own idiom: the spinner it shows in its own terminal
// while it works, and the mark it rests on — Claude Code's ✻ flower, Codex's
// braille dots. A reader who uses these tools already reads that spinner as "this
// agent is working", so the lamp borrows a meaning instead of teaching one, and
// the shape says who holds the card before the colour does.
//
// Motion means "running now", which is what a spinner means everywhere else. A
// card can be claimed by an agent that is between jobs, and it can be claimed by
// one that is mid-run; those are different facts about the machine, and a still
// mark beside a turning one tells a stale claim from live work without reading
// the bar. (The first lamp had it the other way round, breathing while idle; a
// pulse on the claim that was doing nothing read as the busy one.)
//
// The lamp's colour is the holder's, not a single "agent" colour: two agents can
// hold cards on one host, and the brand colour is what tells them apart where a
// glyph is too small to.

// snapshotAt is the moment a snapshot draws its lamps at (-once, the demo, and
// tests): a fixed phase, so two runs print the same bytes. It is mid-cycle for
// every spinner, where each shows a full mark rather than its opening dot.
const snapshotAt = 500 * time.Millisecond

// lampGlyph is a lit lamp whose holder has no idiom of its own.
//
// A filled circle is the one shape that reads as a lamp at a single cell: it is
// solid at every size, which the crescent and ring glyphs are not, and it does not
// collide with the geometry the board already uses for data (■ for a meter's
// cell, braille for a graph).
const (
	lampGlyph   = "●"
	lampGlyphID = "·"
)

// lampStyle is one holder's idiom: the frames it turns through while busy, how
// long it shows each, and the mark it rests on.
type lampStyle struct {
	spin []string
	step time.Duration
	rest string
}

// lampStyles are the idioms the lamp knows, keyed by the claim's runtime.
//
// Claude Code turns ·✢✳✶✻✽ and back and rests on ✻ (its "worked for" line).
// Codex turns the ten-frame braille spinner of codex-tui's status line and rests
// on the • its working line begins with. A holder ftop has no idiom for —
// "human", or an agent that named itself — turns a half-lit disc and rests on
// the plain dot: it still says busy or idle, in a shape that claims no brand.
//
// Every spinner steps on one 100 ms clock, near both agents' own (Claude Code
// 120 ms, Codex 80 ms). At their own rates the two together changed the screen
// on the steps of either, 17 times a second, and every change is a whole frame
// for the renderer to compare and send; on one clock it is 10.
var lampStyles = map[string]lampStyle{
	"claude-code": {
		spin: []string{"·", "✢", "✳", "✶", "✻", "✽", "✻", "✶", "✳", "✢"},
		step: 100 * time.Millisecond,
		rest: "✻",
	},
	"codex": {
		spin: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		step: 100 * time.Millisecond,
		rest: "•",
	},
}

var lampStyleOther = lampStyle{
	spin: []string{"◐", "◓", "◑", "◒"},
	step: 200 * time.Millisecond,
	rest: lampGlyph,
}

// lampState is what a card's lamp has to say this frame.
type lampState struct {
	// lit is false for a free card and for a card the ledger has no answer for.
	lit bool
	// colour is the holder's brand colour when lit.
	colour theme.RGB
	// busy is true when the card is being used, which sets the lamp turning.
	busy bool
	// overdue marks a claim past its declared time; it is drawn as a still dot
	// in the warning colour.
	overdue bool
	// kind is the holder's runtime ("claude-code", "codex", "human"), which
	// picks the idiom the lamp is drawn in.
	kind string
}

// lampFor decides a card's lamp from the ledger and the measurement.
//
// The two are read together and kept separate in the result, because they can
// disagree and the disagreement is the useful part: a held card at 95% is a live
// run, a held card at 2% is a claim with nothing on it yet.
func lampFor(h Holder, ok bool, g wireGPU) lampState {
	if !ok {
		return lampState{}
	}
	// "Busy" is a measured utilisation above the noise floor. A card that reports
	// nothing is not busy: unknown is not activity, and drawing it as busy would
	// hide a holder that is sitting on a card whose driver stopped answering.
	busy := g.util >= 10
	return lampState{
		lit:     true,
		colour:  agentColourFor(h.Owner),
		busy:    busy,
		overdue: h.Overdue,
		kind:    kindOf(h.Owner),
	}
}

// wireGPU is the part of a GPU reading the lamp needs; it keeps lampFor free of
// the wire package so the decision can be tested without building a sample.
type wireGPU struct{ util float64 }

// lampCell renders a lamp's glyph and colour at a moment.
//
// The frame is a function of the frame's timestamp, never of the wall clock: the
// same frame drawn twice is the same picture, which is what lets -once and the
// plain-text snapshot be compared against a golden file. Every lamp of one
// holder turns in step, so a host's busy cards read as one run.
func lampCell(st lampState, now time.Time) (glyph string, fg theme.RGB) {
	if !st.lit {
		// A free card's lamp is drawn, not left blank. Blank is also what a lane
		// with no room for a lamp looks like, and the two must not read alike.
		return lampGlyphID, theme.Default.Track()
	}
	if st.overdue {
		// Still, and in the warning colour rather than the holder's: an overdue
		// claim is the one state the reader must not mistake for live work.
		return lampGlyph, theme.Default.C("warn")
	}
	sty, ok := lampStyles[st.kind]
	if !ok {
		sty = lampStyleOther
	}
	if !st.busy {
		return sty.rest, st.colour
	}
	return sty.spin[int(now.UnixNano()/int64(sty.step))%len(sty.spin)], st.colour
}

// agentColourFor maps a claim's owner string ("kind/session") to its brand colour.
func agentColourFor(owner string) theme.RGB {
	return theme.Default.AgentColour(kindOf(owner))
}

// kindOf is the runtime part of a claim's owner string ("kind/session").
func kindOf(owner string) string {
	for i := 0; i < len(owner); i++ {
		if owner[i] == '/' {
			return owner[:i]
		}
	}
	return owner
}

// lampStep is how long a lamp shows each frame of its spinner, or 0 for a lamp
// that holds still.
func lampStep(st lampState) time.Duration {
	if !st.lit || st.overdue || !st.busy {
		return 0
	}
	sty, ok := lampStyles[st.kind]
	if !ok {
		sty = lampStyleOther
	}
	return sty.step
}

// refreshSpin records how often the turning lamps change frame: the greatest step
// that lands on every one of their frames, so lamps of different steps on one
// screen are all drawn on time. It keeps two answers, the whole board's and the
// host under the cursor's, because a host view draws only that host's lamps.
// Called with a.mu held, wherever the holders, the readings or the cursor change.
func (a *App) refreshSpin() {
	shown := ""
	if len(a.order) > 0 {
		shown = a.order[min(max(a.cursor, 0), len(a.order)-1)]
	}
	var step, here time.Duration
	for _, st := range a.states {
		for _, g := range st.sample.Gpus {
			h, ok := a.holders[ledgerKey(st.name, g)]
			if s := lampStep(lampFor(h, ok, wireGPU{util: g.UtilPct()})); s > 0 {
				step = gcdDuration(step, s)
				if st.name == shown {
					here = gcdDuration(here, s)
				}
			}
		}
	}
	a.spinStep.Store(int64(step))
	a.hostSpin.Store(int64(here))
}

// refreshSpinLocked is refreshSpin for a caller that does not hold a.mu: a key or
// a click that moved the cursor to another host.
func (a *App) refreshSpinLocked() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refreshSpin()
}

func gcdDuration(a, b time.Duration) time.Duration {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

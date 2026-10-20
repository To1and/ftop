package ui

import (
	"math"
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
// A lamp has three things to say and only one of them is about colour:
//
//	free            a dark dot, still
//	held            a dot in the holder's brand colour, breathing
//	held and busy   the same dot, steady
//
// Motion is the second channel on purpose. A card can be claimed by an agent that
// is between jobs, and it can be claimed by one that is mid-run; those are
// different facts about the machine, and the board already draws utilisation for
// the second. Breathing says "claimed, nothing running" and stopping says "running
// now", so the reader can tell a stale claim from live work without reading the
// bar. It also keeps hue doing exactly one job — who holds it — which is the same
// division the ramps make between hue and lightness.
//
// The lamp's colour is the holder's, not a single "agent" colour: two agents can
// hold cards on one host, and a brand colour is the one thing that tells them
// apart at a glance.

// lampBreath is one full breath: dark to bright and back.
//
// Slow enough to read as a breath rather than a blink. A blink at this size reads
// as an alarm, and a blink that means "someone has a claim" trains the reader to
// ignore the one thing on the board that should mean something is wrong.
const lampBreath = 2400 * time.Millisecond

// lampGlyph is the lit lamp.
//
// A filled circle is the one shape that reads as a lamp at a single cell: it is
// solid at every size, which the crescent and ring glyphs are not, and it does not
// collide with the geometry the board already uses for data (■ for a meter's
// cell, braille for a graph).
const (
	lampGlyph   = "●"
	lampGlyphID = "·"
)

// lampState is what a card's lamp has to say this frame.
type lampState struct {
	// lit is false for a free card and for a card the ledger has no answer for.
	lit bool
	// colour is the holder's brand colour when lit.
	colour theme.RGB
	// busy is true when the card is being used, which stops the breath.
	busy bool
	// overdue marks a claim past its declared time; it keeps the holder's colour
	// but is drawn at full brightness so it cannot be missed while dim.
	overdue bool
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
	}
}

// wireGPU is the part of a GPU reading the lamp needs; it keeps lampFor free of
// the wire package so the decision can be tested without building a sample.
type wireGPU struct{ util float64 }

// lampCell renders a lamp's glyph and colour at a moment.
//
// The brightness is a function of the frame's timestamp, never of the wall clock:
// the same frame drawn twice is the same picture, which is what lets -once and the
// plain-text snapshot be compared against a golden file.
func lampCell(st lampState, now time.Time) (glyph string, fg theme.RGB) {
	if !st.lit {
		// A free card's lamp is drawn, not left blank. Blank is also what a lane
		// with no room for a lamp looks like, and the two must not read alike.
		return lampGlyphID, theme.Default.Track()
	}
	if st.overdue {
		// Readable at every point of the breath: an overdue claim is the one state
		// where the reader must not have to wait for the cycle to peak.
		return lampGlyph, theme.Default.C("warn")
	}
	if st.busy {
		return lampGlyph, st.colour
	}
	return lampGlyph, breathe(st.colour, now)
}

// breathe dims and restores a colour on the lamp's period.
//
// It mixes toward the hidden end of the same colour rather than toward the page:
// a lamp that faded to black on a dark card would vanish, and a lamp that faded to
// white would change hue as it rose. Mixing toward the track keeps the hue exactly
// where the brand put it for the whole cycle, so "who holds this" never flickers.
//
// The floor is 0.40 rather than 0: at 0.30 the dark end sat ΔE 11.5 from the track,
// close enough that on a dim panel the claim seemed to go away and come back. At
// 0.40 both brands' dark ends clear the track by ΔE 16 and the breath reads as a
// change of brightness on a lamp that is always on.
func breathe(c theme.RGB, now time.Time) theme.RGB {
	// A cosine rather than a triangle: a linear ramp visibly snaps at both ends at
	// this size, and the lamp is a single cell, so the shape of the curve is most
	// of what makes it read as a breath.
	t := float64(now.UnixNano()%int64(lampBreath)) / float64(lampBreath)
	f := 0.5 - 0.5*math.Cos(2*math.Pi*t) // 0 at the dark end, 1 at the bright one
	f = 0.40 + 0.60*f                    // never fully out: the claim is always visible
	return mixRGB(theme.Default.Track(), c, f)
}

// mixRGB blends two colours; t=0 is a, t=1 is b.
func mixRGB(a, b theme.RGB, t float64) theme.RGB {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	blend := func(x, y int) int {
		return int(float64(x) + float64(y-x)*t + 0.5)
	}
	return theme.RGB{R: blend(a.R, b.R), G: blend(a.G, b.G), B: blend(a.B, b.B)}
}

// agentColourFor maps a claim's owner string ("kind/session") to its brand colour.
func agentColourFor(owner string) theme.RGB {
	for i := 0; i < len(owner); i++ {
		if owner[i] == '/' {
			return theme.Default.AgentColour(owner[:i])
		}
	}
	return theme.Default.AgentColour(owner)
}

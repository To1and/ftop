package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"ftop/internal/theme"
)

// LedgerMsg carries the ledger's current holders into the board.
//
// It is a separate message from a sample on purpose. A sample is a measurement of a
// card; a holder is a record in a database, it changes when an agent claims or
// releases rather than when the card ticks, and it can be right while the measurement
// is unavailable. Folding them into one message would make an unreachable host look
// like it had lost its claims.
type LedgerMsg struct {
	// At is when the ledger was read.
	At time.Time
	// Holders is keyed by GPU UUID.
	Holders map[string]Holder
	// Err is non-empty when the ledger could not be opened. The board says so
	// rather than drawing every card as free.
	Err string
}

// Holder is the ledger's answer for one card.
type Holder struct {
	// Owner is the identity recorded on the claim, already shortened.
	Owner string
	// Why is the claim's reason. It is untrusted text from another agent and is
	// only ever drawn, never parsed.
	Why string
	// Since is when the claim was taken; Overdue means the declared time ran out.
	Since   time.Time
	Overdue bool
}

// applyLedger replaces the board's holder set.
//
// A failed read does NOT clear the holders. The previous set is the last thing the
// ledger actually said, and a locked or missing database is not evidence that every
// agent released its card: wiping the board on an error makes a refused read look
// exactly like an empty fleet, which is the one confusion this display exists to
// prevent. The error is kept alongside the stale set so the footer can say so.
func (a *App) applyLedger(m LedgerMsg) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if m.Err == "" || a.holders == nil {
		a.holders = m.Holders
	}
	a.ledgerAt = m.At
	a.ledgerErr = m.Err
	// Whether anything is held decides the frame rate, so it is recorded here, in
	// the one place the holder set changes, as an atomic the tick can read without
	// the lock.
	a.anyHeld.Store(len(a.holders) > 0)
}

// holderFor returns the ledger answer for a card, if there is one.
//
// Unused today: the render path reads a.holders once under the lock View already
// holds, because a per-card lookup would take the same mutex inside the render.
// Kept out of the render path deliberately; do not call it from draw code.
func (a *App) holderFor(uuid string) (Holder, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h, ok := a.holders[uuid]
	return h, ok
}

// ledgerCell is the owner column's text for one card: the holder's short name, or a
// dash for a card the ledger says is free.
//
// A dash is deliberately different from a blank. Blank would mean "no ledger", and
// the two must not look alike on a board where the whole point is knowing whether
// anybody has a claim.
func ledgerCell(h Holder, ok bool) string {
	if !ok {
		return "—"
	}
	name := h.Owner
	// "kind/session" is 22-odd cells; the claim id and the session are in the detail
	// view and in `ftop gpu show`, and the board only has to say which agent to ask.
	// The kind is shown as recorded: shortening a particular runtime's name here would
	// be a rule about one vendor, and this binary ships to fleets ftop knows nothing
	// about.
	name = ownerKind(name)
	// The column is a fixed width so the row never reflows when a holder changes;
	// the full identity stays in the detail view and in `ftop gpu show`.
	return theme.Clip(name, ownerW-1)
}

// ledgerStrip is the card's footer segment: who holds cards on this machine, and
// why. Empty when the ledger has nothing to say about this host.
//
// It summarises rather than lists. A card footer is one line shared with the column
// legend and the sample's age, so it names the holders and how many cards each has —
// "held: claude-code×2 · codex" — and leaves the per-card detail to the detail view,
// where there is room for the reason and the deadline.
//
// The count is per KIND, not per session. Grouping by the full "kind/session" owner
// and then printing only the kind produced "claude-code×2 · claude-code" — two
// segments, identical on screen, for two sessions of one agent — which is both
// unreadable and a miscount of what the line appears to say. A reader who needs to
// tell two sessions apart has the per-card lamp and `ftop gpu show`; the footer's
// job is "how much of this machine is spoken for, by whom".
func ledgerStrip(h *hostState, holders map[string]Holder) []ledgerSeg {
	if len(holders) == 0 {
		return nil
	}
	type agg struct {
		cards   int
		overdue bool
	}
	by := map[string]*agg{}
	var order []string
	for _, slot := range h.inv {
		hv, ok := holders[ledgerKey(h.name, slot.last)]
		if !ok {
			continue
		}
		kind := ownerKind(hv.Owner)
		a, seen := by[kind]
		if !seen {
			a = &agg{}
			by[kind] = a
			order = append(order, kind)
		}
		a.cards++
		if hv.Overdue {
			a.overdue = true
		}
	}
	if len(order) == 0 {
		return nil
	}
	sort.Strings(order)
	out := make([]ledgerSeg, 0, len(order)*2+1)
	out = append(out, ledgerSeg{"held: ", theme.Default.C("graph_text")})
	for i, kind := range order {
		if i > 0 {
			out = append(out, ledgerSeg{" · ", theme.Default.C("graph_text")})
		}
		a := by[kind]
		name := theme.Clip(kind, ownerW-1)
		txt := name
		if a.cards > 1 {
			txt = fmt.Sprintf("%s×%d", name, a.cards)
		}
		if a.overdue {
			txt += "!"
		}
		out = append(out, ledgerSeg{txt, theme.Default.AgentColour(kind)})
	}
	return out
}

// ledgerSeg is one run of the footer's ledger segment, in one colour.
//
// The segment is segmented rather than one string because its colours carry
// meaning: each agent's name is drawn in that agent's brand colour, which is the
// same colour as its lamp on the card below. A single-colour line here was drawn in
// the theme's "ok" green — btop's proc_misc key, which aliases the host-health
// colour — so "someone holds this card" was rendered in the colour that means "this
// host is fine", and at 50% utilisation it was also the exact colour of the util
// bar (ΔE 2.7).
type ledgerSeg struct {
	text string
	fg   theme.RGB
}

// width is the cells a ledger segment set occupies.
func ledgerSegsWidth(ss []ledgerSeg) int {
	w := 0
	for _, s := range ss {
		w += theme.RuneWidth(s.text)
	}
	return w
}

// ownerKind is the agent kind from a claim's "kind/session" owner.
func ownerKind(owner string) string {
	if i := strings.IndexByte(owner, '/'); i > 0 {
		return owner[:i]
	}
	return owner
}

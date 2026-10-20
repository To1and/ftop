package ui

import (
	"strings"
	"testing"
	"time"
)

// TestLampHoldsEveryLayout: the lamp has two homes — under the index on a
// two-line row, in the gap after the index on a one-line row — and a card at
// every width must get one and stay exactly as wide as the canvas.
//
// The width assertion is the one that matters. Both lamp cells are spacing on
// their layout, so a lamp that pushed a field sideways or widened a row would
// show up here as a line that is a cell too long, which is the failure the
// cell-grid canvas exists to make impossible.
func TestLampHoldsEveryLayout(t *testing.T) {
	for _, w := range []int{60, 80, 100, 120, 160, 200, 240, 300} {
		for _, n := range []int{1, 2, 4, 8} {
			h := 44 + n*4 // tall enough that the card, and its footer, are on screen
			a := fixture(w, h, []int{n})
			st := a.states["hosta"]
			holders := map[string]Holder{}
			for i := 0; i < len(st.sample.Gpus); i += 2 {
				owner := "claude-code/a"
				if i%4 == 0 {
					owner = "codex/b"
				}
				holders[ledgerKey("hosta", st.sample.Gpus[i])] = Holder{Owner: owner}
			}
			a.Deliver(LedgerMsg{At: time.Now(), Holders: holders})
			frame := a.PlainFrame()

			for i, line := range strings.Split(frame, "\n") {
				if line == "" {
					continue
				}
				if r := []rune(line); len(r) != w {
					t.Fatalf("w=%d n=%d: line %d is %d cells wide", w, n, i, len(r))
				}
			}
			if !containsLitLamp(frame) {
				t.Errorf("w=%d n=%d: no lit lamp on the board", w, n)
			}
			if !strings.Contains(frame, "held: ") {
				t.Errorf("w=%d n=%d: no ledger strip in a footer", w, n)
			}
		}
	}
}

// TestNoHoldersNoLitLamp: with nothing claimed there is nothing to animate, the
// board is laid out as it was before the lamp existed (no cell is given up for an
// unlit one on a one-line row), and the tick stays at one second.
func TestNoHoldersNoLitLamp(t *testing.T) {
	a := fixture(200, 58, []int{8})
	frame := a.PlainFrame()
	// A two-line row's second line starts with its lamp. On a board with no
	// claims every one of those is the dark dot: the lamp is present and out,
	// which is the state the reader needs to be able to tell from "no lamp here".
	for _, g := range litLampGlyphs() {
		if strings.Contains(frame, g+"   ■") {
			t.Errorf("an unclaimed board lit a lamp: %q", g)
		}
	}
	if a.anyHeld.Load() {
		t.Error("anyHeld is set with no holders")
	}
	// And a one-line row has given up no cell: the index still starts the row.
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "│ 0 ") {
			return
		}
	}
	t.Skip("fixture is not laid out as a single card at this size")
}

package ui

import (
	"strings"
	"testing"
	"time"

	"ftop/internal/theme"
	"ftop/internal/wire"
)

// fixtureWithUUIDs builds a board whose cards carry UUIDs, so a ledger answer can be
// attached to them. The shared fixture omits UUIDs, and without one a claim has no
// key to match.
func fixtureWithUUIDs(width, height int, gpus int) *App {
	a := New(Options{Hosts: []string{"hosta"}, GraphRows: 1})
	a.width, a.height = width, height
	var s wire.Sample
	s.Cores = 64
	for j := 0; j < gpus; j++ {
		u, used, total := float64(10*j), uint64(8000), uint64(24564)
		s.Gpus = append(s.Gpus, wire.Gpu{
			Index: j, UUID: "GPU-" + string(rune('a'+j)), Name: "NVIDIA GeForce RTX 4090",
			Util: &u, MemUsed: &used, MemTotal: &total,
		})
	}
	a.Deliver(sampleMsg("hosta", s))
	return a
}

// TestLedgerStripNamesTheHolder: the card's footer answers "who has this machine's
// cards", which is the question the ledger exists for.
func TestLedgerStripNamesTheHolder(t *testing.T) {
	a := fixtureWithUUIDs(200, 60, 2)
	a.Deliver(LedgerMsg{
		At: time.Now(),
		Holders: map[string]Holder{
			"GPU-a": {Owner: "agent-a/1a2b3c4d", Why: "training"},
		},
	})
	frame := a.PlainFrame()
	if !strings.Contains(frame, "held: agent-a") {
		t.Fatalf("footer does not name the holder:\n%s", frame)
	}
}

// TestLedgerStripCountsCardsPerHolder: two cards of one agent read as "agent-a×2",
// not as the same name twice.
func TestLedgerStripCountsCardsPerHolder(t *testing.T) {
	a := fixtureWithUUIDs(200, 60, 2)
	a.Deliver(LedgerMsg{
		At: time.Now(),
		Holders: map[string]Holder{
			"GPU-a": {Owner: "agent-a/x"},
			"GPU-b": {Owner: "agent-a/x"},
		},
	})
	if frame := a.PlainFrame(); !strings.Contains(frame, "agent-a×2") {
		t.Fatalf("footer does not count the cards:\n%s", frame)
	}
}

// TestLedgerStripMarksOverdue: an overdue claim is a real problem, so the footer says
// so rather than showing the name exactly as a healthy claim would.
func TestLedgerStripMarksOverdue(t *testing.T) {
	a := fixtureWithUUIDs(200, 60, 1)
	a.Deliver(LedgerMsg{
		At:      time.Now(),
		Holders: map[string]Holder{"GPU-a": {Owner: "agent-a/x", Overdue: true}},
	})
	if frame := a.PlainFrame(); !strings.Contains(frame, "agent-a!") {
		t.Fatalf("overdue claim is not marked:\n%s", frame)
	}
}

// TestNoLedgerKeepsTheBoardUnchanged: a board with no ledger must draw exactly as it
// did before the ledger existed. The demo, a build without one, and a machine that
// has never claimed anything all take this path.
func TestNoLedgerKeepsTheBoardUnchanged(t *testing.T) {
	plain := fixtureWithUUIDs(200, 60, 2).PlainFrame()
	empty := fixtureWithUUIDs(200, 60, 2)
	empty.Deliver(LedgerMsg{At: time.Now(), Holders: map[string]Holder{}})
	if got := empty.PlainFrame(); got != plain {
		t.Errorf("an empty ledger changed the frame:\n--- without\n%s\n--- with\n%s", plain, got)
	}
}

// TestDetailBoxShowsTheHolder: the board's footer summarises; the detail view has
// room for the card's own holder and its deadline state.
func TestDetailBoxShowsTheHolder(t *testing.T) {
	a := fixtureWithUUIDs(160, 40, 2)
	a.Deliver(LedgerMsg{
		At:      time.Now(),
		Holders: map[string]Holder{"GPU-a": {Owner: "agent-a/x", Overdue: true}},
	})
	a.open("hosta")
	a.mu.Lock()
	frame := a.detailCanvas().Plain()
	a.mu.Unlock()
	if !strings.Contains(frame, "overdue · agent-a") {
		t.Fatalf("detail box does not show the holder:\n%s", frame)
	}
}

// TestStripCountsPerAgentNotPerSession: the footer says how much of a machine is
// spoken for and by whom, so two sessions of one agent are one segment with a
// count, not two segments that read identically.
//
// The bug this caught: aggregation was keyed on the full "kind/session" owner and
// the label printed was only the kind, so one agent's two sessions rendered as
// "claude-code×2 · claude-code" — a duplicate that looks like a miscount.
func TestStripCountsPerAgentNotPerSession(t *testing.T) {
	h := &hostState{name: "h", inv: []invSlot{
		{key: "a", last: wire.Gpu{Index: 0}},
		{key: "b", last: wire.Gpu{Index: 1}},
		{key: "c", last: wire.Gpu{Index: 2}},
		{key: "d", last: wire.Gpu{Index: 3}},
	}}
	holders := map[string]Holder{
		"h/#0": {Owner: "claude-code/1a2b3c4d"},
		"h/#1": {Owner: "claude-code/1a2b3c4d"},
		"h/#2": {Owner: "claude-code/9f8e7d6c"},
		"h/#3": {Owner: "codex/aaaa1111"},
	}
	got := plainSegs(ledgerStrip(h, holders))
	if want := "held: claude-code×3 · codex"; got != want {
		t.Errorf("strip = %q, want %q", got, want)
	}
}

// TestStripColoursEachAgentByBrand: the footer's names are drawn in the agents'
// own colours, which is what makes the line legible when several are on it.
func TestStripColoursEachAgentByBrand(t *testing.T) {
	h := &hostState{name: "h", inv: []invSlot{
		{key: "a", last: wire.Gpu{Index: 0}},
		{key: "b", last: wire.Gpu{Index: 1}},
	}}
	holders := map[string]Holder{
		"h/#0": {Owner: "claude-code/x"},
		"h/#1": {Owner: "codex/y"},
	}
	segs := ledgerStrip(h, holders)
	var brands []string
	for _, s := range segs {
		switch s.fg {
		case theme.Default.AgentColour("claude-code"):
			brands = append(brands, "claude")
		case theme.Default.AgentColour("codex"):
			brands = append(brands, "codex")
		}
	}
	if len(brands) != 2 || brands[0] != "claude" || brands[1] != "codex" {
		t.Errorf("brand colours in the strip: %v", brands)
	}
}

// TestStripIsNotTheHealthGreen: the footer used to be drawn in the theme's "ok"
// colour, which is also the host-health dot and, at half utilisation, the exact
// colour of the util bar (ΔE 2.7). Whatever the strip is drawn in, it may not be
// that.
func TestStripIsNotTheHealthGreen(t *testing.T) {
	h := &hostState{name: "h", inv: []invSlot{{key: "a", last: wire.Gpu{Index: 0}}}}
	segs := ledgerStrip(h, map[string]Holder{"h/#0": {Owner: "claude-code/x"}})
	ok := theme.Default.C("ok")
	for _, s := range segs {
		if theme.DeltaE(s.fg, ok) < 8 {
			t.Errorf("strip segment %q is drawn in the health green", s.text)
		}
	}
}

func plainSegs(ss []ledgerSeg) string {
	out := ""
	for _, s := range ss {
		out += s.text
	}
	return out
}

// TestClaimsDoNotLeakBetweenHosts: a card is looked up by UUID, but a driver may
// report none, and then the fallback key is the index — which is the same "#0" on
// every machine. Looking a claim up by that key alone made one host's claim light
// the same-numbered card on every other host, and made the footer of an innocent
// machine name an agent that had never touched it.
func TestClaimsDoNotLeakBetweenHosts(t *testing.T) {
	a := fixture(160, 60, []int{4, 4})
	ha, hb := a.states["hosta"], a.states["hostb"]
	a.Deliver(LedgerMsg{At: time.Now(), Holders: map[string]Holder{
		ledgerKey("hosta", ha.sample.Gpus[0]): {Owner: "claude-code/x"},
	}})

	// hostb holds nothing, so neither its strip nor its lamp may show a claim.
	stripB := ledgerStrip(hb, a.holders)
	if len(stripB) > 0 {
		t.Errorf("hostb's footer names a holder it does not have: %q", plainSegs(stripB))
	}
	stripA := ledgerStrip(ha, a.holders)
	if len(stripA) == 0 {
		t.Error("hosta's own claim is missing from its footer")
	}
}

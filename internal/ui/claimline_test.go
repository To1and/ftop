package ui

import (
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"ftop/internal/theme"
	"ftop/internal/wire"
)

// claimRow finds device box idx in a host view and returns the canvas, the row of
// its claim line, and that row's text. Callers use a single-column width, so the
// row is that box's alone.
func claimRow(t *testing.T, a *App, idx int) (*Canvas, int, string) {
	t.Helper()
	a.open("hosta")
	c := a.detailCanvas()
	title := theme.ShortGPU("NVIDIA GeForce RTX 4090")
	for y := 0; y < c.H; y++ {
		if strings.Contains(rowText(c, y), "┤"+string(rune('0'+idx))+" · "+title+"├") {
			return c, y + 1, rowText(c, y+1)
		}
	}
	t.Fatalf("no box for device %d", idx)
	return nil, 0, ""
}

// cellX is the column of the first cell of sub in a row of single-width cells.
func cellX(row, sub string) int {
	i := strings.Index(row, sub)
	if i < 0 {
		return -1
	}
	return utf8.RuneCountInString(row[:i])
}

// TestClaimLineNamesTheThread: a held card's first line is the holding thread's
// id — its first eight characters, not the runtime's name — in that runtime's
// brand colour.
func TestClaimLineNamesTheThread(t *testing.T) {
	a := fixtureWithUUIDs(160, 50, 2)
	a.Deliver(LedgerMsg{At: time.Now(), Holders: map[string]Holder{
		"GPU-a": {Owner: "claude-code/0123456789abcdef", Why: "sweep", Since: time.Now()},
		"GPU-b": {Owner: "codex/fedcba9876543210", Why: "eval", Since: time.Now()},
	}})
	for idx, want := range map[int]struct{ id, cut, kind string }{
		0: {"01234567", "89ab", "claude-code"},
		1: {"fedcba98", "7654", "codex"},
	} {
		c, y, row := claimRow(t, a, idx)
		x := cellX(row, want.id)
		if x < 0 || strings.Contains(row, want.cut) {
			t.Fatalf("device %d: the claim line does not show the thread's first eight characters: %q", idx, row)
		}
		if strings.Contains(row, want.kind) {
			t.Errorf("device %d: the claim line names the runtime: %q", idx, row)
		}
		if got, wantFG := c.at(x, y).FG, theme.Default.AgentColour(want.kind); got != wantFG {
			t.Errorf("device %d: the thread is drawn in %s, want %s's %s", idx, got.Hex(), want.kind, wantFG.Hex())
		}
	}
}

// TestClaimLineSaysFreeOrUnclaimed: a card nobody holds says which of the two it
// is — free when nothing runs on it, unclaimed, in the warning colour, when a job
// the ledger knows nothing about does.
func TestClaimLineSaysFreeOrUnclaimed(t *testing.T) {
	a := fixtureWithUUIDs(160, 50, 2)
	a.mu.Lock()
	a.states["hosta"].sample.Gpus[1].Procs = []wire.Proc{{PID: 9, User: "z", Name: "job", MemMB: 100}}
	a.mu.Unlock()
	a.Deliver(LedgerMsg{At: time.Now(), Holders: map[string]Holder{}})

	if _, _, row := claimRow(t, a, 0); !strings.Contains(row, "free") || strings.Contains(row, "unclaimed") {
		t.Errorf("an idle card nobody holds is not free: %q", row)
	}
	c, y, row := claimRow(t, a, 1)
	x := cellX(row, "unclaimed")
	if x < 0 {
		t.Fatalf("a running card nobody holds is not unclaimed: %q", row)
	}
	if got := c.at(x, y).FG; got != a.skin.Theme.C("warn") {
		t.Errorf("unclaimed is drawn in %s, want the warning colour", got.Hex())
	}
}

// TestDeviceBoxesAreOneHeight: every box keeps two process rows whatever is on
// its card, so an idle card beside a busy one lines up with it and a job starting
// moves nothing; a third process is counted, not listed.
func TestDeviceBoxesAreOneHeight(t *testing.T) {
	a := fixtureWithUUIDs(200, 60, 2)
	a.mu.Lock()
	g := &a.states["hosta"].sample.Gpus[1]
	for i := 0; i < 5; i++ {
		g.Procs = append(g.Procs, wire.Proc{PID: 100 + i, User: "z", Name: "job", MemMB: uint64(500 - i)})
	}
	a.mu.Unlock()
	a.open("hosta")
	gr := grid(a.detailCanvas().Plain())
	var heights []int
	for y := range gr {
		for x := 0; x+1 < len(gr[y]); x++ {
			// A device box's title is its index: ╭─┤0 · RTX 4090├. The host's own box
			// above them starts with its state glyph instead.
			if gr[y][x] != '╭' || at(gr, x+2, y) != '┤' || !unicode.IsDigit(at(gr, x+3, y)) {
				continue
			}
			h := 1
			for at(gr, x, y+h) == '│' {
				h++
			}
			if at(gr, x, y+h) == '╰' {
				heights = append(heights, h+1)
			}
		}
	}
	if len(heights) != 2 || heights[0] != heights[1] || heights[0] != detailBoxHeight {
		t.Errorf("device boxes are %v rows tall, want both %d", heights, detailBoxHeight)
	}
	if frame := a.detailCanvas().Plain(); !strings.Contains(frame, "+3 more") {
		t.Errorf("the processes past two are not counted:\n%s", frame)
	}
}

// TestThreadIDIsAPrefix: the id is the thread part of the owner, cut to eight
// cells with no ellipsis, the way a short commit hash is; an owner with no thread
// is shown as recorded.
func TestThreadIDIsAPrefix(t *testing.T) {
	for owner, want := range map[string]string{
		"claude-code/bf41a34f-0ea4-4215": "bf41a34f",
		"codex/7b2d":                     "7b2d",
		"human":                          "human",
		"codex/":                         "codex/",
	} {
		if got := threadID(owner); got != want {
			t.Errorf("threadID(%q) = %q, want %q", owner, got, want)
		}
	}
}

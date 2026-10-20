package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"ftop/internal/sshx"
	"ftop/internal/wire"
)

// filterFleet is two GPU hosts, two CPU-only hosts, one laptop the probe
// cannot run on, and one GPU host that is down.
func filterFleet(width int, opts Options) *App {
	opts.Hosts = []string{"lab-gpu-1", "lab-gpu-2", "lab-cpu-1", "lab-cpu-2", "laptop", "lab-gpu-3"}
	opts.GraphRows = 1
	a := New(opts)
	a.width, a.height = width, 50
	gpus := func(n int) wire.Sample {
		s := wire.Sample{}
		s.Cores = 32
		s.TotalKB = 64 << 20
		for j := 0; j < n; j++ {
			u, used, total := 20.0, uint64(4000), uint64(24564)
			s.Gpus = append(s.Gpus, wire.Gpu{Index: j, Name: "NVIDIA GeForce RTX 4090", Util: &u, MemUsed: &used, MemTotal: &total})
		}
		return s
	}
	a.Deliver(sshxSample("lab-gpu-1", gpus(4)))
	a.Deliver(sshxSample("lab-gpu-2", gpus(2)))
	a.Deliver(sshxSample("lab-cpu-1", gpus(0)))
	a.Deliver(sshxSample("lab-cpu-2", gpus(0)))
	a.Deliver(sshx.ErrMsg{Host: "laptop", Err: "unsupported platform Darwin arm64: the probe runs on Linux x86_64", Unsupported: true})
	a.Deliver(sshx.ErrMsg{Host: "lab-gpu-3", Err: "connection timed out"})
	return a
}

func shown(a *App) string { return strings.Join(a.sortedNames(), ",") }

// TestBoardHidesHostsWithNothingToShow: a machine with no NVIDIA GPU and one the
// probe cannot run on are off the board by default — the skyline, the cards and
// the header's counts alike — and the status line says how many are hidden. A
// GPU host that is down is still shown: that is news.
func TestBoardHidesHostsWithNothingToShow(t *testing.T) {
	a := filterFleet(200, Options{})
	if got := shown(a); got != "lab-gpu-1,lab-gpu-2,lab-gpu-3" {
		t.Errorf("board shows %q", got)
	}
	frame := a.PlainFrame()
	for _, h := range []string{"lab-cpu-1", "lab-cpu-2", "laptop"} {
		if strings.Contains(frame, h) {
			t.Errorf("%s is on the board:\n%s", h, frame)
		}
	}
	lines := strings.Split(frame, "\n")
	if !strings.Contains(lines[1], "2/3 hosts") {
		t.Errorf("the header counts hidden hosts: %q", lines[1])
	}
	if !strings.Contains(lines[0], "1 down") {
		t.Errorf("the header lost the GPU host that is down: %q", lines[0])
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "3 hidden") {
		t.Errorf("the status line does not say what is hidden: %q", last)
	}
}

// TestBoardFiltersFollowTheHost: a host is hidden for what it was last seen to
// be, so a CPU-only machine that drops off stays off the board, a host that has
// not answered yet is shown, and a host that turns out to have GPUs comes back.
func TestBoardFiltersFollowTheHost(t *testing.T) {
	a := New(Options{Hosts: []string{"lab-a", "lab-b"}, GraphRows: 1})
	a.width, a.height = 200, 50
	if got := shown(a); got != "lab-a,lab-b" {
		t.Fatalf("hosts that have not answered are hidden: %q", got)
	}
	a.Deliver(sshxSample("lab-a", wire.Sample{}))
	if got := shown(a); got != "lab-b" {
		t.Errorf("a CPU-only host is shown: %q", got)
	}
	a.Deliver(sshxErr("lab-a", "connection timed out"))
	if got := shown(a); got != "lab-b" {
		t.Errorf("a CPU-only host came back as a failed one: %q", got)
	}
	u, used, total := 10.0, uint64(1), uint64(2)
	a.Deliver(sshxSample("lab-a", wire.Sample{Gpus: []wire.Gpu{{Index: 0, Util: &u, MemUsed: &used, MemTotal: &total}}}))
	if got := shown(a); got != "lab-a,lab-b" {
		t.Errorf("a host that now reports a GPU is hidden: %q", got)
	}
}

// TestSettingsShowHiddenHosts: o opens the settings over the board, a row flips
// with space, a click flips the row it lands on, and esc closes it. The two
// filters are independent, and config can start either one shown.
func TestSettingsShowHiddenHosts(t *testing.T) {
	a := filterFleet(200, Options{})
	a.press("o")
	if !a.settings {
		t.Fatal("o did not open the settings")
	}
	panel := a.settingsCanvas().Plain()
	for _, want := range []string{"ftop settings", "hosts without a GPU", "hidden", "2 hosts", "unsupported platforms", "1 host", "util history"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the panel does not say %q:\n%s", want, panel)
		}
	}
	a.press("space")
	if got := shown(a); !strings.Contains(got, "lab-cpu-1") || strings.Contains(got, "laptop") {
		t.Errorf("showing CPU-only hosts showed %q", got)
	}
	// Keys go to the panel while it is open, not to the board beneath it.
	focus := a.focused()
	a.press("down")
	if a.focused() != focus || a.setRow != 1 {
		t.Errorf("down moved the board (focus %s → %s) or not the panel (row %d)", focus, a.focused(), a.setRow)
	}
	// A click on the second row flips it.
	a.settingsCanvas()
	var row *hit
	for i := range a.hits {
		if j, ok := settingClicked(a.hits[i].key); ok && j == 1 {
			row = &a.hits[i]
		}
	}
	if row == nil {
		t.Fatal("the unsupported row is not clickable")
	}
	a.Update(tea.MouseClickMsg{X: row.x + 2, Y: row.y, Button: tea.MouseLeft})
	if !strings.Contains(shown(a), "laptop") {
		t.Errorf("clicking the row did not show the laptop: %q", shown(a))
	}
	if a.hiddenCount() != 0 {
		t.Errorf("%d hosts still hidden", a.hiddenCount())
	}
	a.press("esc")
	if a.settings {
		t.Error("esc did not close the settings")
	}
	if a.press("o"); !a.settings {
		t.Fatal("o did not reopen the settings")
	}
	a.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if a.settings {
		t.Error("a click outside the panel did not close it")
	}

	b := filterFleet(200, Options{ShowUnsupported: true})
	if got := shown(b); !strings.Contains(got, "laptop") || strings.Contains(got, "lab-cpu") {
		t.Errorf("ShowUnsupported alone shows %q", got)
	}
}

// TestHiddenFocusMovesOn: hiding the host under the highlight moves the
// highlight to a host that is drawn, rather than leaving it on nothing.
func TestHiddenFocusMovesOn(t *testing.T) {
	a := filterFleet(200, Options{ShowCPUOnly: true})
	a.focusName = "lab-cpu-1"
	a.press("o")
	a.press("space")
	if f := a.focused(); f == "lab-cpu-1" || f == "" {
		t.Errorf("focus is on %q after hiding it", f)
	}
}

// TestAllHiddenSaysSo: a fleet whose every host is filtered out says what is
// hidden and how to show it, instead of drawing an empty board.
func TestAllHiddenSaysSo(t *testing.T) {
	a := New(Options{Hosts: []string{"lab-cpu-1", "lab-cpu-2"}, GraphRows: 1})
	a.width, a.height = 160, 40
	a.Deliver(sshxSample("lab-cpu-1", wire.Sample{}))
	a.Deliver(sshxSample("lab-cpu-2", wire.Sample{}))
	if frame := a.PlainFrame(); !strings.Contains(frame, "2 hosts hidden") || !strings.Contains(frame, "o to show") {
		t.Errorf("an all-hidden board does not say so:\n%s", frame)
	}
}

// TestSettingsFitEveryTerminal: the panel never runs past the terminal, and its
// rows stay inside its box, from the smallest supported size up.
func TestSettingsFitEveryTerminal(t *testing.T) {
	for _, h := range []int{minHeight, 16, 24, 40} {
		for w := minWidth; w <= 240; w += 7 {
			a := filterFleet(w, Options{})
			a.height = h
			a.settings = true
			frame := a.settingsCanvas().Plain()
			lines := strings.Split(frame, "\n")
			if len(lines) > h {
				t.Errorf("%dx%d: %d lines", w, h, len(lines))
			}
			for i, l := range lines {
				if n := len([]rune(l)); n > w {
					t.Errorf("%dx%d: line %d is %d wide", w, h, i, n)
				}
			}
		}
	}
}

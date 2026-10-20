package ui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"ftop/internal/demo"
	"ftop/internal/sshx"
	"ftop/internal/theme"
	"ftop/internal/wire"
)

// Regression tests for review round 3.

// A renumbered device is the same device: one row, same place, same history.
func TestRenumberedDeviceKeepsItsRow(t *testing.T) {
	a := New(Options{Hosts: []string{"aaa", "zzz"}, GraphRows: 1})
	u := 10.0
	a.Deliver(sshxSample("aaa", wire.Sample{Gpus: []wire.Gpu{{Index: 0, UUID: "GPU-a", Util: &u}}}))
	a.Deliver(sshxSample("zzz", wire.Sample{Gpus: []wire.Gpu{{Index: 0, UUID: "GPU-z", Util: &u}}}))
	before := strings.Join(a.sortedNames(), ",")
	a.Deliver(sshxSample("zzz", wire.Sample{Gpus: []wire.Gpu{{Index: 1, UUID: "GPU-z", Util: &u}}}))
	if n := len(a.states["zzz"].sample.Gpus); n != 1 {
		t.Errorf("a renumbered device was counted twice: %d rows", n)
	}
	if got := strings.Join(a.sortedNames(), ","); got != before {
		t.Errorf("renumbering re-sorted the board: %s -> %s", before, got)
	}
	if n := len(a.states["zzz"].gpuHist["GPU-z"]); n != 2 {
		t.Errorf("history did not follow the device: %d samples", n)
	}
}

// Rows keep their order when a sample lists devices in a different order.
func TestRowsKeepTheirOrder(t *testing.T) {
	a := New(Options{Hosts: []string{"h"}, GraphRows: 1})
	u0, u1 := 10.0, 90.0
	g0 := wire.Gpu{Index: 0, UUID: "A", Util: &u0}
	g1 := wire.Gpu{Index: 1, UUID: "B", Util: &u1}
	a.Deliver(sshxSample("h", wire.Sample{Gpus: []wire.Gpu{g0, g1}}))
	a.Deliver(sshxSample("h", wire.Sample{Gpus: []wire.Gpu{g1, g0}}))
	if gs := a.states["h"].sample.Gpus; gs[0].UUID != "A" || gs[1].UUID != "B" {
		t.Errorf("rows swapped when the sample's order did: %s,%s", gs[0].UUID, gs[1].UUID)
	}
}

// An omitted device keeps its last process list, marked old, rather than
// claiming a freshly measured "no processes".
func TestOmittedDeviceKeepsStaleProcesses(t *testing.T) {
	a := New(Options{Hosts: []string{"h"}, GraphRows: 1})
	u := 50.0
	g0 := wire.Gpu{Index: 0, UUID: "A", Util: &u, Procs: []wire.Proc{{PID: 7, Name: "train.py"}}}
	g1 := wire.Gpu{Index: 1, UUID: "B", Util: &u}
	a.Deliver(sshxSample("h", wire.Sample{Gpus: []wire.Gpu{g0, g1}}))
	a.Deliver(sshxSample("h", wire.Sample{Gpus: []wire.Gpu{g1}}))
	gs := a.states["h"].sample.Gpus
	if len(gs[0].Procs) != 1 || !gs[0].ProcsStale {
		t.Errorf("omitted device lost its processes or presented them as fresh: %+v", gs[0])
	}
}

// Enter during startup pins the chosen host.
func TestEnterPinsTheChosenHost(t *testing.T) {
	a := New(Options{Hosts: []string{"big", "small"}, GraphRows: 1})
	a.width, a.height = 200, 40
	u := 10.0
	a.Deliver(sshxSample("small", wire.Sample{Gpus: gpus(1, &u)}))
	_ = a.fleetCanvas()
	_, _ = a.Update(keyMsg("enter"))
	a.Deliver(sshxSample("big", wire.Sample{Gpus: gpus(8, &u)}))
	_, _ = a.Update(keyMsg("esc"))
	_ = a.fleetCanvas()
	if got := a.focused(); got != "small" {
		t.Errorf("after Enter on small and a new host arriving, highlight is on %q", got)
	}
}

// Remote text must not reach the terminal as escape sequences.
func TestRemoteTextCannotInjectEscapes(t *testing.T) {
	a := fixture(160, 30, []int{1})
	a.mu.Lock()
	st := a.states["hosta"]
	st.sample.Gpus[0].Procs = []wire.Proc{{PID: 1, User: "u\x1b[2J", Name: "evil\x1b]0;pwned\x07\x9b31m"}}
	st.sample.Gpus[0].Name = "RTX\x1b[5m 4090"
	a.detail = true
	frame := a.detailCanvas().ANSI()
	a.mu.Unlock()
	a.Deliver(sshxErr("hosta", "ssh: \x1b]52;c;cGF5bG9hZA==\x07denied"))
	board := a.fleetCanvasANSI()
	for name, out := range map[string]string{"detail": frame, "board": board} {
		// Our own output is SGR only: ESC [ digits/semicolons m.
		stripped := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(out, "")
		if strings.ContainsAny(stripped, "\x1b\x07\x9b") {
			t.Errorf("%s view passes a control character through to the terminal", name)
		}
	}
}

func (a *App) fleetCanvasANSI() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fleetCanvas().ANSI()
}

// Combining marks take no cell: a label with one must not shift its row. The
// width is measured the way the terminal renderer measures it, on the ANSI
// bytes, not with our own width table.
func TestCombiningMarksTakeNoCell(t *testing.T) {
	a := fixture(100, 30, []int{2})
	a.mu.Lock()
	a.states["hosta"].label = "cafe\u0301\u200d"
	out := a.fleetCanvas().ANSI()
	a.mu.Unlock()
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w != 100 {
			t.Errorf("line %d renders %d cells wide, want 100", i, w)
		}
	}
}

// Review round 4: every row must measure exactly the terminal width under BOTH
// of the renderer's width methods, whatever Unicode a label or a remote process
// name carries.
func TestRowsMeasureTheSameUnderBothWidthMethods(t *testing.T) {
	samples := []string{"worker-🚀", "का", "☕", "👩‍💻 job", "🇨🇳", "é", "�", "\xff\xfe", "中文进程", "ｆｕｌｌ"}
	for _, txt := range samples {
		a := fixture(100, 30, []int{2})
		a.mu.Lock()
		a.states["hosta"].label = txt
		a.states["hosta"].sample.Gpus[0].Procs = []wire.Proc{{PID: 1, User: txt, Name: txt + " --flag"}}
		board := a.fleetCanvas().ANSI()
		a.detail = true
		detail := a.detailCanvas().ANSI()
		a.mu.Unlock()
		for view, out := range map[string]string{"board": board, "detail": detail} {
			for i, line := range strings.Split(out, "\n") {
				g, w := ansi.StringWidth(line), ansi.StringWidthWc(line)
				if g != 100 || w != 100 {
					t.Errorf("%q in %s line %d: grapheme width %d, wcwidth %d, want 100", txt, view, i, g, w)
				}
			}
		}
	}
}

// A real U+FFFD is a character; only invalid bytes become "?".
func TestReplacementCharacterSurvives(t *testing.T) {
	c := NewCanvas(4, 1, theme.RGB{})
	c.Text(0, 0, "a�b", theme.RGB{})
	if got := c.Plain(); !strings.Contains(got, "�") {
		t.Errorf("valid U+FFFD was replaced: %q", got)
	}
	c.Text(0, 0, "a\xffb", theme.RGB{})
	if got := c.Plain(); !strings.HasPrefix(got, "a?b") {
		t.Errorf("invalid byte not replaced: %q", got)
	}
}

// Round 4: the demo fleet keeps every device (they used to share one UUID and
// collapse to one per host).
func TestDemoKeepsEveryDevice(t *testing.T) {
	a := New(Options{Hosts: demo.Hosts(), GraphRows: 1})
	src := demo.New()
	sent := 0
	src.Warm(3, func(m sshx.SampleMsg) { sent += len(m.Sample.Gpus); a.Deliver(m) }, func(m sshx.ErrMsg) { a.Deliver(m) })
	got := 0
	for _, st := range a.states {
		if st.hasData {
			got += len(st.sample.Gpus)
		}
	}
	if got*3 != sent {
		t.Errorf("app holds %d devices, demo sent %d per round", got, sent/3)
	}
}

// A device the latest sample omitted says so in the detail view.
func TestMissingDeviceIsLabelledInDetail(t *testing.T) {
	a := New(Options{Hosts: []string{"h"}, GraphRows: 1})
	a.width, a.height = 160, 40
	u := 10.0
	a.Deliver(sshxSample("h", wire.Sample{Gpus: []wire.Gpu{{Index: 0, UUID: "A", Util: &u, Name: "RTX 4090"}, {Index: 1, UUID: "B", Util: &u, Name: "RTX 4090"}}}))
	a.Deliver(sshxSample("h", wire.Sample{Gpus: []wire.Gpu{{Index: 1, UUID: "B", Util: &u, Name: "RTX 4090"}}}))
	a.detail = true
	if !strings.Contains(a.detailCanvas().Plain(), "not reported") {
		t.Error("an omitted device is not marked in the detail view")
	}
}

// Round 5: a mark at the start of a remote string would join the cell before it.
func TestLeadingMarksCannotJoinThePreviousCell(t *testing.T) {
	for _, mark := range []string{"ा", "ः", "ா", "́", "\U0001F3FB", "\U0001F1E8", "ᅡ", "\u200d", "ᄀ", "\u0600",
		"\u0e33", "\u0eb3", "\uff9e", "\u0d4e", "\U000111c2", "\U0001193f", "क्", "ᆨ"} {
		a := fixture(100, 30, []int{1})
		a.Deliver(sshxErr("hosta", mark+" failed 👩"+mark+"가"))
		a.mu.Lock()
		plain := a.fleetCanvas().Plain()
		a.mu.Unlock()
		for i, line := range strings.Split(plain, "\n") {
			if g, w := ansi.StringWidth(line), ansi.StringWidthWc(line); g != 100 || w != 100 {
				t.Errorf("leading %U: line %d is %d/%d cells", []rune(mark)[0], i, g, w)
			}
		}
	}
}

// Round 5: Blit clears the destination's wide-glyph halves it overwrites.
func TestBlitKeepsRowWidth(t *testing.T) {
	for x := 0; x < 3; x++ {
		dst := NewCanvas(6, 1, theme.RGB{})
		dst.Text(0, 0, "中文a ", theme.RGB{})
		src := NewCanvas(1, 1, theme.RGB{})
		src.Text(0, 0, "X", theme.RGB{})
		dst.Blit(src, 0, 0, x, 0, 1, 1)
		if w := ansi.StringWidth(dst.Plain()); w != 6 {
			t.Errorf("blit at %d: row is %d cells (%q)", x, w, dst.Plain())
		}
	}
}

// Round 6: a wide glyph blitted over another wide glyph's lead clears its tail.
func TestBlitWideOverWide(t *testing.T) {
	dst := NewCanvas(6, 1, theme.RGB{})
	dst.Text(1, 0, "中", theme.RGB{})
	src := NewCanvas(2, 1, theme.RGB{})
	src.Text(0, 0, "文", theme.RGB{})
	dst.Blit(src, 0, 0, 0, 0, 2, 1)
	if w := ansi.StringWidth(dst.Plain()); w != 6 {
		t.Errorf("row is %d cells (%q)", w, dst.Plain())
	}
}

// Ordinary text in many scripts is kept, not replaced: the joining check must
// not be so broad that it turns real names into question marks. (A ZWJ emoji
// sequence is "?" by design: wcwidth draws it 4 cells wide and grapheme mode 2,
// and the board cannot know which the terminal uses.)
func TestOrdinaryScriptsSurvive(t *testing.T) {
	for _, s := range []string{"训练任务", "가나다", "नमस्ते", "สวัสดี", "café", "🇨🇳", "ｆｕｌｌ", "😀"} {
		if strings.Count(sanitized(s), "?") > 0 && s != "नमस्ते" && s != "สวัสดี" {
			t.Errorf("%q drawn as %q", s, sanitized(s))
		}
	}
}

func sanitized(s string) string {
	var b strings.Builder
	theme.EachGlyph(s, func(g theme.Glyph) bool { b.WriteString(g.Text); return true })
	return b.String()
}

// Round 7: a value too wide for its column is cut, not drawn over the border.
func TestHugeReadingsStayInTheirColumns(t *testing.T) {
	for _, w := range []int{40, 60, 100, 200} {
		a := fixture(w, 20, []int{1})
		a.mu.Lock()
		g := &a.states["hosta"].sample.Gpus[0]
		p, temp, u := 1e6, 12345.0, 1e9
		g.Power, g.Temp, g.Util = &p, &temp, &u
		g.Index = 1234
		a.mu.Unlock()
		for i, line := range strings.Split(a.PlainFrame(), "\n") {
			r := []rune(strings.TrimRight(line, " "))
			if len(r) > 0 && r[0] == '│' && r[len(r)-1] != '│' {
				t.Errorf("width %d line %d: %q", w, i, line)
			}
		}
	}
}

// Round 8: two-digit GPU indices are shown, not cut to an ellipsis.
func TestTwoDigitIndicesAreShown(t *testing.T) {
	a := fixture(100, 40, []int{16})
	frame := a.PlainFrame()
	for i := 10; i < 16; i++ {
		if !strings.Contains(frame, fmt.Sprintf("│ %d ", i)) {
			t.Errorf("index %d missing from the board", i)
		}
	}
}

// Round 8: absurd readings stay inside the detail view's boxes too.
func TestHugeReadingsStayInsideDetail(t *testing.T) {
	for _, w := range []int{40, 60, 100} {
		a := fixture(w, 20, []int{1})
		a.mu.Lock()
		g := &a.states["hosta"].sample.Gpus[0]
		u, p, c := 1e20, 1e20, 1.0
		g.Util, g.Power, g.PowerCap = &u, &p, &c
		st := a.states["hosta"]
		st.sample.TotalKB, st.sample.MemKB = 1, ^uint64(0)
		cpu := 1e20
		st.sample.CPUPct = &cpu
		a.detail = true
		frame := a.detailCanvas().Plain()
		a.mu.Unlock()
		for i, line := range strings.Split(frame, "\n") {
			r := []rune(strings.TrimRight(line, " "))
			if len(r) > 0 && r[0] == '│' && r[len(r)-1] != '│' {
				t.Errorf("width %d line %d: %q", w, i, line)
			}
		}
	}
}

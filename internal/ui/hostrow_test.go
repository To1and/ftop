package ui

import (
	"strings"
	"testing"
	"time"

	"ftop/internal/wire"
)

// sampleWith is a host sample with the host-level facts a test wants to see
// rendered: a cgroup scope, or the whole machine, a limit, and a swap.
func sampleWith(usedKB, totalKB uint64, cpu float64) wire.Sample {
	s := wire.Sample{}
	s.Cores, s.CPUQuota = 64, true
	s.CPUPct = &cpu
	s.MemKB, s.TotalKB, s.MemLimited = usedKB, totalKB, true
	s.CacheKB = usedKB / 4
	s.SwapKnown, s.SwapLimited = true, true
	s.SwapTotalKB, s.SwapKB = 32*1024*1024, 1024*1024
	s.Load1, s.Load5, s.Load15 = 12, 11.5, 10.25
	s.Procs = 300
	s.Uptime = 49 * time.Hour
	s.ProcsSampled = true
	return s
}

// hostLines is a card's two host lines, cpu then memory, in canvas rows.
//
// p.y is board-relative and drawCards applies a.boardTop when it draws. Rather
// than re-deriving that offset here — it depends on whether the board scrolled,
// which changes with the header's height — this finds the card by its own border
// column and reads its cpu and memory rows from there.
//
// The card is located by its top border at p.x ("╭─┤● name"), NOT by any "│ cpu "
// line: the header's totals panel now has cpu and mem meters of its own, they also
// start with a border, and they sit above every card.
func hostLines(c *Canvas, p placed) (cpu, mem string) {
	lines := strings.Split(c.Plain(), "\n")
	top := -1
	for y := len(lines) - 1; y >= 0; y-- {
		if p.x < len(lines[y]) && strings.HasPrefix(lines[y][p.x:], "╭") {
			top = y
			break
		}
	}
	if top < 0 {
		return "", ""
	}
	return lines[top+1], lines[top+2]
}

// TestEveryCardCarriesTheHostBlock: a host's own cpu and memory are on its card,
// above the GPUs, on every lane, one line each — like a GPU's two lines — and
// not only in the host view.
func TestEveryCardCarriesTheHostBlock(t *testing.T) {
	for _, w := range []int{80, 120, 160, 200, 240} {
		a := fixture(w, 80, []int{4, 4, 2, 1})
		lines := strings.Split(a.PlainFrame(), "\n")
		for _, p := range a.placed {
			if p.host.err != "" || !p.host.hasData {
				continue
			}
			cpu, mem := lines[a.boardTop+p.y+1], lines[a.boardTop+p.y+2]
			if !strings.Contains(cpu, "│ cpu ") || strings.Contains(cpu, "mem") {
				t.Errorf("width %d: %s's first line is not its cpu: %q", w, p.host.name, cpu)
			}
			if !strings.Contains(mem, "│ mem ") || strings.Contains(mem, "cpu") {
				t.Errorf("width %d: %s's second line is not its memory: %q", w, p.host.name, mem)
			}
			rows := laneRows(a.width, a.lanes, 1)
			if got := p.h; got != 2+hostRows+rows*len(p.host.sample.Gpus) {
				t.Errorf("width %d: %s is %d rows, want border + host block + gpus", w, p.host.name, got)
			}
		}
	}
}

// TestHostRowNamesItsOwnMetrics: "cpu" and "mem" are the host lines' own labels
// and the bottom border keeps naming the device columns, so no word names two
// scopes.
func TestHostRowNamesItsOwnMetrics(t *testing.T) {
	a := fixture(200, 80, []int{4})
	c := a.fleetCanvas()
	p := cardOf(t, a, "hosta")
	bottom := strings.Split(c.Plain(), "\n")[p.y+p.h-1]
	if strings.Contains(bottom, "cpu") || strings.Contains(bottom, "mem") {
		t.Errorf("the border names a host reading: %q", bottom)
	}
	if !strings.Contains(bottom, "util") || !strings.Contains(bottom, "vram") {
		t.Errorf("the bottom border lost the device columns: %q", bottom)
	}
}

// TestHostBlockGivesCoresAndMemory: each line is one meter, its percentage, and
// used of what the scope has — busy of its cores, used of its memory limit. The
// block draws no history: the card's graphs are its devices'.
func TestHostBlockGivesCoresAndMemory(t *testing.T) {
	for _, w := range []int{160, 200} { // one line per GPU, then two
		a := fixture(w, 80, []int{4})
		a.mu.Lock()
		st := a.states["hosta"]
		gpus := st.sample.Gpus
		st.sample = sampleWith(32*1024*1024, 64*1024*1024, 50)
		st.sample.Gpus = gpus
		st.cpuHist, st.memHist = []float64{10, 30, 50}, []float64{40, 45, 50}
		a.mu.Unlock()
		c := a.fleetCanvas()
		cpu, mem := hostLines(c, cardOf(t, a, "hosta"))
		for _, l := range []struct{ name, row, want string }{
			{"cpu", cpu, "32/64 cores"},
			{"mem", mem, "32.0G/64.0G"},
		} {
			if !strings.Contains(l.row, l.want) {
				t.Errorf("width %d: the %s line lacks %q: %q", w, l.name, l.want, l.row)
			}
			if n := strings.Count(l.row, "%"); n != 1 {
				t.Errorf("width %d: the %s line has %d percentages, want 1: %q", w, l.name, n, l.row)
			}
			if !strings.Contains(l.row, "■") {
				t.Errorf("width %d: the %s line has no meter: %q", w, l.name, l.row)
			}
		}
	}
}

// TestUnknownHostReadingsAreDashes: a host whose cpu or memory the probe could
// not read shows "—" for it, never an empty meter (which reads as 0%). Its core
// count is still known and still shown; how many are busy is not.
func TestUnknownHostReadingsAreDashes(t *testing.T) {
	a := fixture(200, 80, []int{2})
	a.mu.Lock()
	gpus := a.states["hosta"].sample.Gpus
	s := wire.Sample{Gpus: gpus}
	s.Cores = 64
	a.states["hosta"].sample = s
	a.mu.Unlock()
	cpu, mem := hostLines(a.fleetCanvas(), cardOf(t, a, "hosta"))
	for _, row := range []string{cpu, mem} {
		if strings.Count(row, "—") != 1 || strings.Contains(row, "■") {
			t.Errorf("want one dash and no meter for an unknown reading: %q", row)
		}
	}
	if !strings.Contains(cpu, "64 cores") || strings.Contains(cpu, "/64") {
		t.Errorf("the cores are known and the busy count is not: %q", cpu)
	}
	if strings.Contains(mem, "/") {
		t.Errorf("an unknown memory still prints a reading: %q", mem)
	}
}

// TestHostMetersStandInTheCardsColumns: the host's meters start in the GPUs'
// utilisation column and their percentages stand in the GPUs' percentage
// column, on one-line and two-line lanes alike, so a card is one column of
// meters rather than two blocks with their own grids.
func TestHostMetersStandInTheCardsColumns(t *testing.T) {
	firstBar := func(r []rune) int {
		for i, ch := range r {
			if ch == '■' {
				return i
			}
		}
		return -1
	}
	pctCol := func(r []rune, from int) int {
		for i := from; i < len(r); i++ {
			if r[i] == '%' {
				return i
			}
		}
		return -1
	}
	for _, w := range []int{120, 160, 200, 240} {
		a := fixture(w, 80, []int{4})
		a.mu.Lock()
		st := a.states["hosta"]
		gpus := st.sample.Gpus
		st.sample = sampleWith(32*1024*1024, 64*1024*1024, 50)
		st.sample.Gpus = gpus
		a.mu.Unlock()
		c := a.fleetCanvas()
		p := cardOf(t, a, "hosta")
		g := grid(c.Plain())
		cardRow := func(y int) []rune { return g[y][p.x : p.x+p.w] }
		gpu := cardRow(p.y + 1 + hostRows + 2) // GPU 1, which has a non-zero util
		for i, y := range []int{p.y + 1, p.y + 2} {
			host := cardRow(y)
			if hb, gb := firstBar(host), firstBar(gpu); hb != gb {
				t.Errorf("width %d: host line %d's meter starts at %d, the GPUs' at %d:\n%s\n%s", w, i, hb, gb, string(host), string(gpu))
			}
			if hp, gp := pctCol(host, 0), pctCol(gpu, 0); hp != gp {
				t.Errorf("width %d: host line %d's percentage ends at %d, the GPUs' at %d:\n%s\n%s", w, i, hp, gp, string(host), string(gpu))
			}
		}
	}
}

// TestHostBlockTakesItsBand: the host block is the first of the card's
// alternating bands, so the first GPU, plain, does not run on from it as one
// block, and every GPU keeps the band it had.
func TestHostBlockTakesItsBand(t *testing.T) {
	a := fixture(200, 60, []int{4})
	c := a.fleetCanvas()
	p := cardOf(t, a, "hosta")
	bg := a.skin.Bg
	for y := p.y + 1; y < p.y+1+hostRows; y++ {
		for x := p.x + 1; x < p.x+p.w-1; x++ {
			if c.at(x, y).BG == bg {
				t.Fatalf("host line %d cell %d is off the band", y-p.y-1, x-p.x)
			}
		}
	}
	if c.at(p.x+2, p.y+1+hostRows).BG != bg {
		t.Error("GPU 0 is banded: it runs on from the host block")
	}
}

// TestBusyCoresKeepsALightLoad: below ten cores' worth the count keeps a
// decimal, so 2.5% of 128 cores is 3.2, not a rounded 3 that would read as
// whole idle cores.
func TestBusyCoresKeepsALightLoad(t *testing.T) {
	for _, tc := range []struct {
		pct   float64
		cores int
		want  string
	}{{2.5, 128, "3.2"}, {57, 128, "73"}, {0, 16, "0.0"}, {100, 16, "16"}, {7.77, 128, "9.9"}, {7.8, 128, "10"}} {
		if got := busyCores(tc.pct, tc.cores); got != tc.want {
			t.Errorf("busyCores(%v, %d) = %q, want %q", tc.pct, tc.cores, got, tc.want)
		}
	}
}

// TestHostReadingIsNeverCut: a reading that does not fit its line is left out
// whole. "339.2G/5…" states something false about the limit.
func TestHostReadingIsNeverCut(t *testing.T) {
	for w := 60; w <= 240; w += 3 {
		a := fixture(w, 80, []int{4, 2})
		a.mu.Lock()
		for _, st := range a.states {
			gpus := st.sample.Gpus
			st.sample = sampleWith(339*1024*1024, 512*1024*1024, 57)
			st.sample.Cores = 128
			st.sample.Gpus = gpus
		}
		a.mu.Unlock()
		c := a.fleetCanvas()
		for _, p := range a.placed {
			cpu, mem := hostLines(c, p)
			for _, row := range []string{cpu, mem} {
				if strings.Contains(row, "…") {
					t.Errorf("width %d: a host reading is cut: %q", w, row)
				}
			}
			if strings.Contains(mem, "/") && !strings.Contains(mem, "339.0G/512.0G") {
				t.Errorf("width %d: the memory reading is partial: %q", w, mem)
			}
		}
	}
}

// TestHostViewNamesTheScope: a cgroup-confined host says so and gives the quota;
// an unconfined one says the figures are the whole machine's. The two must not
// look alike, because their percentages are not comparable.
func TestHostViewNamesTheScope(t *testing.T) {
	a := fixture(200, 50, []int{2})
	a.mu.Lock()
	st := a.states["hosta"]
	st.sample = sampleWith(32*1024*1024, 64*1024*1024, 40)
	a.mu.Unlock()
	a.open("hosta")
	frame := a.detailCanvas().Plain()
	if !strings.Contains(frame, "cgroup quota") {
		t.Errorf("a quota host does not name its scope:\n%s", frame)
	}
	if !strings.Contains(frame, "128.0G/256.0G") && !strings.Contains(frame, "/64.0G") {
		t.Errorf("the memory limit is missing:\n%s", frame)
	}

	a.mu.Lock()
	st.sample.CPUQuota, st.sample.MemLimited = false, false
	st.sample.TotalKB = 512 * 1024 * 1024
	a.mu.Unlock()
	frame = a.detailCanvas().Plain()
	if strings.Contains(frame, "cgroup quota") {
		t.Errorf("a whole-machine host still claims a quota:\n%s", frame)
	}
	if !strings.Contains(frame, "whole machine") {
		t.Errorf("a whole-machine host does not say so:\n%s", frame)
	}
}

// TestHostViewSaysWhenThereIsNoSwap: no swap is a fact, and an empty meter would
// report it as "swap idle".
func TestHostViewSaysWhenThereIsNoSwap(t *testing.T) {
	a := fixture(160, 50, []int{1})
	a.mu.Lock()
	s := sampleWith(8*1024*1024, 16*1024*1024, 10)
	s.SwapKnown = false
	a.states["hosta"].sample = s
	a.mu.Unlock()
	a.open("hosta")
	frame := a.detailCanvas().Plain()
	var row string
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "│ swap") {
			row = line
		}
	}
	if !strings.Contains(row, "none") {
		t.Errorf("a host with no swap does not say so: %q", row)
	}
	if strings.Contains(row, "■") || strings.Contains(row, "░") {
		t.Errorf("a host with no swap draws a meter: %q", row)
	}
}

// TestProcessViewJoinsTheCards: the full table lists the host's processes by
// memory and by cpu, and marks which hold a GPU. A process on no card shows "·",
// which is the fact that sends a reader to the card view to find out why.
func TestProcessViewJoinsTheCards(t *testing.T) {
	a := fixture(200, 40, []int{2})
	a.mu.Lock()
	st := a.states["hosta"]
	one, two := 12.5, 340.0
	st.hostProcs = []wire.HostProc{
		{PID: 40001, RSSKB: 64 * 1024 * 1024, CPUPct: &one, User: "alice", Name: "train.py"},
		{PID: 50001, RSSKB: 1024 * 1024, CPUPct: &two, User: "bob", Name: "jupyter-lab"},
	}
	st.sample.Gpus[0].Procs = []wire.Proc{{PID: 40001, User: "alice", Name: "train.py"}}
	st.sample.Procs = 2
	a.mu.Unlock()
	frame := a.RenderProcPlain("hosta", 200, 40)
	if !strings.Contains(frame, "by memory") || !strings.Contains(frame, "by cpu") {
		t.Errorf("the table has no two orders:\n%s", frame)
	}
	if !strings.Contains(frame, "train.py") || !strings.Contains(frame, "jupyter-lab") {
		t.Errorf("a process is missing:\n%s", frame)
	}
	if !strings.Contains(frame, "340%") {
		t.Errorf("the cpu column is not percent-of-a-core:\n%s", frame)
	}
	if !strings.Contains(frame, "·") {
		t.Errorf("a process on no card is not marked:\n%s", frame)
	}
}

// TestProcessKeyOpensFromBoardAndHost: p opens the table from either view, and
// esc returns to the view it came from.
func TestProcessKeyOpensFromBoardAndHost(t *testing.T) {
	a := fixture(200, 40, []int{2})
	a.mu.Lock()
	a.states["hosta"].hostProcs = []wire.HostProc{{PID: 7, RSSKB: 1024, User: "u", Name: "n"}}
	a.mu.Unlock()
	a.press("p")
	if !a.procView || a.detail {
		t.Fatalf("p on the board: procView=%v detail=%v", a.procView, a.detail)
	}
	a.press("esc")
	if a.procView {
		t.Fatal("esc did not close the table")
	}
	a.open("hosta")
	a.press("p")
	if !a.procView {
		t.Fatal("p in a host view did not open the table")
	}
	a.press("esc")
	if a.procView || !a.detail {
		t.Fatalf("esc from the table should return to the host view: procView=%v detail=%v", a.procView, a.detail)
	}
}

// TestHeaderHostsRowIsWeighted: the fleet's cpu is busy cores over cores, not a
// mean of percentages — a tiny host at 100% must not outweigh a big one at 5%.
func TestHeaderHostsRowIsWeighted(t *testing.T) {
	a := fixture(200, 50, []int{1, 1})
	ten, five := 100.0, 5.0
	a.mu.Lock()
	small := a.states["hosta"]
	small.sample.Cores, small.sample.CPUPct = 4, &ten
	small.sample.TotalKB, small.sample.MemKB = 1024, 512
	big := a.states["hostb"]
	big.sample.Cores, big.sample.CPUPct = 96, &five
	big.sample.TotalKB, big.sample.MemKB = 1024, 512
	a.mu.Unlock()
	fs := a.fleetStats()
	// (4*1 + 96*0.05) / 100 = 8.8%.
	if got, want := fs.hostCPUPct(), 8.8; got < want-0.01 || got > want+0.01 {
		t.Errorf("fleet cpu = %.2f%%, want %.2f%% (Σbusy/Σcores)", got, want)
	}
	if got := fs.hostMemPct(); got != 50 {
		t.Errorf("fleet mem = %.1f%%, want 50%%", got)
	}
}

// TestHostWithNoGPUsKeepsItsBlock: a host that answers with no devices still
// has a cpu and a memory to report. Its card is tall enough for both lines, so
// the second does not land on the bottom border.
func TestHostWithNoGPUsKeepsItsBlock(t *testing.T) {
	a := fixture(200, 60, []int{2, 1})
	a.mu.Lock()
	s := sampleWith(8*1024*1024, 16*1024*1024, 20)
	a.states["hosta"].sample = s
	a.mu.Unlock()
	c := a.fleetCanvas()
	p := cardOf(t, a, "hosta")
	if p.h != 2+hostRows {
		t.Fatalf("a host with no GPUs is %d rows, want border + host block", p.h)
	}
	lines := strings.Split(c.Plain(), "\n")
	cpu, mem, bottom := lines[p.y+1], lines[p.y+2], []rune(lines[p.y+3])
	if !strings.Contains(cpu, "cpu") || !strings.Contains(mem, "mem") {
		t.Errorf("the host block is missing:\n%s\n%s", cpu, mem)
	}
	if bottom[p.x] != '╰' && bottom[p.x] != '└' {
		t.Errorf("the bottom border is overwritten: %q", string(bottom))
	}
}

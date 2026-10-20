package ui

import (
	"strings"
	"testing"
)

// TestHostBlockFillsItsWidth: the host block's readings stop well short of a
// card's inner width, and that space used to be empty on every card — or, for
// one revision, filled with load, process count, page cache and uptime, which
// were removed because they were neither asked for nor legible as a run of
// numbers at the end of a line.
//
// The space carries the machine's own utilisation history instead: the same graph
// the GPU rows below draw, two rows tall, in the host cpu ramp. It ends on the
// card's inner margin so a column of cards has one right edge.
func TestHostBlockFillsItsWidth(t *testing.T) {
	for _, w := range []int{160, 200, 240} {
		a := fixture(w, 80, []int{4})
		a.mu.Lock()
		st := a.states["hosta"]
		gpus := st.sample.Gpus
		st.sample = sampleWith(32*1024*1024, 64*1024*1024, 50)
		st.sample.Gpus = gpus
		st.cpuHist = []float64{10, 40, 70, 50, 20, 60, 90}
		a.mu.Unlock()
		c := a.fleetCanvas()
		p := cardOf(t, a, "hosta")
		cpu, mem := hostLines(c, p)
		// Both lines end at the same column, and it is the card's margin: the
		// graph is drawn as two rows of one chart, not two ragged runs.
		if got, want := brailleTail(cpu), brailleTail(mem); got != want {
			t.Errorf("width %d: the cpu graph ends at %d and the memory graph at %d; they are one chart", w, got, want)
		}
		if brailleTail(cpu) < 0 {
			t.Errorf("width %d: the host block draws no history: %q", w, cpu)
		}
		// hostLines returns the whole canvas row, so the card is cut out of it
		// before the margin is checked.
		row := []rune(cpu)
		if p.x+p.w > len(row) {
			t.Fatalf("width %d: card runs past the line", w)
		}
		card := row[p.x : p.x+p.w]
		if card[len(card)-1] != '│' || card[len(card)-2] != ' ' {
			t.Errorf("width %d: the graph does not end on the margin: %q", w, string(card))
		}
	}
}

// TestHostGraphNeverOverwritesTheReadings: a graph is placed only where it leaves
// the readings their room. A narrow lane keeps the cores and the memory total
// whole and draws a shorter graph, or none at all; it never lets a graph run over
// a number.
func TestHostGraphNeverOverwritesTheReadings(t *testing.T) {
	for w := 60; w <= 240; w += 4 {
		a := fixture(w, 80, []int{4})
		a.mu.Lock()
		st := a.states["hosta"]
		gpus := st.sample.Gpus
		st.sample = sampleWith(32*1024*1024, 64*1024*1024, 50)
		st.sample.Gpus = gpus
		st.cpuHist = []float64{10, 40, 70, 50, 20, 60, 90}
		a.mu.Unlock()
		c := a.fleetCanvas()
		p := cardOf(t, a, "hosta")
		cpu, mem := hostLines(c, p)
		for _, l := range []struct{ name, row, want string }{
			{"cpu", cpu, "32/64 cores"},
			{"mem", mem, "32.0G/64.0G"},
		} {
			if !strings.Contains(l.row, l.want) {
				t.Errorf("width %d: the %s line lacks %q: %q", w, l.name, l.want, l.row)
			}
			// The reading must finish before the graph starts: a graph written
			// into the same columns as a number is an unreadable overlap.
			if at := strings.Index(l.row, l.want); at >= 0 {
				tail := l.row[at+len(l.want):]
				if i := strings.IndexFunc(tail, isBraille); i >= 0 {
					if body := strings.TrimSpace(tail[:i]); body != "" {
						// There is text between the reading and the graph with no
						// separator: only a gap is allowed.
						if n := len(strings.Split(body, " ")); n > 1 {
							t.Errorf("width %d: %s reading runs into the graph (gap = %q)", w, l.name, body)
						}
					}
				}
			}
		}
	}
}

// brailleTail is the column of the last braille cell in a line, or -1.
func brailleTail(s string) int {
	last := -1
	for i, r := range []rune(s) {
		if r >= 0x2800 && r <= 0x28ff {
			last = i
		}
	}
	return last
}

func isBraille(r rune) bool { return r >= 0x2800 && r <= 0x28ff }

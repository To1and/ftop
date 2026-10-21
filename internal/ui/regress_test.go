package ui

import (
	"strings"
	"testing"

	"ftop/internal/theme"
	"ftop/internal/wire"
)

// TestTinyTerminalDoesNotPanic: at 4x24 the fallback graph width hits zero, Graph
// returns nil and indexing it panicked.
func TestTinyTerminalDoesNotPanic(t *testing.T) {
	for _, sz := range [][2]int{{4, 24}, {10, 5}, {39, 12}, {40, 12}} {
		a := fixture(sz[0], sz[1], []int{8, 4, 2})
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%dx%d panicked: %v", sz[0], sz[1], r)
				}
			}()
			_ = a.View()
		}()
	}
}

// TestDetailProcessRowsDoNotOverwriteVRAMBar checks the actual rows: the claim
// line is first, the gauge block owns the three rows under it, the process table
// starts below them with its column names in the bottom border, and nothing from
// the table may land on a gauge row.
func TestDetailProcessRowsDoNotOverwriteVRAMBar(t *testing.T) {
	a := fixture(160, 40, []int{1})
	a.mu.Lock()
	st := a.states["hosta"]
	st.sample.Gpus[0].Procs = []wire.Proc{{PID: 4242, Name: "worker", User: "z", MemMB: 4096}}
	a.mu.Unlock()
	a.detail = true

	c := a.detailCanvas()
	boxTop := -1
	for y := 0; y < c.H; y++ {
		if strings.Contains(rowText(c, y), "0 · RTX 4090") {
			boxTop = y
			break
		}
	}
	if boxTop < 0 {
		t.Fatal("could not locate the GPU box")
	}
	vramRow := rowText(c, boxTop+3)
	if !strings.HasPrefix(vramRow, "│ vram") {
		t.Fatalf("second gauge row is not VRAM: %q", vramRow)
	}
	if strings.Contains(vramRow, "4242") {
		t.Errorf("process text overwrote the VRAM bar: %q", vramRow)
	}
	if !strings.Contains(vramRow, "G/") && !strings.Contains(vramRow, "M/") {
		t.Errorf("VRAM row lost its memory reading: %q", vramRow)
	}
	// Under the gauges: a dashed rule, the header naming the columns, then the
	// processes.
	if rule := rowText(c, boxTop+2+detailGauges); !strings.Contains(rule, "┄┄┄") {
		t.Errorf("no rule between the gauges and the process table: %q", rule)
	}
	if head := rowText(c, boxTop+3+detailGauges); !strings.Contains(head, "Pid") || !strings.Contains(head, "Command") {
		t.Errorf("the process columns are not named over them: %q", head)
	}
	if procRow := rowText(c, boxTop+4+detailGauges); !strings.Contains(procRow, "4242") {
		t.Errorf("first process line is not directly below the header: %q", procRow)
	}
}

// TestProcessListSurvivesGaugeOnlyRound: the reader must carry the previous list
// forward when a round did not sample processes.
func TestProcessListSurvivesGaugeOnlyRound(t *testing.T) {
	a := fixture(160, 40, []int{1})
	a.mu.Lock()
	st := a.states["hosta"]
	st.sample.Gpus[0].Procs = []wire.Proc{{PID: 11, Name: "job", User: "z"}}
	st.sample.Gpus[0].ProcsStale = true
	a.mu.Unlock()
	a.detail = true
	if !strings.Contains(a.detailCanvas().Plain(), "job") {
		t.Error("carried-over process list was dropped")
	}
}

// TestHeaderCountsAreHonest: "all online" must not appear while nothing has answered.
func TestHeaderCountsAreHonest(t *testing.T) {
	a := New(Options{Hosts: []string{"h1", "h2"}, GraphRows: 2})
	a.width, a.height = 200, 30
	head := strings.Split(a.fleetCanvas().Plain(), "\n")[0]
	if strings.Contains(head, "all online") {
		t.Errorf("claims all online with zero samples: %q", head)
	}
}

// rowText extracts one row without colour.
func rowText(c *Canvas, y int) string {
	if y < 0 || y >= c.H {
		return ""
	}
	var b strings.Builder
	for x := 0; x < c.W; x++ {
		cell := &c.cells[y*c.W+x]
		if cell.Cont {
			continue
		}
		if cell.Text == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(cell.Text)
		}
	}
	return b.String()
}

var _ = theme.RuneWidth

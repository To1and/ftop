package theme

import "testing"

// TestGraphUsesNewestSamples guards the regression where a long series was sliced
// from the head, leaving the graph blank because the head was all padding.
func TestGraphUsesNewestSamples(t *testing.T) {
	tm := Default
	data := make([]float64, 200)
	for i := 100; i < 200; i++ {
		data[i] = 100 // only the newest half is busy
	}
	rows := Graph(tm, data, 40, 2, "", GraphOpts{})
	blank := 0
	for _, c := range rows[0] {
		if c.Text == " " {
			blank++
		}
	}
	if blank > 2 {
		t.Errorf("%d of 40 cells blank; the graph is showing the old half", blank)
	}
}

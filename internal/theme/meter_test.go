package theme

import "testing"

// TestMeterFillsProportionally is the check the fleet view failed on its first live
// run: every bar rendered full, so 21% and 100% were indistinguishable.
func TestMeterFillsProportionally(t *testing.T) {
	tm := Default
	const w = 40
	m := Meter{Width: w, Scale: ptrScale(rampUtil)}

	// The empty portion must be distinguishable WITHOUT colour; a bar drawn as solid
	// blocks on a background colour is invisible to any plain-text consumer.
	for _, c := range m.Render(tm, 30) {
		if c.FG == tm.C("meter_bg") && c.Text == "█" {
			t.Fatal("unfilled meter cell is a solid block; plain-text output would show a full bar")
		}
	}

	for _, tc := range []struct{ value, wantFilled int }{
		{0, 0}, {10, 4}, {21, 8}, {50, 20}, {75, 30}, {100, w},
	} {
		// The unfilled half is marked by Dim, not by a colour that happens to
		// differ: the track is mixed toward the page now.
		filled := 0
		cells := m.Render(tm, tc.value)
		for _, c := range cells {
			if !c.Dim {
				filled++
			}
		}
		if filled != tc.wantFilled {
			t.Errorf("value %d filled %d/%d cells, want %d", tc.value, filled, w, tc.wantFilled)
		}
		if len(cells) != w {
			t.Errorf("value %d produced %d cells, want %d", tc.value, len(cells), w)
		}
	}
}

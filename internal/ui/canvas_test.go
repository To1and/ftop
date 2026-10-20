package ui

import (
	"strings"
	"testing"

	"ftop/internal/theme"
)

// TestGlyphOverwriteKeepsRowWidth: overwriting the lead of a wide glyph with a
// narrow one used to leave its continuation marker behind, which made every row
// containing a CJK label one cell short and shifted the rest left.
func TestGlyphOverwriteKeepsRowWidth(t *testing.T) {
	c := NewCanvas(6, 1, theme.RGB{})
	c.Text(0, 0, "中文a", theme.RGB{R: 1, G: 2, B: 3})
	// Overwrite the first cell with a narrow glyph, then the second.
	c.Put(0, 0, "x", theme.RGB{})
	c.Put(1, 0, "y", theme.RGB{})
	if got := theme.RuneWidth(c.Plain()); got != 6 {
		t.Errorf("row width = %d, want 6 (plain %q)", got, c.Plain())
	}
}

// TestWideGlyphInLastColumnIsDropped: a double-width rune cannot occupy the final
// column of a fixed grid; drawing it would extend the row past the canvas.
func TestWideGlyphInLastColumnIsDropped(t *testing.T) {
	c := NewCanvas(4, 1, theme.RGB{})
	c.Put(3, 0, "中", theme.RGB{})
	if got := theme.RuneWidth(c.Plain()); got > 4 {
		t.Errorf("row width = %d, want at most 4", got)
	}
}

// TestCJKLabelDoesNotShiftTheRow is the user-visible form of the same bug: a host
// labelled in Chinese must not move everything to its right.
func TestCJKLabelDoesNotShiftTheRow(t *testing.T) {
	a := fixture(120, 30, []int{1})
	a.mu.Lock()
	a.states["hosta"].label = "实验室"
	a.mu.Unlock()
	frame := a.PlainFrame()
	for i, line := range strings.Split(frame, "\n") {
		if w := theme.RuneWidth(line); w > 120 {
			t.Errorf("line %d is %d cells, want ≤120", i, w)
		}
	}
	if !strings.Contains(frame, "实验室") {
		t.Error("the CJK label did not render at all")
	}
}

package ui

import (
	"strings"
	"testing"

	"ftop/internal/theme"
)

// TestHeaderFitsExactly: the totals line must never be wider than the terminal, and
// a right-aligned status that does not fit must be dropped, not overprinted.
func TestHeaderFitsExactly(t *testing.T) {
	for _, w := range []int{60, 80, 100, 140, 200} {
		a := fixture(w, 30, []int{2, 4})
		head := strings.Split(a.PlainFrame(), "\n")[0]
		if got := theme.RuneWidth(head); got > w {
			t.Errorf("width %d: header is %d cells", w, got)
		}
		t.Logf("w=%3d |%s|", w, head)
	}
}

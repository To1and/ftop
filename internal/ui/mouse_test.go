package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func clickAt(a *App, x, y int) {
	a.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	a.View()
}

// TestClickSelectsThenOpens: the first click on a card selects it, a second
// click on the selected card opens it.
func TestClickSelectsThenOpens(t *testing.T) {
	a := fixture(200, 50, []int{4, 4, 2})
	a.View()
	var target *placed
	for i := range a.placed {
		if a.placed[i].host.name != a.focused() {
			target = &a.placed[i]
			break
		}
	}
	if target == nil {
		t.Fatal("no second card to click")
	}
	name := target.host.name
	x, y := target.x+target.w/2, a.boardTop+target.y-a.boardScroll+1
	clickAt(a, x, y)
	if a.focused() != name || a.detail {
		t.Fatalf("first click: focused %q detail %v, want %q selected and not open", a.focused(), a.detail, name)
	}
	clickAt(a, x, y)
	if !a.detail || a.order[a.cursor] != name {
		t.Fatalf("second click did not open %q (detail %v)", name, a.detail)
	}
}

// TestClickOnHintPressesItsKey: the status line's hints are buttons.
func TestClickOnHintPressesItsKey(t *testing.T) {
	a := fixture(200, 50, []int{4})
	a.View()
	var help *hit
	for i := range a.hits {
		if a.hits[i].key == "?" {
			help = &a.hits[i]
		}
	}
	if help == nil {
		t.Fatal("the ? hint is not clickable")
	}
	clickAt(a, help.x+help.w-1, help.y)
	if !a.help {
		t.Fatal("clicking the help hint did not open help")
	}
	clickAt(a, 5, 5)
	if a.help {
		t.Fatal("a click did not close help")
	}
}

// TestWheelScrollsTheBoard: the wheel moves a board taller than the terminal,
// and stops at its ends.
func TestWheelScrollsTheBoard(t *testing.T) {
	a := fixture(80, 20, []int{8, 8, 8, 8})
	a.View()
	a.Update(tea.MouseWheelMsg{X: 10, Y: 10, Button: tea.MouseWheelDown})
	a.View()
	if a.boardScroll != wheelStep {
		t.Fatalf("scroll after one notch = %d, want %d", a.boardScroll, wheelStep)
	}
	for i := 0; i < 100; i++ {
		a.Update(tea.MouseWheelMsg{X: 10, Y: 10, Button: tea.MouseWheelDown})
	}
	a.View()
	end := a.boardScroll
	if end == 0 || end+a.boardView < 4*10 {
		t.Fatalf("wheel did not reach the end of the board: scroll %d", end)
	}
	a.Update(tea.MouseWheelMsg{X: 10, Y: 10, Button: tea.MouseWheelUp})
	a.View()
	if a.boardScroll != end-wheelStep {
		t.Fatalf("wheel up from the end: %d, want %d", a.boardScroll, end-wheelStep)
	}
}

// TestClickMapsThroughScroll: on a scrolled board a click lands on the card
// drawn under the pointer, not the one at that position before scrolling.
func TestClickMapsThroughScroll(t *testing.T) {
	a := fixture(80, 20, []int{8, 8, 8, 8})
	a.View()
	for i := 0; i < 5; i++ {
		a.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	}
	a.View()
	y := a.boardTop + a.boardView - 2
	p := a.cardAt(10, y)
	if p == nil {
		t.Fatal("no card under the pointer")
	}
	if p.y > a.boardScroll+a.boardView || p.y+p.h <= a.boardScroll {
		t.Fatalf("card %s at board rows %d..%d is not in the window %d..%d", p.host.name, p.y, p.y+p.h, a.boardScroll, a.boardScroll+a.boardView)
	}
}

// TestMouseCanBeTurnedOff: with NoMouse the terminal keeps plain text selection.
func TestMouseCanBeTurnedOff(t *testing.T) {
	a := fixture(120, 30, []int{2})
	if v := a.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode %v, want cell motion", v.MouseMode)
	}
	b := New(Options{Hosts: []string{"h"}, NoMouse: true})
	b.width, b.height = 120, 30
	if v := b.View(); v.MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse mode %v with NoMouse, want none", v.MouseMode)
	}
}

// TestDetailHintsNavigate: in a host, the prev/next hints switch hosts.
func TestDetailHintsNavigate(t *testing.T) {
	a := fixture(200, 50, []int{4, 4})
	a.View()
	a.open(a.sortedNames()[0])
	a.View()
	var next *hit
	for i := range a.hits {
		if a.hits[i].key == "right" {
			next = &a.hits[i]
		}
	}
	if next == nil {
		t.Fatal("no next-host hint in the detail view")
	}
	clickAt(a, next.x, next.y)
	if got := a.order[a.cursor]; got != a.sortedNames()[1] {
		t.Fatalf("next hint opened %q, want %q", got, a.sortedNames()[1])
	}
}

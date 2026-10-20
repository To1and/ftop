package ui

import (
	"fmt"
	"sort"
	"strings"

	"ftop/internal/theme"
)

// legendLabel writes a column name into a box's bottom border in the colour of
// the metric it names, so the same hue means the same reading in the skyline,
// the card's border and the help screen.
func (c *Canvas) legendLabel(x, y int, txt string, s *theme.Skin) int {
	t := s.Theme
	c.Put(x-1, y, " ", t.C("graph_text"))
	words := strings.Split(txt, " ")
	cx := x
	for i, w := range words {
		if i > 0 {
			cx = c.Text(cx, y, " ", t.C("graph_text"))
		}
		var fg theme.RGB
		switch w {
		case "util", "history":
			// The history IS the utilisation, so it says so in util's hue; the
			// hue identifies the reading, not the fact that this is a label.
			fg = s.Scale.CPU.At(72)
		case "vram":
			fg = s.Scale.VRAM.At(78)
		default:
			fg = t.C("graph_text")
		}
		cx = c.Text(cx, y, w, fg)
	}
	c.Put(cx, y, " ", t.C("graph_text"))
	return cx + 1
}

// subtitleSegs is a card's GPU label: each model in its own colour, a shade of
// its product line's hue, so a board of mixed cards says which is which at a
// glance; the count as bright as the text and the size secondary.
func subtitleSegs(h *hostState, s *theme.Skin) []seg {
	t := s.Theme
	if len(h.sample.Gpus) == 0 {
		return nil
	}
	counts := map[string]int{}
	full := map[string]string{} // short name -> the name NVML reported
	for _, g := range h.sample.Gpus {
		k := theme.ShortGPU(g.Name)
		counts[k]++
		full[k] = g.Name
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	dim := t.C("graph_text")
	var out []seg
	for i, k := range keys {
		if i > 0 {
			out = append(out, seg{" + ", dim, false})
		}
		// The count is a fact about this host, so it is as bright as the model
		// rather than part of the grey apart from it.
		out = append(out, seg{fmt.Sprintf("%d× ", counts[k]), t.C("main_fg"), false}, seg{k, s.Model(full[k]), false})
	}
	if total, ok := uniformVRAM(h.sample.Gpus); ok {
		out = append(out, seg{" · " + theme.MiB(total), dim, false})
	}
	return out
}

// errorLine writes a host's failure: the reason in the alert colour, then the
// rest of the message a gap later, dimmer. "connection timed out: ssh: connect to
// host … port 22" was one red line in which the two words that say what happened
// were no louder than the address.
func (c *Canvas) errorLine(x, y, w int, err string, t *theme.Theme) {
	why, rest := err, ""
	if i := strings.Index(err, ": "); i > 0 {
		why, rest = err[:i], err[i+2:]
	}
	end := c.Text(x, y, theme.Clip(why, w), t.C("hi_fg"))
	if rx := end + 3; rest != "" && rx < x+w {
		c.Text(rx, y, theme.Clip(rest, x+w-rx), t.C("graph_text"))
	}
}

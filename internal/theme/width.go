package theme

import (
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Text width, the way the terminal renderer will see it.
//
// Bubble Tea measures a frame with one of two methods: wcwidth, or grapheme
// clustering when the terminal supports it (mode 2027). A hand-written width
// table agrees with neither on emoji, Indic scripts or combining marks, and every
// disagreement is a row one cell short or long: the right border lands in the
// wrong column, or the line wraps.
//
// So text is walked as grapheme clusters, and a cluster is drawn only when BOTH
// methods give it the same width of 1 or 2. Anything else (a cluster the two
// methods disagree on, a control character, invalid UTF-8) is drawn as "?". The
// frame then measures the same whichever method the terminal uses.
//
// Control characters matter beyond layout: remote strings (process command
// lines, ssh errors, GPU names) reach the canvas verbatim and the canvas is
// written to the terminal verbatim, so an escape sequence in a process name was
// an escape sequence another machine chose to send to this terminal.

// Glyph is one drawable grapheme cluster and the cells it occupies (1 or 2).
type Glyph struct {
	Text string
	W    int
}

var replacement = Glyph{Text: "?", W: 1}

// EachGlyph walks s as drawable glyphs, stopping early if fn returns false.
// Zero-width clusters (a combining mark with nothing to combine with) are
// dropped: they have no cell to be drawn in.
func EachGlyph(s string, fn func(Glyph) bool) {
	// Most strings drawn are one glyph — a block, a rule, a dot — and a frame draws
	// thousands of them. Segmenting each afresh was an eighth of a frame's CPU, so
	// a string already known to be exactly one cluster is answered from a cache.
	if len(s) <= 16 && len(s) > 0 && s[0] >= 0x80 {
		if v, ok := singleCache.Load(s); ok {
			if g := v.(Glyph); g.W > 0 {
				fn(g)
			}
			return
		}
	}
	for len(s) > 0 {
		// Fast path: printable ASCII not followed by a combining continuation.
		if c := s[0]; c >= 0x20 && c < 0x7f && (len(s) == 1 || s[1] < 0x80) {
			if !fn(Glyph{Text: s[:1], W: 1}) {
				return
			}
			s = s[1:]
			continue
		}
		cluster, _ := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		if cluster == "" {
			cluster = s[:1]
		}
		whole := len(cluster) == len(s)
		s = s[len(cluster):]
		g := glyphOf(cluster)
		if whole && len(cluster) <= 16 {
			singleCache.Store(cluster, g)
		}
		if g.W == 0 {
			continue
		}
		if !fn(g) {
			return
		}
	}
}

var glyphCache sync.Map // cluster -> Glyph

// singleCache maps a string that is exactly one cluster to its glyph.
var singleCache sync.Map

// glyphOf sanitises and measures one cluster.
func glyphOf(cluster string) Glyph {
	if v, ok := glyphCache.Load(cluster); ok {
		return v.(Glyph)
	}
	g := measure(cluster)
	if len(cluster) <= 16 { // the cache holds glyphs, not arbitrary long clusters
		glyphCache.Store(cluster, g)
	}
	return g
}

func measure(cluster string) Glyph {
	for i, r := range cluster {
		// An invalid byte decodes as RuneError with size 1; a real U+FFFD is
		// three bytes and is a legitimate character to show.
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(cluster[i:]); size <= 1 {
				return replacement
			}
		}
		if unicode.IsControl(r) {
			return replacement
		}
	}
	if joinsNeighbour(cluster) {
		return replacement
	}
	_, gw := ansi.FirstGraphemeCluster(cluster, ansi.GraphemeWidth)
	ww := ansi.StringWidthWc(cluster)
	switch {
	case gw == 0 && ww == 0:
		return Glyph{Text: cluster, W: 0}
	case gw != ww || gw < 1 || gw > 2:
		return replacement
	}
	return Glyph{Text: cluster, W: gw}
}

// RuneWidth is the cell width of s as drawn.
func RuneWidth(s string) int {
	w := 0
	EachGlyph(s, func(g Glyph) bool { w += g.W; return true })
	return w
}

// joinsNeighbour reports a cluster that would merge with the glyph drawn in the
// cell before or after it, which shortens the row by a cell.
//
// A cluster is complete on its own only relative to its own string: a leading
// mark (U+093E, U+0E33, U+FF9E…), a Hangul medial or final jamo, a trailing
// zero-width joiner, virama or prepended mark all combine across a cell
// boundary. Instead of listing those properties by hand (every list so far was
// incomplete), the cluster is segmented next to representative neighbours with
// the renderer's own segmenter. The neighbours are glyphs that can themselves be
// drawn, so the check is closed: nothing that passes can join anything else
// that passes.
func joinsNeighbour(cluster string) bool {
	if r, n := utf8.DecodeRuneInString(cluster); n == len(cluster) && r >= 0x1F1E6 && r <= 0x1F1FF {
		return true // a lone regional indicator pairs with the next one
	}
	for _, nb := range neighbours {
		if c, _ := ansi.FirstGraphemeCluster(nb+cluster, ansi.GraphemeWidth); len(c) != len(nb) {
			return true
		}
		if c, _ := ansi.FirstGraphemeCluster(cluster+nb, ansi.GraphemeWidth); len(c) != len(cluster) {
			return true
		}
	}
	return false
}

// neighbours stand for the classes of cluster that matter to segmentation: an
// ordinary letter, a Hangul syllable, a pictographic emoji, an Indic consonant.
var neighbours = []string{"a", "가", "😀", "क"}

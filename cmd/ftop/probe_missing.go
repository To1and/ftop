//go:build !probe

package main

// Building without `-tags probe` produces a working TUI with no remote sampler.
// The tools are useful in that state (`-list`, `-version`, config debugging), and
// failing at compile time with a missing embed target would make a fresh clone
// look broken. Running it against a fleet reports the missing probe instead.
var probeBin []byte

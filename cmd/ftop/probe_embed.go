//go:build probe

package main

import _ "embed"

// The probe binary is embedded so `ftop` stays one command with no install step on
// either end: it is pushed on first connect and cached remotely by content hash.
//
// This file is only compiled with `-tags probe`, which `make` passes after
// building the probe. That keeps a fresh clone buildable: the binary is a 1.9 MB
// build artefact and does not belong in the repository, but go:embed requires the
// path to exist at compile time.
//
//go:embed probe/ftop-probe
var probeBin []byte

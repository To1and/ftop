//go:build !linux

package main

import "os"

// fileOwner is a no-op off Linux; the probe only ever runs on the fleet.
func fileOwner(os.FileInfo) (string, bool) { return "", false }

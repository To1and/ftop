//go:build linux

package main

import (
	"os"
	"os/user"
	"syscall"
)

// fileOwner resolves a uid to a name without shelling out.
func fileOwner(fi os.FileInfo) (string, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	u, err := user.LookupId(itoa(int(st.Uid)))
	if err != nil {
		return itoa(int(st.Uid)), true
	}
	return u.Username, true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

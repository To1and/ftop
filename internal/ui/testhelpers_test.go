package ui

import (
	tea "charm.land/bubbletea/v2"

	"ftop/internal/sshx"
	"ftop/internal/wire"
)

func sshxSample(host string, s wire.Sample) sshx.SampleMsg {
	return sshx.SampleMsg{Host: host, Sample: s}
}

func sshxErr(host, msg string) sshx.ErrMsg {
	return sshx.ErrMsg{Host: host, Err: msg}
}

// keyMsg builds a key press the way Bubble Tea v2 delivers one.
func keyMsg(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Text: s, Code: rune(s[0])})
}

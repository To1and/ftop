package config

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// First value wins, multi-value keywords accumulate, negation vetoes, and Host
// patterns are case-sensitive, as in ssh.
func TestEvaluationSemantics(t *testing.T) {
	sc, err := readSSHConfig(writeTemp(t, `
Port 2000
Host GPU-*
    Port 2100
    IdentityFile ~/.ssh/a
Host * !bastion
    Port 2200
    IdentityFile ~/.ssh/b
    User everyone
Host bastion
    User admin
`))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := sc.eval("GPU-1")
	if v.port != "2000" || len(v.opts["identityfile"]) != 2 || v.user != "everyone" {
		t.Errorf("GPU-1: port %s, identities %v, user %s", v.port, v.opts["identityfile"], v.user)
	}
	if l, _ := sc.eval("gpu-1"); len(l.opts["identityfile"]) != 1 {
		t.Errorf("gpu-1 matched Host GPU-*: identities %v", l.opts["identityfile"])
	}
	b, _ := sc.eval("bastion")
	if b.user != "admin" {
		t.Errorf("negated pattern did not veto: user %s", b.user)
	}
	u, _ := sc.eval("ssh://root@gpu-2:2999")
	if u.user != "root" || u.port != "2999" || u.host != "gpu-2" {
		t.Errorf("destination user/port must win: %+v", u)
	}
	d, _ := sc.eval("ssh://first%40last@box:2400")
	if d.user != "first@last" {
		t.Errorf("URI user not percent-decoded: %q", d.user)
	}
}

// Glob classes and escapes keep working in `drop`; * spans '/'.
func TestGlobClasses(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"node-[12]", "node-1", true}, {"node-[12]", "node-3", false},
		{"node-[!12]", "node-3", true}, {"node-[a-c]", "node-b", true},
		{"*", "ssh://gpu@192.0.2.1", true}, {"*lab*", "ssh://u@LAB-1", true},
		{"node-?", "node-10", false}, {`a\*b`, "a*b", true}, {`a\*b`, "axb", false},
		{`node-[a\-c]`, "node--", true}, {`node-[a\-c]`, "node-b", false},
		{`node-[\]ab]`, "node-]", true}, {`node-[]ab]`, "node-]", true},
		{`node-[a-c]`, "node--", false}, {`x[\\]`, `x\`, true},
	}
	for _, c := range cases {
		if got := globMatch(c.pat, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pat, c.s, got, c.want)
		}
	}
}

func TestDestHost(t *testing.T) {
	cases := map[string]string{
		"host": "host", "u@host": "host", "ssh://u@host:2222": "host",
		"ssh://host/path": "host", "[fe80::1]:22": "fe80::1", "fe80::1": "fe80::1",
	}
	for in, want := range cases {
		if got := destHost(in); got != want {
			t.Errorf("destHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// A line of stars must not hang startup: the matcher is iterative.
func TestPathologicalPatternIsFast(t *testing.T) {
	ssh := "Host " + strings.Repeat("*", 200) + "z\n  User x\nHost node-1\n  HostName 192.0.2.9\n"
	done := make(chan struct{})
	go func() {
		Load(isolated(t, ssh, ""))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a pattern of stars hung startup")
	}
}

// Differential: the address ftop shows (and `drop` matches against) must agree
// with OpenSSH. `ssh -F file -G name` evaluates and never connects; none of these
// configs has a Match, whose conditions ftop leaves to ssh.
func TestReaderAgreesWithOpenSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh binary")
	}
	configs := []string{
		"Host Foo\n  HostName 192.0.2.1\nHost foo\n  HostName 192.0.2.2\n",
		"Host 'lab-q'\n  HostName 192.0.2.3\n  Port 2233\n",
		"Host x\n  HostName = 192.0.2.4\n  Port = 2222\n",
		"Host=y\n  HostName=192.0.2.5\n  Port\t2300\n",
		"\"Host\" z\n  \"HostName\" 192.0.2.6\n",
		"Port 2000\nHost * !b\n  Port 2100\n  User u1\nHost b\n  User u2\n",
		"Host t1 t2\n  HostName %h.example\n",
		"Host a\\ b c\n  HostName 192.0.2.7\n",
		"Host *1\n  User star\nHost ?2\n  User q\n",
		"Host w   # trailing comment\n  HostName 192.0.2.8 # comment\n",
		"Host *\n  HostName %h.example\n",
		"Host p1\n  Port 002222\nHost p2\n  Port +2222\n",
		"Host x\r\n  HostName 192.0.2.9\r\n",
		"Host *\n  User top\nHost x\n  User later\n",
		"Host x\n  User later\nHost *\n  User bottom\n",
	}
	dests := []string{"Foo", "foo", "FOO", "lab-q", "x", "y", "z", "b", "t1", "t2", "c", "a b", "n1", "n2",
		"w", "u@x", "ssh://v@y:2400", "GPU", "p1", "p2", "ssh://first%40last@x:2400", "ssh://dev%75ser@x"}
	compared := 0
	for ci, body := range configs {
		path := writeTemp(t, body)
		sc, err := readSSHConfig(path)
		if err != nil {
			t.Fatalf("config %d: %v", ci, err)
		}
		for _, d := range dests {
			out, err := exec.Command("ssh", "-F", path, "-G", "--", d).Output()
			if err != nil {
				continue // ssh rejects this destination or config; nothing to compare
			}
			want := map[string]string{}
			for _, line := range strings.Split(string(out), "\n") {
				if k, v, ok := strings.Cut(line, " "); ok && (k == "hostname" || k == "port" || k == "user") {
					want[k] = v
				}
			}
			v, err := sc.eval(d)
			if err != nil {
				t.Errorf("config %d dest %q: ftop failed (%v), ssh gives %v", ci, d, err, want)
				continue
			}
			compared++
			got := map[string]string{"hostname": v.host, "port": v.port, "user": v.user}
			for k, w := range want {
				if got[k] != w {
					t.Errorf("config %d dest %q: %s = %q, OpenSSH says %q", ci, d, k, got[k], w)
				}
			}
		}
	}
	if compared < 50 {
		t.Errorf("only %d destinations compared with OpenSSH", compared)
	}
}

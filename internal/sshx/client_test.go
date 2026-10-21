package sshx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDurArgIsAValidGoDuration pins the unit bug that made the remote probe run
// unthrottled. Durations must round-trip through Go's own parser.
func TestDurArgIsAValidGoDuration(t *testing.T) {
	for _, d := range []time.Duration{time.Second, 500 * time.Millisecond, 2500 * time.Millisecond, 10 * time.Second} {
		s := durArg(d)
		got, err := time.ParseDuration(s)
		if err != nil {
			t.Fatalf("durArg(%v) = %q, which Go cannot parse: %v", d, s, err)
		}
		if got != d {
			t.Errorf("durArg(%v) round-tripped to %v", d, got)
		}
	}
	if durArg(0) == "0s" {
		t.Error("a zero interval must fall back to a positive default, not pass 0 through")
	}
}

// TestProcIntervalIsSlowerThanGauges: the process walk costs ~8x the rest of the
// fields, so it must never be polled at the gauge rate.
func TestProcIntervalIsSlowerThanGauges(t *testing.T) {
	c := &Client{Interval: time.Second}
	if got := c.procInterval(); got <= c.Interval {
		t.Errorf("procInterval = %v, want slower than the %v gauge interval", got, c.Interval)
	}
}

// TestParseGpuKeepsUnknownAsNil: "-" means the driver refused the query, which is a
// different fact from a reported zero.
func TestParseGpuKeepsUnknownAsNil(t *testing.T) {
	f := []string{"G", "0", "GPU-abc", "NVIDIA H200", "-", "-", "-", "-", "-", "-", "-", "-", "-", "-", "0"}
	g, ok := parseGpu(f)
	if !ok {
		t.Fatal("parseGpu rejected a valid record")
	}
	if g.Util != nil || g.MemUsed != nil || g.MemTotal != nil {
		t.Error("unknown gauges were decoded as numbers")
	}
	if g.UtilPct() >= 0 || g.MemPct() >= 0 {
		t.Error("unknown gauges must report a negative percentage")
	}
}

// TestParseGpuKeepsZero: an honest zero must survive as zero.
func TestParseGpuKeepsZero(t *testing.T) {
	f := []string{"G", "0", "GPU-abc", "NVIDIA H200", "0", "0", "143771", "-", "-", "-", "-", "-", "-", "-", "0"}
	g, _ := parseGpu(f)
	if g.UtilPct() != 0 {
		t.Error("a reported 0%% was not preserved")
	}
	if g.MemPct() != 0 {
		t.Error("used=0 should be 0% of total")
	}
}

// TestParseGpuRejectsNonFinite: ParseFloat accepts "NaN" and "Inf"; neither is a
// reading, and NaN slips past every "< 0 means unknown" check downstream.
func TestParseGpuRejectsNonFinite(t *testing.T) {
	for _, v := range []string{"NaN", "nan", "Inf", "-Inf", "+Inf"} {
		f := []string{"G", "0", "GPU-abc", "NVIDIA H200", v, "0", "143771", v, v, "-", "-", "-", "-", "-", "0"}
		g, ok := parseGpu(f)
		if !ok {
			t.Fatalf("%s: parseGpu rejected the record", v)
		}
		if g.Util != nil || g.UtilPct() >= 0 {
			t.Errorf("%s utilisation decoded as a reading", v)
		}
		if g.Temp != nil || g.Power != nil {
			t.Errorf("%s temperature or power decoded as a reading", v)
		}
	}
}

// TestParseHeaderToleratesShortRecords: a probe build that stops emitting a field
// must not shift every other field into the wrong slot.
func TestParseHeaderToleratesShortRecords(t *testing.T) {
	s := parseHeader([]string{"H", "node-1"}, "fallback")
	if s.Host != "node-1" {
		t.Errorf("host = %q", s.Host)
	}
	if s.Cores != 0 {
		t.Errorf("cores = %d, want 0 for a short record", s.Cores)
	}
	// A probe from before the sampling flag existed says nothing about processes;
	// the reader must not read that silence as "sampled and empty".
	if s.ProcsSampled {
		t.Error("a short header must not claim processes were sampled")
	}
}

// TestProcsSampledBitIsParsed: the flag is what separates "not sampled" from
// "sampled and empty". Reading it backwards makes every finished job persist.
func TestProcsSampledBitIsParsed(t *testing.T) {
	on := parseHeader([]string{"H", "node-1", "8", "1", "0.5", "1", "1", "1"}, "x")
	if !on.ProcsSampled {
		t.Error("trailing 1 must mean processes were sampled")
	}
	off := parseHeader([]string{"H", "node-1", "8", "1", "0.5", "1", "1", "0"}, "x")
	if off.ProcsSampled {
		t.Error("trailing 0 must mean processes were not sampled")
	}
}

// A host name starting with "-" must reach ssh as the destination, never as an
// option: "--" separates the two.
func TestHostIsNeverParsedAsAnOption(t *testing.T) {
	c := &Client{Host: "-oProxyCommand=touch /tmp/x"}
	args := c.cmdArgs("true")
	for i, a := range args {
		if a == "--" {
			if args[i+1] != c.Host {
				t.Fatalf("destination after -- is %q, want the alias", args[i+1])
			}
			return
		}
		if a == c.Host {
			t.Fatalf("the alias reached ssh before --: %q", args)
		}
	}
	t.Fatal("no -- in the argument list")
}

// The connection is `ssh <alias>`: the user's ssh configuration applies in full,
// so a proxy, an Include or a Match works exactly as it does for their own ssh.
// ftop passes -F only when config.toml names a file, and adds only the options a
// host needs beyond that configuration.
func TestConnectionUsesTheUsersSSHConfig(t *testing.T) {
	plain := (&Client{Host: "gpu"}).cmdArgs("true")
	for _, a := range plain {
		if a == "-F" || a == "/dev/null" {
			t.Fatalf("the user's ssh_config is bypassed: %q", plain)
		}
	}
	if plain[len(plain)-2] != "gpu" {
		t.Errorf("destination = %q, want the alias", plain[len(plain)-2])
	}
	named := (&Client{Host: "gpu", SSHConfig: "/etc/alt", Options: []string{"HostName=192.0.2.7"}}).cmdArgs("true")
	joined := strings.Join(named, " ")
	if !strings.HasPrefix(joined, "-F /etc/alt ") || !strings.Contains(joined, "-o HostName=192.0.2.7") {
		t.Errorf("args = %q", joined)
	}
}

// Control sockets live only in a directory this process created.
func TestControlSocketsArePrivateToTheRun(t *testing.T) {
	controlBase = t.TempDir()
	c := &Client{Host: "gpu"}
	joined := strings.Join(c.cmdArgs("true"), " ")
	dir := controlDir()
	if dir == "" || !strings.Contains(joined, "ControlPath="+dir+"/%C") {
		t.Fatalf("control path not private to the run: %s", joined)
	}
	if strings.Contains(joined, ".ssh/ftop-%C") || strings.Contains(joined, ".ssh/ftop2-%C") {
		t.Errorf("a shared control path is still used: %s", joined)
	}
	Cleanup()
}

// TestInstallScriptIsSafeOnASharedHome runs the remote install command locally, from
// many concurrent "machines" sharing one home directory, the way a cluster's network
// home is shared. Every install must succeed, the probe must be intact, an existing
// probe must never be replaced, and no temp file may be left behind.
func TestInstallScriptIsSafeOnASharedHome(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	home := t.TempDir()
	payload := bytes.Repeat([]byte("ftop-probe-test-payload\n"), 20000) // ~480 KB
	name := "probe-0123456789ab"
	script := installScript(name)
	dest := filepath.Join(home, ".cache", "ftop", name)

	run := func() error {
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = bytes.NewReader(payload)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return nil
	}
	// One install first, so the concurrent ones race against an existing probe that
	// a reader is using — the case that failed.
	if err := run(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	readerErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				readerErr <- nil
				return
			default:
			}
			if _, err := os.Stat(dest); err != nil {
				readerErr <- fmt.Errorf("the probe vanished during a concurrent install: %v", err)
				return
			}
		}
	}()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- run() }()
	}
	wg.Wait()
	close(stop)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a concurrent install failed: %v", err)
		}
	}
	if err := <-readerErr; err != nil {
		t.Error(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("probe is not intact after concurrent installs (err %v)", err)
	}
	after, _ := os.Stat(dest)
	if !os.SameFile(before, after) {
		t.Error("an existing probe was replaced; another machine may have been about to execute it")
	}
	left, _ := filepath.Glob(filepath.Join(home, ".cache", "ftop", "."+name+".*"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// TestCheckTellsUnsupportedFromUnreachable: the cache check's outcome is read
// into "cached", "upload", "this machine cannot run the probe" and "could not
// reach it", and only the third is ErrUnsupported. A Windows host is recognised
// by its shell's refusal of the POSIX check, which used to read as a cache miss
// followed by a failed upload.
func TestCheckTellsUnsupportedFromUnreachable(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	exit := func(code int) error {
		return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	}
	cases := []struct {
		name           string
		err            error
		stdout, stderr string
		upload         bool
		unsupported    bool
		failed         bool
		mention        string
	}{
		{name: "cached", err: nil},
		{name: "not cached", err: exit(missCode), upload: true},
		// The probe shares the check's command line now; its own failure is not a miss.
		{name: "probe failed", err: exit(1), stderr: "ftop-probe: nvml: driver not loaded", failed: true, mention: "nvml"},
		{name: "macOS", err: exit(3), stdout: "Darwin arm64\n", unsupported: true, failed: true, mention: "Darwin arm64"},
		{name: "no uname", err: exit(3), unsupported: true, failed: true, mention: "unknown"},
		{name: "cmd.exe", err: exit(49), stderr: "'p' is not recognized as an internal or external command,\r\noperable program or batch file.\r\n",
			unsupported: true, failed: true, mention: "Windows"},
		{name: "PowerShell", err: exit(1), stderr: "p=... : The term 'p=...' is not recognized as the name of a cmdlet, function, script file, or operable program.",
			unsupported: true, failed: true, mention: "Windows"},
		// An ssh warning beside a cache miss is still a cache miss.
		{name: "miss with a warning", err: exit(missCode), stderr: "Warning: Permanently added 'lab-gpu-1' (ED25519) to the list of known hosts.", upload: true},
		{name: "unreachable", err: exit(255), stderr: "ssh: connect to host 192.0.2.10 port 22: Connection refused", failed: true, mention: "refused"},
		// ssh's own failure is never read as a platform, whatever it printed.
		{name: "unreachable, odd text", err: exit(255), stderr: "x is not recognized as an internal or external command", failed: true},
	}
	for _, c := range cases {
		upload, err := classifyCheck(c.err, c.stdout, c.stderr)
		if upload != c.upload {
			t.Errorf("%s: upload = %v, want %v", c.name, upload, c.upload)
		}
		if (err != nil) != c.failed {
			t.Errorf("%s: err = %v, want failure %v", c.name, err, c.failed)
		}
		if got := errors.Is(err, ErrUnsupported); got != c.unsupported {
			t.Errorf("%s: unsupported = %v, want %v (err %v)", c.name, got, c.unsupported, err)
		}
		if c.mention != "" && (err == nil || !strings.Contains(err.Error(), c.mention)) {
			t.Errorf("%s: %v does not say %q", c.name, err, c.mention)
		}
	}
}

// TestParseGpuReadsTheReservation: field 15 is the driver's reservation, kept apart
// from used; a record without it (an older probe) leaves it unknown, not zero.
func TestParseGpuReadsTheReservation(t *testing.T) {
	f := []string{"G", "0", "GPU-abc", "NVIDIA GeForce RTX 4090", "0", "2", "24564",
		"-", "-", "-", "-", "-", "-", "", "0", "457"}
	g, _ := parseGpu(f)
	if g.MemUsed == nil || *g.MemUsed != 2 {
		t.Errorf("used = %v, want 2", g.MemUsed)
	}
	if g.MemReserved == nil || *g.MemReserved != 457 {
		t.Errorf("reserved = %v, want 457", g.MemReserved)
	}
	old, _ := parseGpu(f[:15])
	if old.MemReserved != nil {
		t.Errorf("a record without field 15 decoded a reservation: %d", *old.MemReserved)
	}
	refused, _ := parseGpu(append(f[:15:15], "-"))
	if refused.MemReserved != nil {
		t.Error(`"-" decoded as a reservation`)
	}
}

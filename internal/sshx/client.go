// Package sshx owns one streaming connection per host.
package sshx

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ftop/internal/wire"
)

// Client streams samples from one remote host.
//
// ONE long-lived ssh per host, not one per sample. Measured over ssh: process
// start plus handshake costs ~628 ms (~209 ms with ControlMaster) while the actual
// per-round-trip work is ~1 ms, so forking per sample would spend 99.8% of each
// second waiting for a handshake that has nothing to do with the data.
type Client struct {
	Host     string
	Probe    []byte
	Interval time.Duration
	// SSHConfig is passed to ssh with -F when set; empty means ssh's own default.
	// The connection is `ssh <alias>`, so the user's configuration — proxies,
	// Include, Match, keys — applies exactly as it does to their own ssh.
	SSHConfig string
	// Options are extra -o options for this host (a config.toml hostname override).
	Options []string
}

// SampleMsg carries one parsed sample to the UI.
type SampleMsg struct {
	Host   string
	Sample wire.Sample
}

// ErrMsg reports a connection-level failure.
type ErrMsg struct {
	Host string
	Err  string
	// Unsupported says the host is reachable but the probe cannot run on it
	// (ErrUnsupported). It is a field rather than a reading of Err so the board's
	// choice to hide such hosts does not depend on the message's wording.
	Unsupported bool
}

// durArg formats a duration the way Go's flag package parses it.
//
// Writing the millisecond count with a "g" suffix produced "1000g", which is not a
// Go duration: flag parsing set the interval to zero, returned an error, and — because
// the probe used ContinueOnError with a package-level Parse() — silently ran with no
// throttle at all.
func durArg(d time.Duration) string {
	if d <= 0 {
		d = time.Second
	}
	return d.String()
}

// procInterval is how often the expensive process walk runs, relative to gauges.
func (c *Client) procInterval() time.Duration {
	d := 5 * c.Interval
	if d < time.Second {
		d = time.Second
	}
	return d
}

// sshArgs are the connection options shared by the cache check, the upload and the
// stream. They live in one place so the three cannot drift apart.
//
// Every call puts "--" between these and the host: a host name beginning with
// "-" (say "-oProxyCommand=…") would otherwise be parsed as an ssh option and run
// a local command.
func (c *Client) sshArgs() []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=6",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "LogLevel=ERROR",
	}
	// Multiplexing makes a reconnect ~3x cheaper. The sockets live in a
	// directory created by this process and persist for seconds, so ftop never
	// rides on a master it did not open: a long-lived master of the user's own
	// can outlive the network path behind it, and a dead one fails every
	// connection made through it.
	if dir := controlDir(); dir != "" {
		return append(args,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+filepath.Join(dir, "%C"),
			"-o", "ControlPersist=10s")
	}
	return append(args, "-o", "ControlMaster=no", "-o", "ControlPath=none")
}

var (
	ctlOnce sync.Once
	ctlDir  string
	// controlBase is where the per-run socket directory is made; ~/.ssh keeps
	// the socket path well under the Unix limit (104 bytes on macOS), which a
	// $TMPDIR path does not.
	controlBase = ""
)

func controlDir() string {
	ctlOnce.Do(func() {
		base := controlBase
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return
			}
			base = filepath.Join(home, ".ssh")
		}
		d, err := os.MkdirTemp(base, "ftop-")
		if err != nil || os.Chmod(d, 0o700) != nil {
			return
		}
		ctlDir = d
	})
	return ctlDir
}

// Cleanup removes this run's control-socket directory.
func Cleanup() {
	if ctlDir != "" {
		os.RemoveAll(ctlDir)
	}
}

// cmdArgs is the full ssh argument list for running remote on the host: the
// user's ssh configuration (or the one config.toml names), ftop's own options,
// any per-host options, then "--" and the alias.
func (c *Client) cmdArgs(remote string) []string {
	var args []string
	if c.SSHConfig != "" {
		args = append(args, "-F", c.SSHConfig)
	}
	args = append(args, c.sshArgs()...)
	for _, o := range c.Options {
		args = append(args, "-o", o)
	}
	return append(args, "--", c.Host, remote)
}

// ProbeName is the remote filename, derived from the binary's own hash so a new
// build lands beside the old one instead of overwriting a running copy.
func (c *Client) ProbeName() string {
	sum := sha256.Sum256(c.Probe)
	return "probe-" + hex.EncodeToString(sum[:6])
}

// Run streams until ctx ends. It invokes emit per sample and fail on errors.
//
// Reconnection is deliberately NOT handled here: the caller owns backoff so one
// flaky host cannot make the others thread through a retry policy that does not fit
// them.
func (c *Client) Run(ctx context.Context, emit func(SampleMsg), fail func(ErrMsg)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Upload and launch are two separate remote commands, because the old
	// one-liner could not tell a cache hit from a miss and raced on a shared
	// temporary name.
	//
	// The one-liner wrote every upload to "$f.part": several hosts uploading at
	// once is fine (each has its own home), but a host whose connection retried
	// while another writer still held the same path could see the file renamed
	// out from under it — "mv: cannot stat ... .part", which is exactly what a
	// fresh fleet showed. A unique temp name plus an atomic rename removes the
	// race, and checking the cache first means a hit transfers no bytes at all.
	remotePath, err := c.ensureProbe(ctx)
	if err != nil {
		if ctx.Err() == nil {
			fail(ErrMsg{Host: c.Host, Err: err.Error(), Unsupported: errors.Is(err, ErrUnsupported)})
		}
		return err
	}

	// 3. Stream. Stdin is /dev/null from here on: the probe reads nothing, and the
	// old code kept feeding 3 MB into a process that never consumed it.
	remote := fmt.Sprintf(`exec %s --interval=%s --procs=%s`,
		remotePath, durArg(c.Interval), durArg(c.procInterval()))

	args := c.cmdArgs(remote)
	cmd := exec.CommandContext(ctx, "ssh", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		fail(ErrMsg{Host: c.Host, Err: friendly(err.Error())})
		return err
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 128<<10), 1<<20)

	var cur wire.Sample
	var gpus []wire.Gpu
	got := false
	// The process walk runs on a slower cadence than the gauges, so most rounds carry
	// no P records. Carrying the previous list forward keeps a running job from
	// blinking out once every few seconds; Header.ProcsSampled is what says whether
	// that carry-over is "not sampled yet" or "sampled and empty".
	lastProcs := map[int][]wire.Proc{}
	var hostProcs, lastHostProcs []wire.HostProc
	var lastCPU uint64
	var lastAt time.Time

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		switch f[0] {
		case wire.KindHeader:
			cur = parseHeader(f, c.Host)
			if !got {
				got = true
			}
		case wire.KindGpu:
			if g, ok := parseGpu(f); ok {
				gpus = append(gpus, g)
			}
		case wire.KindProcsKnown:
			if idx, err := strconv.Atoi(field(f, 1)); err == nil {
				for i := range gpus {
					if gpus[i].Index == idx {
						gpus[i].ProcsKnown = true
						break
					}
				}
			}
		case wire.KindProc:
			gpus = attachProc(gpus, f)
		case wire.KindHostProc:
			if hp, ok := parseHostProc(f); ok {
				hostProcs = append(hostProcs, hp)
			}
		case wire.KindEnd:
			// The header says whether this round walked processes at all. Without
			// that bit the reader only knows "no P records arrived", which is true
			// both for "not sampled" and for "sampled, nothing running" — and it
			// resolved the ambiguity by carrying the old list forever, so a job
			// that exited stayed on the card until the stream restarted.
			if cur.ProcsSampled {
				for i := range gpus {
					// A sampled round replaces the list outright; a card whose
					// enumeration was refused keeps ProcsKnown=false and its list
					// unknown rather than being reported as empty.
					lastProcs[gpus[i].Index] = gpus[i].Procs
				}
			} else {
				for i := range gpus {
					// Not sampled: carry the last list, and say so. A ledger must
					// treat this as "unknown", never as "empty".
					gpus[i].Procs = lastProcs[gpus[i].Index]
					gpus[i].ProcsStale = len(gpus[i].Procs) > 0
					gpus[i].ProcsKnown = false
				}
			}
			cur.Gpus = gpus
			// Host processes follow the same rule as the cards' lists: a sampled
			// round replaces them, an unsampled one carries the last walk and says
			// it is old.
			if cur.ProcsSampled {
				lastHostProcs = hostProcs
				cur.HostProcs = hostProcs
			} else {
				cur.HostProcs, cur.HostProcsStale = lastHostProcs, len(lastHostProcs) > 0
			}
			hostProcs = nil
			now := time.Now()
			cur.At = now
			// Difference the cumulative counter. The first sample has no baseline, so
			// CPU stays unknown rather than claiming the host is idle.
			if lastAt.IsZero() {
				cur.CPUPct = nil
			} else if dt := now.Sub(lastAt).Seconds(); dt > 0 && cur.CPUUs >= lastCPU {
				cores := float64(cur.Cores)
				if cores < 1 {
					cores = 1
				}
				pct := 100 * (float64(cur.CPUUs-lastCPU) / 1e6) / dt / cores
				if pct < 0 {
					pct = 0
				}
				if pct > 100 {
					pct = 100
				}
				cur.CPUPct = &pct
			}
			lastCPU, lastAt = cur.CPUUs, now
			emit(SampleMsg{Host: c.Host, Sample: cur})
			gpus = nil
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	msg := strings.TrimSpace(stderr.String())
	if err != nil {
		if msg == "" {
			msg = friendly(err.Error())
		}
		fail(ErrMsg{Host: c.Host, Err: msg})
		return err
	}
	fail(ErrMsg{Host: c.Host, Err: "remote probe exited"})
	return fmt.Errorf("probe exited")
}

// friendly turns an ssh failure into something actionable.
func friendly(s string) string {
	switch {
	case strings.Contains(s, "timed out"), strings.Contains(s, "Timeout"):
		return "connection timed out"
	case strings.Contains(s, "refused"):
		return "connection refused"
	case strings.Contains(s, "resolve"), strings.Contains(s, "nodename"):
		return "host name does not resolve"
	case strings.Contains(s, "Permission denied"), strings.Contains(s, "authentication"):
		return "authentication failed"
	case strings.Contains(s, "No such file"):
		return "remote cannot write ~/.cache/ftop"
	}
	return s
}

func parseHeader(f []string, host string) wire.Sample {
	var s wire.Sample
	get := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	s.Host = get(1)
	if s.Host == "" {
		s.Host = host
	}
	s.Cores, _ = strconv.Atoi(get(2))
	s.CPUUs, _ = strconv.ParseUint(get(3), 10, 64)
	s.Load1, _ = strconv.ParseFloat(get(4), 64)
	s.MemKB, _ = strconv.ParseUint(get(5), 10, 64)
	s.TotalKB, _ = strconv.ParseUint(get(6), 10, 64)
	s.ProcsSampled = get(7) == "1"
	// The fields below are appended, so a stream from an older probe simply has
	// none of them: a zero bit means "the machine" for CPUQuota and "no swap" for
	// SwapKnown, which is exactly what an old probe's silence should mean.
	s.CPUQuota = get(8) == "1"
	s.Load5, _ = strconv.ParseFloat(get(9), 64)
	s.Load15, _ = strconv.ParseFloat(get(10), 64)
	s.CacheKB, _ = strconv.ParseUint(get(11), 10, 64)
	s.MemLimited = get(12) == "1"
	s.SwapKB, _ = strconv.ParseUint(get(13), 10, 64)
	s.SwapTotalKB, _ = strconv.ParseUint(get(14), 10, 64)
	s.SwapKnown = get(15) == "1"
	s.SwapLimited = get(16) == "1"
	if v, err := strconv.ParseFloat(get(17), 64); err == nil {
		s.Uptime = time.Duration(v * float64(time.Second))
	}
	s.Procs, _ = strconv.Atoi(get(18))
	s.Driver = get(19)
	return s
}

// field returns f[i], or "" when the record is shorter than expected.
func field(f []string, i int) string {
	if i < len(f) {
		return f[i]
	}
	return ""
}

func parseGpu(f []string) (wire.Gpu, bool) {
	if len(f) < 7 {
		return wire.Gpu{}, false
	}
	get := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	g := wire.Gpu{UUID: get(2), Name: get(3)}
	g.Index, _ = strconv.Atoi(get(1))
	g.Util = numPtr(get(4))
	g.MemUsed = u64Ptr(get(5))
	g.MemTotal = u64Ptr(get(6))
	g.Temp = numPtr(get(7))
	g.Power = numPtr(get(8))
	g.PowerCap = numPtr(get(9))
	g.ClockSM = numPtr(get(10))
	g.ClockMem = numPtr(get(11))
	g.Fan = numPtr(get(12))
	g.Throttle, _ = strconv.ParseUint(get(14), 10, 64)
	return g, true
}

// parseHostProc reads one R record: pid, rss KiB, cpu percent ("-" unknown),
// user, label.
func parseHostProc(f []string) (wire.HostProc, bool) {
	if len(f) < 6 {
		return wire.HostProc{}, false
	}
	pid, err := strconv.Atoi(f[1])
	if err != nil {
		return wire.HostProc{}, false
	}
	rss, _ := strconv.ParseUint(f[2], 10, 64)
	return wire.HostProc{PID: pid, RSSKB: rss, CPUPct: numPtr(f[3]), User: f[4], Name: f[5]}, true
}

func attachProc(gpus []wire.Gpu, f []string) []wire.Gpu {
	if len(f) < 6 {
		return gpus
	}
	idx, _ := strconv.Atoi(f[1])
	pid, _ := strconv.Atoi(f[2])
	mem, _ := strconv.ParseUint(f[3], 10, 64)
	p := wire.Proc{PID: pid, MemMB: mem, User: f[4], Name: f[5]}
	for i := range gpus {
		if gpus[i].Index == idx {
			gpus[i].Procs = append(gpus[i].Procs, p)
			break
		}
	}
	return gpus
}

// numPtr parses "-" as absent rather than zero. A non-finite number is absent
// too: ParseFloat accepts "NaN" and "Inf", and neither is a reading.
func numPtr(s string) *float64 {
	if s == "-" || s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// u64Ptr is numPtr for the counters, with the same absent-means-unknown rule.
func u64Ptr(s string) *uint64 {
	if s == "-" || s == "" {
		return nil
	}
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

var _ = os.Getenv

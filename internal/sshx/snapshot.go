package sshx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ftop/internal/wire"
)

// Snapshot runs one sample on the host and returns it.
//
// The stream path in Run() and this one share upload, launch and parsing; only the
// probe's flags and the loop differ. Keeping them on the same code is what stops a
// second command from growing its own way of contacting a machine.
func (c *Client) Snapshot(ctx context.Context, timeout time.Duration) (wire.Sample, error) {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	// The upload runs under the caller's context only: a new build's first contact
	// spends seconds uploading the probe, and charging that to the sample's timeout
	// reported a healthy host as unavailable once per upgrade.
	sampleCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The common case — this build is already on the host — is ONE ssh: the cache
	// check and the sample share a connection. It used to be two (check, then exec),
	// and a handshake costs far more than the probe: measured on a real fleet, the
	// probe's own work is 10–160 ms and each extra ssh 200–600 ms.
	remotePath := "$HOME/.cache/ftop/" + c.ProbeName()
	out, errb, err := c.capture(sampleCtx, checkScript(remotePath)+
		` && exec `+remotePath+` --once`)
	if err != nil && sampleCtx.Err() != nil {
		return wire.Sample{}, sampleCtx.Err()
	}
	if err != nil {
		// A failing probe (its own exit 1 or 2) and ssh's 255 both come back as
		// errors here; only missCode asks for an upload.
		if _, cerr := classifyCheck(err, out, errb); cerr != nil {
			return wire.Sample{}, cerr
		}
		if err := c.upload(ctx); err != nil {
			return wire.Sample{}, err
		}
		sampleCtx2, cancel2 := context.WithTimeout(ctx, timeout)
		defer cancel2()
		if out, err = c.run(sampleCtx2, `exec `+remotePath+` --once`); err != nil {
			return wire.Sample{}, err
		}
	}
	// --once makes the probe emit exactly one round with the process list included,
	// which is the only mode a ledger may act on: a round that skipped the process
	// walk says nothing about whether a card is busy.
	return parseSnapshot(out)
}

// missCode is the check's exit status for "this build is not cached here".
//
// Not 1: the check and the probe now share one command line, and the probe exits 1
// on its own failures. A probe error read as a cache miss would upload the probe
// again and fail the same way, reporting a broken probe as a network problem.
const missCode = 42

// checkScript is the remote command that answers three questions in one round
// trip: can the probe run here (exit 3 and the platform otherwise), is this build
// cached (exit missCode otherwise), and is the host reachable at all (ssh's 255).
//
// Every machine in the ssh configuration is managed, laptops and VPSes included, and
// the probe is a linux/amd64 binary — so a machine it cannot run on is told so in
// words, not as an upload or exec failure that reads like a network problem.
func checkScript(remotePath string) string {
	return `p="$(uname -sm 2>/dev/null)"; case "$p" in "Linux x86_64") ;; ` +
		`*) echo "$p"; exit 3 ;; esac; test -x ` + remotePath + ` || exit ` +
		strconv.Itoa(missCode)
}

// ensureProbe uploads the probe if the host does not already have this build. The
// stream path uses it; Snapshot folds the same check into its sample.
func (c *Client) ensureProbe(ctx context.Context) (string, error) {
	remotePath := "$HOME/.cache/ftop/" + c.ProbeName()
	out, errb, err := c.capture(ctx, checkScript(remotePath))
	if err != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	switch upload, err := classifyCheck(err, out, errb); {
	case err != nil:
		return "", err
	case !upload:
		return remotePath, nil
	}
	if err := c.upload(ctx); err != nil {
		return "", err
	}
	return remotePath, nil
}

// capture runs remote and returns its stdout, stderr and the raw error, so the
// caller can read the exit status.
func (c *Client) capture(ctx context.Context, remote string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "ssh", c.cmdArgs(remote)...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// upload installs this build's probe on the host.
func (c *Client) upload(ctx context.Context) error {
	upload := installScript(c.ProbeName())
	cmd := exec.CommandContext(ctx, "ssh", c.cmdArgs(upload)...)
	cmd.Stdin = bytes.NewReader(c.Probe)
	var why strings.Builder
	cmd.Stderr = &why
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.TrimSpace(why.String())
		if msg == "" {
			msg = friendly(err.Error())
		}
		return fmt.Errorf("probe upload failed: %s", msg)
	}
	return nil
}

// installScript is the remote command that stores the probe read from stdin as
// ~/.cache/ftop/<name>, exiting 0 only if that file is then executable.
//
// Several machines often share one home directory (a cluster's network home), so
// several ftop connections can install the same build into the same directory at
// the same moment. Two things follow, and both were seen on a real fleet:
//
//   - The temp file must be unique across machines, not only within one: `$$` is
//     a pid, and containers on different hosts hand out the same pids. mktemp
//     creates the file exclusively.
//   - An existing probe must never be replaced. The name is the content hash, so
//     an existing file is already the right one, while replacing it races another
//     machine that has just checked it and is about to execute it ("No such file
//     or directory"), and some network filesystems refuse to rename over a file
//     in use ("cannot overwrite ... File exists"). A hard link creates the name
//     only if it is absent; `mv -n` is the fallback where links are unsupported.
func installScript(name string) string {
	return `d="$HOME/.cache/ftop"; f="$d/` + name + `"; mkdir -p "$d" && ` +
		`t="$(mktemp "$d/.` + name + `.XXXXXX")" && cat > "$t" && chmod 755 "$t" && ` +
		`{ ln "$t" "$f" 2>/dev/null || [ -e "$f" ] || mv -n "$t" "$f"; }; ` +
		`rm -f "$t"; test -x "$f"`
}

// ErrUnsupported marks a host the probe cannot run on. It is a fact about the
// machine, not a failure to reach it, and the board treats the two differently:
// a GPU host that is down is news, a laptop in the same ssh config is not.
var ErrUnsupported = errors.New("unsupported platform")

// unsupported is the error for one such host, naming its platform.
type unsupported struct{ platform string }

func (e unsupported) Error() string {
	return "unsupported platform " + e.platform + ": the probe runs on Linux x86_64"
}

func (e unsupported) Is(target error) bool { return target == ErrUnsupported }

// classifyCheck reads the cache check's outcome: whether the probe must be
// uploaded, or the error that ends the attempt. A nil error and no upload is a
// cache hit.
//
// A Windows host never reaches the platform test: its shell is cmd.exe or
// PowerShell, which cannot parse the check at all and say so on stderr. That
// used to fall through as "not cached", and the upload that followed failed
// with a message about paths, so a Windows machine read as a broken Linux one.
// Only the shells' own words for an unknown command are taken as Windows: an ssh
// warning on stderr is not, and a host misread as unsupported would be hidden.
func classifyCheck(err error, stdout, stderr string) (upload bool, _ error) {
	if err == nil {
		return false, nil
	}
	code := exitCode(err)
	switch {
	case code == 3:
		platform := strings.TrimSpace(stdout)
		if platform == "" {
			platform = "unknown"
		}
		return false, unsupported{platform}
	case code != 255 && windowsShell(stdout+stderr):
		return false, unsupported{"Windows"}
	case code == missCode:
		return true, nil
	}
	// 255 is ssh's own failure (unreachable, refused, authentication).
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = friendly(err.Error())
	}
	return false, fmt.Errorf("%s", friendly(msg))
}

// windowsShell reports whether output is cmd.exe's or PowerShell's refusal of a
// POSIX command line.
func windowsShell(output string) bool {
	return strings.Contains(output, "is not recognized as an internal or external command") ||
		strings.Contains(output, "is not recognized as the name of a cmdlet")
}

// exitCode is the remote command's exit status, or -1 when there is none.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func (c *Client) run(ctx context.Context, remote string) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh", c.cmdArgs(remote)...)
	var out, stderr strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%s", msg)
		}
		return "", fmt.Errorf("%s", friendly(err.Error()))
	}
	return out.String(), nil
}

// parseSnapshot reads one complete round.
//
// A snapshot with no End record is a truncated transfer, not an empty fleet: returning
// what arrived would let a half-read host look like one with no cards.
func parseSnapshot(body string) (wire.Sample, error) {
	var (
		cur  wire.Sample
		gpus []wire.Gpu
		done bool
	)
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		switch f[0] {
		case wire.KindHeader:
			cur = parseHeader(f, "")
		case wire.KindGpu:
			if g, ok := parseGpu(f); ok {
				gpus = append(gpus, g)
			}
		case wire.KindProcsKnown:
			idx, _ := strconv.Atoi(field(f, 1))
			for i := range gpus {
				if gpus[i].Index == idx {
					gpus[i].ProcsKnown = true
					break
				}
			}
		case wire.KindProc:
			gpus = attachProc(gpus, f)
		case wire.KindEnd:
			done = true
		}
	}
	if err := sc.Err(); err != nil {
		return wire.Sample{}, err
	}
	if !done {
		return wire.Sample{}, fmt.Errorf("incomplete sample (no end record)")
	}
	cur.Gpus = gpus
	cur.At = time.Now()
	return cur, nil
}

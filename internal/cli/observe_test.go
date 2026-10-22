package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ftop/internal/ledger"
)

// fakeSSH puts an `ssh` on PATH that records its arguments and answers every command
// with one probe sample, and gives the build a probe so observation runs. It is how
// these tests watch which machines a command contacts without contacting any.
func fakeSSH(t *testing.T, sample string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "ssh.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\ncat <<'SAMPLE'\n" +
		sample + "\nSAMPLE\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := ProbeBin
	ProbeBin = []byte("synthetic probe")
	t.Cleanup(func() { ProbeBin = old })
	return log
}

// TestAPlacementObservesOnlyWhereItMayPlace: a manual host and an excluded host cannot
// receive the claim, so they are not asked. Asking them cost time and could fail the
// claim on a machine it was never going to use.
func TestAPlacementObservesOnlyWhereItMayPlace(t *testing.T) {
	store, cfg := placementFleet(t)
	log := fakeSSH(t, "H\th\t1\t0\t0\t0\t0\t1\nE")
	run(t, store, "claim", "-exclude-host", "lab-four", "-why", "x", "-for", "1h", "-as", "a",
		"-config", cfg)
	body := string(mustRead(t, log))
	if !strings.Contains(body, "lab-two") {
		t.Fatalf("the one placeable host was not observed:\n%s", body)
	}
	for _, h := range []string{"lab-four", "lab-desk"} {
		if strings.Contains(body, h) {
			t.Errorf("%s was observed although the claim could not use it:\n%s", h, body)
		}
	}
}

// TestAHostIsObservedEvenWhenAFilterHidesItsCards: -held keeps only claimed rows. A
// host whose only claimed card has been retired was sampled just now; the filter must
// not turn that into observed: false.
func TestAHostIsObservedEvenWhenAFilterHidesItsCards(t *testing.T) {
	cfg := isolatedSSH(t, "Host h1\n")
	store := seedStore(t)
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ledger.ClaimRequest{GPUUUID: "GPU-0", Owner: "a", Why: "x",
		For: time.Hour}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	// The host now reports only GPU-1: GPU-0, the held card, is retired.
	fakeSSH(t, "H\th1\t1\t0\t0\t0\t0\t1\nG\t1\tGPU-1\tRTX\t0\t0\t24564\nK\t1\nE")
	env, code := run(t, store, "gpu", "list", "-held", "-config", cfg)
	if code != ExitOK {
		t.Fatalf("list: %+v", env.Error)
	}
	var d gpuListData
	remarshal(t, env.Data, &d)
	if len(d.Hosts) != 1 || !d.Hosts[0].Observed {
		t.Fatalf("a host sampled just now is reported as %+v", d.Hosts)
	}
}

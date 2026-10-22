package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ftop/internal/ledger"
)

// TestElasticClaimSizesItselfToTheFleet: the sweep form, end to end. 4..40 over six
// idle cards takes all six, says so, and leaves a group that one release closes.
func TestElasticClaimSizesItselfToTheFleet(t *testing.T) {
	store, cfg := placementFleet(t)
	env, code := run(t, store, "claim", "-n", "2..40", "-spread", "-why", "sweep",
		"-for", "1h", "-as", "a", "-no-ssh", "-config", cfg)
	if code != ExitOK {
		t.Fatalf("claim -n 2..40: code=%d err=%+v", code, env.Error)
	}
	var data claimData
	remarshal(t, env.Data, &data)
	if len(data.Claims) != 6 {
		t.Fatalf("took %d cards, want the 6 idle ones", len(data.Claims))
	}
	if data.Requested == nil || data.Requested.Got != 6 || data.Requested.Max != 40 {
		t.Errorf("requested = %+v, want 2..40 got 6", data.Requested)
	}
	if !strings.Contains(data.Hint, "size the job for 6") {
		t.Errorf("hint does not tell the caller what it got: %q", data.Hint)
	}
	if _, code := run(t, store, "release", data.Group, "-as", "a"); code != ExitOK {
		t.Errorf("releasing the group: code=%d", code)
	}
}

// TestElasticClaimUnderItsMinimumClaimsNothing: "at least 2" is a real floor.
func TestElasticClaimUnderItsMinimumClaimsNothing(t *testing.T) {
	store, cfg := placementFleet(t)
	// One card held on the two-card host, and nothing else free.
	env, code := run(t, store, "claim", "-host", "lab-four", "-why", "x", "-for", "1h",
		"-as", "a", "-no-ssh", "-config", cfg)
	if code != ExitOK {
		t.Fatalf("first claim: %+v", env.Error)
	}
	env, code = run(t, store, "claim", "-n", "40..60", "-spread", "-why", "x", "-for", "1h",
		"-as", "a", "-no-ssh", "-config", cfg)
	if code != ExitHeld || env.Error == nil || env.Error.Code != CodeNoCapacity {
		t.Fatalf("claim 40..60 with 5 idle: code=%d err=%+v", code, env.Error)
	}
}

// TestExcludeHostKeepsAPlacementOff: the operator says "everything except that one",
// and the argument is checked before anything is claimed.
func TestExcludeHostKeepsAPlacementOff(t *testing.T) {
	store, cfg := placementFleet(t)
	env, code := run(t, store, "claim", "-n", "4..4", "-spread", "-exclude-host", "lab-four",
		"-exclude-host", "lab-two", "-why", "x", "-for", "1h", "-as", "a", "-no-ssh", "-config", cfg)
	// lab-desk is manual, so excluding the two others leaves nothing placeable.
	if code != ExitHeld || env.Error == nil || env.Error.Code != CodeNoCapacity {
		t.Fatalf("everything excluded: code=%d err=%+v", code, env.Error)
	}
	if _, code := run(t, store, "claim", "-host", "lab-two", "-exclude-host", "lab-four",
		"-why", "x", "-for", "1h", "-as", "a", "-no-ssh", "-config", cfg); code != ExitUsage {
		t.Errorf("-exclude-host with -host is not a usage error: code=%d", code)
	}
}

// TestKeepRoundTrip: a claim marked -keep reports it, can be flipped with `ftop keep`,
// and the flip belongs to the holder.
func TestKeepRoundTrip(t *testing.T) {
	store, cfg := placementFleet(t)
	env, code := run(t, store, "claim", "-keep", "-why", "staging", "-for", "4h", "-as", "a",
		"-no-ssh", "-config", cfg)
	if code != ExitOK {
		t.Fatalf("claim -keep: %+v", env.Error)
	}
	var data claimData
	remarshal(t, env.Data, &data)
	if !data.Claim.Keep {
		t.Fatal("the claim does not report keep")
	}
	if !strings.Contains(data.Hint, "kept while idle") {
		t.Errorf("hint does not mention keep: %q", data.Hint)
	}
	env, code = run(t, store, "keep", data.Group, "-off", "-as", "a")
	if code != ExitOK {
		t.Fatalf("keep -off: %+v", env.Error)
	}
	var rel releaseData
	remarshal(t, env.Data, &rel)
	if rel.Claims[0].Keep {
		t.Error("keep was not cleared")
	}
	if _, code := run(t, store, "keep", data.Group, "-as", "b"); code != ExitState {
		t.Errorf("another owner changed keep: code=%d", code)
	}
}

// TestMalformedCountIsAJSONUsageError: the flag package answers a failed Set by
// printing its own usage to stderr, and this CLI promises JSON on stdout and nothing
// else. A count that cannot be read is refused by the command instead.
func TestMalformedCountIsAJSONUsageError(t *testing.T) {
	store, cfg := placementFleet(t)
	for _, n := range []string{"abc", "2..abc", "0", "4..2"} {
		env, code := run(t, store, "claim", "-n", n, "-why", "x", "-for", "1h", "-as", "a",
			"-no-ssh", "-config", cfg)
		if code != ExitUsage {
			t.Errorf("-n %s: code=%d, want a usage error", n, code)
		}
		if env.Error == nil || env.Error.Code != CodeUsage {
			t.Errorf("-n %s: error = %+v", n, env.Error)
		}
	}
	if env, _ := run(t, store, "claim", "list"); !strings.Contains(fmt.Sprint(env.Data), "count:0") {
		t.Errorf("a refused count claimed a card: %v", env.Data)
	}
}

// TestKeepAGroupAfterARestart: keep follows the holder rule release and renew use, for
// a group as much as for one claim. It looked the group id up as a claim id, found
// nothing, and refused the holder's own successor.
func TestKeepAGroupAfterARestart(t *testing.T) {
	store := seedStore(t)
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	repo, branch := "", ""
	if id := DetectIdentity(); id.Repo != "" {
		repo, branch = id.Repo, id.Branch
	}
	a, err := s.Allocate(ledger.ClaimRequest{Host: "h1", Count: 2,
		Owner: "claude-code/aaaa1111", Why: "staging", For: time.Hour,
		Repo: repo, Branch: branch, Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "bbbb2222")
	t.Setenv("AI_AGENT", "")
	t.Setenv("FTOP_AGENT", "")
	t.Setenv("FTOP_SESSION", "")
	env, code := run(t, store, "keep", a.Group)
	if code != ExitOK {
		t.Fatalf("the restarted holder could not keep its group: code=%d err=%+v", code, env.Error)
	}
}

// TestAPlacementWithNowhereToGoDoesNotObserve: every placeable host manual or excluded
// is the caller's own decision, knowable without a network. It used to observe the
// whole fleet first — reporting no_observation for machines it was never going to use.
func TestAPlacementWithNowhereToGoDoesNotObserve(t *testing.T) {
	store, cfg := placementFleet(t)
	// No -no-ssh: this build has no probe, so any attempt to observe fails loudly.
	env, code := run(t, store, "claim", "-n", "1..4", "-exclude-host", "lab-four",
		"-exclude-host", "lab-two", "-why", "x", "-for", "1h", "-as", "a", "-config", cfg)
	if code != ExitHeld || env.Error == nil || env.Error.Code != CodeNoCapacity {
		t.Fatalf("code=%d err=%+v, want no_capacity without observing", code, env.Error)
	}
	if len(env.Unavailable) != 0 {
		t.Errorf("unavailable_hosts = %v for a claim that should not have looked", env.Unavailable)
	}
}

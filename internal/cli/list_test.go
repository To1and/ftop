package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ftop/internal/ledger"
)

// TestListDropsRetiredCards: a card row is never deleted, so a resized container's old
// card stayed in the fleet's summary forever and the counts answered "20 unknown" about
// cards that no longer exist.
func TestListDropsRetiredCards(t *testing.T) {
	store := seedStore(t)
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	// A third card that the host's next complete observation did not include.
	if err := s.UpsertCards([]ledger.Card{{Host: "h1", GPUUUID: "GPU-old", Index: 9,
		MemTotal: 24564}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCards([]ledger.Card{
		{Host: "h1", GPUUUID: "GPU-0", Index: 0, MemTotal: 24564},
		{Host: "h1", GPUUUID: "GPU-1", Index: 1, MemTotal: 24564},
	}, "h1"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	env, code := run(t, store, "gpu", "list", "-no-ssh")
	if code != ExitOK {
		t.Fatalf("gpu list: %+v", env.Error)
	}
	var data gpuListData
	remarshal(t, env.Data, &data)
	if data.Summary.Cards != 2 {
		t.Errorf("summary counts %d cards, want the 2 present ones", data.Summary.Cards)
	}
	for _, h := range data.Hosts {
		for _, c := range h.Cards {
			if c.GPUUUID == "GPU-old" {
				t.Error("a retired card is still listed as part of the fleet")
			}
		}
	}
}

// TestObservedIsAPropertyOfTheHost: a host whose first card was replaced reported
// observed: false although the cards it has now were measured seconds ago.
func TestObservedIsAPropertyOfTheHost(t *testing.T) {
	now := time.Now()
	views := []ledger.CardView{
		// The retired card sorts first — index 0 with an older UUID — and it has no
		// observation, because it is not there any more.
		{Card: ledger.Card{Host: "h1", GPUUUID: "GPU-retired", Index: 0}},
		{Card: ledger.Card{Host: "h1", GPUUUID: "GPU-0", Index: 0, Present: true}},
		{Card: ledger.Card{Host: "h1", GPUUUID: "GPU-1", Index: 1, Present: true}},
	}
	obs := map[string]ledger.Observation{"GPU-1": {At: now}}
	byHost, _ := groupByHost(views, obs, map[string]string{"h1": "1"})
	h := byHost["h1"]
	if h == nil || !h.Observed {
		t.Fatalf("a host measured seconds ago reports observed: false (%+v)", h)
	}
	if h.AgeSec > 5 {
		t.Errorf("age = %.1fs, want the newest observation", h.AgeSec)
	}
}

// TestCardsOnAMachineRemovedFromTheFleetAreRetired: taking a host out of ssh_config
// keeps its rows and its claims — that is what makes removing one safe — but its cards
// are not part of the fleet an agent chooses from, and counting them made the summary
// read "unknown: 12" about machines nobody could claim.
func TestCardsOnAMachineRemovedFromTheFleetAreRetired(t *testing.T) {
	cfg := isolatedSSH(t, "Host lab-gpu-1\n")
	store := filepath.Join(t.TempDir(), "ledger.db")
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCards([]ledger.Card{
		{Host: "lab-gpu-1", GPUUUID: "GPU-live", Index: 0, MemTotal: 24564},
		{Host: "lab-old-gpu-9", GPUUUID: "GPU-gone", Index: 0, MemTotal: 24564},
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	env, code := run(t, store, "gpu", "list", "-no-ssh", "-config", cfg)
	if code != ExitOK {
		t.Fatalf("gpu list: %+v", env.Error)
	}
	var data gpuListData
	remarshal(t, env.Data, &data)
	if data.Summary.Cards != 1 || data.Summary.Retired != 1 {
		t.Errorf("summary = %+v, want 1 card and 1 retired", data.Summary)
	}
	for _, h := range data.Hosts {
		if h.Host == "lab-old-gpu-9" {
			t.Error("a machine that left the fleet is still listed")
		}
	}
	// -host refuses the name, like any name the fleet does not contain: the machine
	// left on purpose, and an empty board would read as "it has no cards". Its history
	// is still reachable by card, through `gpu show <uuid>`.
	if env, _ := run(t, store, "gpu", "list", "-no-ssh", "-config", cfg, "-host",
		"lab-old-gpu-9"); env.Error == nil || env.Error.Code != CodeUsage {
		t.Errorf("naming a machine that left the fleet: %+v, want a usage error", env.Error)
	}
}

// TestARetiredCardWithAClaimIsShownOnce: a machine removed from the fleet while a claim
// was on it is the case an agent must still see — the claim is real, its holder is
// working on a card nobody can now observe. It is shown, counted once as held, and it
// does not inflate the fleet's free or unknown counts.
func TestARetiredCardWithAClaimIsShownOnce(t *testing.T) {
	cfg := isolatedSSH(t, "Host lab-gpu-1\n")
	store := filepath.Join(t.TempDir(), "ledger.db")
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCards([]ledger.Card{
		{Host: "lab-gpu-1", GPUUUID: "GPU-live", Index: 0, MemTotal: 24564},
		{Host: "lab-old-gpu-9", GPUUUID: "GPU-gone", Index: 0, MemTotal: 24564},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ledger.ClaimRequest{GPUUUID: "GPU-gone", Owner: "agent-a",
		Why: "work on a machine that was removed", For: time.Hour}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	env, code := run(t, store, "gpu", "list", "-no-ssh", "-config", cfg)
	if code != ExitOK {
		t.Fatalf("gpu list: %+v", env.Error)
	}
	var data gpuListData
	remarshal(t, env.Data, &data)
	if data.Summary.Held != 1 {
		t.Errorf("held = %d, want the retired card's claim counted exactly once: %+v",
			data.Summary.Held, data.Summary)
	}
	if data.Summary.Retired != 1 || data.Summary.Free != 1 {
		t.Errorf("summary = %+v, want 1 retired and 1 free", data.Summary)
	}
	var shown bool
	for _, h := range data.Hosts {
		for _, c := range h.Cards {
			if c.GPUUUID == "GPU-gone" {
				shown = true
			}
		}
	}
	if !shown {
		t.Error("a held card on a removed machine is not shown at all")
	}
}

// TestAHostWithNoCardsLeftListsAnEmptyArray: a host whose every card was filtered out
// as retired is "no cards", and the listing promises arrays, never null.
func TestAHostWithNoCardsLeftListsAnEmptyArray(t *testing.T) {
	store := seedStore(t)
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	// h1's next complete observation found no cards at all.
	if err := s.UpsertCards(nil, "h1"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var buf bytes.Buffer
	t.Setenv("FTOP_LEDGER", store)
	if code := dispatch(IO{Out: &buf, Err: &buf}, []string{"gpu", "list", "-no-ssh"}); code != ExitOK {
		t.Fatalf("gpu list: %s", buf.String())
	}
	if strings.Contains(buf.String(), `"cards": null`) {
		t.Errorf("a host's cards serialised as null:\n%s", buf.String())
	}
}

// TestADeliberatelyEmptyFleetRetiresEveryCard: `drop = ["*"]` is a decision — no
// machine is in the fleet — and every cached card is retired. It must not be confused
// with having no ssh_config at all, which says nothing about the machines.
func TestADeliberatelyEmptyFleetRetiresEveryCard(t *testing.T) {
	store, cfg := placementFleet(t)
	body := strings.Replace(string(mustRead(t, cfg)), "[general]\n", "[general]\ndrop = [\"*\"]\n", 1)
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env, code := run(t, store, "gpu", "list", "-no-ssh", "-config", cfg)
	if code != ExitOK {
		t.Fatalf("list: %+v", env.Error)
	}
	var d gpuListData
	remarshal(t, env.Data, &d)
	if d.Summary.Cards != 0 || d.Summary.Free != 0 || d.Summary.Retired != 7 {
		t.Fatalf("an empty fleet is listed as %+v, want 0 cards and 7 retired", d.Summary)
	}
}

// TestHeldRetiredCardIsNotAFleetCard: shown and counted as held, but not one of the
// fleet's cards.
func TestHeldRetiredCardIsNotAFleetCard(t *testing.T) {
	cfg := isolatedSSH(t, "Host lab-gpu-1\n")
	store := filepath.Join(t.TempDir(), "ledger.db")
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCards([]ledger.Card{
		{Host: "lab-gpu-1", GPUUUID: "GPU-live", Index: 0, MemTotal: 24564},
		{Host: "lab-gone", GPUUUID: "GPU-gone", Index: 0, MemTotal: 24564},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ledger.ClaimRequest{GPUUUID: "GPU-gone", Owner: "agent-a", Why: "x",
		For: time.Hour}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	env, _ := run(t, store, "gpu", "list", "-no-ssh", "-config", cfg)
	var d gpuListData
	remarshal(t, env.Data, &d)
	if d.Summary.Cards != 1 || d.Summary.Held != 1 || d.Summary.Free != 1 || d.Summary.Retired != 1 {
		t.Errorf("summary = %+v, want cards 1, held 1, free 1, retired 1", d.Summary)
	}
}

// TestACardOnARemovedMachineCannotBeNamed: -host refuses a machine that left the
// fleet, and -gpu is the same request by another route.
func TestACardOnARemovedMachineCannotBeNamed(t *testing.T) {
	cfg := isolatedSSH(t, "Host lab-live\n")
	store := filepath.Join(t.TempDir(), "ledger.db")
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCards([]ledger.Card{
		{Host: "lab-live", GPUUUID: "live", Index: 0},
		{Host: "lab-removed", GPUUUID: "retired", Index: 0},
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	env, code := run(t, store, "claim", "-gpu", "retired", "-no-ssh", "-config", cfg,
		"-why", "x", "-for", "1h", "-as", "a")
	if code != ExitState || env.Error == nil || env.Error.Code != CodeUnknownCard {
		t.Fatalf("claimed a card on a removed machine: code=%d err=%+v", code, env.Error)
	}
	if _, code := run(t, store, "claim", "-gpu", "live", "-no-ssh", "-config", cfg,
		"-why", "x", "-for", "1h", "-as", "a"); code != ExitOK {
		t.Errorf("a card on a live machine: code=%d", code)
	}
}

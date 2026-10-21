package ledger

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fleet seeds hosts with the given card counts and returns an observation that
// shows every card idle.
func fleet(t *testing.T, s *Store, hosts map[string]int) map[string]Observation {
	t.Helper()
	obs := map[string]Observation{}
	for h, n := range hosts {
		seed(t, s, h, n)
		for i := 0; i < n; i++ {
			obs[fmt.Sprintf("%s-gpu%d", h, i)] = Observation{ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564}
		}
	}
	return obs
}

func auto(n int, obs map[string]Observation) ClaimRequest {
	r := req("", "agent-a")
	r.Count, r.Observed, r.RequireIdle = n, obs, true
	return r
}

func hostsOf(a Allocation) map[string]int {
	out := map[string]int{}
	for _, c := range a.Claims {
		out[c.Card.Host]++
	}
	return out
}

// TestPlacementKeepsLargeMachinesWhole is the operator's own example: with an eight-,
// a four- and a two-card machine all idle, two cards go to the two-card machine and
// three to the four-card one — never into the eight.
func TestPlacementKeepsLargeMachinesWhole(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{{1, "two"}, {2, "two"}, {3, "four"}, {4, "four"}, {5, "eight"}, {8, "eight"}} {
		s := newStore(t)
		obs := fleet(t, s, map[string]int{"eight": 8, "four": 4, "two": 2})
		a, err := s.Allocate(auto(tc.n, obs))
		if err != nil {
			t.Fatalf("n=%d: %v", tc.n, err)
		}
		if got := hostsOf(a); len(got) != 1 || got[tc.want] != tc.n {
			t.Errorf("n=%d went to %v, want all on %s", tc.n, got, tc.want)
		}
		if a.Placement.Mode != "fit" || len(a.Placement.Candidates) == 0 {
			t.Errorf("n=%d: placement %+v does not explain itself", tc.n, a.Placement)
		}
	}
}

// TestPlacementFillsAHoleFirst: one card goes into a four-card machine that has one
// idle card left, not into an idle two-card machine — which stays whole for the next
// two-card job.
func TestPlacementFillsAHoleFirst(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"four": 4, "two": 2})
	for i := 0; i < 3; i++ {
		if _, err := s.Claim(req("four", "other")); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.Allocate(auto(1, obs))
	if err != nil {
		t.Fatal(err)
	}
	if a.Claims[0].Card.Host != "four" {
		t.Errorf("one card went to %s, want the last idle card on four", a.Claims[0].Card.Host)
	}
}

// TestPlacementPrefersAPartlyUsedHostOnATie: equal leftovers, so the host already in
// use is filled before a whole one is opened.
func TestPlacementPrefersAPartlyUsedHostOnATie(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"a": 4, "b": 2})
	// a: 2 of 4 held, 2 idle. b: 2 of 2 idle. Both leave 0 after two cards.
	for i := 0; i < 2; i++ {
		if _, err := s.Claim(req("a", "other")); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.Allocate(auto(2, obs))
	if err != nil {
		t.Fatal(err)
	}
	if got := hostsOf(a); got["a"] != 2 {
		t.Errorf("went to %v, want the partly used host a", got)
	}
}

// TestPlacementSpreadFillsSmallHostsFirst: independent one-card jobs land on the
// one-card hosts, then where the fewest cards are left.
func TestPlacementSpreadFillsSmallHostsFirst(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"one-a": 1, "one-b": 1, "four": 4, "eight": 8})
	r := auto(3, obs)
	r.Spread = true
	a, err := s.Allocate(r)
	if err != nil {
		t.Fatal(err)
	}
	got := hostsOf(a)
	if got["one-a"] != 1 || got["one-b"] != 1 || got["four"] != 1 {
		t.Errorf("spread went to %v, want one-a, one-b and four", got)
	}
	if !a.Placement.Spread || a.Claims[0].Claim.Group != a.Group {
		t.Errorf("placement %+v / group %q", a.Placement, a.Claims[0].Claim.Group)
	}
}

// TestPlacementIsAllOrNothing: when no host has enough idle cards the request fails
// whole, says how close the fleet came, and claims nothing.
func TestPlacementIsAllOrNothing(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"four": 4, "two": 2})
	_, err := s.Allocate(auto(5, obs))
	var ie *InsufficientError
	if !errors.As(err, &ie) || !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("err = %v, want an InsufficientError", err)
	}
	if ie.MostOnOneHost != 4 || ie.BestHost != "four" || ie.Idle != 6 {
		t.Errorf("error says %+v, want 4 on four and 6 idle", ie)
	}
	if n, _ := s.OpenCount(); n != 0 {
		t.Errorf("%d claims left behind by a failed request", n)
	}
	// Spread can take more than any one host has, but not more than the fleet.
	r := auto(7, obs)
	r.Spread = true
	if _, err := s.Allocate(r); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("spread of 7 over 6 idle: err = %v", err)
	}
	r.Count = 6
	if _, err := s.Allocate(r); err != nil {
		t.Fatalf("spread of 6 over 6 idle: %v", err)
	}
}

// TestPlacementOnlyCountsIdleCards: a card with processes, hidden memory or no
// readable process list is not idle, and placement must not count it.
func TestPlacementOnlyCountsIdleCards(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"two": 2, "four": 4})
	obs["two-gpu0"] = Observation{ProcsKnown: true, MemKnown: true, Procs: 1, MemTotalMiB: 24564}
	obs["four-gpu0"] = Observation{ProcsKnown: true, MemKnown: true, UnattributedMiB: 8000, MemTotalMiB: 24564}
	delete(obs, "four-gpu1")
	a, err := s.Allocate(auto(2, obs))
	if err != nil {
		t.Fatal(err)
	}
	if got := hostsOf(a); got["four"] != 2 {
		t.Fatalf("went to %v; two has one idle card, four has two", got)
	}
	for _, c := range a.Claims {
		if c.Card.GPUUUID != "four-gpu2" && c.Card.GPUUUID != "four-gpu3" {
			t.Errorf("took %s, which is not idle", c.Card.GPUUUID)
		}
	}
}

// TestPlacementStaysInsideItsHosts: hosts outside the list — the operator's manual
// ones — are never chosen automatically, even when they fit best.
func TestPlacementStaysInsideItsHosts(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"workstation": 1, "four": 4})
	r := auto(1, obs)
	r.Hosts = []string{"four"}
	a, err := s.Allocate(r)
	if err != nil {
		t.Fatal(err)
	}
	if a.Claims[0].Card.Host != "four" {
		t.Errorf("placed on %s, outside the allowed hosts", a.Claims[0].Card.Host)
	}
	r.Hosts = []string{}
	if _, err := s.Allocate(r); !errors.Is(err, ErrNoCapacity) {
		t.Errorf("an empty host list placed a card: err = %v", err)
	}
}

// TestNamedHostTakesSeveralCards: -host with a count takes the lowest idle indices,
// and too few is a capacity error naming what the host has.
func TestNamedHostTakesSeveralCards(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"h": 4})
	r := auto(3, obs)
	r.Host = "h"
	a, err := s.Allocate(r)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range a.Claims {
		if want := fmt.Sprintf("h-gpu%d", i); c.Card.GPUUUID != want {
			t.Errorf("card %d is %s, want %s", i, c.Card.GPUUUID, want)
		}
	}
	r.Count = 2
	_, err = s.Allocate(r)
	var ie *InsufficientError
	if !errors.As(err, &ie) || ie.Host != "h" || ie.Idle != 1 {
		t.Fatalf("err = %v, want h has 1 idle", err)
	}
}

// TestNamedCardMustBeIdle: naming a card used to skip the idle check, so an agent
// could take a card a job nobody claimed was running on. Adopt is the way to take
// your own.
func TestNamedCardMustBeIdle(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"h": 2})
	obs["h-gpu0"] = Observation{ProcsKnown: true, MemKnown: true, Procs: 2, MemTotalMiB: 24564}
	r := auto(1, obs)
	r.GPUUUID = "h-gpu0"
	_, err := s.Allocate(r)
	var ne *NotIdleError
	if !errors.As(err, &ne) || ne.Procs != 2 || !errors.Is(err, ErrNotIdle) {
		t.Fatalf("err = %v, want NotIdleError with 2 procs", err)
	}
	delete(obs, "h-gpu1")
	r.GPUUUID = "h-gpu1"
	if _, err := s.Allocate(r); !errors.As(err, &ne) || ne.Observed {
		t.Fatalf("an unobserved card: err = %v, want NotIdleError, not observed", err)
	}
	r.GPUUUID, r.Adopt = "h-gpu0", true
	if _, err := s.Allocate(r); err != nil {
		t.Fatalf("adopting one's own busy card: %v", err)
	}
}

// TestGroupReleasesAndRenewsTogether: one command's cards are let go of, and kept,
// in one step.
func TestGroupReleasesAndRenewsTogether(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"h": 4})
	a, err := s.Allocate(auto(3, obs))
	if err != nil {
		t.Fatal(err)
	}
	if !IsGroupID(a.Group) || IsGroupID(a.Claims[0].Claim.ID) {
		t.Fatalf("group %q / claim %q: ids must tell themselves apart", a.Group, a.Claims[0].Claim.ID)
	}
	if _, err := s.RenewGroup(a.Group, "someone-else", time.Hour); !errors.Is(err, ErrNotOwner) {
		t.Errorf("another owner renewed the group: %v", err)
	}
	renewed, err := s.RenewGroup(a.Group, "agent-a", 5*time.Hour)
	if err != nil || len(renewed) != 3 {
		t.Fatalf("renew: %v (%d claims)", err, len(renewed))
	}
	for _, c := range renewed {
		if time.Until(c.Expires) < 4*time.Hour {
			t.Errorf("%s still expires %v", c.ID, c.Expires)
		}
	}
	// One card let go of by its own id; the group takes the rest.
	if _, err := s.Release(a.Claims[0].Claim.ID, "agent-a", ""); err != nil {
		t.Fatal(err)
	}
	released, err := s.ReleaseGroup(a.Group, "agent-a", "", "agent-a")
	if err != nil || len(released) != 2 {
		t.Fatalf("release: %v (%d claims), want the 2 still open", err, len(released))
	}
	if n, _ := s.OpenCount(); n != 0 {
		t.Errorf("%d claims still open", n)
	}
	if _, err := s.ReleaseGroup(a.Group, "agent-a", "", "agent-a"); !errors.Is(err, ErrAlreadyClosed) {
		t.Errorf("second release: %v, want already closed", err)
	}
	if _, err := s.ReleaseGroup("G20990101T000000-00000000", "agent-a", "", "agent-a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown group: %v, want not found", err)
	}
}

// TestSweepKeepsACardWithHiddenUse: Sweep's idea of idle used to skip the
// unattributed-memory check that claim applies, so it could close an overdue claim on
// a card a job invisible to this host was still using.
func TestSweepKeepsACardWithHiddenUse(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	r := req("h", "agent-a")
	r.For = time.Millisecond
	res, err := s.Claim(r)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	released, kept, err := s.Sweep(map[string]Observation{
		res.Claim.GPUUUID: {ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564, UnattributedMiB: 9000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 || len(kept) != 1 {
		t.Fatalf("released=%d kept=%d, want 0/1", len(released), len(kept))
	}
}

// TestMigrationKeepsEarlierClaims: a v2 ledger opened by this build gains groups and
// keeps every claim, each still released by its own id.
func TestMigrationKeepsEarlierClaims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range append(append([]string{}, v1Schema...), v2Schema...) {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixNano()
	if _, err := db.Exec(`INSERT INTO cards (gpu_uuid, host, idx, first_seen, last_seen)
		VALUES ('GPU-0', 'h', 0, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO claims (id, host, gpu_uuid, owner, why, created_ns,
		updated_ns, expires_ns) VALUES ('Lold', 'h', 'GPU-0', 'agent-a', 'old', ?, ?, ?)`,
		now, now, now+int64(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening a v2 ledger: %v", err)
	}
	defer s.Close()
	c, err := s.Get("Lold")
	if err != nil || c.Group != "" || c.Why != "old" {
		t.Fatalf("old claim after migration: %+v, %v", c, err)
	}
	if _, err := s.Release("Lold", "agent-a", ""); err != nil {
		t.Fatalf("releasing an old claim by id: %v", err)
	}
}

// TestUnknownMemoryIsNotIdle: a card whose process list was read but whose memory
// was not used to count as idle, its unattributed memory a zero nobody measured.
func TestUnknownMemoryIsNotIdle(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"h": 2})
	obs["h-gpu0"] = Observation{ProcsKnown: true, MemTotalMiB: 24564}
	a, err := s.Allocate(auto(1, obs))
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Claims[0].Card.GPUUUID; got != "h-gpu1" {
		t.Errorf("took %s, whose memory was never read", got)
	}
	views := []CardView{{Card: Card{Host: "h", GPUUUID: "h-gpu0"}}}
	Annotate(views, obs)
	if !strings.Contains(views[0].Conflict, "memory") {
		t.Errorf("conflict = %q, want it to say memory is unknown", views[0].Conflict)
	}
}

// TestConcurrentOpensMigrateOnce: every agent opens the ledger at once after an
// upgrade. Migrating outside a transaction let two of them add the same column, and
// the loser failed.
func TestConcurrentOpensMigrateOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range append(append([]string{}, v1Schema...), v2Schema...) {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	errs := make(chan error, 12)
	for i := 0; i < cap(errs); i++ {
		go func() {
			s, err := Open(path)
			if err == nil {
				s.Close()
			}
			errs <- err
		}()
	}
	for i := 0; i < cap(errs); i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent open: %v", err)
		}
	}
}

// TestMemoryTieBreakUsesTheCardsTaken: the tie-break compared the smallest card on
// each host, but placement takes the lowest index, so a host whose small card came
// second gave away its large first card.
func TestMemoryTieBreakUsesTheCardsTaken(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertCards([]Card{
		{Host: "a", GPUUUID: "a0", Index: 0, MemTotal: 81920},
		{Host: "a", GPUUUID: "a1", Index: 1, MemTotal: 24564},
		{Host: "b", GPUUUID: "b0", Index: 0, MemTotal: 32768},
		{Host: "b", GPUUUID: "b1", Index: 1, MemTotal: 32768},
	}); err != nil {
		t.Fatal(err)
	}
	obs := map[string]Observation{}
	for _, u := range []string{"a0", "a1", "b0", "b1"} {
		obs[u] = Observation{ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564}
	}
	a, err := s.Allocate(auto(1, obs))
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Claims[0].Card.GPUUUID; got != "b0" {
		t.Errorf("took %s, want b0 rather than a's 80 GiB card", got)
	}
}

// TestTooSmallIsNotInUse: cards excluded only by -min-vram made an idle host look
// partly used, so it beat a host that really was in use, and the reason said so.
func TestTooSmallIsNotInUse(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertCards([]Card{
		{Host: "a", GPUUUID: "a0", Index: 0, MemTotal: 48000},
		{Host: "a", GPUUUID: "a1", Index: 1, MemTotal: 24000},
		{Host: "b", GPUUUID: "b0", Index: 0, MemTotal: 48000},
		{Host: "b", GPUUUID: "b1", Index: 1, MemTotal: 48000},
		{Host: "b", GPUUUID: "b2", Index: 2, MemTotal: 48000},
	}); err != nil {
		t.Fatal(err)
	}
	obs := map[string]Observation{}
	for _, u := range []string{"a0", "a1", "b0", "b1", "b2"} {
		obs[u] = Observation{ProcsKnown: true, MemKnown: true, MemTotalMiB: 48000}
	}
	obs["b1"] = Observation{ProcsKnown: true, MemKnown: true, Procs: 1, MemTotalMiB: 48000}
	obs["b2"] = obs["b1"]
	r := auto(1, obs)
	r.MinVRAM = 40000
	a, err := s.Allocate(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Claims[0].Card.Host; got != "b" {
		t.Errorf("went to %s, want b, the host already in use (%s)", got, a.Placement.Reason)
	}
}

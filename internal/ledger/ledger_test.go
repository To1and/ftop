package ledger

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seed(t *testing.T, s *Store, host string, n int) {
	t.Helper()
	cards := make([]Card, n)
	for i := range cards {
		cards[i] = Card{
			Host: host, GPUUUID: host + "-gpu" + string(rune('0'+i)),
			Index: i, Name: "RTX 4090", MemTotal: 24564,
		}
	}
	if err := s.UpsertCards(cards); err != nil {
		t.Fatal(err)
	}
}

func req(host, owner string) ClaimRequest {
	return ClaimRequest{
		Host: host, Owner: owner, Why: "training run",
		For: 2 * time.Hour,
	}
}

// TestClaimTakesOneCardAndRecordsWhy is the basic contract: an agent asks for a
// card, gets one, and the record says who and why.
func TestClaimTakesOneCardAndRecordsWhy(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 4)

	res, err := s.Claim(req("gpu-4", "claude-code"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Claim.GPUUUID != "gpu-4-gpu0" {
		t.Errorf("chose %q, want the lowest free index", res.Claim.GPUUUID)
	}
	if res.Claim.Why != "training run" {
		t.Errorf("why = %q", res.Claim.Why)
	}
	if !strings.HasPrefix(res.Claim.ID, "L") {
		t.Errorf("id = %q, want an L-prefixed id", res.Claim.ID)
	}
}

// TestSecondClaimGetsAnotherCard: two agents on the same host must not land on the
// same card. This is the property the whole ledger exists for.
func TestSecondClaimGetsAnotherCard(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 4)

	a, err := s.Claim(req("gpu-4", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Claim(req("gpu-4", "agent-b"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Claim.GPUUUID == b.Claim.GPUUUID {
		t.Fatalf("both agents got %s", a.Claim.GPUUUID)
	}
}

// TestConflictNamesTheHolder: a refused claim has to say who holds the card and why,
// because that is what the agent needs to either wait or escalate.
func TestConflictNamesTheHolder(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)

	first, err := s.Claim(req("gpu-4", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}

	// By explicit UUID: the error names the card's holder directly.
	byUUID := req("", "agent-b")
	byUUID.GPUUUID = first.Claim.GPUUUID
	_, err = s.Claim(byUUID)
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("err = %v, want ErrHeld", err)
	}
	var he *HeldError
	if !errors.As(err, &he) {
		t.Fatalf("err = %T, want *HeldError", err)
	}
	if he.Owner != "agent-a" || he.Why != "training run" {
		t.Errorf("conflict names %q / %q", he.Owner, he.Why)
	}

	// By host with nothing free: the error lists every holder, so the agent can pick
	// a different machine or ask the operator in one step.
	_, err = s.Claim(req("gpu-4", "agent-c"))
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("err = %v, want ErrHeld", err)
	}
	var nf *NoFreeCardError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %T, want *NoFreeCardError", err)
	}
	if len(nf.Holders) != 1 || nf.Holders[0].Owner != "agent-a" {
		t.Errorf("holders = %+v", nf.Holders)
	}
}

// TestConcurrentClaimsNeverDoubleBook: the unique index is the authority, not the
// SELECT. Two goroutines racing for the last card must produce exactly one winner.
func TestConcurrentClaimsNeverDoubleBook(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Claim(req("gpu-4", "agent")); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d claims succeeded on one card; want exactly 1", wins)
	}
}

// TestReleaseFreesTheCard: a released card is immediately claimable again.
func TestReleaseFreesTheCard(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)

	a, err := s.Claim(req("gpu-4", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Release(a.Claim.ID, "agent-a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(req("gpu-4", "agent-b")); err != nil {
		t.Fatalf("the card stayed held: %v", err)
	}
}

// TestOnlyTheOwnerMayRelease: an agent must not be able to close someone else's
// claim. Without this, "release" is a quieter --force.
func TestOnlyTheOwnerMayRelease(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)

	a, err := s.Claim(req("gpu-4", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Release(a.Claim.ID, "agent-b", ""); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("err = %v, want ErrNotOwner", err)
	}
}

// TestExpiryAloneDoesNotFreeTheCard: a deadline is not an eviction. The card stays
// held until an observation says it is idle, because the alternative is handing a
// running training job's card to someone else.
func TestExpiryAloneDoesNotFreeTheCard(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)

	r := req("gpu-4", "agent-a")
	r.For = time.Millisecond
	if _, err := s.Claim(r); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)

	// No observation at all: the sweep cannot tell, so it keeps the claim.
	released, kept, err := s.Sweep(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 || len(kept) != 1 {
		t.Fatalf("released=%d kept=%d, want 0/1", len(released), len(kept))
	}
	if _, err := s.Claim(req("gpu-4", "agent-b")); !errors.Is(err, ErrHeld) {
		t.Fatalf("err = %v, want ErrHeld", err)
	}
}

// TestSweepReleasesOnlyWhenObservedIdle: the one case where an overdue claim is
// closed automatically.
func TestSweepReleasesOnlyWhenObservedIdle(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)

	r := req("gpu-4", "agent-a")
	r.For = time.Millisecond
	res, err := s.Claim(r)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)

	// Enumerated and empty: genuinely idle.
	released, kept, err := s.Sweep(map[string]Observation{
		res.Claim.GPUUUID: {Procs: 0, ProcsKnown: true, MemKnown: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 1 || len(kept) != 0 {
		t.Fatalf("released=%d kept=%d, want 1/0", len(released), len(kept))
	}
}

// TestSweepKeepsWhenEnumerationFailed: "could not enumerate" must never be read as
// "nothing running". That confusion is how a live job's card gets handed away.
func TestSweepKeepsWhenEnumerationFailed(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)

	r := req("gpu-4", "agent-a")
	r.For = time.Millisecond
	res, err := s.Claim(r)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)

	released, kept, err := s.Sweep(map[string]Observation{
		res.Claim.GPUUUID: {Procs: 0, ProcsKnown: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 || len(kept) != 1 {
		t.Fatalf("released=%d kept=%d, want 0/1", len(released), len(kept))
	}
}

// TestOverdueIsVisible: an overdue claim must announce itself, because a silent
// overdue claim is how a fleet fills up with cards nobody can use.
func TestOverdueIsVisible(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)

	r := req("gpu-4", "agent-a")
	r.For = time.Millisecond
	if _, err := s.Claim(r); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)

	views, err := s.List(ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Claim == nil {
		t.Fatal("expected one held card")
	}
	if !views[0].Claim.Overdue() {
		t.Error("the claim should report itself overdue")
	}
}

// TestAnnotateIgnoresDriverOverhead: an idle card can still report memory with no
// compute process behind it — a desktop's graphics contexts (587 MiB measured on an
// idle 5090), or the reservation inside a v1-only driver's used figure (458 MiB on a
// 4090). Flagging that would put a conflict on every such idle card.
func TestAnnotateIgnoresDriverOverhead(t *testing.T) {
	for _, ob := range []Observation{
		{MemUsedMiB: 587, MemTotalMiB: 32607, UnattributedMiB: 587},
		{MemUsedMiB: 458, MemTotalMiB: 24564, UnattributedMiB: 458},
	} {
		ob.ProcsKnown, ob.MemKnown = true, true
		views := []CardView{{Card: Card{Host: "h", GPUUUID: "u", MemTotal: ob.MemTotalMiB}}}
		Annotate(views, map[string]Observation{"u": ob})
		if views[0].Conflict != "" {
			t.Errorf("idle card with %d MiB flagged: %q", ob.MemUsedMiB, views[0].Conflict)
		}
	}
}

// TestAnnotateFlagsRealUnattributedUse: memory well past the noise floor with no
// process on this host means something this host cannot see is using the card.
func TestAnnotateFlagsRealUnattributedUse(t *testing.T) {
	views := []CardView{{Card: Card{Host: "h", GPUUUID: "u", MemTotal: 24564}}}
	Annotate(views, map[string]Observation{
		"u": {Procs: 0, ProcsKnown: true, MemKnown: true, MemUsedMiB: 12000, MemTotalMiB: 24564, UnattributedMiB: 12000},
	})
	if views[0].Conflict == "" {
		t.Fatal("12 GiB unattributed on an idle card must be flagged")
	}
}

// TestAnnotateReportsUnclaimedWork: the ledger calls a card free, the probe sees a
// process. That disagreement is the signal an agent uses to decide whether the card
// is genuinely idle, so it must be stated rather than smoothed over.
func TestAnnotateReportsUnclaimedWork(t *testing.T) {
	views := []CardView{{Card: Card{Host: "h", GPUUUID: "u", MemTotal: 24564}}}
	Annotate(views, map[string]Observation{
		"u": {Procs: 2, ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564, UnattributedMiB: 8000},
	})
	if views[0].Conflict == "" {
		t.Fatal("a free card with running processes must be flagged")
	}
	if !strings.Contains(views[0].Conflict, "unclaimed") {
		t.Errorf("conflict = %q", views[0].Conflict)
	}
}

// TestAnnotateTreatsUnknownAsUnknown: an unavailable process list on a free card is
// not evidence of idleness.
func TestAnnotateTreatsUnknownAsUnknown(t *testing.T) {
	views := []CardView{{Card: Card{Host: "h", GPUUUID: "u"}}}
	Annotate(views, map[string]Observation{
		"u": {Procs: 0, ProcsKnown: false},
	})
	if views[0].Conflict == "" {
		t.Fatal("an unreadable card must not read as free")
	}
	if !strings.Contains(views[0].Conflict, "unavailable") {
		t.Errorf("conflict = %q", views[0].Conflict)
	}
}

// TestSanitizeStripsControlAndBidi: the reason field is written by an agent and read
// by other agents. Invisible direction overrides make "safe" render as "unsafe".
func TestSanitizeStripsControlAndBidi(t *testing.T) {
	got := sanitizeText("train\x00\x1b[31m model ‮evil\u009b\u0085", 200)
	if strings.ContainsRune(got, 0x1b) || strings.ContainsRune(got, 0x202e) || strings.ContainsRune(got, 0) {
		t.Fatalf("sanitize left control characters: %q", got)
	}
	// C1 is the ASCII control range's other half: U+009B is the C1 CSI, and some
	// terminals honour it exactly as they honour ESC [.
	for _, r := range got {
		if r >= 0x80 && r <= 0x9f {
			t.Fatalf("sanitize left a C1 control %U in %q", r, got)
		}
	}
	if !strings.Contains(got, "train") || !strings.Contains(got, "evil") {
		t.Errorf("sanitize removed visible text: %q", got)
	}
}

// TestWhyIsRequired: a claim with no reason is exactly the situation the user asked
// the ledger to prevent — a card nobody can explain.
func TestWhyIsRequired(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)
	r := req("gpu-4", "agent-a")
	r.Why = "   "
	if _, err := s.Claim(r); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

// TestForIsRequired: an open-ended claim is one nobody will ever notice is dead.
func TestForIsRequired(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)
	r := req("gpu-4", "agent-a")
	r.For = 0
	if _, err := s.Claim(r); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

// TestUnknownHostIsDistinctFromFull: "no such host" and "all cards held" need
// different next actions from the agent.
func TestUnknownHostIsDistinctFromFull(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)

	if _, err := s.Claim(req("nope", "a")); !errors.Is(err, ErrNoSuchCard) {
		t.Fatalf("unknown host: err = %v", err)
	}
	if _, err := s.Claim(req("gpu-4", "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(req("gpu-4", "b")); !errors.Is(err, ErrHeld) {
		t.Fatalf("full host: err = %v, want ErrHeld", err)
	}
}

// TestMinVRAMSkipsSmallCards: asking for a card with enough memory must not hand
// back a smaller one.
func TestMinVRAMSkipsSmallCards(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertCards([]Card{
		{Host: "h", GPUUUID: "small", Index: 0, MemTotal: 8000},
		{Host: "h", GPUUUID: "big", Index: 1, MemTotal: 80000},
	}); err != nil {
		t.Fatal(err)
	}
	r := req("h", "a")
	r.MinVRAM = 40000
	res, err := s.Claim(r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Claim.GPUUUID != "big" {
		t.Errorf("chose %q, want the large card", res.Claim.GPUUUID)
	}
}

// TestHistorySurvivesRelease: the record of who had a card outlives the claim.
func TestHistorySurvivesRelease(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)

	a, err := s.Claim(req("gpu-4", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Release(a.Claim.ID, "agent-a", ""); err != nil {
		t.Fatal(err)
	}
	ev, err := s.History(a.Claim.GPUUUID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range ev {
		kinds = append(kinds, e.Kind)
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "claim") || !strings.Contains(joined, "released") {
		t.Errorf("history = %v, want a claim and a release", kinds)
	}
}

// TestRenewExtendsTheDeadline: an agent that knows its job is long must be able to
// say so without releasing and re-claiming.
func TestRenewExtendsTheDeadline(t *testing.T) {
	s := newStore(t)
	seed(t, s, "gpu-4", 1)

	r := req("gpu-4", "agent-a")
	r.For = time.Minute
	a, err := s.Claim(r)
	if err != nil {
		t.Fatal(err)
	}
	before := a.Claim.Expires
	b, err := s.Renew(a.Claim.ID, "agent-a", 4*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Expires.After(before.Add(3 * time.Hour)) {
		t.Errorf("expires = %v, want about 4h from now", b.Expires)
	}
}

// TestAbsentCardIsNotClaimable: a card that leaves its host's inventory must stop
// being offered. The row is kept for its history, so "gone" has to be recorded.
func TestAbsentCardIsNotClaimable(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertCards([]Card{
		{Host: "h", GPUUUID: "old", Index: 0, MemTotal: 24564},
		{Host: "h", GPUUUID: "new", Index: 0, MemTotal: 24564},
	}, "h"); err != nil {
		t.Fatal(err)
	}
	// A complete observation of h now reports only "new".
	if err := s.UpsertCards([]Card{
		{Host: "h", GPUUUID: "new", Index: 0, MemTotal: 24564},
	}, "h"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ClaimRequest{Host: "h", GPUUUID: "old", Owner: "a",
		Why: "x", For: time.Hour}); !errors.Is(err, ErrNoSuchCard) {
		t.Errorf("claiming a card that left the fleet: err = %v, want ErrNoSuchCard", err)
	}
	// And the automatic choice never lands on it either.
	res, err := s.Claim(req("h", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Claim.GPUUUID != "new" {
		t.Errorf("chose %q, want the card that is still present", res.Claim.GPUUUID)
	}
}

// TestUnobservedHostKeepsItsCards: an unreachable host is not evidence that its
// cards were removed. Only a complete observation may mark them absent.
func TestUnobservedHostKeepsItsCards(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	// A pass that observed some other host must not touch h.
	if err := s.UpsertCards([]Card{
		{Host: "other", GPUUUID: "other-0", Index: 0, MemTotal: 24564},
	}, "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(req("h", "a")); err != nil {
		t.Errorf("a host nobody observed lost its card: %v", err)
	}
}

// TestExplicitUUIDHonoursHostAndMemory: an explicit UUID selects a card, it does not
// waive the other constraints.
func TestExplicitUUIDHonoursHostAndMemory(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertCards([]Card{
		{Host: "h1", GPUUUID: "g1", Index: 0, MemTotal: 24564},
		{Host: "h2", GPUUUID: "g2", Index: 0, MemTotal: 8192},
	}); err != nil {
		t.Fatal(err)
	}
	// Wrong host: the agent named a machine it was told to use.
	if _, err := s.Claim(ClaimRequest{Host: "h1", GPUUUID: "g2", Owner: "a",
		Why: "x", For: time.Hour}); !errors.Is(err, ErrNoSuchCard) {
		t.Errorf("uuid on another host: err = %v, want ErrNoSuchCard", err)
	}
	// Too small: the agent asked for memory this card does not have.
	if _, err := s.Claim(ClaimRequest{Host: "h2", GPUUUID: "g2", MinVRAM: 80000,
		Owner: "a", Why: "x", For: time.Hour}); !errors.Is(err, ErrNoSuchCard) {
		t.Errorf("uuid below min-vram: err = %v, want ErrNoSuchCard", err)
	}
}

// TestRenewCannotExceedTheClaimCeiling: a limit only Claim enforces is a limit an
// agent steps around by renewing.
func TestRenewCannotExceedTheClaimCeiling(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	res, err := s.Claim(req("h", "a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []time.Duration{10000 * time.Hour, 2562047 * time.Hour} {
		if _, err := s.Renew(res.Claim.ID, "a", d); !errors.Is(err, ErrInvalid) {
			t.Errorf("renew for %v: err = %v, want ErrInvalid", d, err)
		}
	}
	c, err := s.Get(res.Claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Expires.Sub(c.Created) > maxClaimDuration+time.Minute {
		t.Errorf("an over-long renew still moved the deadline to %v", c.Expires)
	}
}

// TestOwnerWhitespaceStillOwns: an agent that quotes its own identity with a stray
// space must still be able to release the claim it made.
func TestOwnerWhitespaceStillOwns(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	r := req("h", " alice ")
	res, err := s.Claim(r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Claim.Owner != "alice" {
		t.Errorf("stored owner = %q, want it trimmed", res.Claim.Owner)
	}
	if _, err := s.Release(res.Claim.ID, " alice ", "released"); err != nil {
		t.Errorf("releasing with the identity as it was typed: %v", err)
	}
}

// TestOwnerCannotBecomeEmpty: sanitising '   ' must not silently produce a claim
// owned by "" — an empty owner skips the ownership check entirely.
func TestOwnerCannotBecomeEmpty(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	r := req("h", " ‮​ ")
	if _, err := s.Claim(r); !errors.Is(err, ErrInvalid) {
		t.Errorf("owner that sanitises to empty: err = %v, want ErrInvalid", err)
	}
}

// TestReleaseAsRecordsTheActor: an override must be recorded as the identity that
// ran it, not as the claim's owner, or the history says the holder released a claim
// it did not.
func TestReleaseAsRecordsTheActor(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	res, err := s.Claim(req("h", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseAs(res.Claim.ID, "", "override", "operator"); err != nil {
		t.Fatal(err)
	}
	ev, err := s.History("h-gpu0", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) == 0 || ev[0].Actor != "operator" || ev[0].Kind != "override" {
		t.Errorf("event = %+v, want an override by operator", ev)
	}
}

// TestOpenWaitsForAWriterInsteadOfFailing: two processes opening the ledger at once
// is the ordinary case (two agents claiming on a fresh machine). journal_mode used to
// be set in the DSN, which runs before busy_timeout is applied: switching a non-WAL
// file to WAL needs a write lock, so an open under a held lock failed in about a
// millisecond instead of waiting.
func TestOpenWaitsForAWriterInsteadOfFailing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	// A plain rollback-journal file with a writer holding its lock: the state a second
	// process finds while the first is still creating the ledger.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`CREATE TABLE warmup (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	tx, err := raw.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO warmup VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		tx.Commit()
		close(released)
	}()

	start := time.Now()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open under a held write lock failed after %v: %v", time.Since(start), err)
	}
	defer s.Close()
	<-released
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Errorf("open returned after %v; it cannot have waited for the writer", waited)
	}
}

// TestReleaseByAgentKindSurvivesARestart: an agent's identity carries a session id
// that changes on restart, so the agent that comes back is not the one that claimed.
// It must still be able to release its own kind's claim, and must not be able to
// release another kind's.
func TestReleaseByAgentKindSurvivesARestart(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 2)
	at := func(owner string) ClaimRequest {
		r := req("h", owner)
		r.Repo, r.Branch, r.Cwd = "proj", "main", "/src/proj"
		return r
	}
	mine, err := s.Claim(at("claude-code/aaaa1111"))
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.Claim(at("codex/bbbb2222"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseByAgentKind(mine.Claim.ID, "claude-code", "claude-code/cccc3333",
		"proj", "main", "/src/proj"); err != nil {
		t.Errorf("a restarted claude-code could not release its own claim: %v", err)
	}
	if _, err := s.ReleaseByAgentKind(theirs.Claim.ID, "claude-code", "claude-code/cccc3333",
		"proj", "main", "/src/proj"); !errors.Is(err, ErrNotOwner) {
		t.Errorf("claude-code released a codex claim: err = %v", err)
	}
}

// TestReleaseByAgentKindRefusesAnotherConversation: two conversations of one runtime run
// at the same time. Matching on kind alone let either close the other's claim, which is
// the eviction the ledger exists to prevent.
func TestReleaseByAgentKindRefusesAnotherConversation(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	r := req("h", "claude-code/aaaa1111")
	r.Repo, r.Branch, r.Cwd = "proj", "main", "/src/proj"
	c, err := s.Claim(r)
	if err != nil {
		t.Fatal(err)
	}
	// Same runtime, same repo, different working directory: another conversation.
	if _, err := s.ReleaseByAgentKind(c.Claim.ID, "claude-code", "claude-code/bbbb2222",
		"proj", "main", "/src/proj-worktree"); !errors.Is(err, ErrNotOwner) {
		t.Errorf("a second conversation released the first one's claim: err = %v", err)
	}
	// The exact owner still works, and so does the operator's override.
	if _, err := s.ReleaseAs(c.Claim.ID, "claude-code/aaaa1111", "released", "x"); err != nil {
		t.Errorf("the owner could not release its own claim: %v", err)
	}
}

// TestExplicitNamesAreNotAKind: a claim made with -as agent-a has no kind/session
// split. Treating the whole name as a kind would let anyone passing "agent-a" as
// their kind release it.
func TestExplicitNamesAreNotAKind(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	c, err := s.Claim(req("h", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseByAgentKind(c.Claim.ID, "agent-a", "agent-a/x", "", "", ""); !errors.Is(err, ErrNotOwner) {
		t.Errorf("an explicit name was matched as a kind: err = %v", err)
	}
}

// TestConflictCarriesTheDirective: the skill tells two colliding agents to decide by
// the operator's instruction, so the conflict has to carry it.
func TestConflictCarriesTheDirective(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	r := req("h", "agent-a")
	r.Directive = "use h for the sweep"
	if _, err := s.Claim(r); err != nil {
		t.Fatal(err)
	}
	_, err := s.Claim(ClaimRequest{GPUUUID: "h-gpu0", Owner: "agent-b", Why: "x", For: time.Hour})
	var he *HeldError
	if !errors.As(err, &he) || he.Directive != "use h for the sweep" {
		t.Errorf("by-card conflict lost the directive: %+v", err)
	}
	_, err = s.Claim(req("h", "agent-b"))
	var nf *NoFreeCardError
	if !errors.As(err, &nf) || len(nf.Holders) != 1 || nf.Holders[0].Directive != "use h for the sweep" {
		t.Errorf("full-host conflict lost the directive: %+v", err)
	}
}

// TestClaimNeverPicksAnUnclaimedBusyCard: a card with a job nobody claimed looked free
// to the ledger, and `claim -host` put an agent on top of it while reporting success.
func TestClaimNeverPicksAnUnclaimedBusyCard(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 3)
	r := req("h", "agent-a")
	r.RequireIdle = true
	r.Observed = map[string]Observation{
		"h-gpu0": {Procs: 2, ProcsKnown: true, MemKnown: true}, // busy, nobody claimed
		"h-gpu1": {Procs: 0, ProcsKnown: true, MemKnown: true}, // idle
		// h-gpu2 not enumerated: unknown, and unknown is not idle.
	}
	res, err := s.Claim(r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Card.GPUUUID != "h-gpu1" {
		t.Errorf("claimed %s, want the only enumerated idle card h-gpu1", res.Card.GPUUUID)
	}
	// With every observed card busy, the claim is refused rather than upgrading to an
	// unknown one.
	r2 := req("h", "agent-b")
	r2.RequireIdle = true
	r2.Observed = map[string]Observation{
		"h-gpu0": {Procs: 1, ProcsKnown: true, MemKnown: true},
		"h-gpu2": {Procs: 3, ProcsKnown: true, MemKnown: true},
	}
	_, err = s.Claim(r2)
	if !errors.Is(err, ErrNoSuchCard) {
		t.Fatalf("claim on a host of busy unclaimed cards: err = %v", err)
	}
	if !strings.Contains(err.Error(), "processes") {
		t.Errorf("the refusal does not say the cards are in use: %v", err)
	}
}

// TestClaimWithoutAnObservationStillWorks: -no-ssh and an explicit -gpu keep the old
// behaviour, because there the caller has said what it knows.
func TestClaimWithoutAnObservationStillWorks(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	if _, err := s.Claim(req("h", "agent-a")); err != nil {
		t.Fatalf("claim without an observation failed: %v", err)
	}
}

// TestOpenRefusesAMissingDirectory: a mistyped -store created a fresh empty database
// and every command succeeded against it — no cards, no history, no collision
// protection — which is indistinguishable from an empty fleet.
func TestOpenRefusesAMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "ledger.db")
	if _, err := Open(path); err == nil {
		t.Fatal("Open created a database in a directory that does not exist")
	}
	if _, err := os.Stat(filepath.Dir(path)); err == nil {
		t.Error("Open created the directory")
	}
}

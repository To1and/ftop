package ledger

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// backdate moves a claim's recorded activity and change time into the past, which is
// what a test needs instead of waiting half an hour.
func backdate(t *testing.T, s *Store, id string, d time.Duration) {
	t.Helper()
	at := time.Now().Add(-d).UnixNano()
	if _, err := s.db.Exec(`UPDATE claims SET last_busy_ns = ?, updated_ns = ? WHERE id = ?`,
		at, at, id); err != nil {
		t.Fatal(err)
	}
}

// TestIdleClaimIsReclaimedBeforeItsDeadline is the point of the whole reclaim: an agent
// that finished early and forgot holds a card for hours, because its deadline was a
// guess. The observation is what gives the card back, not the clock.
func TestIdleClaimIsReclaimedBeforeItsDeadline(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	res, err := s.Claim(req("h", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, s, res.Claim.ID, IdleReclaimAfter+time.Minute)

	released, kept, err := s.Sweep(map[string]Observation{
		res.Claim.GPUUUID: {ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 1 || len(kept) != 0 {
		t.Fatalf("released=%d kept=%d, want the idle claim reclaimed", len(released), len(kept))
	}
	if released[0].CloseCause != "idle" {
		t.Errorf("close cause = %q, want idle", released[0].CloseCause)
	}
	if _, err := s.Claim(req("h", "agent-b")); err != nil {
		t.Fatalf("the card was not freed: %v", err)
	}
}

// TestFreshIdleClaimIsKept: "idle now" is not "abandoned". A job that has just started
// has not touched the card yet, and reclaiming it would be the eviction of a running
// job that the whole design refuses.
func TestFreshIdleClaimIsKept(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	res, err := s.Claim(req("h", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	released, _, err := s.Sweep(map[string]Observation{
		res.Claim.GPUUUID: {ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatalf("a claim made moments ago was reclaimed: %+v", released)
	}
}

// TestBusyCardIsNeverReclaimed: work on the card is the evidence that keeps the claim,
// whatever the deadline says.
func TestBusyCardIsNeverReclaimed(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	r := req("h", "agent-a")
	r.For = time.Millisecond
	res, err := s.Claim(r)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	backdate(t, s, res.Claim.ID, 24*time.Hour)
	released, kept, err := s.Sweep(map[string]Observation{
		res.Claim.GPUUUID: {ProcsKnown: true, MemKnown: true, Procs: 1, MemTotalMiB: 24564},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 || len(kept) != 1 {
		t.Fatalf("released=%d kept=%d, want the overdue claim kept while busy", len(released), len(kept))
	}
}

// TestBusyObservationRestartsTheClock: the card was observed in use, so the idle window
// starts again from that observation.
func TestBusyObservationRestartsTheClock(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	res, err := s.Claim(req("h", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, s, res.Claim.ID, IdleReclaimAfter+time.Minute)
	if _, _, err := s.Sweep(map[string]Observation{
		res.Claim.GPUUUID: {ProcsKnown: true, MemKnown: true, Procs: 2, MemTotalMiB: 24564},
	}); err != nil {
		t.Fatal(err)
	}
	released, _, err := s.Sweep(map[string]Observation{
		res.Claim.GPUUUID: {ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatalf("the clock was not restarted by the busy observation: %+v", released)
	}
	c, err := s.Get(res.Claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(c.LastBusy) > time.Minute {
		t.Errorf("last_busy is %v old, want it moved to the busy observation", time.Since(c.LastBusy))
	}
}

// TestKeepStopsTheReclaim: a holder between jobs says so, and the card stays theirs.
func TestKeepStopsTheReclaim(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	r := req("h", "agent-a")
	r.Keep = true
	res, err := s.Claim(r)
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, s, res.Claim.ID, 24*time.Hour)
	released, _, err := s.Sweep(map[string]Observation{
		res.Claim.GPUUUID: {ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatalf("a kept claim was reclaimed: %+v", released)
	}
}

// TestRenewRestartsTheIdleClock: renewing is the holder saying the card is still
// wanted, so it is the way out of a reclaim without -keep.
func TestRenewRestartsTheIdleClock(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	res, err := s.Claim(req("h", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, s, res.Claim.ID, IdleReclaimAfter+time.Minute)
	if _, err := s.Renew(res.Claim.ID, "agent-a", 4*time.Hour); err != nil {
		t.Fatal(err)
	}
	released, _, err := s.Sweep(map[string]Observation{
		res.Claim.GPUUUID: {ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatalf("a renewed claim was reclaimed: %+v", released)
	}
}

// TestReclaimNeedsAnObservation: the window alone never closes a claim. A card nobody
// looked at is not evidence of anything.
func TestReclaimNeedsAnObservation(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	res, err := s.Claim(req("h", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, s, res.Claim.ID, 24*time.Hour)
	for _, obs := range []map[string]Observation{
		nil,
		{res.Claim.GPUUUID: {ProcsKnown: false, MemKnown: true}},
		{res.Claim.GPUUUID: {ProcsKnown: true}},
	} {
		released, _, err := s.Sweep(obs)
		if err != nil {
			t.Fatal(err)
		}
		if len(released) != 0 {
			t.Fatalf("observation %+v reclaimed a claim it could not judge", obs)
		}
	}
}

// TestUpgradedClaimIsNotReclaimedAtOnce: a claim written before last_busy existed has
// no recorded activity, and reading that as "idle since 1970" would reclaim every open
// claim on the first observation after the upgrade.
func TestUpgradedClaimIsNotReclaimedAtOnce(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	res, err := s.Claim(req("h", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE claims SET last_busy_ns = 0 WHERE id = ?`,
		res.Claim.ID); err != nil {
		t.Fatal(err)
	}
	released, _, err := s.Sweep(map[string]Observation{
		res.Claim.GPUUUID: {ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatalf("a claim with no recorded activity was reclaimed on its first look: %+v", released)
	}
}

// TestSetKeepIsTheHolders: a group flips whole, and only its owner may.
func TestSetKeepIsTheHolders(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"h": 3})
	a, err := s.Allocate(auto(2, obs))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetKeep(a.Group, "agent-b", true); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("another owner changed keep: %v", err)
	}
	cs, err := s.SetKeep(a.Group, "agent-a", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || !cs[0].Keep || !cs[1].Keep {
		t.Fatalf("keep = %+v, want both", cs)
	}
	cs, err = s.SetKeep(a.Group, "agent-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if cs[0].Keep || cs[1].Keep {
		t.Errorf("keep was not turned off: %+v", cs)
	}
}

// TestMigrationFromV3StartsTheIdleClockAtTheUpgrade opens a real v3 ledger. Its open
// claims have no recorded activity — ftop did not keep any — and one of them was made
// hours ago. Judged from when it last changed, it would be reclaimed the first time its
// job paused; the upgrade is when ftop starts watching, so that is where its window
// starts.
func TestMigrationFromV3StartsTheIdleClockAtTheUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range append(append(append([]string{}, v1Schema...), v2Schema...), v3Schema...) {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-6 * time.Hour).UnixNano()
	if _, err := db.Exec(`INSERT INTO cards (gpu_uuid, host, idx, first_seen, last_seen)
		VALUES ('GPU-0', 'h', 0, ?, ?)`, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO claims (id, host, gpu_uuid, owner, why, created_ns,
		updated_ns, expires_ns, grp) VALUES ('Lold', 'h', 'GPU-0', 'agent-a', 'old job', ?, ?, ?, '')`,
		old, old, time.Now().Add(8*time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening a v3 ledger: %v", err)
	}
	defer s.Close()
	c, err := s.Get("Lold")
	if err != nil {
		t.Fatal(err)
	}
	if c.Keep || time.Since(c.LastBusy) > time.Minute {
		t.Fatalf("after migration keep=%v last_busy=%v, want reclaim on and the clock at the upgrade",
			c.Keep, c.LastBusy)
	}
	released, _, err := s.Sweep(map[string]Observation{"GPU-0": {
		At: time.Now(), ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatal("a six-hour-old claim was reclaimed on the first idle look after the upgrade")
	}
}

// TestIdleEvidenceIsDatedByItsSample: an observation pass waits for its slowest host.
// An idle reading taken before the window closed is not evidence about the card after
// it, however late it reaches Sweep.
func TestIdleEvidenceIsDatedByItsSample(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	res, err := s.Claim(req("h", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, s, res.Claim.ID, IdleReclaimAfter+busyResolution+10*time.Second)
	released, _, err := s.Sweep(map[string]Observation{res.Claim.GPUUUID: {
		At:         time.Now().Add(-20 * time.Second),
		ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatal("reclaimed on an idle reading taken before the window had passed")
	}
}

// TestAProcessIsWorkEvenWithoutAMemoryReading: a sample whose memory query failed but
// whose process list shows a job is still proof the job was running. Discarding it let
// the idle clock start from an older time, and the claim was reclaimed the moment the
// job finished instead of 30 minutes later.
func TestAProcessIsWorkEvenWithoutAMemoryReading(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	res, err := s.Claim(req("h", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	backdate(t, s, res.Claim.ID, 2*IdleReclaimAfter)
	if _, _, err := s.Sweep(map[string]Observation{res.Claim.GPUUUID: {
		At: time.Now(), ProcsKnown: true, Procs: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	released, _, err := s.Sweep(map[string]Observation{res.Claim.GPUUUID: {
		At: time.Now(), ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatal("reclaimed right after a process was seen on the card")
	}
}

// TestCoalescedWorkDoesNotShortenTheWindow: touchBusy skips a write within
// busyResolution of the recorded time, so the last work may be that much later than
// recorded. The window must be measured from the latest it could have been.
func TestCoalescedWorkDoesNotShortenTheWindow(t *testing.T) {
	s := newStore(t)
	seed(t, s, "h", 1)
	res, err := s.Claim(req("h", "agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	recorded := IdleReclaimAfter + 20*time.Second
	backdate(t, s, res.Claim.ID, recorded)
	// Work seen 40 s after the recorded time: coalesced, so not written.
	if _, _, err := s.Sweep(map[string]Observation{res.Claim.GPUUUID: {
		At:         time.Now().Add(-recorded + 40*time.Second),
		ProcsKnown: true, Procs: 1, MemKnown: true, MemTotalMiB: 24564,
	}}); err != nil {
		t.Fatal(err)
	}
	released, _, err := s.Sweep(map[string]Observation{res.Claim.GPUUUID: {
		At: time.Now(), ProcsKnown: true, MemKnown: true, MemTotalMiB: 24564,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatal("reclaimed less than the window after work was last seen")
	}
}

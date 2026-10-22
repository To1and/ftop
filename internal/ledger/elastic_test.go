package ledger

import (
	"errors"
	"testing"
)

// elastic builds an automatic claim for a range.
func elastic(min, max int, spread bool, obs map[string]Observation) ClaimRequest {
	r := auto(min, obs)
	r.Max, r.Spread = max, spread
	return r
}

// TestElasticTakesAsManyAsFit: the sweep case. Eight cards idle in all, asked for
// 4..40, so it takes all eight — and reports the range, because the caller's job has to
// be sized for what it got.
func TestElasticTakesAsManyAsFit(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"a": 4, "b": 4})
	a, err := s.Allocate(elastic(4, 40, true, obs))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Claims) != 8 {
		t.Fatalf("took %d cards, want all 8 idle", len(a.Claims))
	}
	if a.Placement.Range == nil || a.Placement.Range.Min != 4 || a.Placement.Range.Max != 40 ||
		a.Placement.Range.Got != 8 {
		t.Errorf("requested = %+v, want 4..40 got 8", a.Placement.Range)
	}
}

// TestElasticStopsAtMax: the ceiling is a ceiling. This is the difference from a plain
// count, which would take exactly this many or nothing at all.
func TestElasticStopsAtMax(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"a": 4, "b": 4})
	a, err := s.Allocate(elastic(2, 5, true, obs))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Claims) != 5 {
		t.Fatalf("took %d, want exactly the maximum 5", len(a.Claims))
	}
	if a.Placement.Range.Got != 5 {
		t.Errorf("requested = %+v", a.Placement.Range)
	}
}

// TestElasticNeedsItsMinimum: below the minimum nothing is claimed at all, and the
// error still says what the fleet has.
func TestElasticNeedsItsMinimum(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"a": 2})
	_, err := s.Allocate(elastic(4, 40, true, obs))
	var ie *InsufficientError
	if !errors.As(err, &ie) || ie.Idle != 2 || ie.Want != 4 {
		t.Fatalf("err = %v, want no capacity for 4 with 2 idle", err)
	}
	if n, _ := s.OpenCount(); n != 0 {
		t.Errorf("%d cards were claimed by a failed request", n)
	}
}

// TestElasticOnOneHostTakesAsManyAsOneHostCan: without spread the range is filled
// from one machine — the one that can give the most, up to max, and among those the
// closest fit for that many. Ranking by fit for min first gave a 2..6 request the four
// cards of the four-card host while the eight-card host had six to give.
func TestElasticOnOneHostTakesAsManyAsOneHostCan(t *testing.T) {
	for _, tc := range []struct {
		hosts    map[string]int
		min, max int
		want     string
		n        int
	}{
		{map[string]int{"four": 4, "eight": 8}, 2, 6, "eight", 6},
		{map[string]int{"four": 4, "eight": 8}, 2, 4, "four", 4},
		{map[string]int{"four": 4, "eight": 8}, 2, 40, "eight", 8},
		{map[string]int{"two": 2, "four": 4}, 1, 3, "four", 3},
	} {
		s := newStore(t)
		obs := fleet(t, s, tc.hosts)
		a, err := s.Allocate(elastic(tc.min, tc.max, false, obs))
		if err != nil {
			t.Fatal(err)
		}
		if got := hostsOf(a); len(got) != 1 || got[tc.want] != tc.n {
			t.Errorf("%d..%d over %v went to %v, want %d on %s", tc.min, tc.max, tc.hosts,
				got, tc.n, tc.want)
		}
		if a.Placement.Range.Got != len(a.Claims) {
			t.Errorf("requested.got = %d, claims = %d", a.Placement.Range.Got, len(a.Claims))
		}
	}
}

// TestExcludedHostsAreNeverPlacedOn: the operator's "use everything except that one"
// is expressible without naming every other machine.
func TestExcludedHostsAreNeverPlacedOn(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"busy-job": 8, "other": 4})
	r := auto(2, obs)
	r.ExcludeHosts = []string{"busy-job"}
	a, err := s.Allocate(r)
	if err != nil {
		t.Fatal(err)
	}
	if a.Claims[0].Card.Host != "other" {
		t.Errorf("placed on %s, which was excluded", a.Claims[0].Card.Host)
	}
	r.ExcludeHosts = []string{"busy-job", "other"}
	if _, err := s.Allocate(r); !errors.Is(err, ErrNoCapacity) {
		t.Errorf("everything was excluded and a card was still placed: %v", err)
	}
}

// TestRangeWithANamedHostIsRefused: a range means "as many as are free", which a named
// machine cannot answer without the caller knowing how many it has.
func TestRangeWithANamedHostIsRefused(t *testing.T) {
	s := newStore(t)
	obs := fleet(t, s, map[string]int{"h": 4})
	r := elastic(1, 4, false, obs)
	r.Host = "h"
	if _, err := s.Allocate(r); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

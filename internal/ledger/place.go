package ledger

import (
	"database/sql"
	"fmt"
	"sort"
)

// Placement says how a claim's cards were chosen.
type Placement struct {
	// Mode is "gpu" when the card was named, "host" when the machine was named, and
	// "fit" when ftop chose.
	Mode   string `json:"mode"`
	Spread bool   `json:"spread,omitempty"`
	// Reason is one sentence an agent can repeat to the operator.
	Reason string `json:"reason"`
	// Range is set for a request that named a range rather than a count: at least Min,
	// at most Max, as many as are idle. Got is what it took, which is Min..Max or — when
	// even Min was not available — zero, with nothing claimed.
	Range *Requested `json:"requested,omitempty"`
	// Candidates are the hosts a "fit" placement weighed: for one host, every host
	// that could have taken the whole request, closest fit first; for a spread one,
	// every host with an idle card, the ones used first.
	Candidates []Candidate `json:"candidates,omitempty"`
}

// Requested is the range an elastic claim asked for.
type Requested struct {
	Min int `json:"min"`
	Max int `json:"max"`
	Got int `json:"got"`
}

// Candidate is one host as placement saw it.
type Candidate struct {
	Host  string `json:"host"`
	Cards int    `json:"cards"` // cards present on the host
	Idle  int    `json:"idle"`  // free and observed idle before this claim
	Taken int    `json:"taken"` // this claim's cards placed here
	Left  int    `json:"left"`  // idle cards the host still has after the claim
}

// slot is one host's cards as the claim transaction sees them.
type slot struct {
	host   string
	cards  int    // present cards, whatever their state
	held   int    // carrying an open claim
	busy   int    // unclaimed, but processes or hidden memory on them
	unseen int    // unclaimed, but not observed: nobody can say whether they are idle
	small  int    // unclaimed, but less memory than the request needs
	idle   []Card // unclaimed, big enough, observed idle; lowest index first
	placed int    // taken by this request (spread)

	busyUUID string // one of the busy cards, to name in an error
	idle0    int    // len(idle) before placement took any
}

// partial reports whether the host is already in use: a card held, busy, or not
// known to be idle, or one this request placed. A card that is merely too small for
// this request is not use — counting it made an idle machine look busy and win.
func (s *slot) partial() bool { return s.held+s.busy+s.unseen+s.placed > 0 }

// takenMem is the memory of the k cards this host would give: its k lowest idle
// indices, the ones placement takes.
func (s *slot) takenMem(k int) uint64 {
	var m uint64
	for _, c := range s.idle[:k] {
		m += c.MemTotal
	}
	return m
}

// loadSlots reads every present card the request may consider, grouped by host in
// name order.
func loadSlots(tx *sql.Tx, req ClaimRequest) ([]*slot, error) {
	q := `SELECT c.gpu_uuid, c.host, c.idx, c.name, c.mem_total, c.first_seen, c.last_seen,
		EXISTS (SELECT 1 FROM claims k WHERE k.gpu_uuid = c.gpu_uuid AND k.closed = 0)
		FROM cards c WHERE c.present = 1`
	var args []any
	if req.Host != "" {
		q += ` AND c.host = ?`
		args = append(args, req.Host)
	}
	q += ` ORDER BY c.host, c.idx`
	var allowed map[string]bool
	if req.Host == "" && req.Hosts != nil {
		allowed = make(map[string]bool, len(req.Hosts))
		for _, h := range req.Hosts {
			allowed[h] = true
		}
	}
	excluded := make(map[string]bool, len(req.ExcludeHosts))
	for _, h := range req.ExcludeHosts {
		excluded[h] = true
	}
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	defer rows.Close()
	var out []*slot
	by := map[string]*slot{}
	for rows.Next() {
		var c Card
		var fs, ls int64
		var held bool
		if err := rows.Scan(&c.GPUUUID, &c.Host, &c.Index, &c.Name, &c.MemTotal, &fs, &ls,
			&held); err != nil {
			return nil, fmt.Errorf("ledger: %w", err)
		}
		if allowed != nil && !allowed[c.Host] {
			continue
		}
		if excluded[c.Host] {
			continue
		}
		c.FirstSeen, c.LastSeen, c.Present = fromNS(fs), fromNS(ls), true
		s := by[c.Host]
		if s == nil {
			s = &slot{host: c.Host}
			by[c.Host] = s
			out = append(out, s)
		}
		s.cards++
		switch {
		case held:
			s.held++
		case req.MinVRAM > 0 && c.MemTotal < req.MinVRAM:
			s.small++
		case !idleForClaim(req, c):
			// A card the probe did not enumerate is skipped: "does not know" is not
			// "idle", and picking it is how an agent ended up on a card with another
			// job's processes on it. Only one it saw in use counts as busy.
			if ob, ok := req.Observed[c.GPUUUID]; ok && ob.ProcsKnown && (ob.Procs > 0 || ob.Hidden()) {
				s.busy++
				s.busyUUID = c.GPUUUID
			} else {
				s.unseen++
			}
		default:
			s.idle = append(s.idle, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	for _, s := range out {
		s.idle0 = len(s.idle)
	}
	return out, nil
}

// fits reports whether host a is a closer fit than host b for k more cards.
//
// The rule keeps whole machines whole, so the next large job still finds one:
//
//  1. fewest idle cards left over once these are placed — two cards go to a
//     two-card host, not into an eight-card one;
//  2. then a host already partly in use, so a hole is filled before a whole machine
//     is opened: one card goes to a four-card host with one left, not to an idle
//     two-card one;
//  3. then the smaller host;
//  4. then less memory on the cards it would take, so a job that fits on the common
//     card does not take the rare large one;
//  5. then the name, so the same state always gives the same answer.
func fits(a, b *slot, k int) bool {
	if la, lb := len(a.idle)-k, len(b.idle)-k; la != lb {
		return la < lb
	}
	if pa, pb := a.partial(), b.partial(); pa != pb {
		return pa
	}
	if a.cards != b.cards {
		return a.cards < b.cards
	}
	if ma, mb := a.takenMem(k), b.takenMem(k); ma != mb {
		return ma < mb
	}
	return a.host < b.host
}

// placeRange chooses where cards go when the caller named neither a host nor a card:
// at least min, at most max, or nothing.
//
// Without spread all of them go on one host, the closest fit (see fits). With spread
// each card is placed by itself, one after another, by the same rule — which fills
// one-card hosts and the holes in partly used ones first.
//
// It is what a sweep needs. "Give me up to N, but at least K" is the shape of a job
// that adapts to the fleet — 162 configs that can run on whatever is free — and the
// all-or-none form made those agents count the fleet themselves, then lose the whole
// batch to one card taken in between. When min == max the request is the ordinary one,
// and nothing here is less strict: a partial result is only possible for a range, and
// it is still the whole range or none.
func placeRange(slots []*slot, min, max int, spread bool) ([]Card, Placement, error) {
	idle, most, best := 0, 0, ""
	for _, s := range slots {
		idle += len(s.idle)
		if len(s.idle) > most {
			most, best = len(s.idle), s.host
		}
	}
	short := &InsufficientError{Want: min, Spread: spread, MostOnOneHost: most, BestHost: best,
		Idle: idle, Hosts: len(slots)}
	if spread {
		// A spread takes one card at a time, so it is bounded by the fleet, and an
		// elastic one takes as many as exist up to max.
		if idle < min {
			return nil, Placement{}, short
		}
		n := min
		if min != max {
			if idle < max {
				n = idle
			} else {
				n = max
			}
		}
		cards, plc, err := placeSpread(slots, n, short)
		if err != nil {
			return nil, Placement{}, err
		}
		return cards, withRange(plc, min, max, n), nil
	}
	var can []*slot
	for _, s := range slots {
		if len(s.idle) >= min {
			can = append(can, s)
		}
	}
	if len(can) == 0 {
		return nil, Placement{}, short
	}
	// A range asks for as many as one machine can give, up to max, so that comes
	// first: the hosts that could give the most are the only candidates, and the
	// closest-fit rule chooses among them for that many cards. Ranking every host by
	// fit for min, then taking what the winner happened to have, gave a 2..6 request
	// four cards from a four-card machine while an eight-card one had six to give.
	n := min
	if min != max {
		for _, s := range can {
			n = maxInt(n, minInt(len(s.idle), max))
		}
		best := can[:0:0]
		for _, s := range can {
			if len(s.idle) >= n {
				best = append(best, s)
			}
		}
		can = best
	}
	sort.SliceStable(can, func(i, j int) bool { return fits(can[i], can[j], n) })
	chosen := can[0]
	plc := Placement{Mode: "fit"}
	for _, s := range can {
		c := Candidate{Host: s.host, Cards: s.cards, Idle: s.idle0, Left: s.idle0}
		if s == chosen {
			c.Taken, c.Left = n, s.idle0-n
		}
		plc.Candidates = append(plc.Candidates, c)
	}
	plc.Reason = fmt.Sprintf("%s is the closest fit for %d card(s): %d of its %d cards were idle "+
		"and %d stay idle", chosen.host, n, chosen.idle0, chosen.cards, chosen.idle0-n)
	if min != max {
		plc.Reason = fmt.Sprintf("%s can take %d card(s) now (asked for %d..%d): %d of its %d "+
			"cards were idle and %d stay idle", chosen.host, n, min, max, chosen.idle0,
			chosen.cards, chosen.idle0-n)
	}
	if chosen.partial() {
		plc.Reason += "; it was already partly in use, which keeps whole machines free"
	}
	if len(can) > 1 {
		plc.Reason += fmt.Sprintf(" (%d host(s) could take them; see candidates)", len(can))
	}
	return chosen.idle[:n], withRange(plc, min, max, n), nil
}

// withRange records an elastic request's range and what it got.
func withRange(plc Placement, min, max, got int) Placement {
	if min != max {
		plc.Range = &Requested{Min: min, Max: max, Got: got}
	}
	return plc
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func placeSpread(slots []*slot, n int, short *InsufficientError) ([]Card, Placement, error) {
	if short.Idle < n {
		return nil, Placement{}, short
	}
	taken := map[*slot]int{}
	var order []*slot
	var cards []Card
	for len(cards) < n {
		var best *slot
		for _, s := range slots {
			if len(s.idle) > 0 && (best == nil || fits(s, best, 1)) {
				best = s
			}
		}
		cards = append(cards, best.idle[0])
		best.idle = best.idle[1:]
		best.placed++
		if taken[best] == 0 {
			order = append(order, best)
		}
		taken[best]++
	}
	plc := Placement{Mode: "fit", Spread: true, Reason: fmt.Sprintf(
		"%d card(s) on %d host(s), each placed where it fits most closely", n, len(order))}
	for _, s := range order {
		plc.Candidates = append(plc.Candidates, Candidate{Host: s.host, Cards: s.cards,
			Idle: s.idle0, Taken: taken[s], Left: len(s.idle)})
	}
	for _, s := range slots {
		if taken[s] == 0 && s.idle0 > 0 {
			plc.Candidates = append(plc.Candidates, Candidate{Host: s.host, Cards: s.cards,
				Idle: s.idle0, Left: s.idle0})
		}
	}
	// Host, then index: the order a reader groups them in, and the order the
	// per-host CUDA_VISIBLE_DEVICES lists are built in.
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].Host != cards[j].Host {
			return cards[i].Host < cards[j].Host
		}
		return cards[i].Index < cards[j].Index
	})
	return cards, plc, nil
}

// InsufficientError says the request cannot be met right now, and how close the
// fleet comes. Nothing was claimed.
//
// It answers the agent's next question in the same reply: how many it could have
// had, and where.
type InsufficientError struct {
	Want   int    `json:"want"`
	Host   string `json:"host,omitempty"` // the host the request named, if any
	Spread bool   `json:"spread,omitempty"`
	// MostOnOneHost is the largest number of idle cards any one host has, and
	// BestHost is that host: the most a one-host request can get.
	MostOnOneHost int    `json:"most_on_one_host"`
	BestHost      string `json:"best_host,omitempty"`
	// Idle counts idle cards across every host considered — the most a spread
	// request can get.
	Idle  int `json:"idle"`
	Hosts int `json:"hosts"` // hosts considered
}

func (e *InsufficientError) Error() string {
	switch {
	case e.Host != "":
		return fmt.Sprintf("%s has %d idle card(s); %d were asked for", e.Host, e.Idle, e.Want)
	case e.Idle == 0:
		return fmt.Sprintf("no idle card on any of the %d host(s) ftop may place on", e.Hosts)
	case e.Spread:
		return fmt.Sprintf("%d idle card(s) across %d host(s); %d were asked for",
			e.Idle, e.Hosts, e.Want)
	}
	return fmt.Sprintf("no host has %d idle cards: the most on one host is %d (%s), "+
		"and %d are idle across %d host(s)", e.Want, e.MostOnOneHost, e.BestHost, e.Idle, e.Hosts)
}
func (e *InsufficientError) Unwrap() error { return ErrNoCapacity }

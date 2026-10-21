package ledger

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Observation is the last measured state of one card.
//
// It is evidence, not authority. It can contradict the ledger — a claim that says
// held while the card is empty, or a card the ledger calls free while something is
// running — and the ledger reports the disagreement rather than resolving it.
type Observation struct {
	At          time.Time `json:"at"`
	UtilPct     float64   `json:"util_pct"`
	MemUsedMiB  uint64    `json:"mem_used_mib,omitempty"`
	MemTotalMiB uint64    `json:"mem_total_mib,omitempty"`
	// MemKnown is false when the driver did not report used memory. Then the
	// unattributed figure is not zero, it is unknown, and the card is not idle.
	MemKnown bool `json:"mem_known"`
	// MemReservedMiB is the driver's own reservation, which MemUsedMiB does not
	// include. Nil when the driver only answers the v1 query: then MemUsedMiB is
	// total minus free and the reservation is inside it.
	MemReservedMiB *uint64 `json:"mem_reserved_mib,omitempty"`
	Procs          int     `json:"procs"`
	ProcsKnown     bool    `json:"procs_known"`
	// UnattributedMiB is used memory no process on this host accounts for.
	UnattributedMiB int64 `json:"unattributed_mib,omitempty"`
}

// CardView is one card as `ftop gpu list` reports it: what the ledger says, and
// what the probe saw, side by side.
type CardView struct {
	Card
	Observation *Observation `json:"observed,omitempty"`
	// Conflict is a one-line statement of how the two disagree, empty when they
	// agree. This is the field an agent reads to decide whether to escalate.
	Conflict string `json:"conflict,omitempty"`
}

// UpsertCards records what the probes see.
//
// Card rows are a cache of the fleet's hardware inventory, not an authority. A card
// row is never deleted, because its history is the point; instead `present` records
// whether the card was in its host's most recent COMPLETE observation, and a card
// that has left the fleet stops being offered. Without that, a UUID that disappeared
// (a container resized, a card replaced) stayed in the free list forever and any
// agent that had cached it could claim a card that no longer exists.
//
// hosts names the hosts this pass observed completely. Their cards not in `cards`
// are marked absent. A host that is not named is left alone: an unreachable host is
// not evidence that its cards were removed, and marking them absent would make them
// unclaimable the moment the network hiccups.
func (s *Store) UpsertCards(cards []Card, hosts ...string) error {
	now := nowNS()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()
	seen := make(map[string]bool, len(cards))
	for _, c := range cards {
		if c.GPUUUID == "" || c.Host == "" {
			continue
		}
		seen[c.GPUUUID] = true
		_, err := tx.Exec(`INSERT INTO cards
			(gpu_uuid, host, idx, name, mem_total, first_seen, last_seen, present, gone_since_ns)
			VALUES (?,?,?,?,?,?,?,1,0)
			ON CONFLICT(gpu_uuid) DO UPDATE SET
				host = excluded.host,
				idx = excluded.idx,
				name = excluded.name,
				mem_total = excluded.mem_total,
				last_seen = excluded.last_seen,
				present = 1,
				gone_since_ns = 0`,
			c.GPUUUID, c.Host, c.Index, c.Name, c.MemTotal, now, now)
		if err != nil {
			return fmt.Errorf("ledger: %w", err)
		}
	}
	for _, host := range hosts {
		if host == "" {
			continue
		}
		rows, err := tx.Query(`SELECT gpu_uuid FROM cards WHERE host = ? AND present = 1`, host)
		if err != nil {
			return fmt.Errorf("ledger: %w", err)
		}
		var gone []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("ledger: %w", err)
			}
			if !seen[id] {
				gone = append(gone, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("ledger: %w", err)
		}
		for _, id := range gone {
			// A claim on a vanished card is left open, not closed: the history should
			// show that ftop lost sight of it rather than that its owner released it,
			// and an agent's work does not end because a container was resized.
			if _, err := tx.Exec(`UPDATE cards SET present = 0, gone_since_ns = ?
				WHERE gpu_uuid = ?`, now, id); err != nil {
				return fmt.Errorf("ledger: %w", err)
			}
		}
	}
	return tx.Commit()
}

// ListOptions filters a listing.
type ListOptions struct {
	Host string
	// Free limits the result to cards with no open claim.
	Free bool
	// Held limits the result to cards with an open claim.
	Held bool
}

// List returns the cards the ledger knows, joined with their holders.
//
// A card is "stale" once its host has not been seen for StaleAfter; it stays in the
// list because losing sight of a machine does not mean its claims evaporated.
func (s *Store) List(opt ListOptions) ([]CardView, error) {
	q := `SELECT c.gpu_uuid, c.host, c.idx, c.name, c.mem_total, c.first_seen, c.last_seen,
		c.present,
		k.id, k.owner, k.session, k.repo, k.branch, k.cwd, k.why, k.note, k.directive,
		k.created_ns, k.updated_ns, k.expires_ns, k.closed, k.close_cause, k.closed_ns, k.grp
		FROM cards c
		LEFT JOIN claims k ON k.gpu_uuid = c.gpu_uuid AND k.closed = 0`
	var args []any
	if opt.Host != "" {
		q += ` WHERE c.host = ?`
		args = append(args, opt.Host)
	}
	q += ` ORDER BY c.host, c.idx`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	defer rows.Close()

	// Never nil: a JSON list that is sometimes null and sometimes [] forces every
	// caller to handle both, and an agent that does `len(x)` on the result crashes.
	out := []CardView{}
	for rows.Next() {
		var (
			v        CardView
			fs, ls   int64
			claimID  sql.NullString
			owner    sql.NullString
			session  sql.NullString
			repo     sql.NullString
			branch   sql.NullString
			cwd      sql.NullString
			why      sql.NullString
			note     sql.NullString
			direct   sql.NullString
			created  sql.NullInt64
			updated  sql.NullInt64
			expires  sql.NullInt64
			closed   sql.NullInt64
			cause    sql.NullString
			closedNS sql.NullInt64
			group    sql.NullString
		)
		if err := rows.Scan(&v.GPUUUID, &v.Host, &v.Index, &v.Name, &v.MemTotal, &fs, &ls,
			&v.Present, &claimID, &owner, &session, &repo, &branch, &cwd, &why, &note, &direct,
			&created, &updated, &expires, &closed, &cause, &closedNS, &group); err != nil {
			return nil, fmt.Errorf("ledger: %w", err)
		}
		v.FirstSeen, v.LastSeen = fromNS(fs), fromNS(ls)
		if claimID.Valid {
			c := &Claim{
				ID: claimID.String, Host: v.Host, GPUUUID: v.GPUUUID,
				Owner: owner.String, Session: session.String, Repo: repo.String,
				Branch: branch.String, Cwd: cwd.String, Why: why.String,
				Note: note.String, Directive: direct.String,
				Created: fromNS(created.Int64), Updated: fromNS(updated.Int64),
				Expires: fromNS(expires.Int64),
				Closed:  closed.Int64 == 1, CloseCause: cause.String,
				ClosedAt: fromNS(closedNS.Int64), Group: group.String,
			}
			v.Claim = c
		}
		if (opt.Free && v.Claim != nil) || (opt.Held && v.Claim == nil) {
			continue
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	return out, nil
}

// UnattributedThreshold is how much memory with no process behind it counts as
// evidence that something is running.
//
// Used memory no longer includes the driver's reservation: it comes from
// nvmlDeviceGetMemoryInfo_v2, and an idle headless 4090 now reads 2 MiB where it used
// to read 458 (457 of them reserved). The threshold stays at five percent with a 1 GiB
// floor anyway, because two cases still show idle memory with no compute process
// behind it:
//
//   - A workstation's desktop. Graphics contexts are not in NVML's compute-process
//     list, and an idle 5090 driving a display was measured at 587 MiB.
//   - A driver without the v2 query, which still reports total minus free — the
//     reservation (about 0.5 GiB on these cards) plus the rest.
//
// Lowering it would mark those idle cards unclaimed and take them out of every
// automatic placement; a warning that is always on is a warning nobody reads.
func UnattributedThreshold(memTotalMiB uint64) int64 {
	const floorMiB = 1024
	if memTotalMiB == 0 {
		return floorMiB
	}
	pct := int64(memTotalMiB / 20) // 5%
	if pct < floorMiB {
		return floorMiB
	}
	return pct
}

// Idle reports whether the observation shows nothing on the card: the processes
// were enumerated, there are none, and no more memory is in use than an idle card
// shows.
//
// It is the one definition of idle. Claim, `gpu list -safe` and Sweep each had their
// own, and Sweep's ignored unattributed memory, so it could close an overdue claim on
// a card a hidden job was still using.
func (o Observation) Idle() bool {
	return o.ProcsKnown && o.MemKnown && o.Procs == 0 && !o.Hidden()
}

// Hidden reports memory in use that no process on this host accounts for.
func (o Observation) Hidden() bool {
	return o.UnattributedMiB > UnattributedThreshold(o.MemTotalMiB)
}

// Annotate joins observations onto a listing and states, in one line, how the ledger
// and the card disagree.
//
// The four combinations are not symmetric and must not be flattened:
//
//	held  + procs        agrees
//	held  + no procs     the holder may be between jobs; not a reason to evict
//	free  + procs        someone started work without claiming — report it
//	free  + unattributed the same, with the process hidden from this host's /proc
func Annotate(views []CardView, obs map[string]Observation) {
	now := time.Now()
	for i := range views {
		ob, seen := obs[views[i].GPUUUID]
		if !seen {
			continue
		}
		views[i].Observation = &ob
		known := ob.ProcsKnown
		hidden := ob.Hidden()

		switch {
		case views[i].Claim == nil && !known:
			views[i].Conflict = "ledger free; process list unavailable, cannot confirm idle"
		case views[i].Claim == nil && ob.Procs == 0 && !ob.MemKnown:
			views[i].Conflict = "ledger free; memory reading unavailable, cannot confirm idle"
		case views[i].Claim == nil && ob.Procs > 0:
			// Not an error and not a conflict: work is running and nobody wrote down
			// who owns it. The card stays claimable — that is how the operator wants
			// to hand the fleet out — but the next agent deserves to know it is not
			// empty, or it will take a card that is 90% busy.
			msg := fmt.Sprintf("unclaimed: %d process(es) running; "+
				"nobody has claimed this card — confirm with the operator before taking it",
				ob.Procs)
			if hidden {
				msg = fmt.Sprintf("unclaimed: %d process(es) running and %d MiB "+
					"unaccounted for — confirm with the operator before taking it",
					ob.Procs, ob.UnattributedMiB)
			}
			views[i].Conflict = msg
		case views[i].Claim == nil && hidden:
			views[i].Conflict = fmt.Sprintf(
				"unclaimed: %d MiB in use with no process visible on this host "+
					"(another slice, or a stale allocation) — confirm before using it",
				ob.UnattributedMiB)
		case views[i].Claim != nil && views[i].Claim.Overdue() && known && ob.Procs == 0:
			views[i].Conflict = "claim is past its deadline and the card looks idle; " +
				"the holder should release it"
		case views[i].Claim != nil && views[i].Claim.Overdue():
			views[i].Conflict = "claim is past its deadline but the card is busy; " +
				"the holder is still running — extend it or ask the operator"
		case views[i].Claim != nil && known && ob.Procs == 0 && now.Sub(views[i].Claim.Updated) > 10*time.Minute:
			views[i].Conflict = "held, but no process has been seen for a while; " +
				"the holder may have finished without releasing"
		}
	}
}

// History returns recent events for a card, newest first.
//
// This is what turns "who has it now" into "who had it and why", which is the
// question that actually repeats on a fleet shared by agents that do not remember
// each other.
func (s *Store) History(gpuUUID string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT at_ns, kind, claim_id, host, gpu_uuid, actor, detail FROM events`
	var args []any
	if gpuUUID != "" {
		q += ` WHERE gpu_uuid = ?`
		args = append(args, gpuUUID)
	}
	q += ` ORDER BY at_ns DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at int64
		if err := rows.Scan(&at, &e.Kind, &e.ClaimID, &e.Host, &e.GPUUUID, &e.Actor, &e.Detail); err != nil {
			return nil, fmt.Errorf("ledger: %w", err)
		}
		e.At = fromNS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Event is one recorded change.
type Event struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"` // claim, renew, released, expired, adopted, superseded
	ClaimID string    `json:"claim_id,omitempty"`
	Host    string    `json:"host,omitempty"`
	GPUUUID string    `json:"gpu_uuid,omitempty"`
	Actor   string    `json:"actor,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

// CardHost is the host the ledger last saw a card on, and whether it knows the card.
func (s *Store) CardHost(uuid string) (string, bool, error) {
	var host string
	err := s.db.QueryRow(`SELECT host FROM cards WHERE gpu_uuid = ?`, uuid).Scan(&host)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("ledger: %w", err)
	}
	return host, true, nil
}

// CardCount is how many cards the ledger knows on a host.
func (s *Store) CardCount(host string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM cards WHERE host = ?`, host).Scan(&n)
	return n, err
}

// OpenCount is how many cards carry an open claim.
func (s *Store) OpenCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM claims WHERE closed = 0`).Scan(&n)
	return n, err
}

// ForCard returns the open claim on a card, or nil.
func (s *Store) ForCard(uuid string) (*Claim, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM claims WHERE gpu_uuid = ? AND closed = 0`, uuid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	c, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// SortByHost keeps listings in the same order the TUI uses, so a human moving
// between the two does not have to re-find the machine.
func SortByHost(v []CardView) {
	sort.SliceStable(v, func(i, j int) bool {
		if v[i].Host != v[j].Host {
			return v[i].Host < v[j].Host
		}
		return v[i].Index < v[j].Index
	})
}

package ledger

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ClaimRequest is what an agent asks for.
type ClaimRequest struct {
	Host    string
	GPUUUID string // optional: when empty, any free card on Host is chosen
	MinVRAM uint64 // MiB; 0 means no constraint
	// Owner is the declared identity. It is a display string, not a credential:
	// every agent runs as the same unix user, so this cannot be authenticated and
	// must never be used to grant anything.
	Owner   string
	Session string
	Repo    string
	Branch  string
	Cwd     string
	Why     string // untrusted free text
	Note    string
	// For is how long the claim is meant to last. Required: a claim with no end is
	// one nobody will ever notice has been abandoned.
	For time.Duration
	// Directive records the operator's instruction behind this claim, if any.
	Directive string

	// Observed is what the probe saw on each card, keyed by UUID, from the pass that
	// immediately preceded this claim.
	//
	// Without it the ledger can only answer "is somebody's claim on this card", and a
	// card with a training job nobody claimed looks free. The selection then picked it
	// and returned success — the agent was told to run on a card another job was using,
	// which is the collision the whole tool exists to prevent. A card missing from this
	// map is not known to be idle, so it is not offered.
	Observed map[string]Observation
	// RequireIdle makes Observed authoritative: every candidate must be present in it
	// with an enumeration that found nothing. claim sets it whenever it has a fresh
	// observation.
	RequireIdle bool
}

// ClaimResult describes a successful claim.
type ClaimResult struct {
	Claim Claim `json:"claim"`
	Card  Card  `json:"card"`
}

// Card is a card the ledger knows about, joined with its current holder.
type Card struct {
	Host      string    `json:"host"`
	GPUUUID   string    `json:"gpu_uuid"`
	Index     int       `json:"index"`
	Name      string    `json:"name"`
	MemTotal  uint64    `json:"mem_total_mib"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Present is false when the card was in an earlier observation of this host but
	// not in the most recent complete one. Such a card is kept (its history is the
	// point) but is not offered for claim.
	Present bool `json:"present"`

	// Claim is the open claim, if any. Nil means the ledger has no holder — see
	// Observation for whether the card is actually busy.
	//
	// Serialised as an explicit null rather than omitted. An omitted key and "nobody
	// holds this" are different answers, and a reader that does
	// card.get("claim", {}).get("owner") on a free card got None either way — but one
	// that checked `"claim" in card` to decide whether the ledger had spoken at all
	// got the wrong answer. `gpu show` already reports its `owner` this way.
	Claim *Claim `json:"claim"`
}

// Overdue reports whether a claim has passed its declared deadline without being
// released. An overdue claim still holds its card: it is a signal for a human, not
// an eviction.
func (c Claim) Overdue() bool {
	return !c.Closed && !c.Expires.IsZero() && time.Now().After(c.Expires)
}

// maxClaimDuration is the ceiling on a claim's lifetime, in both claim and renew.
//
// It is not a security limit: it is the point past which a "deadline" stops being a
// deadline. Thirty days is already far longer than a training run, and a year
// is indistinguishable from an abandoned claim nobody will ever revisit.
const maxClaimDuration = 30 * 24 * time.Hour

// validateDuration is shared by Claim and Renew so the two cannot drift: a ceiling
// only one of them enforces is a ceiling an agent can step around by renewing.
func validateDuration(name string, d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("%w: %s is required and must be positive", ErrInvalid, name)
	}
	if d > maxClaimDuration {
		return fmt.Errorf("%w: %s may not exceed 30 days", ErrInvalid, name)
	}
	return nil
}

// ownerKey is the canonical form of an owner identity for comparison.
//
// Every write path stores an already-sanitised owner, and every read path compares
// against the same transform: without this, `-as ' alice '` created a claim that
// `-as ' alice '` could not release, because the stored value was trimmed and the
// lookup was not.
func ownerKey(s string) string { return sanitizeText(s, 120) }

// CanonicalOwner is ownerKey for callers outside the package.
//
// `claim list -mine` compares an owner the agent typed against one the store already
// cleaned up, so it has to apply the same transform — otherwise quoting your own name
// with a space fails to find the claim you just made.
func CanonicalOwner(s string) string { return ownerKey(s) }

// Claim takes a card and records why.
//
// The selection is deliberately simple: an explicit UUID, or the first free card on
// the named host that has enough memory. No scoring, no backfilling, no fairness.
// The user assigns work to agents by hand and wants conflicts to reach them, not to
// be resolved by a heuristic in a laptop process.
func (s *Store) Claim(req ClaimRequest) (ClaimResult, error) {
	res, err := s.ClaimMany([]ClaimRequest{req})
	if err != nil {
		var be *BatchError
		if errors.As(err, &be) {
			return ClaimResult{}, be.Err
		}
		return ClaimResult{}, err
	}
	return res[0], nil
}

// BatchError is a ClaimMany failure: which request failed, after how many had been
// chosen. Nothing was recorded — the batch is one transaction — so Done says how
// far the selection got, which is what tells "only 3 of the 8 cards asked for are
// free" from "the third card named is held".
type BatchError struct {
	Index int // the request that failed, 0-based
	Want  int // how many were asked for
	Err   error
}

func (e *BatchError) Error() string {
	if e.Want == 1 {
		return e.Err.Error()
	}
	return fmt.Sprintf("card %d of %d: %v (nothing was claimed)", e.Index+1, e.Want, e.Err)
}
func (e *BatchError) Unwrap() error { return e.Err }

// ClaimMany takes several cards in one transaction: all of them, or none.
//
// It exists for speed and for honesty. An agent asked to "use every free GPU" used to
// loop over `ftop claim`, one process, one observation and one transaction per card;
// on a 40-card fleet that is most of a minute, and a loop that fails half way leaves
// half a fleet claimed for a job that will not run. Here every request is chosen
// against the same transaction, so the second request for "any card on h1" sees the
// first one's claim and takes the next card, and a failure anywhere rolls all of them
// back.
func (s *Store) ClaimMany(reqs []ClaimRequest) ([]ClaimResult, error) {
	if len(reqs) == 0 {
		return nil, fmt.Errorf("%w: nothing to claim", ErrInvalid)
	}
	batch := func(i int, err error) error { return &BatchError{Index: i, Want: len(reqs), Err: err} }
	for i := range reqs {
		if err := normalizeClaim(&reqs[i]); err != nil {
			return nil, batch(i, err)
		}
	}
	now := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()
	out := make([]ClaimResult, 0, len(reqs))
	for i, req := range reqs {
		r, err := claimIn(tx, req, now)
		if err != nil {
			return nil, batch(i, err)
		}
		out = append(out, r)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	return out, nil
}

// normalizeClaim validates a request and sanitises its free text in place.
func normalizeClaim(req *ClaimRequest) error {
	if req.Host == "" && req.GPUUUID == "" {
		return fmt.Errorf("%w: host or gpu_uuid is required", ErrInvalid)
	}
	note := sanitizeText(req.Note, 1000)
	directive := sanitizeText(req.Directive, 300)
	why := sanitizeText(req.Why, 200)
	if err := validateDuration("--for", req.For); err != nil {
		return err
	}
	if why == "" {
		return fmt.Errorf("%w: --why is required", ErrInvalid)
	}
	// Owner is sanitised here and on every later lookup (see ownerKey), so an agent
	// that quotes its own identity back with stray whitespace still owns its claim.
	owner := sanitizeText(req.Owner, 120)
	if owner == "" {
		return fmt.Errorf("%w: owner is required", ErrInvalid)
	}
	req.Owner, req.Why, req.Note, req.Directive = owner, why, note, directive
	return nil
}

// claimIn chooses a card for one normalised request and records the claim inside tx.
func claimIn(tx *sql.Tx, req ClaimRequest, now time.Time) (ClaimResult, error) {
	nowNSV := now.UnixNano()
	id := newID(now)

	gpu, err := chooseCard(tx, req)
	if err != nil {
		return ClaimResult{}, err
	}

	c := Claim{
		ID: id, Host: gpu.Host, GPUUUID: gpu.GPUUUID,
		Owner: req.Owner, Session: req.Session, Repo: req.Repo,
		Branch: req.Branch, Cwd: req.Cwd, Why: req.Why, Note: req.Note,
		Directive: req.Directive,
		Created:   now, Updated: now,
		Expires: now.Add(req.For),
	}
	_, err = tx.Exec(`INSERT INTO claims
		(id, host, gpu_uuid, owner, session, repo, branch, cwd, why, note, directive,
		 created_ns, updated_ns, expires_ns, closed, close_cause, closed_ns)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,'',0)`,
		c.ID, c.Host, c.GPUUUID, c.Owner, c.Session, c.Repo, c.Branch, c.Cwd,
		c.Why, c.Note, c.Directive,
		c.Created.UnixNano(), c.Updated.UnixNano(), c.Expires.UnixNano())
	if err != nil {
		// The partial unique index is the authority on "one open claim per card".
		// Losing that race is an ordinary conflict, not a database failure.
		if isUniqueViolation(err) {
			return ClaimResult{}, conflictErr(tx, gpu.GPUUUID)
		}
		return ClaimResult{}, fmt.Errorf("ledger: %w", err)
	}
	if err := appendEvent(tx, nowNSV, "claim", c.ID, c.Host, c.GPUUUID, c.Owner, c.Why); err != nil {
		return ClaimResult{}, err
	}
	gpu.Claim = &c
	return ClaimResult{Claim: c, Card: gpu}, nil
}

// chooseCard resolves the request to one free card.
func chooseCard(tx *sql.Tx, req ClaimRequest) (Card, error) {
	const cols = `c.gpu_uuid, c.host, c.idx, c.name, c.mem_total, c.first_seen, c.last_seen, c.present`

	scan := func(row *sql.Row) (Card, error) {
		var c Card
		var fs, ls int64
		err := row.Scan(&c.GPUUUID, &c.Host, &c.Index, &c.Name, &c.MemTotal, &fs, &ls, &c.Present)
		c.FirstSeen, c.LastSeen = fromNS(fs), fromNS(ls)
		return c, err
	}

	// An explicit UUID wins, but only if it is free; a held card is a conflict the
	// agent must surface to the operator, not something to route around.
	if req.GPUUUID != "" {
		var held string
		err := tx.QueryRow(
			`SELECT id FROM claims WHERE gpu_uuid = ? AND closed = 0`, req.GPUUUID).Scan(&held)
		if err == nil {
			return Card{}, conflictErr(tx, req.GPUUUID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Card{}, fmt.Errorf("ledger: %w", err)
		}
		row := tx.QueryRow(`SELECT `+cols+` FROM cards c WHERE c.gpu_uuid = ?`, req.GPUUUID)
		c, err := scan(row)
		if errors.Is(err, sql.ErrNoRows) {
			return Card{}, fmt.Errorf("%w: %s", ErrNoSuchCard, req.GPUUUID)
		}
		if err != nil {
			return Card{}, fmt.Errorf("ledger: %w", err)
		}
		// An explicit UUID selects a card; it does not overrule the other
		// constraints. `-host h1 -gpu GPU-3` where GPU-3 lives on h2 used to take the
		// card on h2 and report success, so an agent that named a host it was told to
		// use got a card somewhere else and had no reason to look.
		if req.Host != "" && !strings.EqualFold(c.Host, req.Host) {
			return Card{}, fmt.Errorf("%w: %s is on %s, not %s",
				ErrNoSuchCard, req.GPUUUID, c.Host, req.Host)
		}
		if !c.Present {
			return Card{}, fmt.Errorf("%w: %s is no longer reported by %s", ErrNoSuchCard,
				c.GPUUUID, c.Host)
		}
		if req.MinVRAM > 0 && c.MemTotal < req.MinVRAM {
			return Card{}, fmt.Errorf("%w: %s has %d MiB, less than the %d MiB asked for",
				ErrNoSuchCard, req.GPUUUID, c.MemTotal, req.MinVRAM)
		}
		return c, nil
	}

	// Otherwise any free card on the host that the probe saw idle. Lowest index first so
	// a repeated claim lands on the same card and the operator's mental map stays
	// stable.
	q := `SELECT ` + cols + ` FROM cards c
		WHERE c.host = ? AND c.present = 1
		  AND NOT EXISTS (SELECT 1 FROM claims k WHERE k.gpu_uuid = c.gpu_uuid AND k.closed = 0)`
	args := []any{req.Host}
	if req.MinVRAM > 0 {
		q += ` AND c.mem_total >= ?`
		args = append(args, req.MinVRAM)
	}
	q += ` ORDER BY c.idx`

	rows, err := tx.Query(q, args...)
	if err != nil {
		return Card{}, fmt.Errorf("ledger: %w", err)
	}
	var candidates []Card
	for rows.Next() {
		var c Card
		var fs, ls int64
		if err := rows.Scan(&c.GPUUUID, &c.Host, &c.Index, &c.Name, &c.MemTotal, &fs, &ls, &c.Present); err != nil {
			rows.Close()
			return Card{}, fmt.Errorf("ledger: %w", err)
		}
		c.FirstSeen, c.LastSeen = fromNS(fs), fromNS(ls)
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Card{}, fmt.Errorf("ledger: %w", err)
	}
	// The first candidate the probe found empty. A card it did not enumerate is
	// skipped: "does not know" is not "idle", and picking it is how an agent ended up
	// on a card with another job's processes on it.
	var busy int
	var busyUUID string
	for _, c := range candidates {
		if !idleForClaim(req, c) {
			if ob, ok := req.Observed[c.GPUUUID]; ok && ob.ProcsKnown && ob.Procs > 0 {
				busy++
				busyUUID = c.GPUUUID
			}
			continue
		}
		return c, nil
	}
	// Nothing selectable. Distinguish the reasons: the agent's next action differs for
	// each, and a single "unavailable" tells it nothing.
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM cards WHERE host = ? AND present = 1`,
		req.Host).Scan(&n); err != nil {
		return Card{}, fmt.Errorf("ledger: %w", err)
	}
	if n == 0 {
		return Card{}, fmt.Errorf("%w: no cards known on %s (has ftop observed it?)",
			ErrNoSuchCard, req.Host)
	}
	var held int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM claims k JOIN cards c ON c.gpu_uuid = k.gpu_uuid
		 WHERE c.host = ? AND k.closed = 0`, req.Host).Scan(&held); err != nil {
		return Card{}, fmt.Errorf("ledger: %w", err)
	}
	if held >= n {
		holders, err := holdersOnHost(tx, req.Host)
		if err != nil {
			return Card{}, err
		}
		return Card{}, &NoFreeCardError{Host: req.Host, Cards: n, Held: held, Holders: holders}
	}
	if req.RequireIdle {
		if busy > 0 {
			return Card{}, fmt.Errorf("%w: every unclaimed card on %s has processes on it "+
				"(%s); someone is running work nobody claimed. Take an idle card from "+
				"`ftop gpu list -safe`, or ask the operator",
				ErrNoSuchCard, req.Host, busyUUID)
		}
		return Card{}, fmt.Errorf("%w: no card on %s has been observed idle; run "+
			"`ftop gpu list -safe` to see which are", ErrNoSuchCard, req.Host)
	}
	return Card{}, fmt.Errorf("%w: no free card on %s with at least %d MiB",
		ErrNoSuchCard, req.Host, req.MinVRAM)
}

// idleForClaim reports whether a card may be handed out automatically.
//
// When the caller has an observation, only a card it enumerated and found empty
// qualifies. A claim that names an exact UUID is exempt: there the agent has chosen,
// and refusing would only make it claim the same card under another name.
func idleForClaim(req ClaimRequest, c Card) bool {
	if !req.RequireIdle {
		return true
	}
	ob, ok := req.Observed[c.GPUUUID]
	if !ok || !ob.ProcsKnown {
		return false
	}
	return ob.Procs == 0 && ob.UnattributedMiB <= UnattributedThreshold(ob.MemTotalMiB)
}

// conflictErr builds the error an agent needs to decide whether to escalate.
//
// It carries the holder, their reason and how long the claim has left, because the
// whole point of the ledger is that an agent can find out who has the card and why,
// then either wait or ask the operator — instead of guessing.
func conflictErr(tx *sql.Tx, uuid string) error {
	var (
		owner, why, id, session, directive string
		created, expires                   int64
	)
	err := tx.QueryRow(`SELECT id, owner, why, session, directive, created_ns, expires_ns
		FROM claims WHERE gpu_uuid = ? AND closed = 0`, uuid).
		Scan(&id, &owner, &why, &session, &directive, &created, &expires)
	if err != nil {
		return ErrHeld
	}
	return &HeldError{
		GPUUUID: uuid, ClaimID: id, Owner: owner, Why: why, Session: session,
		Directive: directive, Since: fromNS(created), Expires: fromNS(expires),
	}
}

// HeldError is a conflict that names the holder.
type HeldError struct {
	GPUUUID string `json:"gpu_uuid"`
	ClaimID string `json:"claim_id"`
	Owner   string `json:"owner"`
	Session string `json:"session,omitempty"`
	Why     string `json:"why"`
	// Directive is the operator instruction behind the claim. It is what decides a
	// collision between two agents, so it travels with the conflict rather than
	// needing a second lookup.
	Directive string    `json:"directive,omitempty"`
	Since     time.Time `json:"since"`
	Expires   time.Time `json:"expires"`
}

func (e *HeldError) Error() string { return "card is already held" }
func (e *HeldError) Unwrap() error { return ErrHeld }

// NoFreeCardError says the host exists but has nothing to give. It is distinct from
// a named-holder conflict: "all four cards are held" and "that card is held by X"
// lead to different next actions.
type NoFreeCardError struct {
	Host    string      `json:"host"`
	Cards   int         `json:"cards"`
	Held    int         `json:"held"`
	Holders []CardOwner `json:"holders,omitempty"`
}

// CardOwner is the minimum an agent needs to route around a full host: which card,
// who has it, why, and when it is due back.
type CardOwner struct {
	GPUUUID   string    `json:"gpu_uuid"`
	ClaimID   string    `json:"claim_id"`
	Owner     string    `json:"owner"`
	Session   string    `json:"session,omitempty"`
	Why       string    `json:"why"`
	Directive string    `json:"directive,omitempty"`
	Expires   time.Time `json:"expires"`
}

func holdersOnHost(tx *sql.Tx, host string) ([]CardOwner, error) {
	rows, err := tx.Query(`SELECT k.gpu_uuid, k.id, k.owner, k.session, k.why, k.directive,
		k.expires_ns
		FROM claims k JOIN cards c ON c.gpu_uuid = k.gpu_uuid
		WHERE c.host = ? AND k.closed = 0 ORDER BY c.idx`, host)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	defer rows.Close()
	out := []CardOwner{}
	for rows.Next() {
		var o CardOwner
		var exp int64
		if err := rows.Scan(&o.GPUUUID, &o.ClaimID, &o.Owner, &o.Session, &o.Why, &o.Directive,
			&exp); err != nil {
			return nil, fmt.Errorf("ledger: %w", err)
		}
		o.Expires = fromNS(exp)
		out = append(out, o)
	}
	return out, rows.Err()
}

func (e *NoFreeCardError) Error() string {
	return fmt.Sprintf("all %d cards on %s are held; run `ftop gpu list --host %s` to see by whom",
		e.Cards, e.Host, e.Host)
}
func (e *NoFreeCardError) Unwrap() error { return ErrHeld }

// Release closes a claim its owner asked to close.
func (s *Store) Release(id, owner string, cause string) (Claim, error) {
	return s.ReleaseAs(id, owner, cause, owner)
}

// ReleaseByAgentKind closes a claim held by the same KIND of agent, in the same place,
// whatever session made it.
//
// This is the restart case: an agent's identity includes a session id that changes
// every time it starts, so the agent that made a claim is not the agent that comes
// back. The kind ("agent-a" in "agent-a/1a2b3c4d") is what survives.
//
// The kind alone is not enough, though. Two conversations of one runtime run at the
// same time — two windows on one project, two tasks on one machine — and matching on
// kind would let either one close the other's claim, which is the eviction the ledger
// exists to prevent. So the claim must also have been made from the same repo, branch
// and working directory: that is what "the same agent, restarted" looks like, and two
// concurrent conversations on one project are the case the operator arbitrates.
//
// It stays narrower than the operator override: another kind of agent's claim is still
// refused, and the event records which session actually did the release.
//
// A claim with no recorded location cannot be matched this way, so it is refused; -as
// with the exact owner still works, and so does the operator's -any-owner.
func (s *Store) ReleaseByAgentKind(id, kind, actor, repo, branch, cwd string) (Claim, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Claim{}, fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()
	c, err := loadClaim(tx, id)
	if err != nil {
		return Claim{}, err
	}
	if c.Closed {
		return Claim{}, fmt.Errorf("%w: %s", ErrAlreadyClosed, id)
	}
	ownerKind, _, hasSession := strings.Cut(c.Owner, "/")
	// hasSession matters: a plain name like "agent-a" has no kind/session split, so it
	// has no kind to match on. Treating the whole name as a kind would make every
	// explicitly-named owner releasable by any caller that passed the same string.
	if kind == "" || !hasSession || !strings.EqualFold(ownerKind, kind) {
		return Claim{}, fmt.Errorf("%w: held by %s", ErrNotOwner, c.Owner)
	}
	// Same kind, same place. A recorded location that differs is a different
	// conversation, even of the same runtime.
	if c.Cwd == "" || c.Cwd != cwd || c.Repo != repo || c.Branch != branch {
		return Claim{}, fmt.Errorf("%w: held by %s in another session (%s); only that "+
			"session or the operator can release it", ErrNotOwner, c.Owner, c.Cwd)
	}
	tx.Rollback()
	return s.ReleaseAs(id, c.Owner, "released", actor)
}

// ReleaseAs closes a claim and records who did it.
//
// Only the owning agent may release its own claim; an empty owner skips that check
// and is the operator's override. There is no in-band force for an agent: one that
// could evict another is one that can destroy hours of work, and the user's stated
// path for that is to ask them. `actor` is separate from `owner` so an override is
// recorded as the identity that ran it rather than as the claim's owner, which would
// make `ftop history` say the holder released a claim it did not.
func (s *Store) ReleaseAs(id, owner string, cause string, actor string) (Claim, error) {
	now := nowNS()
	tx, err := s.db.Begin()
	if err != nil {
		return Claim{}, fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()

	c, err := loadClaim(tx, id)
	if err != nil {
		return Claim{}, err
	}
	if c.Closed {
		return Claim{}, fmt.Errorf("%w: %s", ErrAlreadyClosed, id)
	}
	if owner != "" && c.Owner != ownerKey(owner) {
		return Claim{}, fmt.Errorf("%w: held by %s", ErrNotOwner, c.Owner)
	}
	if cause == "" {
		cause = "released"
	}
	if _, err := tx.Exec(`UPDATE claims SET closed = 1, closed_ns = ?, close_cause = ?, updated_ns = ?
		WHERE id = ?`, now, cause, now, id); err != nil {
		return Claim{}, fmt.Errorf("ledger: %w", err)
	}
	if err := appendEvent(tx, now, cause, id, c.Host, c.GPUUUID, actor, ""); err != nil {
		return Claim{}, err
	}
	if err := tx.Commit(); err != nil {
		return Claim{}, fmt.Errorf("ledger: %w", err)
	}
	c.Closed, c.CloseCause, c.ClosedAt, c.Updated = true, cause, fromNS(now), fromNS(now)
	return c, nil
}

// Renew extends a live claim.
//
// This is for an agent that knows its job will run longer. It is not a heartbeat:
// nothing requires periodic contact, and a claim that is never renewed simply keeps
// its original deadline.
func (s *Store) Renew(id, owner string, forDur time.Duration) (Claim, error) {
	if err := validateDuration("--for", forDur); err != nil {
		return Claim{}, err
	}
	now := nowNS()
	tx, err := s.db.Begin()
	if err != nil {
		return Claim{}, fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()

	c, err := loadClaim(tx, id)
	if err != nil {
		return Claim{}, err
	}
	if c.Closed {
		return Claim{}, fmt.Errorf("%w: %s", ErrAlreadyClosed, id)
	}
	if owner != "" && c.Owner != ownerKey(owner) {
		return Claim{}, fmt.Errorf("%w: held by %s", ErrNotOwner, c.Owner)
	}
	exp := time.Unix(0, now).Add(forDur)
	if _, err := tx.Exec(`UPDATE claims SET expires_ns = ?, updated_ns = ? WHERE id = ?`,
		exp.UnixNano(), now, id); err != nil {
		return Claim{}, fmt.Errorf("ledger: %w", err)
	}
	if err := appendEvent(tx, now, "renew", id, c.Host, c.GPUUUID, owner, ""); err != nil {
		return Claim{}, err
	}
	if err := tx.Commit(); err != nil {
		return Claim{}, fmt.Errorf("ledger: %w", err)
	}
	c.Expires, c.Updated = exp, fromNS(now)
	return c, nil
}

func loadClaim(tx *sql.Tx, id string) (Claim, error) {
	var c Claim
	var created, updated, expires, closedNS int64
	var closed int
	err := tx.QueryRow(`SELECT id, host, gpu_uuid, owner, session, repo, branch, cwd, why, note,
		directive, created_ns, updated_ns, expires_ns, closed, close_cause, closed_ns
		FROM claims WHERE id = ?`, id).
		Scan(&c.ID, &c.Host, &c.GPUUUID, &c.Owner, &c.Session, &c.Repo, &c.Branch, &c.Cwd,
			&c.Why, &c.Note, &c.Directive, &created, &updated, &expires, &closed,
			&c.CloseCause, &closedNS)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Claim{}, fmt.Errorf("ledger: %w", err)
	}
	c.Created, c.Updated, c.Expires = fromNS(created), fromNS(updated), fromNS(expires)
	c.Closed, c.ClosedAt = closed == 1, fromNS(closedNS)
	return c, nil
}

// Get returns one claim, open or closed.
func (s *Store) Get(id string) (Claim, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Claim{}, fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()
	c, err := loadClaim(tx, id)
	if err != nil {
		return Claim{}, err
	}
	return c, nil
}

// appendEvent records history. A claim row answers "who has it now"; events answer
// "who had it before, and how did it end" — which is the question that repeats on a
// shared fleet.
func appendEvent(tx *sql.Tx, at int64, kind, claimID, host, uuid, actor, detail string) error {
	_, err := tx.Exec(`INSERT INTO events (at_ns, kind, claim_id, host, gpu_uuid, actor, detail)
		VALUES (?,?,?,?,?,?,?)`, at, kind, claimID, host, uuid, actor, sanitizeText(detail, 500))
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	return nil
}

// Sweep closes overdue claims whose card is OBSERVED idle, and returns the ones it
// left alone because something may still be running.
//
// This is the whole of ftop's automatic cleanup, and its shape is the point. The
// deadline alone never releases a card: a job that overran its estimate is still a
// job, and handing its card to the next agent is the one failure the user said must
// go to them. Only an observation that says "this card has no processes at all"
// closes the claim — and `known == false` (the probe could not enumerate) is not
// that observation.
func (s *Store) Sweep(observed map[string]Observation) (released []Claim, kept []Claim, err error) {
	now := nowNS()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id FROM claims WHERE closed = 0 AND expires_ns <= ?`, now)
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("ledger: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("ledger: %w", err)
	}

	for _, id := range ids {
		c, err := loadClaim(tx, id)
		if err != nil {
			return nil, nil, err
		}
		ob, seen := observed[c.GPUUUID]
		idle := seen && ob.ProcsKnown && ob.Procs == 0
		if !idle {
			kept = append(kept, c)
			continue
		}
		if _, err := tx.Exec(`UPDATE claims SET closed = 1, closed_ns = ?, close_cause = 'expired',
			updated_ns = ? WHERE id = ? AND closed = 0`, now, now, id); err != nil {
			return nil, nil, fmt.Errorf("ledger: %w", err)
		}
		if err := appendEvent(tx, now, "expired", id, c.Host, c.GPUUUID, "",
			"deadline passed and the card was observed idle"); err != nil {
			return nil, nil, err
		}
		c.Closed, c.CloseCause, c.ClosedAt = true, "expired", fromNS(now)
		released = append(released, c)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("ledger: %w", err)
	}
	return released, kept, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

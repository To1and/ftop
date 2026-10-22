package ledger

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ClaimRequest is what an agent asks for.
//
// Where the cards come from is one of three things: the exact card named by
// GPUUUID, idle cards on the host named by Host, or — when neither is given —
// wherever they fit best (see place).
type ClaimRequest struct {
	Host    string // optional: take the cards on this host
	GPUUUID string // optional: take this exact card
	// Hosts limits automatic placement to these machines; nil leaves every host the
	// ledger knows. It is ignored when Host or GPUUUID names the place.
	Hosts []string
	// ExcludeHosts keeps automatic placement off these machines, whatever Hosts says.
	// It is the negative form of Hosts, for "use the fleet except that one" — an
	// instruction an operator gives in a sentence and could not express before without
	// listing every other machine by name.
	ExcludeHosts []string
	// Count is how many cards, all or none. Zero means one. When Max is larger, Count
	// is the minimum and the request takes as many as are idle, up to Max.
	Count int
	// Max is the upper bound of an elastic request; zero means the same as Count, which
	// is the ordinary all-or-none claim.
	Max int
	// Spread lets automatic placement put the cards on different hosts: separate
	// one-card jobs rather than one job on several cards. Without it every card is on
	// one host, because a job across machines needs a network ftop knows nothing about.
	Spread  bool
	MinVRAM uint64 // MiB; 0 means no constraint
	// Adopt takes the card named by GPUUUID although processes are running on it:
	// they are the caller's own, started before it claimed.
	Adopt bool
	// Keep turns off idle reclaim for this claim: the holder is between jobs on purpose,
	// so an idle card is not an abandoned one. Without it a claim whose card shows no
	// work for IdleReclaimAfter is released before its deadline.
	Keep bool
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
	// RequireIdle makes Observed authoritative: every card handed out, a named one
	// included, must be present in it with an enumeration that found nothing. claim
	// sets it whenever it has a fresh observation.
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

// Claim takes one card and records why. It is Allocate for a request that wants
// a single card and only needs that card back.
func (s *Store) Claim(req ClaimRequest) (ClaimResult, error) {
	a, err := s.Allocate(req)
	if err != nil {
		return ClaimResult{}, err
	}
	return a.Claims[0], nil
}

// Allocation is what one claim request took.
type Allocation struct {
	// Group names every card taken; release and renew accept it.
	Group     string        `json:"group"`
	Claims    []ClaimResult `json:"claims"`
	Placement Placement     `json:"placement"`
}

// Allocate takes the cards a request asks for in one transaction: all of them, or
// none.
//
// One transaction is the point. An agent that needed four cards used to loop over
// `ftop claim`, and a loop that failed half way left half a job's cards claimed for a
// job that would not run; two agents looping on one host took turns and neither got
// a full set. Every transaction takes the write lock up front (see Open), so the
// choice and the records cannot interleave with another claim.
func (s *Store) Allocate(req ClaimRequest) (Allocation, error) {
	if err := normalizeClaim(&req); err != nil {
		return Allocation{}, err
	}
	now := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return Allocation{}, fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()
	cards, plc, err := chooseCards(tx, req)
	if err != nil {
		return Allocation{}, err
	}
	out := Allocation{Group: newGroupID(now), Placement: plc}
	for _, card := range cards {
		r, err := recordClaim(tx, req, card, out.Group, now)
		if err != nil {
			return Allocation{}, err
		}
		out.Claims = append(out.Claims, r)
	}
	if err := tx.Commit(); err != nil {
		return Allocation{}, fmt.Errorf("ledger: %w", err)
	}
	return out, nil
}

// normalizeClaim validates a request and sanitises its free text in place.
func normalizeClaim(req *ClaimRequest) error {
	if req.Count == 0 {
		req.Count = 1
	}
	if req.Max == 0 {
		req.Max = req.Count
	}
	switch {
	case req.Count < 0 || req.Max < 0:
		return fmt.Errorf("%w: the card count must be positive", ErrInvalid)
	case req.Max < req.Count:
		return fmt.Errorf("%w: the maximum (%d) is below the minimum (%d)", ErrInvalid,
			req.Max, req.Count)
	case req.GPUUUID != "" && req.Max > 1:
		return fmt.Errorf("%w: a named card is one card; name a host to take several", ErrInvalid)
	case req.Spread && (req.Host != "" || req.GPUUUID != ""):
		return fmt.Errorf("%w: spread is for automatic placement; it cannot be combined "+
			"with a named host or card", ErrInvalid)
	case req.Adopt && req.GPUUUID == "":
		return fmt.Errorf("%w: adopt takes a card by name; give its uuid", ErrInvalid)
	case req.Max > req.Count && (req.Host != "" || req.GPUUUID != ""):
		return fmt.Errorf("%w: a range is placed by ftop; it cannot be combined with a "+
			"named host or card", ErrInvalid)
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

// recordClaim writes the claim on one chosen card inside tx.
func recordClaim(tx *sql.Tx, req ClaimRequest, gpu Card, group string, now time.Time) (ClaimResult, error) {
	c := Claim{
		ID: newID(now), Host: gpu.Host, GPUUUID: gpu.GPUUUID,
		Owner: req.Owner, Session: req.Session, Repo: req.Repo,
		Branch: req.Branch, Cwd: req.Cwd, Why: req.Why, Note: req.Note,
		Directive: req.Directive, Group: group,
		Created: now, Updated: now,
		Expires: now.Add(req.For),
		// Claiming a card is the assertion that it will be used, so the reclaim clock
		// starts now rather than at the zero time (which would read as "idle forever").
		LastBusy: now, Keep: req.Keep,
	}
	_, err := tx.Exec(`INSERT INTO claims
		(id, host, gpu_uuid, owner, session, repo, branch, cwd, why, note, directive,
		 created_ns, updated_ns, expires_ns, closed, close_cause, closed_ns, grp,
		 last_busy_ns, keep)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,'',0,?,?,?)`,
		c.ID, c.Host, c.GPUUUID, c.Owner, c.Session, c.Repo, c.Branch, c.Cwd,
		c.Why, c.Note, c.Directive,
		c.Created.UnixNano(), c.Updated.UnixNano(), c.Expires.UnixNano(), c.Group,
		c.LastBusy.UnixNano(), boolInt(c.Keep))
	if err != nil {
		// The partial unique index is the authority on "one open claim per card".
		// Losing that race is an ordinary conflict, not a database failure.
		if isUniqueViolation(err) {
			return ClaimResult{}, conflictErr(tx, gpu.GPUUUID)
		}
		return ClaimResult{}, fmt.Errorf("ledger: %w", err)
	}
	if err := appendEvent(tx, now.UnixNano(), "claim", c.ID, c.Host, c.GPUUUID, c.Owner, c.Why); err != nil {
		return ClaimResult{}, err
	}
	gpu.Claim = &c
	return ClaimResult{Claim: c, Card: gpu}, nil
}

// chooseCards resolves a request to the cards it gets: the named card, cards on the
// named host, or a placement.
func chooseCards(tx *sql.Tx, req ClaimRequest) ([]Card, Placement, error) {
	if req.GPUUUID != "" {
		c, err := chooseNamedCard(tx, req)
		if err != nil {
			return nil, Placement{}, err
		}
		return []Card{c}, Placement{Mode: "gpu",
			Reason: fmt.Sprintf("%s on %s was named", c.GPUUUID, c.Host)}, nil
	}
	slots, err := loadSlots(tx, req)
	if err != nil {
		return nil, Placement{}, err
	}
	if req.Host != "" {
		return chooseOnHost(tx, req, slots)
	}
	return placeRange(slots, req.Count, req.Max, req.Spread)
}

// chooseNamedCard checks that the card a request names may be taken.
func chooseNamedCard(tx *sql.Tx, req ClaimRequest) (Card, error) {
	// A held card is a conflict the agent must surface to the operator, not something
	// to route around.
	var held string
	err := tx.QueryRow(
		`SELECT id FROM claims WHERE gpu_uuid = ? AND closed = 0`, req.GPUUUID).Scan(&held)
	if err == nil {
		return Card{}, conflictErr(tx, req.GPUUUID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Card{}, fmt.Errorf("ledger: %w", err)
	}
	var c Card
	var fs, ls int64
	err = tx.QueryRow(`SELECT gpu_uuid, host, idx, name, mem_total, first_seen, last_seen, present
		FROM cards WHERE gpu_uuid = ?`, req.GPUUUID).
		Scan(&c.GPUUUID, &c.Host, &c.Index, &c.Name, &c.MemTotal, &fs, &ls, &c.Present)
	if errors.Is(err, sql.ErrNoRows) {
		return Card{}, fmt.Errorf("%w: %s", ErrNoSuchCard, req.GPUUUID)
	}
	if err != nil {
		return Card{}, fmt.Errorf("ledger: %w", err)
	}
	c.FirstSeen, c.LastSeen = fromNS(fs), fromNS(ls)
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
	// Naming a card does not make it idle. A named card used to skip the check, so an
	// agent could take a card a job nobody claimed was running on; the probe's answer
	// applies here like everywhere else. Adopt is the one way past it, for the caller
	// whose own unclaimed job that is.
	if req.RequireIdle && !req.Adopt {
		if ob, seen := req.Observed[c.GPUUUID]; !seen || !ob.Idle() {
			return Card{}, &NotIdleError{GPUUUID: c.GPUUUID, Host: c.Host, Observed: seen,
				Procs: ob.Procs, ProcsKnown: ob.ProcsKnown, UnattributedMiB: ob.UnattributedMiB}
		}
	}
	return c, nil
}

// chooseOnHost takes the lowest-index idle cards on the named host, so a repeated
// claim lands on the same cards and the operator's mental map stays stable.
func chooseOnHost(tx *sql.Tx, req ClaimRequest, slots []*slot) ([]Card, Placement, error) {
	var sl *slot
	for _, x := range slots {
		if x.host == req.Host {
			sl = x
		}
	}
	if sl != nil && len(sl.idle) >= req.Count {
		cards := sl.idle[:req.Count]
		return cards, Placement{Mode: "host", Reason: fmt.Sprintf(
			"%s was named; %d of its %d cards were idle", sl.host, len(sl.idle), sl.cards)}, nil
	}
	if sl != nil && len(sl.idle) > 0 {
		return nil, Placement{}, &InsufficientError{Want: req.Count, Host: sl.host,
			MostOnOneHost: len(sl.idle), BestHost: sl.host, Idle: len(sl.idle), Hosts: 1}
	}
	// Nothing selectable. Distinguish the reasons: the agent's next action differs for
	// each, and a single "unavailable" tells it nothing.
	if sl == nil || sl.cards == 0 {
		return nil, Placement{}, fmt.Errorf("%w: no cards known on %s (has ftop observed it?)",
			ErrNoSuchCard, req.Host)
	}
	if sl.held >= sl.cards {
		holders, err := holdersOnHost(tx, req.Host)
		if err != nil {
			return nil, Placement{}, err
		}
		return nil, Placement{}, &NoFreeCardError{Host: req.Host, Cards: sl.cards, Held: sl.held,
			Holders: holders}
	}
	if req.RequireIdle && sl.busy > 0 {
		return nil, Placement{}, fmt.Errorf("%w: every unclaimed card on %s has processes on it "+
			"(%s); someone is running work nobody claimed. Take an idle card from "+
			"`ftop gpu list -safe`, or ask the operator",
			ErrNoSuchCard, req.Host, sl.busyUUID)
	}
	if req.RequireIdle && sl.small == 0 {
		return nil, Placement{}, fmt.Errorf("%w: no card on %s has been observed idle; run "+
			"`ftop gpu list -safe` to see which are", ErrNoSuchCard, req.Host)
	}
	return nil, Placement{}, fmt.Errorf("%w: no free card on %s with at least %d MiB",
		ErrNoSuchCard, req.Host, req.MinVRAM)
}

// idleForClaim reports whether a card may be handed out: with an observation, only
// a card it enumerated and found empty qualifies.
func idleForClaim(req ClaimRequest, c Card) bool {
	if !req.RequireIdle {
		return true
	}
	ob, ok := req.Observed[c.GPUUUID]
	return ok && ob.Idle()
}

// NotIdleError refuses a named card the observation does not show idle.
type NotIdleError struct {
	GPUUUID string `json:"gpu_uuid"`
	Host    string `json:"host"`
	// Observed is false when the pass before the claim did not see the card at all.
	Observed        bool  `json:"observed"`
	Procs           int   `json:"procs"`
	ProcsKnown      bool  `json:"procs_known"`
	UnattributedMiB int64 `json:"unattributed_mib,omitempty"`
}

func (e *NotIdleError) Error() string {
	switch {
	case !e.Observed:
		return fmt.Sprintf("%s on %s was not observed, so ftop cannot confirm it is idle",
			e.GPUUUID, e.Host)
	case !e.ProcsKnown:
		return fmt.Sprintf("%s on %s: its process list could not be read, so ftop cannot "+
			"confirm it is idle", e.GPUUUID, e.Host)
	case e.Procs > 0:
		return fmt.Sprintf("%s on %s has %d process(es) running that nobody claimed",
			e.GPUUUID, e.Host, e.Procs)
	}
	return fmt.Sprintf("%s on %s has %d MiB in use with no process on that host",
		e.GPUUUID, e.Host, e.UnattributedMiB)
}
func (e *NotIdleError) Unwrap() error { return ErrNotIdle }

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
	if err := closeIn(tx, &c, cause, actor, now); err != nil {
		return Claim{}, err
	}
	if err := tx.Commit(); err != nil {
		return Claim{}, fmt.Errorf("ledger: %w", err)
	}
	return c, nil
}

// closeIn closes one open claim inside tx and records who did it.
func closeIn(tx *sql.Tx, c *Claim, cause, actor string, now int64) error {
	if cause == "" {
		cause = "released"
	}
	if _, err := tx.Exec(`UPDATE claims SET closed = 1, closed_ns = ?, close_cause = ?, updated_ns = ?
		WHERE id = ?`, now, cause, now, c.ID); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	if err := appendEvent(tx, now, cause, c.ID, c.Host, c.GPUUUID, actor, ""); err != nil {
		return err
	}
	c.Closed, c.CloseCause, c.ClosedAt, c.Updated = true, cause, fromNS(now), fromNS(now)
	return nil
}

// Group returns every claim in a group, open or closed, in host and card order.
func (s *Store) Group(group string) ([]Claim, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()
	return groupIn(tx, group, false)
}

// groupIn loads a group's claims, only the open ones when openOnly is set. A group
// with no claims at all is ErrNotFound; one whose claims are all closed, asked for
// its open ones, is ErrAlreadyClosed.
func groupIn(tx *sql.Tx, group string, openOnly bool) ([]Claim, error) {
	rows, err := tx.Query(`SELECT k.id, k.closed FROM claims k
		LEFT JOIN cards c ON c.gpu_uuid = k.gpu_uuid
		WHERE k.grp = ? ORDER BY k.host, c.idx, k.id`, group)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	var ids []string
	found := false
	for rows.Next() {
		var id string
		var closed bool
		if err := rows.Scan(&id, &closed); err != nil {
			rows.Close()
			return nil, fmt.Errorf("ledger: %w", err)
		}
		found = true
		if !openOnly || !closed {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, group)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: every claim in %s", ErrAlreadyClosed, group)
	}
	out := make([]Claim, 0, len(ids))
	for _, id := range ids {
		c, err := loadClaim(tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// ReleaseGroup closes every open claim in a group, or none of them.
//
// The owner rule is ReleaseAs's, applied to each claim before any is closed: a group
// is one command's cards, so its claims share an owner, and a group that somehow
// mixed owners is refused whole rather than released in part.
func (s *Store) ReleaseGroup(group, owner, cause, actor string) ([]Claim, error) {
	now := nowNS()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()
	claims, err := groupIn(tx, group, true)
	if err != nil {
		return nil, err
	}
	for _, c := range claims {
		if owner != "" && c.Owner != ownerKey(owner) {
			return nil, fmt.Errorf("%w: %s is held by %s", ErrNotOwner, c.ID, c.Owner)
		}
	}
	for i := range claims {
		if err := closeIn(tx, &claims[i], cause, actor, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	return claims, nil
}

// RenewGroup extends every open claim in a group to the same new deadline. A job's
// cards end together, and renewing them one by one is how one card was forgotten and
// expired under a running job.
func (s *Store) RenewGroup(group, owner string, forDur time.Duration) ([]Claim, error) {
	if err := validateDuration("--for", forDur); err != nil {
		return nil, err
	}
	now := nowNS()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()
	claims, err := groupIn(tx, group, true)
	if err != nil {
		return nil, err
	}
	for _, c := range claims {
		if owner != "" && c.Owner != ownerKey(owner) {
			return nil, fmt.Errorf("%w: %s is held by %s", ErrNotOwner, c.ID, c.Owner)
		}
	}
	exp := time.Unix(0, now).Add(forDur)
	for i := range claims {
		if err := renewIn(tx, &claims[i], owner, exp, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	return claims, nil
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
	if err := renewIn(tx, &c, owner, time.Unix(0, now).Add(forDur), now); err != nil {
		return Claim{}, err
	}
	if err := tx.Commit(); err != nil {
		return Claim{}, fmt.Errorf("ledger: %w", err)
	}
	return c, nil
}

// renewIn moves one open claim's deadline inside tx.
//
// updated_ns is what a reclaiming observer reads, so a renew is also a statement that
// the card is still wanted: it restarts the idle clock even if the card is between
// jobs. That is the whole reason a holder has to do something — silence is what
// eventually gives the card back.
func renewIn(tx *sql.Tx, c *Claim, actor string, exp time.Time, now int64) error {
	if _, err := tx.Exec(`UPDATE claims SET expires_ns = ?, updated_ns = ?, last_busy_ns = ?
		WHERE id = ?`, exp.UnixNano(), now, now, c.ID); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	if err := appendEvent(tx, now, "renew", c.ID, c.Host, c.GPUUUID, actor, ""); err != nil {
		return err
	}
	c.Expires, c.Updated, c.LastBusy = exp, fromNS(now), fromNS(now)
	return nil
}

// SetKeep turns the idle reclaim for one claim, or a whole group, on or off.
//
// It is the holder's own switch and follows the holder rule: only the owner may flip
// it, and a group is flipped whole.
func (s *Store) SetKeep(id, owner string, keep bool) ([]Claim, error) {
	now := nowNS()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()
	cs, isGroup, err := claimsFor(tx, id)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		if owner != "" && c.Owner != ownerKey(owner) {
			return nil, fmt.Errorf("%w: %s is held by %s", ErrNotOwner, c.ID, c.Owner)
		}
	}
	for i := range cs {
		if _, err := tx.Exec(`UPDATE claims SET keep = ?, updated_ns = ? WHERE id = ?`,
			boolInt(keep), now, cs[i].ID); err != nil {
			return nil, fmt.Errorf("ledger: %w", err)
		}
		detail := "reclaim turned off: the card is kept while idle"
		if !keep {
			detail = "reclaim turned back on"
		}
		if err := appendEvent(tx, now, "keep", cs[i].ID, cs[i].Host, cs[i].GPUUUID, owner,
			detail); err != nil {
			return nil, err
		}
		cs[i].Keep, cs[i].Updated = keep, fromNS(now)
	}
	if isGroup {
		for i := range cs {
			cs[i].Group = id
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	return cs, nil
}

// claimsFor resolves an id that may be a claim or a group to its open claims.
func claimsFor(tx *sql.Tx, id string) (cs []Claim, isGroup bool, err error) {
	if IsGroupID(id) {
		cs, err = groupIn(tx, id, true)
		return cs, true, err
	}
	c, err := loadClaim(tx, id)
	if err != nil {
		return nil, false, err
	}
	if c.Closed {
		return nil, false, fmt.Errorf("%w: %s", ErrAlreadyClosed, id)
	}
	return []Claim{c}, false, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func loadClaim(tx *sql.Tx, id string) (Claim, error) {
	var c Claim
	var created, updated, expires, closedNS, busyNS int64
	var closed, keep int
	err := tx.QueryRow(`SELECT id, host, gpu_uuid, owner, session, repo, branch, cwd, why, note,
		directive, created_ns, updated_ns, expires_ns, closed, close_cause, closed_ns, grp,
		last_busy_ns, keep
		FROM claims WHERE id = ?`, id).
		Scan(&c.ID, &c.Host, &c.GPUUUID, &c.Owner, &c.Session, &c.Repo, &c.Branch, &c.Cwd,
			&c.Why, &c.Note, &c.Directive, &created, &updated, &expires, &closed,
			&c.CloseCause, &closedNS, &c.Group, &busyNS, &keep)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Claim{}, fmt.Errorf("ledger: %w", err)
	}
	c.Created, c.Updated, c.Expires = fromNS(created), fromNS(updated), fromNS(expires)
	c.Closed, c.ClosedAt = closed == 1, fromNS(closedNS)
	c.LastBusy, c.Keep = fromNS(busyNS), keep == 1
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

// Sweep closes claims that are over: those past their deadline, and those whose card
// has been observed idle for longer than IdleReclaimAfter. It returns what it closed
// and what it left alone.
//
// This is the whole of ftop's automatic cleanup, and its shape is the point. Nothing
// is closed on a deadline or a timer alone: the probe must have ENUMERATED the card and
// found nothing on it. `known == false` is not that observation, neither is a card with
// no processes but memory in use that none accounts for, and an overdue card that a job
// is still using is kept — a job that overran its estimate is still a job, and handing
// its card to the next agent is the one failure the user said must go to them.
//
// The second rule is what makes the fleet usable. An agent that finishes early and
// forgets to release holds a card nobody can use until the deadline it guessed, which
// the ledger's history shows was hours too long; a claim whose card has shown no work
// for IdleReclaimAfter is treated as abandoned and closed with cause "idle". A claim
// the holder marked Keep, or renewed within that window, is never reclaimed: renewing
// is the holder saying the card is still wanted, and it restarts the clock.
func (s *Store) Sweep(observed map[string]Observation) (released []Claim, kept []Claim, err error) {
	now := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id, host, gpu_uuid, expires_ns, last_busy_ns, updated_ns,
		keep FROM claims WHERE closed = 0`)
	if err != nil {
		return nil, nil, fmt.Errorf("ledger: %w", err)
	}
	var open []Claim
	for rows.Next() {
		var c Claim
		var exp, busy, updated int64
		var keep int
		if err := rows.Scan(&c.ID, &c.Host, &c.GPUUUID, &exp, &busy, &updated,
			&keep); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("ledger: %w", err)
		}
		c.Expires, c.LastBusy, c.Updated = fromNS(exp), fromNS(busy), fromNS(updated)
		c.Keep = keep == 1
		open = append(open, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("ledger: %w", err)
	}

	for _, c := range open {
		ob, seen := observed[c.GPUUUID]
		overdue := !c.Expires.IsZero() && now.After(c.Expires)
		// When the evidence was gathered, not when it reached this function: a pass
		// over the fleet waits for its slowest host, and an idle reading taken before
		// the window closed says nothing about the card after it.
		at := ob.At
		if at.IsZero() || at.After(now) {
			at = now
		}
		// Work seen on the card is the only thing that moves last_busy forward, and
		// it is read before the idle guard below, which a busy card fails by
		// definition. A process counts on its own: a sample whose memory query failed
		// but whose process list shows a job is still proof the job was running.
		if seen && ob.Working() {
			if err := touchBusy(tx, c.GPUUUID, at.UnixNano()); err != nil {
				return nil, nil, err
			}
			if overdue {
				kept = append(kept, c)
			}
			continue
		}
		if !seen || !ob.Idle() {
			if overdue {
				kept = append(kept, c)
			}
			continue
		}
		if overdue {
			if err := closeIdle(tx, &c, "expired",
				"deadline passed and the card was observed idle", now); err != nil {
				return nil, nil, err
			}
			released = append(released, c)
			continue
		}
		// Not overdue: only an idle claim older than the reclaim window is abandoned.
		// updated_ns moves whenever the holder renews or changes the claim, so a live
		// holder is never reclaimed; a claim written before last_busy existed (0) is
		// judged from when it last changed, which is the same thing.
		if c.Keep {
			continue
		}
		since := c.LastBusy
		if since.IsZero() || since.Before(c.Updated) {
			since = c.Updated
		}
		// touchBusy coalesces writes within busyResolution, so the last work may have
		// been seen up to that much after the recorded time. The window is measured
		// from the latest it could have been, never shortened by the coalescing.
		if at.Sub(since) < IdleReclaimAfter+busyResolution {
			continue
		}
		if err := closeIdle(tx, &c, "idle", fmt.Sprintf(
			"no work seen on the card for %s; reclaimed for the next job",
			humanShort(at.Sub(since))), now); err != nil {
			return nil, nil, err
		}
		released = append(released, c)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("ledger: %w", err)
	}
	return released, kept, nil
}

// busyResolution is how far last_busy may lag the last observation of work: touchBusy
// does not rewrite a timestamp younger than this. The reclaim adds it to its window,
// so coalescing never makes a card look idle sooner than IdleReclaimAfter.
const busyResolution = time.Minute

// touchBusy records that the card was in use at `at`, inside tx.
//
// It only writes when the recorded time is more than busyResolution older. An
// observation happens on every listing, and rewriting the same timestamp for every busy
// card each time turned a read into a write.
func touchBusy(tx *sql.Tx, uuid string, at int64) error {
	if _, err := tx.Exec(`UPDATE claims SET last_busy_ns = ?
		WHERE gpu_uuid = ? AND closed = 0 AND last_busy_ns < ?`,
		at, uuid, at-int64(busyResolution)); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	return nil
}

// closeIdle closes one claim inside tx because its card was observed idle.
func closeIdle(tx *sql.Tx, c *Claim, cause, detail string, now time.Time) error {
	ns := now.UnixNano()
	if _, err := tx.Exec(`UPDATE claims SET closed = 1, closed_ns = ?, close_cause = ?,
		updated_ns = ? WHERE id = ? AND closed = 0`, ns, cause, ns, c.ID); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	if err := appendEvent(tx, ns, cause, c.ID, c.Host, c.GPUUUID, "", detail); err != nil {
		return err
	}
	c.Closed, c.CloseCause, c.ClosedAt, c.Updated = true, cause, now, now
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

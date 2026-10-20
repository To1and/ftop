package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"ftop/internal/ledger"
)

// claimData is what `ftop claim` returns.
type claimData struct {
	Claim ledger.Claim `json:"claim"`
	Card  ledger.Card  `json:"card"`
	// Env is the environment the agent should export for its own process. It is
	// informational: ftop does not run the job, so nothing enforces it. Session id and
	// card UUID are there so a later `ftop gpu show` can line an observation up with
	// this claim by hand.
	Env map[string]string `json:"env"`
	// Hint is the next step, phrased for an agent that reads one line.
	Hint string `json:"hint"`
}

// runClaim implements `ftop claim`.
func runClaim(io_ IO, args []string) int {
	fs := flag.NewFlagSet("claim", flag.ContinueOnError)
	fs.SetOutput(io_.Err)
	var (
		host    = fs.String("host", "", "host to take a card on")
		uuid    = fs.String("gpu", "", "take this exact card instead of any free one")
		minVRAM = fs.Uint64("min-vram", 0, "minimum card memory in MiB")
		// No default: a claim with a guessed duration is a claim whose deadline
		// nobody chose. The flag is required and checked below.
		forDur    = fs.Duration("for", 0, "how long the claim is meant to last (required)")
		why       = fs.String("why", "", "one line: what this card is for (required)")
		note      = fs.String("note", "", "optional longer note")
		as        = fs.String("as", "", "identity to record (default: detected)")
		direct    = fs.String("directive", "", "the operator instruction behind this claim")
		storeAt   = fs.String("store", "", "ledger path (default: ~/.local/state/ftop/ledger.db)")
		noObserve = fs.Bool("no-ssh", false,
			"never observe the fleet; claim only a card the ledger already knows")
		cfgPath = fs.String("config", "", "config.toml path")
		wait    = fs.Duration("wait", 8*time.Second, "how long to collect an observation")
	)
	if err := parseFlags(fs, args); err != nil {
		return emitErr(io_.Out, "claim", usageErr("%v", err))
	}
	if *host == "" && *uuid == "" {
		return emitErr(io_.Out, "claim", usageErr("one of -host or -gpu is required"))
	}
	if *why == "" {
		return emitErr(io_.Out, "claim", usageErr("-why is required: it is the only record of "+
			"why this card is taken"))
	}
	if *forDur <= 0 {
		return emitErr(io_.Out, "claim", usageErr("-for is required: a claim needs a deadline, "+
			"or nobody will notice it was abandoned"))
	}
	// A person assigns work by the label the board shows ("gpu-4"); the ledger and
	// ssh key machines by alias.
	if alias, rerr := resolveHost(*cfgPath, *host); rerr != nil {
		return emitErr(io_.Out, "claim", rerr)
	} else {
		*host = alias
	}

	id := DetectIdentity()
	owner := id.Name
	if *as != "" {
		owner = *as
		id.Known = true
	}
	// A claim is a record of who holds a card. "unknown" is every unidentified
	// process at once, so a claim written under it would be released by any of them.
	if err := requireIdentity(id, "claiming"); err != nil {
		return emitErr(io_.Out, "claim", err)
	}

	s, err := ledger.Open(storePath(*storeAt))
	if err != nil {
		return emitErr(io_.Out, "claim", classify(err))
	}
	defer s.Close()

	// Observe before choosing, always.
	//
	// The ledger alone cannot answer "which card may I take": a card with a training
	// job nobody claimed looks free, and picking it puts this agent on top of another
	// job. That is exactly what the manual warns about, and it used to be what `claim`
	// did — it only observed when the ledger knew no cards at all. One observation per
	// claim, over the one host involved, is a few seconds.
	var observed map[string]ledger.Observation
	requireIdle := false
	if *host != "" && !*noObserve {
		sn, err := observe(*cfgPath, *host, false, *wait, s)
		if err != nil {
			// Not fatal when the ledger already knows the host: an explicit -gpu with a
			// ledger-recorded card can still be honoured, and refusing would make a
			// transient network failure look like a missing machine.
			if n, cerr := s.CardCount(*host); cerr != nil || n == 0 {
				return emitErr(io_.Out, "claim", &ExitError{Status: ExitUnavail, API: &APIError{
					Code: CodeNoObservation,
					Message: fmt.Sprintf("%s could not be observed and the ledger knows no "+
						"cards there: %v", *host, err),
					Hint: "check that ftop can reach the host, or run `ftop gpu list -host " +
						*host + "` and read unavailable_hosts.",
				}})
			}
		} else {
			if err := s.UpsertCards(sn.Cards, sn.Completed...); err != nil {
				return emitErr(io_.Out, "claim", classify(err))
			}
			observed, requireIdle = sn.Obs, true
		}
	}

	res, err := s.Claim(ledger.ClaimRequest{
		Host: *host, GPUUUID: *uuid, MinVRAM: *minVRAM,
		Owner: owner, Session: id.Session, Repo: id.Repo, Branch: id.Branch, Cwd: id.Cwd,
		Why: *why, Note: *note, For: *forDur, Directive: *direct,
		Observed: observed, RequireIdle: requireIdle,
	})
	if err != nil {
		return emitErr(io_.Out, "claim", classify(err))
	}

	c := res.Claim
	emitOK(io_.Out, "claim", claimData{
		Claim: c,
		Card:  res.Card,
		Env: map[string]string{
			"FTOP_CLAIM":      c.ID,
			"FTOP_GPU_UUID":   c.GPUUUID,
			"FTOP_AGENT":      c.Owner,
			"FTOP_CLAIM_HOST": c.Host,
		},
		Hint: fmt.Sprintf("run your job on %s (%s) on %s, then `ftop release %s`. "+
			"The claim expires %s; extend it with `ftop renew %s -for 4h` if the job runs longer.",
			c.GPUUUID, res.Card.Name, c.Host, c.ID,
			c.Expires.Local().Format("2006-01-02 15:04 MST"), c.ID),
	})
	return ExitOK
}

// releaseData is what `ftop release` returns.
type releaseData struct {
	Claim ledger.Claim `json:"claim"`
}

// runRelease implements `ftop release`.
func runRelease(io_ IO, args []string) int {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	fs.SetOutput(io_.Err)
	var (
		as = fs.String("as", "", "identity to release as (default: detected)")
		// -any-owner is the operator's override and nobody else's.
		//
		// It is not a security control — every agent runs as the same unix user, so
		// nothing here can be enforced — but it must not happen by accident, which is
		// why it is its own flag rather than something -as implies. The event records
		// the identity that ran it, so an override is visible in `ftop history`
		// afterwards. The skill tells agents not to use it; an agent that does is
		// recorded as having done so.
		anyOwner = fs.Bool("any-owner", false,
			"operator: release a claim held by someone else. Requires FTOP_OPERATOR=1; "+
				"recorded in the history as an override")
		storeAt = fs.String("store", "", "ledger path")
	)
	claimID, err := parsePositional(fs, args)
	if err != nil {
		return emitErr(io_.Out, "release", usageErr("%v", err))
	}
	if claimID == "" {
		return emitErr(io_.Out, "release", usageErr("usage: ftop release <claim-id>"))
	}
	id := DetectIdentity()
	owner := id.Name
	if *as != "" {
		owner = *as
		id.Known = true
	}
	override := *anyOwner
	// Releasing is as much a write as claiming: under a shared "unknown" owner any
	// unidentified process could close another's claim.
	if !override {
		if err := requireIdentity(id, "releasing"); err != nil {
			return emitErr(io_.Out, "release", err)
		}
	}
	// -any-owner is the operator's override, and this is the only gate it can have.
	//
	// It cannot be a security control: every agent runs as the same unix user with write
	// access to the ledger, so an agent determined to take a card can edit the database.
	// What it can be is explicit and visible — a claim is only released across owners
	// when the caller says FTOP_OPERATOR=1, which the operator sets in their own shell
	// profile and no agent inherits by accident. The skill tells agents not to use it,
	// and the history records whoever did.
	if override && os.Getenv("FTOP_OPERATOR") != "1" {
		return emitErr(io_.Out, "release", &ExitError{Status: ExitUsage, API: &APIError{
			Code:    CodeUsage,
			Message: "-any-owner is the operator's override and needs FTOP_OPERATOR=1",
			Hint: "an agent must not release another agent's claim. Ask the holder, or ask " +
				"the operator to release it. `ftop gpu show <card>` names the holder.",
		}})
	}
	// The identity that ran the command is what the event credits, override or not.
	actor := owner
	if override {
		owner = ""
	}

	s, err := ledger.Open(storePath(*storeAt))
	if err != nil {
		return emitErr(io_.Out, "release", classify(err))
	}
	defer s.Close()

	cause := "released"
	if override {
		cause = "override"
	}
	// A restart changes this agent's session id but not its kind. Releasing is the
	// action an agent takes on its own work, so it is allowed across a restart of the
	// same kind; taking another KIND of agent's card still is not, and -as names an
	// exact owner when that is what the caller meant.
	//
	// An exact owner match is tried first, because it is the strongest claim an agent can
	// have and it covers -as; only then is the kind-and-place rule used, which widens
	// across a restart.
	var c ledger.Claim
	exact := false
	if !override && *as == "" && id.Known {
		if cur, gerr := s.Get(claimID); gerr == nil {
			exact = cur.Owner == ledger.CanonicalOwner(id.Name)
		}
	}
	switch {
	case !override && *as == "" && id.Known && !exact && id.Session != "" && id.Agent != "":
		c, err = s.ReleaseByAgentKind(claimID, id.Agent, id.Name, id.Repo, id.Branch, id.Cwd)
	default:
		c, err = s.ReleaseAs(claimID, owner, cause, actor)
	}
	if err != nil {
		return emitErr(io_.Out, "release", classify(err))
	}
	emitOK(io_.Out, "release", releaseData{Claim: c})
	return ExitOK
}

// runRenew implements `ftop renew`.
func runRenew(io_ IO, args []string) int {
	fs := flag.NewFlagSet("renew", flag.ContinueOnError)
	fs.SetOutput(io_.Err)
	var (
		forDur  = fs.Duration("for", 0, "new duration from now")
		as      = fs.String("as", "", "identity to renew as (default: detected)")
		storeAt = fs.String("store", "", "ledger path")
	)
	claimID, err := parsePositional(fs, args)
	if err != nil {
		return emitErr(io_.Out, "renew", usageErr("%v", err))
	}
	if claimID == "" {
		return emitErr(io_.Out, "renew", usageErr("usage: ftop renew <claim-id> -for 4h"))
	}
	if *forDur <= 0 {
		return emitErr(io_.Out, "renew", usageErr("-for is required"))
	}
	id := DetectIdentity()
	owner := id.Name
	if *as != "" {
		owner = *as
		id.Known = true
	}
	if err := requireIdentity(id, "renewing"); err != nil {
		return emitErr(io_.Out, "renew", err)
	}

	s, err := ledger.Open(storePath(*storeAt))
	if err != nil {
		return emitErr(io_.Out, "renew", classify(err))
	}
	defer s.Close()

	// Renew is what an agent does about its own work, so it follows the same rule as
	// release: the exact owner, or — after a restart — the same runtime in the same
	// place. A claim `-mine` lists must be one you can act on, renewing included.
	if *as == "" && id.Known {
		if cur, gerr := s.Get(claimID); gerr == nil && mineToRelease(cur, id) {
			owner = cur.Owner
		}
	}

	c, err := s.Renew(claimID, owner, *forDur)
	if err != nil {
		return emitErr(io_.Out, "renew", classify(err))
	}
	emitOK(io_.Out, "renew", releaseData{Claim: c})
	return ExitOK
}

// claimListData is what `ftop claim list` returns.
type claimListData struct {
	Claims []ledger.Claim `json:"claims"`
	Count  int            `json:"count"`
}

// sameAgentKind reports whether a claim's owner is this agent, ignoring the session.
//
// Identity is "agent/1a2b3c4d" — the kind plus a session id that changes whenever
// the agent restarts. Comparing the whole string meant "what do I still hold?" answered
// nothing after a restart, and `release` refused the agent's own cards, so an agent that
// came back to a fleet found its own claims locked against it. The kind is what an agent
// can recognise across restarts; the session is what tells two conversations apart, and
// it is still shown, still recorded, and still what `-as <exact>` filters by.
// mineToRelease is the rule `claim list -mine` and `release` share: a claim this
// identity may close.
//
// The kind survives a restart; the location distinguishes two conversations of one
// runtime running at the same time. An identity with no session to widen the match (an
// agent that declared only FTOP_AGENT) matches its exact recorded name instead.
func mineToRelease(c ledger.Claim, id Identity) bool {
	if !id.Known {
		return false
	}
	// An exact owner match always counts, and it is what an agent that named itself with
	// -as has: its sessions are all the same name, so kind-and-place would be the wrong
	// question. (It could not release under this rule at all, and its claims were
	// invisible to `-mine`.)
	if c.Owner == ledger.CanonicalOwner(id.Name) {
		return true
	}
	if id.Session == "" || !sameAgentKind(c.Owner, id) {
		return false
	}
	return c.Cwd != "" && c.Cwd == id.Cwd && c.Repo == id.Repo && c.Branch == id.Branch
}

func sameAgentKind(owner string, id Identity) bool {
	if id.Agent == "" || id.Agent == "unknown" {
		return false
	}
	kind, _, hasSession := strings.Cut(owner, "/")
	// No session part means the owner was named explicitly ("agent-a") rather than
	// detected, and an explicit name is a different identity, not the same agent kind.
	return hasSession && strings.EqualFold(kind, id.Agent)
}

// runClaimList implements `ftop claim list`.
func runClaimList(io_ IO, args []string) int {
	fs := flag.NewFlagSet("claim list", flag.ContinueOnError)
	fs.SetOutput(io_.Err)
	var (
		storeAt = fs.String("store", "", "ledger path")
		mine    = fs.Bool("mine", false, "only claims made by this agent kind")
		as      = fs.String("as", "", "exact owner to filter by (overrides -mine)")
	)
	if err := parseFlags(fs, args); err != nil {
		return emitErr(io_.Out, "claim list", usageErr("%v", err))
	}
	s, err := ledger.Open(storePath(*storeAt))
	if err != nil {
		return emitErr(io_.Out, "claim list", classify(err))
	}
	defer s.Close()

	views, err := s.List(ledger.ListOptions{Held: true})
	if err != nil {
		return emitErr(io_.Out, "claim list", classify(err))
	}
	out := make([]ledger.Claim, 0, len(views))
	id := DetectIdentity()
	want := ledger.CanonicalOwner(id.Name)
	if *as != "" {
		want = ledger.CanonicalOwner(*as)
	}
	for _, v := range views {
		if v.Claim == nil {
			continue
		}
		// -mine means "claims I could release", so it uses one rule with release, in two
		// steps: an exact owner match (an agent that named itself, with -as or with
		// FTOP_AGENT alone), or the same agent kind AND the same repository, branch and
		// working directory (the same conversation after a restart). Listing by kind
		// alone showed a second conversation's claims as one's own.
		if *mine && *as == "" && !mineToRelease(*v.Claim, id) {
			continue
		}
		if *as != "" && v.Claim.Owner != want {
			continue
		}
		out = append(out, *v.Claim)
	}
	emitOK(io_.Out, "claim list", claimListData{Claims: out, Count: len(out)})
	return ExitOK
}

// writeText is used only by -h and by `ftop help`, never on a JSON path.
func writeText(w io.Writer, s string) { fmt.Fprint(w, s) }

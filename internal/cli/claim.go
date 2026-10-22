package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"ftop/internal/config"
	"ftop/internal/ledger"
)

// claimData is what `ftop claim` returns.
type claimData struct {
	// Group names every card this claim took; `release` and `renew` accept it.
	Group  string         `json:"group"`
	Claims []ledger.Claim `json:"claims"`
	Cards  []ledger.Card  `json:"cards"`
	// Claim and Card are the first of them. Until -n a claim was always one card,
	// and callers written then read these two.
	Claim ledger.Claim `json:"claim"`
	Card  ledger.Card  `json:"card"`
	// Hosts groups the cards by machine, each with the CUDA_VISIBLE_DEVICES value for
	// a job there. One entry unless -spread put the cards on several machines.
	Hosts []claimHost `json:"hosts"`
	// Placement says why these cards: named, or chosen, and against what.
	Placement ledger.Placement `json:"placement"`
	// Requested is set for an elastic claim (-n MIN..MAX): the range and what it got.
	Requested *ledger.Requested `json:"requested,omitempty"`
	// Env is the environment the agent should export for its own process. It is
	// informational: ftop does not run the job, so nothing enforces it. Session id and
	// card UUID are there so a later `ftop gpu show` can line an observation up with
	// this claim by hand.
	Env map[string]string `json:"env"`
	// Hint is the next step, phrased for an agent that reads one line.
	Hint string `json:"hint"`
}

// claimHost is one machine's share of a claim.
type claimHost struct {
	Host               string   `json:"host"`
	GPUUUIDs           []string `json:"gpu_uuids"`
	ClaimIDs           []string `json:"claim_ids"`
	CUDAVisibleDevices string   `json:"cuda_visible_devices"`
}

// countRange is -n's value: a count, or a range such as 4..40.
//
// One flag rather than -min/-max, because "how many cards" is one question and two
// flags make it possible to say contradictory things with it. A range is elastic: at
// least the first number, at most the second, as many as are idle — which is what a
// sweep wants, and what the all-or-none form could not express without the agent
// counting the fleet first and losing the batch to one card taken in between.
type countRange struct {
	lo, hi int
	// bad is the value that could not be read. The flag package cannot report it:
	// a failed Set makes it print its own usage to stderr, and stdout must carry
	// JSON and nothing else, so the value is remembered for runClaim to refuse.
	bad string
}

func (c *countRange) String() string {
	if c == nil || c.hi == 0 {
		return "1"
	}
	if c.lo == c.hi {
		return strconv.Itoa(c.lo)
	}
	return fmt.Sprintf("%d..%d", c.lo, c.hi)
}

func (c *countRange) Set(s string) error {
	lo, hi, ranged := strings.Cut(s, "..")
	parse := func(v string) int {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			c.bad = strings.TrimSpace(v)
			return 0
		}
		return n
	}
	c.lo = parse(lo)
	c.hi = c.lo
	c.bad = ""
	if !ranged {
		return nil
	}
	c.hi = parse(hi)
	return nil
}

// elastic reports whether the request takes as many cards as are idle.
func (c *countRange) elastic() bool { return c.hi != c.lo }

// listFlag is a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	if v = strings.TrimSpace(v); v == "" {
		return fmt.Errorf("an empty value names nothing")
	}
	*l = append(*l, v)
	return nil
}

// runClaim implements `ftop claim`.
func runClaim(io_ IO, args []string) int {
	fs := flag.NewFlagSet("claim", flag.ContinueOnError)
	fs.SetOutput(io_.Err)
	var (
		host    = fs.String("host", "", "take the cards on this host (default: ftop chooses)")
		uuid    = fs.String("gpu", "", "take this exact card")
		count   = &countRange{lo: 1, hi: 1}
		spread  = fs.Bool("spread", false, "the cards may be on different hosts: separate one-card jobs")
		adopt   = fs.Bool("adopt", false, "with -gpu: take the card although processes run on it, because they are yours")
		minVRAM = fs.Uint64("min-vram", 0, "minimum card memory in MiB")
		keep    = fs.Bool("keep", false,
			"keep the card while it is idle: preparing work on the CPU, holding it for a next stage")
		// No default: a claim with a guessed duration is a claim whose deadline
		// nobody chose. The flag is required and checked below.
		forDur    = fs.Duration("for", 0, "how long the claim is meant to last (required)")
		why       = fs.String("why", "", "one line: what this card is for (required)")
		note      = fs.String("note", "", "optional longer note")
		as        = fs.String("as", "", "identity to record (default: detected)")
		direct    = fs.String("directive", "", "the operator instruction behind this claim")
		storeAt   = fs.String("store", "", "ledger path (default: ~/.local/state/ftop/ledger.db)")
		noObserve = fs.Bool("no-ssh", false,
			"do not observe; choose from the ledger alone, without checking that cards are idle")
		cfgPath = fs.String("config", "", "config.toml path")
		wait    = fs.Duration("wait", 8*time.Second, "how long to collect an observation")
		exclude listFlag
	)
	fs.Var(count, "n", "how many cards: N for all-or-none, or MIN..MAX for as many as are idle")
	fs.Var(&exclude, "exclude-host", "never place on this host (repeatable)")
	if err := parseFlags(fs, args); err != nil {
		return emitErr(io_.Out, "claim", usageErr("%v", err))
	}
	switch {
	case count.bad != "":
		return emitErr(io_.Out, "claim", usageErr("-n %q: a card count is a whole "+
			"number, or MIN..MAX", count.bad))
	case count.lo < 1:
		return emitErr(io_.Out, "claim", usageErr("-n must be at least 1"))
	case count.hi < count.lo:
		return emitErr(io_.Out, "claim", usageErr("-n %s: the maximum is below the "+
			"minimum", count))
	case *uuid != "" && count.hi > 1:
		return emitErr(io_.Out, "claim", usageErr("-gpu names one card; for several on one "+
			"machine use -host H -n N"))
	case *spread && (*host != "" || *uuid != ""):
		return emitErr(io_.Out, "claim", usageErr("-spread lets ftop place the cards; it "+
			"cannot be combined with -host or -gpu"))
	case count.elastic() && (*host != "" || *uuid != ""):
		return emitErr(io_.Out, "claim", usageErr("a card range is placed by ftop; it "+
			"cannot be combined with -host or -gpu"))
	case *adopt && *uuid == "":
		return emitErr(io_.Out, "claim", usageErr("-adopt takes one card by name; give it with -gpu"))
	case len(exclude) > 0 && (*host != "" || *uuid != ""):
		return emitErr(io_.Out, "claim", usageErr("-exclude-host is for a placement; the "+
			"host or card is already named"))
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
	for i, h := range exclude {
		alias, rerr := resolveHost(*cfgPath, h)
		if rerr != nil {
			return emitErr(io_.Out, "claim", rerr)
		}
		exclude[i] = alias
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

	// A named card must be on a machine that is still in the fleet. Naming the machine
	// is refused once it leaves ssh_config (resolveHost), and naming one of its cards
	// is the same request by another route; under -no-ssh it used to succeed and hand
	// out a card on a machine the operator had taken away.
	if *uuid != "" {
		if h, ok, err := s.CardHost(*uuid); err != nil {
			return emitErr(io_.Out, "claim", classify(err))
		} else if ok {
			if cfg, cerr := config.Load(expandHome(*cfgPath)); cerr == nil && cfg.Deliberate() {
				inFleet := false
				for _, x := range cfg.Hosts {
					inFleet = inFleet || x.Name == h
				}
				if !inFleet {
					return emitErr(io_.Out, "claim", stateErr(CodeUnknownCard,
						"%s is on %s, which is no longer in the fleet", *uuid, h))
				}
			}
		}
	}

	// With no host and no card named, ftop places the claim, on any host the user has
	// not marked manual and this claim did not exclude.
	var placeOn []string
	fleet := 0
	if *host == "" && *uuid == "" {
		cfg, err := config.Load(expandHome(*cfgPath))
		if err != nil {
			return emitErr(io_.Out, "claim", usageErr("%v", err))
		}
		fleet = len(cfg.Hosts)
		placeOn = []string{}
		for _, h := range cfg.Hosts {
			if !h.Manual && !slices.Contains(exclude, h.Name) {
				placeOn = append(placeOn, h.Name)
			}
		}
	}

	// Observe before choosing, always.
	//
	// The ledger alone cannot answer "which card may I take": a card with a training
	// job nobody claimed looks free, and picking it puts this agent on top of another
	// job. One observation per claim is a few seconds for one host; with no host named
	// it is every host with cards, which is what placement has to compare.
	var observed map[string]ledger.Observation
	var unavailable []string
	requireIdle := false
	if !*noObserve {
		// A named card is observed on the host the ledger last saw it on.
		scope := *host
		if *uuid != "" && scope == "" {
			if h, ok, err := s.CardHost(*uuid); err != nil {
				return emitErr(io_.Out, "claim", classify(err))
			} else if ok {
				scope = h
			}
		}
		// A placement observes only the hosts it may place on. When the fleet has
		// hosts but every one is manual or excluded, that is the caller's decision and
		// there is nothing to look at: the claim answers no_capacity below without
		// contacting anything. A fleet with no hosts at all is a configuration that
		// could not be read, and observing reports it.
		var sn snapshot
		var err error
		switch {
		case scope != "":
			sn, err = observe(*cfgPath, scope, false, *wait, s)
		case len(placeOn) > 0 || fleet == 0:
			sn, err = observeHosts(*cfgPath, "", placeOn, true, *wait, s)
		default:
			sn = snapshot{Obs: map[string]ledger.Observation{}}
		}
		// The hosts this claim depends on that did not answer: the named one, or for a
		// placement every host it may use. Others (a laptop in the same ssh config)
		// are not this claim's business.
		for _, h := range sn.Unavailable {
			if h == scope || (scope == "" && slices.Contains(placeOn, h)) {
				unavailable = append(unavailable, h)
			}
		}
		// ftop hands out only cards it has just seen idle, so a claim it could not
		// look for fails as such. It used to fall back to the ledger when the host or
		// card was already known, which in practice meant a build without a probe
		// handed out cards silently; -no-ssh is the way to say "the ledger is enough".
		switch {
		case err != nil:
		case scope != "" && len(unavailable) > 0:
			err = fmt.Errorf("%s did not answer: %s", scope, sn.Errors[scope])
		case scope == "" && len(placeOn) > 0 && len(unavailable) == len(placeOn):
			err = fmt.Errorf("none of the %d hosts a claim may be placed on answered",
				len(placeOn))
		}
		if err != nil {
			return emitClaim(io_.Out, nil, &ExitError{Status: ExitUnavail, API: &APIError{
				Code: CodeNoObservation,
				Message: fmt.Sprintf("ftop hands out only cards it has just seen idle, and "+
					"could not look: %v", err),
				Hint: "unavailable_hosts names the hosts that did not answer; `ftop gpu list` " +
					"says why. Retry, or claim elsewhere.",
			}}, unavailable)
		}
		if err := s.UpsertCards(sn.Cards, sn.Completed...); err != nil {
			return emitErr(io_.Out, "claim", classify(err))
		}
		// Close what has expired on a card now seen idle before choosing: an
		// abandoned claim used to keep its card from claim until someone happened
		// to run `gpu list`.
		if _, _, err := s.Sweep(sn.Obs); err != nil {
			return emitErr(io_.Out, "claim", classify(err))
		}
		observed, requireIdle = sn.Obs, true
	}

	a, err := s.Allocate(ledger.ClaimRequest{
		Host: *host, GPUUUID: *uuid, Hosts: placeOn, ExcludeHosts: exclude,
		Count: count.lo, Max: count.hi, Spread: *spread,
		Adopt: *adopt, MinVRAM: *minVRAM, Keep: *keep,
		Owner: owner, Session: id.Session, Repo: id.Repo, Branch: id.Branch, Cwd: id.Cwd,
		Why: *why, Note: *note, For: *forDur, Directive: *direct,
		Observed: observed, RequireIdle: requireIdle,
	})
	if err != nil {
		e := classify(err)
		if e.API.Code == CodeNoCapacity && len(unavailable) > 0 {
			e.API.Hint += fmt.Sprintf(" %d host(s) did not answer and were not considered "+
				"(unavailable_hosts).", len(unavailable))
		}
		return emitClaim(io_.Out, nil, e, unavailable)
	}
	return emitClaim(io_.Out, newClaimData(a), nil, unavailable)
}

// emitClaim writes claim's envelope, which also names the hosts the claim needed and
// could not observe: a placement made without them, or a refusal because of them,
// should say so.
func emitClaim(out io.Writer, data any, e *ExitError, unavailable []string) int {
	env := Envelope{APIVersion: APIVersion, OK: e == nil, Command: "claim", At: time.Now().UTC(),
		Unavailable: unavailable}
	if e != nil {
		env.Error = e.API
		write(out, env)
		return e.Status
	}
	env.Data = data
	write(out, env)
	return ExitOK
}

// newClaimData lays an allocation out for the agent: by host, with what to export.
func newClaimData(a ledger.Allocation) claimData {
	d := claimData{Group: a.Group, Placement: a.Placement}
	var all []string
	at := map[string]int{}
	for _, r := range a.Claims {
		d.Claims = append(d.Claims, r.Claim)
		d.Cards = append(d.Cards, r.Card)
		all = append(all, r.Card.GPUUUID)
		i, ok := at[r.Card.Host]
		if !ok {
			i = len(d.Hosts)
			at[r.Card.Host] = i
			d.Hosts = append(d.Hosts, claimHost{Host: r.Card.Host})
		}
		d.Hosts[i].GPUUUIDs = append(d.Hosts[i].GPUUUIDs, r.Card.GPUUUID)
		d.Hosts[i].ClaimIDs = append(d.Hosts[i].ClaimIDs, r.Claim.ID)
	}
	for i := range d.Hosts {
		// CUDA accepts UUIDs here, and they survive a reboot that renumbers the cards.
		d.Hosts[i].CUDAVisibleDevices = strings.Join(d.Hosts[i].GPUUUIDs, ",")
	}
	d.Claim, d.Card = d.Claims[0], d.Cards[0]
	// An elastic claim that got less than it asked for has to say so: the caller's job
	// must be sized for what it received, not for what it requested.
	d.Requested = a.Placement.Range
	short := ""
	if r := a.Placement.Range; r != nil {
		short = fmt.Sprintf(" %d card(s) were idle, so this is %d of the %d..%d asked for; "+
			"size the job for %d.", r.Got, r.Got, r.Min, r.Max, r.Got)
	}
	// FTOP_CLAIM is the group, which release and renew take like a claim id, so a
	// script that releases $FTOP_CLAIM lets go of every card it was given.
	d.Env = map[string]string{
		"FTOP_CLAIM":    a.Group,
		"FTOP_AGENT":    d.Claim.Owner,
		"FTOP_GPU_UUID": strings.Join(all, ","),
	}
	expires := d.Claim.Expires.Local().Format("2006-01-02 15:04 MST")
	reclaim := " If nothing runs on a card for 30 minutes, ftop takes it back, so keep" +
		" this claim by renewing it if you are between jobs: `ftop renew " + a.Group +
		" -for 4h`, or start the job with -keep."
	if d.Claim.Keep {
		reclaim = " The card is kept while idle (-keep), so it is yours until you release" +
			" it or the deadline passes."
	}
	if len(d.Hosts) == 1 {
		h := d.Hosts[0]
		d.Env["FTOP_CLAIM_HOST"] = h.Host
		d.Env["CUDA_VISIBLE_DEVICES"] = h.CUDAVisibleDevices
		d.Hint = fmt.Sprintf("run your job on %s with CUDA_VISIBLE_DEVICES=%s, then "+
			"`ftop release %s`. The claim expires %s; extend it with `ftop renew %s -for 4h` "+
			"if the job runs longer.%s%s", h.Host, h.CUDAVisibleDevices, a.Group, expires,
			a.Group, reclaim, short)
		return d
	}
	d.Hint = fmt.Sprintf("the %d cards are on %d hosts (see hosts): run each job on its host "+
		"with that host's cuda_visible_devices. Release a card by its claim id when its job "+
		"ends, or all of them with `ftop release %s`. The claims expire %s; extend them with "+
		"`ftop renew %s -for 4h`.%s%s", len(d.Cards), len(d.Hosts), a.Group, expires, a.Group,
		reclaim, short)
	return d
}

// releaseData is what `ftop release` and `ftop renew` return.
type releaseData struct {
	// Claim is the first claim acted on; Claims is all of them, one unless the id
	// was a group.
	Claim  ledger.Claim   `json:"claim"`
	Claims []ledger.Claim `json:"claims"`
	Group  string         `json:"group,omitempty"`
}

// runKeep implements `ftop keep`, which turns idle reclaim on or off for a claim or a
// group.
func runKeep(io_ IO, args []string) int {
	fs := flag.NewFlagSet("keep", flag.ContinueOnError)
	fs.SetOutput(io_.Err)
	var (
		off     = fs.Bool("off", false, "turn reclaim back on")
		as      = fs.String("as", "", "identity to act as (default: detected)")
		storeAt = fs.String("store", "", "ledger path")
	)
	claimID, err := parsePositional(fs, args)
	if err != nil {
		return emitErr(io_.Out, "keep", usageErr("%v", err))
	}
	if claimID == "" {
		return emitErr(io_.Out, "keep", usageErr("usage: ftop keep <claim-id|group> [-off]"))
	}
	id := DetectIdentity()
	owner := id.Name
	if *as != "" {
		owner = *as
		id.Known = true
	}
	if err := requireIdentity(id, "changing a claim"); err != nil {
		return emitErr(io_.Out, "keep", err)
	}
	s, err := ledger.Open(storePath(*storeAt))
	if err != nil {
		return emitErr(io_.Out, "keep", classify(err))
	}
	defer s.Close()
	// Same rule as release and renew: the exact owner, or the same runtime in the same
	// place after a restart. Changing a claim is an action on one's own work, and a
	// group follows the rule like a single claim does.
	if *as == "" && id.Known {
		if ledger.IsGroupID(claimID) {
			owner = mineInGroup(s, claimID, id, owner)
		} else if cur, gerr := s.Get(claimID); gerr == nil && mineToRelease(cur, id) {
			owner = cur.Owner
		}
	}
	cs, err := s.SetKeep(claimID, owner, !*off)
	if err != nil {
		return emitErr(io_.Out, "keep", classify(err))
	}
	emitOK(io_.Out, "keep", releaseData{Claim: cs[0], Claims: cs, Group: claimID})
	return ExitOK
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
		return emitErr(io_.Out, "release", usageErr("usage: ftop release <claim-id|group>"))
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
	// A group is one command's cards, so it carries one owner: the rule that lets
	// this caller release one of them lets it release all.
	if ledger.IsGroupID(claimID) {
		if !override && *as == "" && id.Known {
			owner = mineInGroup(s, claimID, id, owner)
		}
		cs, err := s.ReleaseGroup(claimID, owner, cause, actor)
		if err != nil {
			return emitErr(io_.Out, "release", classify(err))
		}
		emitOK(io_.Out, "release", releaseData{Claim: cs[0], Claims: cs, Group: claimID})
		return ExitOK
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
	emitOK(io_.Out, "release", releaseData{Claim: c, Claims: []ledger.Claim{c}})
	return ExitOK
}

// mineInGroup is the owner to act on a group as: the group's own owner when this
// identity may release its claims (see mineToRelease), and fallback otherwise.
func mineInGroup(s *ledger.Store, group string, id Identity, fallback string) string {
	cs, err := s.Group(group)
	if err != nil {
		return fallback
	}
	for _, c := range cs {
		if !c.Closed && mineToRelease(c, id) {
			return c.Owner
		}
	}
	return fallback
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
		return emitErr(io_.Out, "renew", usageErr("usage: ftop renew <claim-id|group> -for 4h"))
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
	if ledger.IsGroupID(claimID) {
		if *as == "" && id.Known {
			owner = mineInGroup(s, claimID, id, owner)
		}
		cs, err := s.RenewGroup(claimID, owner, *forDur)
		if err != nil {
			return emitErr(io_.Out, "renew", classify(err))
		}
		emitOK(io_.Out, "renew", releaseData{Claim: cs[0], Claims: cs, Group: claimID})
		return ExitOK
	}
	if *as == "" && id.Known {
		if cur, gerr := s.Get(claimID); gerr == nil && mineToRelease(cur, id) {
			owner = cur.Owner
		}
	}

	c, err := s.Renew(claimID, owner, *forDur)
	if err != nil {
		return emitErr(io_.Out, "renew", classify(err))
	}
	emitOK(io_.Out, "renew", releaseData{Claim: c, Claims: []ledger.Claim{c}})
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

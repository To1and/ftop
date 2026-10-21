package cli

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"ftop/internal/config"
	"ftop/internal/ledger"
	"ftop/internal/sshx"
	"ftop/internal/wire"
)

// gpuListData is what `ftop gpu list` returns.
type gpuListData struct {
	Hosts []hostView `json:"hosts"`
	// Summary is the one-line answer to "is there anything free".
	Summary summary `json:"summary"`
}

// summary is the one-line answer to "is there anything free".
//
// The counts are NOT a partition, and reading them as one is the mistake this comment
// exists to prevent. `cards` is the total. `free`/`held` split it by the ledger alone.
// `unclaimed` and `unknown` are cuts across both: a card can be free, unclaimed AND
// unknown at once, and `free` includes every unclaimed and every unknown card. The
// number that answers "what may I take right now" is `safe`.
type summary struct {
	Cards   int `json:"cards"`
	Free    int `json:"free"`
	Held    int `json:"held"`
	Overdue int `json:"overdue"`
	// Unclaimed counts free cards something is already running on. It is not an
	// error: it is the fleet's most useful warning, because a free card that is 90%
	// busy will be taken by the next agent unless it reads this first.
	Unclaimed int `json:"unclaimed"`
	// Unknown counts cards whose process list could not be read — including every
	// card on a pass that did not observe at all. A card that is unknown is not
	// free, and the count is what stops "39 free" from being a lie.
	Unknown int `json:"unknown"`
	// Safe counts cards with no claim and nothing running on them: what `-safe`
	// returns, and the only count that answers "can I start now". It is zero under
	// -no-ssh, which is not the same as "nothing is free".
	Safe int `json:"safe"`
}

// hostView is one machine.
type hostView struct {
	Host     string            `json:"host"`
	Label    string            `json:"label,omitempty"`
	Err      string            `json:"error,omitempty"`
	Observed bool              `json:"observed"`
	AgeSec   float64           `json:"age_sec,omitempty"`
	Cards    []ledger.CardView `json:"cards"`
}

// runGPUList implements `ftop gpu list`.
func runGPUList(io_ IO, args []string) int {
	fs := flag.NewFlagSet("gpu list", flag.ContinueOnError)
	fs.SetOutput(io_.Err)
	var (
		hostFilter = fs.String("host", "", "only this host")
		freeOnly   = fs.Bool("free", false, "only cards with no open claim")
		heldOnly   = fs.Bool("held", false, "only cards with an open claim")
		safeOnly   = fs.Bool("safe", false,
			"only cards that are free AND show no processes (what to take)")
		noSSH   = fs.Bool("no-ssh", false, "answer from the ledger only; do not contact hosts")
		wait    = fs.Duration("wait", 8*time.Second, "how long to collect observations")
		cfgPath = fs.String("config", "", "config.toml path")
		storeAt = fs.String("store", "", "ledger path")
	)
	if err := parseFlags(fs, args); err != nil {
		return emitErr(io_.Out, "gpu list", usageErr("%v", err))
	}
	// -free and -held together describe no card at all. Returning an empty list with
	// exit 0 read exactly like "the fleet is empty", which is the opposite of a fleet
	// where every card is claimed.
	if *freeOnly && *heldOnly {
		return emitErr(io_.Out, "gpu list", usageErr("-free and -held select nothing together"))
	}
	// -safe and -held describe no card: safe is a subset of free.
	if *safeOnly && *heldOnly {
		return emitErr(io_.Out, "gpu list", usageErr(
			"-safe and -held select nothing together: a safe card has no holder"))
	}
	if *safeOnly {
		// A safe card is a free one with nothing running on it, so the claim filter
		// comes with it (and saying -free as well is harmless). — and so does an observation. Answering "-safe" from the ledger
		// alone would be a list ftop cannot vouch for, which is the one thing this flag
		// exists to avoid; an empty list would read as "nothing free", which is also
		// wrong. Say which it is.
		if *noSSH {
			return emitErr(io_.Out, "gpu list", usageErr(
				"-safe needs an observation: a card that looks free in the ledger may have "+
					"a job on it. Drop -no-ssh, or read `observed.procs` per card yourself."))
		}
		*freeOnly = true
	}
	// Accept the label the board shows as well as the ssh alias; see resolveHost.
	if alias, err := resolveHost(*cfgPath, *hostFilter); err != nil {
		return emitErr(io_.Out, "gpu list", err)
	} else {
		*hostFilter = alias
	}

	s, err := ledger.Open(storePath(*storeAt))
	if err != nil {
		return emitErr(io_.Out, "gpu list", classify(err))
	}
	defer s.Close()

	var (
		obs         map[string]ledger.Observation
		unavailable []string
		labels      map[string]string
		reasons     map[string]string
		completed   []string
		skipped     []string
	)
	if !*noSSH {
		// -safe, -free and -held list cards and nothing else, so a machine a recent full
		// pass found no cards on cannot change the answer — and the slowest machine in
		// a fleet is often a far-away one with no GPU at all. A plain listing still asks
		// every host: it is what shows CPU-only machines, and what refreshes the cache.
		cardsOnly := *safeOnly || *freeOnly || *heldOnly
		snap, err := observe(*cfgPath, *hostFilter, cardsOnly, *wait, s)
		if err != nil {
			return emitErr(io_.Out, "gpu list", &ExitError{Status: ExitUnavail, API: &APIError{
				Code: CodeNoObservation, Message: err.Error(),
				Hint: "the fleet could not be observed. Re-run with -no-ssh to answer from " +
					"the ledger alone, and treat every card as unknown rather than free.",
			}})
		}
		obs, unavailable, labels = snap.Obs, snap.Unavailable, snap.Labels
		skipped = snap.Skipped
		reasons, completed = snap.Errors, snap.Completed
		// Keep the inventory current: a card first seen now becomes claimable.
		if err := s.UpsertCards(snap.Cards, snap.Completed...); err != nil {
			return emitErr(io_.Out, "gpu list", classify(err))
		}
		// Expiry is evaluated here, on a real observation, because this is the only
		// place that has one. Until it was wired in, `Sweep` existed and was tested but
		// nothing called it: a claim's deadline was a number in a column that never
		// changed anything, and an abandoned claim held its card forever.
		//
		// It cannot hand out a card that is in use: Sweep closes an overdue claim only
		// when the probe enumerated that card and found no processes, and a card it
		// did not enumerate is left alone. That is why it is safe to run on every
		// listing rather than in a scheduler ftop does not have.
		if _, _, err := s.Sweep(obs); err != nil {
			return emitErr(io_.Out, "gpu list", classify(err))
		}
	}

	views, err := s.List(ledger.ListOptions{Host: *hostFilter, Free: *freeOnly, Held: *heldOnly})
	if err != nil {
		return emitErr(io_.Out, "gpu list", classify(err))
	}
	if obs != nil {
		ledger.Annotate(views, obs)
	}
	// -safe answers "what may I take without disturbing anyone", which the ledger alone
	// cannot: a free card with processes on it belongs to a job started outside ftop. It
	// is a filter on the observation, so with -no-ssh every card is unknown and the
	// honest answer is an empty list rather than cards nobody can vouch for.
	if *safeOnly {
		kept := views[:0]
		for _, v := range views {
			if safeToTake(v) {
				kept = append(kept, v)
			}
		}
		views = kept
	}

	// Group by host in the same order the TUI uses, so a human moving between the
	// two does not have to re-find a machine.
	byHost := map[string]*hostView{}
	var order []string
	for _, v := range views {
		h, ok := byHost[v.Host]
		if !ok {
			h = &hostView{Host: v.Host, Label: labels[v.Host]}
			if o, seen := obs[v.GPUUUID]; seen {
				h.Observed = true
				h.AgeSec = roundSec(time.Since(o.At))
			}
			byHost[v.Host] = h
			order = append(order, v.Host)
		}
		h.Cards = append(h.Cards, v)
	}
	// An unobserved host still has to appear: its holdings are real even when it is
	// unreachable, and hiding it would make a card look free. It carries the reason,
	// so an agent can tell "down" from "the probe cannot run here".
	for _, name := range unavailable {
		h, ok := byHost[name]
		if !ok {
			h = &hostView{Host: name, Label: labels[name]}
			byHost[name] = h
			order = append(order, name)
		}
		h.Observed = false
		h.Err = reasons[name]
		if h.Err == "" {
			h.Err = "not observed"
		}
	}
	// A host observed with no cards is a CPU-only machine. It is part of the fleet
	// like every other, so it is listed — with an empty card list — rather than
	// silently missing because the ledger has no card rows for it.
	if !*freeOnly && !*heldOnly {
		for _, name := range completed {
			if *hostFilter != "" && name != *hostFilter {
				continue
			}
			if _, ok := byHost[name]; !ok {
				byHost[name] = &hostView{Host: name, Label: labels[name], Observed: true}
				order = append(order, name)
			}
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	data := gpuListData{Hosts: make([]hostView, 0, len(order))}
	for _, name := range order {
		h := byHost[name]
		if h.Cards == nil {
			h.Cards = []ledger.CardView{}
		}
		data.Summary.Cards += len(h.Cards)
		for _, c := range h.Cards {
			switch {
			case c.Claim != nil:
				data.Summary.Held++
				if c.Claim.Overdue() {
					data.Summary.Overdue++
				}
			default:
				data.Summary.Free++
			}
			// Unknown is derived from the observation, not from the conflict prose.
			// Reading it off the conflict text made a card nobody had observed count
			// as known-idle: `-no-ssh` over an entire ledger returned unknown: 0 and
			// free: 39, which is exactly the claim that cannot be supported. No
			// observation, or an enumeration that failed, is unknown; a card the
			// ledger calls free while something runs is unclaimed, which is a
			// different sentence with a different next step.
			ob := c.Observation
			switch {
			case ob == nil, !ob.ProcsKnown:
				data.Summary.Unknown++
			case c.Claim == nil && ob.Procs > 0:
				data.Summary.Unclaimed++
			case c.Claim == nil && ob.UnattributedMiB > ledger.UnattributedThreshold(ob.MemTotalMiB):
				data.Summary.Unclaimed++
			}
			if safeToTake(c) {
				data.Summary.Safe++
			}
		}
		data.Hosts = append(data.Hosts, *h)
	}

	env := Envelope{
		APIVersion: APIVersion, OK: true, Command: "gpu list", At: time.Now().UTC(),
		Unavailable: unavailable, CPUOnly: skipped, Data: data,
	}
	if obs != nil {
		var newest time.Time
		for _, o := range obs {
			if o.At.After(newest) {
				newest = o.At
			}
		}
		env.AsOf = &newest
	}
	write(io_.Out, env)
	return ExitOK
}

// gpuShowData is what `ftop gpu show` returns: one card, everything known.
type gpuShowData struct {
	Card ledger.CardView `json:"card"`
	// Processes are what the probe sees right now. They are observations, not claims:
	// a process here does not mean the card is properly taken.
	Processes []processView  `json:"processes"`
	History   []ledger.Event `json:"history"`
	// Owner is the open claim. It is serialised as an explicit null when the ledger
	// says free: an absent key and "nobody holds it" are different answers, and an
	// agent that defaults a missing field to "free" would claim a held card.
	Owner *ledger.Claim `json:"owner"`
}

type processView struct {
	PID   int    `json:"pid"`
	Name  string `json:"name"`
	User  string `json:"user"`
	MemMB uint64 `json:"mem_mib"`
}

// runGPUShow implements `ftop gpu show`.
func runGPUShow(io_ IO, args []string) int {
	fs := flag.NewFlagSet("gpu show", flag.ContinueOnError)
	fs.SetOutput(io_.Err)
	var (
		wait    = fs.Duration("wait", 8*time.Second, "how long to collect observations")
		noSSH   = fs.Bool("no-ssh", false, "answer from the ledger only")
		history = fs.Int("history", 10, "how many past events to include")
		cfgPath = fs.String("config", "", "config.toml path")
		storeAt = fs.String("store", "", "ledger path")
	)
	target, err := parsePositional(fs, args)
	if err != nil {
		return emitErr(io_.Out, "gpu show", usageErr("%v", err))
	}
	if target == "" {
		return emitErr(io_.Out, "gpu show", usageErr("usage: ftop gpu show <gpu-uuid|host:index>"))
	}
	// `gpu-4:2` is what a reader copies off the board; the ledger keys cards by the
	// ssh alias. Resolve the host part before looking anything up.
	if i := strings.LastIndexByte(target, ':'); i > 0 {
		if alias, err := resolveHost(*cfgPath, target[:i]); err != nil {
			return emitErr(io_.Out, "gpu show", err)
		} else if alias != target[:i] {
			target = alias + target[i:]
		}
	}

	s, err := ledger.Open(storePath(*storeAt))
	if err != nil {
		return emitErr(io_.Out, "gpu show", classify(err))
	}
	defer s.Close()

	var snap map[string]ledger.Observation
	var procs map[string][]wire.Proc
	if !*noSSH {
		sn, err := observe(*cfgPath, "", false, *wait, s)
		if err != nil {
			return emitErr(io_.Out, "gpu show", &ExitError{Status: ExitUnavail, API: &APIError{
				Code: CodeNoObservation, Message: err.Error()}})
		}
		snap, procs = sn.Obs, sn.Procs
		// Record what was just seen. `gpu show` observes the whole fleet, so it has
		// the inventory in hand; discarding it meant a fresh machine answered
		// `unknown_card` for a card it had just measured, and kept doing so forever,
		// because the only command that writes inventory is `gpu list`.
		if err := s.UpsertCards(sn.Cards, sn.Completed...); err != nil {
			return emitErr(io_.Out, "gpu show", classify(err))
		}
		// Same rule as `gpu list`: a fresh observation is the one moment expiry can
		// be judged, and only an enumerated, empty card is released.
		if _, _, err := s.Sweep(sn.Obs); err != nil {
			return emitErr(io_.Out, "gpu show", classify(err))
		}
	}

	views, err := s.List(ledger.ListOptions{})
	if err != nil {
		return emitErr(io_.Out, "gpu show", classify(err))
	}
	var match *ledger.CardView
	for i := range views {
		if views[i].GPUUUID == target ||
			(fmt.Sprintf("%s:%d", views[i].Host, views[i].Index) == target) {
			match = &views[i]
			break
		}
	}
	if match == nil {
		hint := "run `ftop gpu list` for the cards ftop has seen"
		if *noSSH {
			hint = "run `ftop gpu list` (without -no-ssh) to observe the fleet, or check the name"
		}
		return emitErr(io_.Out, "gpu show", stateErr(CodeUnknownCard,
			"no card %q in the ledger; %s", target, hint))
	}
	ledger.Annotate(views, snap)
	ev, err := s.History(match.GPUUUID, *history)
	if err != nil {
		return emitErr(io_.Out, "gpu show", classify(err))
	}

	// Re-read after Annotate so Conflicts is populated for this card too.
	var card ledger.CardView
	for _, v := range views {
		if v.GPUUUID == match.GPUUUID {
			card = v
			break
		}
	}
	pv := make([]processView, 0)
	for _, p := range procs[match.GPUUUID] {
		pv = append(pv, processView{PID: p.PID, Name: p.Name, User: p.User, MemMB: p.MemMB})
	}
	emitOK(io_.Out, "gpu show", gpuShowData{
		Card: card, Processes: pv, History: ev, Owner: card.Claim,
	})
	return ExitOK
}

// runGPUWho implements `ftop gpu why`: one line, the question the user asked the
// ledger to answer.
func runGPUWhy(io_ IO, args []string) int {
	fs := flag.NewFlagSet("gpu why", flag.ContinueOnError)
	fs.SetOutput(io_.Err)
	var (
		host    = fs.String("host", "", "answer for every card on this host")
		noSSH   = fs.Bool("no-ssh", true, "answer from the ledger only")
		wait    = fs.Duration("wait", 8*time.Second, "how long to collect observations")
		cfgPath = fs.String("config", "", "config.toml path")
		storeAt = fs.String("store", "", "ledger path")
	)
	if err := parseFlags(fs, args); err != nil {
		return emitErr(io_.Out, "gpu why", usageErr("%v", err))
	}
	if alias, err := resolveHost(*cfgPath, *host); err != nil {
		return emitErr(io_.Out, "gpu why", err)
	} else {
		*host = alias
	}
	s, err := ledger.Open(storePath(*storeAt))
	if err != nil {
		return emitErr(io_.Out, "gpu why", classify(err))
	}
	defer s.Close()

	views, err := s.List(ledger.ListOptions{Host: *host})
	if err != nil {
		return emitErr(io_.Out, "gpu why", classify(err))
	}
	// `gpu why` answers from the ledger by default, so -no-ssh is the normal case. It
	// is a real switch rather than decoration: turning it off promises a fresh look,
	// and swallowing a failure would then answer "nobody holds this card" from stale
	// records while the card is 100% busy. Report it like `gpu list` does.
	if !*noSSH {
		sn, err := observe(*cfgPath, *host, true, *wait, s)
		if err != nil {
			return emitErr(io_.Out, "gpu why", &ExitError{Status: ExitUnavail, API: &APIError{
				Code: CodeNoObservation, Message: err.Error(),
				Hint: "the fleet could not be observed; re-run without -no-ssh=false, or with " +
					"-no-ssh to answer from the ledger alone.",
			}})
		}
		ledger.Annotate(views, sn.Obs)
	}
	if len(views) == 0 {
		return emitErr(io_.Out, "gpu why", stateErr(CodeUnknownCard,
			"no cards match; run `ftop gpu list` first"))
	}
	emitOK(io_.Out, "gpu why", map[string]any{"cards": views})
	return ExitOK
}

// snapshot is one observation pass over the fleet.
type snapshot struct {
	// Obs is keyed by card UUID.
	Obs map[string]ledger.Observation
	// Procs is keyed by card UUID: what the probe listed on that card.
	Procs map[string][]wire.Proc
	// Cards is the inventory this pass implies, in host:index order.
	Cards []ledger.Card
	// Completed lists the hosts this pass sampled completely. A card on such a host
	// that did not appear is gone from the fleet; a host that failed is not listed
	// here and its cards are left alone.
	Completed []string
	// Unavailable lists hosts that did not answer.
	Unavailable []string
	// Skipped lists hosts not asked because a recent full pass found no cards there.
	Skipped []string
	// Errors says why each unavailable host was not observed. "Not observed" alone
	// leaves an agent guessing between "retry", "this machine is down" and "the
	// probe cannot run here", which call for different next steps.
	Errors map[string]string
	// Labels maps host name to display label.
	Labels map[string]string
}

// observe runs one snapshot pass over the fleet.
//
// It reuses the same probe and the same connection as the TUI: there is exactly
// one way ftop contacts a machine, so a new command cannot acquire a second one.
//
// cardsOnly skips hosts a recent full pass found no cards on (see readCPUOnly); store
// may be nil, which disables both the skip and the cache.
func observe(cfgPath, hostFilter string, cardsOnly bool, wait time.Duration, store *ledger.Store) (snapshot, error) {
	out := snapshot{
		Obs:    map[string]ledger.Observation{},
		Procs:  map[string][]wire.Proc{},
		Labels: map[string]string{},
		Errors: map[string]string{},
	}

	cfg, err := config.Load(expandHome(cfgPath))
	if err != nil {
		return out, err
	}
	// A question about cards need not wait for a machine that has none. Only hosts a
	// recent full pass reached and found zero cards on are skipped: a host that was
	// down, or one added to ssh_config since, is still asked, so a card cannot go
	// missing from the answer for any reason other than "it was not there minutes ago".
	var skip map[string]bool
	if cardsOnly && hostFilter == "" && store != nil {
		skip = readCPUOnly(store.Path(), time.Now())
	}
	fullPass := hostFilter == "" && skip == nil && store != nil
	var hosts []config.Host
	for _, h := range cfg.Hosts {
		if hostFilter != "" && !strings.EqualFold(h.Name, hostFilter) {
			continue
		}
		out.Labels[h.Name] = h.Label
		if skip[h.Name] {
			out.Skipped = append(out.Skipped, h.Name)
			continue
		}
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 && hostFilter != "" {
		return out, fmt.Errorf("no host named %q (run `ftop -list` to see the fleet)", hostFilter)
	}
	if len(hosts) == 0 && len(out.Skipped) > 0 {
		return out, nil
	}
	if len(hosts) == 0 {
		return out, fmt.Errorf("no hosts matched")
	}
	// Checked before any connection attempt, so a build without a probe says that
	// instead of reporting every host as unreachable.
	if len(probeBinary()) == 0 {
		return out, fmt.Errorf("this build has no probe binary; run `make`")
	}

	// The sample itself gets `wait`; the probe upload a new build needs on first
	// contact gets its own allowance on top. Counting the upload against `wait` made
	// the first listing after every upgrade report healthy hosts as unavailable.
	ctx, cancel := context.WithTimeout(context.Background(), wait+uploadBudget)
	defer cancel()

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, h := range hosts {
		wg.Add(1)
		go func(h config.Host) {
			defer wg.Done()
			c := &sshx.Client{Host: h.Name, Probe: probeBinary(),
				SSHConfig: cfg.SSHConfig, Options: h.SSHOptions()}
			sample, err := c.Snapshot(ctx, wait)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.Unavailable = append(out.Unavailable, h.Name)
				out.Errors[h.Name] = err.Error()
				return
			}
			out.Completed = append(out.Completed, h.Name)
			for _, g := range sample.Gpus {
				if g.UUID == "" {
					continue
				}
				o := ledger.Observation{
					At: sample.At, Procs: len(g.Procs), ProcsKnown: g.ProcsKnown,
				}
				if u := g.UtilPct(); u >= 0 {
					o.UtilPct = u
				}
				var total uint64
				if g.MemTotal != nil {
					total = *g.MemTotal
					o.MemTotalMiB = total
				}
				if g.MemUsed != nil {
					o.MemUsedMiB = *g.MemUsed
				}
				o.MemReservedMiB = g.MemReserved
				// Attributed memory is what this host's own process list says. The
				// difference is the card being used by something the host cannot see,
				// which is the signature of the ledger missing a job. Used is the
				// driver's own figure, so the reservation is not part of it.
				var attributed uint64
				for _, p := range g.Procs {
					attributed += p.MemMB
				}
				if g.MemUsed != nil && g.ProcsKnown && *g.MemUsed > attributed {
					o.UnattributedMiB = int64(*g.MemUsed - attributed)
				}
				out.Obs[g.UUID] = o
				out.Procs[g.UUID] = g.Procs
				out.Cards = append(out.Cards, ledger.Card{
					Host: h.Name, GPUUUID: g.UUID, Index: g.Index,
					Name: g.Name, MemTotal: total,
				})
			}
		}(h)
	}
	wg.Wait()
	sort.Strings(out.Unavailable)
	// Stable inventory order so a listing does not shuffle between runs.
	sort.Slice(out.Cards, func(i, j int) bool {
		if out.Cards[i].Host != out.Cards[j].Host {
			return out.Cards[i].Host < out.Cards[j].Host
		}
		return out.Cards[i].Index < out.Cards[j].Index
	})
	if fullPass {
		withCards := map[string]bool{}
		for _, c := range out.Cards {
			withCards[c.Host] = true
		}
		var none []string
		for _, h := range out.Completed {
			if !withCards[h] {
				none = append(none, h)
			}
		}
		writeCPUOnly(store.Path(), none, time.Now())
	}
	return out, nil
}

func roundSec(d time.Duration) float64 {
	return float64(d.Milliseconds()) / 1000
}

// safeToTake reports whether a card may be taken without disturbing anyone: no claim,
// and an observation that enumerated the card and found nothing on it.
//
// Both halves are required. Without the observation a card can be free in the ledger
// while a training job is using all of it — which is the collision this filter exists
// to prevent — and an enumeration that failed (ProcsKnown false) means ftop does not
// know, so it is not safe either.
func safeToTake(v ledger.CardView) bool {
	if v.Claim != nil || !v.Present {
		return false
	}
	ob := v.Observation
	if ob == nil || !ob.ProcsKnown {
		return false
	}
	return ob.Procs == 0 &&
		ob.UnattributedMiB <= ledger.UnattributedThreshold(ob.MemTotalMiB)
}

// uploadBudget is the time a first contact may spend uploading the probe, on top
// of the observation wait.
const uploadBudget = 30 * time.Second

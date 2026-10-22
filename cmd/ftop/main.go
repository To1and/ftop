// Command ftop is a live fleet-wide GPU monitor.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"ftop/internal/cli"
	"ftop/internal/config"
	"ftop/internal/demo"
	"ftop/internal/ledger"
	"ftop/internal/sshx"
	"ftop/internal/ui"
)

// version is stamped at link time (`make` passes -X main.version).
var version = "dev"

func main() {
	// Agent-facing subcommands are a separate surface from the TUI: they print JSON
	// and never open a terminal. Dispatching before flag.Parse keeps the TUI flags
	// from having to tolerate `ftop claim -host …`.
	if len(os.Args) > 1 && isSubcommand(os.Args[1]) {
		cli.ProbeBin = probeBin
		cli.BuildVersion = version
		os.Exit(cli.Main(os.Args[1:]))
	}
	// A bare word that is not a flag and not a subcommand is a misspelling, not a
	// request for the monitor. Falling through to the TUI meant `ftop gpuu` opened a
	// full-screen fleet monitor (and, piped, failed with a TTY error and no JSON) when
	// the caller wanted a JSON answer. `ftop` alone still opens the TUI: the argument
	// list is what distinguishes them.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		cli.ProbeBin = probeBin
		cli.BuildVersion = version
		// The CLI answers this itself: one JSON object on stdout, exit 2, and a
		// message naming the word it did not understand.
		os.Exit(cli.Main(os.Args[1:]))
	}

	var (
		hostFilter = flag.String("host", "", "only hosts whose name contains this")
		interval   = flag.Duration("interval", 0, "override the sampling interval")
		graphRows  = flag.Int("graph", 1, "inline per-GPU history graph: 1 on, 0 off")
		cfgPath    = flag.String("config", "", "config.toml path")
		list       = flag.Bool("list", false, "list discovered hosts and exit")
		once       = flag.Bool("once", false, "render one frame to stdout and exit")
		jsonOut    = flag.String("json", "", "with -once, write a JSON snapshot to this path (- for stdout)")
		wait       = flag.Duration("wait", 12*time.Second, "with -once, how long to collect data")
		showVer    = flag.Bool("version", false, "print version and exit")
		demoMode   = flag.Bool("demo", false, "run on a synthetic fleet instead of ssh (no host is contacted)")
		mouse      = flag.Bool("mouse", true, "click and scroll with the mouse (-mouse=false keeps plain text selection)")
	)
	flag.Parse()

	// Validate rather than silently substituting. Values up to 6 were row counts
	// when each card had a multi-row graph; they are still accepted and mean "on".
	if *graphRows < 0 || *graphRows > 6 {
		fmt.Fprintf(os.Stderr, "ftop: -graph must be 0 (off) or 1 (on), got %d\n", *graphRows)
		os.Exit(2)
	}

	if *showVer {
		fmt.Println("ftop " + version)
		return
	}

	// Demo mode does not read ssh_config at all: a synthetic fleet must not depend
	// on, or show anything of, the real one. Only the presentation settings are read.
	var cfg *config.Config
	if *demoMode {
		cfg = config.LoadSettings(*cfgPath)
	} else {
		var err error
		if cfg, err = config.Load(*cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, "ftop:", err)
			os.Exit(1)
		}
	}
	if *interval > 0 {
		cfg.Interval = interval.Seconds()
	}

	var hosts []string
	if *demoMode {
		cfg.Hosts = nil
		for _, h := range demo.Hosts() {
			cfg.Hosts = append(cfg.Hosts, config.Host{Name: h, Label: h})
		}
	}
	for _, h := range cfg.Hosts {
		if *hostFilter != "" && !strings.Contains(strings.ToLower(h.Name), strings.ToLower(*hostFilter)) {
			continue
		}
		hosts = append(hosts, h.Name)
	}

	if *list {
		for _, h := range cfg.Hosts {
			fmt.Printf("%-30s %-24s %s\n", h.Name, h.Label, h.Hostname)
		}
		return
	}
	if len(hosts) == 0 {
		fmt.Fprintln(os.Stderr, "ftop: no hosts discovered (check ~/.ssh/config)")
		os.Exit(1)
	}
	if len(probeBin) == 0 && !*demoMode {
		fmt.Fprintln(os.Stderr, "ftop: probe binary not embedded; run `make`")
		os.Exit(1)
	}

	labels := make(map[string]string, len(cfg.Hosts))
	sshOpts := make(map[string][]string, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		labels[h.Name] = h.Label
		sshOpts[h.Name] = h.SSHOptions()
	}
	app := ui.New(ui.Options{
		Labels:    labels,
		Hosts:     hosts,
		GraphRows: *graphRows,
		MaxCols:   cfg.Columns,
		Interval:  time.Duration(cfg.Interval * float64(time.Second)),
		NoMouse:   !*mouse,

		ShowCPUOnly:     cfg.ShowCPUOnly,
		ShowUnsupported: cfg.ShowUnsupported,
		SkillInstall: func() string {
			cli.ProbeBin = probeBin
			cli.BuildVersion = version
			var buf strings.Builder
			if code := cli.MainInto(&buf, []string{"skill", "install"}); code != 0 {
				return "skill install failed — run `ftop skill install`"
			}
			return "skill installed"
		},
		SkillStatus: func() string {
			cli.BuildVersion = version
			return cli.SkillSummary()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer sshx.Cleanup()

	// msgq decouples the reader goroutines from the UI. Reads happen before the
	// program exists, so sends must not depend on it: a buffered channel means a
	// host that answered during startup is not dropped.
	msgq := make(chan tea.Msg, 256)
	var readers sync.WaitGroup
	probeInterval := time.Duration(cfg.Interval * float64(time.Second))

	if *demoMode {
		// History is replayed straight into the app before the program exists, so
		// the first frame already has full graphs; only the live stream goes
		// through the queue.
		src := demo.New()
		src.Warm(90, func(m sshx.SampleMsg) { app.Deliver(m) }, func(m sshx.ErrMsg) { app.Deliver(m) })
		// The ledger reader below is skipped in demo mode, so the demo's claims are
		// delivered here, once: they change only when an agent claims or releases.
		app.Deliver(ui.DemoLedger(time.Now()))
		readers.Add(1)
		go func() {
			defer readers.Done()
			src.Run(ctx, probeInterval, func(m sshx.SampleMsg) { enqueue(msgq, m) })
		}()
		// No ssh readers: the demo is the only source.
		hosts = nil
	}
	for _, h := range hosts {
		readers.Add(1)
		go func(host string) {
			defer readers.Done()
			client := &sshx.Client{Host: host, Probe: probeBin, Interval: probeInterval,
				SSHConfig: cfg.SSHConfig, Options: sshOpts[host]}
			backoff := time.Second
			for ctx.Err() == nil {
				// A stream that produced at least one sample proves the host is up,
				// so the next failure starts its backoff from scratch. Without this
				// a host that flapped once keeps paying an escalated delay forever.
				healthy := false
				client.Run(ctx,
					func(m sshx.SampleMsg) { healthy = true; enqueue(msgq, m) },
					func(m sshx.ErrMsg) { enqueue(msgq, m) },
				)
				if ctx.Err() != nil {
					return
				}
				if healthy {
					backoff = time.Second
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				// Exponential with a hard ceiling: a host down for maintenance should
				// settle at one attempt every 30 s, not hammer the network. The clamp
				// has to be on the RESULT — multiplying a value below the ceiling by
				// 1.7 overshoots it (22 s -> 37 s).
				backoff = time.Duration(float64(backoff) * 1.7)
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
		}(h)
	}

	// The ledger is read on its own cadence: holdings change when an agent claims or
	// releases, not when a card ticks, and a ledger that cannot be opened must not
	// stop the monitor from showing readings.
	if !*demoMode {
		ledgerAt := cli.StorePath("")
		go func() {
			tick := time.NewTicker(5 * time.Second)
			defer tick.Stop()
			for {
				if msg := readLedger(ledgerAt); msg != nil {
					enqueue(msgq, *msg)
				}
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
			}
		}()
	}

	if *once {
		// Drain until every host has either a sample or a failure, then let the
		// stream settle briefly so the frame is not half-filled, and print once.
		//
		// "Quiet for 1.5 s" alone is wrong in both directions: a fleet still
		// uploading the probe is silent, which looks identical to a finished one,
		// and a healthy 1 Hz stream is never quiet, so it always burned the whole
		// budget. Readiness is the condition; silence is only the tie-breaker.
		deadline := time.After(*wait)
		settle := time.NewTimer(*wait)
		defer settle.Stop()
		ready := false
	loop:
		for {
			select {
			case m := <-msgq:
				app.Deliver(m)
				if !ready && app.Ready(hosts) {
					ready = true
					resetTimer(settle, 1200*time.Millisecond)
				} else if ready {
					resetTimer(settle, 400*time.Millisecond)
				}
			case <-settle.C:
				// Only a ready fleet may end early. Otherwise this is the deadline
				// timer expiring and we print whatever arrived.
				break loop
			case <-deadline:
				break loop
			}
		}
		cancel()
		readers.Wait()
		if *jsonOut != "" {
			body, err := app.ExportJSON()
			if err != nil {
				fmt.Fprintln(os.Stderr, "ftop:", err)
				sshx.Cleanup()
				os.Exit(1)
			}
			if *jsonOut == "-" {
				os.Stdout.Write(body)
				fmt.Println()
				return
			}
			if err := os.WriteFile(*jsonOut, body, 0o644); err != nil {
				fmt.Fprintln(os.Stderr, "ftop:", err)
				sshx.Cleanup()
				os.Exit(1)
			}
			return
		}
		fmt.Println(app.PlainFrame())
		return
	}

	// Signals must restore the terminal, not leave it in the alternate screen.
	sig := make(chan os.Signal, 1)
	// SIGHUP too: closing the terminal (or killing its tmux session) sends it,
	// and the run's control-socket directory must still be removed.
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Forward queued messages into the running program. This goroutine owns the
	// handoff, so the readers never touch tea.Program directly.
	//
	// It is started only AFTER prog exists. Starting it first meant it could
	// dequeue a message while the pointer was still nil and drop it — a host that
	// answered during startup silently lost its first sample. Program.Send blocks
	// until Run drains, so nothing is lost in the window before Run either.
	prog := tea.NewProgram(app, tea.WithContext(ctx))

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-msgq:
				prog.Send(m)
			}
		}
	}()
	go func() {
		<-sig
		cancel()
		prog.Quit()
	}()

	// WithContext ties the program's lifetime to ctx: when a signal cancels it, the
	// program restores the terminal itself instead of leaving the alt screen set.
	_, runErr := prog.Run()
	cancel()
	readers.Wait()
	sshx.Cleanup()
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "ftop:", runErr)
		os.Exit(1) // sshx.Cleanup already ran above
	}
}

// readLedger reads the current holders for the board.
//
// A missing ledger is not an error: on a machine that has never claimed anything there
// is no database yet, and the board should show dashes rather than a warning. A ledger
// that exists but cannot be read is different — that one is reported, because every
// card looks free without it and "free" is the wrong answer to give silently.
func readLedger(path string) *ui.LedgerMsg {
	if _, err := os.Stat(path); err != nil {
		return &ui.LedgerMsg{At: time.Now(), Holders: map[string]ui.Holder{}}
	}
	st, err := ledger.Open(path)
	if err != nil {
		return &ui.LedgerMsg{At: time.Now(), Err: err.Error()}
	}
	defer st.Close()
	views, err := st.List(ledger.ListOptions{Held: true})
	if err != nil {
		return &ui.LedgerMsg{At: time.Now(), Err: err.Error()}
	}
	holders := make(map[string]ui.Holder, len(views))
	for _, v := range views {
		if v.Claim == nil {
			continue
		}
		holders[v.GPUUUID] = ui.Holder{
			Owner:   v.Claim.Owner,
			Why:     v.Claim.Why,
			Since:   v.Claim.Created,
			Overdue: v.Claim.Overdue(),
		}
	}
	return &ui.LedgerMsg{At: time.Now(), Holders: holders}
}

// isSubcommand reports whether the first argument selects the agent-facing CLI
// rather than the TUI.
//
// The list is explicit, not "any bare word": a future TUI flag must never be
// mistaken for a subcommand, and a typo like `ftop gpus` has to fall through to the
// flag parser's error rather than silently becoming one.
func isSubcommand(s string) bool {
	switch s {
	case "gpu", "claim", "release", "renew", "keep", "history", "host", "whoami", "skill",
		"help", "version":
		return true
	}
	return false
}

// resetTimer re-arms t without the stop/drain dance every call site would otherwise
// have to repeat.
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// enqueue never blocks: a full queue drops the message rather than stalling a
// reader. Losing one sample of a 1 Hz stream is invisible; blocking the reader is
// how a slow UI turns into a fleet-wide backlog.
//
// Errors are the exception. A dropped sample costs one second of one card; a
// dropped error leaves a dead host looking merely quiet, and the queue is full
// precisely when a fleet is in trouble. Errors get a short bounded wait instead.
func enqueue(q chan<- tea.Msg, m tea.Msg) {
	if _, isErr := m.(sshx.ErrMsg); !isErr {
		select {
		case q <- m:
		default:
		}
		return
	}
	select {
	case q <- m:
	case <-time.After(2 * time.Second):
	}
}

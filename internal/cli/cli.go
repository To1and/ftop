// Package cli is ftop's agent-facing command surface.
//
// The TUI is for a person watching a wall of cards; this is for an agent that runs
// one command and reads the answer. Those are different shapes, so they are separate
// front ends over the same observation and ledger code rather than one command that
// switches personality on a flag.
//
// Three rules hold throughout:
//
//   - stdout carries JSON and nothing else. Diagnostics go to stderr, so a caller can
//     pipe stdout into a parser without stripping banners.
//   - every failure has a stable code and a coarse exit status. An agent branches on
//     those; it must never have to match on prose.
//   - free text the agent wrote (Why, Note) is echoed as data, never interpreted.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ftop/internal/config"
	"ftop/internal/ledger"
)

// APIVersion is the envelope's shape. Bumped when a field changes meaning, not when
// one is added: readers must tolerate unknown fields, and agents written against v1
// keep working.
const APIVersion = 1

// Exit codes. Coarse on purpose: an agent needs to tell "retry", "give up", "ask the
// operator" and "fix your command" apart, and nothing finer survives contact with
// real use.
const (
	ExitOK      = 0
	ExitUsage   = 2 // the command itself is wrong
	ExitHeld    = 3 // the cards are not there to take: held, busy, or too few; read conflicts
	ExitState   = 4 // the ledger says no: unknown card, wrong owner, already closed
	ExitUnavail = 5 // the fleet could not be observed or the ledger could not be opened
)

// ErrorCode is the machine-readable half of a failure.
type ErrorCode string

const (
	CodeUsage         ErrorCode = "usage"
	CodeHeld          ErrorCode = "held"
	CodeUnknownCard   ErrorCode = "unknown_card"
	CodeNotOwner      ErrorCode = "not_owner"
	CodeAlreadyClosed ErrorCode = "already_closed"
	CodeNotFound      ErrorCode = "lease_not_found"
	CodeSchemaTooNew  ErrorCode = "schema_too_new"
	CodeNoObservation ErrorCode = "no_observation"
	CodeNoCapacity    ErrorCode = "no_capacity"
	CodeNotIdle       ErrorCode = "not_idle"
	CodeInternal      ErrorCode = "internal"
)

// Envelope wraps every response. A fixed outer shape means an agent can read the
// status before it knows which subcommand it ran, and a future version can add data
// without breaking that.
type Envelope struct {
	APIVersion int        `json:"api_version"`
	OK         bool       `json:"ok"`
	Command    string     `json:"command"`
	At         time.Time  `json:"at"`
	AsOf       *time.Time `json:"observation_as_of,omitempty"`
	// Unavailable lists hosts whose observation is stale or missing this time. Absent
	// data is never rendered as an idle fleet.
	Unavailable []string `json:"unavailable_hosts,omitempty"`
	// CPUOnly lists hosts not asked this time because a full pass minutes ago found no
	// cards on them. They are not unavailable; they had nothing a card query wants.
	CPUOnly []string  `json:"cpu_only_hosts,omitempty"`
	Data    any       `json:"data,omitempty"`
	Error   *APIError `json:"error,omitempty"`
}

// APIError is the failure half of the envelope.
type APIError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	// Hint is one sentence an agent can act on. It never contains the answer to
	// "should I force this": ftop has no force.
	Hint string `json:"hint,omitempty"`
	// Conflicts carries the holders, when the failure was a claim conflict.
	Conflicts any `json:"conflicts,omitempty"`
}

// IO is the CLI's terminal. Tests replace it; nothing else does.
type IO struct {
	Out io.Writer
	Err io.Writer
}

// ExitError carries the code and status a failure maps to.
type ExitError struct {
	Status int
	API    *APIError
}

func (e *ExitError) Error() string { return string(e.API.Code) + ": " + e.API.Message }

func usageErr(format string, a ...any) *ExitError {
	return &ExitError{Status: ExitUsage, API: &APIError{
		Code: CodeUsage, Message: fmt.Sprintf(format, a...),
	}}
}

func stateErr(code ErrorCode, format string, a ...any) *ExitError {
	return &ExitError{Status: ExitState, API: &APIError{
		Code: code, Message: fmt.Sprintf(format, a...),
	}}
}

// classify turns a ledger error into an exit status and a code.
func classify(err error) *ExitError {
	var he *ledger.HeldError
	if errors.As(err, &he) {
		return &ExitError{Status: ExitHeld, API: &APIError{
			Code:    CodeHeld,
			Message: fmt.Sprintf("%s is held by %s: %s", he.GPUUUID, he.Owner, he.Why),
			Hint: "wait for the holder to release, claim a different card, or ask the operator " +
				"to decide. ftop will not evict a holder.",
			Conflicts: he,
		}}
	}
	var nf *ledger.NoFreeCardError
	if errors.As(err, &nf) {
		return &ExitError{Status: ExitHeld, API: &APIError{
			Code:    CodeHeld,
			Message: nf.Error(),
			Hint: "every card on this host is held; read `holders` for who and why, then " +
				"wait, pick another host, or ask the operator.",
			Conflicts: nf,
		}}
	}
	var ie *ledger.InsufficientError
	if errors.As(err, &ie) {
		hint := "nothing was claimed. Ask for fewer cards, wait for some to be released, or " +
			"ask the operator."
		switch {
		case ie.Host != "":
			hint = fmt.Sprintf("nothing was claimed. Ask for at most %d here, or leave out -host "+
				"and ftop will find a machine with enough.", ie.Idle)
		case !ie.Spread && ie.Idle >= ie.Want:
			hint = fmt.Sprintf("nothing was claimed. %d cards are idle, but no one machine has %d. "+
				"If the job runs as separate one-card processes, add -spread; otherwise ask for "+
				"at most %d, or wait.", ie.Idle, ie.Want, ie.MostOnOneHost)
		}
		return &ExitError{Status: ExitHeld, API: &APIError{
			Code: CodeNoCapacity, Message: ie.Error(), Hint: hint, Conflicts: ie,
		}}
	}
	var ne *ledger.NotIdleError
	if errors.As(err, &ne) {
		hint := "something nobody claimed is using that card; `ftop gpu show " + ne.GPUUUID +
			"` lists it. Take another card or ask the operator. If those processes are " +
			"yours, claim the card with -adopt."
		if !ne.Observed || !ne.ProcsKnown {
			hint = "ftop could not see what runs on that card. Retry, or take a card " +
				"`ftop gpu list -safe` shows."
		}
		return &ExitError{Status: ExitHeld, API: &APIError{
			Code: CodeNotIdle, Message: ne.Error(), Hint: hint, Conflicts: ne,
		}}
	}
	switch {
	case errors.Is(err, ledger.ErrSchemaTooNew):
		return &ExitError{Status: ExitUnavail, API: &APIError{
			Code: CodeSchemaTooNew, Message: err.Error(),
			Hint: "upgrade ftop; this ledger was written by a newer build.",
		}}
	case errors.Is(err, ledger.ErrNoSuchCard):
		return &ExitError{Status: ExitState, API: &APIError{Code: CodeUnknownCard, Message: err.Error()}}
	case errors.Is(err, ledger.ErrNotOwner):
		return &ExitError{Status: ExitState, API: &APIError{
			Code: CodeNotOwner, Message: err.Error(),
			Hint: "only the holder may release or renew a claim. To take a card from another " +
				"agent, ask the operator.",
		}}
	case errors.Is(err, ledger.ErrAlreadyClosed):
		return &ExitError{Status: ExitState, API: &APIError{
			Code: CodeAlreadyClosed, Message: err.Error()}}
	case errors.Is(err, ledger.ErrNotFound):
		return &ExitError{Status: ExitState, API: &APIError{
			Code: CodeNotFound, Message: err.Error()}}
	case errors.Is(err, ledger.ErrInvalid):
		return &ExitError{Status: ExitUsage, API: &APIError{Code: CodeUsage, Message: err.Error()}}
	}
	return &ExitError{Status: ExitUnavail, API: &APIError{Code: CodeInternal, Message: err.Error()}}
}

// write emits the envelope. JSON only: a caller piping stdout must never have to
// strip a banner.
func write(w io.Writer, env Envelope) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(env)
}

// emitOK writes a success envelope.
func emitOK(out io.Writer, cmd string, data any) {
	write(out, Envelope{APIVersion: APIVersion, OK: true, Command: cmd, At: time.Now().UTC(), Data: data})
}

// emitErr writes a failure envelope and returns its exit status.
func emitErr(out io.Writer, cmd string, e *ExitError) int {
	write(out, Envelope{
		APIVersion: APIVersion, OK: false, Command: cmd,
		At: time.Now().UTC(), Error: e.API,
	})
	return e.Status
}

// newFlagSet is the shared flag setup: errors go to stderr, and parsing a bad flag is
// a usage failure, never a silent zero value.
func newFlagSet(name string, errw io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errw)
	return fs
}

// parseFlags parses flags for a command that takes no positional argument, and
// refuses anything left over.
//
// Go's flag package stops at the first non-flag token and reports success, so
// `ftop claim … -as bob` — with a stray word before `-as` — used to claim under the
// detected identity and say nothing. A command that takes no arguments must not
// silently discard the ones it was given.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q; this command takes only flags", fs.Arg(0))
	}
	return nil
}

// parsePositional parses flags that may appear before or after one positional
// argument.
//
// Go's flag package stops at the first non-flag token, so `ftop release ID -as x`
// silently ignores -as and releases as the detected identity instead — the flag would
// be read as part of a stray argument. Reordering once and re-parsing is the smallest
// fix that keeps the familiar `command ID -flag value` order working.
func parsePositional(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() == 1 {
		return fs.Arg(0), nil
	}
	if fs.NArg() > 1 {
		rest := fs.Args()
		if !strings.HasPrefix(rest[0], "-") {
			reordered := append(append([]string{}, rest[1:]...), rest[0])
			if err := fs.Parse(reordered); err != nil {
				return "", err
			}
			if fs.NArg() == 1 {
				return fs.Arg(0), nil
			}
		}
		return "", fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return "", nil
}

// ProbeBin is the remote probe binary, supplied by the main package because the
// embed lives there (`-tags probe`). A CLI built without it cannot observe hosts,
// and says so rather than answering from a stale ledger as if it were live.
var ProbeBin []byte

func probeBinary() []byte { return ProbeBin }
func storePath(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if p := os.Getenv("FTOP_LEDGER"); p != "" {
		return p
	}
	return ledger.DefaultPath()
}

// StorePath is storePath for callers outside the package (the TUI).
func StorePath(flagVal string) string { return storePath(flagVal) }

// resolveHost turns whatever the caller typed into the ssh alias the ledger uses.
//
// A person reads the board and sees "gpu-4"; the ledger and ssh use
// "lab-gpu-4". Accepting only the alias made `-host gpu-4` filter to nothing
// and still exit 0, which reads exactly like "that machine has no cards". Errors are
// returned as ExitErrors so every command reports the same way.
//
// A name the fleet does not contain is an error, not a filter that matches nothing.
// The two were indistinguishable from the answer — `gpu list -host typo` printed an
// empty board and exited 0, while `claim -host typo` failed — and an empty board is
// the one answer that cannot be told apart from "every card is held".
func resolveHost(cfgPath, name string) (string, *ExitError) {
	if name == "" {
		return "", nil
	}
	alias, found, ambiguous := config.ResolveHost(expandHome(cfgPath), name)
	if ambiguous {
		return "", &ExitError{Status: ExitUsage, API: &APIError{
			Code:    CodeUsage,
			Message: "more than one host is labelled " + name,
			Hint:    "use the ssh alias; `ftop -list` shows both.",
		}}
	}
	if !found {
		// The fleet could not be read at all is a different failure; then the name is
		// passed through and the observation reports the real problem.
		if cfg, err := config.Load(expandHome(cfgPath)); err == nil && len(cfg.Hosts) > 0 {
			return "", &ExitError{Status: ExitUsage, API: &APIError{
				Code:    CodeUsage,
				Message: "no host named " + name,
				Hint: "`ftop -list` prints every machine in the fleet, with its alias " +
					"and the label the board shows.",
			}}
		}
		return name, nil
	}
	return alias, nil
}

// expandHome resolves a leading ~/ for the --config flag.
func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

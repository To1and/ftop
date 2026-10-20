package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// Identity is who is asking, assembled from what the process tree actually exposes.
//
// It is a label, not a credential. Every agent reaches the fleet from the same unix
// account, so nothing here can be authenticated, nothing here grants a right, and an
// agent that wants to impersonate another can. That is acceptable because the
// alternative — a token per agent — buys nothing when the agents are all the
// operator's own processes with full access to the same files. The ledger's value is
// that claims are *written down*, not that they are unforgeable.
type Identity struct {
	// Agent is the kind: a runtime name such as "claude-code" or "codex", or the
	// name an agent declared for itself.
	Agent string `json:"agent"`
	// Session is the agent's own session id when one can be read.
	Session string `json:"session,omitempty"`
	// Name is the display identity stored on the claim.
	Name   string `json:"name"`
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	Cwd    string `json:"cwd,omitempty"`
	// Evidence says which variable the detection used, so a surprising identity can
	// be explained instead of guessed at.
	Evidence []string `json:"evidence,omitempty"`
	// Known is false when nothing identified the caller. Such an identity is shared
	// by every unidentified process, so it may not write or close a claim: see
	// requireIdentity.
	Known bool `json:"known"`
}

// String renders the identity the way claims store it: "agent/1a2b3c4d".
func (i Identity) String() string {
	if i.Session != "" {
		return i.Agent + "/" + short(i.Session)
	}
	return i.Agent
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// runtime is one agent runtime ftop can recognise on its own.
//
// The table is small on purpose: a marker listed here has to be one ftop has seen or
// one the runtime documents, and a wrong guess would record the wrong owner. An agent
// that is not in the table is not left out — it is covered by AI_AGENT (the
// cross-vendor "<kind>_<version>_agent" convention) or by declaring itself with
// FTOP_AGENT, which the manual tells every agent to do. Detection never needs a
// release to learn about a new agent.
type runtime struct {
	kind string
	// markers identify the runtime when any is present.
	markers []string
	// prefix markers identify it through AI_AGENT, matched as a prefix because
	// AI_AGENT carries the client version.
	prefixes []string
	// sessions are the variables that name this conversation, first non-empty wins.
	sessions []string
}

var runtimes = []runtime{
	{
		kind:     "claude-code",
		markers:  []string{"CLAUDECODE"},
		prefixes: []string{"claude-code"},
		sessions: []string{"CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID"},
	},
	{
		kind:     "codex",
		markers:  []string{"CODEX_HOME", "CODEX_THREAD_ID"},
		prefixes: []string{"codex"},
		// CODEX_THREAD_ID is the conversation. Without it every Codex session was
		// recorded as plain "codex", so two unrelated conversations were one owner
		// and could release each other's claims by exact match. With it they are
		// codex/<thread>, the same shape as claude-code/<session>.
		sessions: []string{"CODEX_THREAD_ID", "CODEX_SESSION_ID"},
	},
}

// DetectIdentity reads the environment ftop was started in.
//
// Three ways to be recognised, in order:
//
//  1. FTOP_AGENT — the agent declares itself. This is the universal path: it works
//     for any runtime, including one ftop has never heard of, and it is what the
//     manual instructs every agent to set. FTOP_SESSION names the conversation when
//     the runtime has an id of its own.
//  2. A known runtime's marker variables (Claude Code, Codex), with its own session
//     variable.
//  3. AI_AGENT, the cross-vendor convention "<kind>_<version>_agent". The version
//     suffix is stripped, so a client upgrade does not change the owner.
//
// Anything else is "unknown", and it stays unknown: promoting an unidentified caller
// to the operator would quietly give it the operator's standing. An unknown identity
// is shared by every unidentified process, so the command layer refuses to claim or
// release under it (see requireIdentity) rather than letting two strangers act as
// one owner.
func DetectIdentity() Identity {
	id := Identity{Agent: "unknown"}
	if v, err := os.Getwd(); err == nil {
		id.Cwd = v
	}
	if repo, branch := gitRepo(id.Cwd); repo != "" {
		id.Repo, id.Branch = repo, branch
	}

	env := os.Environ()
	get := func(k string) string {
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, k+"="); ok {
				return v
			}
		}
		return ""
	}
	first := func(keys ...string) (string, string) {
		for _, k := range keys {
			if v := strings.TrimSpace(get(k)); v != "" {
				return v, k
			}
		}
		return "", ""
	}

	// 1. The agent's own declaration.
	if v := strings.TrimSpace(get("FTOP_AGENT")); v != "" {
		id.Agent = cleanKind(v)
		id.Evidence = append(id.Evidence, "FTOP_AGENT")
		if s, k := first("FTOP_SESSION"); s != "" {
			id.Session = s
			id.Evidence = append(id.Evidence, k)
		}
		id.Known = id.Agent != ""
		id.Name = id.String()
		return id
	}

	aiAgent := strings.TrimSpace(get("AI_AGENT"))

	// 2. A runtime ftop knows by its own markers.
	for _, rt := range runtimes {
		hit := ""
		for _, m := range rt.markers {
			if strings.TrimSpace(get(m)) != "" {
				hit = m
				break
			}
		}
		if hit == "" {
			for _, p := range rt.prefixes {
				if strings.HasPrefix(aiAgent, p) {
					hit = "AI_AGENT"
					break
				}
			}
		}
		if hit == "" {
			continue
		}
		id.Agent = rt.kind
		id.Evidence = append(id.Evidence, hit)
		if s, k := first(rt.sessions...); s != "" {
			id.Session = s
			id.Evidence = append(id.Evidence, k)
		}
		id.Known = true
		id.Name = id.String()
		return id
	}

	// 3. Any other runtime that follows the AI_AGENT convention.
	if kind := kindFromAIAgent(aiAgent); kind != "" {
		id.Agent = kind
		id.Evidence = append(id.Evidence, "AI_AGENT")
		if s, k := first("FTOP_SESSION", "AI_AGENT_SESSION", "AGENT_SESSION_ID"); s != "" {
			id.Session = s
			id.Evidence = append(id.Evidence, k)
		}
		id.Known = true
	}

	// 4. A person at a terminal.
	//
	// An agent's tool shell is a pipe; a person's shell is a terminal. That is the only
	// signal separating the two, and it grants nothing: no branch in ftop looks at
	// "human" to allow an operation (release -any-owner is its own flag, recorded
	// whoever runs it). So recording "human" here is a label, and the cost of being
	// wrong is a claim that says human where it should say an agent — not an agent
	// gaining the operator's standing.
	if !id.Known && atTerminal() {
		id.Agent = "human"
		id.Evidence = append(id.Evidence, "tty")
		id.Known = true
	}

	id.Name = id.String()
	return id
}

// atTerminal reports whether this command was typed by a person.
//
// Both ends are checked: an agent that merely inherited a terminal on one stream (a
// wrapper piping stdout) is not a person at a shell.
func atTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// kindFromAIAgent turns "claude-code_2-1-303_agent" into "claude-code".
//
// AI_AGENT is assembled as "<kind>_<version>_agent". The version starts at the first
// underscore followed by a digit, so a kind may contain hyphens and underscores but
// does not carry the client's version into the owner (which would change with every
// upgrade and split one agent's claims in two).
func kindFromAIAgent(v string) string {
	if v == "" {
		return ""
	}
	v = strings.TrimSuffix(v, "_agent")
	for i := 0; i < len(v)-1; i++ {
		if v[i] == '_' && v[i+1] >= '0' && v[i+1] <= '9' {
			v = v[:i]
			break
		}
	}
	return cleanKind(v)
}

// cleanKind keeps a declared or derived runtime name to the shape an owner may take:
// letters, digits, dot, dash, underscore, slash. It is a label other agents read, so
// whitespace and control characters are refused rather than carried into the ledger.
func cleanKind(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '/':
			b.WriteRune(r)
		}
	}
	out := b.String()
	// A name that starts or ends with a separator, or is only separators, identifies
	// nothing.
	if strings.Trim(out, "-_./") == "" {
		return ""
	}
	return out
}

// requireIdentity refuses to write or close a claim under an identity ftop could not
// establish.
//
// Every unidentified process shares "unknown", so letting it claim would put several
// unrelated agents on one owner — and letting it release would let any of them close
// the others' claims by exact match, which is precisely the eviction the ledger
// exists to prevent. The refusal names the two ways to fix it, and it costs an agent
// one line in its own instructions.
func requireIdentity(id Identity, action string) *ExitError {
	if id.Known {
		return nil
	}
	return &ExitError{Status: ExitUsage, API: &APIError{
		Code: CodeUsage,
		Message: "ftop cannot tell which agent this is, and " + action +
			" under a shared identity would let unrelated agents act as one owner",
		Hint: "pass -as <name> with a name of your own, or export FTOP_AGENT=<name> " +
			"(and FTOP_SESSION=<id> if your runtime has one) before running ftop. " +
			"`ftop whoami` shows what would be recorded.",
	}}
}

// gitRepo reports the repository root and branch for cwd.
//
// Best effort and display-only: agents are often started outside a repo, and a
// failed lookup must not make a claim fail.
func gitRepo(dir string) (repo, branch string) {
	if dir == "" {
		return "", ""
	}
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel", "--abbrev-ref", "HEAD")
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) >= 1 {
		repo = filepath.Base(strings.TrimSpace(lines[0]))
	}
	if len(lines) >= 2 {
		branch = strings.TrimSpace(lines[1])
	}
	return repo, branch
}

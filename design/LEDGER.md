# The agent-facing CLI and the ledger

Written 2026-10-19, after four rounds of questions and two rounds of adversarial
review (two models, each attacking the other's design as well as the lead's
recommendations).

## What the user decided

1. Cards are shared only by the operator and the operator's own agents.
2. Agents run on one machine and reach the GPU hosts over ssh.
3. One card, one holder at a time. Several processes on that card are the holder's
   business; splitting VRAM is explicitly **not** ftop's job.
4. ftop **maintains state only**. It starts nothing, runs nothing, kills nothing,
   resets nothing, reboots nothing.
5. The operator assigns work and arbitrates conflicts; between tasks it is
   first-come-first-served. Agents are expected to coordinate with each other, and
   the operator's own instruction wins without needing to be re-confirmed.
6. A CPU-only claim exists: it occupies a machine's CPU without taking a card.
7. `ftop skill` is installed into `~/.agents/skills` with the others symlinked to it.

Decisions 3 and 4 together removed most of the design both reviewers had produced.

## Shape

```
Control machine                        GPU hosts
┌──────────────────────────┐           ┌──────────────────────┐
│ ftop CLI (JSON)          │           │ ftop-probe (static)  │
│ ftop TUI                 │──ssh──────│  NVML + /proc        │
│ SQLite ledger (WAL)      │           │  read-only           │
│  ~/.local/state/ftop/    │           │  ~/.cache/ftop/      │
└──────────────────────────┘           └──────────────────────┘
```

Single authority on the control machine. The reviewers' objection to this — "the
laptop sleeps" — does not apply: every agent runs on that machine, so when it sleeps
there is nobody to schedule.

The remote side stays observation-only: the probe is read-only, and ftop writes
nothing but the probe binary to `~/.cache/ftop/`.

## What was rejected, and why

| Rejected | Why |
|---|---|
| A central controller with authentication and roles | One user, one machine. It would only turn "look at the fleet" into "operate a service first". |
| `held → begin` + 30 s heartbeat + suspect/reconcile | LLM agents block inside one command for hours and cannot heartbeat. Every claim would end in `suspect` and stay there. |
| Binding claims to `FTOP_LEASE` in `/proc/<pid>/environ` | Agents forget to export it; torchrun rewrites `CUDA_VISIBLE_DEVICES`; Ray workers are daemon-spawned; a setuid process is unreadable even at the same uid. |
| Per-host ledger shards | Agents never run on the hosts, so the shard buys nothing and loses global idempotency. |
| Shared-card VRAM accounting | The user's answer withdrew it: one card, one agent. |
| `--force` on another agent's claim | An agent that can evict an agent can destroy hours of work. The stated path is to ask the operator. |
| Running, killing, resetting, rebooting | The user withdrew it in round 4. |

## Invariants

- **One open claim per card**, enforced by `CREATE UNIQUE INDEX claims_one_open_per_card
  ON claims(gpu_uuid) WHERE closed = 0`. The index, not a SELECT, is the authority.
- **`--why` and `--for` are required.** A claim with no reason is the thing this
  ledger exists to prevent; one with no deadline is one nobody will notice is dead.
- **`unknown` is never `human`.** An unrecognised caller is recorded as `unknown`
  unless it is a person at a terminal, which is a check on the two streams, not a
  promotion: no branch of ftop allows an operation because the caller is human. (A
  person typing `ftop claim` must work without `-as`; an agent's tool shell is a pipe,
  so the two are distinguishable.)
- **Identity is a label, not a credential.** Every agent runs as the same unix user;
  nothing here can be authenticated, and the design does not pretend otherwise.
- **Absent is never zero.** A process list that could not be enumerated is
  `procs_known: false`, never `procs: 0`.
- **The reason field is data.** It is sanitised of control and bidi characters on the
  way in, and never enters a command line, a SQL string or a shell.

## Expiry

A deadline does not release a card. `Sweep` closes an overdue claim only when the
probe enumerated that card's processes and found none; if the enumeration failed or a
process is present, the claim stays and is reported `overdue`. The cost of being
wrong this way is a stuck card a human can see; the cost of the other way is two
agents writing to one GPU.

`Sweep` runs inside `gpu list` and `gpu show` whenever they observe, because those are
the only moments ftop has an observation and there is no daemon. In the first commit
it existed and was tested but had no caller, so a deadline changed nothing; both
reviewers found that independently.

## Observation versus record

The ledger and the probe can disagree, and the CLI states the disagreement rather
than resolving it:

| ledger | probe | reported as |
|---|---|---|
| free | no processes | free |
| free | processes | `unclaimed` — nobody claimed it, confirm before taking it |
| free | memory with no process on this host | `unclaimed` — another slice, or a stale allocation |
| free | enumeration failed | `unknown` — cannot confirm idle |
| held | processes | agrees |
| held | no processes, overdue | `overdue` — the holder should release |
| held | no processes, fresh | agrees (a holder may be between jobs) |

The unattributed-memory threshold is `max(1 GiB, 5%)`: idle cards were measured
reporting under 600 MiB with zero processes, so anything smaller would flag every idle
card.

## Identity

Three ways to be recognised, in order:

| Way | What it sees |
|---|---|
| `FTOP_AGENT` / `FTOP_SESSION` | The agent declares itself. Works for any runtime, including one ftop has never heard of. |
| A known runtime's markers | Claude Code exports `CLAUDECODE`, `CLAUDE_CODE_SESSION_ID`; Codex exports `CODEX_HOME`, `CODEX_THREAD_ID`. `AI_AGENT` carries the client version (`claude-code_2-1-303_agent`), so it is matched by prefix. |
| The `AI_AGENT` convention | `<kind>_<version>_agent` for any other runtime; the version is stripped so an upgrade does not change the owner. |

A person at a terminal is identified by the terminal itself: an agent's tool shell is a
pipe, so stdin and stdout both being a tty is the one signal that separates them. It is
a label and it grants nothing — no branch of ftop allows an operation because the caller
is human — so being wrong about it costs a claim that says human where it should say an
agent, not an agent gaining the operator's standing.

An unidentified caller is `unknown`, and claiming or releasing under it is refused: every
unidentified process shares that one owner, so any of them could close the others'
claims by exact match. The refusal names `-as` and `FTOP_AGENT`.

## The skill

`ftop skill install` writes the manual to `~/.agents/skills/ftop/SKILL.md` and links it
into every runtime directory the user has: `~/.claude/skills`, `~/.codex/skills`,
`~/.cursor/skills`, `~/.gemini/skills`. `~/.agents/skills` is the interoperable
location, read directly by Cursor and Gemini CLI. One real copy plus links is the only
arrangement that cannot drift; a second real copy goes stale as soon as one build writes
it.

A skill, though, is only found by a model that goes looking for one. Install therefore
also writes a four-line marked block into the file each runtime loads at the start of
every session — `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.gemini/GEMINI.md`,
`~/.config/opencode/AGENTS.md` — pointing at the manual. That is what covers an agent
that has never heard of ftop: it is told before it starts work, not only if it happens
to search. The block is the only thing ftop ever touches in those files; `skill
uninstall` removes it and leaves the rest as it was.

`npx skills@latest add <pkg> -g` also works and produces the same layout, but its
`--agent codex` filter was measured not to write `.codex` at all, so ftop does not
shell out to it.

An existing real directory is never replaced; the install reports the conflict
instead.

## One way to reach a machine

The CLI observes through the same `sshx.Client` as the TUI — `ssh <alias>` with the
user's own ssh configuration — so a new subcommand cannot grow a second way of
contacting a machine. Every host in the ssh configuration is managed alike; ftop
ships with no list of special machines.

A container that is a slice of a larger chassis sees only its own cards: NVML,
`/dev/nvidia*` and `/proc/driver/nvidia/gpus/` agree on the slice's cards, and the
memory each reports is accounted for by the slice's own processes. So one slice's
cards cannot leak into another slice's inventory, and the ledger's card list per
host is the host's own.

## Fixed first, before any ledger code

1. **TOML parsing.** The hand-rolled reader had two safety-relevant defects, both
   reproduced against a real `~/.ssh/config`:
   - `drop = ["*"]   # off` matched nothing and returned the whole fleet (10 hosts)
     instead of zero. Inline comments are now parsed properly.
   - An unknown section after `[[host]]` wrote its keys onto the last host, so
     `[ledger] hostname = "…"` silently rewrote another host's address.
   A real TOML parser replaced it, and an unknown key is now a load error.
2. **Process enumeration.** `ProcsKnown` never reached the wire, and the reader
   treated "no P records this round" as "keep the last list", so a finished job stayed
   on its card forever. The header now carries a `procs_sampled` bit and each card a
   `K` record; a round that did not sample carries the old list as *unknown*, and a
   sampled round replaces it.
3. **UTF-8 in process labels.** The 40-cell cut was by byte, producing invalid UTF-8
   on the wire. It is by rune now.

Each has a regression test that fails on the old code.

## Testing

- `internal/config`: the two TOML defects, plus the existing empty-fleet property.
- `internal/ledger`: 21 tests, including a concurrency test that races 8 claims at one
  card and requires exactly one winner, and the three expiry cases (no observation,
  enumeration failed, observed idle).
- `internal/cli`: the claim → conflict → release loop, flag-after-positional, stable
  error codes, `unknown` is not `human`, JSON-only stdout, empty lists as `[]`, and
  `owner` as an explicit `null`.
- `internal/ui`: the footer holder strip, per-holder card counts, the overdue mark, and
  that a board with no ledger renders byte-identically to before.

## Not done on purpose

- No queueing, no priority, no preemption.
- No per-card VRAM reservation.
- No MCP server. The CLI plus the generated skill is the surface; an MCP adapter would
  be a thin wrapper over the same JSON.
- No daemon. Each command opens the ledger, reads or writes one transaction, and exits.


## Review of the first commit

Both reviewers (one per model family) ran the CLI against throwaway ledgers with
`-no-ssh` and never contacted a host. What they found, and what changed:

| Finding | Fix |
|---|---|
| Concurrent claims from separate processes failed with `SQLITE_BUSY` (5 of 24 at once). A deferred transaction that upgrades to a writer does not consult the busy handler. | `_txlock=immediate`; 24 simultaneous claims now give 24 distinct cards, 20 on one card give 1 winner and 19 `held`. |
| `journal_mode(WAL)` in the DSN ran before `busy_timeout`; opening a non-WAL file under a held lock failed in 0.3 ms. | WAL is set once after the connection is configured, with a bounded retry. The regression test fails on the old DSN. |
| `release -any-owner` let any agent free another's card and logged no actor. | The event records the identity that ran it, with cause `override`; the skill names it the operator's. |
| A restarted agent (new session id) could neither list nor release its own claims. | `-mine` and `release` match the agent kind; another kind is still refused, and an explicit `-as` name is never treated as a kind. |
| `Sweep` had no caller. | Wired into `gpu list` and `gpu show`. |
| `-gpu` ignored `-host` and `-min-vram`. | Both are checked against the chosen card. |
| A card that left its host stayed claimable. | Schema v2 `cards.present`, set from complete observations only. |
| Trailing flags and stray arguments were silently dropped (`history ID -n 1`, `claim … word -as bob`). | Positional commands reorder once; flag-only commands refuse leftovers. |
| `summary.unknown` was 0 under `-no-ssh`. | Derived from the observation, not from conflict prose. |
| `gpu why -no-ssh=false` swallowed an observation failure. | Reported as `no_observation`, like `gpu list`. |
| A misspelled subcommand opened the TUI; `ftop version` exited 255 with no output. | Unknown bare words are a JSON usage error; `version` is a real command. |
| `-host gpu-4` (the label every reader sees) matched nothing and exited 0. | `config.ResolveHost` maps label, alias and stanza aliases to the alias. |
| C1 controls (U+009B, U+0085) survived sanitising; an owner could sanitise to "". | Both stripped; an empty owner is refused; owners compare in canonical form. |
| A ledger read error cleared the TUI's holders and showed nothing. | The last holders are kept and the status line says they may be stale. |
| `skill install` pointed at `entries[].error` in an envelope with no entries. | The entries travel in `error.conflicts`, and the message lists the failed paths. |
| The skill told agents to read the holder's `directive`, which the conflict did not carry. | `HeldError` and `CardOwner` carry it. |

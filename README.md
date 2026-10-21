# ftop

A live GPU, CPU and memory monitor for a whole fleet of machines: one `ftop`
command, every development host on one screen. It borrows btop's drawing
techniques — braille history graphs, `■` meters, a boxed layout — and uses its own
palette, specified in OKLCH so that a reading's colour and its size move together.

```
╭─────────────────────────┤23:44:07├─────────────────────────┬────────────────────────┤✕ 2 down├───╮
│                          ▇ ▄ ▇ ▇ ▇ ▄ ▄ ▄     ▃       ▁   ▅  │ 11/13 hosts   44 gpus   44/44 read │
│ util   ▃ ▅ ▄ ▄ ▄ ▅ ▃ ▇   ▄ ▄ ▄ ▄ ▄ ▄ ▄ ▄   ▃ ▄ ▇ ▄ ▅ ▄ ▇ ▄  │  6 idle  28 ≥30%  18 ≥60%  11 ≥85% │
│        ─── rig-08 ────   ── dc-a ───────   ─── node-a ────  │ util  ■■■■■■░░░░░░░░ 49%           │
│ vram   ▁ ▅ ▁ ▅ ▂ ▅ ▂ ▄   ▀ ▀ ▀ ▀ ▀ ▀ ▀ ▀   ▀ ▁ ▀ ▁ ▀ ▀ ▀ ▀  │ vram  ■■■■■■■■░░░░░░ 63%  1.4T/2.2T│
│                          ▂ ▁ ▂ ▁ ▃ ▁ ▃ ▁   ▇   ▇            │ power ■■■■■■■░░░░░░░ 52%  11.6/22.4│
╰─────────────────────────────────────────────────────────────┴────────────────────────────────────╯
╭─┤● rig-08├─────────────────────────────────────────────────────────────────┤8× RTX 4090 · 24.0G├─╮
│ 0   ■■░░░░░░░░░░░░ 17%    ■■■■■■░░░░░░░░ 43%     10.3G    37°C     97W   ⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀ │
│ 1   ■■■■░░░░░░░░░░ 31%    ■■░░░░░░░░░░░░ 18%      4.3G    44°C    151W   ⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣤⣠⣤⣤⣤⣤⣤⣤⣤⣤⣤ │
│ 2   ■■■░░░░░░░░░░░ 22%    ■■■■■░░░░░░░░░ 42%     10.0G    41°C    117W   ⣤⣤⣤⣤⣤⣤⣤⣀⣄⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀ │
│ 3   ■■■░░░░░░░░░░░ 26%    ■■░░░░░░░░░░░░ 19%      4.6G    41°C    132W   ⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀ │
╰──── util ──────────────── vram ───────────────────────────────────────── util history ───────────╯
```

(`░` is an unfilled meter cell in plain text; in a terminal both halves are `■`,
told apart by colour alone.)

## Install

```bash
brew install To1and/tap/ftop
```

Or from source:

```bash
git clone https://github.com/To1and/ftop && cd ftop
make install        # builds the probe and ftop, installs to ~/.local/bin/ftop
```

Nothing is installed on the remote hosts. The probe is a 1.9 MB static Go
binary; it is pushed on first connect, cached in `~/.cache/ftop/`, and only
executed after that.

## What it does

- **Manages every host in `~/.ssh/config`.** Every concrete `Host` entry is a
  machine ftop watches and the ledger hands out; there is no built-in list of
  names to keep or skip, and no machine is special. Leaving a machine out is the
  user's decision, made with `drop` in the ftop config.
- **Connects with plain `ssh <alias>`.** Your ssh configuration applies exactly
  as it does to your own ssh — `Include`, `Match`, `ProxyJump`, `ProxyCommand`,
  keys and certificates. ftop reads the file only to find the host names (it
  follows `Include`, skips `Match`, ignores keywords it does not know, and never
  runs `ssh -G`, which would execute `Match exec`). Its own connections use
  control sockets in a directory private to the run.
- **Machines without a GPU are hosts too.** A Linux machine with no NVIDIA
  driver reports its CPU and memory with zero cards, in the board and in
  `ftop gpu list`, instead of failing.
- **Says when the probe cannot run.** The probe is a linux/amd64 binary. A host
  of another platform — a Mac, an ARM box, a Windows machine with an ssh server —
  is shown as "unsupported platform <what uname says>" rather than as a connection
  failure, and `ftop gpu list` gives every unobserved host's reason in `error`.
  Nothing else in ftop assumes the platform of either end.
- **One resident ssh stream per host.** The remote samples every second and
  streams continuously. It does not reconnect every second: the handshake is
  ~628 ms, a round of actual work ~1 ms.
- **Two sampling rates.** Utilisation, VRAM, temperature and power every second
  (~2 ms for 8 cards); the process list every 5 seconds (~17 ms for 8 cards).
  Between process samples the last list is **kept** and marked old, so it does
  not blink out every 5 seconds.
- **A skyline of the fleet.** The header draws each GPU as a bar, utilisation
  rising above an axis and VRAM hanging below it, with each host's name on the
  axis: which hosts are full, which have free cards, where memory is tight.
  Each host stands on its own stretch of axis. A bar's height is its reading, so
  every bar takes its metric's one colour — green for utilisation, blue for
  memory — and nothing else: shading along the height said the number twice and
  faded the chart to near-white at the top. Both bands end on the same glyph, so
  the hanging VRAM band has one straight edge rather than a sawtooth, and a card
  that reported a figure keeps a half cell even at 0.4% so it reads as present
  rather than as a spike. Hosts that do not fit in the chart's
  width end
  in `…`, and below about 87 columns the chart gives way to the panel. The panel
  has the counts, the cards by utilisation band (idle, then ≥30%, ≥60%, ≥85%,
  each count in its band's colour; a card at 90% counts in all three), then one
  row per meter — util, vram, power, and the hosts' own cpu and mem, each with its
  label, bar, percentage and absolute reading in the same columns as all the rest.
  Click a host's bars to jump to it.
- **One row per GPU, two bars.** Utilisation and VRAM each get a short bar (at
  most 14 cells) and a percentage, then temperature, power and **that card's
  own** utilisation history, `util history`, newest at the right. The column
  names are on each card's bottom border, each in its metric's colour. Bars
  are capped because past ~14 cells they add no precision; the width goes to
  more lanes and longer graphs. On a narrow lane a GPU takes two lines, one per
  kind of fact: utilisation with temperature and power, then VRAM with its
  used/total size. Every other GPU sits on a faint band. The card's label names
  the model in its product line's colour (`8× RTX 4090 · 24.0G`).
- **Every card leads with its machine.** Two lines above the GPUs: `cpu` with a
  meter, its percentage and busy-of-available cores, and `mem` with a meter, its
  percentage and used-of-limit — both denominators the cgroup's when one limits
  the scope, the machine's otherwise. The rest of those lines carries the
  machine's other facts, right-aligned on the card's margin so a row of cards has
  one right edge: `load`, then the process count; `cache`, then uptime. A narrow
  lane keeps the facts that fit and drops the rest whole — a reading is never cut,
  and the load figure is never written over the core count.
- **Lanes by width, packed in bands.** About 80 columns per lane, with a column
  and a row of space between cards. Cards of the same size line up in rows, and
  smaller cards stack beside a taller one when they fit in its height (a 2-GPU
  and a 1-GPU host beside an 8-GPU one). More lanes only when the board does
  not fit, scrolling only after that.
- **A stable order.** Hosts sort by online → connecting → failed, then GPU
  count, then name, **never by load**. However the load moves, cards stay put;
  only a host coming online or dropping off re-sorts the board. The selection
  follows the host, not the position. The one exception: an online host that
  reports a new GPU mid-session gets taller and is re-sorted by its new count. A
  sample that merely omits a card does not move anything; that card keeps its
  row and shows `—`.
- **No process information on the board.** Owners, script names and process
  counts are only in a host's view: dc-a runs 16 processes per card, and
  that row would squeeze two bars into one.
- **Process names that mean something.** The probe reads the real script and
  `--task_name` from `/proc/<pid>/cmdline`, showing `worker.py:pick_place`
  rather than `python`.
- **Failures are visible.** `●` fresh, `◐` stale sample, `◌` connecting, `✕`
  unreachable. An unreachable host keeps its place and shows why; it never
  silently disappears.
- **An unread value is not 0.** A field the driver refused to report shows `—`
  and is left out of the fleet means. Printing a value never measured as 0% is
  the worst mistake this tool could make.

## Keys and mouse

| Key | Action |
|---|---|
| `↑↓←→` / `hjkl` | move the selection between cards (up/down in a lane, left/right across) |
| `tab` | next host in board order |
| `enter` | open the selected host |
| `pgup` / `pgdn` | page the board or the host view |
| `← →` / `k j` | in a host: previous / next host |
| `↑` / `↓` | in a host: scroll |
| `c` | per-GPU utilisation history on / off |
| `?` / `esc` | help / close |
| `q` / `ctrl-c` | quit |
| click | select a card or a host in the skyline; click it again to open it |
| wheel | scroll the board or the host view |
| click a status hint | does what its key does |

With the mouse on, hold Shift (Option in iTerm2) to select text, or run
`ftop -mouse=false`.

## Options

```bash
ftop                 # every host
ftop -host node      # only hosts whose name contains "node"
ftop -list           # list every managed host, then exit
ftop -once           # sample once and print one plain-text frame (scripts, screenshots)
ftop -interval 2s    # sampling interval
ftop -graph 0        # per-GPU graphs off
ftop -mouse=false    # no mouse reporting; plain text selection
ftop -demo           # a synthetic fleet; no host is contacted
ftop -once -json -   # sample once and write a JSON snapshot
ftop -version
```

## Configuration

None is needed. To add or remove hosts or change labels, write
`~/Library/Application Support/ftop/config.toml` on macOS
(`$XDG_CONFIG_HOME/ftop/config.toml` elsewhere — ftop uses `os.UserConfigDir`,
not `~/.config`; see `ftop.example.toml`):

```toml
[general]
interval = 1.0
columns  = 0            # at most this many lanes; 0 = fit the terminal
drop     = ["my-laptop"]

[labels]
"lab-node-1" = "node-a"
```

`drop` matches the ssh alias, the card label or the HostName, with wildcards.
Without `[labels]`, a naming prefix the whole fleet shares (such as `lab-`) is
stripped from the titles. An explicit empty fleet (`drop = ["*"]`) stays
empty; it never falls back to monitoring every host.

The file is parsed by a real TOML parser and every unknown key is an error: a
typo in `drop` must never silently widen the fleet. Inline comments are honoured,
and a section ftop does not know can no longer be read as another `[[host]]`.

## For agents: the ledger

ftop records who holds which GPU card and why. It never starts, stops or kills
anything — that is the agent's job — so the ledger is a record, not a scheduler.
Agents reach the GPU hosts over ssh, so one SQLite database on the machine the
agents run on can be the single authority, and watching a card needs nothing
installed on the hosts.

```sh
ftop gpu list -safe                  # cards free AND idle: what to take
ftop gpu list -free                  # cards with no claim (may be busy)
ftop claim -host <host> -why "train ~3h" -for 3h
ftop release L20260101T120000-1a2b3c4d
ftop gpu why                         # one line per card: who, and why
ftop history <gpu-uuid>              # who had it before, and how it ended
ftop skill install                   # manual into ~/.agents/skills, linked everywhere
ftop whoami                          # the identity ftop will record for you
```

`ftop skill install` writes the manual once and links it into every agent runtime
present on the machine — `~/.claude/skills`, `~/.codex/skills`, `~/.cursor/skills`,
`~/.gemini/skills` — and appends a short marked block to the file each runtime
loads at the start of every session (`~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`,
`~/.gemini/GEMINI.md`, `~/.config/opencode/AGENTS.md`). A skill alone is found only
by a model that goes looking for one; the block is what reaches an agent that has
never heard of ftop, before it starts a job. `ftop skill uninstall` removes both
and leaves the rest of those files untouched.

An agent is identified by `FTOP_AGENT`/`FTOP_SESSION` if it sets them, by its
runtime's own variables otherwise (Claude Code, Codex), and by the `AI_AGENT`
convention (`<kind>_<version>_agent`) for anything else. A caller none of those
identify is `unknown`, and cannot claim or release: every unidentified process
shares that one owner, and any of them could close the others' claims.

Hosts may be named by the label the board shows (`gpu-4`) or by their ssh alias
(`lab-gpu-4`); `gpu show` also accepts `label:index`.

Every command prints one JSON object on stdout and nothing else; diagnostics go
to stderr (`ftop help` and `ftop skill print` are text, for a person). Failures
carry a stable `error.code`, and exit status 3 means a card is held —
`error.conflicts` names the holder, their session, their reason, the operator
instruction behind the claim, and when it is due back, which is what an agent
needs to decide whether to wait, ask the holder, or ask the operator.

Rules enforced rather than documented:

- **One card, one open claim**, by a partial unique index in the schema, so a
  crash between the check and the insert cannot double-book a card. Every
  transaction takes the write lock up front, so two agents claiming at once get
  one success and one `held`, never a lock error.
- **A deadline is not an eviction.** An overdue claim keeps its card. It is
  closed only when `gpu list` or `gpu show` observes that card and the probe
  enumerated its processes and found none; a process list that could not be
  read is never read as "idle".
- **Only the holder releases.** An agent's identity is `<agent>/<session>`; a
  restarted agent of the same kind may release its own earlier claims, another
  kind may not. `release -any-owner` is the operator's override and is recorded
  in the history under whoever ran it.
- **A card that leaves its host stops being claimable.** Its row is kept for the
  history and reported `present: false`; a host that could not be reached keeps
  its cards.

`ftop` also says when the ledger and reality disagree. A card with no claim but
running processes is reported as `unclaimed`, with the memory no process on that
host accounts for — the signature of a job started outside ftop. `summary.unknown`
counts every card whose process list was not read, including all of them under
`-no-ssh`: unknown is not free.

## What the numbers mean

- **VRAM**: `memory.used` as NVML reports it, the same number `nvidia-smi` shows.
- **Power**: NVML returns **milliwatts**, divided by 1000. It once was not, and
  an idle 27 W card read 27791 W.
- **Host memory**: cgroup usage **minus reclaimable page cache**
  (`inactive_file`). `memory.current` alone counts file cache, and an idle host
  read "78% full". Without cgroup accounting it falls back to
  `MemTotal - MemAvailable`, both from the same layer.
- **CPU**: the `usage_usec` delta from `cpu.stat` over the cgroup quota; 100%
  means the quota is saturated, not every physical core. The `/proc/stat`
  fallback sums only the busy fields (idle and iowait excluded). The first
  sample has no baseline and shows `—`.
- **`[N/A]` is not shown**: H200 cards report no fan speed, so there is none,
  rather than a fake 0.
- **Per-process VRAM**: some drivers always report 0 (dc-a at 72/140G still
  said 0). It shows `—`, not `0M`.

## Performance

Measured on a 200×58 terminal:

| | |
|---|---|
| one frame | ~0.4 ms |
| NVML fast fields | ~2 ms / 8 cards |
| NVML process list | ~17 ms / 8 cards (every 5 s) |
| ssh connect | 628 ms (209 ms reused) |
| per-round transfer | ~1 ms |
| probe size | 1.9 MB, `CGO_ENABLED=0` |

## Development

```bash
make            # build the probe and ftop
make test       # unit tests
make race       # race detector
make vet
```

| Path | Role |
|---|---|
| `cmd/ftop/` | the program: host discovery, one reader per host, the Bubble Tea shell |
| `cmd/ftop-probe/` | the remote sampler (linux/amd64, embedded in ftop) |
| `internal/nvml/` | libnvidia-ml FFI (purego, no cgo) |
| `internal/sshx/` | one ssh stream per host, TSV parsing, probe cache and push |
| `internal/ui/` | layout, the cell canvas, ANSI output, keys and mouse |
| `internal/theme/` | btop's drawing primitives (braille graph, meter) and ftop's palette |
| `internal/wire/` | the record format the probe and the UI share |
| `internal/config/` | host discovery from ssh_config, TOML overrides |
| `internal/demo/` | the `-demo` fleet, delivered through the same messages as ssh |
| `cmd/protogen/` | renders the real `internal/ui` with demo data to HTML and an xterm.js replay |
| `design/` | layout notes and the handover record |

### Why some details look the way they do

- **Colour is specified in OKLCH, and lightness means "how much".** Every
  measurement owns a ramp with a start hue, an end hue and a lightness band; a
  reading picks a point along it. The ramps are built so that four rules hold, and
  the tests assert all four: lightness never falls as a reading rises; no two
  ramps meet anywhere; a ramp keeps its place in the gamut from 0% to 100%; and no
  ramp comes close to the grey used for labels. btop interpolated its ramps in
  sRGB between two or three stops, which is why so many of its ramps were one hue
  getting dimmer — across its 41 themes, 102 of 210 ramps got *darker* as the
  value rose, and 118 held a hue arc under 25°.
- **Chroma is a fraction of what sRGB can show, not a fixed number.** At L .50
  blue can hold chroma 0.21 and at L .90 only 0.05, so a ramp written in absolute
  chroma gets silently reduced near its top and every hot end converges on
  near-white. Specifying saturation as a fraction of the ceiling at each point
  keeps a ramp coloured at 100%.
- **The palette is quiet, and quiet is a measured quantity.** The same fraction
  also decides how loud the board is. Kanagawa sits at 0.37–0.73 of the gamut
  ceiling, Nord at 0.20–0.57, Rosé Pine at 0.22–0.81; an earlier ftop palette sat
  at 0.70–0.98, roughly twice Kanagawa's chroma, and read as garish. The ceiling
  here is 0.55 and a test pins it. Lowering the ceiling costs separation, and the
  cost was solved rather than guessed: the six ramps can be placed for a
  worst-pair ΔE of 10.6 at a ceiling of 0.50, 11.5 at 0.55, 12.2 at 0.60 and 12.5
  at 0.70. Paying one ΔE to halve the saturation is the trade this palette makes.
- **A bar walks from its ramp's cold end to the reading, not from 0 to 100.**
  That is the only rule that gives both properties at once: every bar is a
  gradient however short it is, and the bar's last cell is the colour of the
  number printed beside it. btop indexes by cell position, so a quiet bar is
  painted in the alarm colours it has not reached; indexing by value alone makes
  a 5% bar one flat cell.
- **A graph's colour is its height.** Each cell takes the ramp's colour for how
  many dot rows it reaches above the graph's floor, so the peak of a busy graph
  is at the hot end and an idle floor at the cold end, and two cells at the same
  height never differ because one column was busier. A cell has one colour, so a
  braille glyph's four dot rows cannot differ: that is a property of the cell
  grid, not of the rule.
- **Temperature is three fixed bands, not a ramp.** It answers "will this
  throttle", a threshold question, not "how much". A gradient would also reuse
  the warm hues the utilisation ramp already owns, on the row directly above it.
- **`Canvas.Put` takes exactly one glyph and panics on more.** It once silently
  wrote only the `d` of `"ftop"` while every test passed (they only checked that
  rows did not overflow).
- **Titles sit on the border between two strokes, `─┤title├─`.** A terminal
  only places characters in cells, so a shape exists only if some character
  has it. A title closed above and below cost two rows per box, and a frame that
  is both tight around the text and rounded has no characters: `╭╮╰╯` join lines
  through the middle of a cell, and lines against the text run on cell edges
  (`▁▔`), which have no rounded corner. The column names sit on the bottom
  border with no strokes leaving the line.
- **Graphs shade continuously.** A cell has one colour, so a braille glyph's
  four dot rows cannot differ. Each cell takes the ramp at the height of its own
  dots above the floor, so a column's surface is at the hot end and everything
  below it walks down the ramp toward the cold end, and the whole graph reads as
  one surface lit from above. btop's one colour per row of cells painted the
  whole bottom of a two-row graph one yellow. A host's graph runs the full
  height of its device box, its floor on the box's bottom.
- **Colour means a measurement, and scope comes before magnitude.** The six
  ramps split into two families by hue. Device readings are green (util) and blue
  (vram); host readings are violet (cpu) and rose (mem); power is the one warm
  ramp, and swap the one teal. Rose is not a shade of blue on purpose: memory and
  VRAM were the pair that could not be told apart, and a hue range that is
  disjoint from vram's is what fixes it. Swap sits lowest of the six, in a
  dark teal, because it is the host's third resource and the last thing read on
  that page. A card shows all six at once, so a host's memory can never be mistaken for
  a device's — that overlap was the original complaint. Within the device pair the
  lightness bands are identical, so a full util and a full vram look equally
  "full". Units are a step dimmer than their numbers.
  Box frames are a quiet slate; only a host that is stale, connecting or down is
  coloured. GPU models take one hue
  per product line (GeForce, the A/H/B data-centre line, the L/T inference
  cards, workstation cards, Jetson) and a shade of it per model.
- **A failed host leads with the reason.** `connection timed out` in the alert
  colour, the rest of the message dimmer after it.
- **A host's view lists each card's top three processes** by memory and counts
  the rest on the box's border: a card shared by sixteen workers no longer
  takes half the screen.
- **A GPU takes two lines on a narrow lane.** Under 70 columns of card content
  (three lanes on a 200-column terminal) one line left short bars, two-cell
  gaps and an eight-cell graph; two lines stack the VRAM bar under the
  utilisation bar and power under temperature, and give the graph both lines.
- **One rhythm for every row.** A bar and its percentage are one element, a
  cell apart; elements of different kinds are one gap apart (three cells, two
  on narrow lanes), the GPU index included. Readings are right-aligned in fixed
  columns so digits line up down a card, and the graph takes the rest of the
  row so it ends at the same margin it starts.
- **Hand-written NVML signatures are the easiest thing to get wrong in pure Go.**
  Three were fixed: power units (milliwatts), `nvmlDeviceGetName` missing its
  buffer length, and arrays passed by value instead of by pointer; and the
  process list bound the v1 symbol with the v2 struct (24 vs 16 bytes), so every
  process after the first decoded from the wrong offset.
- **The probe checks its cache before uploading.** The old command streamed the
  binary into `cat > $f.part` on stdin, concurrent uploads overwrote each other,
  and hosts died with `mv: cannot stat`. A cache hit now transfers nothing; a
  miss writes to a unique temporary file and renames it atomically.

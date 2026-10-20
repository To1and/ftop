# Project history: devtop → ftop

## Consolidation

An earlier prototype was consolidated from three scattered directories into one project
that can be carried forward.

### Directory consolidation

| Original directory | Contents | Disposition |
|---|---|---|
| `devtop` | First version in Python/Textual (superseded) | Deleted; the **measurement conventions** from its README (cgroup memory, CPU deltas, power units) were migrated into the new README |
| `devtop-proto` | HTML layout prototypes + btop algorithm port | Deleted; the prototype pages and design notes were moved to `design/`, and the btop port had long since gone into the Go implementation |
| `devtop2` | The real Go implementation | Renamed to `ftop` |

The rename covered: the Go module path, the binary name, the remote cache directory `~/.cache/ftop`,
the ssh ControlPath directory, the config directory, and the embedded probe name `ftop-probe`.

### Technical evolution across three stages

1. **Python/Textual version**: it worked, but reconnecting to 16 ssh hosts every second brought the UI to its knees.
   Measurement gave the key numbers: one `nvidia-smi` query 342 ms, ssh handshake 628 ms, actual work per round 1 ms.
   → The conclusion was that persistent connections plus a different sampling method were required.

2. **HTML prototype**: the layout was confirmed. Six variants, rendering from a real cluster snapshot, verification in a 760px narrow window.
   The most valuable output of this step was **the 7 defects found through screenshots**, not the HTML itself:
   the theme parser read the wrong format (the four variants were in fact identical), `rowOptsFor` reserved the maximum meter width
   and wide cards therefore dropped fields, and the process count used the number of records rather than deduplicated pids.

3. **Go implementation**: Bubble Tea v2 + direct NVML calls via purego + one OKLCH palette.
   The pure-Go static probe is 1.9 MB, pushed to the remote cache, zero installation.

## Disposition status of the first external review

The review report had 9 BLOCKERs and 14 MAJORs. **Fixed in the previous round**:
the interval unit (`1000g`), the process enumeration ABI (v1 symbols paired with v2 structs), the clock selector,
the CPU `/proc/stat` fallback, the memory fallback, processes lost between rounds, the narrow-terminal crash, and inline comments in theme files.

**Fixed in that round** (all with tests or end-to-end verification):

| Problem | Impact |
|---|---|
| `drop = ["*"]` fell back to the entire cluster | Explicit exclusion stopped working; machines the user excluded could be connected |
| Probe uploads shared one `.part` temporary path | Concurrent uploads overwrote each other, `mv: cannot stat`, and the host was lost entirely |
| A cache hit still wrote 3 MB into stdin | Wasted bandwidth, and the writer could block |
| A failed query rendered as 0 | "Cannot read" and "idle" were conflated, and the mean was pulled down as well |
| The detail page could not scroll | Devices beyond the 16 cards of a host silently disappeared |
| The detail page had no staleness indication | A sample from 8 seconds ago was presented as the current value |
| Enter opened the wrong host | The screen was sorted by utilisation, but the cursor was an index into name order |
| `?` help was an empty promise | The status line advertised it, but nothing was implemented |
| The bottom status line was overpainted by the layer above | Screen content covered the footer |
| Each cell of the graph held only one sample | Half the horizontal resolution was wasted |
| Quantisation rounded down | A load of 10–20% was drawn the same as idle |
| Wide-character overwriting left orphan continuations | A line containing Chinese labels was one cell narrower, and everything to its right shifted left |
| The forwarding goroutine started before the program was created | Startup samples were dropped into a nil pointer |
| When the queue was full, errors were dropped along with it | A failing host merely looked quiet |
| Backoff exceeded its cap and was never reset | A machine that had once flapped paid the 40 s delay forever |
| `-graph=0` was silently changed to 2 | The user's choice was silently ignored |
| The `columns` setting never took effect | The config file was decoration |

**Still unaddressed** (in order of importance):

- **Curve shape of multi-row fluctuations**: two-sample pairing now matches btop, but glyph-by-glyph comparison has not been done.
- **The TOML parser is hand-written**: it does not support quoted keys, multi-line arrays, or escapes. Adequate for now, but the boundaries are clear.
- **NVML's `Dlclose` and the distinction between required and optional symbols**: bounded within a single process lifetime, so this is not a leak.
- **The readiness criterion for `-once`**: currently "all hosts have a sample or have failed + stable for 1.2 s",
  but while probes are being pushed it still only waits, with no progress feedback.

## Measured baseline (after the changes)

```
13 hosts / 43 GPUs / 200×58 terminal
one frame render    0.64 ms      0.46 MB      2184 allocs
one detail frame    0.46 ms      0.28 MB
NVML fast fields    ~2 ms / 8 cards
NVML processes      ~17 ms / 8 cards (once every 5 s)
end to end          14/14 online, 43 GPUs, all with readings
```

## Possible next steps

1. **Cross-check the Graph output against btop**: run the same data set through the btop source and through this program, and compare glyph by glyph.
2. **Push progress**: the first connection to a brand-new host has to transfer 1.9 MB, and for the duration of `-once` the UI is empty.
   Add a status line "pushing probe → host".
3. **Process table header on the detail page**: the PID/user/name columns currently have no header, so at first glance one has to guess.
4. **Alert thresholds**: currently only colour. Consider configurable highlighting such as "a card above 90% for 5 minutes".
5. **The semantics of `columns`**: it is currently an upper bound rather than a fixed value. If the user wants to force 3 columns, another key is needed.

## Host selection

ftop manages every concrete `Host` in the user's ssh configuration. An earlier
version shipped a "production guard": a list of one installation's machine names
and addresses compiled into the binary, a strict ssh_config reader that refused
`Match` and `Include`, and connections pinned with `ssh -F /dev/null`. It was
removed on 2026-10-19 at the operator's direction: ftop is distributed software,
so it may hold no knowledge of any particular fleet, and every machine is under
central management without exception. Which machines belong to the fleet is the
user's decision, expressed in their ssh config and in `drop` / `[[host]]`.

What survives from that work, because it is not about any machine:

- `drop = ["*"]` leaves the fleet empty and never falls back to discovery.
- config.toml is decoded by a real TOML parser; unknown keys are errors.
- Discovery never runs `ssh -G` (it would execute `Match exec`).
- Connections put `--` before the destination, so a name starting with `-` cannot
  become an ssh option, and use control sockets private to the run.

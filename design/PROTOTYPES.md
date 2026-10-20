# Early prototypes · delivery notes

The HTML prototype pages described below (`design/prototypes.html`, `design/variants.html`) have been removed from the tree and are available in git history at commit 86df5e2.

`out/prototypes.html` — 6 layout variants rendered from a recorded fleet snapshot + 1 detail popup.
The page scales with the window width; the right-hand side is no longer cut off.

## Information boundary of the overview page

The overview page **shows no process/thread information at all**: no owner, no script name, no process count.
Only device-level facts are kept — utilisation, VRAM, power, temperature, SM clock. The process list exists only in the detail popup.

The reason is not just saving space: dc-a has 16 processes per card, and on the main screen that line would squeeze out one of the two progress bars.

## Two progress bars per card

```
 0 ████████████████████▏ 97%  160W 38°  2.0G        ← utilisation (green→yellow→red)
   ████████▌             51%  72.1G/140.4G         ← VRAM (dark red→bright red)
```

Both share the same set of starting columns, so "busy but light on VRAM" and "idle but VRAM full" are a difference in shape, not mental arithmetic.
Both use btop's position-indexed gradient: the same fill ratio necessarily produces the same colour sequence.

## Fields that have been removed

`P1x16` (PCIe generation and width) and `405M` (VRAM clock) **no longer appear in the overview**. They answer "why is it slow",
which is a question for after opening the single-card detail, not for scanning the cluster. Removing them is exactly what gives the width to the second progress bar.

## Consistency with btop

`internal/btop/` is a line-by-line port of btop's rendering algorithm, locked down by tests:

- a 101-entry integer gradient table, including truncating division
- **meter colours are indexed by cell position**, not by value
- a 25-entry braille glyph table, taken from `btop_draw.cpp`
- one gradient index per graph row (colour varies with height)
- btop's `.theme` syntax (removed: the palette is now specified in OKLCH)

## Fixed in that round

1. **HTML was clipped**: the canvas is a fixed character width, `.frame` had `overflow:hidden`,
   and as soon as the window narrowed the right-hand block vanished → changed to `overflow-x:auto` + three media-query steps that scale the font size
2. **The progress bar was squashed into one cell**: the rewrite missed `cx++`, so the whole bar was drawn at a single point
3. **Two-row misalignment**: the GPU step still advanced by 1 row, leaving an empty row between the two → changed to `rr*2`
4. **`16 procs` truncated**: `layout.go` still contained a duplicate stat block that still drew the process count → merged and deduplicated
5. **Empty gap at the right of the card**: meters had a 34-cell cap, so wide cards were not filled → cap removed, the meter takes all remaining width
6. **Fields clipped by total width**: changed to per-field decisions, preferring to drop a whole field over leaving half of one
7. **Border subtitle overflow**: `8× RTX 4090` was pushed into the corner → reserve a corner cell and clip

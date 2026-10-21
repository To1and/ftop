# ftop board layout · as built

The layout was chosen in review rounds with the HTML/xterm.js renders that
`cmd/protogen` produces. Where it stands:

## Header: the fleet skyline

```
╭──────────────────────────┤23:44:07├──────────────────────────┬──────────────────────┤✕ 2 down├─╮
│                          ▇ ▄ ▇ ▇ ▇ ▄ ▄ ▄     ▃       ▁   ▅   │ 11/13 hosts    44 gpus   44/44 read │
│ util   ▃ ▅ ▄ ▄ ▄ ▅ ▃ ▇   ▄ ▄ ▄ ▄ ▄ ▄ ▄ ▄   ▃ ▄ ▇ ▄ ▅ ▄ ▇ ▄   │  6 idle   28 ≥30%   18 ≥60%   11 ≥85% │
│        ─── rig-08 ────   ── dc-a ───────   ─── node-a ────   │ util  ■■■■■■░░░░░░░░ 49%            │
│ vram   ▁ ▅ ▁ ▅ ▂ ▅ ▂ ▄   ▀ ▀ ▀ ▀ ▀ ▀ ▀ ▀   ▀ ▁ ▀ ▁ ▀ ▀ ▀ ▀   │ vram  ■■■■■■■■░░░░░░ 63%   1.4T/2.2T│
│                          ▂ ▁ ▂ ▁ ▃ ▁ ▃ ▁   ▇   ▇             │ power ■■■■■■■░░░░░░░ 52%   11.6/22. │
╰──────────────────────────────────────────────────────────────┴─────────────────────────────────────╯
```

- Every GPU is a bar: utilisation rises above the axis, VRAM hangs below it
  (hanging bars use the bar colour as background under a lower block in the page
  colour, since text has no upper partial blocks). Host names sit on the axis.
  Each host's group is at least as wide as its name.
- Each host has its own stretch of axis, under its own bars: no line joins one
  host to the next or to the util / vram keys. One rule through the whole chart
  read as one long bar rather than as the ground each host stands on.
- A bar walks its metric's ramp along its height: a full cell is split in two
  (`▄` for a rising bar, `▀` for a hanging one) with the lower half's colour in
  the glyph and the upper's behind it, so two rows give four steps; a full bar
  crosses the ramp, a short one stays at its dark end.
- Hosts are three cells apart; the gap does the spacing a name needs, so a
  host is only as wide as its bars or its name. When no bar width fits that,
  hosts close up to one cell, before the bars lose the spaces between them.
  Faint dashed lines in the gaps were tried and dropped: the space reads
  cleaner.
- The `util` / `vram` keys sit one each side of the axis, the same three cells
  from the first host, in the same colours as the bars and the column names on
  the cards, so the header is also the colour key for the board. The header has no title of its
  own: the clock and the connection status are its only labels.
- An unknown utilisation is `·`, never the idle floor. A measured idle card keeps
  a one-eighth floor in the meter background colour.
- Hosts that do not fit in the chart's width end in `…`; below about 87 columns
  the chart is left out and the panel takes the box.
- The panel: hosts and GPUs and how many of them were read, the cards by
  utilisation band (idle under 5%, then ≥30%, ≥60%, ≥85%, each count in its
  band's colour on the util ramp; the bands nearest saturation are kept when
  the panel is narrow), and the fleet's util, vram and power meters (capped at
  14 cells like the board's). "busy" is gone: it was one of these bands under
  another name. Unknown readings are excluded, never 0.
  Power shows draw over cap only when every card reporting draw reports a cap;
  otherwise the total draw alone, with no percentage.
- The clock label is centred over the chart, clear of the divider's joint, and
  is left out on terminals too narrow for it to clear the other two labels.
- The panel's meters follow the rows' rhythm: label, meter, the percentage a
  cell after it, the reading two cells later.
- A fleet mean graph was tried and dropped: the mean barely moves and drew a flat
  band.

## Cards

```
╭─┤● rig-08├─────────────────────────────────────────────────────────────────┤8× RTX 4090 · 24.0G├─╮
│ 0   ■■░░░░░░░░░░░░ 17%    ■■■■■■░░░░░░░░ 43%     10.3G    37°C     97W   ⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀ │
│ 1   ■■■■░░░░░░░░░░ 31%    ■■░░░░░░░░░░░░ 18%      4.3G    44°C    151W   ⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣤⣠⣤⣤⣤⣤⣤⣤⣤⣤⣤ │
╰──── util ──────────────── vram ───────────────────────────────────────── util history ───────────╯
```

1. **One row per GPU, two short bars**: `idx  util■■■■ 42%  vram■■■■ 68%
   [used]  temp  power  graph`. Bars are capped at 14 cells (`maxBar`); past
   that a cell is already ~7% and the width goes to more lanes and longer graphs.
   Temperature, power and used VRAM carry their units.
2. **Two lines on a narrow lane**: under 70 columns of content (three lanes at
   200 columns) a GPU takes two lines, one per kind of fact. The utilisation
   line carries the compute readings, temperature and power side by side; the
   VRAM line carries the memory reading, used of total, in the VRAM colour like
   its bar. A column that alternated °C and W made the reader switch units on
   every line. The graph is two rows tall beside them.

   ```
   ╭─┤● rig-08├───────────────────────────────┤8× RTX 4090 · 24.0G├─╮
   │ 0   ■■░░░░░░░░░░░░ 17%     37°C     97W   ⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀ │
   │     ■■■■■■░░░░░░░░ 43%      10.3G/24.0G   ⣤⣤⣤⣤⣤⣄⣄⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀ │
   │ 1   ■■■■░░░░░░░░░░ 31%     44°C    151W   ⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀ │
   │     ■■■░░░░░░░░░░░ 18%       4.3G/24.0G   ⣀⣀⣀⣤⣤⣤⣤⣤⣤⣶⣴⣶⣶⣶⣶⣶⣶⣶⣶⣶ │
   ╰──── util · vram ───────────────────────── util history ────────╯
   ```

   Every other GPU sits on a faint band (8% toward the text colour, the card's
   whole inner width), so a card reads as pairs of lines rather than one run,
   and the graphs of neighbouring GPUs no longer touch.

3. **One rhythm**: a bar and its percentage are a cell apart; every other pair of
   neighbouring elements, the index and its bar included, is one gap apart:
   three cells, or two when a narrow lane would otherwise lose a field
   (`planRow`). Readings are right-aligned in fixed columns; the graph takes the
   rest of the row, so the row ends at the margin it starts at.
4. **Titles sit on the top border between two strokes**, `─┤● rig-08├─`, with
   no space inside and no rows of their own. The host label is at the left, the
   GPU summary at the right; the right one is shortened, then dropped, before
   the left one is.
5. **Column names on the bottom border**, plain text on the line with no strokes
   leaving it, placed under the columns they name, each in its metric's colour.
   The graph is named `util history`: it is that card's own utilisation, newest
   at the right; "last 56s" named neither the measurement nor the direction of
   time. A stale sample's age claims the right end and any name it would cut is
   left out.
6. **Colour by meaning**: each metric has its own ramp, and a reading, its bar
   and its column name are drawn in it: util on the cpu ramp, vram on the used
   ramp, power on the cached ramp; on a host's own line, its cpu on the process
   ramp and its memory on the download ramp, never a device meter's. A bar's
   cells take the colour of the reading each stands for, and its percentage the
   colour of its value, the % sign dimmer. Units after numbers (°C, W, G) are in
   the secondary text colour. The GPU model in a card's title is in its product
   line's colour, so a mixed board says which cards are which: every GeForce
   (4090, 5090, …) shares one, the A-, H- and B-series data-centre lines and the
   L-series each have their own. A healthy box's frame is a quiet grey; only a
   host that is stale, connecting or down has a coloured frame.
7. **A failed host leads with the reason**: its card's line is the reason in
   the alert colour (`connection timed out`), then the rest of the message a gap
   later in the secondary colour. One red line made the two words that say what
   happened no louder than the address.
8. **Spacing**: a column between lanes and a row between cards, whether they
   are in different bands or stacked in one lane. Touching boxes put each card's
   column names right against the next card's title.

## Host view

Each device box opens with its claim line: the card's lamp, then the holding
thread's id (the first eight characters of the conversation, in its runtime's
brand colour), why it was claimed, and `since 14:02` at the right end, with
`overdue` before it once the claim's time has run out. A card nobody holds says
`free` when nothing runs on it and `unclaimed`, in the warning colour, when
something does. The runtime's name is not on the line: the id is what a reader
looks up, and on a fleet run by one kind of agent the name said the same thing
on every card.

Under it are the three gauges, each with the readings about the same thing:
the compute clock and the link beside utilisation, used of total and the memory
clock beside VRAM, the watts and the heat they make (`41°C fan 41%`) beside
power. Then a dashed rule, a header row (`Pid  User  VRAM  Command`) and two
process rows, always two, so every box is the same height and an idle card
lines up with a busy one beside it; the top two by memory are listed and the
rest counted in the bottom border (`+4 more`). The rule ends the gauges: without
it the header read as a fourth gauge with no bar. The column names were in the
bottom border for a while, the way the board names its own, but there they sat
two empty rows below a single process and read as the box's footer.

The device's history runs down the right side from the gauges to the box's
bottom, so the graph's floor is the box's floor. The reading columns are as wide
as the widest value of each field the host's devices report, and a field none of
them reports takes no column at all (no `— MEM` in every box); the graph starts a
gap after the last reading, but never before a command has 28 cells to be read
in, and the command column stops a gap short of it.

Graphs colour by height: each cell takes the ramp's colour for how many dot rows
it reaches above the graph's floor, so a tall spike walks the ramp to its bright
end and an idle floor stays at the cold end. The reading does not tint the graph
— two cells at the same height are the same colour whatever their columns are
doing. A cell has one colour, so a braille glyph's four dot rows cannot differ;
that is a limit of the grid, not of the rule.

## Lanes and packing

About 80 columns per lane (`laneTarget`); more lanes only when the board does not
fit (no lane narrower than 60), scrolling only after that. Every lane uses the
narrowest lane's row plan, so columns line up across lanes.

Cards are packed in **bands**: each band is as tall as the first unplaced card,
and every lane of the band is filled, in order, with the next cards that still
fit, a row apart. A card is its GPU count plus its two borders, so an 8-GPU card
is 10 rows and a 4-GPU (6) and a 1-GPU (3) card stack beside it exactly; with
two lines per GPU the same cards are 18, 10 and 4 rows. The real fleet (2×8,
4×4, 3×2, 1×1 GPUs) in three lanes, one line per GPU:

```
┌ rig-09 (10) ┐┌ rig-08 (10) ┐┌ box-01 (6) ┐
│             ││             │└────────────┘
│             ││             │┌ worker-01 ─┐
└─────────────┘└─────────────┘└────────────┘
┌ box-02 (6) ─┐┌ box-08 (6) ─┐┌ box-03 (6) ┐
┌ box-04 (4) ─┐┌ box-07 (4) ─┐┌ dc-b (4) ──┐
```

A "shortest lane next" waterfall staggered every row of such fleets. Gaps are
filled only with hosts in the same state: a failed host is never used to plug a
hole between busy ones.

## Order

The sort key is only state (online / connecting / failed), GPU count and name
(natural order). Load is not in it, so a card moves only when a host comes or
goes; the selection is tracked by host name. Every header slot has a fixed width
and the header a fixed height, so readings cannot move the board.

## Why not finer than a row

A terminal addresses whole cells; every TUI framework (Bubble Tea, Ratatui,
Textual, Ink) only writes a character and two colours into each. A shape exists
only if some character has it. Sub-cell effects come from characters: ½-cell
blocks (`▀▄▌▐`), ⅛-cell edge lines (`▁▔▏▕`), braille (2×4 dots). Animation is
the same characters redrawn quickly; real pixels need a graphics protocol
(Kitty, Sixel, iTerm2), and no protocol is supported everywhere (macOS
Terminal supports none, Ghostty only Kitty's).

So a title frame that is tight around its text *and* rounded cannot be drawn
with characters: `╭╮╰╯` join lines through the middle of a cell, and lines
against the text run on cell edges, which have no rounded corner. Tried and
dropped: tight square frames of edge lines (`▁▔▎`), titles on hairline frames,
filled chips, braille-dot frames (dotted, and they cannot join the border), and
a Kitty-graphics mock-up (Ghostty only). The title is the text between two
strokes on the border, which needs no row of its own.

## Verification

`go run ./cmd/protogen` renders the real `internal/ui` with `internal/demo`:

- `out/board.html`: the cell canvas converted straight to HTML.
- `out/term.html`: the same ANSI that `View()` returns, replayed in xterm.js.
  Judge glyph widths here: a browser `<pre>` falls back to proportional fonts for
  braille and `■`, and right borders go ragged.

`ftop -demo` runs the same synthetic fleet in a real terminal.

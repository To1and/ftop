// Package ui is the Bubble Tea application: fleet state, layout, and key handling.
package ui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"ftop/internal/sshx"
	"ftop/internal/theme"
	"ftop/internal/wire"
)

// hostState is the mutable per-host state the UI keeps.
//
// Bounded history rather than a growing slice: the graph only ever shows the last
// `graphWidth` samples, so keeping more would be memory the render cannot use.
type hostState struct {
	name    string
	label   string
	sample  wire.Sample
	hasData bool
	err     string
	history []float64
	// gpuHist is each device's own utilisation history, keyed by GPU index. The
	// inline graph on a GPU row plots that card, not the host average: eight rows
	// repeating one host-wide curve would draw the same line eight times.
	gpuHist map[string][]float64
	// cpuHist and memHist are the host's own two readings, on the same one-second
	// cadence and under the same cap. They are the series the board's host row and
	// the host view's three graphs draw; history (above) is the fleet's GPU mean.
	cpuHist, memHist, swapHist []float64
	// hostProcs is the host's busiest processes from the last process walk, and
	// hostProcsStale says the walk did not run this round.
	hostProcs      []wire.HostProc
	hostProcsStale bool
	// inv is every device this host has reported since it last came online, in
	// display order. A sample that omits one (a driver hiccup, a card that stopped
	// answering) still draws it, as an unknown row: rebuilding the card from each
	// sample changed its height and its sort key, and the board repacked
	// mid-session.
	inv []invSlot
	// missing holds the inventory keys the latest sample omitted.
	missing map[string]bool
	age     time.Time
	// noGPU says the host's last sample reported no NVIDIA device: a CPU-only
	// machine. It outlives a disconnect, so a CPU-only host that drops off stays
	// one rather than coming back onto the board as a failed GPU host.
	noGPU bool
	// unsupported says the probe cannot run on this host's platform (macOS,
	// Windows, anything that is not Linux x86_64).
	unsupported bool
}

// invSlot is one device in a host's inventory.
type invSlot struct {
	key   string // gpuKey: the UUID when the driver gives one
	first int    // the index it was first seen at, which fixes its place
	last  wire.Gpu
}

// gpuKey identifies a device across samples. The UUID survives a renumbering;
// the index is the fallback for drivers that report none.
//
// It is scoped to its host and is only safe for comparisons within one host's
// inventory: two machines that both fall back to the index produce the same key
// ("#0"), which is correct for "this host's card 0" and wrong as a board-wide
// identity. Use ledgerKey for anything keyed across the fleet.
func gpuKey(g wire.Gpu) string {
	if g.UUID != "" {
		return g.UUID
	}
	return fmt.Sprintf("#%d", g.Index)
}

// ledgerKey is the key a claim is looked up by: the card's identity as the ledger
// stores it, which is the UUID, and a host-qualified index for a driver that
// reports none.
//
// The host prefix is not decoration. The ledger is keyed by UUID alone, so a card
// without one cannot have a claim in it at all; but the board still looks up every
// card, and an unqualified "#0" made every host's first GPU answer to the same
// entry. That is invisible on real hardware — every driver that supports claims
// reports UUIDs — and it is exactly the kind of thing that stops being invisible
// on the one fleet whose driver does not.
func ledgerKey(host string, g wire.Gpu) string {
	if g.UUID != "" {
		return g.UUID
	}
	return host + "/" + gpuKey(g)
}

// Minimum usable terminal. Below this a card has no interior left.
const (
	minWidth  = 40
	minHeight = 12
)

// App is the root Bubble Tea model.
type App struct {
	order  []string
	states map[string]*hostState
	mu     sync.Mutex

	width, height int
	skin          *theme.Skin
	detail        bool
	cursor        int
	// focusName is the host under the highlight. It is a name rather than an index
	// so that a host coming online, which re-sorts the board, does not silently
	// move the highlight onto a different machine.
	focusName string
	help      bool
	// graphRows is kept from the flag for compatibility: 0 turns the inline graph
	// off, anything else turns it on. Rows are one line per GPU now, so the graph
	// is always a single braille row.
	graphRows int
	interval  time.Duration
	// boardScroll is the overview's vertical offset when the board is taller than
	// the terminal; follow asks the next frame to bring the focused card into view.
	boardScroll int
	follow      bool
	// placed is the last frame's card geometry, which arrow keys and clicks
	// navigate; boardTop and boardView are where the board's visible window sat
	// on screen, to map a click back into board coordinates.
	placed    []placed
	lanes     int
	boardTop  int
	boardView int

	// holders is the ledger's answer for each card, keyed by GPU UUID. It is the
	// only place the board learns that a card is claimed: a claim is a record, not a
	// measurement, and the two can disagree.
	holders   map[string]Holder
	ledgerAt  time.Time
	ledgerErr string
	// onSkill installs the agent manual and returns the line to show. Nil in builds
	// and tests with no installer wired.
	onSkill   func() string
	skillNote string
	// detailView is the detail view's visible body height in the last frame.
	detailView int
	// hits are the last frame's clickable regions other than cards: the status
	// line's key hints, the scroll hint.
	hits []hit
	// mouse turns mouse reporting on. It is a choice because a terminal that
	// reports the mouse no longer selects text with a plain drag.
	mouse bool
	// detailScroll is how many lines the detail view is scrolled down. Reset
	// whenever the cursor moves to another host, so a scroll position from one
	// machine's process list does not hide another's.
	detailScroll int
	// procView is the full host process table, opened over either the board or a
	// host view; procScroll is its shared scroll offset.
	procView   bool
	procScroll int
	// maxCols caps cards per row from config; 0 means fit the terminal.
	maxCols int
	// frame and body are reused across renders. Allocating a fresh cell grid per
	// frame was the single largest source of garbage: Bubble Tea calls View on
	// every message, and a 200x58 grid is ~835 KB.
	frame   *Canvas
	body    *Canvas
	started time.Time
	// anyHeld says whether the board has a lit lamp anywhere, which is the only
	// reason to repaint faster than once a second. It is an atomic rather than a
	// field read under a.mu because tickPeriod runs on the program's own goroutine
	// and must not be able to block on the render lock.
	anyHeld atomic.Bool
	// now is the clock the render reads for anything time-varying, and the only
	// one it may read: a draw that called time.Now() itself would make -once and
	// PlainFrame non-deterministic, and would make a lamp's breath jump whenever a
	// frame was dropped. Updated on every tick and every message.
	now time.Time

	// showCPUOnly and showUnsupported put back the hosts the board leaves out by
	// default: machines with no NVIDIA GPU, and machines the probe cannot run on.
	// A board for finding a free card has nothing to say about either, and on a
	// fleet whose ssh config also lists laptops and VPSes they were a third of
	// the skyline. They are still monitored; this is only what is drawn.
	showCPUOnly, showUnsupported bool
	// settings is the settings panel, open over the board; setRow is its
	// highlighted row.
	settings bool
	setRow   int
}

// Options configures the app.
type Options struct {
	Hosts     []string
	GraphRows int
	// MaxCols caps cards per row (0 = decide from the terminal width).
	MaxCols int
	// Interval is the sampling period, used to label the graph's time span.
	Interval time.Duration
	// Labels maps host names to display names; a missing entry falls back to a
	// shortened host name.
	Labels map[string]string
	// NoMouse leaves mouse reporting off, so a drag selects text as usual.
	NoMouse bool
	// SkillInstall, when set, is what the s key runs: install the agent manual and
	// return the status line to show. Nil disables the key.
	SkillInstall func() string
	// SkillStatus is the footer's one-line manual state, refreshed when the board is
	// built. Nil leaves the line empty.
	SkillStatus func() string
	// ShowCPUOnly and ShowUnsupported start the board with those hosts shown.
	ShowCPUOnly, ShowUnsupported bool
}

// New builds the app.
func New(opts Options) *App {
	rows := opts.GraphRows
	// 0 is a real choice (no graph, one line per GPU), not an unset value.
	if rows < 0 {
		rows = 0
	}
	if rows > 6 {
		rows = 6
	}
	a := &App{
		states:    make(map[string]*hostState, len(opts.Hosts)),
		skin:      theme.NewSkin(),
		graphRows: rows,
		maxCols:   opts.MaxCols,
		interval:  opts.Interval,
		started:   time.Now(),
		mouse:     !opts.NoMouse,
		onSkill:   opts.SkillInstall,

		showCPUOnly:     opts.ShowCPUOnly,
		showUnsupported: opts.ShowUnsupported,
	}
	if opts.SkillStatus != nil {
		a.skillNote = opts.SkillStatus()
	}
	for _, h := range opts.Hosts {
		label := opts.Labels[h]
		if label == "" {
			label = theme.ShortLabel(h)
		}
		a.states[h] = &hostState{name: h, label: label}
		a.order = append(a.order, h)
	}
	return a
}

// Init starts the clock.
func (a *App) Init() tea.Cmd {
	return tea.Batch(tea.Tick(a.tickPeriod(), func(t time.Time) tea.Msg { return tickMsg(t) }))
}

// lampFPS is the lamp's frame rate while any card is held.
//
// 8 is chosen from what the eye needs to read a slow breath rather than from what
// the renderer can afford: a full frame costs ~0.9 ms, so 8 Hz is under 1% of a
// core, and Bubble Tea's renderer diffs the screen buffer, so the terminal only
// receives the lamp's cells changing. A higher rate would spend a multiple of that
// to say the same thing.
const lampFPS = 8

// tickPeriod is how often to repaint: the lamp's period while something is held,
// one second otherwise.
//
// The answer comes from an atomic flag set where the holders are, not from taking
// a.mu. This runs on the Bubble Tea goroutine between frames, and the one thing it
// must never do is block on the render lock: a lock-free read here means the frame
// rate cannot deadlock against a draw, and the cost of being at most one message
// stale is a single repaint at the old period.
func (a *App) tickPeriod() time.Duration {
	if a.anyHeld.Load() {
		return time.Second / lampFPS
	}
	return time.Second
}

// clock returns the moment the current frame is drawn at.
//
// A zero a.now means no message has been delivered yet, which is the -once and
// demo path: those print a snapshot rather than run a program, and a snapshot must
// not depend on when the process happened to start. So they get a fixed phase —
// the bright end of the breath — and two runs of `ftop -once` on two machines
// produce the same bytes.
func (a *App) clock() time.Time {
	if a.now.IsZero() {
		return time.Unix(0, 0).Add(lampBreath / 2)
	}
	return a.now
}

type tickMsg time.Time

// Update handles every message.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Every message is a repaint, so every message advances the render clock. This
	// is the only place the frame's notion of "now" is set: draw code reads it and
	// never the real clock, which is what keeps the lamp's breath a function of the
	// frame's timestamp rather than of how fast the messages happened to arrive.
	if t, ok := msg.(tickMsg); ok {
		a.now = time.Time(t)
	} else {
		a.now = time.Now()
	}
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = m.Width, m.Height
		return a, nil

	case tea.KeyPressMsg:
		return a.press(m.String())

	case tea.MouseClickMsg:
		return a.click(m.Mouse())

	case tea.MouseWheelMsg:
		a.wheel(m.Mouse())
		return a, nil

	case tickMsg:
		// A repaint even with no new data keeps the freshness glyph, the "N
		// seconds ago" reading and the holders' lamp honest. The period is the
		// lamp's: with somebody holding a card the board redraws at the lamp's
		// frame rate, and with nothing held it falls back to one second, because
		// there is then nothing to animate.
		return a, tea.Tick(a.tickPeriod(), func(t time.Time) tea.Msg { return tickMsg(t) })

	case sshx.SampleMsg:
		a.applySample(m)
		return a, nil

	case sshx.ErrMsg:
		a.applyErr(m)
		return a, nil

	case LedgerMsg:
		a.applyLedger(m)
		return a, nil
	}
	return a, nil
}

// applySample folds one sample into state.
func (a *App) applySample(m sshx.SampleMsg) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.states[m.Host]
	if !ok {
		return
	}
	st.sample = st.mergeInventory(m.Sample)
	st.hasData = true
	st.err = ""
	st.noGPU = len(st.sample.Gpus) == 0
	st.unsupported = false
	st.age = time.Now()
	// No readable card is a gap in the host's history, not a 0% second.
	avg := math.NaN()
	if m.Sample.KnownGPUs() > 0 {
		avg = m.Sample.UtilAvg()
	}
	st.history = appendCapped(st.history, avg)
	// The host's own readings are recorded as gaps when unknown, for the same
	// reason: a missing sample is not 0%.
	cpu := math.NaN()
	if v := m.Sample.CPUPctValue(); v >= 0 {
		cpu = v
	}
	st.cpuHist = appendCapped(st.cpuHist, cpu)
	// -1 means "not reported", and a graph would draw it on the floor as a
	// measured zero; a gap is the honest series.
	memPct, swapPct := m.Sample.MemPct(), m.Sample.SwapPct()
	if memPct < 0 {
		memPct = math.NaN()
	}
	if swapPct < 0 {
		swapPct = math.NaN()
	}
	st.memHist = appendCapped(st.memHist, memPct)
	st.swapHist = appendCapped(st.swapHist, swapPct)
	st.hostProcs, st.hostProcsStale = m.Sample.HostProcs, m.Sample.HostProcsStale
	if st.gpuHist == nil {
		st.gpuHist = make(map[string][]float64, len(st.sample.Gpus))
	}
	for _, g := range st.sample.Gpus {
		// An unreadable sample is recorded as a gap. Recording 0 would draw an idle
		// moment that never happened; skipping it froze the graph, so the last
		// good reading kept sitting at "now" while time moved on.
		u := math.NaN()
		if v := g.UtilPct(); v >= 0 {
			u = v
		}
		k := gpuKey(g)
		st.gpuHist[k] = appendCapped(st.gpuHist[k], u)
	}
}

// applyErr marks a host failed. Its device inventory ends with the session: a
// host that comes back may come back with different cards.
func (a *App) applyErr(m sshx.ErrMsg) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if st, ok := a.states[m.Host]; ok {
		st.err = m.Err
		st.inv, st.missing = nil, nil
		st.unsupported = m.Unsupported
	}
}

// mergeInventory folds a sample into the host's device inventory and returns it
// with one entry per known device, in inventory order.
//
// Devices are matched by gpuKey, so a renumbered card keeps its row. A device the
// sample omitted keeps its identity and its last process list, marked old, and
// loses every reading. A device seen for the first time mid-session is inserted
// at its index: that is a real change in what the host has, and the card grows.
func (st *hostState) mergeInventory(s wire.Sample) wire.Sample {
	present := make(map[string]wire.Gpu, len(s.Gpus))
	for _, g := range s.Gpus {
		k := gpuKey(g)
		present[k] = g
		found := false
		for i := range st.inv {
			if st.inv[i].key == k {
				st.inv[i].last = g
				found = true
				break
			}
		}
		if !found {
			st.inv = append(st.inv, invSlot{key: k, first: g.Index, last: g})
		}
	}
	sort.SliceStable(st.inv, func(i, j int) bool { return st.inv[i].first < st.inv[j].first })

	out := s
	out.Gpus = make([]wire.Gpu, len(st.inv))
	st.missing = nil
	for i, sl := range st.inv {
		if g, ok := present[sl.key]; ok {
			out.Gpus[i] = g
			continue
		}
		if st.missing == nil {
			st.missing = map[string]bool{}
		}
		st.missing[sl.key] = true
		out.Gpus[i] = wire.Gpu{
			Index: sl.last.Index, UUID: sl.last.UUID, Name: sl.last.Name,
			Procs: sl.last.Procs, ProcsStale: true,
		}
	}
	return out
}

// historyCap bounds every history series: ten minutes at 1 Hz, which covers the
// widest graph any view draws (two samples per braille cell) on terminals up to
// ~600 columns.
const historyCap = 600

func appendCapped(h []float64, v float64) []float64 {
	h = append(h, v)
	if len(h) > historyCap {
		h = h[len(h)-historyCap:]
	}
	return h
}

// press acts on one key, by its Bubble Tea name. Clicking a status-line hint
// comes here too, so a hint does exactly what its key does.
func (a *App) press(key string) (tea.Model, tea.Cmd) {
	if a.settings && key != "q" && key != "ctrl+c" {
		a.settingsKey(key)
		return a, nil
	}
	switch key {
	case "q", "ctrl+c":
		return a, tea.Quit
	case "o":
		// The settings panel opens over whichever view is showing and closes
		// the same way it opens.
		a.settings, a.help = true, false
		return a, nil
	case "esc":
		if a.procView {
			a.procView = false
			return a, nil
		}
		a.detail = false
		a.help = false
		return a, nil
	case "?":
		a.help = !a.help
		return a, nil
	case "p":
		// The full process table, from the board (the focused host) or from a
		// host view. It closes the same way it opens.
		if a.procView {
			a.procView = false
		} else if a.detail || a.focused() != "" {
			a.procView = true
			a.procScroll = 0
		}
		return a, nil
	case "c":
		if a.graphRows == 0 {
			a.graphRows = 1
		} else {
			a.graphRows = 0
		}
		return a, nil
	case "s":
		// Install or refresh the agent manual, and say what happened on the status
		// line. It is a write to the user's home, so it is a key press and not
		// something the board does on its own.
		if a.onSkill != nil {
			a.skillNote = a.onSkill()
		}
		return a, nil
	}
	if a.detail {
		a.detailKey(key)
	} else {
		a.boardKey(key)
	}
	return a, nil
}

// boardKey moves the highlight over the cards as they are drawn: up and down stay
// in a column, left and right cross to the neighbouring one.
func (a *App) boardKey(key string) {
	switch key {
	case "enter":
		if name := a.focused(); name != "" {
			a.open(name)
		}
	case "down", "j":
		a.moveFocus(0, 1)
	case "up", "k":
		a.moveFocus(0, -1)
	case "right", "l":
		a.moveFocus(1, 0)
	case "left", "h":
		a.moveFocus(-1, 0)
	case "tab":
		a.stepFocus(1)
	case "shift+tab":
		a.stepFocus(-1)
	case "pgdown":
		a.boardScroll += page(a.boardView)
	case "pgup":
		a.boardScroll = max(a.boardScroll-page(a.boardView), 0)
	case "home", "g":
		a.boardScroll = 0
	}
}

// page is how far a page key scrolls a window of view rows: exactly one window,
// so no row is skipped. A guess from the terminal height went stale every time
// the header or the box chrome changed height.
func page(view int) int { return max(view, 1) }

// open shows a host's detail view. Opening a host is choosing it: the highlight
// is pinned there, so returning to the board finds it where it was left even if
// hosts arrived since.
func (a *App) open(name string) {
	a.focusName = name
	a.cursor = a.indexOf(name)
	a.detail = true
	a.detailScroll = 0
}

// hit is a clickable region of the last frame and the key it stands for.
type hit struct {
	x, y, w int
	key     string
	host    string // a host chip: a click selects that host, again opens it
}

// click handles a left click. On the board, the first click on a card selects
// it and a click on the selected card opens it, so a double click opens any
// card. A click on a key hint presses that key; any click closes the help.
func (a *App) click(m tea.Mouse) (tea.Model, tea.Cmd) {
	if m.Button != tea.MouseLeft {
		return a, nil
	}
	if a.help {
		a.help = false
		return a, nil
	}
	if a.settings {
		// A click on a row flips it; anywhere else closes the panel, the way a
		// click closes the help.
		for _, h := range a.hits {
			if m.Y == h.y && m.X >= h.x && m.X < h.x+h.w {
				if i, ok := settingClicked(h.key); ok {
					a.flipSetting(i)
					return a, nil
				}
			}
		}
		a.settings = false
		return a, nil
	}
	for _, h := range a.hits {
		if m.Y != h.y || m.X < h.x || m.X >= h.x+h.w {
			continue
		}
		if h.host != "" {
			a.choose(h.host)
			return a, nil
		}
		return a.press(h.key)
	}
	if a.detail {
		return a, nil
	}
	if p := a.cardAt(m.X, m.Y); p != nil {
		a.choose(p.host.name)
	}
	return a, nil
}

// choose is a click on a host: the first selects it and brings its card into
// view, a second on the selected host opens it.
func (a *App) choose(name string) {
	if name == a.focused() {
		a.open(name)
		return
	}
	a.focusName = name
	a.follow = true
}

// cardAt is the card drawn at screen cell (x, y) in the last frame.
func (a *App) cardAt(x, y int) *placed {
	if y < a.boardTop || y >= a.boardTop+a.boardView {
		return nil
	}
	by := y - a.boardTop + a.boardScroll
	for i := range a.placed {
		p := &a.placed[i]
		if x >= p.x && x < p.x+p.w && by >= p.y && by < p.y+p.h {
			return p
		}
	}
	return nil
}

// wheelStep is how many lines one notch of the wheel scrolls.
const wheelStep = 3

// wheel scrolls whichever view is showing.
func (a *App) wheel(m tea.Mouse) {
	d := 0
	switch m.Button {
	case tea.MouseWheelDown:
		d = wheelStep
	case tea.MouseWheelUp:
		d = -wheelStep
	default:
		return
	}
	switch {
	case a.help:
	case a.detail:
		a.detailScroll = max(a.detailScroll+d, 0)
	default:
		// Clamped against the board's height when the next frame is laid out.
		a.boardScroll = max(a.boardScroll+d, 0)
	}
}

func (a *App) detailKey(key string) {
	if a.procView {
		switch key {
		case "down", "j":
			a.procScroll++
		case "up", "k":
			a.procScroll = max(a.procScroll-1, 0)
		case "pgdown":
			a.procScroll += page(a.height - 4)
		case "pgup":
			a.procScroll = max(a.procScroll-page(a.height-4), 0)
		case "home", "g":
			a.procScroll = 0
		}
		return
	}
	switch key {
	case "enter", "j", "right", "l":
		a.moveCursor(1)
	case "k", "left", "h":
		a.moveCursor(-1)
	case "down":
		a.detailScroll++
	case "up":
		a.detailScroll = max(a.detailScroll-1, 0)
	case "pgdown":
		a.detailScroll += page(a.detailView)
	case "pgup":
		a.detailScroll = max(a.detailScroll-page(a.detailView), 0)
	case "home":
		a.detailScroll = 0
	case "g":
		a.cursor = a.indexOf(a.sortedNames()[0])
		a.detailScroll = 0
	case "G":
		names := a.sortedNames()
		a.cursor = a.indexOf(names[len(names)-1])
		a.detailScroll = 0
	}
}

// indexOf maps a host name to its position in a.order. It returns 0 when the name
// is unknown, which keeps the detail view on a real machine.
func (a *App) indexOf(name string) int {
	for i, n := range a.order {
		if n == name {
			return i
		}
	}
	return 0
}

// focused is the highlighted host, defaulting to the first card drawn.
//
// While hosts are still connecting the default follows the top-left card, which
// is the only sensible "home" while the board fills in. Once every host has
// answered or failed, the default is pinned to the host it is on, so a later
// re-sort (a host dropping off) cannot slide the highlight onto another machine.
func (a *App) focused() string {
	if st, ok := a.states[a.focusName]; ok && !a.hidden(st) {
		return a.focusName
	}
	names := a.sortedNames()
	if len(names) == 0 {
		return ""
	}
	if a.settled() {
		a.focusName = names[0]
	}
	return names[0]
}

// settled reports that no host is still waiting for its first answer.
func (a *App) settled() bool {
	for _, st := range a.states {
		if st.rank() == 1 {
			return false
		}
	}
	return true
}

// moveFocus walks the highlight spatially over the last frame's cards.
//
// Vertical moves stay in the same column; horizontal ones pick the card in the
// next column that overlaps the current one's rows most, so → from the bottom of
// a tall card lands beside it rather than back at the top of the board.
func (a *App) moveFocus(dx, dy int) {
	cur := a.focused()
	var from *placed
	for i := range a.placed {
		if a.placed[i].host.name == cur {
			from = &a.placed[i]
		}
	}
	if from == nil {
		a.stepFocus(dx + dy)
		return
	}
	var best *placed
	bestScore := 0
	for i := range a.placed {
		p := &a.placed[i]
		if p == from {
			continue
		}
		var score int
		switch {
		case dy > 0 && p.lane == from.lane && p.y > from.y:
			score = -(p.y - from.y)
		case dy < 0 && p.lane == from.lane && p.y < from.y:
			score = -(from.y - p.y)
		case dx != 0 && p.lane == from.lane+dx:
			// Prefer overlap; break ties toward the card nearest the top.
			overlap := min(p.y+p.h, from.y+from.h) - max(p.y, from.y)
			score = overlap*1000 - abs(p.y-from.y)
		default:
			continue
		}
		if best == nil || score > bestScore {
			best, bestScore = p, score
		}
	}
	if best != nil {
		a.focusName = best.host.name
		a.follow = true
	}
}

// stepFocus moves the highlight through the drawing order.
func (a *App) stepFocus(d int) {
	names := a.sortedNames()
	if len(names) == 0 {
		return
	}
	i := 0
	for j, n := range names {
		if n == a.focused() {
			i = j
		}
	}
	i = min(max(i+d, 0), len(names)-1)
	a.focusName = names[i]
	a.follow = true
}

// moveCursor steps the detail view through the board's order, so j/k visit hosts
// in the sequence they appear on the overview.
func (a *App) moveCursor(d int) {
	names := a.sortedNames()
	if len(names) == 0 {
		return
	}
	cur := a.order[min(max(a.cursor, 0), len(a.order)-1)]
	i := 0
	for j, n := range names {
		if n == cur {
			i = j
		}
	}
	i = min(max(i+d, 0), len(names)-1)
	a.cursor = a.indexOf(names[i])
	a.detailScroll = 0
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// View renders the frame.
func (a *App) View() tea.View {
	var v tea.View
	if a.width == 0 || a.height == 0 {
		v.Content = "ftop: waiting for terminal size…"
		return v
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// A terminal this small cannot hold a card; drawing anyway panics inside the graph
	// and meter code on a zero-width slice. Say so instead of crashing.
	if a.width < minWidth || a.height < minHeight {
		v.Content = tooSmall(a.width, a.height)
		v.AltScreen = true
		return v
	}
	if a.mouse {
		// Cell motion reports clicks and the wheel, and motion only while a button
		// is held: the board never needs to track a hovering pointer.
		v.MouseMode = tea.MouseModeCellMotion
	}
	if a.settings {
		v.Content = a.settingsCanvas().ANSI()
	} else if a.help {
		v.Content = a.helpCanvas().ANSI()
	} else if a.procView {
		// A process table is a host view with its own body, so it is drawn on the
		// same path: the board-level key opens it, and esc closes it back to
		// whichever view opened it — the cursor is already on the right host.
		v.Content = a.detailCanvas().ANSI()
	} else if a.detail {
		v.Content = a.detailCanvas().ANSI()
	} else {
		v.Content = a.fleetCanvas().ANSI()
	}
	v.AltScreen = true
	return v
}

// tooSmall is the message for a terminal below the minimum, itself cut to the
// terminal: a line wider than the screen wraps, and the wrapped rows scroll.
func tooSmall(w, h int) string {
	lines := []string{
		"ftop: terminal too small",
		fmt.Sprintf("%d×%d, need %d×%d", w, h, minWidth, minHeight),
		"q to quit",
	}
	lines = lines[:min(len(lines), max(h, 1))]
	for i, l := range lines {
		lines[i] = theme.Clip(l, w)
	}
	return strings.Join(lines, "\n")
}

// hidden reports whether the board leaves this host out under the current
// settings. A host that has not answered yet is never hidden: until it does,
// nothing says it is not a GPU machine.
func (a *App) hidden(st *hostState) bool {
	switch {
	case st.unsupported:
		return !a.showUnsupported
	case st.noGPU:
		return !a.showCPUOnly
	}
	return false
}

// filtered counts the hosts each filter covers, shown or not: what the settings
// panel says a row is about.
func (a *App) filtered() (cpuOnly, unsupported int) {
	for _, st := range a.states {
		switch {
		case st.unsupported:
			unsupported++
		case st.noGPU:
			cpuOnly++
		}
	}
	return cpuOnly, unsupported
}

// hiddenCount is how many hosts the board is leaving out right now.
func (a *App) hiddenCount() int {
	n := 0
	for _, st := range a.states {
		if a.hidden(st) {
			n++
		}
	}
	return n
}

// sortedHosts orders the fleet the way the board draws it, leaving out the
// hosts the settings hide.
//
// The key uses only facts that change when a machine comes online or drops off:
// reachability, GPU count, name. Sorting by utilisation made the board reshuffle
// every second as load moved between hosts, so a machine was never where you last
// saw it. Online hosts come first with the largest machines leading, which is
// also what lets equal-sized cards line up in rows.
func (a *App) sortedHosts() []*hostState {
	out := make([]*hostState, 0, len(a.order))
	for _, n := range a.order {
		if st, ok := a.states[n]; ok && !a.hidden(st) {
			out = append(out, st)
		}
	}
	// Insertion sort: a dozen hosts, and the order is almost always unchanged.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && hostLess(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (a *App) sortedNames() []string {
	hs := a.sortedHosts()
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.name
	}
	return out
}

// rank groups hosts: answering, still connecting, failed.
func (h *hostState) rank() int {
	switch {
	case h.err != "":
		return 2
	case !h.hasData:
		return 1
	}
	return 0
}

func hostLess(a, b *hostState) bool {
	if ra, rb := a.rank(), b.rank(); ra != rb {
		return ra < rb
	}
	if ga, gb := len(a.sample.Gpus), len(b.sample.Gpus); a.rank() == 0 && ga != gb {
		return ga > gb
	}
	if a.label != b.label {
		return naturalLess(a.label, b.label)
	}
	return a.name < b.name
}

// naturalLess compares digit runs by value, so box-10 sorts after box-9.
func naturalLess(a, b string) bool {
	ar, br := []rune(a), []rune(b)
	i, j := 0, 0
	for i < len(ar) && j < len(br) {
		if unicode.IsDigit(ar[i]) && unicode.IsDigit(br[j]) {
			si, sj := i, j
			for i < len(ar) && unicode.IsDigit(ar[i]) {
				i++
			}
			for j < len(br) && unicode.IsDigit(br[j]) {
				j++
			}
			na, nb := trimZeros(ar[si:i]), trimZeros(br[sj:j])
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if sa, sb := string(na), string(nb); sa != sb {
				return sa < sb
			}
			continue
		}
		if ar[i] != br[j] {
			return ar[i] < br[j]
		}
		i++
		j++
	}
	return len(ar)-i < len(br)-j
}

func trimZeros(r []rune) []rune {
	for len(r) > 1 && r[0] == '0' {
		r = r[1:]
	}
	return r
}

func (h *hostState) fresh() time.Duration { return time.Since(h.age) }

// Deliver folds a message into state without going through the Elm loop.
//
// Startup reads before Bubble Tea exists: the first samples arrive while the
// program is still being constructed, and dropping them just to honour the update
// discipline would make every launch start blank.
func (a *App) Deliver(msg tea.Msg) {
	switch m := msg.(type) {
	case sshx.SampleMsg:
		a.applySample(m)
	case sshx.ErrMsg:
		a.applyErr(m)
	case LedgerMsg:
		// -once and the demo both deliver before the program exists, so this path
		// must know every message Update does. A message that only Update handles
		// works interactively and silently does nothing in a snapshot.
		a.applyLedger(m)
	}
}

// Ready reports whether every named host has produced a sample or failed.
func (a *App) Ready(hosts []string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, h := range hosts {
		st, ok := a.states[h]
		if !ok {
			continue
		}
		if !st.hasData && st.err == "" {
			return false
		}
	}
	return true
}

// PlainFrame renders one frame without ANSI, for -once and for tests.
func (a *App) PlainFrame() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.width == 0 {
		a.width, a.height = 200, 58
	}
	return a.fleetCanvas().Plain()
}

// canvas returns the reusable frame buffer, reset to the requested geometry.
//
// Callers must not retain it past the render that produced it: the next View
// overwrites it.
func (a *App) canvas(w, h int, bg theme.RGB) *Canvas {
	if a.frame == nil {
		a.frame = &Canvas{}
	}
	a.frame.Reset(w, h, bg)
	return a.frame
}

// bodyCanvas is the offscreen buffer the detail view lays its device list on
// before blitting the visible window. Kept separate from the frame: the two are
// live at the same time.
func (a *App) bodyCanvas(w, h int, bg theme.RGB) *Canvas {
	if a.body == nil {
		a.body = &Canvas{}
	}
	a.body.Reset(w, h, bg)
	return a.body
}

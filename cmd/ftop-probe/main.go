package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"ftop/internal/nvml"
	"ftop/internal/wire"
)

func main() {
	var (
		interval  = flag.Duration("interval", time.Second, "gauge sampling interval")
		procEvery = flag.Duration("procs", 5*time.Second, "process-list interval")
		once      = flag.Bool("once", false, "emit one sample and exit")
	)
	// Only data goes to stdout, ever: the usage banner must not enter the record
	// stream. And a parse failure must EXIT rather than fall through with a zero
	// interval, which turns the probe into an unthrottled sampler.
	flag.CommandLine.Init(os.Args[0], flag.ContinueOnError)
	flag.CommandLine.SetOutput(os.Stderr)
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ftop-probe:", err)
		os.Exit(2)
	}
	if *interval <= 0 || *procEvery <= 0 {
		fmt.Fprintln(os.Stderr, "ftop-probe: interval and procs must be positive")
		os.Exit(2)
	}

	client := &nvml.Client{}
	devs, err := client.Devices()
	switch {
	case errors.Is(err, nvml.ErrNoDriver):
		// No NVIDIA driver: a CPU-only machine. It is managed like every other
		// host — its CPU and memory are sampled and it can be claimed for CPU work —
		// so the probe reports it with zero cards instead of failing.
		devs = nil
	case err != nil:
		fmt.Fprintf(os.Stderr, "ftop-probe: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	out := bufio.NewWriterSize(os.Stdout, 64<<10)
	host, _ := os.Hostname()

	// Labels are read from /proc once per pid and cached; a busy host re-reads
	// nothing between rounds, which keeps the process poll cheap.
	labels := newLabelCache()
	// The host's own processes are walked on the same slower cadence as the
	// cards', and reused between walks with HostProcsStale set.
	hp := newHostProcs()
	var lastHostProcs []wire.HostProc
	driver := client.DriverVersion()

	emit := func(procs bool) {
		h := readHeader(host, driver, countProcs())
		h.ProcsSampled = procs
		// The trailing bit says whether this round walked the process list. A round
		// without it carries no P records either way, and the reader must not read
		// "no P records" as "nothing running".
		// One record, every host fact, in the order parseHeader reads them. Appending
		// fields rather than adding record kinds keeps an older reader able to read a
		// newer probe's stream up to the fields it knows.
		fmt.Fprintf(out, "%s\t%s\t%d\t%d\t%.2f\t%d\t%d\t%d",
			wire.KindHeader, h.Host, h.Cores, h.CPUUs, h.Load1, h.MemKB, h.TotalKB,
			boolBit(procs))
		fmt.Fprintf(out, "\t%d\t%.2f\t%.2f\t%d\t%d\t%d\t%d\t%d\t%d\t%f\t%d\t%s\n",
			boolBit(h.CPUQuota), h.Load5, h.Load15,
			h.CacheKB, boolBit(h.MemLimited), h.SwapKB, h.SwapTotalKB,
			boolBit(h.SwapKnown), boolBit(h.SwapLimited),
			h.Uptime.Seconds(), h.Procs, h.Driver)

		// live collects the pids resolved during THIS round. The label cache keys on
		// pid alone, so without pruning, a pid the kernel later reuses inherits the
		// previous job's name and owner for as long as the process stays up.
		live := make(map[int]struct{}, 64)
		for _, d := range devs {
			r := client.Read(d, procs)
			// A failed query is not a zero reading. Marking the field absent makes
			// the reader drop it, so a driver that refused to answer cannot be
			// rendered as an idle card.
			util, memUsed, memTotal := "-", "-", "-"
			if r.UtilKnown {
				util = strconv.FormatInt(int64(r.Util+0.5), 10)
			}
			if r.MemKnown {
				memUsed = strconv.FormatUint(r.MemUsed, 10)
				memTotal = strconv.FormatUint(r.MemTotal, 10)
			}
			fmt.Fprintf(out, "%s\t%d\t%s\t%s\t%s\t%s\t%s",
				wire.KindGpu, d.Index, d.UUID, d.Name, util, memUsed, memTotal)
			fmt.Fprintf(out, "\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d",
				fmtField(r.Temp), fmtField(r.Power), fmtField(r.PowerCap),
				fmtField(r.ClockSM), fmtField(r.ClockMem), fmtField(r.Fan),
				"", r.Throttle)
			fmt.Fprintln(out)

			if r.ProcsKnown {
				// Say whether the enumeration succeeded, on the GPU line, so the
				// reader can tell a refused NVML call from an idle card even when
				// the card has no processes to report.
				fmt.Fprintf(out, "%s\t%d\n", wire.KindProcsKnown, d.Index)
			}
			for _, p := range r.Procs {
				pid := int(p.PID)
				lbl, user := labels.lookup(pid)
				live[pid] = struct{}{}
				fmt.Fprintf(out, "%s\t%d\t%d\t%d\t%s\t%s\n",
					wire.KindProc, d.Index, pid, p.MemOf()>>20, user, lbl)
			}
		}
		if procs {
			labels.retain(live)
			lastHostProcs = hp.scan(labels)
		}
		for _, p := range lastHostProcs {
			cpu := "-"
			if p.CPUPct != nil {
				cpu = strconv.FormatInt(int64(*p.CPUPct+0.5), 10)
			}
			fmt.Fprintf(out, "%s\t%d\t%d\t%s\t%s\t%s\n",
				wire.KindHostProc, p.PID, p.RSSKB, cpu, p.User, p.Name)
		}
		fmt.Fprintln(out, wire.KindEnd)
		out.Flush()
	}

	if *once {
		emit(true)
		return
	}

	// Two cadences in one loop: gauges every `interval`, the process list every
	// `procEvery`. The process walk costs ~4x the rest of the fields combined, and it
	// changes far more slowly than utilisation does.
	deadline := time.Now()
	procAt := time.Now()
	for {
		now := time.Now()
		wantProcs := now.After(procAt) || procAt.IsZero()
		emit(wantProcs)
		if wantProcs {
			procAt = now.Add(*procEvery)
		}
		deadline = deadline.Add(*interval)
		if sleep := time.Until(deadline); sleep > 0 {
			time.Sleep(sleep)
		} else {
			deadline = time.Now() // fell behind: resync rather than spin
		}
	}
}

// boolBit renders a flag as the wire's 0/1 field.
func boolBit(b bool) int {
	if b {
		return 1
	}
	return 0
}

// fmtField renders an optional numeric field as a literal "-" when the driver
// refused it, so the reader can tell "not supported" from "zero".
func fmtField(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatInt(int64(*v+0.5), 10)
}

// readHeader gathers host-level facts from procfs and cgroup v2.
//
// ONE scope per metric, chosen by the host's cgroup. A numeric limit means the
// probe is confined and every number describes that confinement. The literal
// "max", or no cgroup at all, means nothing is withheld from this workload, so
// the whole machine is the scope — numerator and denominator both.
func readHeader(host, driver string, nprocs int) wire.Header {
	h := wire.Header{Host: host, Driver: driver, Procs: nprocs}
	h.Load1, h.Load5, h.Load15 = readLoad()
	h.Uptime = readUptime()
	h.Cores, h.CPUQuota = countCores()
	h.CPUUs = readCPUTotal(h.CPUQuota)

	used, total, cache, limited, ok := readMem()
	if ok {
		h.MemKB, h.TotalKB, h.CacheKB, h.MemLimited = used, total, cache, limited
	}
	h.SwapKB, h.SwapTotalKB, h.SwapKnown, h.SwapLimited = readSwap(limited)
	return h
}

// countCores returns the CPU denominator and whether it is a cgroup quota.
//
// A numeric cpu.max is the quota; anything else is the machine's physical cores.
// The two are never mixed: a quota percentage over every core would read a
// confined job as idle, and a busy-time sum over a quota would read a host at
// full speed as saturated.
func countCores() (int, bool) {
	if body, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		f := strings.Fields(string(body))
		if len(f) == 2 && f[0] != "max" {
			q, err1 := strconv.Atoi(f[0])
			p, err2 := strconv.Atoi(f[1])
			if err1 == nil && err2 == nil && p > 0 && q > 0 {
				return (q + p - 1) / p, true
			}
		}
	}
	// Count with the line at the start of the file included: the first "processor"
	// record has no newline in front of it, so counting "\nprocessor\t:" missed it
	// and every host reported one core fewer than it has.
	n := 0
	if body, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "processor") {
				n++
			}
		}
	}
	if n == 0 {
		n = 1
	}
	return n, false
}

// readCPUTotal returns cumulative microseconds of busy CPU time in the scope
// countCores chose: cgroup usage_usec, or /proc/stat's busy fields.
func readCPUTotal(quota bool) uint64 {
	if quota {
		if body, err := os.ReadFile("/sys/fs/cgroup/cpu.stat"); err == nil {
			for _, line := range strings.Split(string(body), "\n") {
				if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
					n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
					return n
				}
			}
		}
	}
	body, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	// Sum the BUSY fields only (user, nice, system, irq, softirq, steal), excluding
	// idle and iowait, and excluding guest (already counted inside user/nice).
	// Summing every field makes the rate approach total capacity regardless of load,
	// which reads as a permanently saturated host.
	line, _, _ := strings.Cut(string(body), "\n")
	f := strings.Fields(line)
	if len(f) < 9 {
		return 0
	}
	var busy uint64
	for _, i := range []int{1, 2, 3, 6, 7, 8} { // user nice system irq softirq steal
		if i < len(f) {
			if n, err := strconv.ParseUint(f[i], 10, 64); err == nil {
				busy += n
			}
		}
	}
	// Jiffies are 10 ms on the usual 100 Hz USER_HZ configuration; a host that reports
	// otherwise falls back to the wall-clock delta below.
	return busy * 10000
}

func readLoad() (l1, l5, l15 float64) {
	body, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	f := strings.Fields(string(body))
	get := func(i int) float64 {
		if i < len(f) {
			v, _ := strconv.ParseFloat(f[i], 64)
			return v
		}
		return 0
	}
	return get(0), get(1), get(2)
}

func readUptime() time.Duration {
	body, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(body))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return time.Duration(v * float64(time.Second))
}

// readMem reports memory in ONE scope, with the page cache it excluded.
//
// A numeric memory.max confines the workload: usage is memory.current minus
// inactive_file (reporting memory.current alone made an idle host read 78%
// full, because the page cache holds almost everything), over memory.max.
// An unlimited cgroup is not a scope at all — the whole machine belongs to the
// workload — so the machine's MemTotal/MemAvailable is used for BOTH numerator
// and denominator. The old code took the cgroup numerator over the machine
// total in that case, a percentage of nothing.
func readMem() (usedKB, totalKB, cacheKB uint64, limited, ok bool) {
	max := readUint("/sys/fs/cgroup/memory.max")
	if max != ^uint64(0) && max > 0 {
		cur := readUint("/sys/fs/cgroup/memory.current")
		if cur == 0 {
			return 0, 0, 0, true, false
		}
		var inactive uint64
		if body, err := os.ReadFile("/sys/fs/cgroup/memory.stat"); err == nil {
			for _, line := range strings.Split(string(body), "\n") {
				if v, ok := strings.CutPrefix(line, "inactive_file "); ok {
					inactive, _ = strconv.ParseUint(strings.TrimSpace(v), 10, 64)
				}
			}
		}
		if cur > inactive {
			cur -= inactive
		}
		return cur / 1024, max / 1024, inactive / 1024, true, true
	}
	used, total, cache, ok := readProcMeminfo()
	return used, total, cache, false, ok
}

// readProcMeminfo returns (MemTotal-MemAvailable, MemTotal, Cached) in KiB.
//
// MemAvailable is the kernel's own estimate of what a new workload could claim,
// the same question the cgroup path answers with usage minus inactive_file.
func readProcMeminfo() (used, total, cache uint64, ok bool) {
	body, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, false
	}
	var avail uint64
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total, _ = strconv.ParseUint(f[1], 10, 64)
		case "MemAvailable:":
			avail, _ = strconv.ParseUint(f[1], 10, 64)
		case "Cached:":
			cache, _ = strconv.ParseUint(f[1], 10, 64)
		}
	}
	if total == 0 {
		return 0, 0, 0, false
	}
	if avail > total {
		avail = total
	}
	return total - avail, total, cache, true
}

// readSwap follows the same scope as readMem: a numeric memory.swap.max is the
// cgroup's swap limit, anything else is the machine's swap file.
//
// SwapKnown is false when the scope has no swap configured at all. That is not
// "0 of 0 used": a host with swap disabled is not a host whose swap is idle.
func readSwap(cgroupScope bool) (used, total uint64, known, limited bool) {
	if cgroupScope {
		max := readUint("/sys/fs/cgroup/memory.swap.max")
		if max != ^uint64(0) && max > 0 {
			cur := readUint("/sys/fs/cgroup/memory.swap.current")
			return cur / 1024, max / 1024, true, true
		}
	}
	body, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false, false
	}
	var free uint64
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "SwapTotal:":
			total, _ = strconv.ParseUint(f[1], 10, 64)
		case "SwapFree:":
			free, _ = strconv.ParseUint(f[1], 10, 64)
		}
	}
	if total == 0 {
		return 0, 0, false, false
	}
	if free > total {
		free = total
	}
	return total - free, total, true, false
}

func readUint(path string) uint64 {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(body))
	if s == "max" {
		return ^uint64(0)
	}
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

// countProcs is how many processes the host has. It walks /proc's directory
// entries only — no per-process files — so it is cheap enough to run every
// interval.
func countProcs() int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() && isPid(e.Name()) {
			n++
		}
	}
	return n
}

func isPid(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// hostProcTops is how many of the host's processes are reported, by RSS and by
// CPU. The UI's full table scrolls through these; the cap keeps one round of
// wire traffic at a few kilobytes even on a host with thousands of processes.
const hostProcTops = 40

// hostProcs walks /proc and returns the busiest processes by memory and by CPU.
//
// CPU is a delta of the process's own utime+stime between two walks, so the
// first walk has no baseline and reports nil rather than 0%. Percent is of ONE
// core, as top reports it: a process using four cores reads 400%, and a number
// capped at 100 would hide exactly the process worth finding.
type hostProcs struct {
	prev map[int]procCPU
}

type procCPU struct {
	ticks uint64
	at    time.Time
}

func newHostProcs() *hostProcs { return &hostProcs{prev: map[int]procCPU{}} }

// scan returns up to hostProcTops processes by RSS and up to hostProcTops by
// CPU, with the CPU figure filled from the previous walk.
func (hp *hostProcs) scan(labels *labelCache) []wire.HostProc {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	now := time.Now()
	pageKB := uint64(os.Getpagesize()) / 1024
	cur := make(map[int]procCPU, len(ents))
	type row struct {
		pid   int
		rssKB uint64
		ticks uint64
	}
	rows := make([]row, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() || !isPid(e.Name()) {
			continue
		}
		pid, _ := strconv.Atoi(e.Name())
		rssKB, ticks, ok := readProcStat(pid, pageKB)
		if !ok {
			continue // exited between ReadDir and the read
		}
		cur[pid] = procCPU{ticks: ticks, at: now}
		rows = append(rows, row{pid: pid, rssKB: rssKB, ticks: ticks})
	}

	// The baseline is per process, so a process that is new this round has no
	// CPU reading, and one that exited simply disappears from the map.
	cpuPct := func(pid int, ticks uint64) *float64 {
		p, seen := hp.prev[pid]
		if !seen {
			return nil
		}
		dt := now.Sub(p.at).Seconds()
		if dt <= 0 || ticks < p.ticks {
			return nil
		}
		// The kernel reports USER_HZ ticks, 100 on the usual configuration; Go has no
		// sysconf sibling that would read it, so the constant is assumed.
		v := float64(ticks-p.ticks) / 100 / dt * 100
		if v < 0 {
			v = 0
		}
		return &v
	}

	byRSS := append([]row(nil), rows...)
	sort.SliceStable(byRSS, func(i, j int) bool { return byRSS[i].rssKB > byRSS[j].rssKB })
	byCPU := append([]row(nil), rows...)
	sort.SliceStable(byCPU, func(i, j int) bool {
		return byCPU[i].ticks-hp.prevTicks(byCPU[i].pid) > byCPU[j].ticks-hp.prevTicks(byCPU[j].pid)
	})

	out := make([]wire.HostProc, 0, 2*hostProcTops)
	seen := make(map[int]bool, 2*hostProcTops)
	add := func(r row) {
		if seen[r.pid] || len(out) >= 2*hostProcTops {
			return
		}
		seen[r.pid] = true
		name, user := labels.lookup(r.pid)
		out = append(out, wire.HostProc{
			PID: r.pid, RSSKB: r.rssKB, CPUPct: cpuPct(r.pid, r.ticks),
			User: user, Name: name,
		})
	}
	for i := 0; i < len(byRSS) && i < hostProcTops; i++ {
		add(byRSS[i])
	}
	for i := 0; i < len(byCPU) && i < hostProcTops; i++ {
		add(byCPU[i])
	}
	hp.prev = cur
	return out
}

func (hp *hostProcs) prevTicks(pid int) uint64 {
	if p, ok := hp.prev[pid]; ok {
		return p.ticks
	}
	return 0
}

// readProcStat returns one process's resident pages as KiB and its cumulative
// utime+stime in clock ticks.
//
// statm gives the resident set in pages without the formatting traps of stat's
// comm field, which can contain spaces and parentheses. stat is read separately
// for the CPU counters, splitting after the LAST ')' so a process named
// "foo) bar" cannot shift its own fields.
func readProcStat(pid int, pageKB uint64) (rssKB, ticks uint64, ok bool) {
	dir := "/proc/" + strconv.Itoa(pid)
	if body, err := os.ReadFile(dir + "/statm"); err == nil {
		f := strings.Fields(string(body))
		if len(f) >= 2 {
			if pages, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				rssKB = pages * pageKB
			}
		}
	}
	body, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return 0, 0, false
	}
	i := strings.LastIndexByte(string(body), ')')
	if i < 0 || i+2 >= len(body) {
		return rssKB, 0, true
	}
	f := strings.Fields(string(body[i+2:]))
	// f[0] is state; utime is field 14 and stime field 15 in proc(5), so 11 and
	// 12 after the fields consumed by pid and comm.
	if len(f) >= 13 {
		u, _ := strconv.ParseUint(f[11], 10, 64)
		s, _ := strconv.ParseUint(f[12], 10, 64)
		ticks = u + s
	}
	return rssKB, ticks, true
}

// labelCache resolves pid -> (label, user) from /proc/<pid>/cmdline.
//
// nvidia-smi and NVML both report only argv[0], which on these hosts is always an
// interpreter path. The label that says what a job IS lives in the rest of the
// command line — "worker.py:pick_place" instead of "python".
type labelCache struct {
	m map[int]label
}

type label struct{ text, user string }

func newLabelCache() *labelCache { return &labelCache{m: map[int]label{}} }

// lookup returns the cached label, or resolves it.
func (c *labelCache) lookup(pid int) (string, string) {
	if l, ok := c.m[pid]; ok {
		return l.text, l.user
	}
	l := resolveLabel(pid)
	c.m[pid] = l
	// Bound the cache: a host that churns pids would otherwise grow it forever.
	if len(c.m) > 4096 {
		c.m = map[int]label{pid: l}
	}
	return l.text, l.user
}

// retain drops entries for pids that are no longer on any GPU.
//
// Expiring them on every successful process poll is what keeps a reused pid from
// wearing the previous job's name. Keying on pid alone cannot tell the two apart,
// so the safer direction is to re-resolve: a mislabelled job is worse than one
// extra /proc read.
func (c *labelCache) retain(live map[int]struct{}) {
	for pid := range c.m {
		if _, ok := live[pid]; !ok {
			delete(c.m, pid)
		}
	}
}

func resolveLabel(pid int) label {
	dir := fmt.Sprintf("/proc/%d", pid)
	user := "-"
	if fi, err := os.Stat(dir); err == nil {
		if st, ok := fileOwner(fi); ok {
			user = st
		}
	}

	body, err := os.ReadFile(dir + "/cmdline")
	if err != nil || len(body) == 0 {
		if exe, err := os.Readlink(dir + "/exe"); err == nil {
			return label{text: filepath.Base(exe), user: user}
		}
		return label{text: "-", user: user}
	}
	argv := strings.Split(strings.TrimRight(string(body), "\x00"), "\x00")

	// Prefer a .py/.sh argument: that is the program, not the interpreter.
	script := ""
	for _, a := range argv {
		base := filepath.Base(a)
		if strings.HasSuffix(base, ".py") || strings.HasSuffix(base, ".sh") {
			script = base
			break
		}
	}
	if script == "" {
		script = filepath.Base(argv[0])
	}
	script = strings.TrimSuffix(strings.TrimSuffix(script, ".py"), ".sh")

	// Append --task_name when present: two workers of the same program are
	// otherwise indistinguishable, and that distinction is the whole point.
	task := ""
	for i, a := range argv {
		if (a == "--task_name" || a == "--task-name") && i+1 < len(argv) {
			task = argv[i+1]
			break
		}
		if v, ok := strings.CutPrefix(a, "--task_name="); ok {
			task = v
			break
		}
	}
	text := script
	if task != "" {
		text += ":" + task
	}
	return label{text: truncateRunes(text, 40), user: user}
}

// truncateRunes cuts s to at most n runes.
//
// Slicing bytes instead split multi-byte characters, and the resulting invalid
// UTF-8 went out on the wire: a strict JSON reader on the other side rejects it,
// and the label is the only part of the process record a human wrote.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for off := range s {
		if i == n {
			return s[:off]
		}
		i++
	}
	return s
}

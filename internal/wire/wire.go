// Package wire defines the record format between the remote probe and the TUI.
//
// One definition shared by both binaries, because the alternative — a shell
// producer and a hand-written parser — is where the previous implementation lost
// fields silently. A field the probe stops emitting shows up as a zero, not an error.
package wire

import "math"

import "time"

// Record kinds. A sample is a Header plus N Gpus plus N Procs, terminated by End.
const (
	KindHeader = "H"
	KindGpu    = "G"
	KindProc   = "P"
	// KindHostProc is one process on the host rather than on a card. It arrives
	// on the same cadence as KindProc and is absent from a round that did not
	// walk processes.
	KindHostProc = "R"
	// KindProcsKnown precedes the P records of a card whose process enumeration
	// actually succeeded. Its absence for a card means the driver refused the
	// query, which is not the same fact as "nothing is running".
	KindProcsKnown = "K"
	KindEnd        = "E"
)

// Gpu is one device reading.
//
// Pointers distinguish "reported as zero" from "not supported by this driver".
// Some drivers report per-process VRAM as 0 while the device is 72 GiB full; collapsing
// that to a plain float makes the UI print a measurement that was never taken.
type Gpu struct {
	Index int
	UUID  string
	Name  string
	// Util and Mem are pointers for the same reason the gauges below are: "-" means
	// the driver refused the query, which is a different fact from "reported 0%".
	// Collapsing them renders an unreadable card as an idle one.
	Util     *float64
	MemUsed  *uint64 // MiB
	MemTotal *uint64 // MiB
	// MemReserved is what the driver and firmware keep for themselves (MiB). It is
	// not in MemUsed. Nil from a driver that only answers the v1 memory query, whose
	// MemUsed includes it, and from a probe that predates the field.
	MemReserved *uint64
	Temp        *float64
	Power       *float64 // W
	PowerCap    *float64 // W
	ClockSM     *float64 // MHz
	ClockMem    *float64 // MHz
	Fan         *float64 // %
	PcieGen     *int
	PcieWide    *int
	Throttle    uint64 // NVML clocks-throttle reason bitmask

	Procs []Proc

	// ProcsStale marks a list carried over from an earlier process poll. Showing a
	// five-second-old process list as current is a small lie; the UI needs to be able
	// to tell it apart from a fresh one.
	ProcsStale bool

	// ProcsKnown is false when this round could not enumerate the card's processes.
	// It is the per-card half of Header.ProcsSampled: an empty list means "nothing
	// running" only when it is true. Collapsing the two is how a permission failure
	// or a refused NVML call renders as an idle card.
	ProcsKnown bool
}

// UtilPct is utilisation, or -1 when the driver did not report it or reported
// something that is not a number.
func (g Gpu) UtilPct() float64 {
	if g.Util == nil || math.IsNaN(*g.Util) || math.IsInf(*g.Util, 0) {
		return -1
	}
	return *g.Util
}

// MemPct is VRAM utilisation in 0..1, or -1 when unknown.
func (g Gpu) MemPct() float64 {
	if g.MemUsed == nil || g.MemTotal == nil || *g.MemTotal == 0 {
		return -1
	}
	return float64(*g.MemUsed) / float64(*g.MemTotal)
}

// PowerPct is draw against the enforced cap, 0..1.
func (g Gpu) PowerPct() float64 {
	if g.PowerCap == nil || *g.PowerCap <= 0 || g.Power == nil {
		return 0
	}
	return *g.Power / *g.PowerCap
}

// Proc is one compute process.
type Proc struct {
	PID   int
	Name  string // short label: "worker.py:pick_place"
	User  string
	MemMB uint64
}

// Header carries host-level facts for one sample.
//
// CPU and memory are read at ONE scope, chosen by the host's cgroup:
//
//   - A numeric cgroup limit means the probe is confined, and every number
//     describes that confinement: cpu.max's quota is the CPU denominator, and
//     memory.current minus reclaimable cache is read over memory.max.
//   - No numeric limit means the whole machine belongs to this workload: the
//     busy time in /proc/stat over every core, and MemTotal-MemAvailable over
//     MemTotal.
//
// The scope is chosen per metric and never mixed. Reading a cgroup numerator
// over a machine denominator — which "memory.max" being the literal max used to
// do — produces a percentage that describes neither scope. CPUQuota and
// MemLimited say which scope each reading came from, so the UI can name it
// rather than guess.
type Header struct {
	Host  string
	Cores int    // denominator for the CPU percentage: quota cores, or physical cores
	CPUUs uint64 // cumulative microseconds of the same scope; the reader differences it
	// CPUQuota is true when Cores came from a numeric cpu.max and CPUUs from
	// cpu.stat. False is the machine: physical cores and /proc/stat's busy time.
	CPUQuota bool
	Load1    float64
	Load5    float64
	Load15   float64
	// MemKB is used memory of the scope; TotalKB is its limit (memory.max, or
	// MemTotal when the scope is the machine).
	MemKB      uint64
	TotalKB    uint64
	MemLimited bool
	// CacheKB is reclaimable page cache the used figure already excludes:
	// inactive_file in cgroup scope, Cached in machine scope.
	CacheKB uint64
	// SwapKB and SwapTotalKB are cgroup swap when memory.swap.max is numeric,
	// the machine's swap when it is not. SwapKnown is false when the host has no
	// swap at all, which is not the same fact as 0 of it used.
	SwapKB      uint64
	SwapTotalKB uint64
	SwapKnown   bool
	SwapLimited bool
	// Procs is the number of processes on the host, GPU or not.
	Procs  int
	Uptime time.Duration
	// Driver is the NVIDIA driver version, as NVML reports it. Empty when NVML
	// did not answer.
	Driver string
	// SampleAt is when the probe took the reading.
	SampleAt time.Time

	// ProcsSampled says whether THIS round walked the process list. The probe
	// samples gauges every interval and processes every few intervals, so a round
	// with no P records is usually "not sampled yet", not "nothing running".
	//
	// Without this bit the reader cannot tell those apart, and it chose to carry
	// the previous list forward — which meant a finished job stayed on screen
	// forever, and a lease built on that observation would never be released.
	ProcsSampled bool
}

// HostProc is one process on the host, independent of any card.
//
// It exists because the GPU list answers "who is on this card" but not "what is
// eating this machine's memory". The two are different questions on a host whose
// training job holds 300 GiB of RSS and 20 GiB of VRAM.
type HostProc struct {
	PID    int
	RSSKB  uint64
	CPUPct *float64 // percent of ONE core, as top reports it; nil on the first round
	User   string
	Name   string
}

// Sample is one complete round.
type Sample struct {
	Header
	Gpus []Gpu
	// HostProcs is the busiest of the host's processes, by RSS. A round that did
	// not walk processes carries the previous list and HostProcsStale says so.
	HostProcs      []HostProc
	HostProcsStale bool
	At             time.Time

	// CPUPct is derived by the reader from consecutive CPUUs values. Nil means
	// "no baseline yet", which the UI shows as an em dash rather than 0%.
	CPUPct *float64
}

// MemPct is used memory against the scope's limit, or -1 when unknown.
func (s Sample) MemPct() float64 {
	if s.TotalKB == 0 {
		return -1
	}
	return float64(s.MemKB) / float64(s.TotalKB) * 100
}

// SwapPct is used swap against the scope's limit, or -1 when the host has none.
func (s Sample) SwapPct() float64 {
	if !s.SwapKnown || s.SwapTotalKB == 0 {
		return -1
	}
	return float64(s.SwapKB) / float64(s.SwapTotalKB) * 100
}

// CPUPctValue is CPUPct as a plain float, -1 when there is no baseline yet.
func (s Sample) CPUPctValue() float64 {
	if s.CPUPct == nil {
		return -1
	}
	return *s.CPUPct
}

// ProcCount is the number of distinct processes across the host's GPUs.
func (s Sample) ProcCount() int {
	seen := make(map[int]struct{}, 8)
	for _, g := range s.Gpus {
		for _, p := range g.Procs {
			seen[p.PID] = struct{}{}
		}
	}
	return len(seen)
}

// BusyGPUs counts cards above the idle threshold.
//
// Cards with no reading are not counted as idle: they are not idle, they are
// unknown, and "0 busy" on a fleet of unreadable cards is the wrong statement.
func (s Sample) BusyGPUs() int {
	n := 0
	for _, g := range s.Gpus {
		if u := g.UtilPct(); u >= 5 {
			n++
		}
	}
	return n
}

// UtilAvg is the mean utilisation across the devices that reported one.
//
// Averaging unknown cards in as zeros is how a fleet with half its GPUs
// unreadable reads as uniformly idle.
func (s Sample) UtilAvg() float64 {
	sum, n := 0.0, 0
	for _, g := range s.Gpus {
		if u := g.UtilPct(); u >= 0 {
			sum += u
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// KnownGPUs counts devices the driver answered for.
func (s Sample) KnownGPUs() int {
	n := 0
	for _, g := range s.Gpus {
		if g.UtilPct() >= 0 {
			n++
		}
	}
	return n
}

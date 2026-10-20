// Package demo streams a synthetic fleet through the same messages the ssh
// readers produce.
//
// It exists for design work and screenshots. The real fleet is often idle, and an
// idle board hides every layout problem worth finding: empty graphs, bars that
// never reach the hot end of their gradient, numbers that never get wide. `ftop
// -demo` drives the real UI, not a mock-up of it, so what it shows is what ftop
// draws.
package demo

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"ftop/internal/sshx"
	"ftop/internal/wire"
)

// spec is one synthetic machine.
type spec struct {
	name  string
	gpus  int
	model string
	vram  uint64 // MiB
	cap   float64
	load  float64 // mean utilisation the host hovers around
	// workers is how many small processes share each busy card with its main
	// job: some workloads run many per card.
	workers int
	// quota > 0 makes the host cgroup-confined: its cpu denominator and its
	// memory limit are the quota, not the machine. Without one the host is a
	// whole machine, which is the other scope the UI must render correctly.
	quota int
	// memFull is the memory limit's fill at rest, 0..1. The fleet needs a host
	// near its limit and one with room: an all-comfortable board hides every
	// layout problem a full one has.
	memFull float64
	// swapGB is the swap the scope has; 0 means none at all, which is a fact the
	// host box must state rather than draw as an empty meter.
	swapGB float64
	err    string // non-empty: the host never answers
}

var fleet = []spec{
	// The fleet covers both scopes, a full host and an empty one, and a host with
	// no swap: every one of those is a different rendering of the host row.
	{name: "dc-a", gpus: 8, model: "NVIDIA H200", vram: 143771, cap: 700, load: 96, workers: 5, quota: 128, memFull: 0.65, swapGB: 64},
	{name: "node-a", gpus: 8, model: "NVIDIA GeForce RTX 4090", vram: 24564, cap: 450, load: 48, memFull: 0.93, swapGB: 32},
	{name: "rig-08", gpus: 8, model: "NVIDIA GeForce RTX 4090", vram: 24564, cap: 450, load: 26, quota: 64, memFull: 0.48},
	{name: "box-01", gpus: 4, model: "NVIDIA GeForce RTX 4090", vram: 24564, cap: 450, load: 71, memFull: 0.74, swapGB: 16},
	{name: "box-02", gpus: 4, model: "NVIDIA GeForce RTX 4090", vram: 24564, cap: 450, load: 33, quota: 32, memFull: 0.31},
	{name: "box-03", gpus: 4, model: "NVIDIA GeForce RTX 4090", vram: 24564, cap: 450, load: 90, memFull: 0.88, swapGB: 32},
	{name: "dc-b", gpus: 2, model: "NVIDIA H200", vram: 143771, cap: 700, load: 12, quota: 32, memFull: 0.2},
	{name: "duo-a", gpus: 2, model: "NVIDIA GeForce RTX 4090", vram: 24564, cap: 450, load: 18, memFull: 0.42, swapGB: 16},
	{name: "box-04", gpus: 2, model: "NVIDIA GeForce RTX 4090", vram: 24564, cap: 450, load: 0, memFull: 0.08},
	{name: "dev", gpus: 1, model: "NVIDIA GeForce RTX 5090", vram: 32607, cap: 575, load: 6, memFull: 0.55, swapGB: 8},
	{name: "box-05", gpus: 1, model: "NVIDIA GeForce RTX 4090", vram: 24564, cap: 450, load: 0, quota: 16, memFull: 0.11},
	{name: "box-06", err: "connection timed out: ssh: connect to host 192.0.2.21 port 22"},
	{name: "box-07", err: "authentication failed: Permission denied (publickey)"},
}

// Hosts lists the synthetic fleet's names.
func Hosts() []string {
	out := make([]string, len(fleet))
	for i, s := range fleet {
		out[i] = s.name
	}
	return out
}

// Source is a running synthetic fleet. Its state is the tick counter and the
// noise source, so a warm-up followed by Run continues one timeline.
type Source struct {
	tick int
	rng  *rand.Rand
}

// New starts a synthetic fleet with a fixed seed, so screenshots are repeatable.
func New() *Source { return &Source{rng: rand.New(rand.NewSource(7))} }

// Warm replays n intervals at once, so the graphs open with history instead of a
// blank strip that takes a minute to fill. Call it before Run, synchronously.
func (src *Source) Warm(n int, sample func(sshx.SampleMsg), fail func(sshx.ErrMsg)) {
	for _, s := range fleet {
		if s.err != "" {
			fail(sshx.ErrMsg{Host: s.name, Err: s.err})
		}
	}
	for i := 0; i < n; i++ {
		src.emit(sample)
	}
}

// Run emits one sample per host per interval until ctx ends.
func (src *Source) Run(ctx context.Context, interval time.Duration, sample func(sshx.SampleMsg)) {
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			src.emit(sample)
		}
	}
}

func (src *Source) emit(sample func(sshx.SampleMsg)) {
	for i, s := range fleet {
		if s.err == "" {
			sample(sshx.SampleMsg{Host: s.name, Sample: s.at(i, src.tick, src.rng)})
		}
	}
	src.tick++
}

// at is host i's reading at tick n: each card follows a slow wave of its own
// phase around the host's mean, with a little noise, so neighbouring cards differ
// and graphs have shape.
func (s spec) at(i, n int, rng *rand.Rand) wire.Sample {
	var out wire.Sample
	cores := 64 + 32*(s.gpus/4)
	out.CPUQuota = s.quota > 0
	if s.quota > 0 {
		cores = s.quota
	}
	out.Cores = cores
	out.Load1 = s.load / 6 * float64(cores) / 64
	out.Load5 = out.Load1 * 0.92
	out.Load15 = out.Load1 * 0.85
	cpu := math.Min(100, 4+s.load*0.55+rng.Float64()*6)
	out.CPUPct = &cpu
	// A confined host's limit is its quota; a whole machine's is 512 GiB. The
	// quota case is the one the fleet actually runs in, and it is the case whose
	// absolute reading is much smaller than the machine's.
	totalKB := uint64(512 * 1024 * 1024)
	out.MemLimited = s.quota > 0
	if out.MemLimited {
		totalKB = uint64(s.quota) * 4 * 1024 * 1024
	}
	out.TotalKB = totalKB
	fill := s.memFull
	if fill == 0 {
		fill = 0.12 + s.load/180
	}
	out.MemKB = uint64(float64(totalKB) * math.Min(0.985, fill+rng.Float64()*0.02))
	out.CacheKB = out.MemKB / 4
	if s.swapGB > 0 {
		out.SwapKnown = true
		out.SwapLimited = out.MemLimited
		out.SwapTotalKB = uint64(s.swapGB * 1024 * 1024)
		out.SwapKB = uint64(float64(out.SwapTotalKB) * math.Min(0.9, fill*0.05))
	}
	out.Procs = 40 + 6*s.gpus
	out.Uptime = time.Duration(12*s.gpus+3) * time.Hour
	out.Driver = "550.54.15"
	out.ProcsSampled = true
	out.HostProcs = s.hostProcs(i, out.MemKB, cores)
	out.SampleAt = time.Now()
	for k := 0; k < s.gpus; k++ {
		phase := float64(i*7 + k*3)
		wave := math.Sin(float64(n)*0.09+phase) * 0.6
		wave += math.Sin(float64(n)*0.023+phase*1.7) * 0.4
		u := s.load + wave*math.Min(s.load, 100-s.load+10)*0.6 + (rng.Float64()-0.5)*6
		// Spread cards around the host mean so an 8-GPU host is not 8 equal rows.
		u += float64((k*17)%40-20) * math.Min(s.load, 100-s.load) / 50
		if s.load == 0 {
			u = 0
		}
		u = math.Round(math.Min(100, math.Max(0, u)))

		total := s.vram
		frac := 0.08 + 0.85*math.Min(1, s.load/100+0.15*math.Sin(phase))
		if s.load == 0 {
			frac = 0.002
		}
		used := uint64(float64(total) * math.Min(0.99, math.Max(0, frac)))
		temp := math.Round(30 + u*0.42 + float64(k%3))
		pw := math.Round(s.cap*0.07 + u/100*s.cap*0.86)
		clk := 210 + u*24
		capw := s.cap

		g := wire.Gpu{
			Index: k, Name: s.model, UUID: fmt.Sprintf("GPU-demo-%s-%d", s.name, k),
			Util: &u, MemUsed: &used, MemTotal: &total,
			Temp: &temp, Power: &pw, PowerCap: &capw, ClockSM: &clk,
		}
		if u > 40 {
			g.Procs = []wire.Proc{{PID: 40000 + i*100 + k, Name: "train.py", User: "demo", MemMB: used * 9 / 10}}
			if s.workers > 0 {
				// A shared box: many small workers beside the big job, the shape that
				// makes a host's view list only the top few.
				for w := 0; w < s.workers; w++ {
					g.Procs = append(g.Procs, wire.Proc{
						PID:   41000 + i*100 + k*20 + w,
						Name:  "worker.py:pick_place",
						User:  []string{"demo", "ci", "eval"}[w%3],
						MemMB: used / 10 / uint64(s.workers+w),
					})
				}
			}
		}
		out.Gpus = append(out.Gpus, g)
	}
	return out
}

// hostProcs is a plausible process list for a synthetic host: one big job holding
// most of the memory, a few workers, and a straggler or two. It is built to have
// names wide enough to be cut, users of different widths, and a cpu column that
// disagrees with the memory order — the three things a table has to survive.
func (s spec) hostProcs(i int, memKB uint64, cores int) []wire.HostProc {
	users := []string{"alice", "bob", "ci", "eval", "a-very-long-user"}
	names := []string{
		"train.py --task_name pick_place",
		"worker.py:pick_place",
		"python3 -u eval_harness.py",
		"build_cache",
		"jupyter-lab",
	}
	// Two entries share a pid with the cards' processes, so the join the table
	// displays is visible in the demo fleet rather than only on a real host.
	pids := []int{
		40000 + i*100 + 1, // the card 0 job
		40000 + i*100 + 2, // the card 1 job
		41000 + i*100,     // a shared worker
		41000 + i*100 + 1, // another worker
		1000 + i*1000 + 5, // a process that holds no card
		1000 + i*1000 + 6,
	}
	out := make([]wire.HostProc, 0, 8)
	var total float64
	for k := 1; k <= 6; k++ {
		frac := math.Pow(0.52, float64(k))
		if k == 2 {
			frac = 0.31 // a second job that is not negligible
		}
		total += frac
		cpu := float64(cores) * frac * 1.6
		if k == 4 {
			cpu = 380 // a busy small process: first by cpu, last by memory
		}
		c := cpu
		out = append(out, wire.HostProc{
			PID:    pids[k-1],
			RSSKB:  uint64(float64(memKB) * frac / total),
			CPUPct: &c,
			User:   users[k%len(users)],
			Name:   names[k%len(names)],
		})
	}
	return out
}

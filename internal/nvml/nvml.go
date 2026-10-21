// Package nvml talks to libnvidia-ml directly through purego.
//
// Why not cgo: the probe is cross-compiled to a static linux/amd64 binary on a Mac
// and pushed to hosts that have no toolchain of their own. CGO_ENABLED=0 keeps that
// a single file with no libc-version coupling — the fleet spans glibc 2.39 on 24.04
// and older Ubuntu on the remainder. Why not nvidia-smi: a full query costs ~342 ms
// per host, while the same fields through NVML cost ~2 ms, a 170x difference that is
// the whole reason a 1 Hz fleet view is affordable.
package nvml

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Library names, in the order the driver installs them.
var libNames = []string{
	"libnvidia-ml.so.1",
	"libnvidia-ml.so",
	"/usr/lib/x86_64-linux-gnu/libnvidia-ml.so.1",
	"/usr/lib64/libnvidia-ml.so.1",
}

// Throttle reason bits exposed by nvmlDeviceGetCurrentClocksThrottleReasons.
const (
	ThrottleGpuIdle           = 0x0000000000000001
	ThrottleAppClocks         = 0x0000000000000002
	ThrottleSwPowerCap        = 0x0000000000000004
	ThrottleHwSlowdown        = 0x0000000000000008
	ThrottleSyncBoost         = 0x0000000000000010
	ThrottleSwThermalSlowdown = 0x0000000000000020
	ThrottleHwThermalSlowdown = 0x0000000000000040
	ThrottleHwPowerBrake      = 0x0000000000000080
	ThrottleDisplayClock      = 0x0000000000000100
)

// Temperature sensor selectors.
const tempGpu = 0

// Clock selectors, from nvmlClockType_t: GRAPHICS=0, SM=1, MEM=2.
//
// Using 0/1 labelled graphics as SM and SM as memory, which reads as plausible
// numbers on a quiet card while being entirely the wrong quantity.
const (
	clockSM  = 1
	clockMem = 2
)

// Device is one handle.
type Device struct {
	handle uintptr
	Index  int
	UUID   string
	Name   string
}

// Client owns the loaded library. One per process; nvmlInit is ~27-48 ms and must
// not be paid per sample.
type Client struct {
	once sync.Once
	err  error

	init          func() int32
	shutdown      func() int32
	getCount      func(*uint32) int32
	getHandle     func(uint32, *uintptr) int32
	getUUID       func(uintptr, *byte, uint32) int32
	getName       func(uintptr, *byte, uint32) int32
	getUtil       func(uintptr, *[2]uint32) int32
	getMem        func(uintptr, *memInfo) int32
	getMemV2      func(uintptr, *memInfoV2) int32 // nil when the driver lacks the symbol
	getTemp       func(uintptr, uint32, *uint32) int32
	getPower      func(uintptr, *uint32) int32
	getPowerLimit func(uintptr, *uint32) int32
	getClock      func(uintptr, uint32, *uint32) int32
	getFan        func(uintptr, *uint32) int32
	getThrottle   func(uintptr, *uint64) int32
	getPcieGen    func(uintptr, *uint32) int32
	getPcieWidth  func(uintptr, *uint32) int32
	getProcs      func(uintptr, *uint32, *procInfo) int32
	getProcsV3    func(uintptr, *uint32, *[4]uint64) int32
	getDriverVer  func(*byte, uint32) int32
}

// procBufCap bounds one enumeration call. A 16-GPU multi-tenant host can carry a
// few hundred contexts, so 64 was not enough.
const procBufCap = 512

// memInfo mirrors nvmlMemory_t.
//
// Its Used is Total minus Free, which counts the memory the driver and firmware
// reserve for themselves. On a 4090 under driver 565 that is 457 MiB, so every idle
// card read 458 MiB used and every card in the fleet read the same. It is only the
// fallback for a driver without memInfoV2.
type memInfo struct {
	Total uint64
	Free  uint64
	Used  uint64
}

// memInfoV2 mirrors nvmlMemory_v2_t: the v1 fields with the reservation split out,
// so Used is what processes and the driver actually allocated — the number
// nvidia-smi shows as memory.used.
//
// Go aligns Total to 8 exactly as C does, leaving the same 4 bytes of padding after
// Version; TestMemInfoV2Layout pins it.
type memInfoV2 struct {
	Version  uint32
	Total    uint64
	Reserved uint64
	Free     uint64
	Used     uint64
}

// memInfoV2Version is NVML_STRUCT_VERSION(Memory, 2): the struct's size with the
// version number in the top byte. The driver checks it before writing anything.
const memInfoV2Version = uint32(unsafe.Sizeof(memInfoV2{})) | 2<<24

// procInfo mirrors nvmlProcessInfo_t.
type procInfo struct {
	PID               uint32
	UsedGpuMem        uint64
	GpuInstanceId     uint32
	ComputeInstanceId uint32
}

// Open loads the driver and initialises NVML.
func (c *Client) Open() error {
	c.once.Do(func() {
		var lib uintptr
		var lastErr error
		for _, name := range libNames {
			h, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err == nil {
				lib = h
				break
			}
			lastErr = err
		}
		if lib == 0 {
			c.err = fmt.Errorf("%w: libnvidia-ml not found: %v", ErrNoDriver, lastErr)
			return
		}
		purego.RegisterLibFunc(&c.init, lib, "nvmlInit_v2")
		purego.RegisterLibFunc(&c.shutdown, lib, "nvmlShutdown")
		purego.RegisterLibFunc(&c.getCount, lib, "nvmlDeviceGetCount_v2")
		purego.RegisterLibFunc(&c.getHandle, lib, "nvmlDeviceGetHandleByIndex_v2")
		purego.RegisterLibFunc(&c.getUUID, lib, "nvmlDeviceGetUUID")
		purego.RegisterLibFunc(&c.getName, lib, "nvmlDeviceGetName")
		purego.RegisterLibFunc(&c.getUtil, lib, "nvmlDeviceGetUtilizationRates")
		purego.RegisterLibFunc(&c.getMem, lib, "nvmlDeviceGetMemoryInfo")
		// _v2 arrived with driver 510. RegisterLibFunc panics on a missing symbol, so
		// it is looked up first; an older driver keeps the v1 reading.
		if sym, err := purego.Dlsym(lib, "nvmlDeviceGetMemoryInfo_v2"); err == nil {
			purego.RegisterFunc(&c.getMemV2, sym)
		}
		purego.RegisterLibFunc(&c.getTemp, lib, "nvmlDeviceGetTemperature")
		purego.RegisterLibFunc(&c.getPower, lib, "nvmlDeviceGetPowerUsage")
		purego.RegisterLibFunc(&c.getPowerLimit, lib, "nvmlDeviceGetEnforcedPowerLimit")
		purego.RegisterLibFunc(&c.getClock, lib, "nvmlDeviceGetClockInfo")
		purego.RegisterLibFunc(&c.getFan, lib, "nvmlDeviceGetFanSpeed")
		purego.RegisterLibFunc(&c.getThrottle, lib, "nvmlDeviceGetCurrentClocksThrottleReasons")
		purego.RegisterLibFunc(&c.getPcieGen, lib, "nvmlDeviceGetCurrPcieLinkGeneration")
		purego.RegisterLibFunc(&c.getPcieWidth, lib, "nvmlDeviceGetCurrPcieLinkWidth")
		// _v2 writes nvmlProcessInfo_v2_t, which is exactly the 24-byte struct above.
		// The unversioned symbol writes the 16-byte v1 layout, so binding it here
		// decoded every entry after the first from the wrong offset.
		purego.RegisterLibFunc(&c.getProcs, lib, "nvmlDeviceGetComputeRunningProcesses_v2")
		purego.RegisterLibFunc(&c.getDriverVer, lib, "nvmlSystemGetDriverVersion")

		if rc := c.init(); rc != 0 {
			c.err = fmt.Errorf("nvmlInit failed with rc=%d (driver not loaded?)", rc)
		}
	})
	return c.err
}

// Close shuts NVML down.
func (c *Client) Close() {
	if c.err == nil && c.shutdown != nil {
		c.shutdown()
	}
}

// Devices enumerates the handles once; indices do not change while the driver lives.
func (c *Client) Devices() ([]Device, error) {
	if err := c.Open(); err != nil {
		return nil, err
	}
	var n uint32
	if rc := c.getCount(&n); rc != 0 {
		return nil, fmt.Errorf("nvmlDeviceGetCount rc=%d", rc)
	}
	out := make([]Device, 0, n)
	for i := uint32(0); i < n; i++ {
		var h uintptr
		if rc := c.getHandle(i, &h); rc != 0 {
			continue
		}
		d := Device{handle: h, Index: int(i)}
		// C wants a char*, so pass the first element rather than the array value.
		var buf [96]byte
		if rc := c.getUUID(h, &buf[0], uint32(len(buf))); rc == 0 {
			d.UUID = cstr(buf[:])
		}
		if rc := c.getName(h, &buf[0], uint32(len(buf))); rc == 0 {
			d.Name = cstr(buf[:])
		}
		out = append(out, d)
	}
	return out, nil
}

// DriverVersion is the NVIDIA driver's version string, or "" when the driver
// does not answer.
func (c *Client) DriverVersion() string {
	if err := c.Open(); err != nil || c.getDriverVer == nil {
		return ""
	}
	var buf [80]byte
	if rc := c.getDriverVer(&buf[0], uint32(len(buf))); rc != 0 {
		return ""
	}
	return cstr(buf[:])
}

// ErrNoDriver means the NVIDIA management library is not installed at all: the
// machine has no NVIDIA driver, which on a fleet managed without exceptions is an
// ordinary CPU-only host, not a failure. A library that loads but will not
// initialise is a different error — a GPU host with a broken driver.
var ErrNoDriver = errors.New("no NVIDIA driver")

// errNotSupported marks a getter the driver refused; callers turn it into a nil.
var errNotSupported = errors.New("not supported")

// Read fills a reading for one device.
//
// includeProcs is the expensive part (16.9 ms across 8 devices versus 2.1 ms for
// everything else), so the caller polls it on a slower cadence than the gauges.
func (c *Client) Read(d Device, includeProcs bool) Reading {
	var r Reading
	var util [2]uint32
	if c.getUtil(d.handle, &util) == 0 {
		r.Util = float64(util[0])
		r.UtilKnown = true
	}
	r.readMem(c.getMemV2, c.getMem, d.handle)
	// nvmlDeviceGetTemperature writes an unsigned int; the error code, not a -1
	// sentinel, is what reports "unknown".
	if p := c.u32Ptr(func(v *uint32) int32 { return c.getTemp(d.handle, tempGpu, v) }); p != nil {
		r.Temp = p
	}
	// nvmlDeviceGetPowerUsage and GetEnforcedPowerLimit report MILLIWATTS. Treating
	// them as watts made a 27 W idle card read as 27791 W, which is exactly the kind
	// of unit error that silently ruins every power figure on the screen.
	if p := c.u32Ptr(func(v *uint32) int32 { return c.getPower(d.handle, v) }); p != nil {
		w := *p / 1000
		r.Power = &w
	}
	if p := c.u32Ptr(func(v *uint32) int32 { return c.getPowerLimit(d.handle, v) }); p != nil {
		w := *p / 1000
		r.PowerCap = &w
	}
	r.ClockSM = c.u32Ptr(func(v *uint32) int32 { return c.getClock(d.handle, clockSM, v) })
	r.ClockMem = c.u32Ptr(func(v *uint32) int32 { return c.getClock(d.handle, clockMem, v) })
	r.Fan = c.u32Ptr(func(v *uint32) int32 { return c.getFan(d.handle, v) })
	r.PcieGen = c.u32Ptr(func(v *uint32) int32 { return c.getPcieGen(d.handle, v) })
	r.PcieWide = c.u32Ptr(func(v *uint32) int32 { return c.getPcieWidth(d.handle, v) })

	var throttle uint64
	if c.getThrottle(d.handle, &throttle) == 0 {
		r.Throttle = throttle
	}

	if includeProcs {
		// NVML reports the required count in `count` and returns
		// NVML_ERROR_INSUFFICIENT_SIZE when the array is too small. Discarding that
		// made a 100-process host look like it had none.
		var buf [procBufCap]procInfo
		count := uint32(len(buf))
		rc := c.getProcs(d.handle, &count, &buf[0])
		if rc == 0 {
			n := count
			if n > uint32(len(buf)) {
				n = uint32(len(buf))
			}
			for i := uint32(0); i < n; i++ {
				r.Procs = append(r.Procs, procInfo{PID: buf[i].PID, UsedGpuMem: buf[i].UsedGpuMem})
			}
			// A successful call with zero entries is genuinely "nothing running";
			// any other return code is an enumeration failure, which the caller must
			// be able to tell apart from an idle card.
			r.ProcsKnown = true
		}
	}
	return r
}

// Reading is one device's raw numbers, with nil meaning "the driver refused".
type Reading struct {
	// UtilKnown and MemKnown separate "the driver answered zero" from "the driver
	// did not answer". The plain values cannot: an unsupported or failing query
	// leaves 0, and 0 renders as an idle card. A GPU the monitor cannot read must
	// look unreadable, not idle.
	Util              float64
	UtilKnown         bool
	MemUsed, MemTotal uint64
	MemKnown          bool
	// MemReserved is what the driver and firmware keep for themselves, not counted
	// in MemUsed. ReservedKnown is false on the v1 fallback, whose MemUsed includes it.
	MemReserved           uint64
	ReservedKnown         bool
	Temp, Power, PowerCap *float64
	ClockSM, ClockMem     *float64
	Fan                   *float64
	PcieGen, PcieWide     *float64
	Throttle              uint64
	// ProcsKnown distinguishes "enumerated and empty" from "could not enumerate":
	// collapsing the two is how a permission failure renders as an idle card.
	Procs      []procInfo
	ProcsKnown bool
}

// readMem fills the memory fields, from v2 when the driver answers it and from v1
// otherwise.
//
// Any v2 failure falls back rather than leaving the card unread: NOT_SUPPORTED, a
// version mismatch or a missing symbol says nothing about the memory, and v1 still
// knows it. Only both failing makes the reading unknown — never zero.
func (r *Reading) readMem(v2 func(uintptr, *memInfoV2) int32, v1 func(uintptr, *memInfo) int32, h uintptr) {
	if v2 != nil {
		mi := memInfoV2{Version: memInfoV2Version}
		if v2(h, &mi) == 0 {
			r.MemUsed, r.MemTotal = toMiB(mi.Used), toMiB(mi.Total)
			r.MemReserved, r.ReservedKnown = toMiB(mi.Reserved), true
			r.MemKnown = true
			return
		}
	}
	var mi memInfo
	if v1(h, &mi) == 0 {
		r.MemUsed, r.MemTotal = toMiB(mi.Used), toMiB(mi.Total)
		r.MemKnown = true
	}
}

// toMiB rounds up to a whole MiB, as nvidia-smi does. Truncating read an idle 4090's
// 1.06 MiB as 1 and its 456.94 MiB reservation as 456, one off from the number a
// reader checks it against; rounding up also never shows an allocation as nothing.
func toMiB(b uint64) uint64 { return (b + 1<<20 - 1) >> 20 }

// u32Ptr calls a uint-returning getter and converts to a float pointer.
func (c *Client) u32Ptr(fn func(*uint32) int32) *float64 {
	var v uint32
	if rc := fn(&v); rc != 0 {
		return nil
	}
	f := float64(v)
	return &f
}

// PidOf exposes a procInfo's pid.
func (p procInfo) PidOf() int { return int(p.PID) }

// MemOf exposes a procInfo's VRAM in bytes.
func (p procInfo) MemOf() uint64 { return p.UsedGpuMem }

// cstr trims a NUL-terminated C buffer.
func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

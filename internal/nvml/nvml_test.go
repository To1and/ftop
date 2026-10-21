package nvml

import (
	"errors"
	"strings"
	"testing"
)

// TestMissingLibraryIsNoDriver: a machine without libnvidia-ml is a CPU-only host,
// and the probe tells that apart from a broken driver by this error. On a machine
// that does have the library the test has nothing to check.
func TestMissingLibraryIsNoDriver(t *testing.T) {
	c := &Client{}
	_, err := c.Devices()
	if err == nil {
		t.Skip("libnvidia-ml is present here")
	}
	if strings.Contains(err.Error(), "libnvidia-ml not found") && !errors.Is(err, ErrNoDriver) {
		t.Errorf("a missing library is not ErrNoDriver: %v", err)
	}
}

// An idle 4090 under driver 565, as nvidia-smi reports it: total 24564, reserved
// 457, used 2, free 24106 MiB. v1 answers used = total - free = 458.
const (
	mib        = 1 << 20
	idleTotal  = 24564 * mib
	idleFree   = 24106 * mib
	idleUsed   = 1114112   // measured: 1.06 MiB, which nvidia-smi shows as 2
	idleReserv = 479133696 // measured: 456.94 MiB, shown as 457
)

func v1Idle(_ uintptr, m *memInfo) int32 {
	*m = memInfo{Total: idleTotal, Free: idleFree, Used: idleTotal - idleFree}
	return 0
}

// TestMemUsedIsTheDriversNotTotalMinusFree: v2's used is the reading, and the
// reservation travels separately.
func TestMemUsedIsTheDriversNotTotalMinusFree(t *testing.T) {
	var gotVersion uint32
	v2 := func(_ uintptr, m *memInfoV2) int32 {
		gotVersion = m.Version
		m.Total, m.Reserved, m.Free, m.Used = idleTotal, idleReserv, idleFree, idleUsed
		return 0
	}
	var r Reading
	r.readMem(v2, v1Idle, 0)
	if gotVersion != memInfoV2Version {
		t.Errorf("v2 called with version %#x, want %#x", gotVersion, memInfoV2Version)
	}
	if !r.MemKnown || r.MemUsed != 2 || r.MemTotal != 24564 {
		t.Errorf("used/total = %d/%d known=%v, want 2/24564", r.MemUsed, r.MemTotal, r.MemKnown)
	}
	if !r.ReservedKnown || r.MemReserved != 457 {
		t.Errorf("reserved = %d known=%v, want 457", r.MemReserved, r.ReservedKnown)
	}
}

// TestMemFallsBackToV1: a driver without v2, or one that refuses it, still has a
// memory reading — v1's, with the reservation unknown rather than zero.
func TestMemFallsBackToV1(t *testing.T) {
	refuse := func(_ uintptr, m *memInfoV2) int32 { return 3 } // NVML_ERROR_NOT_SUPPORTED
	for name, v2 := range map[string]func(uintptr, *memInfoV2) int32{
		"missing symbol": nil,
		"not supported":  refuse,
	} {
		var r Reading
		r.readMem(v2, v1Idle, 0)
		if !r.MemKnown || r.MemUsed != 458 || r.MemTotal != 24564 {
			t.Errorf("%s: used/total = %d/%d known=%v, want v1's 458/24564",
				name, r.MemUsed, r.MemTotal, r.MemKnown)
		}
		if r.ReservedKnown {
			t.Errorf("%s: reservation reported known on the v1 fallback", name)
		}
	}
}

// TestMemUnknownWhenBothFail: no answer is unknown, not an empty card.
func TestMemUnknownWhenBothFail(t *testing.T) {
	var r Reading
	r.readMem(func(uintptr, *memInfoV2) int32 { return 999 },
		func(uintptr, *memInfo) int32 { return 999 }, 0)
	if r.MemKnown || r.ReservedKnown {
		t.Errorf("a refused reading is marked known: %+v", r)
	}
}

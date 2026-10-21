package nvml

import (
	"testing"
	"unsafe"
)

// TestProcInfoLayout pins the struct size against the symbol it is bound to.
//
// nvmlDeviceGetComputeRunningProcesses (unversioned) writes nvmlProcessInfo_v1_t,
// 16 bytes on amd64. nvmlProcessInfo_v2_t adds gpuInstanceId and computeInstanceId
// and is 24 bytes. Binding the unversioned symbol to a 24-byte struct makes every
// entry after the first decode from the wrong offset: plausible-looking PIDs and
// memory figures that the driver never reported.
func TestProcInfoLayout(t *testing.T) {
	if got := unsafe.Sizeof(procInfo{}); got != 24 {
		t.Fatalf("procInfo is %d bytes, want 24 (v2)", got)
	}
	// Field offsets are what actually matter for decoding.
	var p procInfo
	base := uintptr(unsafe.Pointer(&p))
	if off := uintptr(unsafe.Pointer(&p.PID)) - base; off != 0 {
		t.Errorf("PID at offset %d, want 0", off)
	}
	if off := uintptr(unsafe.Pointer(&p.UsedGpuMem)) - base; off != 8 {
		t.Errorf("UsedGpuMem at offset %d, want 8 (C aligns the u64 after the u32)", off)
	}
}

// TestClockSelectors pins the nvmlClockType_t values.
//
// 0/1 labelled graphics as SM and SM as memory. That is the worst kind of unit
// error: the numbers look completely normal on a quiet card.
func TestClockSelectors(t *testing.T) {
	if clockSM != 1 {
		t.Errorf("clockSM = %d, want 1 (NVML_CLOCK_SM)", clockSM)
	}
	if clockMem != 2 {
		t.Errorf("clockMem = %d, want 2 (NVML_CLOCK_MEM)", clockMem)
	}
}

// TestProcBufferCapacity covers the fixed-array limit: more than 64 processes used
// to be reported as "no processes" because the size error was discarded.
func TestProcBufferCapacity(t *testing.T) {
	if procBufCap < 128 {
		t.Errorf("process buffer holds %d entries; a busy multi-tenant host routinely exceeds 64", procBufCap)
	}
}

// TestMemInfoV2Layout pins nvmlMemory_v2_t: a u32 version, four bytes of padding,
// then total, reserved, free and used. A field at the wrong offset reads the
// reservation as used memory, which is the bug v2 is here to fix.
func TestMemInfoV2Layout(t *testing.T) {
	if got := unsafe.Sizeof(memInfoV2{}); got != 40 {
		t.Fatalf("memInfoV2 is %d bytes, want 40", got)
	}
	if memInfoV2Version != 0x02000028 {
		t.Errorf("version = %#x, want 0x02000028 (nvmlMemory_v2)", memInfoV2Version)
	}
	var m memInfoV2
	base := uintptr(unsafe.Pointer(&m))
	for _, f := range []struct {
		name string
		p    unsafe.Pointer
		want uintptr
	}{
		{"Version", unsafe.Pointer(&m.Version), 0},
		{"Total", unsafe.Pointer(&m.Total), 8},
		{"Reserved", unsafe.Pointer(&m.Reserved), 16},
		{"Free", unsafe.Pointer(&m.Free), 24},
		{"Used", unsafe.Pointer(&m.Used), 32},
	} {
		if off := uintptr(f.p) - base; off != f.want {
			t.Errorf("%s at offset %d, want %d", f.name, off, f.want)
		}
	}
}

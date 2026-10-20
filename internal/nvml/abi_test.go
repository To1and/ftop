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

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

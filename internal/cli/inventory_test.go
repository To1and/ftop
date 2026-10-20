package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCPUOnlyCache(t *testing.T) {
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	now := time.Now()

	if got := readCPUOnly(ledgerPath, now); got != nil {
		t.Fatalf("no file: want nil (ask everyone), got %v", got)
	}

	writeCPUOnly(ledgerPath, []string{"vps1", "laptop"}, now)
	got := readCPUOnly(ledgerPath, now.Add(time.Minute))
	if !got["vps1"] || !got["laptop"] || len(got) != 2 {
		t.Fatalf("fresh pass: got %v", got)
	}

	// An expired pass means "ask everyone" again, so a card added since shows up.
	if got := readCPUOnly(ledgerPath, now.Add(cpuOnlyTTL+time.Second)); got != nil {
		t.Fatalf("expired pass: want nil, got %v", got)
	}
	// A pass stamped in the future (clock moved back) is not trusted.
	if got := readCPUOnly(ledgerPath, now.Add(-time.Minute)); got != nil {
		t.Fatalf("future pass: want nil, got %v", got)
	}

	// A full pass where every host had cards is still a pass: an empty, non-nil set.
	writeCPUOnly(ledgerPath, nil, now)
	if got := readCPUOnly(ledgerPath, now); got == nil || len(got) != 0 {
		t.Fatalf("empty pass: want empty non-nil set, got %v", got)
	}

	// A corrupt file is ignored, not an error.
	if err := os.WriteFile(cpuOnlyPath(ledgerPath), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readCPUOnly(ledgerPath, now); got != nil {
		t.Fatalf("corrupt file: want nil, got %v", got)
	}

	// No temp files are left behind next to the ledger.
	entries, _ := os.ReadDir(filepath.Dir(ledgerPath))
	for _, e := range entries {
		if e.Name() != "cpu-only-hosts.json" {
			t.Errorf("unexpected file %s", e.Name())
		}
	}
}

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// cpuOnlyTTL is how long a full pass's "this host has no cards" stays good enough to
// skip the host on a question about cards.
//
// Long enough that an agent claiming in a loop pays for the slow machines once, short
// enough that a card added to a machine shows up within minutes. Skipping can only
// leave a card out of an answer, never put a busy one in, so a stale entry costs a
// missed card, not a collision.
const cpuOnlyTTL = 10 * time.Minute

// cpuOnlyFile sits next to the ledger. It is a cache, not state: deleting it only
// makes the next card query observe every host.
type cpuOnlyFile struct {
	At    time.Time `json:"at"`
	Hosts []string  `json:"hosts"`
}

func cpuOnlyPath(ledgerPath string) string {
	return filepath.Join(filepath.Dir(ledgerPath), "cpu-only-hosts.json")
}

// readCPUOnly returns the hosts a recent full pass reached and found no cards on, or
// nil when there is no recent pass — nil means "ask everyone".
func readCPUOnly(ledgerPath string, now time.Time) map[string]bool {
	b, err := os.ReadFile(cpuOnlyPath(ledgerPath))
	if err != nil {
		return nil
	}
	var f cpuOnlyFile
	if json.Unmarshal(b, &f) != nil || now.Sub(f.At) > cpuOnlyTTL || f.At.After(now) {
		return nil
	}
	out := make(map[string]bool, len(f.Hosts))
	for _, h := range f.Hosts {
		out[h] = true
	}
	return out
}

// writeCPUOnly records a full pass. Failure is ignored: the cache only saves time.
func writeCPUOnly(ledgerPath string, hosts []string, now time.Time) {
	if hosts == nil {
		hosts = []string{}
	}
	b, err := json.Marshal(cpuOnlyFile{At: now.UTC(), Hosts: hosts})
	if err != nil {
		return
	}
	// A private temp file and a rename, so two agents finishing a pass at once never
	// leave half of one file and half of the other.
	p := cpuOnlyPath(ledgerPath)
	f, err := os.CreateTemp(filepath.Dir(p), ".cpu-only-*")
	if err != nil {
		return
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr != nil || cerr != nil || os.Rename(f.Name(), p) != nil {
		_ = os.Remove(f.Name())
	}
}

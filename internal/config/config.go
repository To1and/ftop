// Package config discovers hosts from ~/.ssh/config plus a small TOML override.
//
// Every concrete Host in the ssh configuration is a machine ftop manages. There
// is no built-in list of names to keep or leave out: which machines belong to the
// fleet is the user's decision, expressed in their own ssh_config and in the
// `drop` and `[[host]]` keys of config.toml, and the program ships with no
// knowledge of anyone's machines.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Host is one machine to monitor.
type Host struct {
	Name     string // ssh alias
	Label    string // display name
	Hostname string
	Port     string
	// Aliases are the stanza's other names (`Host a b c` -> b, c); `drop`
	// patterns match them too.
	Aliases []string
	Tags    []string
	// Manual marks a host automatic placement never picks: it is monitored and may
	// be claimed by name (`claim -host`), but a claim that names no host does not
	// land on it. Set by the `manual` patterns in config.toml's [claim] section.
	Manual bool

	// override is a [[host]] block's hostname, which replaces ssh_config's.
	override string
}

// SSHOptions are the -o options a connection to h needs beyond the user's own
// ssh_config: only a [[host]] hostname override, which ssh cannot know about.
func (h Host) SSHOptions() []string {
	if h.override != "" {
		return []string{"HostName=" + h.override}
	}
	return nil
}

// Config is the resolved fleet.
type Config struct {
	Hosts    []Host
	Interval float64
	Columns  int
	// SSHConfig is the ssh_config ftop reads hosts from and passes to ssh with -F;
	// empty means ssh's own default, ~/.ssh/config.
	SSHConfig string
	// ShowCPUOnly and ShowUnsupported start the board with the hosts it otherwise
	// leaves out: machines with no NVIDIA GPU, and machines the probe cannot run
	// on. Both are still monitored either way; this is what the board draws, and
	// the settings panel flips it for the session.
	ShowCPUOnly, ShowUnsupported bool

	// resolved records whether a config file actually applied an override. It is
	// what distinguishes "no file, use discovery" from "the file dropped every
	// host", and the two must not collapse: treating an intentional empty
	// selection as an absent one silently reconnects the whole fleet.
	resolved bool

	// manual is the [claim] manual patterns. They are matched once the fleet is final
	// — merged, deduplicated and labelled — because a [[host]] block that overrides a
	// discovered host replaces it, and a flag set on the discovered copy was lost.
	manual []string

	// err is a parse or schema failure recorded by applyTOML. Load returns it
	// instead of proceeding: a config file ftop cannot read exactly is not a
	// config file it may guess at.
	err error
}

// dedupe merges entries for the same ssh alias. A host both discovered and
// listed in a [[host]] block was monitored twice, as two cards. The explicit
// entry wins; what it leaves unset is taken from discovery.
func dedupe(hosts []Host) []Host {
	at := map[string]int{}
	out := make([]Host, 0, len(hosts))
	for _, h := range hosts {
		i, seen := at[h.Name]
		if !seen {
			at[h.Name] = len(out)
			out = append(out, h)
			continue
		}
		prev := out[i]
		if h.Label == "" {
			h.Label = prev.Label
		}
		if h.Hostname == "" {
			h.Hostname = prev.Hostname
		}
		if h.Port == "" {
			h.Port = prev.Port
		}
		h.Aliases = append(append([]string(nil), prev.Aliases...), h.Aliases...)
		out[i] = h
	}
	return out
}

// LoadSettings reads only the presentation settings (interval, columns)
// from the config file. It never reads ssh_config: `-demo` uses it, and a
// synthetic fleet must not depend on, or reveal, the real one.
//
// A parse failure is kept on the result rather than returned: the caller decides
// whether a broken file matters, and in demo mode it still should be reported.
func LoadSettings(path string) *Config {
	cfg := &Config{Interval: 1.0}
	if path == "" {
		path = defaultConfigPath()
	}
	if body, err := os.ReadFile(path); err == nil && path != "" {
		applyTOML(cfg, nil, string(body))
	}
	cfg.Hosts, cfg.resolved = nil, false
	return cfg
}

// Err reports a config file that could not be decoded.
func (c *Config) Err() error { return c.err }

// Load resolves the fleet from ssh_config and an optional TOML file.
func Load(path string) (*Config, error) {
	if path == "" {
		path = defaultConfigPath()
	}
	var body string
	hasFile := false
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			body, hasFile = string(b), true
		}
	}
	// The TOML may name the ssh_config to read, so its settings come first.
	cfg := &Config{Interval: 1.0}
	if hasFile {
		applyTOML(cfg, nil, body)
		if cfg.err != nil {
			return nil, cfg.err
		}
	}
	sshPath := cfg.SSHConfig
	if sshPath == "" {
		sshPath = sshConfigPath()
	}
	sc, err := readSSHConfig(sshPath)
	if err != nil {
		return nil, err
	}

	cfg = &Config{Interval: 1.0}
	// Every machine the ssh configuration names, without exception. Leaving a
	// machine out is the user's call, made with `drop` in config.toml.
	included := sc.discover()
	// The override file may add hosts, drop hosts, relabel them, or change the
	// interval. Anything it says wins; its absence is not an error.
	if hasFile {
		applyTOML(cfg, included, body)
		if cfg.err != nil {
			return nil, cfg.err
		}
	}
	if len(cfg.Hosts) == 0 && !cfg.resolved {
		cfg.Hosts = included
	}
	cfg.Hosts = dedupe(cfg.Hosts)
	for _, h := range cfg.Hosts {
		if err := validName(h.Name); err != nil {
			return nil, err
		}
	}
	sort.Slice(cfg.Hosts, func(i, j int) bool { return cfg.Hosts[i].Name < cfg.Hosts[j].Name })
	names := make([]string, len(cfg.Hosts))
	for i, h := range cfg.Hosts {
		names[i] = h.Name
	}
	prefix := FleetPrefix(names)
	for i := range cfg.Hosts {
		if cfg.Hosts[i].Label == "" {
			cfg.Hosts[i].Label = labelFor(cfg.Hosts[i].Name, prefix)
		}
		cfg.Hosts[i].Manual = matchAnyHost(cfg.Hosts[i], cfg.manual)
	}
	return cfg, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// sshConfigFile overrides the ssh_config used. Empty means ~/.ssh/config. Tests
// point it at an isolated file.
var sshConfigFile string

func sshConfigPath() string {
	if sshConfigFile != "" {
		return sshConfigFile
	}
	return defaultSSHConfig()
}

func defaultSSHConfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}

func defaultConfigPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "ftop", "config.toml")
}

// FleetPrefix finds the naming prefix most of the fleet shares, such as "lab-"
// in lab-gpu-1 … lab-gpu-9.
//
// Every card repeating it spends a third of its title on the one part that does
// not tell machines apart. Only a prefix ending at a '-' counts, so a name is
// never cut mid-word, and it must cover at least half the fleet (and two hosts):
// a prefix two machines out of fourteen happen to share is not the fleet's.
func FleetPrefix(names []string) string {
	count := map[string]int{}
	for _, n := range names {
		for i := 0; i < len(n)-1; i++ {
			if n[i] == '-' {
				count[n[:i+1]]++
			}
		}
	}
	best := ""
	for p, c := range count {
		if c >= 2 && 2*c >= len(names) && len(p) > len(best) {
			best = p
		}
	}
	return best
}

// ResolveHost returns the ssh alias a caller meant when it named a machine.
//
// A person reading the board sees the LABEL ("gpu-4"); the ledger and ssh use the
// ALIAS ("lab-gpu-4"). Accepting only one of them made `-host gpu-4` answer with an
// empty list and exit 0 — a success that looks exactly like "no cards there",
// which is the worst possible answer for a ledger. Labels are unique within a config
// (dedupe keeps the fleet addressable), so the mapping is unambiguous; if a config
// ever produced a collision, the alias still wins and the label lookup refuses.
//
// The returned boolean says whether the name was found; callers that need an alias
// (to ssh) must check it, callers that only filter a listing can use it as-is.
//
// path is the config.toml to read, as for Load; the fleet it describes is the one
// names are resolved in.
func ResolveHost(path, name string) (alias string, found, ambiguous bool) {
	if name == "" {
		return "", false, false
	}
	cfg, err := Load(path)
	if err != nil {
		return name, false, false
	}
	var byLabel []string
	for _, h := range cfg.Hosts {
		if strings.EqualFold(h.Name, name) {
			return h.Name, true, false
		}
		for _, a := range h.Aliases {
			if strings.EqualFold(a, name) {
				return h.Name, true, false
			}
		}
		if strings.EqualFold(h.Label, name) {
			byLabel = append(byLabel, h.Name)
		}
	}
	switch len(byLabel) {
	case 1:
		return byLabel[0], true, false
	case 0:
		return name, false, false
	}
	return name, false, true
}

// labelFor is the display name for an ssh alias: the fleet's shared prefix
// removed, when there is one.
func labelFor(name, prefix string) string {
	if prefix != "" && strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
		return strings.TrimPrefix(name, prefix)
	}
	return name
}

// validName rejects a destination ssh cannot be given: a name with whitespace or
// a quote would be split or reinterpreted. (A leading '-' is safe: connections put
// "--" before the destination.) ssh_config cannot produce such a name; a
// [[host]] block can, and a typo there should be an error, not a silent skip.
func validName(name string) error {
	if name == "" || strings.ContainsAny(name, " \t\r\n\"'") {
		return fmt.Errorf("config: host name %q is not a valid ssh destination", name)
	}
	return nil
}

// fileConfig is the decoded config.toml.
//
// Decoded by a real TOML parser, not by hand. The hand-rolled reader this replaces
// had two defects that both flipped safety-relevant meaning: an unknown section
// after [[host]] wrote its keys onto the last host, and `v` kept any inline
// comment, so `drop = ["*"]   # off` matched nothing and the whole fleet came back.
// Eight flat keys are not worth a parser that gets quoting, comments and sections
// wrong.
type fileConfig struct {
	General struct {
		Interval  float64  `toml:"interval"`
		Columns   int      `toml:"columns"`
		Drop      []string `toml:"drop"`
		SSHConfig string   `toml:"ssh_config"`
		// The board's two host filters. A key of their own rather than an
		// entry in drop: a dropped host is never contacted, while these are
		// monitored and only left off the board.
		ShowCPUOnly     bool `toml:"show_cpu_only"`
		ShowUnsupported bool `toml:"show_unsupported"`
	} `toml:"general"`
	Labels map[string]string `toml:"labels"`
	// Claim holds what the agent-facing ledger needs from the user. Manual names
	// the hosts placement must not choose by itself — a workstation, or a machine
	// without the shared filesystem the rest of the fleet has. Patterns match like
	// drop's.
	Claim struct {
		Manual []string `toml:"manual"`
	} `toml:"claim"`
	Host []struct {
		Name     string `toml:"name"`
		Label    string `toml:"label"`
		Hostname string `toml:"hostname"`
	} `toml:"host"`
}

// applyTOML applies the handful of keys ftop supports.
//
// Every unknown key aborts the load rather than being ignored. A typo in `drop` is
// the difference between an empty fleet and the whole one, and silently ignoring
// it means connecting to machines the user asked to leave alone.
func applyTOML(cfg *Config, discovered []Host, body string) {
	var fc fileConfig
	md, err := toml.Decode(body, &fc)
	if err != nil {
		cfg.err = fmt.Errorf("config: %w", err)
		return
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		cfg.err = fmt.Errorf("config: unknown key(s): %s", strings.Join(keys, ", "))
		return
	}

	if fc.General.Interval > 0 {
		cfg.Interval = fc.General.Interval
	}
	if fc.General.Columns > 0 {
		cfg.Columns = fc.General.Columns
	}
	cfg.SSHConfig = expandHome(fc.General.SSHConfig)
	cfg.manual = fc.Claim.Manual
	cfg.ShowCPUOnly, cfg.ShowUnsupported = fc.General.ShowCPUOnly, fc.General.ShowUnsupported

	extras := make([]Host, 0, len(fc.Host))
	for _, h := range fc.Host {
		extras = append(extras, Host{
			Name: h.Name, Label: h.Label,
			Hostname: h.Hostname, override: h.Hostname,
		})
	}

	kept := make([]Host, 0, len(discovered))
	for _, h := range discovered {
		if matchAnyHost(h, fc.General.Drop) {
			continue
		}
		if l, ok := fc.Labels[h.Name]; ok {
			h.Label = l
		}
		kept = append(kept, h)
	}
	// Explicit hosts go through the same drop list: `drop = ["*"]` empties the
	// fleet, including anything a [[host]] block added.
	for _, h := range extras {
		if h.Name == "" || matchAnyHost(h, fc.General.Drop) {
			continue
		}
		if l, ok := fc.Labels[h.Name]; ok && h.Label == "" {
			h.Label = l
		}
		kept = append(kept, h)
	}
	// An empty `kept` is a decision, not a failure: `drop = ["*"]` must leave the
	// fleet empty. Assigning it unconditionally and letting Load fall back to the
	// discovered list is how a deliberate exclusion turns into a full reconnect.
	cfg.Hosts = kept
	cfg.resolved = true
}

// matchAnyHost matches a host by everything it is known as: alias, label,
// address, the machine part of an ssh:// destination, and the stanza's other
// aliases.
func matchAnyHost(h Host, patterns []string) bool {
	cands := append([]string{h.Name, h.Label, h.Hostname, destHost(h.Name)}, h.Aliases...)
	for _, cand := range cands {
		if cand != "" && globMatchAny(patterns, cand) {
			return true
		}
	}
	return false
}

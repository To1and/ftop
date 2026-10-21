package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain points every test at an empty ssh_config, so no test reads, depends on,
// or reveals the configuration of the machine it runs on.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ftop-config-test")
	if err != nil {
		panic(err)
	}
	empty := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		panic(err)
	}
	sshConfigFile = empty
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// writeConfig drops a config file in a temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeTemp writes an ssh_config and returns its path.
func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ssh_config")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// isolated points ftop at an ssh_config with the given body and returns a
// config.toml path with the given body.
func isolated(t *testing.T, sshConfig, toml string) string {
	t.Helper()
	sc := writeTemp(t, sshConfig)
	prev := sshConfigFile
	sshConfigFile = sc
	t.Cleanup(func() { sshConfigFile = prev })
	return writeConfig(t, toml)
}

func names(hs []Host) string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Name)
	}
	return strings.Join(out, ",")
}

func load(t *testing.T, toml string) *Config {
	t.Helper()
	cfg, err := Load(toml)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestEveryHostIsManaged: every concrete Host in the ssh configuration is in the
// fleet. There is no built-in list of names to keep or leave out — a host called
// "laptop", "bastion" or anything else is managed like the rest, and leaving one
// out is the user's decision, made with `drop`.
func TestEveryHostIsManaged(t *testing.T) {
	ssh := `
Host gpu-a gpu-a-alias
    HostName 192.0.2.1
Host bastion
    HostName 192.0.2.2
Host laptop
    HostName 127.0.0.1
    Port 2216
Host prod-1 reserved-1 production
    HostName 192.0.2.3
Host *
    User everyone
Host gpu-*
    IdentitiesOnly yes
`
	cfg := load(t, isolated(t, ssh, ""))
	if got := names(cfg.Hosts); got != "bastion,gpu-a,laptop,prod-1" {
		t.Errorf("fleet = %q, want every concrete host and no wildcard stanza", got)
	}
}

// TestProxiedHostsAreManaged: a host reached through ProxyJump or ProxyCommand is
// a host like any other. ftop connects with `ssh <alias>`, so ssh applies the proxy.
func TestProxiedHostsAreManaged(t *testing.T) {
	ssh := "Host jump\n  HostName 192.0.2.9\nHost inner\n  HostName 192.0.2.5\n  ProxyJump jump\n" +
		"Host piped\n  ProxyCommand nc %h %p\n"
	cfg := load(t, isolated(t, ssh, ""))
	if got := names(cfg.Hosts); got != "inner,jump,piped" {
		t.Errorf("fleet = %q", got)
	}
}

// TestMatchAndUnknownKeywordsDoNotRefuse: a configuration OpenSSH accepts must not
// stop ftop from starting. Match blocks name conditions, not machines, and are
// left to ssh; a keyword ftop does not know is ignored.
func TestMatchAndUnknownKeywordsDoNotRefuse(t *testing.T) {
	ssh := `
Match host gpu-1 exec "true"
    User matched
Host gpu-1
    HostName 192.0.2.10
    SomeFutureOption yes
CanonicalizeHostname yes
`
	cfg := load(t, isolated(t, ssh, ""))
	if got := names(cfg.Hosts); got != "gpu-1" {
		t.Errorf("fleet = %q", got)
	}
	if cfg.Hosts[0].Hostname != "192.0.2.10" {
		t.Errorf("hostname = %q", cfg.Hosts[0].Hostname)
	}
}

// TestIncludeIsFollowed: hosts in included files are discovered, relative paths
// resolve against ~/.ssh, and globs expand in lexical order.
func TestIncludeIsFollowed(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"a.conf": "Host inc-a\n  HostName 192.0.2.21\n",
		"b.conf": "Host inc-b\n  HostName 192.0.2.22\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ssh := "Include " + filepath.Join(dir, "*.conf") + "\nHost top\n  HostName 192.0.2.20\n"
	cfg := load(t, isolated(t, ssh, ""))
	if got := names(cfg.Hosts); got != "inc-a,inc-b,top" {
		t.Errorf("fleet = %q", got)
	}
}

// TestIncludeLoopIsBounded: a file that includes itself is an error, not a hang.
func TestIncludeLoopIsBounded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "loop")
	if err := os.WriteFile(p, []byte("Include "+p+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSSHConfig(p); err == nil {
		t.Error("a self-including file must be refused")
	}
}

// TestExplicitEmptySelectionStaysEmpty: dropping every host must leave nothing to
// connect to. Falling back to the discovered list would turn "leave these alone"
// into "connect to all of them".
func TestExplicitEmptySelectionStaysEmpty(t *testing.T) {
	cfg := load(t, isolated(t, "Host gpu-1\n  HostName 192.0.2.9\n", "[general]\ndrop = [\"*\"]\n"))
	if len(cfg.Hosts) != 0 {
		t.Errorf("drop=[*] left %d hosts; it must leave none", len(cfg.Hosts))
	}
	if !cfg.resolved {
		t.Error("an applied config file must be recorded as resolved")
	}
}

// TestDropSurvivesInlineComment: `drop = ["*"]   # off` must still empty the fleet.
// A hand-rolled reader once kept the comment as part of the pattern.
func TestDropSurvivesInlineComment(t *testing.T) {
	cfg := load(t, isolated(t, "Host gpu-1\n", "[general]\ndrop = [\"*\"]   # keep everything off\n"))
	if len(cfg.Hosts) != 0 {
		t.Errorf("drop=[*] with an inline comment left %d hosts", len(cfg.Hosts))
	}
}

// TestDropAppliesToExplicitHosts: `drop` empties [[host]] additions too.
func TestDropAppliesToExplicitHosts(t *testing.T) {
	cfg := load(t, isolated(t, "", "[general]\ndrop = [\"*\"]\n\n[[host]]\nname = \"lab-gpu\"\n"))
	if len(cfg.Hosts) != 0 {
		t.Errorf("drop=[*] with a [[host]] block left %d hosts", len(cfg.Hosts))
	}
}

// TestUnknownSectionCannotRewriteAHost: a section ftop does not know must be
// refused, not read as host fields.
func TestUnknownSectionCannotRewriteAHost(t *testing.T) {
	p := writeConfig(t, "[[host]]\nname = \"lab-gpu\"\n\n[ledger]\nhostname = \"192.0.2.80\"\n")
	if _, err := Load(p); err == nil {
		t.Fatal("an unknown section must be refused, not silently reinterpreted")
	}
}

// TestUnknownKeyIsRefused: a typo in `drop` must not be silently ignored.
func TestUnknownKeyIsRefused(t *testing.T) {
	p := writeConfig(t, "[general]\ndrop = [\"*\"]\ndropp = [\"x\"]\n")
	if _, err := Load(p); err == nil {
		t.Fatal("an unknown key must be refused")
	}
}

// TestNoConfigFileUsesDiscovery: no config.toml means "every discovered host".
func TestNoConfigFileUsesDiscovery(t *testing.T) {
	prev := sshConfigFile
	sshConfigFile = writeTemp(t, "Host gpu-1\nHost gpu-2\n")
	defer func() { sshConfigFile = prev }()
	cfg := load(t, filepath.Join(t.TempDir(), "absent.toml"))
	if cfg.resolved {
		t.Error("an absent config file must not count as an override")
	}
	if got := names(cfg.Hosts); got != "gpu-1,gpu-2" {
		t.Errorf("fleet = %q", got)
	}
	if cfg.Interval <= 0 {
		t.Errorf("interval = %v, want a positive default", cfg.Interval)
	}
}

// TestDropMatchesLabelAndHostname: a user drops what they SEE on the card.
func TestDropMatchesLabelAndHostname(t *testing.T) {
	h := Host{Name: "lab-node-1", Label: "node-1", Hostname: "192.0.2.70", Aliases: []string{"n1"}}
	for _, pat := range []string{"node-1", "192.0.2.70", "lab-node-*", "n1"} {
		if !matchAnyHost(h, []string{pat}) {
			t.Errorf("pattern %q did not match the host", pat)
		}
	}
}

// TestHostnameOverrideReachesSSH: a [[host]] hostname is the one thing ssh cannot
// learn from its own configuration, so it is passed on the command line.
func TestHostnameOverrideReachesSSH(t *testing.T) {
	cfg := load(t, isolated(t, "Host gpu-1\n  HostName 192.0.2.1\n",
		"[[host]]\nname = \"gpu-1\"\nhostname = \"192.0.2.99\"\n"))
	if len(cfg.Hosts) != 1 {
		t.Fatalf("fleet = %q", names(cfg.Hosts))
	}
	opts := cfg.Hosts[0].SSHOptions()
	if len(opts) != 1 || opts[0] != "HostName=192.0.2.99" {
		t.Errorf("options = %q", opts)
	}
	plain := load(t, isolated(t, "Host gpu-2\n", ""))
	if o := plain.Hosts[0].SSHOptions(); len(o) != 0 {
		t.Errorf("a host without an override got options %q", o)
	}
}

// TestInvalidExplicitNameIsAnError: a [[host]] name ssh cannot be given is a
// config error, not a silently skipped machine.
func TestInvalidExplicitNameIsAnError(t *testing.T) {
	p := isolated(t, "", "[[host]]\nname = \"two words\"\n")
	if _, err := Load(p); err == nil {
		t.Error("a host name with a space must be refused")
	}
}

// TestFleetPrefix: the shared prefix is stripped from labels, a prefix only a
// minority shares is not, and names are never cut mid-word.
func TestFleetPrefix(t *testing.T) {
	ns := []string{"lab-gpu-1", "lab-gpu-2", "lab-big-1", "lab-big-3", "workstation"}
	if got := FleetPrefix(ns); got != "lab-" {
		t.Errorf("FleetPrefix = %q, want lab-", got)
	}
	if got := labelFor("lab-big-3", "lab-"); got != "big-3" {
		t.Errorf("label = %q", got)
	}
	if got := labelFor("workstation", "lab-"); got != "workstation" {
		t.Errorf("a host outside the prefix lost part of its name: %q", got)
	}
	if got := FleetPrefix([]string{"a-1", "b-2", "c-3", "a-4"}); got != "a-" {
		t.Errorf("FleetPrefix = %q, want a-", got)
	}
	if got := FleetPrefix([]string{"a-1", "b-2", "c-3", "d-4", "a-5"}); got != "" {
		t.Errorf("a minority prefix was taken as the fleet's: %q", got)
	}
}

// TestResolveHost: a label and every stanza alias resolve to the alias, and an
// alias always wins over a label.
func TestResolveHost(t *testing.T) {
	// The fleet prefix is "lab-", so lab-gpu-1 shows as "gpu-1" and lab-cpu-2 as
	// "cpu-2" — which is also another host's alias.
	isolated(t, "Host lab-gpu-1 g1\nHost lab-cpu-2\nHost cpu-2\n", "")
	cases := []struct {
		in, want string
		found    bool
	}{
		{"lab-gpu-1", "lab-gpu-1", true},
		{"g1", "lab-gpu-1", true},
		{"gpu-1", "lab-gpu-1", true}, // the label the board shows
		{"cpu-2", "cpu-2", true},     // an alias wins over lab-cpu-2's label
		{"nowhere", "nowhere", false},
	}
	for _, c := range cases {
		got, found, _ := ResolveHost("", c.in)
		if got != c.want || found != c.found {
			t.Errorf("ResolveHost(%q) = %q, %v; want %q, %v", c.in, got, found, c.want, c.found)
		}
	}
}

// TestLoadSettingsReadsNoSSHConfig: demo mode reads presentation settings only.
func TestLoadSettingsReadsNoSSHConfig(t *testing.T) {
	prev := sshConfigFile
	sshConfigFile = "/nonexistent/dir/ssh_config"
	defer func() { sshConfigFile = prev }()
	p := writeConfig(t, "[general]\ncolumns = 3\n\n[[host]]\nname = \"x\"\n")
	cfg := LoadSettings(p)
	if cfg.Columns != 3 || len(cfg.Hosts) != 0 {
		t.Errorf("settings = %+v", cfg)
	}
}

// TestSSHConfigKeySelectsTheFile: the [general] ssh_config key selects the file,
// and the same path is what connections pass to ssh with -F.
func TestSSHConfigKeySelectsTheFile(t *testing.T) {
	alt := writeTemp(t, "Host gpu-x\n  HostName 192.0.2.77\n")
	cfg := load(t, isolated(t, "Host other\n", "[general]\nssh_config = \""+alt+"\"\n"))
	if names(cfg.Hosts) != "gpu-x" {
		t.Errorf("fleet = %q", names(cfg.Hosts))
	}
	if cfg.SSHConfig != alt {
		t.Errorf("SSHConfig = %q, want %q", cfg.SSHConfig, alt)
	}
}

// TestBoardFiltersDefaultToHidden: CPU-only and unsupported hosts are off the
// board unless the file asks for them, and asking for one does not ask for the
// other. Either way every host is still in the fleet: a filter is not a drop.
func TestBoardFiltersDefaultToHidden(t *testing.T) {
	ssh := "Host lab-gpu-1\nHost lab-cpu-1\n"
	cfg := load(t, isolated(t, ssh, "[general]\ncolumns = 2\n"))
	if cfg.ShowCPUOnly || cfg.ShowUnsupported {
		t.Errorf("filters off by default: cpu-only %v, unsupported %v", cfg.ShowCPUOnly, cfg.ShowUnsupported)
	}
	cfg = load(t, isolated(t, ssh, "[general]\nshow_cpu_only = true\n"))
	if !cfg.ShowCPUOnly || cfg.ShowUnsupported {
		t.Errorf("show_cpu_only alone: cpu-only %v, unsupported %v", cfg.ShowCPUOnly, cfg.ShowUnsupported)
	}
	if names(cfg.Hosts) != "lab-gpu-1,lab-cpu-1" && names(cfg.Hosts) != "lab-cpu-1,lab-gpu-1" {
		t.Errorf("a filter changed the fleet: %q", names(cfg.Hosts))
	}
	cfg = LoadSettings(writeConfig(t, "[general]\nshow_unsupported = true\n"))
	if !cfg.ShowUnsupported {
		t.Error("demo settings ignore show_unsupported")
	}
}

// TestManualHostsStayManaged: [claim] manual keeps a host out of automatic placement
// and nothing else — it is still in the fleet, monitored like every other.
func TestManualHostsStayManaged(t *testing.T) {
	cfg := load(t, isolated(t, "Host lab-gpu-1\nHost lab-gpu-2\nHost lab-desk\n  HostName 192.0.2.9\n",
		"[claim]\nmanual = [\"192.0.2.9\"]\n\n[[host]]\nname = \"lab-gpu-9\"\n"))
	if got := names(cfg.Hosts); got != "lab-desk,lab-gpu-1,lab-gpu-2,lab-gpu-9" {
		t.Fatalf("fleet = %q: a manual host must stay in it", got)
	}
	for _, h := range cfg.Hosts {
		if h.Manual != (h.Name == "lab-desk") {
			t.Errorf("%s: manual = %v", h.Name, h.Manual)
		}
	}
}

// TestManualSurvivesAHostOverride: a [[host]] block for a discovered host replaces
// it, and the manual flag set on the discovered copy used to go with it, putting the
// workstation back into automatic placement.
func TestManualSurvivesAHostOverride(t *testing.T) {
	cfg := load(t, isolated(t, "Host desk desk-alias\n  HostName 192.0.2.9\nHost lab-gpu-1\n",
		"[claim]\nmanual = [\"desk-alias\"]\n\n[[host]]\nname = \"desk\"\nlabel = \"my desk\"\n"))
	for _, h := range cfg.Hosts {
		if h.Manual != (h.Name == "desk") {
			t.Errorf("%s: manual = %v", h.Name, h.Manual)
		}
	}
}

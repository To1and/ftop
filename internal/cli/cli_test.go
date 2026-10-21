package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ftop/internal/ledger"
)

// TestMain gives every test an empty home directory, so no test reads, depends on
// or reveals the ssh_config, ftop config or installed skill of the machine it runs on.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "ftop-cli-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// run executes a command against a private ledger and returns its envelope and status.
func run(t *testing.T, store string, args ...string) (Envelope, int) {
	t.Helper()
	t.Setenv("FTOP_LEDGER", store)
	var buf bytes.Buffer
	io_ := IO{Out: &buf, Err: &buf}
	code := dispatch(io_, args)
	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("%v produced non-JSON output: %q (%v)", args, buf.String(), err)
	}
	return env, code
}

// dispatch mirrors Main without os.Exit and with the test's writers.
func dispatch(io_ IO, args []string) int {
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "gpu":
		return runGPU(io_, rest)
	case "claim":
		if len(rest) > 0 && rest[0] == "list" {
			return runClaimList(io_, rest[1:])
		}
		return runClaim(io_, rest)
	case "release":
		return runRelease(io_, rest)
	case "renew":
		return runRenew(io_, rest)
	case "history":
		return runHistory(io_, rest)
	case "host":
		return runHost(io_, rest)
	case "whoami":
		return runWhoami(io_, rest)
	case "skill":
		return runSkill(io_, rest)
	case "version":
		return runVersion(io_, rest)
	}
	return emitErr(io_.Out, cmd, usageErr("unknown subcommand %q", cmd))
}

func seedStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, err := ledger.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertCards([]ledger.Card{
		{Host: "h1", GPUUUID: "GPU-0", Index: 0, Name: "RTX 4090", MemTotal: 24564},
		{Host: "h1", GPUUUID: "GPU-1", Index: 1, Name: "RTX 4090", MemTotal: 24564},
	}); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestClaimThenConflictThenRelease is the loop an agent actually runs.
func TestClaimThenConflictThenRelease(t *testing.T) {
	store := seedStore(t)

	env, code := run(t, store, "claim", "-host", "h1", "-why", "train", "-for", "1h", "-as", "agent-a", "-no-ssh")
	if code != ExitOK {
		t.Fatalf("claim: code=%d err=%+v", code, env.Error)
	}
	var data claimData
	remarshal(t, env.Data, &data)
	if data.Claim.GPUUUID != "GPU-0" {
		t.Errorf("claimed %q, want the first free card", data.Claim.GPUUUID)
	}

	env, code = run(t, store, "claim", "-host", "h1", "-gpu", "GPU-0", "-why", "x", "-for", "1h", "-as", "agent-b", "-no-ssh")
	if code != ExitHeld {
		t.Fatalf("second claim: code=%d, want ExitHeld", code)
	}
	if env.Error.Code != CodeHeld {
		t.Errorf("code = %q", env.Error.Code)
	}
	var he ledger.HeldError
	remarshal(t, env.Error.Conflicts, &he)
	if he.Owner != "agent-a" || he.Why != "train" {
		t.Errorf("conflict does not name the holder: %+v", he)
	}

	if _, code := run(t, store, "release", data.Claim.ID, "-as", "agent-a"); code != ExitOK {
		t.Errorf("release: code=%d", code)
	}
	if _, code := run(t, store, "claim", "-host", "h1", "-why", "now free", "-for", "1h", "-as", "agent-b", "-no-ssh"); code != ExitOK {
		t.Errorf("card stayed held: code=%d", code)
	}
}

// TestFlagsAfterThePositionalAreHonoured: `ftop release ID -as other` must apply -as.
//
// Go's flag package stops parsing at the first non-flag argument, so without the
// reorder this silently released as the *detected* identity — which, for an agent
// trying to be explicit, is the opposite of what it asked for.
func TestFlagsAfterThePositionalAreHonoured(t *testing.T) {
	store := seedStore(t)
	env, _ := run(t, store, "claim", "-host", "h1", "-why", "held by a", "-for", "1h", "-as", "agent-a", "-no-ssh")
	var data claimData
	remarshal(t, env.Data, &data)

	env, code := run(t, store, "release", data.Claim.ID, "-as", "agent-b")
	if code != ExitState {
		t.Fatalf("releasing another agent's claim: code=%d, want ExitState", code)
	}
	if env.Error.Code != CodeNotOwner {
		t.Errorf("code = %q", env.Error.Code)
	}

	// And the owner, spelling -as after the id, must still work.
	if _, code := run(t, store, "release", data.Claim.ID, "-as", "agent-a"); code != ExitOK {
		t.Errorf("owner release: code=%d", code)
	}
}

// TestErrorsAreJSONWithStableCodes: an agent branches on the code, so every failure
// path must produce one.
func TestErrorsAreJSONWithStableCodes(t *testing.T) {
	store := seedStore(t)
	cases := []struct {
		args []string
		code ErrorCode
		want int
	}{
		{[]string{"claim", "-host", "h1", "-for", "1h", "-as", "a"}, CodeUsage, ExitUsage},
		{[]string{"claim", "-host", "h1", "-why", "x", "-as", "a"}, CodeUsage, ExitUsage},
		{[]string{"claim", "-n", "0", "-why", "x", "-for", "1h", "-as", "a"}, CodeUsage, ExitUsage},
		{[]string{"claim", "-gpu", "GPU-0", "-n", "2", "-why", "x", "-for", "1h", "-as", "a"}, CodeUsage, ExitUsage},
		{[]string{"claim", "-host", "h1", "-spread", "-why", "x", "-for", "1h", "-as", "a"}, CodeUsage, ExitUsage},
		{[]string{"claim", "-adopt", "-why", "x", "-for", "1h", "-as", "a"}, CodeUsage, ExitUsage},
		// With no host named ftop places the claim, and it will not place one it has
		// not observed: this test fleet has no host to observe.
		{[]string{"claim", "-why", "x", "-for", "1h", "-as", "a"}, CodeNoObservation, ExitUnavail},
		{[]string{"release", "L-nope", "-as", "a"}, CodeNotFound, ExitState},
		// -no-ssh pins the answer to the ledger: without it, claim would first try to
		// observe the host and fail with a reachability error instead.
		{[]string{"claim", "-host", "nope", "-why", "x", "-for", "1h", "-as", "a", "-no-ssh"}, CodeUnknownCard, ExitState},
	}
	for _, c := range cases {
		env, code := run(t, store, c.args...)
		if env.OK {
			t.Errorf("%v reported ok", c.args)
			continue
		}
		if code != c.want {
			t.Errorf("%v: code=%d, want %d", c.args, code, c.want)
		}
		if env.Error == nil || env.Error.Code != c.code {
			t.Errorf("%v: error code = %+v, want %s", c.args, env.Error, c.code)
		}
	}
}

// TestWhoamiReportsWhatWillBeRecorded: identity detection is inference, so the agent
// has to be able to see it before it commits to a claim.
func TestWhoamiReportsWhatWillBeRecorded(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "abcd1234-0000")
	t.Setenv("AI_AGENT", "")
	id := DetectIdentity()
	if id.Agent != "claude-code" {
		t.Errorf("agent = %q", id.Agent)
	}
	if id.Session != "abcd1234-0000" {
		t.Errorf("session = %q", id.Session)
	}
	if id.Name != "claude-code/abcd1234" {
		t.Errorf("name = %q", id.Name)
	}
}

// TestUnknownIdentityIsNotHuman: an unrecognised caller is unknown, not promoted to
// the operator. (A person at a terminal is identified as "human" by the tty check,
// which is a label and grants nothing; a test's pipes are not a terminal.)
func TestUnknownIdentityIsNotHuman(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	t.Setenv("AI_AGENT", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_THREAD_ID", "")
	if id := DetectIdentity(); id.Agent != "unknown" {
		t.Errorf("agent = %q, want unknown", id.Agent)
	}
}

// TestAIVersionSuffixIsNotIdentity: AI_AGENT carries the client version
// ("claude-code_2-1-303_agent"), so detection matches the prefix, never equality.
func TestAIVersionSuffixIsNotIdentity(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	t.Setenv("AI_AGENT", "claude-code_9-9-9_agent")
	if id := DetectIdentity(); id.Agent != "claude-code" {
		t.Errorf("agent = %q", id.Agent)
	}
}

// TestJSONOnlyOnStdout: a caller pipes stdout into a parser; a banner would break it.
func TestJSONOnlyOnStdout(t *testing.T) {
	t.Setenv("FTOP_LEDGER", seedStore(t))
	var buf bytes.Buffer
	io_ := IO{Out: &buf, Err: &buf}
	dispatch(io_, []string{"claim", "-host", "h1", "-why", "x", "-for", "1h", "-as", "a", "-no-ssh"})
	body := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(body, "{") {
		t.Fatalf("output does not start with JSON: %q", body)
	}
	if strings.Count(body, "{") == 0 || json.Valid([]byte(body)) == false {
		t.Fatalf("output is not one JSON object: %q", body)
	}
}

// TestSkillStatusIsJSONAndNamesThePrimaryPath.
func TestSkillStatusIsJSON(t *testing.T) {
	env, code := run(t, seedStore(t), "skill", "status")
	if code != ExitOK {
		t.Fatalf("code=%d err=%+v", code, env.Error)
	}
	var data skillStatusData
	remarshal(t, env.Data, &data)
	if !strings.HasSuffix(data.PrimaryPath, ".agents/skills/ftop") {
		t.Errorf("primary path = %q", data.PrimaryPath)
	}
}

// TestEmptyListsSerializeAsArrays: an agent that does len(x) on a list must not crash
// because the list happened to be empty, and a null "owner" must not be mistaken for a
// missing field.
func TestEmptyListsSerializeAsArrays(t *testing.T) {
	store := seedStore(t)
	t.Setenv("FTOP_LEDGER", store)
	var buf bytes.Buffer
	io_ := IO{Out: &buf, Err: &buf}
	dispatch(io_, []string{"gpu", "show", "h1:0", "-no-ssh"})
	body := buf.String()
	for _, want := range []string{`"processes": []`, `"history": []`, `"owner": null`} {
		if !strings.Contains(body, want) {
			t.Errorf("gpu show output lacks %s:\n%s", want, body)
		}
	}

	buf.Reset()
	dispatch(io_, []string{"claim", "list"})
	if !strings.Contains(buf.String(), `"claims": []`) {
		t.Errorf("claim list output lacks an empty array:\n%s", buf.String())
	}
}

// remarshal round-trips a decoded JSON value into a typed struct.
func remarshal(t *testing.T, v any, out any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatal(err)
	}
}

// TestClaimRefusesAStrayArgument: Go's flag package stops at the first non-flag
// token, so a trailing `-as bob` after a stray word was silently dropped and the
// claim was recorded under the detected identity instead.
func TestClaimRefusesAStrayArgument(t *testing.T) {
	env, code := run(t, seedStore(t), "claim", "-host", "h1", "-why", "x", "-for", "1h",
		"-as", "alice", "-no-ssh", "unexpected")
	if code != ExitUsage {
		t.Fatalf("code = %d, want ExitUsage", code)
	}
	if env.Error == nil || env.Error.Code != CodeUsage {
		t.Errorf("error = %+v, want usage", env.Error)
	}
}

// TestHistoryFlagAfterThePositionalIsHonoured: `history GPU-0 -n 1` is the order a
// reader writes, and it used to answer with the default 20.
func TestHistoryFlagAfterThePositionalIsHonoured(t *testing.T) {
	store := seedStore(t)
	t.Setenv("FTOP_LEDGER", store)
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	// Two claim/release cycles on the same card, so there are events to count and
	// the filter has something unambiguous to match.
	for i := 0; i < 2; i++ {
		res, err := s.Claim(ledger.ClaimRequest{Host: "h1", GPUUUID: "GPU-0", Owner: "a",
			Why: "x", For: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Release(res.Claim.ID, "a", "released"); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	env, code := run(t, store, "history", "GPU-0", "-n", "1")
	if code != ExitOK {
		t.Fatalf("code=%d err=%+v", code, env.Error)
	}
	var data struct {
		Count int `json:"count"`
	}
	remarshal(t, env.Data, &data)
	if data.Count != 1 {
		t.Errorf("count = %d, want 1: the trailing -n was ignored", data.Count)
	}
}

// TestVersionIsJSON: `ftop version` used to exit 255 with empty stdout, so an agent
// asking what build it was talking to got a failure and no answer.
func TestVersionIsJSON(t *testing.T) {
	env, code := run(t, seedStore(t), "version")
	if code != ExitOK {
		t.Fatalf("code=%d err=%+v", code, env.Error)
	}
	var data map[string]any
	remarshal(t, env.Data, &data)
	if _, ok := data["version"]; !ok {
		t.Errorf("version reply has no version: %+v", data)
	}
}

// TestClaimListMineAcceptsAs: an agent that claimed with -as could not then ask what
// it held, because -mine compared against the detected identity.
func TestClaimListMineAcceptsAs(t *testing.T) {
	store := seedStore(t)
	if _, code := run(t, store, "claim", "-host", "h1", "-why", "x", "-for", "1h",
		"-as", "alice", "-no-ssh"); code != ExitOK {
		t.Fatal("claim failed")
	}
	env, code := run(t, store, "claim", "list", "-mine", "-as", "alice")
	if code != ExitOK {
		t.Fatalf("code=%d err=%+v", code, env.Error)
	}
	var data claimListData
	remarshal(t, env.Data, &data)
	if data.Count != 1 {
		t.Errorf("count = %d, want 1", data.Count)
	}
}

// TestGPUShowRecordsWhatItObserved is covered where a fleet can be reached; the
// ledger half — that the inventory is written — is tested in the ledger package.
// Here we check the unknown-card hint tells an offline caller what to do.
func TestGPUShowOfflineHintMentionsObserving(t *testing.T) {
	env, code := run(t, seedStore(t), "gpu", "show", "nope:9", "-no-ssh")
	if code != ExitState {
		t.Fatalf("code = %d", code)
	}
	if env.Error == nil || env.Error.Code != CodeUnknownCard {
		t.Fatalf("error = %+v", env.Error)
	}
	if !strings.Contains(strings.ToLower(env.Error.Message), "observe") {
		t.Errorf("hint does not say how to find the card: %q", env.Error.Message)
	}
}

// TestHostLabelResolvesToTheAlias: a person assigns work by the label the board
// shows, and the ledger keys machines by ssh alias. `-host gpu-4` used to filter to
// nothing and still exit 0, which reads exactly like "no cards there".
func TestHostLabelResolvesToTheAlias(t *testing.T) {
	cfg := isolatedSSH(t, "Host lab-gpu-4\nHost lab-cpu-5\n")
	store := seedStore(t)
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCards([]ledger.Card{
		{Host: "lab-gpu-4", GPUUUID: "GPU-L4", Index: 0, MemTotal: 24564},
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	env, code := run(t, store, "claim", "-host", "gpu-4", "-why", "x", "-for", "1h",
		"-as", "a", "-no-ssh", "-config", cfg)
	if code != ExitOK {
		t.Fatalf("claim by label: code=%d err=%+v", code, env.Error)
	}
	var data claimData
	remarshal(t, env.Data, &data)
	if data.Claim.Host != "lab-gpu-4" {
		t.Errorf("claimed on %q, want the alias lab-gpu-4", data.Claim.Host)
	}
}

// TestNoHostIsSpecial: ftop ships with no knowledge of anyone's fleet, so no name
// means anything to it. A host called "prod-db" or "reserved-1" is claimable like
// any other.
func TestNoHostIsSpecial(t *testing.T) {
	store := seedStore(t)
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCards([]ledger.Card{
		{Host: "reserved-1", GPUUUID: "GPU-S1", Index: 0, MemTotal: 24564},
		{Host: "prod-db", GPUUUID: "GPU-P1", Index: 0, MemTotal: 24564},
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, h := range []string{"reserved-1", "prod-db"} {
		env, code := run(t, store, "claim", "-host", h, "-why", "x", "-for", "1h", "-as", "a", "-no-ssh")
		if code != ExitOK {
			t.Errorf("claim on %s: code=%d err=%+v", h, code, env.Error)
		}
	}
}

// isolatedSSH writes a throwaway ssh_config and a config.toml naming it, and
// returns the config.toml path, so a test never reads the configuration of the
// machine it runs on.
func isolatedSSH(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	sc := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(sc, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte("[general]\nssh_config = \""+sc+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestClaimListMineSurvivesARestart: identity carries a session id that changes when
// the agent restarts. `-mine` used to compare the whole string, so a restarted agent
// was told it held nothing while its cards stayed locked under its old session.
func TestClaimListMineSurvivesARestart(t *testing.T) {
	store := seedStore(t)
	t.Setenv("AI_AGENT", "")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "aaaa1111-old")
	env, code := run(t, store, "claim", "-host", "h1", "-why", "x", "-for", "1h", "-no-ssh")
	if code != ExitOK {
		t.Fatalf("claim: code=%d err=%+v", code, env.Error)
	}
	var made claimData
	remarshal(t, env.Data, &made)

	// The agent restarts: same kind, new session.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "bbbb2222-new")
	env, code = run(t, store, "claim", "list", "-mine")
	if code != ExitOK {
		t.Fatalf("list: code=%d", code)
	}
	var data claimListData
	remarshal(t, env.Data, &data)
	if data.Count != 1 {
		t.Fatalf("restarted agent sees %d of its claims, want 1", data.Count)
	}
	if _, code := run(t, store, "release", made.Claim.ID); code != ExitOK {
		t.Errorf("restarted agent could not release its own claim: code=%d", code)
	}
}

// TestRestartDoesNotReachAnotherKind: the restart allowance is by agent kind, so a
// claude-code session must still be refused a codex claim.
func TestRestartDoesNotReachAnotherKind(t *testing.T) {
	store := seedStore(t)
	env, code := run(t, store, "claim", "-host", "h1", "-why", "x", "-for", "1h",
		"-as", "codex/cccc3333", "-no-ssh")
	if code != ExitOK {
		t.Fatalf("claim: code=%d err=%+v", code, env.Error)
	}
	var made claimData
	remarshal(t, env.Data, &made)

	t.Setenv("AI_AGENT", "")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "dddd4444")
	env, code = run(t, store, "release", made.Claim.ID)
	if code != ExitState || env.Error == nil || env.Error.Code != CodeNotOwner {
		t.Errorf("claude-code released a codex claim: code=%d err=%+v", code, env.Error)
	}
}

// TestGPUWhyReportsAFailedObservation: with observation requested, a failure must be
// an error, not a clean answer from stale records.
func TestGPUWhyReportsAFailedObservation(t *testing.T) {
	store := seedStore(t)
	cfg := filepath.Join(t.TempDir(), "config.toml")
	// A config that drops every host: observation has nothing to reach.
	if err := os.WriteFile(cfg, []byte("[general]\ndrop = [\"*\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, code := run(t, store, "gpu", "why", "-no-ssh=false", "-config", cfg)
	if env.OK || code == ExitOK {
		t.Fatalf("gpu why answered ok after an observation failed: code=%d", code)
	}
	if env.Error.Code != CodeNoObservation {
		t.Errorf("error code = %q, want no_observation", env.Error.Code)
	}
}

// TestSkillStatusComparesContentNotVersion: the manual changed between two builds that
// both reported 0.3.0, and a version-only check kept calling the old text current.
func TestSkillStatusComparesContentNotVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	old := BuildVersion
	BuildVersion = "9.9.9"
	defer func() { BuildVersion = old }()

	dir := filepath.Join(home, ".agents", "skills", "ftop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same version marker, different text.
	stale := strings.Replace(skillBody("9.9.9"), "## Rules", "## Rules (old)", 1)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	env, code := run(t, seedStore(t), "skill", "status")
	if code != ExitOK {
		t.Fatalf("code=%d", code)
	}
	var data skillStatusData
	remarshal(t, env.Data, &data)
	if data.UpToDate {
		t.Error("an edited manual with the current version string was reported up to date")
	}
	if _, code := run(t, seedStore(t), "skill", "install"); code != ExitOK {
		t.Fatalf("install: code=%d", code)
	}
	env, _ = run(t, seedStore(t), "skill", "status")
	remarshal(t, env.Data, &data)
	if !data.UpToDate {
		t.Error("a freshly installed manual was reported stale")
	}
}

// TestCodexThreadIsTheSession: two Codex conversations used to be recorded as the
// same owner "codex" and could release each other's claims by exact match.
func TestCodexThreadIsTheSession(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	t.Setenv("AI_AGENT", "")
	t.Setenv("CODEX_HOME", "/tmp/codex")
	t.Setenv("CODEX_THREAD_ID", "019a2b3c-thread-a")
	a := DetectIdentity()
	t.Setenv("CODEX_THREAD_ID", "77fe0d11-thread-b")
	b := DetectIdentity()
	if a.Agent != "codex" || b.Agent != "codex" {
		t.Fatalf("agents = %q, %q", a.Agent, b.Agent)
	}
	if a.Name == b.Name {
		t.Errorf("two Codex threads share the owner %q", a.Name)
	}
	if a.Name != "codex/019a2b3c" {
		t.Errorf("name = %q, want codex/019a2b3c", a.Name)
	}
}

// TestAnyAgentCanDeclareItself: the ledger must not depend on recognising a
// particular vendor. An agent ftop has never heard of names itself with FTOP_AGENT
// and is recorded, so every runtime can be brought under the same rules.
func TestAnyAgentCanDeclareItself(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	t.Setenv("AI_AGENT", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("FTOP_AGENT", "my-agent")
	t.Setenv("FTOP_SESSION", "run-42")
	id := DetectIdentity()
	if !id.Known || id.Agent != "my-agent" || id.Session != "run-42" {
		t.Fatalf("identity = %+v", id)
	}
	if id.Name != "my-agent/run-42" {
		t.Errorf("name = %q", id.Name)
	}
}

// TestUnknownRuntimeStillGetsASession: an agent ftop does not know is recognised
// through the AI_AGENT convention, so it does not land on the shared "unknown"
// owner. This is what makes the rule below unnecessary for well-behaved runtimes.
func TestUnknownRuntimeStillGetsASession(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("FTOP_AGENT", "")
	t.Setenv("AI_AGENT", "some-new-agent_3-1-0_agent")
	id := DetectIdentity()
	if id.Agent != "some-new-agent" {
		t.Errorf("agent = %q, want some-new-agent", id.Agent)
	}
	if !id.Known {
		t.Error("a caller identified through AI_AGENT was reported unknown")
	}
}

// TestClaimRefusesTheSharedUnknownOwner: every unidentified process is "unknown", so
// two of them are one owner — one could release the other's claim by exact match.
// Claiming under it must fail rather than create that situation.
func TestClaimRefusesTheSharedUnknownOwner(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	t.Setenv("AI_AGENT", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("FTOP_AGENT", "")
	store := seedStore(t)
	t.Setenv("FTOP_LEDGER", store)
	var buf bytes.Buffer
	io_ := IO{Out: &buf, Err: &buf}
	if code := dispatch(io_, []string{"claim", "-host", "h1", "-why", "x", "-for", "1h", "-no-ssh"}); code != ExitUsage {
		t.Fatalf("code = %d, want %d; output %s", code, ExitUsage, buf.String())
	}
	if !strings.Contains(buf.String(), "FTOP_AGENT") {
		t.Errorf("the refusal does not say how to identify the agent:\n%s", buf.String())
	}
	// The same command with a declared name succeeds.
	t.Setenv("FTOP_AGENT", "worker-a")
	buf.Reset()
	if code := dispatch(io_, []string{"claim", "-host", "h1", "-why", "x", "-for", "1h", "-no-ssh"}); code != ExitOK {
		t.Fatalf("code = %d with FTOP_AGENT set; output %s", code, buf.String())
	}
}

// TestInstallPointsEveryAgentAtTheManual: a skill is only found by a model that goes
// looking for one, so the manual is also referenced from the file each runtime loads
// at the start of every session. An agent that has never heard of ftop must still be
// told to claim a card before it starts a job.
func TestInstallPointsEveryAgentAtTheManual(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, d := range []string{".claude", ".codex", ".gemini", ".config/opencode"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, code := run(t, seedStore(t), "skill", "install"); code != ExitOK {
		t.Fatalf("install: code=%d", code)
	}
	for _, p := range []string{
		".claude/CLAUDE.md", ".codex/AGENTS.md", ".gemini/GEMINI.md",
		".config/opencode/AGENTS.md",
	} {
		body, err := os.ReadFile(filepath.Join(home, p))
		if err != nil {
			t.Errorf("%s was not written: %v", p, err)
			continue
		}
		if !strings.Contains(string(body), "~/.agents/skills/ftop/SKILL.md") {
			t.Errorf("%s does not point at the manual:\n%s", p, body)
		}
	}
}

// TestInstructionBlockKeepsTheUsersOwnText: the block is appended to a file the user
// already maintains, and reinstalling replaces it in place without touching a line
// around it.
func TestInstructionBlockKeepsTheUsersOwnText(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "CLAUDE.md")
	original := "# My notes\n\nalways run the tests\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, code := run(t, seedStore(t), "skill", "install"); code != ExitOK {
			t.Fatalf("install %d: code=%d", i, code)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.HasPrefix(text, original) {
		t.Errorf("the user's own text was not preserved:\n%s", text)
	}
	if n := strings.Count(text, blockBegin); n != 1 {
		t.Errorf("the block appears %d times after three installs:\n%s", n, text)
	}
	// Uninstall leaves the file as it was.
	if _, code := run(t, seedStore(t), "skill", "uninstall"); code != ExitOK {
		t.Fatalf("uninstall: code=%d", code)
	}
	body, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != original {
		t.Errorf("uninstall did not restore the file:\n%s", body)
	}
}

// TestInstallSkipsRuntimesTheUserDoesNotHave: installing ftop must not invent a home
// directory for an agent that is not installed.
func TestInstallSkipsRuntimesTheUserDoesNotHave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, code := run(t, seedStore(t), "skill", "install"); code != ExitOK {
		t.Fatalf("install: code=%d", code)
	}
	for _, p := range []string{".codex", ".gemini", ".cursor", ".config"} {
		if _, err := os.Stat(filepath.Join(home, p)); err == nil {
			t.Errorf("%s was created for a runtime that is not installed", p)
		}
	}
}

// TestSkillStatusReportsAMissingBlock: an installed manual with no pointer is an
// install that no agent will ever be told about, so status has to say so.
func TestSkillStatusReportsAMissingBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, code := run(t, seedStore(t), "skill", "install"); code != ExitOK {
		t.Fatalf("install: code=%d", code)
	}
	// Something rewrote the instruction file and dropped the block.
	if err := os.WriteFile(filepath.Join(home, ".claude", "CLAUDE.md"), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, code := run(t, seedStore(t), "skill", "status")
	if code != ExitOK {
		t.Fatalf("status: code=%d", code)
	}
	var data skillStatusData
	remarshal(t, env.Data, &data)
	found := false
	for _, e := range data.Entries {
		if strings.HasSuffix(e.Path, "CLAUDE.md") {
			found = true
			if e.OK {
				t.Error("a missing pointer block was reported present")
			}
		}
	}
	if !found {
		t.Error("skill status does not report the instruction files at all")
	}
}

// TestMineFindsASelfNamedAgentWithoutASession: an agent that declares FTOP_AGENT but
// has no conversation id must still be able to find and release what it holds.
func TestMineFindsASelfNamedAgentWithoutASession(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	t.Setenv("AI_AGENT", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("FTOP_AGENT", "solo-worker")
	t.Setenv("FTOP_SESSION", "")
	store := seedStore(t)
	env, code := run(t, store, "claim", "-host", "h1", "-why", "x", "-for", "1h", "-no-ssh")
	if code != ExitOK {
		t.Fatalf("claim: code=%d %+v", code, env.Error)
	}
	env, code = run(t, store, "claim", "list", "-mine")
	if code != ExitOK {
		t.Fatalf("list: code=%d", code)
	}
	var data claimListData
	remarshal(t, env.Data, &data)
	if data.Count != 1 {
		t.Fatalf("a self-named agent found %d of its own claims, want 1", data.Count)
	}
	if _, code := run(t, store, "release", data.Claims[0].ID); code != ExitOK {
		t.Fatalf("release: code=%d", code)
	}
}

// TestPersonAtATerminalIsIdentified: a person typing `ftop claim` must not need -as.
// The check is on stdin and stdout being a terminal, which a tool shell is not, so it
// separates the two without granting the person anything an agent lacks.
func TestPersonAtATerminalIsIdentified(t *testing.T) {
	if !atTerminal() {
		// The test harness runs with pipes, which is the agent case: the rule is that
		// such a caller is NOT called human.
		id := DetectIdentity()
		if id.Agent == "human" {
			t.Fatal("a piped caller was identified as a person")
		}
		return
	}
	id := DetectIdentity()
	if id.Agent != "human" || !id.Known {
		t.Errorf("a caller in a terminal was identified as %q", id.Agent)
	}
}

// TestUnknownHostIsAnErrorNotAnEmptyBoard: a misspelled -host used to print an empty
// board and exit 0, which is indistinguishable from "every card is held". `claim` with
// the same typo failed, so the two commands disagreed about the same mistake.
func TestUnknownHostIsAnErrorNotAnEmptyBoard(t *testing.T) {
	cfg := isolatedSSH(t, "Host lab-gpu-1\n")
	store := seedStore(t)
	for _, args := range [][]string{
		{"gpu", "list", "-host", "lab-gpu-9", "-no-ssh"},
		{"gpu", "why", "-host", "lab-gpu-9"},
	} {
		env, code := run(t, store, append(args, "-config", cfg)...)
		if code != ExitUsage {
			t.Fatalf("%v: code=%d, want %d", args, code, ExitUsage)
		}
		if env.Error == nil || env.Error.Code != CodeUsage {
			t.Fatalf("%v: error = %+v", args, env.Error)
		}
	}
}

// TestFreeAndHeldTogetherIsAUsageError: the pair selects no card, and an empty list
// with exit 0 reads as "the fleet is empty".
func TestFreeAndHeldTogetherIsAUsageError(t *testing.T) {
	env, code := run(t, seedStore(t), "gpu", "list", "-free", "-held", "-no-ssh")
	if code != ExitUsage {
		t.Fatalf("code=%d, want %d (%+v)", code, ExitUsage, env.Error)
	}
}

// TestSafeNeedsAnObservation: -safe promises a card nothing is running on, which the
// ledger alone cannot know. Answering from the ledger would either include other
// people's jobs or be an empty list that reads as "nothing is free".
func TestSafeNeedsAnObservation(t *testing.T) {
	env, code := run(t, seedStore(t), "gpu", "list", "-safe", "-no-ssh")
	if code != ExitUsage {
		t.Fatalf("code=%d, want %d (%+v)", code, ExitUsage, env.Error)
	}
	if !strings.Contains(env.Error.Message, "-no-ssh") {
		t.Errorf("the refusal does not name the flag that caused it: %q", env.Error.Message)
	}
}

// TestFreeCardsCarryAnExplicitNullClaim: an omitted key and "nobody holds this" are
// different answers, and a reader that looked for the key's presence read free cards
// as "the ledger said nothing".
func TestFreeCardsCarryAnExplicitNullClaim(t *testing.T) {
	store := seedStore(t)
	t.Setenv("FTOP_LEDGER", store)
	var buf bytes.Buffer
	io_ := IO{Out: &buf, Err: &buf}
	if code := dispatch(io_, []string{"gpu", "list", "-no-ssh"}); code != ExitOK {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(buf.String(), `"claim": null`) {
		t.Errorf("a free card does not serialise claim as an explicit null:\n%s", buf.String())
	}
}

// TestOperatorOverrideNeedsTheOperatorFlag: -any-owner is the one operation that closes
// another agent's claim, and every agent runs as the same unix user, so it cannot be
// authenticated. It can be explicit: the flag alone is not enough, and the refusal says
// what to do instead.
func TestOperatorOverrideNeedsTheOperatorFlag(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	t.Setenv("AI_AGENT", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("FTOP_AGENT", "some-agent")
	t.Setenv("FTOP_OPERATOR", "")
	store := seedStore(t)
	env, code := run(t, store, "claim", "-host", "h1", "-why", "x", "-for", "1h", "-no-ssh")
	if code != ExitOK {
		t.Fatalf("claim: %d %+v", code, env.Error)
	}
	var data claimData
	remarshal(t, env.Data, &data)
	// Another agent, trying to take the card by releasing it.
	t.Setenv("FTOP_AGENT", "other-agent")
	env, code = run(t, store, "release", data.Claim.ID, "-any-owner")
	if code != ExitUsage {
		t.Fatalf("an agent released another's claim with -any-owner: code=%d %+v", code, env.Error)
	}
	if !strings.Contains(env.Error.Hint, "holder") {
		t.Errorf("the refusal does not say to ask the holder: %q", env.Error.Hint)
	}
	// The operator, with the marker set.
	t.Setenv("FTOP_OPERATOR", "1")
	if _, code := run(t, store, "release", data.Claim.ID, "-any-owner"); code != ExitOK {
		t.Fatalf("the operator could not use -any-owner: code=%d", code)
	}
}

// TestHostListsMachinesNotJustLedgerRows: an agent needs the machine names before
// anything has been observed, and a machine with no cards in the ledger is still one it
// may be told to use. The listing was the ledger's rows, so a brand-new fleet answered
// with an empty list.
func TestHostListsMachinesNotJustLedgerRows(t *testing.T) {
	cfg := isolatedSSH(t, "Host lab-gpu-1\n  HostName 192.0.2.11\nHost lab-cpu-2\n  HostName 192.0.2.12\n")
	// A ledger that knows nothing yet: the fleet's names must still be listed.
	store := filepath.Join(t.TempDir(), "empty.db")
	env, code := run(t, store, "host", "-config", cfg)
	if code != ExitOK {
		t.Fatalf("code=%d %+v", code, env.Error)
	}
	var data struct {
		Count int `json:"count"`
		Hosts []struct {
			Host    string `json:"host"`
			Managed bool   `json:"managed"`
		} `json:"hosts"`
	}
	remarshal(t, env.Data, &data)
	if data.Count != 2 {
		t.Fatalf("count = %d, want 2 (every machine, observed or not): %+v", data.Count, data.Hosts)
	}
	for _, h := range data.Hosts {
		if !h.Managed {
			t.Errorf("%s is not reported as managed", h.Host)
		}
	}
}

// TestSummarySafeCountIsTheAnswerToCanIStart: `free` includes cards something is already
// running on, so the count an agent must read before claiming is `safe`.
func TestSummarySafeCountMatchesTheSafeFilter(t *testing.T) {
	if !safeToTake(ledger.CardView{Card: ledger.Card{Present: true},
		Observation: &ledger.Observation{ProcsKnown: true, MemKnown: true}}) {
		t.Error("an observed, empty, unclaimed card was not safe")
	}
	cases := []struct {
		name string
		v    ledger.CardView
	}{
		{"busy", ledger.CardView{Card: ledger.Card{Present: true},
			Observation: &ledger.Observation{Procs: 1, ProcsKnown: true, MemKnown: true}}},
		{"not enumerated", ledger.CardView{Card: ledger.Card{Present: true},
			Observation: &ledger.Observation{ProcsKnown: false}}},
		{"never observed", ledger.CardView{Card: ledger.Card{Present: true}}},
		{"left the fleet", ledger.CardView{Card: ledger.Card{Present: false},
			Observation: &ledger.Observation{ProcsKnown: true, MemKnown: true}}},
	}
	for _, c := range cases {
		if safeToTake(c.v) {
			t.Errorf("%s was reported safe to take", c.name)
		}
	}
}

// TestMineDoesNotShowAnotherConversation: `-mine` and `release` must use one rule. It
// listed every claim of the same runtime, so a fresh conversation saw a peer's claims as
// its own and could release them.
func TestMineDoesNotShowAnotherConversation(t *testing.T) {
	store := seedStore(t)
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	// A claim made from a different directory, by the same runtime.
	if _, err := s.Claim(ledger.ClaimRequest{
		Host: "h1", Owner: "claude-code/aaaa1111", Why: "theirs", For: time.Hour,
		Repo: "proj", Branch: "main", Cwd: "/src/elsewhere",
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "bbbb2222")
	t.Setenv("AI_AGENT", "")
	env, code := run(t, store, "claim", "list", "-mine")
	if code != ExitOK {
		t.Fatalf("code=%d", code)
	}
	var data claimListData
	remarshal(t, env.Data, &data)
	if data.Count != 0 {
		t.Errorf("another conversation's claims were listed as mine: %+v", data.Claims)
	}
}

// TestNamedAgentFindsAndReleasesItsOwnClaims: a claim made with -as was invisible to
// `claim list -mine` and refused by plain `release`, so a script that lost its id had
// no way to find or free its own card.
func TestNamedAgentFindsAndReleasesItsOwnClaims(t *testing.T) {
	store := seedStore(t)
	if _, code := run(t, store, "claim", "-host", "h1", "-why", "x", "-for", "1h",
		"-as", "my-script", "-no-ssh"); code != ExitOK {
		t.Fatalf("claim: code=%d", code)
	}
	env, code := run(t, store, "claim", "list", "-mine", "-as", "my-script")
	if code != ExitOK {
		t.Fatalf("list: code=%d", code)
	}
	var data claimListData
	remarshal(t, env.Data, &data)
	if data.Count != 1 {
		t.Fatalf("a named agent found %d of its claims, want 1", data.Count)
	}
	// And the release works without repeating -as: the identity is the same name.
	if _, code := run(t, store, "release", data.Claims[0].ID, "-as", "my-script"); code != ExitOK {
		t.Fatalf("release: code=%d", code)
	}
}

// TestRenewAfterARestart: the successor of a claim can renew it too. It could release but
// not renew, so a job that outlived its deadline could only be extended by the session
// that started it.
func TestRenewAfterARestart(t *testing.T) {
	store := seedStore(t)
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	// The place the successor will report: it is what `mineToRelease` compares against.
	cwd, _ := os.Getwd()
	repo, branch := "", ""
	if id := DetectIdentity(); id.Repo != "" {
		repo, branch = id.Repo, id.Branch
	}
	if _, err := s.Claim(ledger.ClaimRequest{
		Host: "h1", Owner: "claude-code/aaaa1111", Why: "long job", For: time.Hour,
		Repo: repo, Branch: branch, Cwd: cwd,
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "bbbb2222")
	t.Setenv("AI_AGENT", "")
	env, code := run(t, store, "claim", "list", "-mine")
	if code != ExitOK {
		t.Fatalf("list: code=%d", code)
	}
	var data claimListData
	remarshal(t, env.Data, &data)
	if data.Count != 1 {
		t.Fatalf("the successor found %d claims, want 1", data.Count)
	}
	if _, code := run(t, store, "renew", data.Claims[0].ID, "-for", "2h"); code != ExitOK {
		t.Fatalf("renew: code=%d", code)
	}
}

// placementFleet is an isolated fleet of a four-card host, a two-card host and a
// one-card workstation the operator marked manual, all in the ledger.
func placementFleet(t *testing.T) (store, cfg string) {
	t.Helper()
	cfg = isolatedSSH(t, "Host lab-four\nHost lab-two\nHost lab-desk\n")
	if err := os.WriteFile(cfg, append(mustRead(t, cfg),
		[]byte("\n[claim]\nmanual = [\"lab-desk\"]\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	store = filepath.Join(t.TempDir(), "ledger.db")
	s, err := ledger.Open(store)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var cards []ledger.Card
	for host, n := range map[string]int{"lab-four": 4, "lab-two": 2, "lab-desk": 1} {
		for i := 0; i < n; i++ {
			cards = append(cards, ledger.Card{Host: host, GPUUUID: fmt.Sprintf("GPU-%s-%d", host, i),
				Index: i, MemTotal: 24564})
		}
	}
	if err := s.UpsertCards(cards); err != nil {
		t.Fatal(err)
	}
	return store, cfg
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestClaimPlacesWithoutAHost: two cards with no host named go to the two-card
// machine, come back with what to export, and leave with one release.
func TestClaimPlacesWithoutAHost(t *testing.T) {
	store, cfg := placementFleet(t)
	env, code := run(t, store, "claim", "-n", "2", "-why", "ddp", "-for", "1h", "-as", "a",
		"-no-ssh", "-config", cfg)
	if code != ExitOK {
		t.Fatalf("claim -n 2: code=%d err=%+v", code, env.Error)
	}
	var data claimData
	remarshal(t, env.Data, &data)
	if len(data.Hosts) != 1 || data.Hosts[0].Host != "lab-two" || len(data.Claims) != 2 {
		t.Fatalf("placed %+v, want both cards on lab-two", data.Hosts)
	}
	want := "GPU-lab-two-0,GPU-lab-two-1"
	if data.Hosts[0].CUDAVisibleDevices != want || data.Env["CUDA_VISIBLE_DEVICES"] != want {
		t.Errorf("CUDA_VISIBLE_DEVICES = %q / %q, want %q", data.Hosts[0].CUDAVisibleDevices,
			data.Env["CUDA_VISIBLE_DEVICES"], want)
	}
	if data.Env["FTOP_CLAIM"] != data.Group || data.Placement.Mode != "fit" {
		t.Errorf("FTOP_CLAIM = %q, group %q, placement %+v", data.Env["FTOP_CLAIM"], data.Group,
			data.Placement)
	}
	if !strings.Contains(data.Hint, "ftop release "+data.Group) {
		t.Errorf("hint does not say how to release: %q", data.Hint)
	}
	env, code = run(t, store, "release", data.Group, "-as", "a")
	if code != ExitOK {
		t.Fatalf("release group: code=%d err=%+v", code, env.Error)
	}
	var rel releaseData
	remarshal(t, env.Data, &rel)
	if len(rel.Claims) != 2 || !rel.Claims[0].Closed || !rel.Claims[1].Closed {
		t.Errorf("release closed %+v, want both", rel.Claims)
	}
}

// TestManualHostIsNamedNotPlaced: the workstation fits one card best, but it is
// manual, so a placement passes it by; naming it still works.
func TestManualHostIsNamedNotPlaced(t *testing.T) {
	store, cfg := placementFleet(t)
	env, code := run(t, store, "claim", "-why", "x", "-for", "1h", "-as", "a", "-no-ssh", "-config", cfg)
	if code != ExitOK {
		t.Fatalf("claim: code=%d err=%+v", code, env.Error)
	}
	var data claimData
	remarshal(t, env.Data, &data)
	if data.Claim.Host == "lab-desk" {
		t.Fatal("a placement chose the manual host")
	}
	if data.Claim.Host != "lab-two" {
		t.Errorf("one card went to %s, want lab-two (the smallest that is not manual)", data.Claim.Host)
	}
	if _, code := run(t, store, "claim", "-host", "lab-desk", "-why", "x", "-for", "1h", "-as", "a",
		"-no-ssh", "-config", cfg); code != ExitOK {
		t.Errorf("claiming the manual host by name: code=%d", code)
	}
	env, _ = run(t, store, "host", "-config", cfg)
	if !strings.Contains(fmt.Sprint(env.Data), "manual:true") {
		t.Errorf("ftop host does not show the manual host: %v", env.Data)
	}
}

// TestNoCapacityIsAStableCode: too few cards is its own code, says how many the
// fleet could give, and claims nothing.
func TestNoCapacityIsAStableCode(t *testing.T) {
	store, cfg := placementFleet(t)
	env, code := run(t, store, "claim", "-n", "5", "-why", "x", "-for", "1h", "-as", "a",
		"-no-ssh", "-config", cfg)
	if code != ExitHeld || env.Error == nil || env.Error.Code != CodeNoCapacity {
		t.Fatalf("claim -n 5: code=%d err=%+v", code, env.Error)
	}
	var ie ledger.InsufficientError
	remarshal(t, env.Error.Conflicts, &ie)
	if ie.MostOnOneHost != 4 || ie.BestHost != "lab-four" || ie.Idle != 6 {
		t.Errorf("conflicts = %+v, want 4 on lab-four and 6 idle", ie)
	}
	if !strings.Contains(env.Error.Hint, "-spread") {
		t.Errorf("hint does not mention -spread although 6 cards are idle: %q", env.Error.Hint)
	}
	env, _ = run(t, store, "claim", "list")
	if !strings.Contains(fmt.Sprint(env.Data), "count:0") {
		t.Errorf("a failed claim left claims behind: %v", env.Data)
	}
}

// TestGroupRenewsTogether: renew takes the group like release does.
func TestGroupRenewsTogether(t *testing.T) {
	store, cfg := placementFleet(t)
	env, _ := run(t, store, "claim", "-n", "3", "-why", "x", "-for", "1h", "-as", "a",
		"-no-ssh", "-config", cfg)
	var data claimData
	remarshal(t, env.Data, &data)
	env, code := run(t, store, "renew", data.Group, "-for", "6h", "-as", "a")
	if code != ExitOK {
		t.Fatalf("renew group: code=%d err=%+v", code, env.Error)
	}
	var rel releaseData
	remarshal(t, env.Data, &rel)
	if len(rel.Claims) != 3 {
		t.Fatalf("renewed %d claims, want 3", len(rel.Claims))
	}
	for _, c := range rel.Claims {
		if time.Until(c.Expires) < 5*time.Hour {
			t.Errorf("%s expires %v", c.ID, c.Expires)
		}
	}
	if _, code := run(t, store, "release", data.Group, "-as", "b"); code != ExitState {
		t.Errorf("another owner released the group: code=%d", code)
	}
}

// TestClaimWithoutAnObservationRefuses: ftop hands out only cards it has just seen
// idle. A claim that could not look used to fall back to the ledger whenever the host
// was known — in practice, a build without a probe handed out cards silently. Only
// -no-ssh says the ledger is enough.
func TestClaimWithoutAnObservationRefuses(t *testing.T) {
	store := seedStore(t)
	env, code := run(t, store, "claim", "-host", "h1", "-why", "x", "-for", "1h", "-as", "a")
	if code != ExitUnavail || env.Error == nil || env.Error.Code != CodeNoObservation {
		t.Fatalf("claim with no observation: code=%d err=%+v, want no_observation", code, env.Error)
	}
	if _, code := run(t, store, "claim", "-host", "h1", "-why", "x", "-for", "1h", "-as", "a",
		"-no-ssh"); code != ExitOK {
		t.Errorf("-no-ssh claim: code=%d", code)
	}
}

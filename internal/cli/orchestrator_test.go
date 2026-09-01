package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/orchestrator"
	"reasonix/internal/tool"
)

// writeOrchestratorProject isolates the user config home and writes a
// reasonix.toml with a resolvable provider plus the given [orchestrator]
// section, then chdirs into the project. It returns the project dir.
func writeOrchestratorProject(t *testing.T, orchestratorTOML string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("REASONIX_CREDENTIALS_STORE", "file")

	dir := t.TempDir()
	cfg := `default_model = "test-model"

[[providers]]
name = "test-model"
kind = "openai"
base_url = "https://example.invalid"
model = "x"
api_key_env = "REASONIX_TEST_KEY_UNSET"

` + orchestratorTOML
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

// fakeBashTool is a minimal bash-shaped tool so the git-agent NoGitBash wrap can
// be observed on the main controller's registry.
type fakeBashTool struct{}

func (fakeBashTool) Name() string        { return "bash" }
func (fakeBashTool) Description() string { return "fake bash for tests" }
func (fakeBashTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)
}
func (fakeBashTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", nil
}
func (fakeBashTool) ReadOnly() bool { return false }

func TestWireOrchestratorNilWithoutAgents(t *testing.T) {
	writeOrchestratorProject(t, "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	ctrl := control.New(control.Options{Sink: event.Discard, Registry: tool.NewRegistry()})
	defer ctrl.Close()

	if orc := wireOrchestrator(context.Background(), cfg, ctrl, event.Discard, 0, "", resolveCLISessionDir()); orc != nil {
		t.Fatalf("wireOrchestrator with no [orchestrator] agents = %v, want nil", orc)
	}
}

func TestWireOrchestratorNilCfg(t *testing.T) {
	ctrl := control.New(control.Options{Sink: event.Discard, Registry: tool.NewRegistry()})
	defer ctrl.Close()

	if orc := wireOrchestrator(context.Background(), nil, ctrl, event.Discard, 0, "", resolveCLISessionDir()); orc != nil {
		t.Fatalf("wireOrchestrator with nil cfg = %v, want nil", orc)
	}
}

func TestWireOrchestratorRegistersAgentToolsAndChild(t *testing.T) {
	writeOrchestratorProject(t, `
[[orchestrator.agents]]
name = "worker"
model = "test-model"
persist = true
`)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	ctrl := control.New(control.Options{Sink: event.Discard, Registry: tool.NewRegistry()})
	defer ctrl.Close()

	orc := wireOrchestrator(context.Background(), cfg, ctrl, event.Discard, 0, "", resolveCLISessionDir())
	if orc == nil {
		t.Fatal("wireOrchestrator returned nil with a configured agent")
	}
	if !orc.HasAgent("worker") {
		t.Fatalf("worker child agent not registered; agents = %v", orc.AgentNames())
	}
	for _, name := range []string{"agent_spawn", "agent_send", "agent_status", "agent_stats"} {
		if _, ok := ctrl.Registry().Get(name); !ok {
			t.Fatalf("orchestrator tool %q not registered on the main controller", name)
		}
	}
}

func TestWireOrchestratorGitAgentWrapsBash(t *testing.T) {
	writeOrchestratorProject(t, `
[[orchestrator.agents]]
name = "git"
model = "test-model"
`)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	reg := tool.NewRegistry()
	reg.Add(fakeBashTool{})
	ctrl := control.New(control.Options{Sink: event.Discard, Registry: reg})
	defer ctrl.Close()

	orc := wireOrchestrator(context.Background(), cfg, ctrl, event.Discard, 0, "", resolveCLISessionDir())
	if orc == nil {
		t.Fatal("wireOrchestrator returned nil with a configured agent")
	}
	if !orc.HasAgent("git") {
		t.Fatalf("git child agent not registered; agents = %v", orc.AgentNames())
	}
	got, ok := ctrl.Registry().Get("bash")
	if !ok {
		t.Fatal("bash tool missing after orchestrator wiring")
	}
	if _, wrapped := got.(*orchestrator.NoGitBash); !wrapped {
		t.Fatalf("bash tool not wrapped in NoGitBash, got %T", got)
	}
}

// providerTOML is the provider block every test project needs to resolve.
const providerTOML = `default_model = "test-model"

[[providers]]
name = "test-model"
kind = "openai"
base_url = "https://example.invalid"
model = "x"
api_key_env = "REASONIX_TEST_KEY_UNSET"

`

func TestWireOrchestratorUsesProvidedSessionDir(t *testing.T) {
	writeOrchestratorProject(t, `
[[orchestrator.agents]]
name = "worker"
model = "test-model"
persist = true
`)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	ctrl := control.New(control.Options{Sink: event.Discard, Registry: tool.NewRegistry()})
	defer ctrl.Close()

	sessionDir := t.TempDir()
	orc := wireOrchestrator(context.Background(), cfg, ctrl, event.Discard, 0, "", sessionDir)
	if orc == nil {
		t.Fatal("wireOrchestrator returned nil with a configured agent")
	}
	if got := orc.SessionDir(); got != sessionDir {
		t.Fatalf("orchestrator session dir = %q, want %q", got, sessionDir)
	}
}

func TestWireOrchestratorRootsChildrenAtWorkspaceRoot(t *testing.T) {
	// The process cwd is project A; the child agents must root at the
	// workspaceRoot (project B), not the process cwd.
	projA := writeOrchestratorProject(t, `
[[orchestrator.agents]]
name = "worker"
model = "test-model"
`)
	projB := t.TempDir()
	if err := os.WriteFile(filepath.Join(projB, "reasonix.toml"), []byte(providerTOML+`
[[orchestrator.agents]]
name = "worker"
model = "test-model"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadForRoot(projA)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	ctrl := control.New(control.Options{Sink: event.Discard, Registry: tool.NewRegistry()})
	defer ctrl.Close()

	orc := wireOrchestrator(context.Background(), cfg, ctrl, event.Discard, 0, projB, config.SessionDir())
	if orc == nil {
		t.Fatal("wireOrchestrator returned nil with a configured agent")
	}
	child, ok := orc.Agent("worker")
	if !ok {
		t.Fatalf("worker child agent not registered; agents = %v", orc.AgentNames())
	}
	bash, ok := child.Ctrl.Registry().Get("bash")
	if !ok {
		t.Fatal("child bash tool missing")
	}
	got, err := bash.Execute(context.Background(), json.RawMessage(`{"command":"pwd"}`))
	if err != nil {
		t.Fatalf("child bash pwd: %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(got))
	if err != nil {
		t.Fatalf("resolve child bash cwd %q: %v", got, err)
	}
	wantDir, err := filepath.EvalSymlinks(projB)
	if err != nil {
		t.Fatalf("resolve workspace root %q: %v", projB, err)
	}
	if gotDir != wantDir {
		t.Fatalf("child bash ran in %q, want workspace root %q", gotDir, wantDir)
	}
}

// TestTrashOrchestratorSessionSucceedsAcrossRepeatedClears guards against the
// fixed orchestrator filename (orchestrator_<name>.jsonl) colliding with its
// own trash key: the first clear moves the transcript to .trash/<key>, and a
// later clear of the same agent must still move the current transcript instead
// of failing with "session already exists in trash" (which previously left the
// old conversation intact and resurrected it on restart).
func TestTrashOrchestratorSessionSucceedsAcrossRepeatedClears(t *testing.T) {
	dir := t.TempDir()
	name := "worker"
	path := filepath.Join(dir, "orchestrator_"+name+".jsonl")

	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// First clear must succeed and remove the live transcript.
	write(`{"role":"user","content":"first"}`)
	if err := trashOrchestratorSession(path); err != nil {
		t.Fatalf("first clear: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("transcript should be gone from live dir after first clear")
	}

	// A second clear of the same agent (new transcript at the same fixed path)
	// must also succeed and remove the live transcript.
	write(`{"role":"user","content":"second"}`)
	if err := trashOrchestratorSession(path); err != nil {
		t.Fatalf("second clear: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("transcript should be gone from live dir after second clear")
	}
}

func TestTrashOrchestratorSessionEmptyPath(t *testing.T) {
	if err := trashOrchestratorSession(""); err != nil {
		t.Fatalf("empty path should be a no-op, got %v", err)
	}
}

func TestWireOrchestratorChildSkillsAllowlist(t *testing.T) {
	dir := writeOrchestratorProject(t, `
[orchestrator]
main_skip_skills = ["deploy"]

[[orchestrator.agents]]
name = "deployer"
model = "test-model"
skills = ["deploy"]
skip_skills = ["review"]
`)
	if err := os.MkdirAll(filepath.Join(dir, ".reasonix", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".reasonix", "skills", "deploy.md"), []byte("---\ndescription: deploy to prod\n---\nplaybook"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".reasonix", "skills", "other.md"), []byte("---\ndescription: a normal skill\n---\nplaybook"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	ctrl := control.New(control.Options{Sink: event.Discard, Registry: tool.NewRegistry()})
	defer ctrl.Close()

	orc := wireOrchestrator(context.Background(), cfg, ctrl, event.Discard, 0, "", resolveCLISessionDir())
	if orc == nil {
		t.Fatal("wireOrchestrator returned nil with a configured agent")
	}
	a, ok := orc.Agent("deployer")
	if !ok {
		t.Fatalf("deployer child agent not registered; agents = %v", orc.AgentNames())
	}

	var hasDeploy, hasOther, hasBuiltin bool
	for _, s := range a.Ctrl.Skills() {
		switch s.Name {
		case "deploy":
			hasDeploy = true
		case "other":
			hasOther = true
		case "explore":
			hasBuiltin = true
		}
	}
	if !hasDeploy {
		t.Fatalf("deployer child should see allowlisted skill 'deploy'; got %v", a.Ctrl.Skills())
	}
	if hasOther {
		t.Fatalf("deployer child should NOT see non-allowlisted skill 'other'; got %v", a.Ctrl.Skills())
	}
	if hasBuiltin {
		t.Fatalf("deployer child should NOT see builtin skill outside allowlist; got %v", a.Ctrl.Skills())
	}

	// The main agent (the ctrl passed to wireOrchestrator) is not rebuilt here,
	// so main_skip_skills cannot be asserted on ctrl. Its behavior is already
	// proven at the boot layer (TestBuildMainAgentSkipsOrchestratorMainSkipSkills).
}

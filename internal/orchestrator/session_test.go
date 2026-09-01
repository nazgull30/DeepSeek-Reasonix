package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestSessionPath(t *testing.T) {
	o := New(event.Discard)
	path := o.SessionPath("/tmp/sessions", "agent-x")
	expected := filepath.Join("/tmp/sessions", "orchestrator_agent-x.jsonl")
	if path != expected {
		t.Fatalf("expected %q, got %q", expected, path)
	}
}

func TestSaveSessionsEmptyDir(t *testing.T) {
	o := New(event.Discard)
	err := o.SaveSessions("")
	if err != nil {
		t.Fatalf("expected nil for empty dir, got %v", err)
	}
}

func TestSaveSessionsNoopForNonPersist(t *testing.T) {
	dir := t.TempDir()
	o := New(event.Discard)

	ctrl := testAgentCtrl(t, "temp", []provider.Message{
		{Role: provider.RoleAssistant, Content: "result"},
	})
	o.AddAgent("temp", ctrl, config.OrchestratorAgentEntry{
		Name:    "temp",
		Persist: false,
	})

	err := o.SaveSessions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveSessionsPersistsAgent(t *testing.T) {
	dir := t.TempDir()
	o := New(event.Discard)

	ctrl := testAgentCtrl(t, "persist-agent", []provider.Message{
		{Role: provider.RoleAssistant, Content: "data"},
	})
	ctrl.SetSessionPath(filepath.Join(dir, "session.jsonl"))
	o.AddAgent("persist-agent", ctrl, config.OrchestratorAgentEntry{
		Name:    "persist-agent",
		Persist: true,
	})

	err := o.SaveSessions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSessionsEmptyDir(t *testing.T) {
	o := New(event.Discard)
	err := o.LoadSessions("")
	if err != nil {
		t.Fatalf("expected nil for empty dir, got %v", err)
	}
}

func TestLoadSessionsNonExistingFile(t *testing.T) {
	dir := t.TempDir()
	o := New(event.Discard)

	ctrl := testAgentCtrl(t, "fresh", []provider.Message{
		{Role: provider.RoleAssistant, Content: "old data"},
	})
	o.AddAgent("fresh", ctrl, config.OrchestratorAgentEntry{
		Name:    "fresh",
		Persist: true,
	})

	// No session file exists on disk yet
	err := o.LoadSessions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveAndLoadSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	o := New(event.Discard)

	ctrl := testAgentCtrl(t, "persistent", []provider.Message{
		{Role: provider.RoleAssistant, Content: "saved result"},
	})
	path := filepath.Join(dir, "session.jsonl")
	ctrl.SetSessionPath(path)

	o.AddAgent("persistent", ctrl, config.OrchestratorAgentEntry{
		Name:    "persistent",
		Persist: true,
	})

	// First save the session via the agent's run to have it snapshot
	err := o.SaveSessions(dir)
	if err != nil {
		t.Fatalf("SaveSessions error: %v", err)
	}

	// Load should succeed
	o2 := New(event.Discard)
	ctrl2 := testAgentCtrl(t, "persistent", nil)
	o2.AddAgent("persistent", ctrl2, config.OrchestratorAgentEntry{
		Name:    "persistent",
		Persist: true,
	})

	err = o2.LoadSessions(dir)
	if err != nil {
		t.Fatalf("LoadSessions error: %v", err)
	}
}

func TestSaveSessionsCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "sessions")
	o := New(event.Discard)

	ctrl := testAgentCtrl(t, "agent", []provider.Message{
		{Role: provider.RoleAssistant, Content: "data"},
	})
	ctrl.SetSessionPath(filepath.Join(dir, "session.jsonl"))
	o.AddAgent("agent", ctrl, config.OrchestratorAgentEntry{
		Name:    "agent",
		Persist: true,
	})

	err := o.SaveSessions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatal("expected session dir to be created")
	}
}

// persistTestCtrl builds a controller whose runner appends a user + assistant
// turn straight into the shared executor session, so a RunTurn produces real
// transcript content without any provider call.
func persistTestCtrl(t *testing.T) *control.Controller {
	t.Helper()
	sess := agent.NewSession("")
	exec := agent.New(nil, tool.NewRegistry(), sess, agent.Options{}, event.Discard)
	runner := &mockRunner{
		runFn: func(ctx context.Context, input string) error {
			task := strings.TrimSpace(input)
			sess.Add(provider.Message{Role: provider.RoleUser, Content: task})
			sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "OK: " + task})
			return nil
		},
	}
	return control.New(control.Options{
		Sink:     event.Discard,
		Executor: exec,
		Runner:   runner,
	})
}

// TestPersistAcrossMainSessionsEndToEnd proves the user-facing contract behind
// persist = true: a child agent keeps its full conversation history across
// completely separate main-agent sessions (fresh controllers, same session
// dir), while a non-persisted sibling starts empty even though it ran.
//
// Main session 1 runs the "keeper" child twice and the "temp" child once, each
// through ManagedAgent.Run (the real agent_spawn/agent_send path), then saves.
// Main session 2 boots brand-new controllers for both children and loads the
// session dir.
func TestPersistAcrossMainSessionsEndToEnd(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// --- Main session 1 -----------------------------------------------------
	o1 := New(event.Discard)
	o1.AddAgent("keeper", persistTestCtrl(t), config.OrchestratorAgentEntry{Name: "keeper", Persist: true})
	o1.AddAgent("temp", persistTestCtrl(t), config.OrchestratorAgentEntry{Name: "temp", Persist: false})
	o1.SetSessionDir(dir)
	if err := o1.LoadSessions(dir); err != nil {
		t.Fatalf("session 1 LoadSessions: %v", err)
	}

	if _, err := o1.SendMessage(ctx, "keeper", "open ports"); err != nil {
		t.Fatalf("keeper turn 1: %v", err)
	}
	if _, err := o1.SendMessage(ctx, "keeper", "deploy v2"); err != nil {
		t.Fatalf("keeper turn 2: %v", err)
	}
	if _, err := o1.SendMessage(ctx, "temp", "scratch work"); err != nil {
		t.Fatalf("temp turn: %v", err)
	}

	// Mirror the exit lifecycle: SaveSessions persists persist-only children.
	if err := o1.SaveSessions(dir); err != nil {
		t.Fatalf("session 1 SaveSessions: %v", err)
	}

	// The persisted keeper file must now exist on disk.
	if _, err := os.Stat(o1.SessionPath(dir, "keeper")); err != nil {
		t.Fatalf("keeper session file missing after session 1: %v", err)
	}
	if _, err := os.Stat(o1.SessionPath(dir, "temp")); !os.IsNotExist(err) {
		t.Fatalf("temp (non-persist) session file should not exist, got %v", err)
	}

	// --- Main session 2 -----------------------------------------------------
	o2 := New(event.Discard)
	keeper2 := persistTestCtrl(t)
	temp2 := persistTestCtrl(t)
	o2.AddAgent("keeper", keeper2, config.OrchestratorAgentEntry{Name: "keeper", Persist: true})
	o2.AddAgent("temp", temp2, config.OrchestratorAgentEntry{Name: "temp", Persist: false})
	o2.SetSessionDir(dir)
	if err := o2.LoadSessions(dir); err != nil {
		t.Fatalf("session 2 LoadSessions: %v", err)
	}

	hist := keeper2.History()
	if len(hist) != 4 {
		t.Fatalf("keeper resumed history = %d messages, want 4 (both main sessions' turns); got %v", len(hist), hist)
	}
	assertTurn := func(i int, role provider.Role, want string) {
		t.Helper()
		if hist[i].Role != role {
			t.Fatalf("history[%d].Role = %q, want %q (history: %v)", i, hist[i].Role, role, hist)
		}
		if !strings.Contains(hist[i].Content, want) {
			t.Fatalf("history[%d].Content %q should contain %q", i, hist[i].Content, want)
		}
	}
	assertTurn(0, provider.RoleUser, "open ports")
	assertTurn(1, provider.RoleAssistant, "OK: open ports")
	assertTurn(2, provider.RoleUser, "deploy v2")
	assertTurn(3, provider.RoleAssistant, "OK: deploy v2")

	// The non-persisted sibling ignored session 1 entirely.
	if got := temp2.History(); len(got) != 0 {
		t.Fatalf("non-persist 'temp' resumed history = %d messages, want 0; got %v", len(got), got)
	}
}

package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/orchestrator"
	"reasonix/internal/provider"
)

// TestSessionDeleteDispatchOpensPicker proves bare "/session-delete" writes the
// session list to the scrollback AND opens the interactive picker overlay.
func TestSessionDeleteDispatchOpensPicker(t *testing.T) {
	dir := t.TempDir()
	saveTestSession(t, filepath.Join(dir, "a.jsonl"), "alpha prompt")
	saveTestSession(t, filepath.Join(dir, "b.jsonl"), "beta prompt")

	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	m := newTestChatTUI()
	m.width = 80
	m.ctrl = control.New(control.Options{Executor: exec, SessionDir: dir, Label: "test"})

	if cmd := m.runSlashCommand("/session-delete"); cmd != nil {
		t.Fatal("/session-delete should not return a tea.Cmd")
	}
	if m.sessionDeletePick == nil {
		t.Fatal("bare /session-delete should open the picker")
	}
	if len(m.sessionDeletePick.sessions) != 2 {
		t.Fatalf("picker should have 2 sessions, got %d", len(m.sessionDeletePick.sessions))
	}
	out := strings.Join(m.transcript, "\n")
	if !strings.Contains(out, "alpha prompt") || !strings.Contains(out, "beta prompt") {
		t.Fatalf("scrollback should contain session previews:\n%s", out)
	}
}

// TestSessionDeletePickerEscDismisses proves pressing Esc closes the picker.
func TestSessionDeletePickerEscDismisses(t *testing.T) {
	dir := t.TempDir()
	saveTestSession(t, filepath.Join(dir, "a.jsonl"), "alpha prompt")

	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	m := newTestChatTUI()
	m.ctrl = control.New(control.Options{Executor: exec, SessionDir: dir, Label: "test"})

	m.runSlashCommand("/session-delete")
	if m.sessionDeletePick == nil {
		t.Fatal("bare /session-delete should open the picker")
	}

	next, _ := m.handleSessionDeletePickerKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(chatTUI)
	if m.sessionDeletePick != nil {
		t.Fatal("picker should close on Esc")
	}
}

// TestSessionDeletePickerEnterMovesToConfirm proves Enter on the picker moves
// the selection into the destructive confirmation overlay, not straight to
// deletion.
func TestSessionDeletePickerEnterMovesToConfirm(t *testing.T) {
	dir := t.TempDir()
	saveTestSession(t, filepath.Join(dir, "a.jsonl"), "alpha prompt")

	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	m := newTestChatTUI()
	m.ctrl = control.New(control.Options{Executor: exec, SessionDir: dir, Label: "test"})

	m.runSlashCommand("/session-delete")
	if m.sessionDeletePick == nil {
		t.Fatal("bare /session-delete should open the picker")
	}

	next, _ := m.handleSessionDeletePickerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(chatTUI)
	if m.sessionDeletePick != nil {
		t.Fatal("picker should close after selection")
	}
	if m.deleteConfirm == nil {
		t.Fatal("selection should open the confirmation overlay")
	}
}

// TestSessionDeleteConfirmDeletesFile drives the confirmation to completion and
// asserts the transcript file is gone while the active session survives.
func TestSessionDeleteConfirmDeletesFile(t *testing.T) {
	dir := t.TempDir()
	active := agent.NewSession("sys")
	active.Add(provider.Message{Role: provider.RoleUser, Content: "active prompt"})
	active.Add(provider.Message{Role: provider.RoleAssistant, Content: "reply"})
	exec := agent.New(nil, nil, active, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{Executor: exec, SessionDir: dir, Label: "test"})
	ctrl.SetSessionPath(filepath.Join(dir, "active.jsonl"))
	if err := ctrl.Snapshot(); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(dir, "target.jsonl")
	saveTestSession(t, target, "target prompt")

	m := newTestChatTUI()
	m.ctrl = ctrl
	m.deleteConfirm = &deleteConfirm{session: target, label: "target.jsonl"}
	next, _ := m.confirmDeleteSession()
	m = next.(chatTUI)

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target session should be deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "active.jsonl")); err != nil {
		t.Fatalf("active session should survive: %v", err)
	}
	if m.deleteConfirm != nil {
		t.Fatal("confirm overlay should close after deletion")
	}
}

// TestSessionDeleteIndexTargetsSession proves "/session-delete <n>" resolves the
// 1-based recent-session index into the confirmation overlay.
func TestSessionDeleteIndexTargetsSession(t *testing.T) {
	dir := t.TempDir()
	saveTestSession(t, filepath.Join(dir, "a.jsonl"), "first")
	saveTestSession(t, filepath.Join(dir, "b.jsonl"), "second")

	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	m := newTestChatTUI()
	m.ctrl = control.New(control.Options{Executor: exec, SessionDir: dir, Label: "test"})

	m.runSessionDeleteCommand("/session-delete 1")
	if m.deleteConfirm == nil {
		t.Fatal("indexed /session-delete should open the confirmation overlay")
	}
}

// TestSessionDeleteRefusesActiveSession proves deleting the currently-active
// session is refused without opening the confirm overlay.
func TestSessionDeleteRefusesActiveSession(t *testing.T) {
	dir := t.TempDir()
	active := agent.NewSession("sys")
	active.Add(provider.Message{Role: provider.RoleUser, Content: "active prompt"})
	active.Add(provider.Message{Role: provider.RoleAssistant, Content: "reply"})
	exec := agent.New(nil, nil, active, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{Executor: exec, SessionDir: dir, Label: "test"})
	ctrl.SetSessionPath(filepath.Join(dir, "active.jsonl"))
	if err := ctrl.Snapshot(); err != nil {
		t.Fatal(err)
	}

	m := newTestChatTUI()
	m.ctrl = ctrl

	target := 0
	for i, s := range recentSessions(dir) {
		if s.Path == ctrl.SessionPath() {
			target = i + 1
		}
	}
	if target == 0 {
		t.Fatal("active session not listed by recentSessions")
	}

	m.runSessionDeleteCommand("/session-delete " + strconv.Itoa(target))
	if m.deleteConfirm != nil {
		t.Fatal("active session must not open the confirmation overlay")
	}
}

// TestSessionDeleteOrchestrationChildResetsAgent proves deleting an
// orchestration child session resets the child controller and purges the file,
// so the next SaveSessions cannot resurrect it.
func TestSessionDeleteOrchestrationChildResetsAgent(t *testing.T) {
	dir := t.TempDir()
	childPath := filepath.Join(dir, "orchestrator_git.jsonl")
	saveTestSession(t, childPath, "git prompt")

	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	childCtrl := control.New(control.Options{Executor: exec, SessionDir: dir, Label: "git"})
	childCtrl.SetSessionPath(childPath)

	orc := orchestrator.New(event.Discard)
	orc.SetSessionDir(dir)
	orc.AddAgent("git", childCtrl, config.OrchestratorAgentEntry{Name: "git"})

	m := newTestChatTUI()
	m.ctrl = control.New(control.Options{Executor: exec, SessionDir: dir, Label: "test"})
	m.orc = orc

	if err := m.deleteSessionPath(childPath); err != nil {
		t.Fatalf("deleteSessionPath: %v", err)
	}
	if _, err := os.Stat(childPath); !os.IsNotExist(err) {
		t.Fatalf("orchestrator child session should be deleted: %v", err)
	}
	if got := childCtrl.SessionPath(); got != childPath {
		t.Fatalf("child session should be re-pinned to %q, got %q", childPath, got)
	}
}

// TestSessionDeleteArgCompletionListsSessions proves "/session-delete " opens an
// indexed menu of the saved sessions.
func TestSessionDeleteArgCompletionListsSessions(t *testing.T) {
	dir := t.TempDir()
	saveTestSession(t, filepath.Join(dir, "a.jsonl"), "first")
	saveTestSession(t, filepath.Join(dir, "b.jsonl"), "second")

	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	m := newTestChatTUI()
	m.ctrl = control.New(control.Options{Executor: exec, SessionDir: dir, Label: "test"})

	m.input.SetValue("/session-delete ")
	m.updateCompletion()
	if !m.completion.active || m.completion.kind != compSlashArg {
		t.Fatalf("/session-delete should open argument completion: %+v", m.completion)
	}
	if got := labels(m.completion.items); len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("session-delete completion = %v, want [1 2]", got)
	}
}

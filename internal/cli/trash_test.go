package cli

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
)

func TestTrashCommandsRestoreAndPurge(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "trashme.jsonl")
	if err := os.WriteFile(sessionPath, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agent.TrashSessionFiles(dir, sessionPath); err != nil {
		t.Fatalf("trash: %v", err)
	}

	ctrl := control.New(control.Options{SessionDir: dir})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80, nil)

	m.runTrashCommand("/trash")
	trashPath := filepath.Join(dir, agent.SessionTrashDir, "trashme.jsonl", "trashme.jsonl")
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("session should remain in trash after /trash: %v", err)
	}

	m.runRestoreCommand("/restore 1")
	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("session should be restored by /restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, agent.SessionTrashDir, "trashme.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("trash item should be gone after /restore: %v", err)
	}

	if err := agent.TrashSessionFiles(dir, sessionPath); err != nil {
		t.Fatalf("re-trash: %v", err)
	}
	m.runPurgeCommand("/purge 1")
	if _, err := os.Stat(filepath.Join(dir, agent.SessionTrashDir, "trashme.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("trash item should be purged after /purge: %v", err)
	}
}

func TestTrashCommandsRejectBadIndex(t *testing.T) {
	dir := t.TempDir()
	ctrl := control.New(control.Options{SessionDir: dir})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80, nil)

	m.runRestoreCommand("/restore 0")
	m.runRestoreCommand("/restore abc")
	m.runRestoreCommand("/restore")
	m.runPurgeCommand("/purge 999")
}

func TestCLISessionRemovalMovesToTrash(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "clear.jsonl")
	if err := os.WriteFile(sessionPath, []byte(`{"role":"user","content":"data"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cliSessionRemoval(sessionPath); err != nil {
		t.Fatalf("cliSessionRemoval: %v", err)
	}
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatal("session should be removed from active dir")
	}
	if _, err := os.Stat(filepath.Join(dir, agent.SessionTrashDir, "clear.jsonl", "clear.jsonl")); err != nil {
		t.Fatalf("session should be in trash: %v", err)
	}
}
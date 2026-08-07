package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrashSessionFilesMovesGoalState(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")
	os.WriteFile(sessionPath, []byte(`{"role":"user","content":"hi"}`+"\n"), 0o644)
	goal := GoalStateSidecar(sessionPath)
	os.WriteFile(goal, []byte(`{"todos":[]}`), 0o644)

	if err := TrashSessionFiles(dir, sessionPath); err != nil {
		t.Fatalf("trash: %v", err)
	}
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatal("session should be gone from active dir")
	}
	if _, err := os.Stat(goal); !os.IsNotExist(err) {
		t.Fatal("goal-state should be gone from active dir")
	}
	trashed := filepath.Join(dir, SessionTrashDir, "session.jsonl", "session.jsonl")
	if _, err := os.Stat(trashed); err != nil {
		t.Fatalf("session should be in trash: %v", err)
	}
	if _, err := os.Stat(trashedGoalState(dir, "session.jsonl")); err != nil {
		t.Fatalf("goal-state should be in trash: %v", err)
	}
}

func TestRestoreTrashedSessionRestoresGoalState(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")
	os.WriteFile(sessionPath, []byte(`{"role":"user","content":"hi"}`+"\n"), 0o644)
	os.WriteFile(GoalStateSidecar(sessionPath), []byte(`{"todos":[]}`), 0o644)

	if err := TrashSessionFiles(dir, sessionPath); err != nil {
		t.Fatalf("trash: %v", err)
	}
	trashed := filepath.Join(dir, SessionTrashDir, "session.jsonl", "session.jsonl")
	if err := RestoreTrashedSession(dir, trashed); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("session should be restored: %v", err)
	}
	if _, err := os.Stat(GoalStateSidecar(sessionPath)); err != nil {
		t.Fatalf("goal-state should be restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, SessionTrashDir, "session.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("trash item should be removed after restore, stat err = %v", err)
	}
}

func TestPurgeTrashedSessionRemovesGoalState(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")
	os.WriteFile(sessionPath, []byte("data"), 0o644)
	os.WriteFile(GoalStateSidecar(sessionPath), []byte(`{"todos":[]}`), 0o644)

	if err := TrashSessionFiles(dir, sessionPath); err != nil {
		t.Fatalf("trash: %v", err)
	}
	trashed := filepath.Join(dir, SessionTrashDir, "session.jsonl", "session.jsonl")
	if err := PurgeTrashedSession(dir, trashed); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, SessionTrashDir, "session.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("trash item should be removed after purge, stat err = %v", err)
	}
}

func TestTrashSessionFilesRejectsExistingItem(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.jsonl")
	os.WriteFile(sessionPath, []byte("data"), 0o644)
	itemDir := filepath.Join(dir, SessionTrashDir, "session.jsonl")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := TrashSessionFiles(dir, sessionPath); err == nil {
		t.Fatal("trash should reject an existing item dir")
	}
	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("session should remain active after rejected trash: %v", err)
	}
}

func TestMoveSessionFilesToTrashReusesExistingItem(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "partial.jsonl")
	os.WriteFile(sessionPath, []byte("data"), 0o644)
	os.WriteFile(GoalStateSidecar(sessionPath), []byte(`{"todos":[]}`), 0o644)
	itemDir := filepath.Join(dir, SessionTrashDir, "partial.jsonl")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := MoveSessionFilesToTrash(dir, sessionPath); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatal("session should be moved")
	}
	if _, err := os.Stat(filepath.Join(itemDir, "partial.jsonl")); err != nil {
		t.Fatalf("session should be in existing trash dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(itemDir, "partial.goal-state.json")); err != nil {
		t.Fatalf("goal-state should be in existing trash dir: %v", err)
	}
}

func trashedGoalState(dir, key string) string {
	return filepath.Join(dir, SessionTrashDir, key, goalNameInTrash(key))
}

func goalNameInTrash(key string) string {
	return trimJSONLExt(key) + ".goal-state.json"
}

func trimJSONLExt(name string) string {
	return name[:len(name)-len(".jsonl")]
}

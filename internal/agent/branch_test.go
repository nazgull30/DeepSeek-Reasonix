package agent

import (
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

func TestBranchMetaRoundTripAndList(t *testing.T) {
	dir := t.TempDir()
	rootPath := filepath.Join(dir, "root.jsonl")
	childPath := filepath.Join(dir, "child.jsonl")

	root := NewSession("sys")
	root.Add(provider.Message{Role: provider.RoleUser, Content: "root prompt"})
	root.Add(provider.Message{Role: provider.RoleAssistant, Content: "root reply"})
	if err := root.Save(rootPath); err != nil {
		t.Fatal(err)
	}
	if err := TouchBranchMeta(rootPath); err != nil {
		t.Fatal(err)
	}

	child := NewSession("sys")
	child.Add(provider.Message{Role: provider.RoleUser, Content: "child prompt"})
	child.Add(provider.Message{Role: provider.RoleAssistant, Content: "child reply"})
	if err := child.Save(childPath); err != nil {
		t.Fatal(err)
	}
	if err := SaveBranchMeta(childPath, BranchMeta{Name: "experiment", ParentID: BranchID(rootPath), ForkTurn: 2}); err != nil {
		t.Fatal(err)
	}

	branches, err := ListBranches(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 2 {
		t.Fatalf("branches = %d, want 2", len(branches))
	}
	var rootFound, childFound bool
	for _, b := range branches {
		if b.ID == "root" {
			rootFound = true
		}
		if b.ParentID == "root" && b.Name == "experiment" {
			childFound = true
		}
	}
	if !rootFound {
		t.Fatal("root branch not found")
	}
	if !childFound {
		t.Fatalf("child with parent root and name experiment not found among %+v", branches)
	}
}

func TestListBranchesSkipsCleanupPending(t *testing.T) {
	dir := t.TempDir()
	visiblePath := filepath.Join(dir, "visible.jsonl")
	pendingPath := filepath.Join(dir, "pending.jsonl")

	visible := NewSession("sys")
	visible.Add(provider.Message{Role: provider.RoleUser, Content: "visible prompt"})
	visible.Add(provider.Message{Role: provider.RoleAssistant, Content: "visible reply"})
	if err := visible.Save(visiblePath); err != nil {
		t.Fatal(err)
	}
	if err := TouchBranchMeta(visiblePath); err != nil {
		t.Fatal(err)
	}

	pending := NewSession("sys")
	pending.Add(provider.Message{Role: provider.RoleUser, Content: "pending prompt"})
	pending.Add(provider.Message{Role: provider.RoleAssistant, Content: "pending reply"})
	if err := pending.Save(pendingPath); err != nil {
		t.Fatal(err)
	}
	if err := SaveBranchMeta(pendingPath, BranchMeta{Name: "pending experiment"}); err != nil {
		t.Fatal(err)
	}
	if err := MarkCleanupPending(pendingPath, "delete"); err != nil {
		t.Fatal(err)
	}

	branches, err := ListBranches(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 {
		t.Fatalf("branches = %d, want 1: %+v", len(branches), branches)
	}
	if branches[0].Path != visiblePath {
		t.Fatalf("listed branch path = %q, want %q", branches[0].Path, visiblePath)
	}
}

func TestListBranchesSkipsSessionWithoutReply(t *testing.T) {
	dir := t.TempDir()
	emptyPath := filepath.Join(dir, "empty.jsonl")
	realPath := filepath.Join(dir, "real.jsonl")

	empty := NewSession("sys")
	empty.Add(provider.Message{Role: provider.RoleUser, Content: "orphaned prompt"})
	if err := empty.Save(emptyPath); err != nil {
		t.Fatal(err)
	}
	if err := TouchBranchMeta(emptyPath); err != nil {
		t.Fatal(err)
	}

	real := NewSession("sys")
	real.Add(provider.Message{Role: provider.RoleUser, Content: "prompt"})
	real.Add(provider.Message{Role: provider.RoleAssistant, Content: "reply"})
	if err := real.Save(realPath); err != nil {
		t.Fatal(err)
	}
	if err := TouchBranchMeta(realPath); err != nil {
		t.Fatal(err)
	}

	branches, err := ListBranches(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 || branches[0].Path != realPath {
		t.Fatalf("branches = %+v, want only the session with a reply", branches)
	}
}

func TestSessionModelRoundTripPreservesActivity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	session := NewSession("sys")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "hello"})
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadSessionModel(path); ok {
		t.Fatal("fresh session should not have a stored model")
	}
	meta, err := EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := SetBranchModelPreserveUpdated(path, "openrouter/anthropic/claude-sonnet"); err != nil {
		t.Fatal(err)
	}
	model, ok := LoadSessionModel(path)
	if !ok || model != "openrouter/anthropic/claude-sonnet" {
		t.Fatalf("LoadSessionModel = %q, %v", model, ok)
	}
	updated, ok, err := LoadBranchMeta(path)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta ok=%v err=%v", ok, err)
	}
	if !updated.UpdatedAt.Equal(meta.UpdatedAt) {
		t.Fatalf("model write refreshed activity: before=%s after=%s", meta.UpdatedAt, updated.UpdatedAt)
	}
}

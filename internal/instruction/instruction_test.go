package instruction

import (
	"strings"
	"testing"

	"reasonix/internal/memory"
)

func TestExtractHostChecksFromFullDoc(t *testing.T) {
	docs := []memory.Source{{
		Path:  "AGENTS.md",
		Scope: memory.ScopeProject,
		Body: strings.Join([]string{
			"# Project rules",
			"## Reasonix host checks",
			"- verify: go test ./internal/...",
			"* verify: git diff --check",
			"- verify: go test ./internal/...",
			"- note: ignored",
			"## Other",
			"- verify: go vet ./...",
		}, "\n"),
	}}

	checks := ExtractHostChecks(docs)
	if len(checks) != 3 {
		t.Fatalf("checks len = %d, want 3: %#v", len(checks), checks)
	}
	if checks[0].Command != "go test ./internal/..." || checks[0].SourcePath != "AGENTS.md" || checks[0].Line != 3 {
		t.Fatalf("first check = %#v", checks[0])
	}
	if checks[1].Command != "git diff --check" || checks[1].SourcePath != "AGENTS.md" || checks[1].Line != 4 {
		t.Fatalf("second check = %#v", checks[1])
	}
	if checks[2].Command != "go vet ./..." || checks[2].SourcePath != "AGENTS.md" || checks[2].Line != 8 {
		t.Fatalf("third check = %#v", checks[2])
	}
}

func TestExtractHostChecksFromAnywhereInDoc(t *testing.T) {
	docs := []memory.Source{{
		Path: "REASONIX.md",
		Body: "Always run go test before committing.\n\n- verify: go test ./...",
	}}

	checks := ExtractHostChecks(docs)
	if len(checks) != 1 || checks[0].Command != "go test ./..." {
		t.Fatalf("verify: anywhere in doc should be extracted: %#v", checks)
	}
}

func TestExtractHostChecksDeduplicates(t *testing.T) {
	docs := []memory.Source{{
		Path: "REASONIX.md",
		Body: "- verify: go test ./...\n- verify: go test ./...\n- verify: go vet ./...",
	}}

	checks := ExtractHostChecks(docs)
	if len(checks) != 2 {
		t.Fatalf("checks len = %d, want 2: %#v", len(checks), checks)
	}
	if checks[0].Command != "go test ./..." {
		t.Fatalf("first check = %#v", checks[0])
	}
	if checks[1].Command != "go vet ./..." {
		t.Fatalf("second check = %#v", checks[1])
	}
}

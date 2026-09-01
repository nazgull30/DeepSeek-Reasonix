package boot

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/event"
)

// TestBuildMainAgentSkipsOrchestratorMainSkipSkills proves that skills named in
// [orchestrator].main_skip_skills are hidden from the top-level agent's index
// even though they remain on disk (so a child can still be allowlisted them).
func TestBuildMainAgentSkipsOrchestratorMainSkipSkills(t *testing.T) {
	dir := robustTempDir(t)
	home := robustTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(dir)
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[orchestrator]
main_skip_skills = ["deploy"]

[[providers]]
name = "test-model"
kind = "openai"
base_url = "https://example.invalid"
model = "x"
api_key_env = "REASONIX_TEST_KEY_UNSET"
`)
	writeFile(t, dir, ".reasonix/skills/deploy.md", "---\ndescription: deploy to prod\n---\nplaybook")
	writeFile(t, dir, ".reasonix/skills/other.md", "---\ndescription: a normal skill\n---\nplaybook")

	ctrl, err := Build(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	for _, s := range ctrl.Skills() {
		if s.Name == "deploy" {
			t.Fatalf("main agent should NOT see main_skip_skills skill 'deploy'; got %v", ctrl.Skills())
		}
	}
	var hasOther bool
	for _, s := range ctrl.Skills() {
		if s.Name == "other" {
			hasOther = true
		}
	}
	if !hasOther {
		t.Fatalf("main agent should still see non-skipped skills; got %v", ctrl.Skills())
	}
}

// TestBuildChildAgentSkillAllowlist proves that an orchestrator child agent with
// a skills allowlist sees ONLY those skills even when the main agent was told to
// skip them — the two filters are independent and scoped per controller.
func TestBuildChildAgentSkillAllowlist(t *testing.T) {
	dir := robustTempDir(t)
	home := robustTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(dir)
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[orchestrator]
main_skip_skills = ["deploy"]

[[providers]]
name = "test-model"
kind = "openai"
base_url = "https://example.invalid"
model = "x"
api_key_env = "REASONIX_TEST_KEY_UNSET"
`)
	writeFile(t, dir, ".reasonix/skills/deploy.md", "---\ndescription: deploy to prod\n---\nplaybook")
	writeFile(t, dir, ".reasonix/skills/other.md", "---\ndescription: a normal skill\n---\nplaybook")

	ctrl, err := Build(context.Background(), Options{
		AgentName:      "deployer",
		SkillAllowlist: []string{"deploy"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	var hasDeploy, hasOther, hasBuiltin bool
	for _, s := range ctrl.Skills() {
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
		t.Fatalf("child agent should see allowlisted skill 'deploy'; got %v", ctrl.Skills())
	}
	if hasOther {
		t.Fatalf("child agent should NOT see non-allowlisted skill 'other'; got %v", ctrl.Skills())
	}
	if hasBuiltin {
		t.Fatalf("child agent should NOT see builtin skills outside the allowlist; got %v", ctrl.Skills())
	}
}

// TestBuildChildAgentSkillDenylist proves skip_skills removes a skill from a
// child even when no allowlist is set.
func TestBuildChildAgentSkillDenylist(t *testing.T) {
	dir := robustTempDir(t)
	home := robustTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(dir)
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[[providers]]
name = "test-model"
kind = "openai"
base_url = "https://example.invalid"
model = "x"
api_key_env = "REASONIX_TEST_KEY_UNSET"
`)
	writeFile(t, dir, ".reasonix/skills/deploy.md", "---\ndescription: deploy to prod\n---\nplaybook")

	ctrl, err := Build(context.Background(), Options{
		AgentName:     "deployer",
		SkillDenylist: []string{"deploy"},
		Stderr:        io.Discard,
		Sink:          event.Discard,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	for _, s := range ctrl.Skills() {
		if s.Name == "deploy" {
			t.Fatalf("child agent should NOT see skipped skill 'deploy'; got %v", ctrl.Skills())
		}
	}
}

// TestBuildSkillsIndexRespectsFilter proves the injected user-attachment skills
// index reflects the per-controller skill filter, not just Skills().
func TestBuildSkillsIndexRespectsFilter(t *testing.T) {
	dir := robustTempDir(t)
	home := robustTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(dir)
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[orchestrator]
main_skip_skills = ["deploy"]

[[providers]]
name = "test-model"
kind = "openai"
base_url = "https://example.invalid"
model = "x"
api_key_env = "REASONIX_TEST_KEY_UNSET"
`)
	writeFile(t, dir, ".reasonix/skills/deploy.md", "---\ndescription: deploy to prod\n---\nplaybook")

	ctrl, err := Build(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	var index strings.Builder
	for _, m := range ctrl.History() {
		index.WriteString(m.Content)
	}
	if strings.Contains(index.String(), "deploy") {
		t.Fatalf("skills index attachment should not mention skipped skill 'deploy':\n%s", index.String())
	}
}
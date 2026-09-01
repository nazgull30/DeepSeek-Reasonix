package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestOrchestratorSkillsParsed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte(`
config_version = 2

[orchestrator]
main_skip_skills = ["deploy", "deploy"]   # duplicate should be dropped

[[orchestrator.agents]]
name = "deployer"
model = "deepseek/deepseek-v4-flash"
skills = ["deploy"]
skip_skills = ["review"]

[[orchestrator.agents]]
name = "other"
skills = []
skip_skills = []
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForRoot(root)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}

	got := cfg.OrchestratorMainSkipSkills()
	if len(got) != 1 || got[0] != "deploy" {
		t.Fatalf("OrchestratorMainSkipSkills = %v, want [deploy]", got)
	}

	if len(cfg.Orchestrator.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(cfg.Orchestrator.Agents))
	}
	deployer := cfg.Orchestrator.Agents[0]
	if al := deployer.SkillAllowlist(); len(al) != 1 || al[0] != "deploy" {
		t.Fatalf("deployer SkillAllowlist = %v, want [deploy]", al)
	}
	if dl := deployer.SkillDenylist(); len(dl) != 1 || dl[0] != "review" {
		t.Fatalf("deployer SkillDenylist = %v, want [review]", dl)
	}
	if al := cfg.Orchestrator.Agents[1].SkillAllowlist(); len(al) != 0 {
		t.Fatalf("other SkillAllowlist = %v, want empty", al)
	}
}

func TestOrchestratorMainSkipSkillsInvalidDropped(t *testing.T) {
	c := Default()
	c.Orchestrator.MainSkipSkills = []string{"", "ok-name", "Bad Name!", "ok-name"}
	got := c.OrchestratorMainSkipSkills()
	if len(got) != 1 || got[0] != "ok-name" {
		t.Fatalf("OrchestratorMainSkipSkills = %v, want [ok-name]", got)
	}
}

func TestRenderTOMLRoundTripsOrchestratorMainSkipSkills(t *testing.T) {
	c := Default()
	c.Orchestrator.MainSkipSkills = []string{"deploy"}

	out := RenderTOML(c)
	if !strings.Contains(out, "[orchestrator]") {
		t.Fatalf("render should emit [orchestrator] section:\n%s", out)
	}
	if !strings.Contains(out, `main_skip_skills = ["deploy"]`) {
		t.Fatalf("render should emit main_skip_skills:\n%s", out)
	}

	got := parseConfigString(t, out)
	if got := got.OrchestratorMainSkipSkills(); len(got) != 1 || got[0] != "deploy" {
		t.Fatalf("round-tripped main_skip_skills = %v, want [deploy]", got)
	}
}

func parseConfigString(t *testing.T, raw string) *Config {
	t.Helper()
	var got Config
	if _, err := toml.Decode(raw, &got); err != nil {
		t.Fatalf("rendered TOML does not parse: %v", err)
	}
	return &got
}

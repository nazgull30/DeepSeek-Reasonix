package boot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/event"
)

// TestBuildColdCacheChildKeepsConnectStub is the end-to-end regression test for
// the cold-cache git agent failure: with an empty schema cache a lazy/background
// MCP server registers only its "mcp__<server>__connect" stub, and a strict
// per-agent allowlist must not strip it — otherwise the child agent is left with
// nothing but bash and no path to bootstrap the server's real tools.
func TestBuildColdCacheChildKeepsConnectStub(t *testing.T) {
	dir := robustTempDir(t)
	home := robustTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("REASONIX_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Chdir(dir)

	cfg := `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[[providers]]
name = "test-model"
kind = "openai"
base_url = "https://example.invalid"
model = "x"
api_key_env = "REASONIX_TEST_KEY_UNSET"

[[orchestrator.agents]]
name = "git"
model = "test-model"
system_prompt = "git agent"
persist = true
tools = [
    "mcp__gitlab__get_merge_request",
    "mcp__gitlab__get_issue",
    "bash",
]

[[plugins]]
name    = "gitlab"
command = "definitely-not-a-real-binary-xyz"
args    = ["-y", "@zereight/mcp-gitlab"]
agents  = ["git"]
env     = { GITLAB_PERSONAL_ACCESS_TOKEN = "${GITLAB_TOKEN}" }
`
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	allowlist := []string{
		"mcp__gitlab__get_merge_request",
		"mcp__gitlab__get_issue",
		"bash",
	}

	ctrl, err := Build(context.Background(), Options{
		AgentName:     "git",
		Sink:          event.Discard,
		ToolAllowlist: allowlist,
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	// Cold cache (REASONIX_CACHE_HOME is empty): only the bootstrap stub
	// registers, and it must survive the strict allowlist trim so the model can
	// drive the handshake that installs the real allowlisted tools.
	if _, ok := ctrl.Registry().Get("mcp__gitlab__connect"); !ok {
		names := ctrl.Registry().Names()
		t.Fatalf("cold-cache git child lost mcp__gitlab__connect; registry has %v", names)
	}
	for _, name := range ctrl.Registry().Names() {
		if name != "mcp__gitlab__connect" && name != "bash" {
			t.Errorf("cold-cache git child has unexpected tool %q", name)
		}
	}
}

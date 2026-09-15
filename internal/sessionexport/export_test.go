package sessionexport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

// writeJSONL writes provider messages as the newline-delimited JSON transcript
// format Save/LoadSession use.
func writeJSONL(t *testing.T, path string, msgs ...provider.Message) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			t.Fatalf("encode message: %v", err)
		}
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestExportCopiesEverythingAndBuildsManifest(t *testing.T) {
	sessionDir := t.TempDir()
	workspace := t.TempDir()
	archive := t.TempDir()

	mainPath := filepath.Join(sessionDir, "sess-main.jsonl")
	writeJSONL(t, mainPath,
		provider.Message{Role: provider.RoleUser, Content: "first question"},
		provider.Message{Role: provider.RoleAssistant, Content: "an answer"},
		provider.Message{Role: provider.RoleUser, Content: "second question"},
		provider.Message{Role: provider.RoleAssistant, Content: "another answer"},
	)
	if err := agent.SaveBranchMeta(mainPath, agent.BranchMeta{
		ID:    "sess-main",
		Model: "deepseek/deepseek-chat",
		SessionUsage: &agent.SessionUsageMeta{
			PromptTokens:     1000,
			CompletionTokens: 100,
			CacheHitTokens:   800,
			CacheMissTokens:  200,
			TotalTokens:      1100,
		},
	}); err != nil {
		t.Fatalf("save main meta: %v", err)
	}

	// Sub-agent lineage.
	subDir := filepath.Join(sessionDir, "subagents")
	writeJSONL(t, filepath.Join(subDir, "sa_abc.jsonl"),
		provider.Message{Role: provider.RoleUser, Content: "delegate this"},
	)
	writeJSON(t, filepath.Join(subDir, "sa_abc.meta.json"), agent.SubagentMeta{
		Ref:           "sa_abc",
		Status:        agent.SubagentCompleted,
		Kind:          "task",
		Name:          "web-fetcher",
		ParentSession: "sess-main",
		Model:         "groq/llama",
		Usage: &agent.SessionUsageMeta{
			PromptTokens:    500,
			CacheHitTokens:  400,
			CacheMissTokens: 100,
			TotalTokens:     600,
		},
	})

	// Orchestrator agent transcript.
	orcPath := filepath.Join(sessionDir, "orchestrator_web.jsonl")
	writeJSONL(t, orcPath, provider.Message{Role: provider.RoleUser, Content: "fetch a page"})
	if err := agent.SaveBranchMeta(orcPath, agent.BranchMeta{
		ID:    "orchestrator_web",
		Model: "groq/llama",
		SessionUsage: &agent.SessionUsageMeta{
			PromptTokens:    200,
			CacheHitTokens:  150,
			CacheMissTokens: 50,
			TotalTokens:     250,
		},
	}); err != nil {
		t.Fatalf("save orchestrator meta: %v", err)
	}

	// Compaction archive + persisted tool results.
	writeJSONL(t, filepath.Join(archive, "compacted-1.jsonl"),
		provider.Message{Role: provider.RoleUser, Content: "old"},
	)
	if err := os.MkdirAll(filepath.Join(archive, "tool-results"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "tool-results", "tr1.txt"), []byte("big output"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Export(Options{
		WorkspaceRoot: workspace,
		SessionDir:    sessionDir,
		MainSession:   mainPath,
		Archive:       archive,
		Sessions:      []NamedSession{{Name: "web", Path: orcPath}},
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Directory layout.
	for _, rel := range []string{
		"manifest.json",
		"summary.md",
		filepath.Join("main", "sess-main.jsonl"),
		filepath.Join("main", "sess-main.jsonl.meta"),
		filepath.Join("subagents", "sa_abc.jsonl"),
		filepath.Join("subagents", "sa_abc.meta.json"),
		filepath.Join("sessions", "orchestrator_web.jsonl"),
		filepath.Join("sessions", "orchestrator_web.jsonl.meta"),
		filepath.Join("archive", "compacted-1.jsonl"),
		filepath.Join("tool-results", "tr1.txt"),
	} {
		if _, err := os.Stat(filepath.Join(res.Dir, rel)); err != nil {
			t.Errorf("expected exported file %s: %v", rel, err)
		}
	}
	if !strings.HasPrefix(res.Dir, filepath.Join(workspace, ".reasonix", "session-export")) {
		t.Errorf("export dir %s not under workspace export root", res.Dir)
	}

	// Manifest contents.
	b, err := os.ReadFile(filepath.Join(res.Dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if m.Main.ID != "sess-main" || m.Main.Model != "deepseek/deepseek-chat" {
		t.Errorf("main entry = %+v", m.Main)
	}
	if m.Main.Turns != 2 {
		t.Errorf("main turns = %d, want 2", m.Main.Turns)
	}
	if len(m.Subagents) != 1 || m.Subagents[0].Ref != "sa_abc" || m.Subagents[0].Name != "web-fetcher" {
		t.Errorf("subagents = %+v", m.Subagents)
	}
	if len(m.Sessions) != 1 || m.Sessions[0].Name != "web" {
		t.Errorf("sessions = %+v", m.Sessions)
	}
	if m.Totals.Sessions != 2 || m.Totals.Subagents != 1 {
		t.Errorf("totals counts = %+v", m.Totals)
	}
	if m.Totals.PromptTokens != 1700 || m.Totals.CacheHitTokens != 1350 || m.Totals.CacheMissTokens != 350 {
		t.Errorf("totals tokens = %+v", m.Totals)
	}
	if len(m.ArchiveFiles) != 1 || len(m.ToolResults) != 1 {
		t.Errorf("archive/tool results = %v / %v", m.ArchiveFiles, m.ToolResults)
	}

	// Summary mentions the cache hit rate.
	sb, err := os.ReadFile(filepath.Join(res.Dir, "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sb), "cache hit rate") {
		t.Errorf("summary missing cache hit rate:\n%s", sb)
	}
	if !strings.Contains(string(sb), "web-fetcher") {
		t.Errorf("summary missing subagent name:\n%s", sb)
	}
}

func TestExportToleratesMissingOptionalInputs(t *testing.T) {
	workspace := t.TempDir()
	res, err := Export(Options{
		WorkspaceRoot: workspace,
		SessionDir:    t.TempDir(),
		Archive:       filepath.Join(t.TempDir(), "does-not-exist"),
	})
	if err != nil {
		t.Fatalf("Export with empty inputs: %v", err)
	}
	if res.Totals.Sessions != 0 || res.Totals.Subagents != 0 {
		t.Errorf("expected no sessions, got %+v", res.Totals)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "manifest.json")); err != nil {
		t.Errorf("manifest missing: %v", err)
	}
}

func TestBuildSummaryGradesCache(t *testing.T) {
	lo := BuildSummary(Manifest{Totals: Totals{CacheHitTokens: 10, CacheMissTokens: 90}})
	if !strings.Contains(lo, "Low hit rate") {
		t.Errorf("expected low grade, got:\n%s", lo)
	}
	hi := BuildSummary(Manifest{Totals: Totals{CacheHitTokens: 990, CacheMissTokens: 10}})
	if !strings.Contains(hi, "cache-friendly") {
		t.Errorf("expected high grade, got:\n%s", hi)
	}
	none := BuildSummary(Manifest{})
	if !strings.Contains(none, "No cache usage recorded") {
		t.Errorf("expected no-usage note, got:\n%s", none)
	}
}

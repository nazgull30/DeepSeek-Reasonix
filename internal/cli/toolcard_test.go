package cli

import (
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestToolCard(t *testing.T) {
	cases := []struct {
		name string
		args string
		want []string
		deny []string
	}{
		{"bash", `{"command":"npm test"}`, []string{"Bash", "npm test"}, nil},
		{"read_file", `{"path":"pkg/a.go"}`, []string{"Read", "pkg/a.go"}, nil},
		{"grep", `{"pattern":"TODO","path":"."}`, []string{"Search", "TODO"}, nil},
		{"wait", `{"job_ids":["bash-1","bash-2"],"timeout_seconds":300}`, []string{"Wait", "bash-1", "bash-2"}, []string{"timeout_seconds", "300", "job_ids"}},
		{"web_fetch", `{"url":"https://x.dev"}`, []string{"Fetch", "https://x.dev"}, nil},
	}
	for _, c := range cases {
		got := toolCard(c.name, c.args, 120)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q missing %q", c.name, got, w)
			}
		}
		for _, d := range c.deny {
			if strings.Contains(got, d) {
				t.Errorf("%s: %q should not contain raw arg %q", c.name, got, d)
			}
		}
	}
}

func TestToolCardUnknownFallsBackToName(t *testing.T) {
	if got := toolCard("frobnicate", `{}`, 80); !strings.Contains(got, "frobnicate") {
		t.Errorf("unknown tool should show its raw name, got %q", got)
	}
}

func TestTaskCardRenderOpen(t *testing.T) {
	got := taskCardRender(`{"description":"Locate the tests"}`, 80, taskCardOpen, nil, "")
	if !strings.Contains(got, "Locate the tests") {
		t.Errorf("open task should contain description, got %q", got)
	}
	if strings.ContainsAny(got, "✓⊘") {
		t.Errorf("open task should have no status glyph, got %q", got)
	}
}

func TestTaskCardRenderComplete(t *testing.T) {
	u := &provider.Usage{
		PromptTokens:     30_000,
		CompletionTokens: 4_000,
		TotalTokens:      34_000,
		CacheHitTokens:   25_000,
		CacheMissTokens:  5_000,
		ReasoningTokens:  1_200,
	}
	got := taskCardRender(`{"description":"Locate the tests"}`, 80, taskCardComplete, u, "")
	for _, want := range []string{"✓", "Locate the tests", "34.0K", "25.0K", "5.0K", "2.8K", "1.2K"} {
		if !strings.Contains(got, want) {
			t.Errorf("complete task card %q missing %q", got, want)
		}
	}
}

func TestTaskCardRenderFailed(t *testing.T) {
	got := taskCardRender(`{"description":"Locate the tests"}`, 80, taskCardFailed, nil, "timeout exceeded")
	if !strings.Contains(got, "⊘") || !strings.Contains(got, "Locate the tests") || !strings.Contains(got, "timeout exceeded") {
		t.Errorf("failed task card should show error inside box, got %q", got)
	}
}

func TestTaskCardRenderFallbackDesc(t *testing.T) {
	got := taskCardRender(`{"prompt":"do the thing"}`, 80, taskCardOpen, nil, "")
	if !strings.Contains(got, "Task") {
		t.Errorf("empty description should fall back to Task, got %q", got)
	}
}

func TestTaskUsageLineEmpty(t *testing.T) {
	if got := taskUsageLine(nil); got != "" {
		t.Errorf("nil usage should yield empty line, got %q", got)
	}
	if got := taskUsageLine(&provider.Usage{}); got != "" {
		t.Errorf("zero usage should yield empty line, got %q", got)
	}
}

func TestAddUsage(t *testing.T) {
	a := &provider.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CacheHitTokens: 4, CacheMissTokens: 6, ReasoningTokens: 2}
	b := &provider.Usage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30, CacheHitTokens: 8, CacheMissTokens: 12, ReasoningTokens: 4}
	sum := addUsage(a, b)
	if sum.PromptTokens != 30 || sum.CompletionTokens != 15 || sum.TotalTokens != 45 ||
		sum.CacheHitTokens != 12 || sum.CacheMissTokens != 18 || sum.ReasoningTokens != 6 {
		t.Errorf("addUsage sum wrong: %+v", sum)
	}
	if got := addUsage(nil, b); got.PromptTokens != 20 || got.CacheHitTokens != 8 || got.TotalTokens != 30 {
		t.Errorf("addUsage(nil, b) should copy b's fields, got %+v", got)
	}
}

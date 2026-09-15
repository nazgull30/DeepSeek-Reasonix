package sessionexport

import (
	"fmt"
	"sort"
	"strings"

	"reasonix/internal/agent"
)

// BuildSummary renders a human-readable report from an export manifest. It is
// written as summary.md inside the export directory so a snapshot can be
// inspected (and shared) without opening the raw JSONL transcripts.
func BuildSummary(m Manifest) string {
	var b strings.Builder

	b.WriteString("# Reasonix session export\n\n")
	if m.WorkspaceRoot != "" {
		fmt.Fprintf(&b, "- Workspace: `%s`\n", m.WorkspaceRoot)
	}
	fmt.Fprintf(&b, "- Exported at: %s\n", m.ExportedAt.Local().Format("2006-01-02 15:04:05"))

	// Main + extra sessions.
	fmt.Fprintf(&b, "\n## Sessions (%d)\n\n", m.Totals.Sessions)
	entries := make([]SessionEntry, 0, m.Totals.Sessions)
	if m.Main.ID != "" {
		e := SessionEntry{Name: "main", ID: m.Main.ID, File: m.Main.File, Model: m.Main.Model, Turns: m.Main.Turns, Preview: m.Main.Preview, Usage: m.Main.Usage}
		entries = append(entries, e)
	}
	entries = append(entries, m.Sessions...)
	if len(entries) == 0 {
		b.WriteString("No transcripts.\n")
	} else {
		b.WriteString("| name | id | model | turns | prompt | cached | new | hit% | cost\n")
		b.WriteString("|------|----|-------|-------|--------|--------|-----|------|-----\n")
		for _, e := range entries {
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %d | %d | %s | %s\n",
				orDash(e.Name), orDash(e.ID), orDash(e.Model), e.Turns,
				u(e.Usage).PromptTokens, u(e.Usage).CacheHitTokens, u(e.Usage).CacheMissTokens,
				pct(u(e.Usage)), costStr(e.Usage))
		}
	}

	// Sub-agents.
	fmt.Fprintf(&b, "\n## Sub-agents (%d)\n\n", m.Totals.Subagents)
	if len(m.Subagents) == 0 {
		b.WriteString("None.\n")
	} else {
		b.WriteString("| ref | name | kind | status | model | turns | prompt | cached | new | hit% | cost\n")
		b.WriteString("|-----|------|------|--------|-------|-------|--------|--------|-----|------|-----\n")
		for _, s := range m.Subagents {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %d | %d | %d | %d | %s | %s\n",
				s.Ref, orDash(s.Name), orDash(s.Kind), orDash(s.Status), orDash(s.Model), s.Turns,
				u(s.Usage).PromptTokens, u(s.Usage).CacheHitTokens, u(s.Usage).CacheMissTokens,
				pct(s.Usage), costStr(s.Usage))
		}
	}

	// Archive + tool results.
	fmt.Fprintf(&b, "\n## Archived content\n\n")
	fmt.Fprintf(&b, "- Compacted/pruned history files: %d\n", len(m.ArchiveFiles))
	fmt.Fprintf(&b, "- Persisted tool results: %d\n", len(m.ToolResults))
	if len(m.ArchiveFiles) > 0 {
		b.WriteString("\nConsider how often compaction runs: each compaction rewrites the\n")
		b.WriteString("conversation prefix and resets the provider cache. See the notes below.\n")
	}

	// Totals + cache grade.
	hit, miss := m.Totals.CacheHitTokens, m.Totals.CacheMissTokens
	fmt.Fprintf(&b, "\n## Totals\n\n")
	fmt.Fprintf(&b, "- Total tokens: %d\n", m.Totals.TotalTokens)
	fmt.Fprintf(&b, "- Prompt tokens: %d (cached %d, new %d)\n", m.Totals.PromptTokens, hit, miss)
	fmt.Fprintf(&b, "- Completion tokens: %d\n", m.Totals.CompletionTokens)
	fmt.Fprintf(&b, "- Reasoning tokens: %d\n", m.Totals.ReasoningTokens)
	if m.Totals.Cost > 0 {
		fmt.Fprintf(&b, "- Cost: %s%.4f\n", orDash(m.Totals.Currency), m.Totals.Cost)
	}

	b.WriteString("\n## Cache notes\n\n")
	b.WriteString(buildCacheNotes(m))

	return b.String()
}

// buildCacheNotes returns the data-driven guidance section: overall cache grade,
// worst sub-agents by miss, and the recurring causes of prefix-cache misses.
func buildCacheNotes(m Manifest) string {
	var b strings.Builder

	// Overall cache grade.
	hit, miss := m.Totals.CacheHitTokens, m.Totals.CacheMissTokens
	if hit+miss == 0 {
		b.WriteString("No cache usage recorded yet (models/providers that don't report it).\n")
		return b.String()
	}
	rate := float64(hit) / float64(hit+miss)
	fmt.Fprintf(&b, "Overall cache hit rate: **%.1f%%**\n\n", rate*100)
	switch {
	case rate >= 0.9:
		b.WriteString("Your context prefix is very cache-friendly. Keep the system prompt and tool\nlist stable; the main remaining lever is turning more *new* prompt tokens into\n*cached* ones by avoiding prefix churn mid-session.\n")
	case rate >= 0.5:
		b.WriteString("Moderate hit rate. Look for what changes between turns (below) — a high miss\ncount usually means the prefix is being rewritten every turn (compaction,\nsummarization, live memory/doc edits, or tool schemas changing).\n")
	default:
		b.WriteString("Low hit rate. The prefix is likely rewritten on nearly every model call.\nFix the largest causes below before tuning anything else.\n")
	}
	b.WriteString("\n")

	// Worst sub-agents by miss tokens.
	type missSlice struct {
		ref  string
		miss int
	}
	var misses []missSlice
	for _, s := range m.Subagents {
		mm := u(s.Usage).CacheMissTokens
		if mm > 0 {
			misses = append(misses, missSlice{ref: s.Ref, miss: mm})
		}
	}
	if len(misses) > 0 {
		sort.Slice(misses, func(i, j int) bool { return misses[i].miss > misses[j].miss })
		b.WriteString("**Highest new (miss) tokens — sub-agents:**\n\n")
		limit := 5
		if len(misses) < limit {
			limit = len(misses)
		}
		for _, s := range misses[:limit] {
			fmt.Fprintf(&b, "- `%s`: %d new tokens\n", s.ref, s.miss)
		}
		b.WriteString("\nRepeated fresh sub-agent spawns re-send a full system prompt and re-read\nfiles from scratch. Reuse context with `continue_from=<ref>` / `fork_from=<ref>`\nand prefer `cache_from_parent` forks so children share the parent's cached prefix.\n")
		b.WriteString("\n")
	}

	b.WriteString("**Recurring causes of cache misses:**\n\n")
	b.WriteString("- **Compaction / summarization** — each pass rewrites the conversation prefix.\n  Compact only when needed; `/context` shows the current usage.\n")
	b.WriteString("- **Large or noisy tool results** — non-deterministic output (timestamps,\n  env dumps) or repeated large reads turn into new tokens. Prefer smaller,\n  stable outputs and targeted reads.\n")
	b.WriteString("- **System prompt / memory / tool changes mid-session** — memory edits and\n  tool schema changes invalidate the cached prefix for every subsequent call.\n")
	b.WriteString("- **Restarting context** — `/new` or respawning a sub-agent from zero re-sends\n  everything. Resume/continue keeps the cached prefix instead.\n")
	return b.String()
}

// u returns a non-nil usage view for stable formatting.
func u(m *agent.SessionUsageMeta) *agent.SessionUsageMeta {
	if m == nil {
		return &agent.SessionUsageMeta{}
	}
	return m
}

func pct(u *agent.SessionUsageMeta) string {
	v := u.CacheHitTokens + u.CacheMissTokens
	if v == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", float64(u.CacheHitTokens)/float64(v)*100)
}

func costStr(u *agent.SessionUsageMeta) string {
	if u == nil || u.Cost <= 0 {
		return "—"
	}
	return fmt.Sprintf("%s%.4f", orDash(u.Currency), u.Cost)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

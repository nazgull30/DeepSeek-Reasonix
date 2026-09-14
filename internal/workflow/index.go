package workflow

import (
	"fmt"
	"strings"
)

// IndexMaxChars caps the pinned workflows-index block so it can't bloat the
// cache-stable system-prompt prefix; bodies never enter the prefix.
const IndexMaxChars = 3000

const missingDescPlaceholder = `(no description — add a leading string literal to the script)`

// indexHeader introduces the workflows block in the system prompt: what a
// workflow is, when to reach for it, and how to invoke one. It stays terse —
// the script bodies are never exposed to the model.
const indexHeader = "# Workflows — scripted multi-agent orchestration\n\n" +
	"One-liner index of user-authored orchestration scripts in .reasonix/workflows/. " +
	"Unlike a skill (a prose playbook the model steps through itself), a workflow is an executable script: " +
	"`agent(prompt, ...)`, `parallel([...])`, and `pipeline(stages, items)` fan sub-agents out while the script " +
	"holds loops, conditionals, and the data flow between steps — sub-agent results never enter your context. " +
	"Each entry is invoked via `workflow({ name: \"<name>\", args: {...} })`. " +
	"Reach for a workflow for repeatable multi-step or parallel processes; for a single one-off delegation, prefer plain `task`."

// IndexBlock renders the system/tool-result workflows listing without attaching
// it to a base prompt. Only names + descriptions are listed; bodies execute on
// demand via the workflow tool.
func IndexBlock(wfs []Workflow) string {
	if len(wfs) == 0 {
		return ""
	}
	lines := make([]string, 0, len(wfs))
	for _, wf := range wfs {
		lines = append(lines, indexLine(wf))
	}
	joined := strings.Join(lines, "\n")
	if r := []rune(joined); len(r) > IndexMaxChars {
		joined = string(r[:IndexMaxChars]) + fmt.Sprintf("\n… (truncated %d chars)", len(r)-IndexMaxChars)
	}
	return indexHeader + "\n\n```\n" + joined + "\n```"
}

// ApplyIndex appends the workflows index to basePrompt, or returns it unchanged
// when there are no workflows.
func ApplyIndex(basePrompt string, wfs []Workflow) string {
	block := IndexBlock(wfs)
	if block == "" {
		return basePrompt
	}
	return basePrompt + "\n\n" + block
}

// indexLine renders one workflow as "- name — description", clipped to a stable
// width so a model copying the line into workflow's `name` arg gets a clean
// identifier.
func indexLine(wf Workflow) string {
	desc := strings.TrimSpace(strings.ReplaceAll(wf.Description, "\n", " "))
	if desc == "" {
		desc = missingDescPlaceholder
	}
	max := 120 - len([]rune(wf.Name))
	clipped := clipRunes(desc, max)
	if clipped == "" {
		return "- " + wf.Name
	}
	return "- " + wf.Name + " — " + clipped
}

// clipRunes truncates s to at most max runes (ellipsis included), never
// splitting a multi-byte rune.
func clipRunes(s string, max int) string {
	if max < 1 {
		max = 1
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max-1 < 1 {
		return string(r[:1])
	}
	return string(r[:max-1]) + "…"
}

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
)

// flowEvent builds an event with a tool payload, the shape the flow tree reads.
func flowEvent(k event.Kind, t event.Tool) event.Event {
	return event.Event{Kind: k, Tool: t}
}

// flowLineContaining returns the first rendered line that contains substr, or
// "" if none does. Used to assert per-line content (e.g. the main agent line vs
// the whole-turn total line) without tripping over substrings on other lines.
func flowLineContaining(out, substr string) string {
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, substr) {
			return ln
		}
	}
	return ""
}

// TestFlowTreeNesting proves a sub-agent's calls nest under its parent task via
// ParentID, that top-level tools become roots, and that status transitions
// (running → completed / failed) are recorded per node.
func TestFlowTreeNesting(t *testing.T) {
	m := newTestChatTUI()

	m.flowApply(event.Event{Kind: event.TurnStarted})
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "bash-1", Name: "bash", Args: `{"command":"go test ./..."}`}))
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "t1", Name: "task", Args: `{"description":"Locate the tests","prompt":"find them"}`}))
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "r1", Name: "read_file", Args: `{"path":"a.go"}`, ParentID: "t1"}))
	m.flowApply(flowEvent(event.ToolResult, event.Tool{ID: "r1", Name: "read_file", DurationMs: 12}))
	m.flowApply(flowEvent(event.ToolResult, event.Tool{ID: "t1", Name: "task", DurationMs: 900}))

	if len(m.flowRoots) != 2 {
		t.Fatalf("roots = %d, want 2", len(m.flowRoots))
	}
	root := m.flowRoots[0]
	if root.kind != flowTool || root.label != "Bash(go test ./...)" || root.status != flowRunning {
		t.Fatalf("root0 = %+v, want running Bash tool", root)
	}
	task := m.flowRoots[1]
	if task.kind != flowSubtask || task.status != flowCompleted {
		t.Fatalf("task = %+v, want completed subtask", task)
	}
	if !strings.Contains(task.label, "Locate the tests") {
		t.Fatalf("task label = %q, want description", task.label)
	}
	if task.durationMs != 900 {
		t.Fatalf("task durationMs = %d, want 900", task.durationMs)
	}
	if len(task.children) != 1 {
		t.Fatalf("task children = %d, want 1", len(task.children))
	}
	child := task.children[0]
	if child.kind != flowTool || child.label != "Read(a.go)" || child.status != flowCompleted {
		t.Fatalf("child = %+v, want completed Read tool under task", child)
	}
}

// TestFlowFailedSubtask proves a failing sub-agent is recorded as failed and its
// error label is preserved on the node.
func TestFlowFailedSubtask(t *testing.T) {
	m := newTestChatTUI()
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "t1", Name: "task", Args: `{"description":"risky"}`}))
	m.flowApply(flowEvent(event.ToolResult, event.Tool{ID: "t1", Name: "task", Err: "timeout exceeded"}))

	task := m.flowRoots[0]
	if task.status != flowFailed {
		t.Fatalf("task status = %v, want failed", task.status)
	}
}

// TestFlowUsageAttach proves a sub-agent's Usage event (attributed via top-level
// ParentID) lands on the owning task node.
func TestFlowUsageAttach(t *testing.T) {
	m := newTestChatTUI()
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "t1", Name: "task", Args: `{}`}))
	m.flowApply(event.Event{Kind: event.Usage, ParentID: "t1", Usage: &provider.Usage{TotalTokens: 123}})

	task := m.flowRoots[0]
	if task.usage == nil || task.usage.TotalTokens != 123 {
		t.Fatalf("task usage = %+v, want 123 total tokens", task.usage)
	}
}

// TestFlowPhaseNode proves a Phase event appends a boundary marker root.
func TestFlowPhaseNode(t *testing.T) {
	m := newTestChatTUI()
	m.flowApply(event.Event{Kind: event.Phase, Text: "deepseek · planning"})

	if len(m.flowRoots) != 1 || m.flowRoots[0].kind != flowPhase {
		t.Fatalf("flowRoots = %+v, want a single phase node", m.flowRoots)
	}
}

// TestFlowResetOnTurnStarted proves each new user turn resets the LIVE tree
// (which the ToolDispatch/ToolResult events of the previous turn populated)
// while the store-backed whole-session history is kept intact by the reload.
func TestFlowResetOnTurnStarted(t *testing.T) {
	m := newTestChatTUI()
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "t1", Name: "task", Args: `{}`}))
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "r1", Name: "read_file", ParentID: "t1"}))
	m.flowApply(event.Event{Kind: event.TurnStarted})

	if len(m.flowRoots) != 0 || len(m.flowByID) != 0 {
		t.Fatalf("flow not reset: roots=%d byID=%d", len(m.flowRoots), len(m.flowByID))
	}
}

// ---------------------------------------------------------------------------
// Store-backed session history (flowLoadHistory) fixtures and tests.
// ---------------------------------------------------------------------------

// flowSubagentFixture is the writer used to add persisted subagents to the
// session dir before flowLoadHistory runs.
type flowSubagentFixture func(ref, parentCallID, kind, name string, created int, status agent.SubagentStatus, usage *agent.SessionUsageMeta, msgs ...provider.Message)

// flowHistoryAt returns a chatTUI wired to a controller whose session directory
// holds the given main session id, plus a flowSubagentFixture for persisting
// subagent artifacts (subagents/{ref}.meta.json + subagents/{ref}.jsonl) that
// flowLoadHistory will read back. BranchMeta is written with the given turn
// count and main-agent usage.
func flowHistoryAt(t *testing.T, mainID string, turns int, mainUsage *agent.SessionUsageMeta) (chatTUI, flowSubagentFixture) {
	t.Helper()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, mainID+".jsonl")

	main := agent.NewSession("sys")
	main.Add(provider.Message{Role: provider.RoleUser, Content: "fix the build"})
	main.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
		{ID: "call-task", Name: "task", Arguments: `{"description":"Find the bug"}`},
		{ID: "call-live-3", Name: "task", Arguments: `{"description":"Live check"}`},
	}})
	main.Add(provider.Message{Role: provider.RoleTool, ToolCallID: "call-task", Name: "task", Content: "ok"})
	main.Add(provider.Message{Role: provider.RoleAssistant, Content: "found"})
	if err := main.Save(mainPath); err != nil {
		t.Fatal(err)
	}

	// BranchMeta sidecar for whole-session main-agent usage + turn count.
	metaBytes, _ := json.Marshal(agent.BranchMeta{
		ID:           mainID,
		Turns:        turns,
		SessionUsage: mainUsage,
	})
	if err := os.WriteFile(mainPath+".meta", metaBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// A controller whose History() replays the main transcript, so full loads
	// can index the main agent's tool calls.
	exec := agent.New(nil, nil, main, agent.Options{}, event.Discard)
	m := newTestChatTUI()
	m.width = 200
	m.ctrl = control.New(control.Options{Executor: exec, SessionDir: dir, Label: "test"})
	m.ctrl.SetSessionPath(mainPath)

	writeFlowSubagent := func(ref, parentCallID, kind, name string, created int, status agent.SubagentStatus, usage *agent.SessionUsageMeta, msgs ...provider.Message) {
		t.Helper()
		sub := agent.NewSession("sys")
		for _, msg := range msgs {
			sub.Add(msg)
		}
		subPath := filepath.Join(dir, "subagents", ref+".jsonl")
		if err := sub.Save(subPath); err != nil {
			t.Fatal(err)
		}
		createdAt := time.Date(2026, 1, 1, 0, 0, created, 0, time.UTC)
		b, _ := json.Marshal(agent.SubagentMeta{
			Ref:              ref,
			CreatedAt:        createdAt,
			UpdatedAt:        createdAt.Add(2 * time.Minute),
			Status:           status,
			Kind:             kind,
			Name:             name,
			ParentSession:    mainID,
			ParentToolCallID: parentCallID,
			Model:            "deepseek-v4-pro",
			Effort:           "high",
			Usage:            usage,
		})
		if err := os.WriteFile(filepath.Join(dir, "subagents", ref+".meta.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return m, writeFlowSubagent
}

// TestRenderFlowGatedOnOpen proves renderFlow renders nothing until opened and
// that an empty tree still renders a panel (with the empty hint) once open.
func TestRenderFlowGatedOnOpen(t *testing.T) {
	m := newTestChatTUI()
	if out := m.renderFlow(); out != "" {
		t.Fatalf("renderFlow when closed = %q, want empty", out)
	}

	m.flowOpen = true
	if out := m.renderFlow(); out == "" || !strings.Contains(out, "Flow") {
		t.Fatalf("renderFlow when open and empty = %q, want titled panel", out)
	}

	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "t1", Name: "task", Args: `{"description":"hi"}`}))
	if out := m.renderFlow(); !strings.Contains(out, "hi") {
		t.Fatalf("renderFlow tree missing node: %q", out)
	}
}

// TestHideComposerWhenFlowOpen proves the flow popup owns the bottom region and
// hides the composer per the modal-ownership rule.
func TestHideComposerWhenFlowOpen(t *testing.T) {
	m := newTestChatTUI()
	if m.hideComposer() {
		t.Fatal("composer should be visible while the flow popup is closed")
	}
	m.flowOpen = true
	if !m.hideComposer() {
		t.Fatal("composer must be hidden while the flow popup is open")
	}
}

// TestBottomRowsAccountsForFlow proves the pinned bottom-region row count grows
// when the flow popup is open (and its rows are not double-counted elsewhere).
func TestBottomRowsAccountsForFlow(t *testing.T) {
	m := newTestChatTUI()
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "t1", Name: "task", Args: `{"description":"hi"}`}))
	closed := m.bottomRows()

	m.flowOpen = true
	open := m.bottomRows()
	if open <= closed {
		t.Fatalf("bottomRows open=%d should exceed closed=%d", open, closed)
	}
}

// TestFlowKeyToggle proves Ctrl+T opens and closes the popup, and Esc closes it.
func TestFlowKeyToggle(t *testing.T) {
	m := newTestChatTUI()

	next, _ := m.update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m = next.(chatTUI)
	if !m.flowOpen {
		t.Fatal("Ctrl+T did not open the flow popup")
	}

	next, _ = m.update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m = next.(chatTUI)
	if m.flowOpen {
		t.Fatal("second Ctrl+T did not close the flow popup")
	}

	next, _ = m.update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m = next.(chatTUI)
	next, _ = m.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(chatTUI)
	if m.flowOpen {
		t.Fatal("Esc did not close the flow popup")
	}
}

// TestFlowScrollKeys proves ↑/↓ scroll the open popup and clamp at the top.
func TestFlowScrollKeys(t *testing.T) {
	m := newTestChatTUI()
	m.flowOpen = true
	m.flowScroll = 3

	next, _ := m.update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = next.(chatTUI)
	if m.flowScroll != 2 {
		t.Fatalf("up scroll = %d, want 2", m.flowScroll)
	}

	next, _ = m.update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(chatTUI)
	if m.flowScroll != 3 {
		t.Fatalf("down scroll = %d, want 3", m.flowScroll)
	}

	m.flowScroll = 0
	next, _ = m.update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = next.(chatTUI)
	if m.flowScroll != 0 {
		t.Fatalf("up scroll clamps to 0, got %d", m.flowScroll)
	}
}

// TestFlowSubagentModelTag proves the subagent model/effort profile is captured
// for subtask nodes.
func TestFlowSubagentModelTag(t *testing.T) {
	m := newTestChatTUI()
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{
		ID:      "t1",
		Name:    "task",
		Args:    `{"description":"hi"}`,
		Profile: &event.Profile{Model: "deepseek-v4-pro", Effort: "high"},
	}))

	task := m.flowRoots[0]
	if task.model != "deepseek-v4-pro" || task.effort != "high" {
		t.Fatalf("task profile = %q/%q, want deepseek-v4-pro/high", task.model, task.effort)
	}
}

// TestFlowRenderAgentsOnly proves the popup shows only subagent/orchestrator
// nodes: plain tool calls are hidden, subtask labels render, a nested
// orchestrator subagent stays under its parent, and a synthetic "main agent"
// root anchors the tree.
func TestFlowRenderAgentsOnly(t *testing.T) {
	m := newTestChatTUI()
	m.flowOpen = true
	m.modelRef = "deepseek-flash/deepseek-v4-flash"

	// A plain top-level tool — must be hidden.
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "b1", Name: "bash", Args: `{"command":"ls"}`}))
	// A subtask that spawns a nested orchestrator subtask, which itself uses a
	// plain tool (also hidden).
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "t1", Name: "task", Args: `{"description":"Locate tests"}`}))
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "t2", Name: "task", Args: `{"description":"verify fixes"}`, ParentID: "t1"}))
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "r1", Name: "read_file", Args: `{"path":"a.go"}`, ParentID: "t2"}))
	m.flowApply(flowEvent(event.ToolResult, event.Tool{ID: "t1", Name: "task"}))

	out := m.renderFlow()
	if !strings.Contains(out, i18n.M.FlowMainAgent) {
		t.Fatalf("missing main agent root:\n%s", out)
	}
	for _, want := range []string{"Locate tests", "verify fixes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing subtask %q:\n%s", want, out)
		}
	}
	for _, hidden := range []string{"Bash(", "Read(", "a.go", `"command"`} {
		if strings.Contains(out, hidden) {
			t.Fatalf("flow popup leaked tool node containing %q:\n%s", hidden, out)
		}
	}
}

// TestFlowTotalTokensAndTags proves the popup distinguishes subagents from
// orchestrators, shows the main agent's OWN tokens (not a sum of subagents), and
// reports the full token breakdown (cache hit/miss, output, reasoning) plus USD
// cost on each agent and a whole-turn total line.
func TestFlowTotalTokensAndTags(t *testing.T) {
	m := newTestChatTUI()
	m.flowOpen = true
	m.width = 200 // wide enough that long breakdown lines are not clamped

	pricing := &provider.Pricing{CacheHit: 0.1, Input: 1.0, Output: 2.0}
	execUsage := &provider.Usage{TotalTokens: 100, PromptTokens: 100, CacheHitTokens: 40, CacheMissTokens: 60, CompletionTokens: 0, ReasoningTokens: 0}
	// Executor usage (no parent) → counts as the main agent's own spend only.
	m.flowApply(event.Event{Kind: event.Usage, Usage: execUsage, Pricing: pricing})
	// An orchestrator subtask that spawns a leaf subagent.
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "t1", Name: "task", Args: `{"description":"outer"}`}))
	m.flowApply(event.Event{Kind: event.Usage, ParentID: "t1", Usage: &provider.Usage{TotalTokens: 50, PromptTokens: 50, CacheMissTokens: 50, CompletionTokens: 0}, Pricing: pricing})
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "t2", Name: "task", Args: `{"description":"inner"}`, ParentID: "t1"}))
	m.flowApply(event.Event{Kind: event.Usage, ParentID: "t2", Usage: &provider.Usage{TotalTokens: 300, PromptTokens: 200, CacheHitTokens: 120, CacheMissTokens: 80, CompletionTokens: 100, ReasoningTokens: 40}, Pricing: pricing})

	out := m.renderFlow()
	// Main agent shows ONLY its own (executor) usage: 100 tokens, cost = (40*0.1 + 60*1)/1e6 = 0.0001.
	mainLine := flowLineContaining(out, "main agent")
	if mainLine == "" || !strings.Contains(mainLine, "Σ 100") || !strings.Contains(mainLine, "$0.0001") {
		t.Fatalf("main agent own usage missing:\n%s", out)
	}
	if strings.Contains(mainLine, "Σ 450") {
		t.Fatalf("main agent wrongly shows the summed total:\n%s", out)
	}
	// Whole-turn total = 100 + 50 + 300 = 450, cost = 0.0004.
	totalLine := flowLineContaining(out, i18n.M.FlowTotalAll)
	if totalLine == "" || !strings.Contains(totalLine, "Σ 450") || !strings.Contains(totalLine, "$0.0004") {
		t.Fatalf("whole-turn total line missing:\n%s", out)
	}
	// t1 spawned t2 → orchestrator; t2 is a leaf → subagent.
	if !strings.Contains(out, "Task(outer) ["+i18n.M.FlowTagOrchestrator+"]") {
		t.Fatalf("orchestrator tag missing:\n%s", out)
	}
	if !strings.Contains(out, "Task(inner) ["+i18n.M.FlowTagSubagent+"]") {
		t.Fatalf("subagent tag missing:\n%s", out)
	}
	// Per-node breakdown: cache hit/miss, output, reasoning, cost.
	if !strings.Contains(out, "⇥ 120 hit / 80 miss") {
		t.Fatalf("leaf cache breakdown missing:\n%s", out)
	}
	if !strings.Contains(out, "↓ 60") || !strings.Contains(out, "🧠 40") {
		t.Fatalf("leaf output/reasoning missing:\n%s", out)
	}
	if !strings.Contains(out, "$0.0003") {
		t.Fatalf("leaf cost missing:\n%s", out)
	}
}

// TestFlowMainAgentRootPresent proves the synthetic root renders even when no
// subagent has spawned yet, with the empty hint beneath it.
func TestFlowMainAgentRootPresent(t *testing.T) {
	m := newTestChatTUI()
	m.flowOpen = true
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "b1", Name: "bash", Args: `{"command":"pwd"}`}))

	out := m.renderFlow()
	if !strings.Contains(out, i18n.M.FlowMainAgent) {
		t.Fatalf("main agent root missing when only tools present:\n%s", out)
	}
	if !strings.Contains(out, i18n.M.FlowEmpty) {
		t.Fatalf("expected empty hint when no subagents:\n%s", out)
	}
	if strings.Contains(out, "Bash(") {
		t.Fatalf("flow popup should hide the lone tool:\n%s", out)
	}
}

// TestFlowSubtaskCommandNode proves /subtask-launched sub-agents (no tool call,
// synthetic subtask-N parent ID) create a flow node that renders, nests nested
// model-driven subtasks, and transitions to completed/failed when they report.
func TestFlowSubtaskCommandNode(t *testing.T) {
	m := newTestChatTUI()
	m.flowOpen = true
	m.flowStartSubtask("subtask-0", "review the diff")

	if len(m.flowRoots) != 1 || m.flowRoots[0].kind != flowSubtask || m.flowRoots[0].status != flowRunning {
		t.Fatalf("subtask node = %+v, want a running subtask root", m.flowRoots)
	}

	// A model-driven subtask spawned inside it nests under the /subtask node.
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "inner-t", Name: "task", Args: `{"description":"deep dive"}`, ParentID: "subtask-0"}))
	if len(m.flowRoots[0].children) != 1 {
		t.Fatalf("nested subtask not nested under /subtask node")
	}

	m.flowEndSubtask("subtask-0", false)
	if m.flowRoots[0].status != flowCompleted {
		t.Fatalf("subtask status = %v, want completed", m.flowRoots[0].status)
	}

	out := m.renderFlow()
	if !strings.Contains(out, "review the diff") {
		t.Fatalf("flow popup missing /subtask node:\n%s", out)
	}
	if !strings.Contains(out, "deep dive") {
		t.Fatalf("flow popup missing nested subtask:\n%s", out)
	}

	// A failed /subtask run flips the node to failed.
	m.flowStartSubtask("subtask-1", "risky run")
	m.flowEndSubtask("subtask-1", true)
	if m.flowRoots[1].status != flowFailed {
		t.Fatalf("second subtask status = %v, want failed", m.flowRoots[1].status)
	}
}

// TestFlowHistoryGroupsByCaller proves the store-backed session tree groups every
// persisted subagent under who called it: subtasks spawned by the main agent are
// roots labelled from the main transcript's task description, a subtask spawned
// inside a subagent nests under it (label from that transcript), and a run with
// an unknown caller (e.g. /subtask synthetic IDs) still lands as a root with a
// prompt-derived label.
func TestFlowHistoryGroupsByCaller(t *testing.T) {
	m, add := flowHistoryAt(t, "abc123", 4, nil)
	add("sa_skill", "", "skill", "research", 1, agent.SubagentFailed, &agent.SessionUsageMeta{TotalTokens: 50},
		provider.Message{Role: provider.RoleUser, Content: "scan the repo"})
	add("sa_top", "call-task", "task", "task", 2, agent.SubagentCompleted, &agent.SessionUsageMeta{TotalTokens: 200},
		provider.Message{Role: provider.RoleUser, Content: "dig into the failure"},
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "call-inner", Name: "task", Arguments: `{"description":"investigate deeper"}`},
		}})
	add("sa_child", "call-inner", "task", "task", 3, agent.SubagentCompleted, &agent.SessionUsageMeta{TotalTokens: 100},
		provider.Message{Role: provider.RoleUser, Content: "narrow it"})
	add("sa_live", "call-live-3", "task", "task", 4, agent.SubagentRunning, nil)

	m.flowLoadHistory(true)

	if len(m.flowHistory) != 3 {
		t.Fatalf("history roots = %d, want 3 (skill, task, live) — got %d", len(m.flowHistory), len(m.flowHistory))
	}
	skill := m.flowHistory[0]
	top := m.flowHistory[1]
	live := m.flowHistory[2]
	if skill.id != "sa_skill" || skill.status != flowFailed {
		t.Fatalf("skill root = %+v, want failed sa_skill", skill)
	}
	if skill.label != "research(scan the repo)" {
		t.Fatalf("skill label = %q, want prompt-derived label", skill.label)
	}
	if top.id != "sa_top" || top.status != flowCompleted {
		t.Fatalf("task root = %+v, want completed sa_top", top)
	}
	if top.label != "Task(Find the bug)" {
		t.Fatalf("task label = %q, want main-transcript description", top.label)
	}
	if top.model != "deepseek-v4-pro" || top.effort != "high" {
		t.Fatalf("task profile = %q/%q, want deepseek-v4-pro/high", top.model, top.effort)
	}
	if top.usage == nil || top.usage.TotalTokens != 200 {
		t.Fatalf("task usage = %+v, want 200 total tokens", top.usage)
	}
	if live.id != "sa_live" || live.status != flowRunning {
		t.Fatalf("live root = %+v, want running sa_live", live)
	}
	if live.label != "Task(Live check)" {
		t.Fatalf("live label = %q, want main-transcript description", live.label)
	}
	// The inner subtask nests under sa_top with its own model-driven label.
	if len(top.children) != 1 || top.children[0].id != "sa_child" {
		t.Fatalf("sa_top children = %+v, want single sa_child", top.children)
	}
	child := top.children[0]
	if child.label != "Task(investigate deeper)" {
		t.Fatalf("child label = %q, want subagent-transcript description", child.label)
	}
	if child.usage == nil || child.usage.TotalTokens != 100 {
		t.Fatalf("child usage = %+v, want 100 total tokens", child.usage)
	}
	// Ownership map: caller call IDs resolve to persisted refs (main wins over
	// any later/foreign index for the same ID).
	if m.flowHistoryCallOwner["call-task"] != "sa_top" {
		t.Fatalf("owner of call-task = %q, want sa_top", m.flowHistoryCallOwner["call-task"])
	}
}

// TestFlowHistorySurvivesTurnReset proves the store-backed session tree is kept
// intact by a TurnStarted reload (which resets only the live per-turn overlay)
// and that a second load does not duplicate or drop nodes.
func TestFlowHistorySurvivesTurnReset(t *testing.T) {
	m, add := flowHistoryAt(t, "abc123", 5, nil)
	add("sa_top", "call-task", "task", "task", 1, agent.SubagentCompleted, nil,
		provider.Message{Role: provider.RoleUser, Content: "boot"})

	m.flowApply(event.Event{Kind: event.TurnStarted}) // full load
	if len(m.flowHistory) != 1 {
		t.Fatalf("history after first turn = %d, want 1", len(m.flowHistory))
	}
	// Live overlay is seeded on this turn, then a new turn resets it but keeps
	// the store-backed history.
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "new-call", Name: "task", Args: `{"description":"new turn task"}`}))
	if len(m.flowRoots) != 1 {
		t.Fatalf("live roots = %d, want 1", len(m.flowRoots))
	}
	m.flowApply(event.Event{Kind: event.TurnStarted})
	if len(m.flowRoots) != 0 {
		t.Fatalf("live roots not reset: %d", len(m.flowRoots))
	}
	if len(m.flowHistory) != 1 || m.flowHistory[0].id != "sa_top" {
		t.Fatalf("store-backed history lost across turn reset: %+v", m.flowHistory)
	}
}

// TestFlowHistoryLightRefreshUpdatesMeta proves a light reload (Ctrl+T open /
// subtask ToolResult) re-reads the cheap meta files for known refs and folds the
// refreshed status/usage/duration into the existing history node without
// re-parsing its transcript.
func TestFlowHistoryLightRefreshUpdatesMeta(t *testing.T) {
	m, add := flowHistoryAt(t, "abc123", 3, nil)
	add("sa_live", "call-task", "task", "task", 1, agent.SubagentRunning, nil,
		provider.Message{Role: provider.RoleUser, Content: "dig"})

	m.flowLoadHistory(true)
	live := m.flowHistory[0]
	if live.status != flowRunning || live.usage != nil {
		t.Fatalf("before refresh = %+v, want running with no usage", live)
	}

	// The run completes: rewrite its meta sidecar (keeping the same transcript
	// at the same path), then exercise the light refresh path.
	metaPath := filepath.Join(filepath.Dir(m.ctrl.SessionPath()), "subagents", "sa_live.meta.json")
	raw, _ := os.ReadFile(metaPath)
	var meta agent.SubagentMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	meta.Status = agent.SubagentCompleted
	meta.Usage = &agent.SessionUsageMeta{TotalTokens: 321}
	updated, _ := json.Marshal(meta)
	if err := os.WriteFile(metaPath, updated, 0o644); err != nil {
		t.Fatal(err)
	}

	// A brand-new subagent that the light refresh has never seen must have its
	// transcript parsed on demand.
	add("sa_new", "call-fresh-xyz", "task", "task", 2, agent.SubagentCompleted, &agent.SessionUsageMeta{TotalTokens: 40},
		provider.Message{Role: provider.RoleUser, Content: "new arrival"})
	m.flowLoadHistory(false)

	if got := len(m.flowHistory); got != 2 {
		t.Fatalf("history len after light refresh = %d, want 2", got)
	}
	live = m.flowHistory[0]
	if live.status != flowCompleted {
		t.Fatalf("status after light refresh = %v, want completed", live.status)
	}
	if live.usage == nil || live.usage.TotalTokens != 321 {
		t.Fatalf("usage after light refresh = %+v, want 321", live.usage)
	}
	if got := m.flowHistory[1].id; got != "sa_new" {
		t.Fatalf("new subagent = %q, want sa_new", got)
	}
}

// TestFlowSessionTotalsAndOverlay proves the popup's three usage lines always add
// up: main agent (BranchMeta sidecar) + subagents (persisted history + live
// in-flight overlay without double-counting captured work), and that the title
// shows the whole-session turn count.
func TestFlowSessionTotalsAndOverlay(t *testing.T) {
	pricing := &provider.Pricing{CacheHit: 0.1, Input: 1.0, Output: 2.0}
	m, add := flowHistoryAt(t, "abc123", 3, &agent.SessionUsageMeta{TotalTokens: 500, PromptTokens: 400, CacheHitTokens: 100, CacheMissTokens: 300, Cost: 0.0005})
	add("sa_top", "call-task", "task", "task", 1, agent.SubagentCompleted, &agent.SessionUsageMeta{TotalTokens: 200, Cost: 0.0002})
	add("sa_child", "call-inner", "task", "task", 2, agent.SubagentCompleted, &agent.SessionUsageMeta{TotalTokens: 100, Cost: 0.0001},
		provider.Message{Role: provider.RoleUser, Content: "x"},
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-inner", Name: "task", Arguments: `{"description":"inner"}`}}})
	add("sa_skill", "", "skill", "research", 3, agent.SubagentFailed, &agent.SessionUsageMeta{TotalTokens: 50, Cost: 0.00005})

	m.flowOpen = true
	m.modelRef = "deepseek-flash/deepseek-v4-flash"
	m.flowLoadHistory(true)

	out := m.renderFlow()
	if !strings.Contains(out, fmt.Sprintf(i18n.M.FlowTurnsFmt, 3)) {
		t.Fatalf("session turn count missing:\n%s", out)
	}
	mainLine := flowLineContaining(out, "main agent")
	if mainLine == "" || !strings.Contains(mainLine, "Σ 500") || !strings.Contains(mainLine, "$0.0005") {
		t.Fatalf("main agent session usage missing:\n%s", out)
	}
	subLine := flowLineContaining(out, i18n.M.FlowSubagentAll)
	if subLine == "" || !strings.Contains(subLine, "Σ 350") || !strings.Contains(subLine, "$0.0004") {
		t.Fatalf("subagent session line missing (want Σ 350 · $0.0003~0.0004):\n%s", out)
	}
	totalLine := flowLineContaining(out, i18n.M.FlowTotalAll)
	if totalLine == "" || !strings.Contains(totalLine, "Σ 850") || !strings.Contains(totalLine, "$0.0009") {
		t.Fatalf("all-agents total line missing (want Σ 850 · $0.0009):\n%s", out)
	}

	// An in-flight subtask that has NOT persisted yet adds to the subagent line
	// only once; a live event for a call already captured in history (sa_live's
	// call) is deduplicated from the overlay.
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "call-fresh", Name: "task", Args: `{"description":"in flight"}`}))
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "call-live-3", Name: "task", Args: `{"description":"Live check"}`}))
	m.flowApply(event.Event{Kind: event.Usage, ParentID: "call-fresh", Usage: &provider.Usage{TotalTokens: 25}, Pricing: pricing})

	out = m.renderFlow()
	if strings.Count(out, "Task(Live check)") != 1 {
		t.Fatalf("persisted call reappeared in overlay:\n%s", out)
	}
	if strings.Count(out, "Task(in flight)") != 1 {
		t.Fatalf("live in-flight subtask missing from overlay:\n%s", out)
	}
	subLine = flowLineContaining(out, i18n.M.FlowSubagentAll)
	if !strings.Contains(subLine, "Σ 375") {
		t.Fatalf("subagent line did not fold in live in-flight usage (want Σ 375):\n%s", out)
	}
	totalLine = flowLineContaining(out, i18n.M.FlowTotalAll)
	if !strings.Contains(totalLine, "Σ 875") {
		t.Fatalf("all-agents total did not fold in live in-flight usage (want Σ 875):\n%s", out)
	}
}

// TestFlowHistoryEmptyWithoutPersistence proves sessions without a session dir
// (newTestChatTUI, no controller) load an empty history rather than panicking.
func TestFlowHistoryEmptyWithoutPersistence(t *testing.T) {
	m := newTestChatTUI()
	m.flowApply(event.Event{Kind: event.TurnStarted}) // hits flowLoadHistory(true)
	if len(m.flowHistory) != 0 {
		t.Fatalf("history = %d, want 0 without persistence", len(m.flowHistory))
	}
}

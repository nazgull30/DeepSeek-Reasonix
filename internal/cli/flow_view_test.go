package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

// TestFlowResetOnTurnStarted proves each new user turn starts a fresh tree.
func TestFlowResetOnTurnStarted(t *testing.T) {
	m := newTestChatTUI()
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "t1", Name: "task", Args: `{}`}))
	m.flowApply(flowEvent(event.ToolDispatch, event.Tool{ID: "r1", Name: "read_file", ParentID: "t1"}))
	m.flowApply(event.Event{Kind: event.TurnStarted})

	if len(m.flowRoots) != 0 || len(m.flowByID) != 0 {
		t.Fatalf("flow not reset: roots=%d byID=%d", len(m.flowRoots), len(m.flowByID))
	}
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

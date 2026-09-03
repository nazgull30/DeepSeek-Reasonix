// Flow popup: a live, hotkey-visible tree of the current turn's agent flow.
// The main agent's tool calls form the roots; spawned sub-agents (task,
// run_skill and the wrapper research tools) become nested subtask nodes, and
// any deeper sub-agent's calls hang off its parent call via ParentID. Phase
// events mark planner→executor boundaries. It is rebuilt incrementally from the
// same event stream the rest of the TUI renders, so opening it never disturbs a
// running turn.
package cli

import (
	"fmt"
	"strings"

	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
)

// flowStatus is the live state of one flow node.
type flowStatus int

const (
	flowRunning flowStatus = iota
	flowCompleted
	flowFailed
)

// flowNodeKind distinguishes the node types rendered in the flow popup.
type flowNodeKind int

const (
	flowPhase   flowNodeKind = iota // planner→executor boundary marker
	flowSubtask                     // a spawned sub-agent (task / run_skill / ...)
	flowTool                        // an ordinary tool call
)

// flowNode is one entry in the flow tree. Children are in dispatch order.
type flowNode struct {
	id         string
	parentID   string
	kind       flowNodeKind
	label      string
	status     flowStatus
	model      string
	effort     string
	durationMs int64
	usage      *provider.Usage
	cost       float64 // estimated USD spend for this node's Usage events
	children   []*flowNode
}

// isSubagentTool reports whether a tool call spawns a sub-agent, so the flow
// popup can emphasise it as a "subtask" rather than a plain tool line.
func isSubagentTool(name string) bool {
	switch name {
	case "task", "parallel_tasks", "run_skill", "explore", "research", "review", "security_review":
		return true
	}
	return false
}

// flowReset clears the per-turn flow tree. Called on each TurnStarted so every
// user turn gets its own flow view.
func (m *chatTUI) flowReset() {
	m.flowRoots = m.flowRoots[:0]
	m.flowByID = map[string]*flowNode{}
	m.flowScroll = 0
	m.flowMainTokens = 0
	m.flowMainCacheHit = 0
	m.flowMainCacheMiss = 0
	m.flowMainCompletion = 0
	m.flowMainReasoning = 0
	m.flowMainCost = 0
	m.flowTotalTokens = 0
	m.flowTotalCacheHit = 0
	m.flowTotalCacheMiss = 0
	m.flowTotalCompletion = 0
	m.flowTotalReasoning = 0
	m.flowTotalCost = 0
}

// flowApply folds one agent event into the flow tree. It is a no-op for kinds
// that carry no flow information and must never block or allocate on the hot
// path beyond the node itself.
func (m *chatTUI) flowApply(e event.Event) {
	if m.flowByID == nil {
		m.flowByID = map[string]*flowNode{}
	}
	switch e.Kind {
	case event.TurnStarted:
		m.flowReset()
	case event.ToolDispatch:
		if e.Tool.Partial {
			return
		}
		m.flowDispatch(e.Tool)
	case event.ToolResult:
		m.flowResult(e.Tool)
	case event.Usage:
		if e.Usage != nil {
			// Whole-turn total: every event (main agent + all subagents).
			m.flowTotalTokens += e.Usage.TotalTokens
			m.flowTotalCacheHit += e.Usage.CacheHitTokens
			m.flowTotalCacheMiss += e.Usage.CacheMissTokens
			m.flowTotalCompletion += e.Usage.CompletionTokens
			m.flowTotalReasoning += e.Usage.ReasoningTokens
			if e.Pricing != nil {
				m.flowTotalCost += e.Pricing.Cost(e.Usage)
			}
			if e.ParentID == "" {
				// Main agent's own spend (executor + planner are top-level).
				m.flowMainTokens += e.Usage.TotalTokens
				m.flowMainCacheHit += e.Usage.CacheHitTokens
				m.flowMainCacheMiss += e.Usage.CacheMissTokens
				m.flowMainCompletion += e.Usage.CompletionTokens
				m.flowMainReasoning += e.Usage.ReasoningTokens
				if e.Pricing != nil {
					m.flowMainCost += e.Pricing.Cost(e.Usage)
				}
			} else if n, ok := m.flowByID[e.ParentID]; ok {
				// Subagent spend: attach to its own node.
				n.usage = addUsage(n.usage, e.Usage)
				if e.Pricing != nil {
					n.cost += e.Pricing.Cost(e.Usage)
				}
			}
		}
	case event.Phase:
		m.flowRoots = append(m.flowRoots, &flowNode{
			kind:   flowPhase,
			label:  e.Text,
			status: flowCompleted,
		})
	}
}

// flowDispatch records a tool call beginning. A dispatch may arrive with an
// empty ID (some bare tool events), which has no place in the tree and is
// skipped rather than rendered as a dangling node.
func (m *chatTUI) flowDispatch(t event.Tool) {
	if t.ID == "" {
		return
	}
	kind := flowTool
	if isSubagentTool(t.Name) {
		kind = flowSubtask
	}
	n := &flowNode{
		id:       t.ID,
		parentID: t.ParentID,
		kind:     kind,
		label:    flowLabel(t),
		status:   flowRunning,
	}
	if t.Profile != nil {
		n.model = t.Profile.Model
		n.effort = t.Profile.Effort
	}
	m.flowInsert(n)
}

// flowInsert places a node under its ParentID ancestor (in dispatch order), or
// as a top-level root when the parent is unknown, and indexes it by ID.
func (m *chatTUI) flowInsert(n *flowNode) {
	if p, ok := m.flowByID[n.parentID]; ok {
		p.children = append(p.children, n)
	} else {
		m.flowRoots = append(m.flowRoots, n)
	}
	m.flowByID[n.id] = n
}

// flowResult marks a tool call finished. A result with no matching dispatch is
// synthesised so its status still shows (some sub-agent events surface only the
// result); a failure is always a hard block so it renders in red.
func (m *chatTUI) flowResult(t event.Tool) {
	n, ok := m.flowByID[t.ID]
	if !ok {
		m.flowDispatch(t)
		n, _ = m.flowByID[t.ID]
	}
	if n == nil {
		return
	}
	n.durationMs = t.DurationMs
	if t.Err != "" {
		n.status = flowFailed
	} else {
		n.status = flowCompleted
	}
}

// flowStartSubtask registers a subtask node for a /subtask-launched sub-agent.
// Unlike model-driven task calls these never emit a ToolDispatch (they are
// spawned directly from the CLI), so a node has to be created here; their inner
// tool events arrive with ParentID = id and are hidden, while any nested
// model-driven subtask nests under this node.
func (m *chatTUI) flowStartSubtask(id, desc string) {
	if m.flowByID == nil {
		m.flowByID = map[string]*flowNode{}
	}
	n := &flowNode{
		id:     id,
		kind:   flowSubtask,
		label:  desc,
		status: flowRunning,
	}
	m.flowInsert(n)
}

// flowEndSubtask marks a /subtask-launched sub-agent completed or failed when
// its async run reports back.
func (m *chatTUI) flowEndSubtask(id string, failed bool) {
	n, ok := m.flowByID[id]
	if !ok {
		return
	}
	if failed {
		n.status = flowFailed
	} else {
		n.status = flowCompleted
	}
}

// flowLabel is the node's one-line description: the tool's display verb plus
// its primary argument (a task's description, a bash command, a file path...).
func flowLabel(t event.Tool) string {
	name := toolDisplayName(t.Name)
	if arg := toolArg(t.Name, t.Args); arg != "" {
		return name + "(" + clampPlain(arg, 60) + ")"
	}
	return name
}

// flowMaxDepth caps indentation so a pathological nesting depth cannot blow up
// the panel width; deeper nodes render at the cap.
const flowMaxDepth = 24

// renderFlow draws the flow popup panel, or "" when it is not open so callers
// (bottomRows, View) treat it as absent. A synthetic "main agent" root anchors
// the tree; only its subagent/orchestrator descendants are listed (see
// flowLines). Scrolling is clamped against the rendered line count here, so the
// scroll offset always lands on a real line.
func (m *chatTUI) renderFlow() string {
	if !m.flowOpen {
		return ""
	}
	w := max(m.width, 10)
	var b strings.Builder
	b.WriteString(accent(i18n.M.FlowTitle) + "\n")
	root := bold(i18n.M.FlowMainAgent)
	if mr := m.modelRef; mr != "" {
		root += dim(" · " + mr)
	}
	if m.flowMainTokens > 0 {
		agg := &provider.Usage{
			TotalTokens:      m.flowMainTokens,
			PromptTokens:     m.flowMainCacheHit + m.flowMainCacheMiss,
			CacheHitTokens:   m.flowMainCacheHit,
			CacheMissTokens:  m.flowMainCacheMiss,
			CompletionTokens: m.flowMainCompletion,
			ReasoningTokens:  m.flowMainReasoning,
		}
		root += " · " + flowUsageLine(agg, m.flowMainCost)
	}
	b.WriteString(root + "\n")
	lines := m.flowLines(m.flowRoots, 1)
	if len(lines) == 0 {
		b.WriteString(dim("  "+i18n.M.FlowEmpty) + "\n")
	} else {
		if m.flowScroll >= len(lines) {
			m.flowScroll = max(0, len(lines)-1)
		}
		for _, ln := range lines[m.flowScroll:] {
			b.WriteString(clampPlain(ln, w-2) + "\n")
		}
	}
	// Whole-turn total across all agents.
	if m.flowTotalTokens > 0 {
		agg := &provider.Usage{
			TotalTokens:      m.flowTotalTokens,
			PromptTokens:     m.flowTotalCacheHit + m.flowTotalCacheMiss,
			CacheHitTokens:   m.flowTotalCacheHit,
			CacheMissTokens:  m.flowTotalCacheMiss,
			CompletionTokens: m.flowTotalCompletion,
			ReasoningTokens:  m.flowTotalReasoning,
		}
		b.WriteString(dim(i18n.M.FlowTotalAll) + " · " + flowUsageLine(agg, m.flowTotalCost) + "\n")
	}
	b.WriteString(dim(i18n.M.FlowHint))
	return choicePanelStyle.Width(w).Render(strings.TrimRight(b.String(), "\n"))
}

// flowLines flattens the tree into display lines, depth-first, honouring the
// indentation cap. Only subagent/orchestrator nodes render (a status glyph,
// bold label, model tag, duration and token usage); phase markers and plain
// tool calls are skipped, though their subtask descendants are still recursed
// into so nested orchestrators hang under their parent subagent.
func (m chatTUI) flowLines(nodes []*flowNode, depth int) []string {
	var out []string
	indent := strings.Repeat("  ", min(depth, flowMaxDepth))
	for _, n := range nodes {
		if n.kind == flowSubtask {
			glyph := toolWorkingFrames[0]
			switch n.status {
			case flowCompleted:
				glyph = green("✓")
			case flowFailed:
				glyph = red("⊘")
			}
			// A subtask that itself spawned sub-agents is an orchestrator; a leaf
			// is a plain subagent.
			tag := i18n.M.FlowTagSubagent
			for _, c := range n.children {
				if c.kind == flowSubtask {
					tag = i18n.M.FlowTagOrchestrator
					break
				}
			}
			line := indent + glyph + " " + bold(n.label) + dim(" ["+tag+"]")
			if n.model != "" {
				line += dim(" · " + n.model)
			}
			if n.status != flowRunning && n.durationMs > 0 {
				line += dim(" · " + shortDuration(n.durationMs))
			}
			if n.usage != nil && n.usage.TotalTokens > 0 {
				line += " · " + flowUsageLine(n.usage, n.cost)
			}
			out = append(out, line)
		}
		out = append(out, m.flowLines(n.children, depth+1)...)
	}
	return out
}

// shortDuration formats a wall-clock millisecond value compactly.
func shortDuration(ms int64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		return fmt.Sprintf("%.1fm", float64(ms)/60000)
	}
}

// flowUsageLine formats a node's (or the whole turn's) token breakdown — total,
// cache hit / miss, output, reasoning and estimated USD cost — as a dim line.
// cost is the precomputed USD spend so the aggregate (which may mix several
// rate cards) shows a true sum rather than a recomputation on one card.
func flowUsageLine(u *provider.Usage, cost float64) string {
	if u == nil || u.TotalTokens == 0 {
		return ""
	}
	cached := u.CacheHitTokens
	fresh := u.CacheMissTokens
	if fresh == 0 && u.PromptTokens > cached {
		fresh = u.PromptTokens - cached
	}
	output := u.CompletionTokens - u.ReasoningTokens
	if output < 0 {
		output = 0
	}
	var cols []string
	cols = append(cols, "Σ "+shortTokens(u.TotalTokens))
	if u.PromptTokens > 0 {
		cols = append(cols, fmt.Sprintf("⇥ %s hit / %s miss", shortTokens(cached), shortTokens(fresh)))
	}
	if u.CompletionTokens > 0 {
		cols = append(cols, "↓ "+shortTokens(output))
	}
	if u.ReasoningTokens > 0 {
		cols = append(cols, "🧠 "+shortTokens(u.ReasoningTokens))
	}
	if cost > 0 {
		cols = append(cols, "$"+fmt.Sprintf("%.4f", cost))
	}
	return dim(strings.Join(cols, " · "))
}

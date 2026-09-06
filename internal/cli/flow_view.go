// Flow popup: a hotkey-visible tree of the session's agent flow.
//
// Two layers feed it. The whole-SESSION history tree is rebuilt from the
// persisted SubagentStore (agent.ListSubagentsByParent), so every sub-agent the
// conversation has ever spawned — across turns and restarts — is shown, grouped
// under the subagent that called it (model-driven task calls, /subtask runs and
// runAs=subagent skills all persist a transcript). On top of that, the live
// event stream keeps the CURRENT turn's tree in m.flowRoots so in-flight
// subagents animate (status glyph, streaming usage); nodes whose spawn already
// landed in the store are deduplicated away from the overlay. It is rebuilt
// incrementally from the same event stream the rest of the TUI renders, so
// opening it never disturbs a running turn.
package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"reasonix/internal/agent"
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

// flowNode is one entry in the flow tree. Children are in dispatch order; for
// store-backed history nodes, created orders siblings chronologically.
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
	created    time.Time
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

// flowReset clears the per-turn live flow tree (the overlay on top of the
// persisted session history). Called on each TurnStarted so the current turn
// starts clean while flowHistory keeps the whole session.
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
}

// flowHistoryByID lazily initialises the whole-session history maps.
func (m *chatTUI) flowHistoryByID() {
	if m.flowHistoryByRef == nil {
		m.flowHistoryByRef = map[string]*flowNode{}
	}
	if m.flowHistoryCallOwner == nil {
		m.flowHistoryCallOwner = map[string]string{}
	}
	if m.flowHistoryPrompt == nil {
		m.flowHistoryPrompt = map[string]string{}
	}
	if m.flowHistoryCallLabels == nil {
		m.flowHistoryCallLabels = map[string]string{}
	}
	if m.flowHistoryCaller == nil {
		m.flowHistoryCaller = map[string]string{}
	}
}

// flowLoadHistory rebuilds the whole-session subagent tree from the persisted
// SubagentStore, so the flow popup shows every sub-agent the session spawned
// across turns and restarts, grouped under the subagent that called it (the main
// agent at the root). group=full re-parses every transcript and refreshes the
// whole-session main-agent usage/turn count from the session's BranchMeta
// sidecar; group=false is a light refresh that re-reads the (cheap) meta files
// for known refs and only parses transcripts for newly-seen ones. Sessions with
// persistence disabled leave the history empty.
func (m *chatTUI) flowLoadHistory(full bool) {
	m.flowHistoryByID()
	if m.ctrl == nil || m.ctrl.SessionDir() == "" || m.ctrl.SessionPath() == "" {
		m.flowHistory = m.flowHistory[:0]
		m.flowHistoryCallOwner = map[string]string{}
		return
	}
	if full {
		m.flowHistoryPrompt = map[string]string{}
		m.flowHistoryCallLabels = map[string]string{}
	}
	parent := agent.BranchID(m.ctrl.SessionPath())
	artifacts, err := agent.ListSubagentsByParent(m.ctrl.SessionDir(), parent)
	if err != nil {
		// Keep whatever history we already hold rather than blanking it on a
		// transient read error.
		return
	}

// Pass 1: index the main transcript's tool calls so top-level ownership and
	// labels resolve. Subagent transcripts were already indexed when first seen;
	// continuations re-parse only on a full refresh.
	m.flowHistoryCaller = map[string]string{}
	labels := m.flowHistoryCallLabels
	if full {
		for _, msg := range m.ctrl.History() {
			indexToolCalls(msg.ToolCalls, labels)
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" {
					m.flowHistoryCaller[tc.ID] = "" // main agent called it
				}
			}
		}
	}

	// Pass 2: (re)parse transcripts — on a full refresh every transcript (they
	// can grow via continuation), otherwise only refs this process has not seen
	// yet — recording each run's opening prompt for label fallbacks and its
	// tool-call IDs so nested runs resolve under the subagent that issued them.
	// Parsing runs before node building so every transcript's call labels are
	// indexed regardless of directory order.
	byRef := map[string]*flowNode{}
	owner := map[string]string{} // caller tool-call ID -> subagent ref spawned by it
	for i := range artifacts {
		art := artifacts[i]
		ref := art.Ref
		known := m.flowHistoryByRef[ref]
		if known != nil && !full {
			continue
		}
		prompt := m.flowHistoryPrompt[ref]
		if full {
			m.flowHistoryPrompt[ref] = ""
			m.flowHistoryByRef[ref] = nil
		}
		if sess, lerr := agent.LoadSession(art.SessionPath); lerr == nil {
			prompt = firstUserPrompt(sess, prompt)
			for _, msg := range sess.Messages {
				for _, tc := range msg.ToolCalls {
					if tc.ID != "" && m.flowHistoryCallLabels[tc.ID] == "" {
						m.flowHistoryCallLabels[tc.ID] = flowLabelFor(tc.Name, tc.Arguments)
					}
					if tc.ID != "" {
						if _, taken := m.flowHistoryCaller[tc.ID]; !taken {
							m.flowHistoryCaller[tc.ID] = ref // this subagent called it
						}
					}
				}
			}
		}
		m.flowHistoryPrompt[ref] = prompt
	}
	for i := range artifacts {
		art := artifacts[i]
		ref := art.Ref
		if known := m.flowHistoryByRef[ref]; known != nil {
			flowApplyMeta(known, art.Meta)
		} else {
			m.flowHistoryByRef[ref] = flowNodeFromArtifact(art, m.flowHistoryCallLabels, m.flowHistoryPrompt[ref])
		}
		byRef[ref] = m.flowHistoryByRef[ref]
		owner[strings.TrimSpace(art.Meta.ParentToolCallID)] = ref
	}

	// Pass 3: link children under the subagent whose transcript contains their
	// caller tool-call ID; runs called by the main agent (or with an unknown
	// caller, e.g. /subtask synthetic IDs, or a run resumed into a new owner)
	// hang off the main agent root.
	var roots []*flowNode
	for _, ref := range sortedRefs(byRef) {
		node := byRef[ref]
		caller := m.flowHistoryCaller[node.parentID]
		switch {
		case caller == "" || caller == ref:
			roots = append(roots, node)
		case byRef[caller] != nil:
			byRef[caller].children = append(byRef[caller].children, node)
		default:
			roots = append(roots, node)
		}
	}
	sortByCreated(roots)
	for _, n := range byRef {
		sortByCreated(n.children)
	}
	m.flowHistory = roots
	m.flowHistoryCallOwner = owner

	// Whole-session main-agent usage + turn count come from the BranchMeta
	// sidecar, which the controller saves after each turn (main agent's own
	// cumulative spend; subagent spend is kept in the SubagentStore instead).
	m.flowSessionMain = nil
	m.flowSessionMainCost = 0
	m.flowSessionTurns = 0
	if meta, ok, _ := agent.LoadBranchMeta(m.ctrl.SessionPath()); ok {
		m.flowSessionTurns = meta.Turns
		if u := meta.SessionUsage; u != nil {
			m.flowSessionMain = &provider.Usage{
				PromptTokens:     u.PromptTokens,
				CompletionTokens: u.CompletionTokens,
				TotalTokens:      u.TotalTokens,
				CacheHitTokens:   u.CacheHitTokens,
				CacheMissTokens:  u.CacheMissTokens,
				ReasoningTokens:  u.ReasoningTokens,
			}
			m.flowSessionMainCost = u.Cost
		}
	}
}

// indexToolCalls records the label for each tool call in a transcript.
func indexToolCalls(calls []provider.ToolCall, labels map[string]string) {
	for _, tc := range calls {
		if tc.ID == "" || labels[tc.ID] != "" {
			continue
		}
		labels[tc.ID] = flowLabelFor(tc.Name, tc.Arguments)
	}
}

// flowLabelFor mirrors the live-tree node label for a tool call so persisted
// subagent nodes read the same ("Task(Locate tests)", "explore(find X)"…).
func flowLabelFor(name, args string) string {
	if arg := toolArg(name, args); arg != "" {
		return flowLabel(event.Tool{Name: name, Args: args})
	}
	return toolDisplayName(name)
}

// firstUserPrompt returns the run's opening user message (used as the label
// fallback when a subagent run carries no description, e.g. /subtask), preferring
// an already-known one when a session lacks user messages.
func firstUserPrompt(sess *agent.Session, fallback string) string {
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	if sess == nil {
		return fallback
	}
	for _, msg := range sess.Messages {
		if msg.Role == provider.RoleUser && strings.TrimSpace(msg.Content) != "" &&
			msg.Content != agent.InterruptedTurnContinueMessage {
			return strings.TrimSpace(msg.Content)
		}
	}
	return fallback
}

// flowNodeFromArtifact builds a flow popup node from a persisted subagent run.
func flowNodeFromArtifact(art agent.SubagentArtifact, labels map[string]string, prompt string) *flowNode {
	meta := art.Meta
	n := &flowNode{
		id:       meta.Ref,
		parentID: strings.TrimSpace(meta.ParentToolCallID),
		kind:     flowSubtask,
		status:   flowStatusFromSubagent(meta.Status),
		model:    meta.Model,
		effort:   meta.Effort,
		created:  meta.CreatedAt,
	}
	if d := labels[meta.ParentToolCallID]; d != "" {
		n.label = d
	} else {
		// The persisted run has no description of its own, so fall back to a
		// generic verb + the run's opening prompt.
		n.label = flowStoreLabel(meta.Name, meta.Kind, prompt)
	}
	if u := meta.Usage; u != nil {
		n.usage = &provider.Usage{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			TotalTokens:      u.TotalTokens,
			CacheHitTokens:   u.CacheHitTokens,
			CacheMissTokens:  u.CacheMissTokens,
			ReasoningTokens:  u.ReasoningTokens,
		}
		n.cost = u.Cost
	}
	if t := meta.UpdatedAt; t.After(meta.CreatedAt) && !meta.CreatedAt.IsZero() {
		n.durationMs = t.Sub(meta.CreatedAt).Milliseconds()
	}
	return n
}

// flowStoreLabel is the persisted-run fallback label ("Task(desc)", "explore(desc)").
func flowStoreLabel(name, kind, prompt string) string {
	verb := name
	if kind == "task" {
		verb = "Task"
	}
	if p := clampPlain(strings.TrimSpace(prompt), 60); p != "" {
		return verb + "(" + p + ")"
	}
	return verb
}

// flowApplyMeta folds a (possibly refreshed) SubagentMeta into an existing
// history node: status, cumulative usage, duration, model/effort.
func flowApplyMeta(n *flowNode, meta agent.SubagentMeta) {
	n.status = flowStatusFromSubagent(meta.Status)
	if u := meta.Usage; u != nil {
		n.usage = &provider.Usage{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			TotalTokens:      u.TotalTokens,
			CacheHitTokens:   u.CacheHitTokens,
			CacheMissTokens:  u.CacheMissTokens,
			ReasoningTokens:  u.ReasoningTokens,
		}
		n.cost = u.Cost
	}
	if meta.Model != "" {
		n.model = meta.Model
	}
	if meta.Effort != "" {
		n.effort = meta.Effort
	}
	if t := meta.UpdatedAt; t.After(meta.CreatedAt) && !meta.CreatedAt.IsZero() {
		n.durationMs = t.Sub(meta.CreatedAt).Milliseconds()
	}
}

// flowStatusFromSubagent maps a persisted run status onto the live flow status.
func flowStatusFromSubagent(s agent.SubagentStatus) flowStatus {
	switch s {
	case agent.SubagentCompleted:
		return flowCompleted
	case agent.SubagentRunning:
		return flowRunning
	default: // failed, interrupted, unknown
		return flowFailed
	}
}

// sortedRefs returns the node refs in stable (creation-time) order.
func sortedRefs(nodes map[string]*flowNode) []string {
	refs := make([]string, 0, len(nodes))
	for ref := range nodes {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := nodes[refs[i]].created, nodes[refs[j]].created
		if a.Equal(b) {
			return refs[i] < refs[j]
		}
		return a.Before(b)
	})
	return refs
}

func sortByCreated(nodes []*flowNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i].created, nodes[j].created
		if a.Equal(b) {
			return nodes[i].id < nodes[j].id
		}
		return a.Before(b)
	})
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
		// A new user turn opens with the whole-session history seeded from the
		// persisted subagent store; the live overlay below re-accumulates as the
		// turn's events arrive.
		m.flowLoadHistory(true)
	case event.ToolDispatch:
		if e.Tool.Partial {
			return
		}
		m.flowDispatch(e.Tool)
	case event.ToolResult:
		m.flowResult(e.Tool)
		// A just-finished sub-agent lands a completed transcript in the store, so
		// its history node (status/usage) catches up while the popup is open.
		if m.flowOpen && isSubagentTool(e.Tool.Name) {
			m.flowLoadHistory(false)
		}
	case event.Usage:
		if e.Usage != nil {
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
// the tree; its subtask descendants come from the whole-session history
// (persisted SubagentStore) plus the current turn's live overlay (see
// flowSessionLines). Scrolling is clamped against the rendered line count here,
// so the scroll offset always lands on a real line.
func (m *chatTUI) renderFlow() string {
	if !m.flowOpen {
		return ""
	}
	w := max(m.width, 10)
	var b strings.Builder
	title := accent(i18n.M.FlowTitle)
	if m.flowSessionTurns > 0 {
		title += dim(" · " + fmt.Sprintf(i18n.M.FlowTurnsFmt, m.flowSessionTurns))
	}
	b.WriteString(title + "\n")

	// Main agent: the BranchMeta sidecar's cumulative spend across the session,
	// reconciled with the live per-turn accumulator. The live counter is the very
	// same accumulation the snapshot was written from — only later — so when it
	// has outrun the snapshot it is simply the newer truth, not something to add
	// on top. This keeps exactly one figure per line (no doubling-looking hints).
	mainUsage, mainCost := m.flowMainSessionUsage()
	root := bold(i18n.M.FlowMainAgent)
	if mr := m.modelRef; mr != "" {
		root += dim(" · " + mr)
	}
	if mainUsage != nil && mainUsage.TotalTokens > 0 {
		root += " · " + flowUsageLine(mainUsage, mainCost)
	}
	b.WriteString(root + "\n")

	lines := m.flowSessionLines()
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

	// Subagent spend across the whole session (persisted runs + live in-flight),
	// then the grand total across all agents. Each line is additive, so the
	// numbers always reconcile instead of drifting when history and live sources
	// disagree.
	subUsage, subCost := m.flowSubagentSessionUsage()
	if subUsage != nil && subUsage.TotalTokens > 0 {
		b.WriteString(dim(i18n.M.FlowSubagentAll) + " · " + flowUsageLine(subUsage, subCost) + "\n")
	}
	if totalUsage, totalCost := flowAddUsage(mainUsage, mainCost, subUsage, subCost); totalUsage.TotalTokens > 0 {
		b.WriteString(dim(i18n.M.FlowTotalAll) + " · " + flowUsageLine(totalUsage, totalCost) + "\n")
	}
	b.WriteString(dim(i18n.M.FlowHint))
	return choicePanelStyle.Width(w).Render(strings.TrimRight(b.String(), "\n"))
}

// flowMainSessionUsage returns the whole-session main-agent spend. The BranchMeta
// sidecar is authoritative across resumes; the live per-turn accumulator (the
// same counter, a few seconds newer) takes over when it has moved past the last
// snapshot — a fresh session mid-first-turn, or an in-flight turn past the last
// auto-save. One source is always returned, never a sum.
func (m *chatTUI) flowMainSessionUsage() (*provider.Usage, float64) {
	live := &provider.Usage{
		TotalTokens:      m.flowMainTokens,
		PromptTokens:     m.flowMainCacheHit + m.flowMainCacheMiss,
		CacheHitTokens:   m.flowMainCacheHit,
		CacheMissTokens:  m.flowMainCacheMiss,
		CompletionTokens: m.flowMainCompletion,
		ReasoningTokens:  m.flowMainReasoning,
	}
	if m.flowSessionMain != nil {
		// Sidecar is at least as fresh as the live counter → it's the number.
		if m.flowMainTokens <= 0 || m.flowMainTokens <= m.flowSessionMain.TotalTokens {
			return m.flowSessionMain, m.flowSessionMainCost
		}
		// Live has moved past the snapshot → it is the number (not a sum).
		if live.TotalTokens > 0 {
			return live, m.flowMainCost
		}
		return m.flowSessionMain, m.flowSessionMainCost
	}
	if live.TotalTokens <= 0 {
		return nil, 0
	}
	return live, m.flowMainCost
}

// flowSubagentSessionUsage sums every subagent node's spend — the persisted
// history tree plus the live overlay subagents still in flight — so the
// "subagents" line is always the union of both sources with no double count.
func (m *chatTUI) flowSubagentSessionUsage() (*provider.Usage, float64) {
	out := &provider.Usage{}
	var cost float64
	var walk func([]*flowNode)
	walk = func(nodes []*flowNode) {
		for _, n := range nodes {
			if n.usage != nil {
				out = addUsage(out, n.usage)
			}
			cost += n.cost
			walk(n.children)
		}
	}
	walk(m.flowHistory)
	walk(m.flowOverlayRoots())
	if out.TotalTokens == 0 {
		return nil, float64(cost)
	}
	return out, float64(cost)
}

// flowAddUsage folds two usage records together (addition only — never a
// subtraction, so cross-source totals cannot go negative or absurdly large).
func flowAddUsage(a *provider.Usage, aCost float64, b *provider.Usage, bCost float64) (*provider.Usage, float64) {
	if a == nil {
		a = &provider.Usage{}
	}
	if b == nil {
		b = &provider.Usage{}
	}
	out := &provider.Usage{}
	out = addUsage(out, a)
	out = addUsage(out, b)
	return out, aCost + bCost
}

// flowSessionLines renders the whole-session subagent tree: the persisted
// history first, then the current turn's live-overlay subagents that have not
// yet persisted a transcript (so in-flight runs animate without duplicating the
// ones history already shows).
func (m *chatTUI) flowSessionLines() []string {
	lines := m.flowLines(m.flowHistory, 1)
	overlay := m.flowOverlayRoots()
	if len(overlay) > 0 {
		lines = append(lines, m.flowLines(overlay, 1)...)
	}
	return lines
}

// flowOverlayRoots returns the live current-turn subtask nodes whose spawn is
// not yet captured by a persisted history node, so they animate (status glyph,
// running spinner, streaming usage) without duplicating history-covered ones.
func (m *chatTUI) flowOverlayRoots() []*flowNode {
	var out []*flowNode
	var walk func([]*flowNode)
	walk = func(nodes []*flowNode) {
		for _, n := range nodes {
			if n.kind != flowSubtask {
				walk(n.children)
				continue
			}
			if _, ok := m.flowHistoryCallOwner[n.id]; ok {
				walk(n.children)
				continue
			}
			out = append(out, n)
		}
	}
	walk(m.flowRoots)
	return out
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

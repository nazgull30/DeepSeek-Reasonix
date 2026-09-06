package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/instruction"
	"reasonix/internal/jobs"
	"reasonix/internal/memory"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// DefaultTaskSystemPrompt steers a sub-agent toward focused, terse delivery —
// it doesn't see the parent's conversation so it must self-contain.
const DefaultTaskSystemPrompt = `You are a sub-agent invoked by a parent coding agent to carry out one focused task.
Use the provided tools to investigate or act. Return a single final answer that is concise
and self-contained — the parent will see only that answer, not your tool calls or reasoning.
If you need to ask for clarification, fail with a precise question instead of guessing.`

var subagentMetaTools = []string{
	"task",
	"parallel_tasks",
	"run_skill",
	"read_skill",
	"install_skill",
	"install_source",
	"explore",
	"research",
	"review",
	"security_review",
}

var subagentJobTools = []string{
	"wait",
	"bash_output",
	"kill_shell",
}

const subagentToolBoundarySummary = "Recursive agent/skill tools and unsupported background job tools (wait, bash_output, kill_shell) are excluded; bash is exposed as foreground-only inside subagents."

// SubagentMetaTools returns the tool names that spawned agents should not inherit
// from the parent registry unless a future call site deliberately opts into a
// different boundary. They can spawn or author more agent work, so excluding them
// preserves one layer of delegation without adding a spawn-count cap.
func SubagentMetaTools() []string {
	out := make([]string, len(subagentMetaTools))
	copy(out, subagentMetaTools)
	return out
}

// SubagentToolRegistry returns the tool set exposed inside spawned sub-agents:
// the requested whitelist (or every parent tool), minus meta tools that would
// spawn more agent work and job tools whose runtime manager is not injected into
// sub-agents. When bash is present, it is wrapped to advertise and allow only
// foreground execution.
func SubagentToolRegistry(parent *tool.Registry, names []string) *tool.Registry {
	exclude := append(SubagentMetaTools(), subagentJobTools...)
	sub := FilterRegistry(parent, names, exclude...)
	if bash, ok := sub.Get("bash"); ok {
		sub.Add(foregroundOnlyBash{inner: bash})
	}
	return sub
}

type foregroundOnlyBash struct {
	inner tool.Tool
}

func (b foregroundOnlyBash) Name() string { return "bash" }

func (b foregroundOnlyBash) Description() string {
	desc := strings.TrimSpace(b.inner.Description())
	if desc == "" {
		desc = "Execute a command in the shell and return combined stdout/stderr."
	}
	desc = strings.Replace(desc, "Execute a command in the shell", "Execute a foreground command in the shell", 1)
	return desc + " Background execution is unavailable inside subagents."
}

func (b foregroundOnlyBash) Schema() json.RawMessage {
	// Derive from the inner bash schema so field descriptions stay in sync.
	// Only the `command` property is exposed — background execution and other
	// fields available in the parent bash are stripped for sub-agents.
	var innerSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	innerJSON := b.inner.Schema()
	if err := json.Unmarshal(innerJSON, &innerSchema); err == nil {
		if cmdProp, ok := innerSchema.Properties["command"]; ok {
			out, err := json.Marshal(map[string]interface{}{
				"type": "object",
				"properties": map[string]json.RawMessage{
					"command": cmdProp,
				},
				"required": []string{"command"},
			})
			if err == nil {
				return json.RawMessage(out)
			}
		}
	}
	// Fallback — should not be reached in practice.
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Shell command to execute in the foreground"}},"required":["command"]}`)
}

func (b foregroundOnlyBash) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		RunInBackground bool `json:"run_in_background"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.RunInBackground {
		return "", fmt.Errorf("background bash is unavailable in subagents; run a foreground command or ask the parent agent to start a background job")
	}
	return b.inner.Execute(ctx, args)
}

func (b foregroundOnlyBash) ReadOnly() bool { return b.inner.ReadOnly() }

// TaskTool spawns a sub-agent in its own session for a focused sub-task. The
// sub-agent runs with a filtered tool whitelist and the same step budget shape
// as the parent (see Execute); its tool calls are forwarded to the parent's
// event stream nested under this call, while only its final assistant message is
// returned to the parent model. Use cases: keep noisy tool sequences (multi-file
// exploration, repeated grep / read_file) out of the parent's context budget, or
// parallel research across independent areas (the parallel-dispatch path picks
// these up only when readOnly, which task is not).
type TaskTool struct {
	prov              provider.Provider
	pricing           *provider.Pricing
	parentReg         *tool.Registry
	maxSteps          int
	contextWindow     int
	softCompactRatio  float64
	compactRatio      float64
	compactForceRatio float64
	timeBasedRatio    float64
	cacheIdleTTL      time.Duration
	recentKeep        int
	temperature       float64
	archiveDir        string
	keepPolicy        KeepPolicy
	sysPrompt         string
	gate              Gate
	subagentModel     string
	subagentEffort    string
	resolveProvider   func(modelRef, effort string) (provider.Provider, *provider.Pricing, int, error)
	transcripts       *SubagentStore
	workspaceRoot     string
	baseModel         string
	baseEffort        string
	identityProfile   func(modelRef, effort string) (string, string)
	projectChecks     []instruction.VerifyCheck
	// parentMessages returns a snapshot of the parent agent's conversation
	// messages at the time of the task call. Used for the fork (cache_from_parent)
	// pattern: the sub-agent inherits the parent's complete message history so
	// multiple children forked from the same point share the server-side prompt
	// cache. When nil, cache_from_parent is unavailable.
	parentMessages func() []provider.Message
	// parentResultState is the parent agent's frozen tool-result replacement
	// state, cloned for each spawned sub-agent so they inherit the parent's
	// byte-identical message prefix. The thunk is evaluated at Execute time so
	// the caller can wire it before the state is constructed.
	parentResultState func() *ContentReplacementState
}

// NewTaskTool wires a task tool to the parent agent's environment so its
// sub-agents can use the same provider and tools. sysPrompt is the system
// prompt every sub-agent starts with; pass "" for DefaultTaskSystemPrompt. gate
// is the permission gate sub-agents inherit — pass the headless variant so
// deny rules still bite while autonomous sub-agents are never blocked on an
// interactive prompt (there is no UI to answer one).
func NewTaskTool(prov provider.Provider, pricing *provider.Pricing, parentReg *tool.Registry,
	maxSteps, contextWindow, recentKeep int, softCompactRatio, compactRatio, compactForceRatio, temperature float64, archiveDir, sysPrompt string, gate Gate,
	keepPolicy KeepPolicy, subagentModel, subagentEffort string, resolveProvider func(string, string) (provider.Provider, *provider.Pricing, int, error), projectChecks []instruction.VerifyCheck) *TaskTool {
	if sysPrompt == "" {
		sysPrompt = DefaultTaskSystemPrompt
	}
	return &TaskTool{
		prov:              prov,
		pricing:           pricing,
		parentReg:         parentReg,
		maxSteps:          maxSteps,
		contextWindow:     contextWindow,
		recentKeep:        recentKeep,
		softCompactRatio:  softCompactRatio,
		compactRatio:      compactRatio,
		compactForceRatio: compactForceRatio,
		temperature:       temperature,
		archiveDir:        archiveDir,
		keepPolicy:        keepPolicy,
		sysPrompt:         sysPrompt,
		gate:              gate,
		subagentModel:     subagentModel,
		subagentEffort:    subagentEffort,
		resolveProvider:   resolveProvider,
		projectChecks:     append([]instruction.VerifyCheck(nil), projectChecks...),
	}
}

// WithTimeBasedCompaction enables idle-time micro-compaction for sub-agents that
// follow the affordances TimeBasedCompactRatio / CacheIdleTTL.
func (t *TaskTool) WithTimeBasedCompaction(ratio float64, idleTTL time.Duration) *TaskTool {
	if ratio > 0 {
		t.timeBasedRatio = ratio
		t.cacheIdleTTL = idleTTL
	}
	return t
}

// WithTranscripts enables persisted sub-agent transcript continuation for this
// task tool. The base model/effort are the parent provider identity used when no
// subagent override is configured.
func (t *TaskTool) WithTranscripts(store *SubagentStore, workspaceRoot, baseModel, baseEffort string) *TaskTool {
	t.transcripts = store
	t.workspaceRoot = strings.TrimSpace(workspaceRoot)
	t.baseModel = strings.TrimSpace(baseModel)
	t.baseEffort = strings.TrimSpace(baseEffort)
	return t
}

func (t *TaskTool) WithTranscriptIdentityResolver(resolve func(modelRef, effort string) (string, string)) *TaskTool {
	t.identityProfile = resolve
	return t
}

// WithParentMessages supplies a function that returns the parent agent's current
// session messages. When set, the task tool can use cache_from_parent to fork the
// parent context for prompt cache sharing. The function is called at Execute time
// to capture the exact fork point.
func (t *TaskTool) WithParentMessages(fn func() []provider.Message) *TaskTool {
	t.parentMessages = fn
	return t
}

// WithParentResultState supplies a thunk that returns the parent agent's frozen
// tool-result replacement state. The thunk is evaluated at Execute time so the
// caller can wire it before the state is constructed. It is cloned for each
// spawned sub-agent so they inherit the parent's byte-identical message prefix.
func (t *TaskTool) WithParentResultState(fn func() *ContentReplacementState) *TaskTool {
	t.parentResultState = fn
	return t
}

func (t *TaskTool) Name() string { return "task" }

func (t *TaskTool) Description() string {
	return "Spawn a sub-agent for a focused sub-task. The sub-agent runs in its own session with the same provider and a filtered tool list (defaults to every parent tool, then applies the subagent boundary: " + subagentToolBoundarySummary + "). Only its final answer is returned. Use this to (a) keep long exploration sequences out of the parent's context budget, or (b) delegate self-contained work like 'find every place that calls X and summarise the patterns'."
}

func (t *TaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "prompt":{"type":"string","description":"What the sub-agent should accomplish. Be specific about the deliverable — the sub-agent does not see this conversation."},
  "description":{"type":"string","description":"Short label for the sub-task (3-7 words). Surfaced in the dispatch line so the user sees what's running."},
  "tools":{"type":"array","items":{"type":"string"},"description":"Optional tool whitelist. ` + subagentToolBoundarySummary + `"},
  "max_steps":{"type":"integer","description":"Optional cap on tool-call rounds. Defaults to half the parent's cap (min 5).","minimum":1},
  "run_in_background":{"type":"boolean","description":"Run the sub-agent asynchronously: returns a job id immediately and keeps working across turns. Collect its final answer with wait, and you'll be notified when it finishes. Use for long, independent sub-tasks you don't need to block on right now."},
  "model":{"type":"string","description":"Optional model override for the sub-agent (a configured provider/model name)."},
  "effort":{"type":"string","description":"Optional reasoning effort for the sub-agent (e.g. high, max)."},
  "cache_from_parent":{"type":"boolean","description":"When true, the sub-agent inherits the parent's full conversation context for prompt cache sharing. Multiple sub-agents spawned with cache_from_parent at the same point share the server-side prompt cache: the first pays cache_create, the rest pay only cache_read. Automatically enabled when the parent context is available — you typically don't need to set this explicitly. Set to false to disable. Not compatible with continue_from or fork_from. Default: true (auto)."},
  "continue_from":{"type":"string","description":"Resume a prior subagent run in place: the subagent retains its context from the previous run; use in iterative loops (e.g. review -> fix -> review again) by passing only the 'sa_...' value from the prior result's 'Subagent reference: ...' line. Requires a compatible subagent identity, including tools, model, effort, and workspace."},
  "fork_from":{"type":"string","description":"Fork a prior subagent run: copies its transcript, leaves the source unchanged, and continues independently. Use only when you need an independent branch; for iterative continuation on the same thread, use continue_from. Pass the 'sa_...' value from the prior result's 'Subagent reference: ...' line. Requires a compatible subagent identity, including tools, model, effort, and workspace. Mutually exclusive with continue_from."}
},
"required":["prompt"]
}`)
}

// ReadOnly is false: a sub-agent can invoke any whitelisted tool, including
// writers. Conservative classification keeps the parallel-dispatch path from
// running two sub-agents at once and letting their writes race.
func (t *TaskTool) ReadOnly() bool { return false }

// ResolveProfile extracts model/effort from task args and applies config defaults.
func (t *TaskTool) ResolveProfile(args json.RawMessage) *event.Profile {
	var p struct {
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil
	}
	model, effort := t.effectiveProfile(p.Model, p.Effort)
	if model == "" && effort == "" {
		return nil
	}
	return &event.Profile{Model: model, Effort: effort}
}

func (t *TaskTool) effectiveProfile(model, effort string) (string, string) {
	model = strings.TrimSpace(model)
	effort = strings.TrimSpace(effort)
	if model == "" {
		model = strings.TrimSpace(t.subagentModel)
	}
	if effort == "" {
		effort = strings.TrimSpace(t.subagentEffort)
	}
	return model, effort
}

func (t *TaskTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Prompt          string   `json:"prompt"`
		Description     string   `json:"description"`
		Tools           []string `json:"tools"`
		MaxSteps        int      `json:"max_steps"`
		RunInBackground bool     `json:"run_in_background"`
		Model           string   `json:"model"`
		Effort          string   `json:"effort"`
		CacheFromParent *bool    `json:"cache_from_parent"`
		ContinueFrom    string   `json:"continue_from"`
		ForkFrom        string   `json:"fork_from"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}

	cacheFromParent := p.CacheFromParent != nil && *p.CacheFromParent
	if cacheFromParent && (p.ContinueFrom != "" || p.ForkFrom != "") {
		return "", fmt.Errorf("cache_from_parent is not compatible with continue_from or fork_from")
	}

	maxSteps := p.MaxSteps
	if maxSteps <= 0 {
		// No explicit cap from the caller: mirror the parent. A finite parent caps
		// the sub-agent at half its budget (min 5) so a delegated sub-task stays
		// shorter than the whole turn; an unbounded parent yields an unbounded
		// sub-agent. The sub-agent shares the parent's ctx, so cancelling the turn
		// stops it, and it compacts its own context — the same bounds the parent has.
		if t.maxSteps > 0 {
			maxSteps = t.maxSteps / 2
			if maxSteps < 5 {
				maxSteps = 5
			}
		}
	}

	subReg := t.buildSubReg(p.Tools)
	modelRef, effortRef := t.effectiveProfile(p.Model, p.Effort)
	parentID, parent, _, _ := CallContext(ctx)
	parentSessionID := ParentSession(ctx)

	// Fork (cache_from_parent): inherit the parent's message history for prompt
	// cache sharing. This creates a subagent with the parent's full context so
	// multiple children from the same fork point share the server-side cache.
	// For forked subagents we skip the transcript identity check (the system
	// prompt lives in the inherited messages) and use an empty system prompt.
	//
	// When parent messages are wired (set via WithParentMessages), the subagent
	// automatically uses cache_from_parent unless it is incompatible (recursive
	// fork or no usable history) — in those cases it falls back to a normal
	// session. An explicit cache_from_parent flag overrides the auto-detection:
	// true forces inheritance (error when not wired), false disables it and runs
	// a fresh isolated subagent with no parent context.
	cacheFromParentExplicit := p.CacheFromParent != nil
	var forkedSession *Session
	var forkGuard bool
	if cacheFromParent || (!cacheFromParentExplicit && t.parentMessages != nil && p.ContinueFrom == "" && p.ForkFrom == "") {
		if t.parentMessages == nil {
			if cacheFromParent {
				return "", fmt.Errorf("cache_from_parent is not available in this context (parent messages not wired)")
			}
		} else {
			parentMsgs := t.parentMessages()
			if !IsForkChild(parentMsgs) && len(parentMsgs) > 1 {
				forkedSession = BuildForkSession(parentMsgs)
				if len(forkedSession.Messages) > 1 {
					forkGuard = true
				}
			}
		}
	}

	run, err := t.prepareTranscriptRun(subReg, modelRef, effortRef, parentSessionID, parentID, p.ContinueFrom, p.ForkFrom)
	if err != nil {
		return "", err
	}
	if forkGuard {
		run.Session = forkedSession
	}

	prov, pricing, ctxWin, err := t.resolveSubSessionRuntime(modelRef, effortRef)
	if err != nil {
		run.Release()
		return "", fmt.Errorf("sub-agent profile: %w", err)
	}

	// Background: register a job that runs the sub-agent under the manager's
	// session context (so it survives this turn) and return immediately. The
	// sub-agent's tool activity still streams, nested under this call, because the
	// nested sink captures the parent ID + stream now (not from the job ctx).
	if p.RunInBackground {
		jm, ok := jobs.FromContext(ctx)
		if !ok {
			if run != nil {
				run.Release()
			}
			return "", fmt.Errorf("background execution is not available in this context")
		}
		nested := subSinkFor(parentID, parent)
		label := p.Description
		if label == "" {
			label = "task"
		}
		if t.transcripts != nil && run != nil && run.Ref != "" {
			if err := t.transcripts.MarkRunning(run); err != nil {
				run.Release()
				return "", err
			}
		}
		job := jm.StartForSession(jobs.SessionFromContext(ctx), "task", label, func(jobCtx context.Context, _ io.Writer) (result string, err error) {
			defer run.Release()
			defer func() {
				if r := recover(); r != nil {
					panicErr := fmt.Errorf("internal error: panic: %v\n%s", r, debug.Stack())
					result = FormatSubagentResult("", run.Ref, true)
					err = errors.Join(panicErr, t.transcripts.SaveFailed(run))
				}
			}()
			answer, err := t.runSubSession(jobCtx, p.Prompt, subReg, nested, maxSteps, prov, pricing, ctxWin, run)
			if err != nil {
				return FormatSubagentResult("", run.Ref, true), errors.Join(err, t.transcripts.SaveFailed(run))
			}
			if err := t.transcripts.SaveCompleted(run); err != nil {
				return FormatSubagentResult("", run.Ref, true), errors.Join(err, t.transcripts.SaveFailed(run))
			}
			return FormatSubagentResult(answer, run.Ref, false), nil
		})
		if run != nil && run.Ref != "" {
			return fmt.Sprintf("Started background task %q (%s).\nSubagent reference: %s\nIt runs across turns; collect its final answer with wait (or wait will return it once done), and you'll be notified when it finishes.", job.ID, label, run.Ref), nil
		}
		return fmt.Sprintf("Started background task %q (%s). It runs across turns; collect its final answer with wait (or wait will return it once done), and you'll be notified when it finishes.", job.ID, label), nil
	}

	// Foreground: run synchronously, nesting events under this call.
	defer run.Release()
	answer, err := t.runSubSession(ctx, p.Prompt, subReg, subSink(ctx), maxSteps, prov, pricing, ctxWin, run)
	if err != nil {
		return "", errors.Join(err, t.transcripts.SaveFailed(run))
	}
	if t.transcripts != nil && run.Ref != "" {
		if err := t.transcripts.SaveCompleted(run); err != nil {
			return "", errors.Join(err, t.transcripts.SaveFailed(run))
		}
		return FormatSubagentResult(answer, run.Ref, false), nil
	}
	return answer, nil
}

func (t *TaskTool) prepareTranscriptRun(subReg *tool.Registry, modelRef, effortRef, parentSession, parentID, continueFrom, forkFrom string) (*SubagentRun, error) {
	continueFrom = strings.TrimSpace(continueFrom)
	forkFrom = strings.TrimSpace(forkFrom)
	parentSession = strings.TrimSpace(parentSession)
	if continueFrom != "" && forkFrom != "" {
		return nil, fmt.Errorf("continue_from and fork_from are mutually exclusive")
	}
	if t.transcripts == nil {
		return nil, fmt.Errorf("subagent transcript store is required")
	}
	// Headless runs (e.g. `reasonix run`) never mint a session path, so there is
	// no parent session to own a transcript. Run the sub-agent ephemerally —
	// exactly as before persisted transcripts existed — instead of failing the
	// call. Continuation/fork need a persisted owner, so they error here.
	if parentSession == "" {
		if continueFrom != "" || forkFrom != "" {
			return nil, fmt.Errorf("continue_from/fork_from require a persisted session; none is active in this run")
		}
		return EphemeralSubagentRun(t.sysPrompt), nil
	}
	identityModel, identityEffort := t.effectiveIdentity(modelRef, effortRef)
	spec := SubagentSpec{
		Kind:             "task",
		Name:             "task",
		WorkspaceRoot:    t.workspaceRoot,
		ParentSession:    parentSession,
		ParentToolCallID: parentID,
		SystemPrompt:     t.sysPrompt,
		Registry:         subReg,
		Model:            identityModel,
		Effort:           identityEffort,
	}
	if continueFrom != "" || forkFrom != "" {
		if continueFrom != "" {
			return t.transcripts.PrepareContinue(continueFrom, spec)
		}
		return t.transcripts.PrepareFork(forkFrom, spec)
	}
	return t.transcripts.PrepareFresh(spec)
}

func (t *TaskTool) effectiveIdentity(modelRef, effort string) (string, string) {
	if t.identityProfile != nil {
		model, eff := t.identityProfile(modelRef, effort)
		return strings.TrimSpace(model), strings.TrimSpace(eff)
	}
	return t.effectiveModelIdentity(modelRef), t.effectiveEffortIdentity(effort)
}

func (t *TaskTool) effectiveModelIdentity(modelRef string) string {
	if strings.TrimSpace(modelRef) != "" {
		return strings.TrimSpace(modelRef)
	}
	return strings.TrimSpace(t.baseModel)
}

func (t *TaskTool) effectiveEffortIdentity(effort string) string {
	if strings.TrimSpace(effort) != "" {
		return strings.TrimSpace(effort)
	}
	return strings.TrimSpace(t.baseEffort)
}

// buildSubReg returns the sub-agent's tool set: the named whitelist (minus
// unavailable sub-agent tools), or every parent tool except those tools.
func (t *TaskTool) buildSubReg(names []string) *tool.Registry {
	return SubagentToolRegistry(t.parentReg, names)
}

// FilterRegistry builds a sub-registry from parent: the named whitelist (empty =
// every parent tool), minus any excluded names. Used to scope what a spawned
// sub-agent — a `task` sub-agent or a subagent skill — may call, e.g. excluding
// `task` to bar recursive nesting, or restricting to a skill's allowed-tools.
func FilterRegistry(parent *tool.Registry, names []string, exclude ...string) *tool.Registry {
	sub := tool.NewRegistry()
	if parent == nil {
		return sub
	}
	ex := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		ex[e] = true
	}
	src := names
	if len(src) == 0 {
		src = parent.Names()
	}
	for _, name := range src {
		if ex[name] {
			continue
		}
		if tl, ok := parent.Get(name); ok {
			sub.Add(tl)
		}
	}
	return sub
}

var plannerNonResearchTools = []string{
	"ask",
	"bash_output",
	"complete_step",
	"slash_command",
	"todo_write",
	"wait",
}

// PlannerToolRegistry returns the tool set exposed to the two-model planner:
// read-only research tools only. It deliberately excludes workflow/meta tools
// that are technically read-only but can prompt the user, update visible task
// state, wait on jobs, or expand commands instead of inspecting context.
func PlannerToolRegistry(parent *tool.Registry) *tool.Registry {
	exclude := append(SubagentMetaTools(), plannerNonResearchTools...)
	return FilterReadOnlyRegistry(parent, exclude...)
}

// FilterReadOnlyRegistry builds a sub-registry containing only tools whose
// ReadOnly contract is true, minus explicit exclusions.
func FilterReadOnlyRegistry(parent *tool.Registry, exclude ...string) *tool.Registry {
	ex := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		ex[e] = true
	}
	sub := tool.NewRegistry()
	if parent == nil {
		return sub
	}
	for _, name := range parent.Names() {
		if ex[name] {
			continue
		}
		tl, ok := parent.Get(name)
		if !ok || !tl.ReadOnly() {
			continue
		}
		sub.Add(tl)
	}
	return sub
}

func (t *TaskTool) resolveSubSessionRuntime(modelRef, effort string) (provider.Provider, *provider.Pricing, int, error) {
	prov, pricing, ctxWin := t.prov, t.pricing, t.contextWindow
	if t.resolveProvider != nil && (modelRef != "" || effort != "") {
		p, pr, cw, err := t.resolveProvider(modelRef, effort)
		if err != nil {
			return nil, nil, 0, err
		}
		prov, pricing, ctxWin = p, pr, cw
	}
	return prov, pricing, ctxWin, nil
}

func (t *TaskTool) runSubSession(ctx context.Context, prompt string, subReg *tool.Registry, sink event.Sink, maxSteps int, prov provider.Provider, pricing *provider.Pricing, ctxWin int, run *SubagentRun) (string, error) {
	mq, _ := memory.QueueFromContext(ctx)
	var resultState *ContentReplacementState
	if t.parentResultState != nil {
		if rs := t.parentResultState(); rs != nil {
			resultState = rs.Clone()
		}
	}
	onUsage := func(u SessionUsageMeta) {
		if run != nil {
			run.Meta.Usage = &u
		}
	}
	return RunSubAgentWithSession(ctx, prov, subReg, run.Session, prompt, Options{
		MaxSteps:          maxSteps,
		Temperature:       t.temperature,
		Pricing:           pricing,
		UsageSource:       event.UsageSourceSubagent,
		Gate:              t.gate,
		ContextWindow:     ctxWin,
		ProjectChecks:     t.projectChecks,
		RecentKeep:        t.recentKeep,
		SoftCompactRatio:  t.softCompactRatio,
		CompactRatio:      t.compactRatio,
		CompactForceRatio: t.compactForceRatio,
		TimeBasedCompactRatio: t.timeBasedRatio,
		CacheIdleTTL:         t.cacheIdleTTL,
		ArchiveDir:        t.archiveDir,
		KeepPolicy:        t.keepPolicy,
		ReasoningLanguage: ReasoningLanguageFromContext(ctx),
		MemoryQueue:       mq,
		ResultState:       resultState,
		OnSubagentUsage:   onUsage,
	}, sink)
}

func FormatSubagentResult(answer, ref string, failed bool) string {
	if ref == "" {
		return answer
	}
	if failed {
		if answer == "" {
			return "Subagent reference (failed): " + ref
		}
		return "Subagent reference (failed): " + ref + "\n\nFinal answer:\n" + answer
	}
	return "Subagent reference: " + ref + "\n\nFinal answer:\n" + answer
}

// RunSubAgentWithSession continues an existing sub-agent session with prompt and
// returns the latest final assistant answer. Fresh sub-agents pass a newly-created
// session; continued sub-agents pass a loaded transcript session.
func RunSubAgentWithSession(ctx context.Context, prov provider.Provider, reg *tool.Registry, sess *Session, prompt string, opts Options, sink event.Sink) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("sub-agent session is nil")
	}
	sub := New(prov, reg, sess, opts, sink)
	if err := sub.Run(ctx, prompt); err != nil {
		return "", fmt.Errorf("sub-agent: %w", err)
	}
	if opts.OnSubagentUsage != nil {
		opts.OnSubagentUsage(sub.SessionUsage())
	}
	// Walk the session backwards for the last assistant message with content —
	// that's the sub-agent's final answer. Intermediate assistant messages with
	// tool_calls but no text don't count.
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		m := sess.Messages[i]
		if m.Role == provider.RoleAssistant && strings.TrimSpace(m.Content) != "" {
			return m.Content, nil
		}
	}
	return "", fmt.Errorf("sub-agent finished without producing a final answer")
}

// NestedSink returns a sink that forwards a sub-agent's tool activity to the
// parent stream, nested under the tool call carried by ctx, so a frontend shows
// it beneath that call (the same nesting `task` uses). Falls back to the given
// sink when ctx carries no call context. Used by subagent skills.
func NestedSink(ctx context.Context, fallback event.Sink) event.Sink {
	parentID, parent, _, ok := CallContext(ctx)
	if !ok || parent == nil {
		return fallback
	}
	return subSinkFor(parentID, parent)
}

// subSink forwards a sub-agent's tool dispatch/result events and billable usage
// to the parent's event stream. Only tool activity is nested visually; the
// sub-agent's text/reasoning stays isolated and only its final answer is returned.
//
// The sub-agent's own turn/text/reasoning events are dropped — forwarding them
// would make the parent transcript noisy and could imply they belong to the
// parent model context, which they do not.
//
// Usage events are observability only, so forwarding them preserves billing
// totals without polluting the parent provider-visible prefix.
//
// Tool events are tagged with the parent task call's ID so a frontend nests them
// under it. The forwarded call IDs are namespaced with the parent ID so a
// sub-agent call can never collide with a parent call in the frontend's
// dispatch→result matching. Falls back to Discard when there's no parent stream
// (the headless run loop, or a direct Execute in tests).
func subSink(ctx context.Context) event.Sink {
	parentID, parent, _, ok := CallContext(ctx)
	if !ok || parent == nil {
		return event.Discard
	}
	return subSinkFor(parentID, parent)
}

// subSinkFor builds the nesting sink from an already-captured parent ID + stream,
// for the background path where the job runs under a context that no longer
// carries the call context. Falls back to Discard when there's no parent stream.
func subSinkFor(parentID string, parent event.Sink) event.Sink {
	if parent == nil {
		return event.Discard
	}
	return event.FuncSink(func(e event.Event) {
		switch e.Kind {
		case event.ToolDispatch, event.ToolResult:
			e.Tool.ParentID = parentID
			e.Tool.ID = parentID + "/" + e.Tool.ID
			parent.Emit(e)
		case event.Usage:
			if e.UsageSource == "" {
				e.UsageSource = event.UsageSourceSubagent
			}
			// Tag the usage with the owning task call so a frontend can
			// attribute sub-agent token spend back to its parent card.
			e.ParentID = parentID
			parent.Emit(e)
		}
	})
}

package workflow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

// Engine executes a workflow body in a sandboxed Starlark interpreter. The
// script's only means of affecting the world are the injected agent hooks:
//
//	agent(prompt, ...)              run one sub-agent, return its final answer
//	parallel([{...}, ...])          run several sub-agents concurrently
//	pipeline(stages, items)         run items through a chain of stages
//	phase(title, detail="")         report a progress phase (observer-only)
//	log(message)                    narrate progress
//	args                            the run's input args (tool-supplied)
//
// The script returns by assigning a top-level `result` variable or by defining
// `def main():` whose value is used; a missing result is None. Execution is
// bounded (a step budget), cancelable (the caller's context kills in-flight
// sub-agents and aborts the interpreter), and side-effect-free apart from the
// hooks.
type Engine struct {
	spawn         SpawnFunc
	onPhase       func(title, detail string)
	onLog         func(message string)
	maxConcurrent int
	maxExecSteps  uint64
}

// AgentSpec describes one sub-agent call a workflow script makes. Fields map
// 1:1 onto the task tool's options, so every per-subtask setting available to
// `/subtask` (true:/false:, continue_from=/fork_from=) and the model's `task`
// call is reachable from a script.
type AgentSpec struct {
	Prompt          string   `json:"prompt"`
	Description     string   `json:"description"`
	Tools           []string `json:"tools"`
	MaxSteps        int      `json:"max_steps"`
	Model           string   `json:"model"`
	Effort          string   `json:"effort"`
	CacheFromParent *bool    `json:"cache_from_parent"`
	ContinueFrom    string   `json:"continue_from"`
	ForkFrom        string   `json:"fork_from"`
	IncludeRef      bool     `json:"include_ref"`
}

// AgentResult is a completed sub-agent call: the final answer text plus an
// optional subagent reference ("sa_...") that future calls can pass to
// continue_from / fork_from.
type AgentResult struct {
	Text string
	Ref  string
}

// SpawnFunc launches one sub-agent from a workflow and returns its result.
type SpawnFunc func(ctx context.Context, spec AgentSpec) (AgentResult, error)

// Options configure an Engine.
type Options struct {
	// Spawn is the per-call sub-agent launch hook. Required.
	Spawn SpawnFunc
	// OnPhase, when set, receives every phase() call (progress labels only).
	OnPhase func(title, detail string)
	// OnLog, when set, receives every log() call.
	OnLog func(message string)
	// MaxConcurrent bounds how many sub-agents run at once. Zero uses
	// DefaultMaxConcurrent.
	MaxConcurrent int
	// MaxExecutionSteps bounds Starlark evaluation. Zero uses
	// DefaultMaxExecutionSteps.
	MaxExecutionSteps uint64
}

const (
	// DefaultMaxExecutionSteps caps Starlark evaluation so a misbehaving script
	// cannot run away; far beyond any real workflow's needs.
	DefaultMaxExecutionSteps uint64 = 1_000_000
	// DefaultMaxConcurrent caps simultaneous sub-agents per run.
	DefaultMaxConcurrent = 8
)

// NewEngine builds an Engine with the given options and the documented defaults
// for zero-valued limits.
func NewEngine(opts Options) *Engine {
	maxC := opts.MaxConcurrent
	if maxC <= 0 {
		maxC = DefaultMaxConcurrent
	}
	maxS := opts.MaxExecutionSteps
	if maxS == 0 {
		maxS = DefaultMaxExecutionSteps
	}
	return &Engine{
		spawn:         opts.Spawn,
		onPhase:       opts.OnPhase,
		onLog:         opts.OnLog,
		maxConcurrent: maxC,
		maxExecSteps:  maxS,
	}
}

// StopReason classifies how a workflow run ended.
type StopReason string

const (
	StopCompleted StopReason = "completed"
	StopError     StopReason = "error"
	StopCancelled StopReason = "cancelled"
)

// Result is the outcome of a single workflow run. Run never rejects — failures
// resolve as a StopError / StopCancelled result rather than an error return.
type Result struct {
	Value         any        `json:"value"`
	StopReason    StopReason `json:"stopReason"`
	Error         string     `json:"error,omitempty"`
	AgentsStarted int        `json:"agentsStarted"`
}

// Run executes body with the given input args (exposed to the script as the
// global `args`; nil becomes None). The return value is a top-level `result`
// assignment or the value returned by a top-level `main()` function.
func (e *Engine) Run(ctx context.Context, body string, inArgs any) Result {
	if e.spawn == nil {
		return Result{StopReason: StopError, Error: "workflow engine has no sub-agent spawn hook"}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	rs := &runState{
		ctx:     runCtx,
		sem:     make(chan struct{}, e.maxConcurrent),
		spawn:   e.spawn,
		onPhase: e.onPhase,
		onLog:   e.onLog,
	}
	thread := &starlark.Thread{
		Name: "workflow",
		Print: func(_ *starlark.Thread, msg string) {
			if e.onLog != nil {
				e.onLog(msg)
			}
		},
	}
	thread.SetMaxExecutionSteps(e.maxExecSteps)
	thread.SetLocal(runLocal, rs)

	// Cancel the interpreter (and in-flight sub-agents) when the caller
	// cancels. The stopWatch channel lets this goroutine retire the moment
	// the run finishes, so even a long-lived session context leaks nothing.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-stopWatch:
		case <-ctx.Done():
			cancel()
			thread.Cancel("workflow cancelled")
		}
	}()

	predeclared := starlark.StringDict{
		"args":     goToStarlark(inArgs),
		"agent":    starlark.NewBuiltin("agent", hookAgent),
		"parallel": starlark.NewBuiltin("parallel", hookParallel),
		"pipeline": starlark.NewBuiltin("pipeline", hookPipeline),
		"phase":    starlark.NewBuiltin("phase", hookPhase),
		"log":      starlark.NewBuiltin("log", hookLog),
	}

	// Full Starlark control flow (top-level loops, while) is available to
	// workflows; the execution-step budget above keeps any runaway script
	// bounded, so the friendlier dialect is safe.
	fileOpts := &syntax.FileOptions{
		TopLevelControl: true,
		While:           true,
	}
	globals, err := starlark.ExecFileOptions(fileOpts, thread, "<workflow>", body, predeclared)
	if err != nil {
		reason := StopError
		if ctx.Err() != nil {
			reason = StopCancelled
		}
		return Result{StopReason: reason, Error: err.Error(), AgentsStarted: int(rs.started.Load())}
	}

	var value starlark.Value = starlark.None
	if v, ok := globals["result"]; ok {
		value = v
	} else if fn, ok := globals["main"].(*starlark.Function); ok {
		callVal, callErr := starlark.Call(thread, fn, nil, nil)
		if callErr != nil {
			reason := StopError
			if ctx.Err() != nil {
				reason = StopCancelled
			}
			return Result{StopReason: reason, Error: callErr.Error(), AgentsStarted: int(rs.started.Load())}
		}
		value = callVal
	}
	return Result{Value: starlarkToGo(value), StopReason: StopCompleted, AgentsStarted: int(rs.started.Load())}
}

const runLocal = "reasonix.workflow.run"

// runState carries the per-run mutable state the hooks read and write. It is
// stored as a thread-local on the Starlark Thread so one Engine can execute
// runs concurrently without shared state.
type runState struct {
	ctx     context.Context
	sem     chan struct{}
	spawn   SpawnFunc
	onPhase func(title, detail string)
	onLog   func(message string)
	started atomic.Int64
}

func (rs *runState) acquire() bool {
	select {
	case rs.sem <- struct{}{}:
		return true
	case <-rs.ctx.Done():
		return false
	}
}

func (rs *runState) release() {
	<-rs.sem
}

// runOne executes one sub-agent call under the run's concurrency slot.
func (rs *runState) runOne(spec AgentSpec) (AgentResult, error) {
	rs.started.Add(1)
	if !rs.acquire() {
		return AgentResult{}, fmt.Errorf("workflow cancelled")
	}
	defer rs.release()
	return rs.spawn(rs.ctx, spec)
}

// runBatch executes a slice of agent specs concurrently, returning results in
// order. It always waits for every sub-agent to settle so no goroutine leaks,
// even when the run is cancelled mid-batch.
func runBatch(rs *runState, specs []AgentSpec) []batchResult {
	results := make([]batchResult, len(specs))
	var wg sync.WaitGroup
	for i, spec := range specs {
		wg.Add(1)
		go func(i int, spec AgentSpec) {
			defer wg.Done()
			r, err := rs.runOne(spec)
			results[i] = batchResult{result: r, err: err}
		}(i, spec)
	}
	wg.Wait()
	return results
}

type batchResult struct {
	result AgentResult
	err    error
}

func runStateOf(thread *starlark.Thread) *runState {
	rs, _ := thread.Local(runLocal).(*runState)
	if rs == nil {
		panic("workflow hook used outside Engine.Run")
	}
	return rs
}

// agentOpts is the keyword-argument surface of the agent() hook, mirroring the
// task tool's option set.
type agentOpts struct {
	description     string
	tools           stringList
	maxSteps        int
	model           string
	effort          string
	cacheFromParent optionalBool
	continueFrom    string
	forkFrom        string
	includeRef      bool
}

func (ao *agentOpts) spec(prompt string) AgentSpec {
	return AgentSpec{
		Prompt:          prompt,
		Description:     ao.description,
		Tools:           []string(ao.tools),
		MaxSteps:        ao.maxSteps,
		Model:           ao.model,
		Effort:          ao.effort,
		CacheFromParent: ao.cacheFromParent.v,
		ContinueFrom:    ao.continueFrom,
		ForkFrom:        ao.forkFrom,
		IncludeRef:      ao.includeRef,
	}
}

func hookAgent(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	rs := runStateOf(thread)
	var prompt string
	var ao agentOpts
	if err := starlark.UnpackArgs("agent", args, kwargs,
		"prompt", &prompt,
		"description?", &ao.description,
		"tools?", &ao.tools,
		"max_steps?", &ao.maxSteps,
		"model?", &ao.model,
		"effort?", &ao.effort,
		"cache_from_parent?", &ao.cacheFromParent,
		"continue_from?", &ao.continueFrom,
		"fork_from?", &ao.forkFrom,
		"include_ref?", &ao.includeRef,
	); err != nil {
		return nil, err
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("agent requires a non-empty prompt")
	}
	spec := ao.spec(prompt)
	r, err := rs.runOne(spec)
	if err != nil {
		return nil, err
	}
	return agentValue(r, spec.IncludeRef)
}

func hookParallel(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	rs := runStateOf(thread)
	var tasksValue starlark.Value
	if err := starlark.UnpackArgs("parallel", args, kwargs, "tasks", &tasksValue); err != nil {
		return nil, err
	}
	tasks, ok := tasksValue.(*starlark.List)
	if !ok {
		return nil, fmt.Errorf("parallel: expected a list of task dicts, got %s", tasksValue.Type())
	}
	n := tasks.Len()
	if n == 0 {
		return starlark.NewList(nil), nil
	}
	specs := make([]AgentSpec, n)
	for i := 0; i < n; i++ {
		spec, err := specFromValue(tasks.Index(i))
		if err != nil {
			return nil, fmt.Errorf("parallel task %d: %w", i+1, err)
		}
		specs[i] = spec
	}
	results := runBatch(rs, specs)
	out := make([]starlark.Value, n)
	for i, br := range results {
		if br.err != nil {
			return nil, br.err
		}
		v, err := agentValue(br.result, specs[i].IncludeRef)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return starlark.NewList(out), nil
}

func hookPipeline(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	rs := runStateOf(thread)
	var stagesValue, itemsValue starlark.Value
	if err := starlark.UnpackArgs("pipeline", args, kwargs, "stages", &stagesValue, "items", &itemsValue); err != nil {
		return nil, err
	}
	stages, ok := stagesValue.(*starlark.List)
	if !ok {
		return nil, fmt.Errorf("pipeline: stages must be a list of agent dicts, got %s", stagesValue.Type())
	}
	items, ok := itemsValue.(*starlark.List)
	if !ok {
		return nil, fmt.Errorf("pipeline: items must be a list, got %s", itemsValue.Type())
	}
	n := items.Len()
	if n == 0 {
		return starlark.NewList(nil), nil
	}
	itemsStr := make([]string, n)
	for i := 0; i < n; i++ {
		s, ok := starlark.AsString(items.Index(i))
		if !ok {
			return nil, fmt.Errorf("pipeline item %d must be a string, got %s", i+1, items.Index(i).Type())
		}
		itemsStr[i] = s
	}
	if stages.Len() == 0 {
		return nil, fmt.Errorf("pipeline requires at least one stage")
	}
	// prev holds each item's previous-stage result; before the first stage it
	// is the item itself, so `{prev}` is meaningful on every stage.
	prev := make([]string, n)
	copy(prev, itemsStr)
	for s := 0; s < stages.Len(); s++ {
		base, err := specFromValue(stages.Index(s))
		if err != nil {
			return nil, fmt.Errorf("pipeline stage %d: %w", s+1, err)
		}
		specs := make([]AgentSpec, n)
		for i := 0; i < n; i++ {
			spec := base
			spec.Prompt = expandTemplate(base.Prompt, itemsStr[i], prev[i])
			specs[i] = spec
		}
		results := runBatch(rs, specs)
		for i, br := range results {
			if br.err != nil {
				return nil, br.err
			}
			prev[i] = br.result.Text
		}
	}
	out := make([]starlark.Value, n)
	for i, text := range prev {
		out[i] = starlark.String(text)
	}
	return starlark.NewList(out), nil
}

func hookPhase(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	rs := runStateOf(thread)
	var title, detail string
	if err := starlark.UnpackArgs("phase", args, kwargs, "title", &title, "detail?", &detail); err != nil {
		return nil, err
	}
	if rs.onPhase != nil {
		rs.onPhase(title, detail)
	}
	return starlark.None, nil
}

func hookLog(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	rs := runStateOf(thread)
	var msg string
	if err := starlark.UnpackArgs("log", args, kwargs, "message", &msg); err != nil {
		return nil, err
	}
	if rs.onLog != nil {
		rs.onLog(msg)
	}
	return starlark.None, nil
}

// agentValue converts an AgentResult into the value a script sees. By default
// the plain answer text; with include_ref, a struct with .text and .ref so the
// script can thread a continue_from reference onward.
func agentValue(r AgentResult, includeRef bool) (starlark.Value, error) {
	if !includeRef {
		return starlark.String(r.Text), nil
	}
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"text": starlark.String(r.Text),
		"ref":  starlark.String(r.Ref),
	}), nil
}

// specFromValue converts a parallel()/pipeline() task entry into an AgentSpec.
// A bare string is shorthand for {"prompt": <string>}; a dict carries the same
// keys as agent()'s keyword arguments.
func specFromValue(v starlark.Value) (AgentSpec, error) {
	if s, ok := starlark.AsString(v); ok {
		return AgentSpec{Prompt: s}, nil
	}
	d, ok := v.(*starlark.Dict)
	if !ok {
		return AgentSpec{}, fmt.Errorf("expected a task dict or prompt string, got %s", v.Type())
	}
	prompt := dictString(d, "prompt")
	if strings.TrimSpace(prompt) == "" {
		return AgentSpec{}, fmt.Errorf("task dict requires a non-empty 'prompt'")
	}
	tools, err := dictStrings(d, "tools")
	if err != nil {
		return AgentSpec{}, err
	}
	spec := AgentSpec{
		Prompt:       prompt,
		Description:  dictString(d, "description"),
		Tools:        tools,
		MaxSteps:     dictInt(d, "max_steps"),
		Model:        dictString(d, "model"),
		Effort:       dictString(d, "effort"),
		ContinueFrom: dictString(d, "continue_from"),
		ForkFrom:     dictString(d, "fork_from"),
		IncludeRef:   dictBool(d, "include_ref"),
	}
	if b := dictBoolPtr(d, "cache_from_parent"); b != nil {
		spec.CacheFromParent = b
	}
	return spec, nil
}

// dictString reads a string field, "" when absent or mistyped (scripts should
// stay friction-free for the user to write).
func dictString(d *starlark.Dict, key string) string {
	v, ok, _ := d.Get(starlark.String(key))
	if !ok {
		return ""
	}
	s, _ := starlark.AsString(v)
	return s
}

func dictStrings(d *starlark.Dict, key string) ([]string, error) {
	v, ok, _ := d.Get(starlark.String(key))
	if !ok {
		return nil, nil
	}
	l, ok := v.(*starlark.List)
	if !ok {
		return nil, fmt.Errorf("'%s' must be a list of strings, got %s", key, v.Type())
	}
	out := make([]string, 0, l.Len())
	for i := 0; i < l.Len(); i++ {
		s, ok := starlark.AsString(l.Index(i))
		if !ok {
			return nil, fmt.Errorf("'%s' must be a list of strings, got %s at index %d", key, l.Index(i).Type(), i)
		}
		out = append(out, s)
	}
	return out, nil
}

func dictInt(d *starlark.Dict, key string) int {
	v, ok, _ := d.Get(starlark.String(key))
	if !ok {
		return 0
	}
	i, err := starlark.AsInt32(v)
	if err != nil {
		return 0
	}
	return int(i)
}

func dictBool(d *starlark.Dict, key string) bool {
	v, ok, _ := d.Get(starlark.String(key))
	if !ok {
		return false
	}
	b, ok := v.(starlark.Bool)
	if !ok {
		return false
	}
	return bool(b)
}

func dictBoolPtr(d *starlark.Dict, key string) *bool {
	v, ok, _ := d.Get(starlark.String(key))
	if !ok {
		return nil
	}
	b, out := v.(starlark.Bool)
	if !out {
		return nil
	}
	val := bool(b)
	return &val
}

// expandTemplate substitutes {item} and {prev} placeholders in a pipeline stage
// prompt with the item and that item's previous-stage result.
func expandTemplate(t, item, prev string) string {
	t = strings.ReplaceAll(t, "{item}", item)
	return strings.ReplaceAll(t, "{prev}", prev)
}

// stringList lets Starlark unpack a list of strings for an agent() keyword.
type stringList []string

func (s *stringList) Unpack(v starlark.Value) error {
	l, ok := v.(*starlark.List)
	if !ok {
		return fmt.Errorf("expected a list of strings, got %s", v.Type())
	}
	out := make([]string, 0, l.Len())
	for i := 0; i < l.Len(); i++ {
		sval, ok := starlark.AsString(l.Index(i))
		if !ok {
			return fmt.Errorf("expected a list of strings, got %s at index %d", l.Index(i).Type(), i)
		}
		out = append(out, sval)
	}
	*s = out
	return nil
}

// optionalBool tracks whether a bool keyword was supplied at all, so false and
// "unset" stay distinct (mirrors the task tool's tri-state cache_from_parent).
type optionalBool struct {
	v *bool
}

func (o *optionalBool) Unpack(v starlark.Value) error {
	b, ok := v.(starlark.Bool)
	if !ok {
		return fmt.Errorf("expected bool, got %s", v.Type())
	}
	val := bool(b)
	o.v = &val
	return nil
}

// goToStarlark converts a JSON-shaped Go value into a Starlark value for the
// `args` global.
func goToStarlark(v any) starlark.Value {
	switch v := v.(type) {
	case nil:
		return starlark.None
	case bool:
		return starlark.Bool(v)
	case string:
		return starlark.String(v)
	case int:
		return starlark.MakeInt(v)
	case int64:
		return starlark.MakeInt64(v)
	case float64:
		return starlark.Float(v)
	case []any:
		out := make([]starlark.Value, len(v))
		for i, e := range v {
			out[i] = goToStarlark(e)
		}
		return starlark.NewList(out)
	case map[string]any:
		d := starlark.NewDict(len(v))
		for k, e := range v {
			_ = d.SetKey(starlark.String(k), goToStarlark(e))
		}
		return d
	default:
		return starlark.String(fmt.Sprintf("%v", v))
	}
}

// starlarkToGo converts a Starlark value into a JSON-shaped Go value for the
// workflow result.
func starlarkToGo(v starlark.Value) any {
	switch v := v.(type) {
	case starlark.NoneType:
		return nil
	case starlark.Bool:
		return bool(v)
	case starlark.Int:
		if i, ok := v.Int64(); ok {
			return i
		}
		return v.String()
	case starlark.Float:
		return float64(v)
	case starlark.String:
		return string(v)
	case *starlark.List:
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = starlarkToGo(v.Index(i))
		}
		return out
	case *starlark.Tuple:
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = starlarkToGo(v.Index(i))
		}
		return out
	case *starlark.Set:
		out := make([]any, 0, v.Len())
		it := v.Iterate()
		defer it.Done()
		var elem starlark.Value
		for it.Next(&elem) {
			out = append(out, starlarkToGo(elem))
		}
		return out
	case *starlark.Dict:
		out := map[string]any{}
		it := v.Iterate()
		defer it.Done()
		var key starlark.Value
		for it.Next(&key) {
			k, ok := starlark.AsString(key)
			if !ok {
				continue
			}
			val, _, _ := v.Get(key)
			out[k] = starlarkToGo(val)
		}
		return out
	default:
		return v.String()
	}
}

package workflow

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type spawnRecorder struct {
	mu      sync.Mutex
	calls   []AgentSpec
	results map[string]AgentResult
}

func newRecorder() *spawnRecorder {
	return &spawnRecorder{results: map[string]AgentResult{}}
}

// spawn records the call and returns a deterministic result. A spec with
// Model "explode" fails; "block" waits for the context to be cancelled (used to
// hold a run open until a test cancels it).
func (r *spawnRecorder) spawn(ctx context.Context, spec AgentSpec) (AgentResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, spec)
	key := spec.Prompt + "|" + spec.Description + "|" + spec.Model
	r.mu.Unlock()

	if spec.Model == "explode" {
		return AgentResult{}, errors.New("boom")
	}
	if spec.Model == "block" {
		<-ctx.Done()
		return AgentResult{}, ctx.Err()
	}
	// Deterministic, prompt-derived results: parallel/pipeline assertions are
	// order-independent even though spells run concurrently.
	if spec.ContinueFrom != "" {
		return AgentResult{Text: "continued-from-" + spec.ContinueFrom, Ref: "sa:" + spec.Prompt}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if res, ok := r.results[key]; ok {
		return res, nil
	}
	res := AgentResult{Text: "out:" + spec.Prompt, Ref: "sa:" + spec.Prompt}
	r.results[key] = res
	return res, nil
}

func (r *spawnRecorder) prompts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	for i, c := range r.calls {
		out[i] = c.Prompt
	}
	return out
}

func (r *spawnRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func runScript(t *testing.T, body string, inArgs any, rec *spawnRecorder) Result {
	t.Helper()
	return NewEngine(Options{Spawn: rec.spawn}).Run(context.Background(), body, inArgs)
}

func TestResultGlobal(t *testing.T) {
	rec := newRecorder()
	res := runScript(t, `result = args["x"] + "!"`, map[string]any{"x": "hi"}, rec)
	if res.StopReason != StopCompleted || res.Error != "" {
		t.Fatalf("failed: StopReason=%q err=%q", res.StopReason, res.Error)
	}
	if got := res.Value; got != "hi!" {
		t.Errorf("Value = %v, want %q", got, "hi!")
	}
}

func TestMainReturn(t *testing.T) {
	rec := newRecorder()
	res := runScript(t, "def main():\n    return {\"n\": 2}\n", nil, rec)
	if res.StopReason != StopCompleted || res.Error != "" {
		t.Fatalf("failed: %q / %q", res.StopReason, res.Error)
	}
	m, ok := res.Value.(map[string]any)
	if !ok || m["n"] != int64(2) {
		t.Errorf("Value = %#v, want map with n=2", res.Value)
	}
}

func TestAgentDefaults(t *testing.T) {
	rec := newRecorder()
	res := runScript(t, "result = agent(\"review this\")", nil, rec)
	if res.StopReason != StopCompleted || res.Error != "" {
		t.Fatalf("failed: %q / %q", res.StopReason, res.Error)
	}
	if got, _ := res.Value.(string); got == "" {
		t.Error("agent() returned empty text")
	}
	spec := rec.calls[0]
	if spec.Prompt != "review this" || spec.Description != "" || len(spec.Tools) != 0 ||
		spec.MaxSteps != 0 || spec.Model != "" || spec.Effort != "" ||
		spec.CacheFromParent != nil || spec.ContinueFrom != "" || spec.ForkFrom != "" {
		t.Errorf("default spec = %+v", spec)
	}
}

func TestAgentOptions(t *testing.T) {
	rec := newRecorder()
	res := runScript(t, `
r = agent(
    prompt = "deep dive",
    description = "docs reviewer",
    tools = ["read", "grep"],
    max_steps = 5,
    model = "deepseek-chat",
    effort = "high",
    cache_from_parent = False,
)
result = r
`, nil, rec)
	if res.StopReason != StopCompleted || res.Error != "" {
		t.Fatalf("failed: %q / %q", res.StopReason, res.Error)
	}
	spec := rec.calls[0]
	if len(spec.Tools) != 2 || spec.Tools[0] != "read" || spec.Tools[1] != "grep" {
		t.Errorf("tools = %v", spec.Tools)
	}
	if spec.Description != "docs reviewer" || spec.MaxSteps != 5 ||
		spec.Model != "deepseek-chat" || spec.Effort != "high" {
		t.Errorf("spec = %+v", spec)
	}
	if spec.CacheFromParent == nil || *spec.CacheFromParent {
		t.Errorf("cache_from_parent = %v, want false", spec.CacheFromParent)
	}

	// cache_from_parent=True is distinct from unset.
	rec2 := newRecorder()
	res = runScript(t, "agent(\"x\", cache_from_parent = True)\nresult = 1", nil, rec2)
	if res.StopReason != StopCompleted || res.Error != "" {
		t.Fatalf("failed: %q / %q", res.StopReason, res.Error)
	}
	if rec2.calls[0].CacheFromParent == nil || !*rec2.calls[0].CacheFromParent {
		t.Errorf("cache_from_parent=True = %v", rec2.calls[0].CacheFromParent)
	}
}

func TestAgentIncludeRefAndContinue(t *testing.T) {
	rec := newRecorder()
	res := runScript(t, `
r = agent("first", include_ref = True)
second = agent("second", continue_from = r.ref)
result = r.text + " -> " + second
`, nil, rec)
	if res.StopReason != StopCompleted || res.Error != "" {
		t.Fatalf("failed: %q / %q", res.StopReason, res.Error)
	}
	if rec.calls[1].ContinueFrom == "" {
		t.Error("continue_from was not set from the include_ref struct")
	}
	if got, _ := res.Value.(string); got == "" {
		t.Error("chained result empty")
	}
}

func TestParallelFailure(t *testing.T) {
	rec := newRecorder()
	res := runScript(t, `
outs = parallel([
    {"prompt": "task one", "model": "a"},
    "task two",
    {"prompt": "task three", "model": "explode"},
])
result = "|".join(outs)
`, nil, rec)
	if res.StopReason != StopError || !strings.Contains(res.Error, "boom") {
		t.Fatalf("StopReason=%q want error with boom: %q", res.StopReason, res.Error)
	}
	if res.AgentsStarted != 3 {
		t.Errorf("AgentsStarted = %d, want 3", res.AgentsStarted)
	}
}

func TestParallelOrder(t *testing.T) {
	rec := newRecorder()
	res := runScript(t, `
outs = parallel([{"prompt": "a"}, {"prompt": "b"}, {"prompt": "c"}])
result = outs
`, nil, rec)
	if res.StopReason != StopCompleted || res.Error != "" {
		t.Fatalf("failed: %q / %q", res.StopReason, res.Error)
	}
	// Results must map 1:1 onto input order.
	got := res.Value.([]any)
	if len(got) != 3 {
		t.Fatalf("parallel returned %d results, want 3", len(got))
	}
	for i, g := range got {
		if gotStr, _ := g.(string); gotStr != "out:"+[]string{"a", "b", "c"}[i] {
			t.Errorf("parallel()[%d] = %v, want out:%s", i, g, []string{"a", "b", "c"}[i])
		}
	}
	// Under concurrency the recorded order is racy; compare as a set.
	gotP := rec.prompts()
	sort.Strings(gotP)
	if !reflect.DeepEqual(gotP, []string{"a", "b", "c"}) {
		t.Errorf("parallel prompts = %v, want {a b c}", gotP)
	}
}

func TestPipeline(t *testing.T) {
	rec := newRecorder()
	res := runScript(t, `
outs = pipeline(
    stages = [
        {"prompt": "outline {item}"},
        {"prompt": "detail {item} after {prev}"},
    ],
    items = ["alpha", "beta"],
)
result = outs
`, nil, rec)
	if res.StopReason != StopCompleted || res.Error != "" {
		t.Fatalf("failed: %q / %q", res.StopReason, res.Error)
	}
	got := res.Value.([]any)
	if len(got) != 2 {
		t.Fatalf("pipeline returned %d results, want 2", len(got))
	}
	prompts := rec.prompts()
	if len(prompts) != 4 {
		t.Fatalf("expected 4 stage calls, got %d: %v", len(prompts), prompts)
	}
	stage1, stage2 := prompts[:2], prompts[2:]
	sort.Strings(stage1)
	sort.Strings(stage2)
	if !reflect.DeepEqual(stage1, []string{"outline alpha", "outline beta"}) {
		t.Errorf("stage-1 prompts = %v", stage1)
	}
	// {prev} is each item's previous-stage result text (prompt-derived, so
	// deterministic): "out:outline <item>".
	wantStage2 := []string{
		"detail alpha after out:outline alpha",
		"detail beta after out:outline beta",
	}
	sort.Strings(wantStage2)
	if !reflect.DeepEqual(stage2, wantStage2) {
		t.Errorf("stage-2 prompts = %v, want %v", stage2, wantStage2)
	}
	if len(got) != 2 {
		t.Fatalf("pipeline returned %d results, want 2", len(got))
	}
}

func TestPhaseAndLog(t *testing.T) {
	rec := newRecorder()
	var phases []string
	var logs []string
	eng := NewEngine(Options{
		Spawn:   rec.spawn,
		OnPhase: func(title, detail string) { phases = append(phases, title+"|"+detail) },
		OnLog:   func(m string) { logs = append(logs, m) },
	})
	res := eng.Run(context.Background(), `
phase("collecting", "leftovers")
log("hello user")
result = 1
`, nil)
	if res.StopReason != StopCompleted || res.Error != "" {
		t.Fatalf("failed: %q / %q", res.StopReason, res.Error)
	}
	if len(phases) != 1 || phases[0] != "collecting|leftovers" {
		t.Errorf("phases = %v", phases)
	}
	if len(logs) != 1 || logs[0] != "hello user" {
		t.Errorf("logs = %v", logs)
	}
}

func TestStepLimit(t *testing.T) {
	rec := newRecorder()
	eng := NewEngine(Options{Spawn: rec.spawn, MaxExecutionSteps: 256})
	// The full dialect is on for workflows; the step budget halts the loop. A
	// function body is used so the counter is a local (globals can't reassign).
	res := eng.Run(context.Background(), "def spin():\n    n = 0\n    while True:\n        n += 1\n    return n\nresult = spin()", nil)
	if res.StopReason != StopError || !strings.Contains(res.Error, "too many") {
		t.Errorf("StopReason=%q err=%q, want step-limit error", res.StopReason, res.Error)
	}
}

func TestCancellation(t *testing.T) {
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		done <- NewEngine(Options{Spawn: rec.spawn}).Run(ctx, "agent(\"hang\", model = \"block\")\nresult = 1", nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for rec.count() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	res := <-done
	if res.StopReason != StopCancelled {
		t.Errorf("StopReason = %q, want cancelled (err=%q)", res.StopReason, res.Error)
	}
}

func TestScriptError(t *testing.T) {
	rec := newRecorder()
	res := runScript(t, "result = undefined_var", nil, rec)
	if res.StopReason != StopError || !strings.Contains(res.Error, "undefined_var") {
		t.Errorf("StopReason=%q err=%q, want starlark error surfaced", res.StopReason, res.Error)
	}
}

func TestEmptyPromptRejected(t *testing.T) {
	rec := newRecorder()
	res := runScript(t, "agent(\"   \")\nresult = 1", nil, rec)
	if res.StopReason != StopError || !strings.Contains(res.Error, "non-empty prompt") {
		t.Errorf("want empty-prompt error, got %+v", res)
	}
}

func TestStarlarkValuesResult(t *testing.T) {
	rec := newRecorder()
	res := runScript(t, `result = [1, 2.5, "x", True, None, {"k": 1}]`, nil, rec)
	if res.StopReason != StopCompleted || res.Error != "" {
		t.Fatalf("failed: %q / %q", res.StopReason, res.Error)
	}
	v := res.Value.([]any)
	if len(v) != 6 {
		t.Fatalf("Value = %#v, want 6 elements", res.Value)
	}
	if v[0] != int64(1) || v[1] != 2.5 || v[2] != "x" || v[3] != true || v[4] != nil {
		t.Errorf("Value = %#v", res.Value)
	}
	m, ok := v[5].(map[string]any)
	if !ok || m["k"] != int64(1) {
		t.Errorf("Value[5] = %#v, want dict", v[5])
	}
}

func TestNoEngineSpawn(t *testing.T) {
	eng := NewEngine(Options{})
	res := eng.Run(context.Background(), "result = 1", nil)
	if res.StopReason != StopError || !strings.Contains(res.Error, "spawn hook") {
		t.Errorf("want engine spawn-hook error, got %+v", res)
	}
}

func boolPtr(b bool) *bool { return &b }

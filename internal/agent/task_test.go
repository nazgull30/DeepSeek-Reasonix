package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func testTaskContext() context.Context {
	return WithParentSession(context.Background(), "parent-session")
}

// TestTaskToolReturnsSubAgentFinalAnswer runs a task against a mock provider
// that emits a single text turn, and verifies the tool returns that text with a
// transcript reference — sub-agent intermediate state isn't supposed to leak.
func TestTaskToolReturnsSubAgentFinalAnswer(t *testing.T) {
	sub := &mockProvider{name: "sub", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "found 3 callers of Foo"},
		{Type: provider.ChunkDone},
	}}
	parentReg := tool.NewRegistry()
	task := newTestTaskTool(t, sub, parentReg, "test-sys-prompt", "", "", nil)

	out, err := task.Execute(testTaskContext(), []byte(`{"prompt":"find callers of Foo"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_ = subagentRefFromOutput(t, out)
	if !strings.Contains(out, "found 3 callers of Foo") {
		t.Errorf("got %q, want sub-agent final answer", out)
	}

	// The sub-agent must have received the prompt as its user message and
	// the configured system prompt at the top — proving the session was
	// fresh, not the parent's.
	if sys := sub.lastReq.Messages[0]; sys.Role != provider.RoleSystem || sys.Content != "test-sys-prompt" {
		t.Errorf("first message = %+v, want system 'test-sys-prompt'", sys)
	}
	if got := lastUser(sub.lastReq); got != "find callers of Foo" {
		t.Errorf("sub-agent user = %q, want the prompt verbatim", got)
	}
}

func TestTaskToolInheritsReasoningLanguageFromContext(t *testing.T) {
	sub := &mockProvider{name: "sub", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "done"},
		{Type: provider.ChunkDone},
	}}
	task := newTestTaskTool(t, sub, tool.NewRegistry(), "sys", "", "", nil)

	ctx := WithReasoningLanguagePreference(testTaskContext(), "zh")
	if _, err := task.Execute(ctx, []byte(`{"prompt":"inspect auth"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := lastUser(sub.lastReq)
	if !strings.HasPrefix(got, "<reasoning-language>") || !strings.Contains(got, "Simplified Chinese") || !strings.HasSuffix(got, "inspect auth") {
		t.Fatalf("sub-agent user = %q, want reasoning-language-prefixed prompt", got)
	}
}

// TestTaskToolFiltersTools verifies the whitelist behaviour: when the caller
// names a subset of tools, the sub-agent's registry contains exactly that set
// with subagent/skill meta-tools stripped to prevent recursive delegation.
func TestTaskToolFiltersTools(t *testing.T) {
	sub := &mockProvider{name: "sub", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "ok"},
		{Type: provider.ChunkDone},
	}}
	parentReg := tool.NewRegistry()
	parentReg.Add(fakeTool{name: "read_file", readOnly: true})
	parentReg.Add(fakeTool{name: "write_file", readOnly: false})
	parentReg.Add(fakeTool{name: "bash", readOnly: false})
	task := newTestTaskTool(t, sub, parentReg, "sys", "", "", nil)
	parentReg.Add(task) // simulate the wiring in cli.setup
	parentReg.Add(fakeTool{name: "run_skill", readOnly: false})
	parentReg.Add(fakeTool{name: "research", readOnly: false})

	args := []byte(`{"prompt":"x","tools":["read_file","task","write_file","run_skill","research"]}`)
	if _, err := task.Execute(testTaskContext(), args); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// The sub-agent's tool schemas should reflect the whitelist minus meta-tools.
	got := map[string]bool{}
	for _, s := range sub.lastReq.Tools {
		got[s.Name] = true
	}
	if !got["read_file"] || !got["write_file"] || got["task"] || got["run_skill"] || got["research"] || got["bash"] {
		t.Errorf("sub-agent tools = %v, want {read_file, write_file} (meta-tools stripped, bash not requested)", got)
	}
}

// TestTaskToolDefaultsToParentToolsWithoutMetaTools covers the no-whitelist
// path: the sub-agent inherits parent tools except subagent/skill meta-tools.
func TestTaskToolDefaultsToParentToolsWithoutMetaTools(t *testing.T) {
	sub := &mockProvider{name: "sub", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "ok"},
		{Type: provider.ChunkDone},
	}}
	parentReg := tool.NewRegistry()
	parentReg.Add(fakeTool{name: "read_file", readOnly: true})
	parentReg.Add(fakeTool{name: "grep", readOnly: true})
	task := newTestTaskTool(t, sub, parentReg, "sys", "", "", nil)
	parentReg.Add(task)
	parentReg.Add(fakeTool{name: "run_skill", readOnly: false})
	parentReg.Add(fakeTool{name: "explore", readOnly: false})
	parentReg.Add(fakeTool{name: "research", readOnly: false})
	parentReg.Add(fakeTool{name: "review", readOnly: false})
	parentReg.Add(fakeTool{name: "security_review", readOnly: false})
	parentReg.Add(fakeTool{name: "remember", readOnly: false})

	if _, err := task.Execute(testTaskContext(), []byte(`{"prompt":"x"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := map[string]bool{}
	for _, s := range sub.lastReq.Tools {
		got[s.Name] = true
	}
	if !got["read_file"] || !got["grep"] || !got["remember"] ||
		got["task"] || got["run_skill"] || got["explore"] || got["research"] || got["review"] || got["security_review"] {
		t.Errorf("default sub-agent tools = %v, want normal tools inherited and meta-tools stripped", got)
	}
}

func TestTaskToolUsesConfiguredProfileForExecution(t *testing.T) {
	parent := &mockProvider{name: "parent", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "parent answer"},
		{Type: provider.ChunkDone},
	}}
	resolved := &mockProvider{name: "resolved", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "resolved answer"},
		{Type: provider.ChunkDone},
	}}
	var gotModel, gotEffort string
	resolve := func(model, effort string) (provider.Provider, *provider.Pricing, int, error) {
		gotModel, gotEffort = model, effort
		return resolved, nil, 0, nil
	}
	task := newTestTaskTool(t, parent, tool.NewRegistry(), "sys", "deepseek-pro", "max", resolve)

	out, err := task.Execute(testTaskContext(), []byte(`{"prompt":"x"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "resolved answer") {
		t.Fatalf("sub-agent did not use resolved provider, got %q", out)
	}
	if gotModel != "deepseek-pro" || gotEffort != "max" {
		t.Fatalf("resolved profile = %q/%q, want deepseek-pro/max", gotModel, gotEffort)
	}
}

func TestTaskToolReturnsProfileResolutionErrors(t *testing.T) {
	parent := &mockProvider{name: "parent", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "parent answer"},
		{Type: provider.ChunkDone},
	}}
	resolve := func(string, string) (provider.Provider, *provider.Pricing, int, error) {
		return nil, nil, 0, errors.New("bad effort")
	}
	task := newTestTaskTool(t, parent, tool.NewRegistry(), "sys", "", "", resolve)

	_, err := task.Execute(testTaskContext(), []byte(`{"prompt":"x","effort":"turbo"}`))
	if err == nil || !strings.Contains(err.Error(), "bad effort") {
		t.Fatalf("Execute error = %v, want profile resolution error", err)
	}
}

func TestTaskToolRequiresTranscriptStore(t *testing.T) {
	sub := &mockProvider{name: "sub", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "answer"},
		{Type: provider.ChunkDone},
	}}
	task := NewTaskTool(sub, nil, tool.NewRegistry(), 20, 0, 0, 0, 0, 0, 0.0, "", "sys", nil, 0, "", "", nil, nil)

	_, err := task.Execute(testTaskContext(), []byte(`{"prompt":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "transcript store is required") {
		t.Fatalf("Execute error = %v, want transcript store requirement", err)
	}
}

// TestTaskToolRunsEphemerallyWithoutParentSession mirrors headless `reasonix run`:
// the store is wired but the context carries no parent session, so the sub-agent
// must run without persistence and return its plain answer (no transcript ref).
func TestTaskToolRunsEphemerallyWithoutParentSession(t *testing.T) {
	sub := &mockProvider{name: "sub", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "headless answer"},
		{Type: provider.ChunkDone},
	}}
	task := newTestTaskTool(t, sub, tool.NewRegistry(), "sys", "", "", nil)

	out, err := task.Execute(context.Background(), []byte(`{"prompt":"x"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "headless answer") {
		t.Fatalf("got %q, want sub-agent final answer", out)
	}
	if strings.Contains(out, "Subagent reference") {
		t.Fatalf("ephemeral run should not emit a transcript reference: %q", out)
	}
}

func TestTaskToolRejectsContinuationWithoutParentSession(t *testing.T) {
	sub := &mockProvider{name: "sub", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "answer"},
		{Type: provider.ChunkDone},
	}}
	task := newTestTaskTool(t, sub, tool.NewRegistry(), "sys", "", "", nil)

	_, err := task.Execute(context.Background(), []byte(`{"prompt":"x","continue_from":"sa_whatever"}`))
	if err == nil || !strings.Contains(err.Error(), "persisted session") {
		t.Fatalf("Execute error = %v, want persisted-session requirement", err)
	}
}

func TestTaskToolPersistsAndContinuesTranscript(t *testing.T) {
	sub := &mockProvider{name: "sub", streams: [][]provider.Chunk{
		{
			{Type: provider.ChunkText, Text: "first answer"},
			{Type: provider.ChunkDone},
		},
		{
			{Type: provider.ChunkText, Text: "second answer"},
			{Type: provider.ChunkDone},
		},
	}}
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true})
	store := NewSubagentStore(t.TempDir())
	task := newTestTaskTool(t, sub, reg, "sys", "", "", nil).
		WithTranscripts(store, t.TempDir(), "base-model", "base-effort")

	first, err := task.Execute(testTaskContext(), []byte(`{"prompt":"first task"}`))
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	ref := subagentRefFromOutput(t, first)
	meta, err := store.LoadMeta(ref)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.ParentSession != "parent-session" {
		t.Fatalf("parent session = %q, want parent-session", meta.ParentSession)
	}
	if !strings.Contains(first, "first answer") {
		t.Fatalf("first output = %q, want answer", first)
	}

	second, err := task.Execute(testTaskContext(), []byte(`{"prompt":"second task","continue_from":"`+ref+`"}`))
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if !strings.Contains(second, "second answer") {
		t.Fatalf("second output = %q, want answer", second)
	}
	if len(sub.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(sub.requests))
	}
	msgs := sub.requests[1].Messages
	if len(msgs) < 4 {
		t.Fatalf("continued request messages = %+v, want prior transcript plus new task", msgs)
	}
	if msgs[1].Content != "first task" || msgs[2].Content != "first answer" || lastUser(sub.requests[1]) != "second task" {
		t.Fatalf("continued request messages = %+v, want first task/answer then second task", msgs)
	}
}

func TestTaskToolFailedForegroundContinuationPersistsAndRejectsReuse(t *testing.T) {
	sub := &mockProvider{name: "sub", streams: [][]provider.Chunk{
		{
			{Type: provider.ChunkText, Text: "first answer"},
			{Type: provider.ChunkDone},
		},
		{
			{Type: provider.ChunkError, Err: errors.New("provider failed")},
		},
	}}
	store := NewSubagentStore(t.TempDir())
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true})
	task := NewTaskTool(sub, nil, reg, 20, 0, 0, 0, 0, 0, 0.0, "", "sys", nil, 0, "", "", nil, nil).
		WithTranscripts(store, t.TempDir(), "base-model", "base-effort")

	first, err := task.Execute(testTaskContext(), []byte(`{"prompt":"first task"}`))
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	ref := subagentRefFromOutput(t, first)

	_, err = task.Execute(testTaskContext(), []byte(`{"prompt":"second task","continue_from":"`+ref+`"}`))
	if err == nil || !strings.Contains(err.Error(), "provider failed") {
		t.Fatalf("second Execute error = %v, want provider failure", err)
	}
	meta, err := store.LoadMeta(ref)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.Status != SubagentFailed {
		t.Fatalf("status = %q, want failed", meta.Status)
	}
	loaded, err := LoadSession(store.sessionPath(ref))
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	msgs := loaded.Snapshot()
	if len(msgs) != 4 || msgs[1].Content != "first task" || msgs[2].Content != "first answer" || msgs[3].Content != "second task" {
		t.Fatalf("failed continuation transcript = %+v, want first task/answer plus second task", msgs)
	}
	if _, err := task.Execute(testTaskContext(), []byte(`{"prompt":"third task","continue_from":"`+ref+`"}`)); err == nil || !strings.Contains(err.Error(), "failed and cannot be continued") {
		t.Fatalf("reuse error = %v, want failed ref rejection", err)
	}
}

func TestTaskToolBackgroundPanicPersistsFailedMetadata(t *testing.T) {
	sub := panicProvider{name: "panic-sub"}
	store := NewSubagentStore(t.TempDir())
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true})
	task := NewTaskTool(sub, nil, reg, 20, 0, 0, 0, 0, 0, 0.0, "", "sys", nil, 0, "", "", nil, nil).
		WithTranscripts(store, t.TempDir(), "base-model", "base-effort")

	jm := jobs.NewManager(event.Discard)
	defer jm.Close()
	ctx := testTaskContext()
	ctx = jobs.WithSession(ctx, "parent-session")
	ctx = jobs.WithManager(ctx, jm)
	out, err := task.Execute(ctx, []byte(`{"prompt":"panic task","run_in_background":true}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ref := subagentRefFromOutput(t, out)
	jobID := extractJobID(out)
	if jobID == "" {
		t.Fatalf("no background job id in output:\n%s", out)
	}
	res := jm.WaitForSession(context.Background(), "parent-session", []string{jobID}, 5)
	if len(res) != 1 || res[0].Status != jobs.Failed {
		t.Fatalf("background job result = %+v, want failed", res)
	}
	if !strings.Contains(res[0].Output, "Subagent reference (failed): "+ref) {
		t.Fatalf("job output = %q, want failed subagent ref %s", res[0].Output, ref)
	}
	meta, err := store.LoadMeta(ref)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.Status != SubagentFailed {
		t.Fatalf("status = %q, want failed", meta.Status)
	}
	if _, err := task.Execute(testTaskContext(), []byte(`{"prompt":"again","continue_from":"`+ref+`"}`)); err == nil || !strings.Contains(err.Error(), "failed and cannot be continued") {
		t.Fatalf("reuse error = %v, want failed continuation rejection", err)
	}
}

func TestTaskToolRejectsMismatchedContinuationProfile(t *testing.T) {
	sub := &mockProvider{name: "sub", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "answer"},
		{Type: provider.ChunkDone},
	}}
	task := newTestTaskTool(t, sub, tool.NewRegistry(), "sys", "", "", nil).
		WithTranscripts(NewSubagentStore(t.TempDir()), t.TempDir(), "base-model", "")

	out, err := task.Execute(testTaskContext(), []byte(`{"prompt":"first task"}`))
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	ref := subagentRefFromOutput(t, out)
	_, err = task.Execute(testTaskContext(), []byte(`{"prompt":"second task","continue_from":"`+ref+`","model":"other-model"}`))
	if err == nil || !strings.Contains(err.Error(), "model/effort") {
		t.Fatalf("mismatched model error = %v, want compatibility failure", err)
	}
}

func subagentRefFromOutput(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Subagent reference: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Subagent reference: "))
		}
	}
	t.Fatalf("no subagent reference in output:\n%s", out)
	return ""
}

func TestSubSinkForwardsUsageToParent(t *testing.T) {
	var got []event.Event
	parent := event.FuncSink(func(e event.Event) {
		got = append(got, e)
	})
	subSinkFor("task_1", parent).Emit(event.Event{
		Kind:        event.Usage,
		Usage:       &provider.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		UsageSource: event.UsageSourceSubagent,
	})
	if len(got) != 1 || got[0].Usage == nil || got[0].UsageSource != event.UsageSourceSubagent {
		t.Fatalf("forwarded events = %+v, want subagent usage", got)
	}
}

func TestSubSinkTagsUsageWithParentID(t *testing.T) {
	var got event.Event
	parent := event.FuncSink(func(e event.Event) {
		got = e
	})
	subSinkFor("task_1", parent).Emit(event.Event{
		Kind:        event.Usage,
		Usage:       &provider.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		UsageSource: event.UsageSourceSubagent,
	})
	if got.ParentID != "task_1" {
		t.Fatalf("forwarded usage ParentID = %q, want task_1", got.ParentID)
	}
}

func TestWithNestedSinkForwardsUsageToParent(t *testing.T) {
	var got event.Event
	parent := event.FuncSink(func(e event.Event) {
		got = e
	})
	ctx := WithNestedSink(context.Background(), "subtask-1", parent)
	subSink(ctx).Emit(event.Event{
		Kind:        event.Usage,
		Usage:       &provider.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		UsageSource: event.UsageSourceSubagent,
	})
	if got.ParentID != "subtask-1" {
		t.Fatalf("forwarded usage ParentID = %q, want subtask-1", got.ParentID)
	}
	if got.Usage == nil || got.UsageSource != event.UsageSourceSubagent {
		t.Fatalf("forwarded event = %+v, want subagent usage", got)
	}
}

// TestForkCacheSharing verifies the cache_from_parent fork subagent flow:
//   - The forked subagent inherits the parent's stable conversation prefix
//     (all complete rounds, i.e. messages before the last in-flight round)
//   - The fork guard tag (ForkPlaceholderTag) is injected as the first user
//     message so IsForkChild can detect recursive forking
//   - Multiple fork children share the exact same message prefix (inherited
//     parent messages + fork guard tag) up to the task prompt, enabling
//     server-side prompt cache sharing
//   - Missing parent messages wiring produces a clear error
//   - Recursive forking (fork inside a fork) is rejected
func TestForkCacheSharing(t *testing.T) {
	// Parent conversation: system, two Q&A turns, then a complete
	// assistant+tools round (assistant with ToolCalls → tool result →
	// assistant response). BuildForkSession inherits up to the boundary
	// determined by lastCompleteRound — the assistant text after the tool
	// result is excluded because the fork child writes its own response.
	parentMsgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a helpful assistant."},
		{Role: provider.RoleUser, Content: "What does the codebase do?"},
		{Role: provider.RoleAssistant, Content: "It's a Go project for coding agents."},
		{Role: provider.RoleUser, Content: "Find all callers of Foo"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "grep", Arguments: `{"pattern":"Foo"}`},
		}, Content: "Searching..."},
		{Role: provider.RoleTool, ToolCallID: "c1", Content: "main.go:42\nutil.go:15"},
		{Role: provider.RoleAssistant, Content: "Found callers in main.go and util.go."},
	}
	// Expected inherited prefix: parentMsgs up to lastCompleteRound (index 6
	// = boundary) = indices 0-5. Then fork guard, then the task prompt.
	inheritBoundary := 6

	rs := NewContentReplacementState(t.TempDir())
	sub := &mockProvider{name: "sub", streams: [][]provider.Chunk{
		{{Type: provider.ChunkText, Text: "analysis complete"}, {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "refactoring done"}, {Type: provider.ChunkDone}},
	}}

	reg := tool.NewRegistry()
	task := newTestTaskTool(t, sub, reg, "ignored-sys", "", "", nil).
		WithParentMessages(func() []provider.Message { return parentMsgs }).
		WithParentResultState(func() *ContentReplacementState { return rs })

	// --- First fork child ---
	out1, err := task.Execute(testTaskContext(), []byte(`{"prompt":"analyze callers","cache_from_parent":true}`))
	if err != nil {
		t.Fatalf("first fork: %v", err)
	}
	if !strings.Contains(out1, "analysis complete") {
		t.Errorf("first fork answer = %q, want 'analysis complete'", out1)
	}

	if len(sub.requests) < 1 {
		t.Fatal("no provider requests recorded for first fork")
	}
	req1 := sub.requests[0]

	// Verify inherited parent messages at the start of the request
	for i := 0; i < inheritBoundary; i++ {
		got := req1.Messages[i]
		if got.Role != parentMsgs[i].Role || got.Content != parentMsgs[i].Content || len(got.ToolCalls) != len(parentMsgs[i].ToolCalls) {
			t.Errorf("message[%d] mismatch:\n  got:  %+v\n  want: %+v", i, got, parentMsgs[i])
		}
	}

	// Verify fork guard tag at the boundary position
	if req1.Messages[inheritBoundary].Role != provider.RoleUser || req1.Messages[inheritBoundary].Content != ForkPlaceholderTag {
		t.Errorf("message[%d] expected fork guard (%q), got %+v", inheritBoundary, ForkPlaceholderTag, req1.Messages[inheritBoundary])
	}

	// Verify task prompt follows the fork guard
	promptIdx := inheritBoundary + 1
	if req1.Messages[promptIdx].Role != provider.RoleUser || req1.Messages[promptIdx].Content != "analyze callers" {
		t.Errorf("message[%d] expected prompt 'analyze callers', got %+v", promptIdx, req1.Messages[promptIdx])
	}

	// --- Second fork child (different prompt, same prefix) ---
	out2, err := task.Execute(testTaskContext(), []byte(`{"prompt":"refactor callers","cache_from_parent":true}`))
	if err != nil {
		t.Fatalf("second fork: %v", err)
	}
	if !strings.Contains(out2, "refactoring done") {
		t.Errorf("second fork answer = %q, want 'refactoring done'", out2)
	}

	if len(sub.requests) < 2 {
		t.Fatal("no provider requests recorded for second fork")
	}
	req2 := sub.requests[1]

	// Both forks must have identical prefix (inherited parent messages + fork guard)
	for i := 0; i <= inheritBoundary; i++ {
		m1, m2 := req1.Messages[i], req2.Messages[i]
		if m1.Role != m2.Role || m1.Content != m2.Content || len(m1.ToolCalls) != len(m2.ToolCalls) {
			t.Errorf("prefix mismatch at index %d:\n  req1: %+v\n  req2: %+v", i, m1, m2)
		}
		for j := range m1.ToolCalls {
			tc1, tc2 := m1.ToolCalls[j], m2.ToolCalls[j]
			if tc1.ID != tc2.ID || tc1.Name != tc2.Name || tc1.Arguments != tc2.Arguments {
				t.Errorf("tool call mismatch at message[%d].ToolCalls[%d]:\n  req1: %+v\n  req2: %+v",
					i, j, tc1, tc2)
			}
		}
	}

	// Second fork's prompt differs
	if req2.Messages[promptIdx].Content != "refactor callers" {
		t.Errorf("message[%d] expected prompt 'refactor callers', got %+v", promptIdx, req2.Messages[promptIdx])
	}

	// --- Error: cache_from_parent without WithParentMessages (explicit flag, missing wiring) ---
	taskNoParent := newTestTaskTool(t, sub, reg, "sys", "", "", nil)
	_, err = taskNoParent.Execute(testTaskContext(), []byte(`{"prompt":"test","cache_from_parent":true}`))
	if err == nil || !strings.Contains(err.Error(), "not available in this context") {
		t.Errorf("expected 'not available' error, got %v", err)
	}

	// --- Recursive fork falls back gracefully ---
	// When parent messages already contain the fork guard tag, cache_from_parent
	// silently skips the fork and runs a normal session instead of erroring.
	sub.streams = append(sub.streams, []provider.Chunk{
		{Type: provider.ChunkText, Text: "fallback result"}, {Type: provider.ChunkDone},
	})
	recursiveMsgs := make([]provider.Message, len(parentMsgs), len(parentMsgs)+1)
	copy(recursiveMsgs, parentMsgs)
	recursiveMsgs = append(recursiveMsgs, provider.Message{Role: provider.RoleUser, Content: ForkPlaceholderTag})
	taskRecurse := newTestTaskTool(t, sub, reg, "sys", "", "", nil).
		WithParentMessages(func() []provider.Message { return recursiveMsgs })
	out3, err := taskRecurse.Execute(testTaskContext(), []byte(`{"prompt":"test","cache_from_parent":true}`))
	if err != nil {
		t.Errorf("recursive fork should fall back, got error: %v", err)
	}
	if !strings.Contains(out3, "fallback result") {
		t.Errorf("recursive fallback result = %q, want 'fallback result'", out3)
	}
}

func TestTaskToolCarriesRecentKeepIntoSubsessions(t *testing.T) {
	task := NewTaskTool(&mockProvider{name: "sub"}, nil, tool.NewRegistry(), 20, 0, 7, 0, 0, 0, 0.0, "", "sys", nil, 0, "", "", nil, nil)
	if task.recentKeep != 7 {
		t.Fatalf("recentKeep = %d, want 7", task.recentKeep)
	}
}

// TestCacheFromParentExplicitFalseRunsFresh verifies that an explicit
// cache_from_parent=false overrides the auto-fork and runs a fresh isolated
// sub-agent with no inherited parent context, even when parent messages are
// wired — the semantics the /subtask false:/no-prefix form relies on.
func TestCacheFromParentExplicitFalseRunsFresh(t *testing.T) {
	parentMsgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a helpful assistant."},
		{Role: provider.RoleUser, Content: "What does the codebase do?"},
		{Role: provider.RoleAssistant, Content: "It's a Go project for coding agents."},
	}
	sub := &mockProvider{name: "sub", streams: [][]provider.Chunk{
		{{Type: provider.ChunkText, Text: "fresh result"}, {Type: provider.ChunkDone}},
	}}
	reg := tool.NewRegistry()
	task := newTestTaskTool(t, sub, reg, "test-sys-prompt", "", "", nil).
		WithParentMessages(func() []provider.Message { return parentMsgs })

	out, err := task.Execute(testTaskContext(), []byte(`{"prompt":"do the thing","cache_from_parent":false}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "fresh result") {
		t.Errorf("answer = %q, want 'fresh result'", out)
	}
	// A fresh sub-agent starts with its own system prompt, not the parent's
	// inherited conversation — proving cache_from_parent=false skipped the fork.
	if sys := sub.lastReq.Messages[0]; sys.Role != provider.RoleSystem || sys.Content != "test-sys-prompt" {
		t.Errorf("first message = %+v, want fresh system 'test-sys-prompt'", sys)
	}
	if got := lastUser(sub.lastReq); got != "do the thing" {
		t.Errorf("user message = %q, want the prompt verbatim", got)
	}
}

func newTestTaskTool(t *testing.T, prov provider.Provider, reg *tool.Registry, sysPrompt, subagentModel, subagentEffort string, resolve func(string, string) (provider.Provider, *provider.Pricing, int, error)) *TaskTool {
	t.Helper()
	return NewTaskTool(prov, nil, reg, 20, 0, 0, 0, 0, 0, 0.0, "", sysPrompt, nil, 0, subagentModel, subagentEffort, resolve, nil).
		WithTranscripts(NewSubagentStore(t.TempDir()), t.TempDir(), "base-model", "base-effort")
}

type panicProvider struct{ name string }

func (p panicProvider) Name() string { return p.name }

func (p panicProvider) Stream(context.Context, provider.Request) (<-chan provider.Chunk, error) {
	panic("subagent boom")
}

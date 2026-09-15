package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/tool"
	"reasonix/internal/workflow"
)

// taskSpawner is the subset of TaskTool the workflow tool depends on, so tests
// can stub the whole sub-agent stack.
type taskSpawner interface {
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// WorkflowTool is the model-facing and programmatic entry point for workflows:
// user-authored Starlark orchestration scripts in .reasonix/workflows/. Each
// agent()/parallel()/pipeline() call inside a script dispatches one task-tool
// call, so sub-agents keep the full parent integration (nested event stream,
// persisted transcripts, cache_from_parent, per-subtask model/effort) for free —
// WorkflowTool itself only marshals specs and parses the task tool's output.
type WorkflowTool struct {
	task  taskSpawner
	store *workflow.Store
}

// NewWorkflowTool wires a workflow tool to the given task tool and store. store
// may be nil when the session has no workflows (list/run then report none).
func NewWorkflowTool(task taskSpawner, store *workflow.Store) *WorkflowTool {
	return &WorkflowTool{task: task, store: store}
}

func (*WorkflowTool) Name() string { return "workflow" }

// ReadOnly is false: a workflow's sub-agents can invoke writer tools, so the
// parallel-dispatch path must not race two workflow runs' writes.
func (*WorkflowTool) ReadOnly() bool { return false }

func (*WorkflowTool) Description() string {
	return "Run a workflow from the Workflows index pinned in the system prompt, or an inline Starlark script. A workflow is a user-authored orchestration script: its agent(...)/parallel(...)/pipeline(...) calls spawn sub-agents whose reasoning stays out of your context — only the script's final result returns. Pass `name` as the BARE identifier from the index (e.g. 'audit'); pass `args` to feed the script parameters it reads as the `args` dict. Prefer a workflow for a repeatable multi-step or parallel process; for a single one-off delegation, use `task`."
}

func (*WorkflowTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "Workflow identifier as listed in the pinned Workflows index (e.g. 'audit'). Mutually exclusive with 'script'."
    },
    "script": {
      "type": "string",
      "description": "Inline Starlark workflow source, as an alternative to 'name'. Same hooks as a .star file: agent, parallel, pipeline, phase, log, and a top-level 'result' variable or def main() return."
    },
    "args": {
      "type": "object",
      "description": "Values exposed to the script as the 'args' dict. Any JSON object."
    }
  }
}`)
}

// Execute implements tool.Tool: run a named workflow (from the store) or an
// inline script given in the args.
func (t *WorkflowTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name   string         `json:"name"`
		Script string         `json:"script"`
		Args   map[string]any `json:"args"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("workflow: invalid args: %w", err)
	}
	switch {
	case strings.TrimSpace(p.Name) != "" && strings.TrimSpace(p.Script) != "":
		return "", fmt.Errorf("workflow: 'name' and 'script' are mutually exclusive")
	case strings.TrimSpace(p.Script) != "":
		return t.render(t.runScript(ctx, "<inline>", p.Script, p.Args))
	case strings.TrimSpace(p.Name) != "":
		return t.render(t.runNamed(ctx, strings.TrimSpace(p.Name), p.Args))
	default:
		return "", fmt.Errorf("workflow requires a 'name' (from the Workflows index) or an inline 'script'")
	}
}

// Run executes a workflow by name, returning its result value for programmatic
// callers (e.g. the /workflow slash command). The context may be wired with a
// nested sink / parent session so sub-agents stream like model-driven task calls.
func (t *WorkflowTool) Run(ctx context.Context, name string, args any) (any, error) {
	return t.runNamed(ctx, name, args)
}

// List returns the store's discoverable workflows (sorted), for the /workflow
// listing and the pinned index.
func (t *WorkflowTool) List() []workflow.Workflow {
	if t.store == nil {
		return nil
	}
	return t.store.List()
}

func (t *WorkflowTool) runNamed(ctx context.Context, name string, args any) (any, error) {
	if t.store == nil {
		return nil, fmt.Errorf("workflow %q: no workflows are loaded in this session", name)
	}
	wf, ok := t.store.Read(name)
	if !ok {
		return nil, fmt.Errorf("unknown workflow %q — available: %s", name, t.available())
	}
	return t.runScript(ctx, wf.Name, wf.Body, args)
}

func (t *WorkflowTool) runScript(ctx context.Context, name, body string, args any) (any, error) {
	res := t.newEngine().Run(ctx, body, args)
	switch res.StopReason {
	case workflow.StopCompleted:
		return res.Value, nil
	case workflow.StopCancelled:
		return nil, fmt.Errorf("workflow %q cancelled", name)
	default:
		return nil, fmt.Errorf("workflow %q failed: %s", name, res.Error)
	}
}

func (t *WorkflowTool) newEngine() *workflow.Engine {
	return workflow.NewEngine(workflow.Options{Spawn: t.spawn})
}

// spawn dispatches one workflow sub-agent through the task tool. The task tool
// reads the parent/sink from the context (wired by the caller) and returns a
// "Subagent reference: sa_..." envelope when transcripts are persisted; the ref
// is what scripts chain through continue_from / fork_from.
func (t *WorkflowTool) spawn(ctx context.Context, spec workflow.AgentSpec) (workflow.AgentResult, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return workflow.AgentResult{}, fmt.Errorf("workflow agent spec: %w", err)
	}
	out, err := t.task.Execute(ctx, raw)
	if err != nil {
		return workflow.AgentResult{}, err
	}
	return parseTaskOutput(out)
}

// parseTaskOutput extracts the final answer and optional subagent reference
// from a task-tool result (see FormatSubagentResult). A plain answer without
// the reference envelope passes through.
func parseTaskOutput(out string) (workflow.AgentResult, error) {
	out = strings.TrimSpace(out)
	for _, prefix := range []string{"Subagent reference (failed): ", "Subagent reference: "} {
		rest, ok := strings.CutPrefix(out, prefix)
		if !ok {
			continue
		}
		ref := rest
		text := ""
		if i := strings.Index(rest, "\n"); i >= 0 {
			ref = strings.TrimSpace(rest[:i])
			text = strings.TrimSpace(rest[i:])
		}
		if i := strings.Index(text, "\n\nFinal answer:\n"); i >= 0 {
			text = strings.TrimSpace(text[i+len("\n\nFinal answer:\n"):])
		} else {
			text = strings.TrimSpace(strings.TrimPrefix(text, "Final answer:\n"))
		}
		return workflow.AgentResult{Text: text, Ref: ref}, nil
	}
	return workflow.AgentResult{Text: out}, nil
}

// render formats a finished run for the model.
func (t *WorkflowTool) render(value any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	data, merr := json.MarshalIndent(value, "", "  ")
	if merr != nil {
		data = []byte(fmt.Sprintf("%v", value))
	}
	return fmt.Sprintf("Workflow completed.\nResult:\n%s", data), nil
}

func (t *WorkflowTool) available() string {
	names := make([]string, 0, len(t.List()))
	for _, wf := range t.List() {
		names = append(names, wf.Name)
	}
	if len(names) == 0 {
		return "(none — add scripts to .reasonix/workflows/)"
	}
	return strings.Join(names, ", ")
}

var _ tool.Tool = (*WorkflowTool)(nil)

package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"reasonix/internal/evidence"
	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(todoWrite{}) }

// todoWrite records the agent's running task list. It has no host side effects —
// the full list lives in the call's args (the model re-sends it whole on every
// update), which a frontend renders as a checklist. Execute just validates the
// shape and acks with a count, so the model gets a stable confirmation. The agent
// keeps one item in_progress at a time and flips each to completed as it finishes.
type todoWrite struct{}

type todoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm,omitempty"`
	Level      int    `json:"level,omitempty"`
}

func (todoWrite) Name() string { return "todo_write" }

func (todoWrite) Description() string {
	return "Record and update a structured task list for the current work. Send the COMPLETE list every call — it replaces the previous one. Use it to plan multi-step work and show progress: keep exactly one item in_progress at a time, and flip an item to completed the moment it's done (don't batch completions). Skip it for trivial single-step tasks. The list is two-level: a `level` 0 item is a PHASE (a milestone) and the `level` 1 items after it are its concrete sub-steps; omit `level` (0) for a flat list. Each item has `content` (imperative, e.g. \"Add the parser\"), `status` (pending|in_progress|completed), `activeForm` (present-continuous shown while in progress, e.g. \"Adding the parser\"), and optional `level` (0 phase | 1 sub-step)."
}

func (todoWrite) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "todos":{
    "type":"array",
    "description":"The complete task list, in order. Replaces any previous list.",
    "items":{
      "type":"object",
      "properties":{
        "content":{"type":"string","description":"Imperative description of the task."},
        "status":{"type":"string","enum":["pending","in_progress","completed"],"description":"Task state. Keep at most one in_progress."},
        "activeForm":{"type":"string","description":"Present-continuous form shown while the task is in progress (e.g. \"Running tests\")."},
        "level":{"type":"integer","enum":[0,1],"description":"Nesting level: 0 = phase/milestone, 1 = a sub-step of the phase above it. Omit for a flat list."}
      },
      "required":["content","status"]
    }
  }
},
"required":["todos"]
}`)
}

// ReadOnly is true: todo_write only records a list (no filesystem or process
// effect), so it never needs approval and stays available in plan mode — where
// laying out a plan as todos is exactly the point.
func (todoWrite) ReadOnly() bool { return true }

func (todoWrite) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Todos []todoItem `json:"todos"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	var done, active, pending int
	for i, t := range p.Todos {
		if t.Content == "" {
			return "", fmt.Errorf("todo %d: content is required", i+1)
		}
		if t.Level < 0 || t.Level > 1 {
			return "", fmt.Errorf("todo %d: invalid level %d (want 0 phase | 1 sub-step)", i+1, t.Level)
		}
		switch t.Status {
		case "completed":
			done++
		case "in_progress":
			active++
		case "pending", "":
			pending++
		default:
			return "", fmt.Errorf("todo %d: invalid status %q (want pending|in_progress|completed)", i+1, t.Status)
		}
	}
	if err := verifyTodoCompletionTransitions(ctx, p.Todos); err != nil {
		return "", err
	}
	return fmt.Sprintf("Todos updated: %d total — %d completed, %d in progress, %d pending.",
		len(p.Todos), done, active, pending), nil
}

func verifyTodoCompletionTransitions(ctx context.Context, todos []todoItem) error {
	evidenceTodos := toEvidenceTodos(todos)
	ledger, ok := evidence.FromContext(ctx)
	if !ok {
		return nil
	}
	missing, hasBaseline := ledger.UnverifiedCompletedTodos(evidenceTodos)
	if !hasBaseline {
		// No baseline todo_write in the current turn — fall back to scanning
		// session messages for a prior turn's todo_write to prevent the model
		// from marking everything completed on the first call of the turn.
		if msgs, ok := evidence.SessionMessagesFromContext(ctx); ok {
			if prior, ok := evidence.LastTodoWriteFromSession(msgs); ok {
				missing = completedWithoutBaseline(evidenceTodos, prior, ledger)
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	const hint = "; sign each finished item off with complete_step first, then re-send this todo_write"
	if len(missing) == 1 {
		m := missing[0]
		return fmt.Errorf("todo %d %q is newly completed but has no matching successful complete_step receipt in this turn%s", m.Index, m.Content, hint)
	}
	return fmt.Errorf("%d todos are newly completed but have no matching successful complete_step receipts in this turn%s", len(missing), hint)
}

// completedWithoutBaseline compares current todos against a prior session
// baseline when no turn baseline exists. Items already completed in the prior
// turn are skipped (matched by content identity, not position); only newly
// completed items without a matching complete_step receipt in the current turn
// ledger are reported.
func completedWithoutBaseline(current, prior []evidence.TodoItem, ledger *evidence.Ledger) []evidence.TodoStepMatch {
	current = evidence.NormalizeTodos(current)
	var missing []evidence.TodoStepMatch
	for i, t := range current {
		if t.Status != "completed" {
			continue
		}
		if evidence.WasCompletedInPrior(t, prior) {
			continue
		}
		if ledger.HasCompleteStepForTodoInList(t.Content, current) {
			continue
		}
		missing = append(missing, evidence.TodoStepMatch{
			Found:      true,
			Index:      i + 1,
			Content:    t.Content,
			Status:     "completed",
			ActiveForm: t.ActiveForm,
		})
	}
	return missing
}

func toEvidenceTodos(todos []todoItem) []evidence.TodoItem {
	out := make([]evidence.TodoItem, 0, len(todos))
	for _, t := range todos {
		out = append(out, evidence.TodoItem{
			Content:    t.Content,
			Status:     t.Status,
			ActiveForm: t.ActiveForm,
			Level:      t.Level,
		})
	}
	return out
}

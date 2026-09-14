package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/workflow"
)

type stubTask struct {
	result string
	err    error
}

func (s *stubTask) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return s.result, s.err
}

func TestParseTaskOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		text string
		ref  string
	}{
		{
			name: "success envelope",
			in:   "Subagent reference: sa_abc123\n\nFinal answer:\nReviewed everything.",
			text: "Reviewed everything.",
			ref:  "sa_abc123",
		},
		{
			name: "failed envelope with answer",
			in:   "Subagent reference (failed): sa_xyz\n\nFinal answer:\nPartial result.",
			text: "Partial result.",
			ref:  "sa_xyz",
		},
		{
			name: "failed envelope no answer",
			in:   "Subagent reference (failed): sa_err",
			text: "",
			ref:  "sa_err",
		},
		{
			name: "plain answer",
			in:   "No subagent involved.",
			text: "No subagent involved.",
			ref:  "",
		},
		{
			name: "empty",
			in:   "",
			text: "",
			ref:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := parseTaskOutput(tt.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if r.Text != tt.text {
				t.Errorf("Text = %q, want %q", r.Text, tt.text)
			}
			if r.Ref != tt.ref {
				t.Errorf("Ref = %q, want %q", r.Ref, tt.ref)
			}
		})
	}
}

func TestWorkflowToolExecuteNamedMissing(t *testing.T) {
	store := workflow.NewStore(t.TempDir())
	sp := &stubTask{result: "Subagent reference: sa_1\n\nFinal answer:\nhello"}
	tool := NewWorkflowTool(sp, store)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"audit"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown workflow") {
		t.Errorf("expected 'unknown workflow' error, got %v", err)
	}
}

func TestWorkflowToolExecuteInlineScript(t *testing.T) {
	sp := &stubTask{result: "Subagent reference: sa_2\n\nFinal answer:\nreviewed"}
	tool := NewWorkflowTool(sp, nil)
	raw, _ := json.Marshal(map[string]any{
		"script": `result = agent("review code")`,
		"args":   map[string]any{"scope": "auth"},
	})
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Workflow completed") || !strings.Contains(out, "reviewed") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestWorkflowToolRunMissing(t *testing.T) {
	store := workflow.NewStore(t.TempDir())
	sp := &stubTask{result: "Subagent reference: sa_3\n\nFinal answer:\nthe value"}
	tool := NewWorkflowTool(sp, store)
	_, err := tool.Run(context.Background(), "nonexistent", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown workflow") {
		t.Errorf("expected unknown workflow error, got %v", err)
	}
}

func TestWorkflowToolList(t *testing.T) {
	if got := NewWorkflowTool(nil, workflow.NewStore(t.TempDir())).List(); len(got) != 0 {
		t.Errorf("List() = %v, want empty", got)
	}
	if got := NewWorkflowTool(nil, nil).List(); got != nil {
		t.Errorf("nil store List() = %v, want nil", got)
	}
}

func TestWorkflowToolNoArgs(t *testing.T) {
	tool := NewWorkflowTool(&stubTask{}, nil)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "requires a") {
		t.Errorf("expected requires error, got %v", err)
	}
}

func TestWorkflowToolMutuallyExclusive(t *testing.T) {
	tool := NewWorkflowTool(&stubTask{}, nil)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"a","script":"b"}`))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutually exclusive error, got %v", err)
	}
}

package control

import (
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/tool"
)

// TestControllerTaskTool verifies the TaskTool accessor surfaces the registered
// `task` tool (used by the TUI's /subtask slash command) and returns nil when
// the tool is absent or the registry is nil.
func TestControllerTaskTool(t *testing.T) {
	if c := (&Controller{}).TaskTool(); c != nil {
		t.Fatalf("TaskTool on a controller with no registry should be nil, got %v", c)
	}

	empty := &Controller{reg: tool.NewRegistry()}
	if tt := empty.TaskTool(); tt != nil {
		t.Fatalf("TaskTool on an empty registry should be nil, got %v", tt)
	}

	tt := agent.NewTaskTool(nil, nil, tool.NewRegistry(), 20, 0, 0, 0, 0, 0, 0.0, "", "sys", nil, 0, "", "", nil, nil)
	reg := tool.NewRegistry()
	reg.Add(tt)
	withTool := &Controller{reg: reg}
	if got := withTool.TaskTool(); got == nil {
		t.Fatal("TaskTool should return the registered task tool")
	} else if got != tt {
		t.Fatal("TaskTool should return the exact registered *agent.TaskTool")
	}
}

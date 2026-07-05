package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/tool"
)

type agentSpawnTool struct {
	orc *Orchestrator
}

func (t *agentSpawnTool) Name() string        { return "agent_spawn" }
func (t *agentSpawnTool) Description() string  { return "Delegate a complete task to a named managed agent. The agent runs independently with its own model, tools, and context. When you receive the result, the agent has finished the work — integrate the outcome and move on. Do not repeat the delegated work." }
func (t *agentSpawnTool) ReadOnly() bool       { return false }

func (t *agentSpawnTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Name of the managed agent to delegate to"},
			"task": {"type": "string", "description": "The task or question for the agent"}
		},
		"required": ["name", "task"]
	}`)
}

func (t *agentSpawnTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name string `json:"name"`
		Task string `json:"task"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("agent_spawn: invalid args: %w", err)
	}
	if p.Name == "" {
		return "", fmt.Errorf("agent_spawn: name is required")
	}
	if p.Task == "" {
		return "", fmt.Errorf("agent_spawn: task is required")
	}

	a, ok := t.orc.Agent(p.Name)
	if !ok {
		names := strings.Join(t.orc.AgentNames(), ", ")
		return "", fmt.Errorf("agent_spawn: agent %q not found. Available agents: %s", p.Name, names)
	}

	parentID, parent, _, ok := agent.CallContext(ctx)
	if ok && parent != nil {
		a.Sink.SetParentID(parentID)
	}

	result, err := a.Run(ctx, p.Task)
	if err != nil {
		return fmt.Sprintf("[Agent %q completed with error] %v\n\nPartial result: %s", p.Name, err, result), nil
	}
	return fmt.Sprintf("[Agent %q completed]\n\n%s", p.Name, result), nil
}

type agentSendTool struct {
	orc *Orchestrator
}

func (t *agentSendTool) Name() string        { return "agent_send" }
func (t *agentSendTool) Description() string  { return "Send a message to a managed agent and wait for its response. Unlike agent_spawn, this continues the agent's existing conversation context. When you receive the result, the agent has finished responding — integrate the outcome and move on. Do not repeat the delegated work." }
func (t *agentSendTool) ReadOnly() bool       { return false }

func (t *agentSendTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Name of the managed agent to message"},
			"message": {"type": "string", "description": "The message to send"}
		},
		"required": ["name", "message"]
	}`)
}

func (t *agentSendTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("agent_send: invalid args: %w", err)
	}
	if p.Name == "" {
		return "", fmt.Errorf("agent_send: name is required")
	}
	if p.Message == "" {
		return "", fmt.Errorf("agent_send: message is required")
	}

	parentID, parent, _, ok := agent.CallContext(ctx)
	if ok && parent != nil {
		if a, found := t.orc.Agent(p.Name); found {
			a.Sink.SetParentID(parentID)
		}
	}

	result, err := t.orc.SendMessage(ctx, p.Name, p.Message)
	if err != nil {
		return fmt.Sprintf("[Agent %q completed with error] %v", p.Name, err), nil
	}
	return fmt.Sprintf("[Agent %q completed]\n\n%s", p.Name, result), nil
}

type agentStatusTool struct {
	orc *Orchestrator
}

func (t *agentStatusTool) Name() string        { return "agent_status" }
func (t *agentStatusTool) Description() string  { return "Get the status of a managed agent (idle/running, turn count, last task)." }
func (t *agentStatusTool) ReadOnly() bool       { return true }

func (t *agentStatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Name of the managed agent (omit to list all)"}
		}
	}`)
}

func (t *agentStatusTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(args, &p)

	if p.Name != "" {
		a, ok := t.orc.Agent(p.Name)
		if !ok {
			names := strings.Join(t.orc.AgentNames(), ", ")
			return "", fmt.Errorf("agent %q not found. Available: %s", p.Name, names)
		}

		usage := a.SessionUsage()
		return fmt.Sprintf("Agent: %s\nStatus: %s\nTurns: %d\nTokens: %d total (prompt %d + completion %d)\nCache: %d hit / %d miss\nLast task: %s\nCost: $%.4f",
			a.Name, a.Status(), a.TurnCount(),
			usage.TotalTokens, usage.PromptTokens, usage.CompletionTokens,
			usage.CacheHitTokens, usage.CacheMissTokens,
			a.LastTask(), usage.Cost), nil
	}

	return t.orc.StatsAll(), nil
}

type agentStatsTool struct {
	orc *Orchestrator
}

func (t *agentStatsTool) Name() string        { return "agent_stats" }
func (t *agentStatsTool) Description() string  { return "Get detailed token/cost statistics for a managed agent." }
func (t *agentStatsTool) ReadOnly() bool       { return true }

func (t *agentStatsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Name of the managed agent"}
		},
		"required": ["name"]
	}`)
}

func (t *agentStatsTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("agent_stats: invalid args: %w", err)
	}
	if p.Name == "" {
		return t.orc.StatsAll(), nil
	}
	return t.orc.Stats(p.Name), nil
}

func OrchestratorTools(orc *Orchestrator) []tool.Tool {
	return []tool.Tool{
		&agentSpawnTool{orc: orc},
		&agentSendTool{orc: orc},
		&agentStatusTool{orc: orc},
		&agentStatsTool{orc: orc},
	}
}

func OrchestratorToolNames() []string {
	return []string{
		"agent_spawn",
		"agent_send",
		"agent_status",
		"agent_stats",
	}
}

// NoGitBash wraps the bash tool to intercept git commands when a dedicated git
// child agent exists. The main agent must use agent_spawn to delegate git
// operations instead of running them directly via bash.
type NoGitBash struct {
	Inner   tool.Tool
	Orc     *Orchestrator
	GitName string
}

func (b *NoGitBash) Name() string { return "bash" }

func (b *NoGitBash) Description() string {
	desc := strings.TrimSpace(b.Inner.Description())
	if desc == "" {
		desc = "Execute a command in the shell and return combined stdout/stderr."
	}
	return desc + " Git operations must use agent_spawn to the git agent."
}

func (b *NoGitBash) Schema() json.RawMessage { return b.Inner.Schema() }

func (b *NoGitBash) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return b.Inner.Execute(ctx, args)
	}
	cmd := strings.TrimSpace(p.Command)
	if isGitCommand(cmd) {
		return "", fmt.Errorf("git operations must be delegated to the %q agent via agent_spawn, not run directly through bash", b.GitName)
	}
	return b.Inner.Execute(ctx, args)
}

func (b *NoGitBash) ReadOnly() bool { return b.Inner.ReadOnly() }

func isGitCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if !strings.HasPrefix(cmd, "git ") {
		return false
	}
	// Allow safe read-only git commands that don't modify the repo
	gitParts := strings.Fields(cmd)
	if len(gitParts) < 2 {
		return false
	}
	subcmd := gitParts[1]
	switch subcmd {
	case "status", "log", "diff", "show", "branch", "describe", "rev-parse", "rev-list", "ls-files", "ls-tree", "cat-file", "config":
		return false
	}
	return true
}



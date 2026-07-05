package memory

import (
	"context"
	"encoding/json"
	"fmt"

	"reasonix/internal/tool"
)

// rememberTool lets the model persist a durable fact to the auto-memory store.
// It is stateful (bound to one project's Store), so boot constructs it and adds
// it to the registry — the same pattern as the task tool — rather than
// self-registering as a stateless built-in.
type rememberTool struct{ store Store }

// NewRememberTool returns the `remember` tool bound to store. A zero/disabled
// store yields a tool that reports the store is unavailable rather than silently
// dropping saves.
func NewRememberTool(store Store) tool.Tool { return rememberTool{store: store} }

func (rememberTool) Name() string { return "remember" }

func (rememberTool) Description() string {
	return "Save a durable fact to project memory so it survives across sessions. Use for: user preferences (\"user\"), guidance (\"feedback\"), goals/projects (\"project\"), references (\"reference\"). Include \"**Why:**\" and \"**How to apply:**\" for feedback/project. Check the memory index first to avoid duplicates."
}

func (rememberTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Short kebab-case slug identifying the fact, e.g. \"prefers-tabs\". Reusing a name overwrites that memory — do that to update an existing fact. Omit to derive one from the description."},
			"title": {"type": "string", "description": "Short human-readable label shown in the memory index, e.g. \"Prefers tabs\". Omit to derive one from the name."},
			"description": {"type": "string", "description": "One-line hook shown in the index — the phrase a future session reads to decide whether to open this memory. Make it specific."},
			"type": {"type": "string", "enum": ["user", "feedback", "project", "reference"], "description": "Category of the fact."},
			"body": {"type": "string", "description": "The fact itself (Markdown). For feedback/project, include a \"**Why:**\" line and a \"**How to apply:**\" line; link related memories with [[their-name]]."}
		},
		"required": ["description", "body"]
	}`)
}

func (t rememberTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Type        string `json:"type"`
		Body        string `json:"body"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if in.Description == "" || in.Body == "" {
		return "", fmt.Errorf("description and body are required")
	}
	name := in.Name
	if name == "" {
		name = in.Title // Save slugifies; the title (or, below, the description) makes a serviceable slug
	}
	if name == "" {
		name = in.Description
	}
	path, err := t.store.Save(Memory{
		Name:        name,
		Title:       in.Title,
		Description: in.Description,
		Type:        NormalizeType(in.Type),
		Body:        in.Body,
	})
	if err != nil {
		return "", err
	}
	if q, ok := QueueFromContext(ctx); ok {
		q.QueueMemory("Saved memory \"" + slug(name) + "\": " + oneLine(in.Description))
	}
	return fmt.Sprintf("Saved memory to %s (it applies now and loads automatically in future sessions).", path), nil
}

func (rememberTool) ReadOnly() bool { return false }

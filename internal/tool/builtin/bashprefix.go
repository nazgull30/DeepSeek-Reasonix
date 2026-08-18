package builtin

import (
	"context"
	"encoding/json"
	"strings"

	"reasonix/internal/tool"
)

// WrapBashPrefix wraps the bash tool so every foreground command is prefixed
// with prefix (e.g. "rtk" to run each command through the RTK token optimizer).
// The wrapper is a no-op passthrough when prefix is empty, so it can be applied
// unconditionally and only rewrites when the user opted in via reasonix.toml.
// Background commands (run_in_background) are never prefixed — the optimization
// targets the token-heavy foreground output the model must parse.
func WrapBashPrefix(inner tool.Tool, prefix string) tool.Tool {
	if strings.TrimSpace(prefix) == "" {
		return inner
	}
	return bashPrefix{Inner: inner, Prefix: strings.TrimSpace(prefix)}
}

// bashPrefix decorates the bash tool by rewriting the command argument before
// delegating to Inner.Execute. It mirrors the NoGitBash decorator's shape: the
// tool keeps its original Name so the registry entry is replaced, not duplicated.
type bashPrefix struct {
	Inner  tool.Tool
	Prefix string
}

func (b bashPrefix) Name() string        { return b.Inner.Name() }
func (b bashPrefix) Description() string { return b.Inner.Description() }
func (b bashPrefix) Schema() json.RawMessage {
	return b.Inner.Schema()
}
func (b bashPrefix) ReadOnly() bool { return b.Inner.ReadOnly() }

func (b bashPrefix) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Command         string `json:"command"`
		RunInBackground bool   `json:"run_in_background"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return b.Inner.Execute(ctx, args)
	}
	if p.RunInBackground || strings.TrimSpace(p.Command) == "" {
		return b.Inner.Execute(ctx, args)
	}
	// Mutate only the command key so every other arg (e.g.
	// preserve_background_processes) survives the rewrite untouched.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err != nil {
		return b.Inner.Execute(ctx, args)
	}
	rewritten, err := json.Marshal(b.Prefix + " " + p.Command)
	if err != nil {
		return b.Inner.Execute(ctx, args)
	}
	m["command"] = rewritten
	out, err := json.Marshal(m)
	if err != nil {
		return b.Inner.Execute(ctx, args)
	}
	return b.Inner.Execute(ctx, out)
}

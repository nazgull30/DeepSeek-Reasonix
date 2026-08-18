package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"reasonix/internal/tool"
)

// recordingBash is a minimal tool.Tool that records the raw args handed to
// Execute, so tests can assert what WrapBashPrefix actually passed through.
type recordingBash struct {
	name     string
	gotArgs  []byte
	executed bool
}

func (r *recordingBash) Name() string        { return r.name }
func (r *recordingBash) Description() string { return "recording bash" }
func (r *recordingBash) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (r *recordingBash) ReadOnly() bool { return false }

func (r *recordingBash) Execute(_ context.Context, args json.RawMessage) (string, error) {
	r.executed = true
	r.gotArgs = append([]byte(nil), args...)
	return "output", nil
}

func mustArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return b
}

func TestWrapBashPrefixNoopWhenEmpty(t *testing.T) {
	inner := &recordingBash{name: "bash"}
	wrapped := WrapBashPrefix(inner, "   ")
	if wrapped != tool.Tool(inner) {
		t.Fatalf("empty prefix should return the inner tool unchanged, got %T", wrapped)
	}
}

func TestWrapBashPrefixKeepsIdentity(t *testing.T) {
	inner := &recordingBash{name: "bash"}
	wrapped := WrapBashPrefix(inner, "rtk")
	if got := wrapped.Name(); got != "bash" {
		t.Errorf("Name() = %q, want bash", got)
	}
	if got := wrapped.ReadOnly(); got {
		t.Errorf("ReadOnly() = true, want false")
	}
	if got := string(wrapped.Schema()); got != `{"type":"object"}` {
		t.Errorf("Schema() = %s, want passthrough", got)
	}
	if wrapped.Description() != "recording bash" {
		t.Errorf("Description() = %q, want passthrough", wrapped.Description())
	}
}

func TestWrapBashPrefixForegroundCommand(t *testing.T) {
	inner := &recordingBash{name: "bash"}
	wrapped := WrapBashPrefix(inner, "rtk")
	out, err := wrapped.Execute(context.Background(), mustArgs(t, map[string]any{
		"command": "ls -la",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "output" {
		t.Errorf("out = %q, want passthrough output", out)
	}
	var got struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(inner.gotArgs, &got); err != nil {
		t.Fatalf("unmarshal recorded args: %v", err)
	}
	if got.Command != "rtk ls -la" {
		t.Errorf("command = %q, want %q", got.Command, "rtk ls -la")
	}
}

func TestWrapBashPrefixSkipsBackground(t *testing.T) {
	inner := &recordingBash{name: "bash"}
	wrapped := WrapBashPrefix(inner, "rtk")
	if _, err := wrapped.Execute(context.Background(), mustArgs(t, map[string]any{
		"command":             "sleep 5",
		"run_in_background":   true,
		"preserve_background": false,
	})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var got struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(inner.gotArgs, &got); err != nil {
		t.Fatalf("unmarshal recorded args: %v", err)
	}
	if got.Command != "sleep 5" {
		t.Errorf("background command = %q, want unprefixed %q", got.Command, "sleep 5")
	}
}

func TestWrapBashPrefixSkipsEmptyCommand(t *testing.T) {
	inner := &recordingBash{name: "bash"}
	wrapped := WrapBashPrefix(inner, "rtk")
	if _, err := wrapped.Execute(context.Background(), mustArgs(t, map[string]any{
		"command": "   ",
	})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var got struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(inner.gotArgs, &got); err != nil {
		t.Fatalf("unmarshal recorded args: %v", err)
	}
	if got.Command != "   " {
		t.Errorf("empty command = %q, want left verbatim", got.Command)
	}
}

func TestWrapBashPrefixPassthroughOnUnmarshalError(t *testing.T) {
	inner := &recordingBash{name: "bash"}
	wrapped := WrapBashPrefix(inner, "rtk")
	bad := json.RawMessage(`{"command": 42}`) // type mismatch → unmarshal error
	if _, err := wrapped.Execute(context.Background(), bad); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !inner.executed {
		t.Fatal("inner tool was not invoked")
	}
	if string(inner.gotArgs) != string(bad) {
		t.Errorf("args not passed through verbatim: %s", inner.gotArgs)
	}
}

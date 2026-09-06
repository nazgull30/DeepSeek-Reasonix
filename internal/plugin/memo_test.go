package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeMemoTransport counts backend tools/call invocations so a test can prove
// that repeated identical read-only calls are served from the memo instead of
// the (fake) MCP server.
type fakeMemoTransport struct {
	calls atomic.Int32
}

func (f *fakeMemoTransport) call(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method == "tools/call" {
		f.calls.Add(1)
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(mustJSON(params), &p)
		return json.RawMessage(fmt.Sprintf(`{"content":[{"type":"text","text":"echo %s: %v"}],"isError":false}`, p.Name, p.Arguments["msg"])), nil
	}
	return json.RawMessage(`{}`), nil
}
func (*fakeMemoTransport) notify(_ context.Context, _ string, _ any) error { return nil }
func (*fakeMemoTransport) close()                                          {}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestRemoteToolMemoizesIdenticalReadOnlyCalls(t *testing.T) {
	tr := &fakeMemoTransport{}
	client := &Client{name: "memo", t: tr, spec: Spec{ReadOnlyToolNames: map[string]bool{"echo": true}}}
	rt := &remoteTool{client: client, name: "mcp__memo__echo", rawName: "echo", readOnly: true}

	ctx := context.Background()
	// Same logical args, differing JSON key order — must dedupe to one call.
	argSets := []json.RawMessage{
		json.RawMessage(`{"msg":"hi"}`),
		json.RawMessage(`{"msg":"hi"}`),
	}
	for i, args := range argSets {
		out, err := rt.Execute(ctx, args)
		if err != nil {
			t.Fatalf("round %d: Execute: %v", i, err)
		}
		if !strings.Contains(out, "echo: hi") {
			t.Fatalf("round %d: unexpected result %q", i, out)
		}
	}
	if got := tr.calls.Load(); got != 1 {
		t.Fatalf("identical read-only call hit the server %d times, want 1 (memoized)", got)
	}
}

func TestRemoteToolDoesNotMemoizeWriters(t *testing.T) {
	tr := &fakeMemoTransport{}
	client := &Client{name: "memo", t: tr}
	rt := &remoteTool{client: client, name: "mcp__memo__write", rawName: "write", readOnly: false}

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := rt.Execute(ctx, json.RawMessage(`{"msg":"hi"}`)); err != nil {
			t.Fatalf("round %d: Execute: %v", i, err)
		}
	}
	if got := tr.calls.Load(); got != 2 {
		t.Fatalf("writer calls should bypass the memo: got %d, want 2", got)
	}
}

func TestCanonicalArgsKeyNormalizesKeyOrder(t *testing.T) {
	a, err := canonicalArgsKey(json.RawMessage(`{"z":1,"a":{"y":2,"x":3}}`))
	if err != nil {
		t.Fatalf("canonicalArgsKey: %v", err)
	}
	b, err := canonicalArgsKey(json.RawMessage(`{"a":{"x":3,"y":2},"z":1}`))
	if err != nil {
		t.Fatalf("canonicalArgsKey: %v", err)
	}
	if a != b {
		t.Fatalf("semantically identical args produced different keys: %s vs %s", a, b)
	}
	want := `{"a":{"x":3,"y":2},"z":1}`
	if a != want {
		t.Fatalf("canonical args key = %s, want %s", a, want)
	}
}

func TestMemoEvictsExpiredEntries(t *testing.T) {
	client := &Client{name: "memo"}
	client.memoSet("k1", "v1")
	if res, ok := client.memoGet("k1"); !ok || res != "v1" {
		t.Fatalf("fresh entry: got %q ok=%v, want v1 ok=true", res, ok)
	}
	entry := client.memo["k1"]
	entry.at = entry.at.Add(-resultMemoTTL - time.Second)
	client.memo["k1"] = entry
	if _, ok := client.memoGet("k1"); ok {
		t.Fatal("expired entry should be evicted")
	}
}

package control

import (
	"path/filepath"
	"sync"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// sessionScopedRecorder is a provider that opts into the per-conversation id the
// way the openai/anthropic clients do, recording what the controller binds.
type sessionScopedRecorder struct {
	provider.Provider
	mu  sync.Mutex
	ids []string
}

func (p *sessionScopedRecorder) SetSessionID(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ids = append(p.ids, id)
}

func (p *sessionScopedRecorder) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.ids...)
}

func TestControllerBindsProviderSessionOnSessionChange(t *testing.T) {
	rec := &sessionScopedRecorder{}
	dir := t.TempDir()
	c := &Controller{
		executor:   agent.New(rec, tool.NewRegistry(), agent.NewSession(""), agent.Options{}, nil),
		sessionDir: dir,
		jobs:       nil,
	}

	// Construction pins the session it was handed.
	c.bindProviderSession(filepath.Join(dir, "boot.jsonl"))
	// A new session, a resume and a branch switch each rebind to their own id —
	// this is what keeps one conversation on one gateway route.
	c.SetSessionPath(filepath.Join(dir, "second.jsonl"))
	c.Resume(agent.NewSession(""), filepath.Join(dir, "resumed.jsonl"))

	want := []string{"boot", "second", "resumed"}
	if got := rec.seen(); len(got) != len(want) {
		t.Fatalf("bound ids = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("bound ids = %v, want %v", got, want)
				break
			}
		}
	}
}

// TestControllerSessionBindToleratesNoSessionAndNoExecutor covers the two shapes
// that must stay silent: persistence disabled (no session id to bind) and a
// controller built without an executor.
func TestControllerSessionBindToleratesNoSessionAndNoExecutor(t *testing.T) {
	rec := &sessionScopedRecorder{}
	c := &Controller{executor: agent.New(rec, tool.NewRegistry(), agent.NewSession(""), agent.Options{}, nil)}
	c.SetSessionPath("") // persistence disabled — the client keeps its own id
	if got := rec.seen(); len(got) != 1 || got[0] != "" {
		t.Errorf("bound ids = %v, want one empty id", got)
	}
	(&Controller{}).bindProviderSession("x.jsonl") // no executor: must not panic
}

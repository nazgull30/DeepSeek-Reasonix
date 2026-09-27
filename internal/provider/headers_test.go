package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequiresSessionHeaderOnlyForOpenCodeGateways(t *testing.T) {
	for _, tc := range []struct {
		base string
		want bool
	}{
		{"https://opencode.ai/zen/go/v1", true},
		{"https://opencode.ai/zen/v1", true},
		{"https://OpenCode.AI/zen/go/v1", true},
		{"https://gateway.opencode.ai/v1", true},
		{"https://api.deepseek.com", false},
		{"https://api.anthropic.com", false},
		{"http://127.0.0.1:8080/v1", false},
		{"https://notopencode.ai.example.com/v1", false},
		{"", false},
		{"://bad url", false},
	} {
		if got := RequiresSessionHeader(tc.base); got != tc.want {
			t.Errorf("RequiresSessionHeader(%q) = %v, want %v", tc.base, got, tc.want)
		}
	}
}

func TestWireHeadersAppliesIdentityAndSession(t *testing.T) {
	h := NewWireHeaders("https://opencode.ai/zen/go/v1", map[string]string{"X-Tenant": "acme"})
	if h.SessionID() == "" {
		t.Fatal("a routing gateway must get a session id before one is bound")
	}
	req := httptest.NewRequest(http.MethodPost, "https://opencode.ai/zen/go/v1/chat/completions", nil)
	h.Apply(req)
	if got := req.Header.Get("User-Agent"); got != UserAgent {
		t.Errorf("User-Agent = %q, want %q", got, UserAgent)
	}
	if got := req.Header.Get("X-Tenant"); got != "acme" {
		t.Errorf("X-Tenant = %q, want %q (configured static header)", got, "acme")
	}
	if got := req.Header.Get(SessionHeader); got != h.SessionID() {
		t.Errorf("%s = %q, want the minted id %q", SessionHeader, got, h.SessionID())
	}

	h.SetSessionID("20260927-101500.000000000-deepseek-v4-flash")
	req = httptest.NewRequest(http.MethodPost, "https://opencode.ai/zen/go/v1/chat/completions", nil)
	h.Apply(req)
	if got := req.Header.Get(SessionHeader); got != "20260927-101500.000000000-deepseek-v4-flash" {
		t.Errorf("%s = %q, want the bound conversation id", SessionHeader, got)
	}
}

func TestWireHeadersSkipsSessionOffGateways(t *testing.T) {
	h := NewWireHeaders("https://api.deepseek.com", nil)
	h.SetSessionID("some-conversation")
	req := httptest.NewRequest(http.MethodPost, "https://api.deepseek.com/chat/completions", nil)
	h.Apply(req)
	if got := req.Header.Get(SessionHeader); got != "" {
		t.Errorf("%s = %q on a non-routing endpoint, want it absent", SessionHeader, got)
	}
	if got := req.Header.Get("User-Agent"); got != UserAgent {
		t.Errorf("User-Agent = %q, want %q", got, UserAgent)
	}
}

func TestWireHeadersEmptySessionClearsHeader(t *testing.T) {
	h := NewWireHeaders("https://opencode.ai/zen/go/v1", map[string]string{SessionHeader: ""})
	h.SetSessionID("")
	req := httptest.NewRequest(http.MethodPost, "https://opencode.ai/zen/go/v1/chat/completions", nil)
	h.Apply(req)
	if got := req.Header.Get(SessionHeader); got != "" {
		t.Errorf("%s = %q with no conversation bound, want it absent", SessionHeader, got)
	}
}

// sessionScopedProvider is the shape a provider takes when it opts in to the
// per-conversation id (see the openai/anthropic clients).
type sessionScopedProvider struct {
	Provider
	id string
}

func (p *sessionScopedProvider) SetSessionID(id string) { p.id = id }

type plainProvider struct{ Provider }

func TestSetSessionIDOnlyStampsSessionScopedProviders(t *testing.T) {
	scoped := &sessionScopedProvider{}
	SetSessionID(scoped, "conv-1")
	if scoped.id != "conv-1" {
		t.Errorf("session-scoped provider id = %q, want %q", scoped.id, "conv-1")
	}
	SetSessionID(plainProvider{}, "conv-1") // must not panic on a non-scoped provider
}

func TestNewSessionIDIsDistinctAndNonEmpty(t *testing.T) {
	seen := map[string]bool{}
	for range 8 {
		id := NewSessionID()
		if id == "" {
			t.Fatal("NewSessionID returned an empty id")
		}
		if seen[id] {
			t.Fatalf("NewSessionID repeated %q", id)
		}
		seen[id] = true
	}
}

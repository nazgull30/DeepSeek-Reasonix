package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"reasonix/internal/provider"
)

// TestStreamSendsSessionHeaderToGateway pins the routing header on the
// Anthropic-shaped path too: OpenCode Go serves MiniMax/Qwen over /v1/messages
// behind the same session-id gate as /chat/completions.
func TestStreamSendsSessionHeaderToGateway(t *testing.T) {
	var gotUA, gotSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotSession = r.Header.Get("User-Agent"), r.Header.Get(provider.SessionHeader)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	// New derives the routing gate from BaseURL, so build the client directly to
	// point the gate at a Zen endpoint while the request lands on the test server.
	p := &client{
		name: "opencode-go", apiKey: "k", baseURL: srv.URL, model: "minimax-m3",
		http:        srv.Client(),
		headers:     provider.NewWireHeaders("https://opencode.ai/zen/go/v1", nil),
		idleTimeout: defaultStreamIdleTimeout,
	}
	p.SetSessionID("conv-42")

	ch, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	if gotSession != "conv-42" {
		t.Errorf("%s = %q, want the bound conversation id %q", provider.SessionHeader, gotSession, "conv-42")
	}
	if gotUA != provider.UserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, provider.UserAgent)
	}
}

// TestStreamSendsConfiguredProviderHeaders covers the config-level escape hatch.
func TestStreamSendsConfiguredProviderHeaders(t *testing.T) {
	var gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get("X-Tenant")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name: "gateway", BaseURL: srv.URL, Model: "m", APIKey: "k",
		Headers: map[string]string{"X-Tenant": "acme"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	if gotTenant != "acme" {
		t.Errorf("X-Tenant = %q, want %q", gotTenant, "acme")
	}
}

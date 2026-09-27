package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"reasonix/internal/provider"
)

// sseOK answers a chat/completions probe with a minimal stream.
func sseOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
}

func drain(t *testing.T, ch <-chan provider.Chunk) {
	t.Helper()
	for range ch { //nolint:revive // draining the stream is the point
	}
}

// TestStreamSendsIdentityAndSessionHeader pins the two wire headers a gateway
// that meters and routes agent traffic (OpenCode Zen/Go) requires: a named user
// agent, and a session id that stays put across requests of one conversation.
func TestStreamSendsIdentityAndSessionHeader(t *testing.T) {
	var gotUA, gotSession string
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		gotUA, gotSession = r.Header.Get("User-Agent"), r.Header.Get(provider.SessionHeader)
		sseOK(w)
	}))
	defer srv.Close()

	// New derives the routing gate from BaseURL, so build the client directly to
	// point the gate at a Zen endpoint while the request lands on the test server.
	p := &client{
		name: "opencode-go", apiKey: "k", baseURL: srv.URL, model: "deepseek-v4-flash",
		http:        srv.Client(),
		headers:     provider.NewWireHeaders("https://opencode.ai/zen/go/v1", nil),
		idleTimeout: defaultStreamIdleTimeout,
	}
	p.SetSessionID("conv-42")

	for range 2 { // a second turn must present the same id
		ch, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		drain(t, ch)
	}
	if reqs != 2 {
		t.Fatalf("server saw %d requests, want 2", reqs)
	}
	if gotUA != provider.UserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, provider.UserAgent)
	}
	if gotSession != "conv-42" {
		t.Errorf("%s = %q, want the bound conversation id %q", provider.SessionHeader, gotSession, "conv-42")
	}
}

// TestStreamSendsConfiguredProviderHeaders covers the config-level escape hatch:
// a provider's `headers` ride on every request, and a configured User-Agent wins
// over the harness default.
func TestStreamSendsConfiguredProviderHeaders(t *testing.T) {
	var gotTenant, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant, gotUA = r.Header.Get("X-Tenant"), r.Header.Get("User-Agent")
		sseOK(w)
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name: "gateway", BaseURL: srv.URL, Model: "m", APIKey: "k",
		Headers: map[string]string{"X-Tenant": "acme", "User-Agent": "acme-agent/2"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(t, ch)
	if gotTenant != "acme" {
		t.Errorf("X-Tenant = %q, want %q", gotTenant, "acme")
	}
	if gotUA != "acme-agent/2" {
		t.Errorf("User-Agent = %q, want the configured override", gotUA)
	}
}

// TestStreamOmitsSessionHeaderOffGateways keeps every other provider's wire
// bytes unchanged: only an opencode.ai endpoint gets the routing header.
func TestStreamOmitsSessionHeaderOffGateways(t *testing.T) {
	var sawSession bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawSession = r.Header.Get(provider.SessionHeader) != ""
		sseOK(w)
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "deepseek", BaseURL: srv.URL, Model: "deepseek-v4-flash", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.(provider.SessionScoped).SetSessionID("conv-42")
	ch, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(t, ch)
	if sawSession {
		t.Errorf("%s was sent to a non-opencode endpoint", provider.SessionHeader)
	}
}

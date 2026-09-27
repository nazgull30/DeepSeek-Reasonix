package provider

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

// SessionHeader carries the per-conversation id a few gateways route on. OpenCode
// Zen/Go rejects a request that arrives without it (HTTP 400, "Request is missing
// x-opencode-session and cannot be routed efficiently") because the id is how it
// keeps one conversation on one route and reuses that conversation's prompt cache.
const SessionHeader = "x-opencode-session"

// UserAgent identifies this harness on the wire. Gateways that meter agent
// traffic (OpenCode Go) ask clients to name themselves instead of shipping Go's
// default "Go-http-client/1.1", which is indistinguishable from a generic SDK.
const UserAgent = "reasonix-agent/1.0"

// RequiresSessionHeader reports whether baseURL is an OpenCode gateway (Zen /
// Go) that rejects requests without SessionHeader. Every other provider is left
// byte-identical on the wire.
func RequiresSessionHeader(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "opencode.ai" || strings.HasSuffix(host, ".opencode.ai")
}

// NewSessionID mints an opaque conversation id for a client that has no stable
// identity yet — a headless run with no session file, a sub-agent, a one-shot
// /models probe. The controller replaces it with the real conversation id as soon
// as one exists, so the value only has to be stable, not meaningful.
func NewSessionID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand cannot realistically fail; keep the header populated anyway
		// rather than dropping back to an unroutable request.
		return "reasonix"
	}
	return hex.EncodeToString(buf[:])
}

// SetUserAgent stamps the client identity on req, leaving a User-Agent the caller
// already set in place.
func SetUserAgent(req *http.Request) {
	if req == nil || req.Header.Get("User-Agent") != "" {
		return
	}
	req.Header.Set("User-Agent", UserAgent)
}

// WireHeaders are the headers a provider client stamps on every request: the
// client identity, the provider's configured static headers, and — on gateways
// that route on it — the current conversation's session id.
//
// The session id is swapped whenever the conversation changes (a new or resumed
// session) while streams may be in flight, so it is stored atomically and every
// request reads it fresh instead of capturing it at build time.
type WireHeaders struct {
	static   map[string]string
	session  atomic.Pointer[string]
	routeKey bool // base_url is a gateway that requires SessionHeader
}

// NewWireHeaders builds the per-request header set for a client on baseURL: the
// configured static headers, plus a freshly minted session id when the endpoint
// routes on one (replaced later with the real conversation id).
func NewWireHeaders(baseURL string, static map[string]string) *WireHeaders {
	h := &WireHeaders{routeKey: RequiresSessionHeader(baseURL)}
	if len(static) > 0 {
		h.static = make(map[string]string, len(static))
		for k, v := range static {
			if k = strings.TrimSpace(k); k != "" {
				h.static[k] = v
			}
		}
	}
	if h.routeKey {
		h.SetSessionID(NewSessionID())
	}
	return h
}

// SetSessionID points subsequent requests at conversation id. An empty id clears
// it, which leaves the session header off the wire entirely.
func (h *WireHeaders) SetSessionID(id string) {
	if h == nil {
		return
	}
	h.session.Store(&id)
}

// SessionID reports the conversation id currently stamped on requests ("" when
// none is set, or when the endpoint does not route on one).
func (h *WireHeaders) SessionID() string {
	if h == nil {
		return ""
	}
	if id := h.session.Load(); id != nil {
		return *id
	}
	return ""
}

// Apply stamps req with the client identity, the configured static headers (so a
// configured User-Agent or session header wins), and finally the session header —
// last, so a gateway that requires it always gets a value even if the static map
// set an empty one.
func (h *WireHeaders) Apply(req *http.Request) {
	if h == nil || req == nil {
		return
	}
	SetUserAgent(req)
	for k, v := range h.static {
		req.Header.Set(k, v)
	}
	if h.routeKey {
		if id := h.SessionID(); id != "" {
			req.Header.Set(SessionHeader, id)
		} else {
			req.Header.Del(SessionHeader)
		}
	}
}

// SessionScoped is implemented by providers that stamp a per-conversation id on
// every request, so a gateway that routes and prompt-caches on it keeps one
// conversation on one route. The controller rebinds the id whenever the session
// changes; providers that ignore it simply don't implement this.
type SessionScoped interface {
	SetSessionID(id string)
}

// SetSessionID stamps id on p when p is session-scoped, and is a no-op otherwise —
// so callers need no type check of their own.
func SetSessionID(p Provider, id string) {
	if s, ok := p.(SessionScoped); ok {
		s.SetSessionID(id)
	}
}

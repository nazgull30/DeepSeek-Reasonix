package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/checkpoint"
	"reasonix/internal/provider"
)

func fakeMsgs() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a helpful assistant."},
		{Role: provider.RoleUser, Content: "Hello"},
		{Role: provider.RoleAssistant, Content: "Hi there!"},
		{Role: provider.RoleUser, Content: "What is Go?"},
		{Role: provider.RoleAssistant, Content: "Go is a programming language."},
	}
}

func TestBreakdownNilCheckpoints(t *testing.T) {
	a := &Agent{
		session: newSession(),
	}
	for _, m := range fakeMsgs() {
		a.session.Add(m)
	}

	b := a.Breakdown(nil, nil)
	if b == nil {
		t.Fatal("Breakdown returned nil")
	}
	if b.TotalEstimated <= 0 {
		t.Fatalf("TotalEstimated = %d, want > 0", b.TotalEstimated)
	}
	if b.SystemPromptTokens <= 0 {
		t.Fatalf("SystemPromptTokens = %d, want > 0", b.SystemPromptTokens)
	}
	if b.ConversationTokens <= 0 {
		t.Fatalf("ConversationTokens = %d, want > 0", b.ConversationTokens)
	}
	if len(b.Turns) != 0 {
		t.Fatalf("Turns with nil checkpoint = %d, want 0", len(b.Turns))
	}
}

func TestBreakdownWithCheckpoints(t *testing.T) {
	a := &Agent{
		session: newSession(),
	}
	for _, m := range fakeMsgs() {
		a.session.Add(m)
	}

	cps := checkpoint.New("", "")
	cps.Begin(0, "Hello", 1)       // msgIndex 1 = after system msg
	cps.Begin(1, "What is Go?", 4) // msgIndex 4 = after assistant resp0

	b := a.Breakdown(nil, cps)
	if b == nil {
		t.Fatal("Breakdown returned nil")
	}
	if len(b.Turns) != 2 {
		t.Fatalf("Turns = %d, want 2", len(b.Turns))
	}
	if b.Turns[0].Turn != 0 || !strings.Contains(b.Turns[0].Prompt, "Hello") {
		t.Fatalf("Turn[0] = %+v, want Turn=0 prompt=Hello", b.Turns[0])
	}
	if b.Turns[1].Turn != 1 || b.Turns[1].Files != 0 {
		t.Fatalf("Turn[1] = %+v, want Turn=1 Files=0 (no files on in-memory Begin)", b.Turns[1])
	}
}

func TestBreakdownSessionUsage(t *testing.T) {
	a := &Agent{
		session:     newSession(),
		contextWindow: 128000,
	}
	for _, m := range fakeMsgs() {
		a.session.Add(m)
	}
	a.sessionUsage = SessionUsageMeta{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		Cost:             0.42,
		Currency:         "$",
	}
	a.sessCacheHit.Store(80)
	a.sessCacheMiss.Store(20)

	b := a.Breakdown(nil, nil)
	if b.Usage.TotalTokens != 150 {
		t.Fatalf("Usage.TotalTokens = %d, want 150", b.Usage.TotalTokens)
	}
	if b.CacheHitPct != 80.0 {
		t.Fatalf("CacheHitPct = %.1f, want 80.0", b.CacheHitPct)
	}
	if b.Window != 128000 {
		t.Fatalf("Window = %d, want 128000", b.Window)
	}
}

func TestBreakdownWithSchemas(t *testing.T) {
	a := &Agent{
		session: newSession(),
	}
	for _, m := range fakeMsgs() {
		a.session.Add(m)
	}

	schemas := []provider.ToolSchema{
		{Name: "read_file", Description: "read a file", Parameters: json.RawMessage(`{"type": "object"}`)},
		{Name: "write_file", Description: "write a file", Parameters: json.RawMessage(`{"type": "object"}`)},
	}
	b := a.Breakdown(schemas, nil)
	if b.ToolSchemaTokens <= 0 {
		t.Fatalf("ToolSchemaTokens = %d, want > 0", b.ToolSchemaTokens)
	}
	if len(b.PerToolSchemas) != 2 {
		t.Fatalf("PerToolSchemas = %d, want 2", len(b.PerToolSchemas))
	}
}

func TestTurnsFromCheckpointsNil(t *testing.T) {
	got := turnsFromCheckpoints(nil, nil)
	if got != nil {
		t.Fatal("turnsFromCheckpoints(nil, nil) should be nil")
	}
}

func TestTurnsFromCheckpointsEmpty(t *testing.T) {
	got := turnsFromCheckpoints(nil, checkpoint.New("", ""))
	if got != nil {
		t.Fatal("turnsFromCheckpoints with empty store should be nil")
	}
}

func TestTurnsFromCheckpointsBoundaries(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "turn0"},
		{Role: provider.RoleAssistant, Content: "resp0"},
		{Role: provider.RoleUser, Content: "turn1"},
		{Role: provider.RoleAssistant, Content: "resp1"},
	}
	cps := checkpoint.New("", "")
	// Begin finalizes the previous checkpoint, so we need to call it for each
	// turn with the correct MsgIndex.
	cps.Begin(0, "turn0 prompt", 1)
	cps.Begin(1, "turn1 prompt", 3)

	got := turnsFromCheckpoints(msgs, cps)
	if len(got) != 2 {
		t.Fatalf("got %d turns, want 2", len(got))
	}
	if got[0].Turn != 0 || got[0].Tokens <= 0 {
		t.Fatalf("turn[0] = %+v", got[0])
	}
	if got[1].Turn != 1 || got[1].Tokens <= 0 {
		t.Fatalf("turn[1] = %+v", got[1])
	}
}

func TestTurnsFromCheckpointsTruncatesPrompt(t *testing.T) {
	long := string(make([]rune, 100))
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: long},
	}
	cps := checkpoint.New("", "")
	cps.Begin(0, long, 0)

	got := turnsFromCheckpoints(msgs, cps)
	if len(got) != 1 {
		t.Fatal("expected 1 turn")
	}
	if len([]rune(got[0].Prompt)) > 49 {
		t.Fatalf("prompt length = %d, want <= 49 (48 + ellipsis)", len([]rune(got[0].Prompt)))
	}
}

func TestTurnsFromCheckpointsBoundsCheck(t *testing.T) {
	msg := []provider.Message{{Role: provider.RoleUser, Content: "hello"}}
	cps := checkpoint.New("", "")
	cps.Begin(0, "prompt", 0)
	got := turnsFromCheckpoints(msg, cps)
	if len(got) != 1 {
		t.Fatalf("want 1 turn, got %d", len(got))
	}
}

func TestFormatBreakdownCompact(t *testing.T) {
	b := &ContextBreakdown{
		SystemPromptTokens: 50,
		ToolSchemaTokens:   200,
		ConversationTokens: 750,
		TotalEstimated:     1000,
		Usage: SessionUsageMeta{
			PromptTokens:     800,
			CompletionTokens: 200,
			TotalTokens:      1000,
			Cost:             0.05,
			Currency:         "$",
		},
		CacheHitPct: 50.0,
		Window:      128000,
		CompactPct:  5.0,
	}
	s := b.FormatBreakdown()
	if !strings.Contains(s, "Next request est. ~1.0K tokens") {
		t.Fatalf("missing next request line:\n%s", s)
	}
	if !strings.Contains(s, "System prompt:") {
		t.Fatalf("missing system prompt line:\n%s", s)
	}
	if !strings.Contains(s, "Tool schemas:") {
		t.Fatalf("missing tool schemas line:\n%s", s)
	}
	if !strings.Contains(s, "Cumulative") {
		t.Fatalf("missing cumulative line:\n%s", s)
	}
	if !strings.Contains(s, "$0.0500") {
		t.Fatalf("missing cost:\n%s", s)
	}
	if !strings.Contains(s, "Window") {
		t.Fatalf("missing window line:\n%s", s)
	}
}

func TestFormatBreakdownVerbose(t *testing.T) {
	b := &ContextBreakdown{
		SystemPromptTokens: 50,
		ToolSchemaTokens:   200,
		ConversationTokens: 750,
		TotalEstimated:     1000,
		Usage: SessionUsageMeta{
			PromptTokens:     3200,
			CompletionTokens: 800,
			TotalTokens:      4000,
			Cost:             0.42,
			Currency:         "$",
		},
		Verbose: true,
	}
	s := b.FormatBreakdown()
	if !strings.Contains(s, "Next request est. ~1000 tokens") {
		t.Fatalf("expected raw token count, got:\n%s", s)
	}
	if !strings.Contains(s, "4000") {
		t.Fatalf("expected raw 4000 in verbose:\n%s", s)
	}
}

func TestFormatBreakdownWithTurns(t *testing.T) {
	b := &ContextBreakdown{
		ConversationTokens: 300,
		TotalEstimated:     550,
		Turns: []TurnBreakdown{
			{Turn: 0, Prompt: "hello", Tokens: 100, Files: 0},
			{Turn: 1, Prompt: "show me the code", Tokens: 200, Files: 3},
		},
	}
	s := b.FormatBreakdown()
	if !strings.Contains(s, "t0") || !strings.Contains(s, "t1") {
		t.Fatalf("expected both turns in breakdown:\n%s", s)
	}
	if !strings.Contains(s, "(3 files)") {
		t.Fatalf("expected file tag:\n%s", s)
	}
}

func TestFormatBreakdownWithReasoning(t *testing.T) {
	b := &ContextBreakdown{
		Usage: SessionUsageMeta{
			CompletionTokens: 1000,
			ReasoningTokens:  400,
			TotalTokens:      2000,
		},
	}
	s := b.FormatBreakdown()
	if !strings.Contains(s, "(400 reasoning)") {
		t.Fatalf("expected reasoning annotation:\n%s", s)
	}
}

func TestFormatBreakdownWithCachedNew(t *testing.T) {
	b := &ContextBreakdown{
		Usage: SessionUsageMeta{
			CacheHitTokens:  300,
			CacheMissTokens: 700,
			PromptTokens:    1000,
			TotalTokens:     1500,
		},
		CacheHitPct: 30.0,
	}
	s := b.FormatBreakdown()
	if !strings.Contains(s, "cached") || !strings.Contains(s, "new") {
		t.Fatalf("expected cached/new split:\n%s", s)
	}
}

func TestFormatBreakdownPerToolSchemas(t *testing.T) {
	b := &ContextBreakdown{
		ToolSchemaTokens: 450,
		TotalEstimated:   1000,
		PerToolSchemas: []ToolSchemaCost{
			{Name: "read_file", Tokens: 200},
			{Name: "write_file", Tokens: 150},
			{Name: "search_code", Tokens: 100},
		},
	}
	s := b.FormatBreakdown()
	if !strings.Contains(s, "read_file") || !strings.Contains(s, "write_file") {
		t.Fatalf("expected tool names in breakdown:\n%s", s)
	}
}

func TestFormatBreakdownNoWindow(t *testing.T) {
	b := &ContextBreakdown{Usage: SessionUsageMeta{TotalTokens: 100}}
	s := b.FormatBreakdown()
	if strings.Contains(s, "Window:") {
		t.Fatalf("expected no Window line when Window=0:\n%s", s)
	}
}

func TestShortTokenCount(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{500, "500"},
		{999, "999"},
		{1000, "1.0K"},
		{1500, "1.5K"},
		{12300, "12.3K"},
		{999000, "999.0K"},
		{999949, "999.9K"},
		{999950, "1.0M"},
		{1000000, "1.0M"},
		{2500000, "2.5M"},
	}
	for _, tc := range tests {
		got := shortTokenCount(tc.n)
		if got != tc.want {
			t.Errorf("shortTokenCount(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestRawTokenCount(t *testing.T) {
	if got := rawTokenCount(12345); got != "12345" {
		t.Fatalf("rawTokenCount = %q, want 12345", got)
	}
}

func TestPct(t *testing.T) {
	if got := pct(0, 0); got != "0%" {
		t.Fatalf("pct(0,0) = %q, want 0%%", got)
	}
	if got := pct(25, 100); got != "25%" {
		t.Fatalf("pct(25,100) = %q, want 25%%", got)
	}
	if got := pct(1, 3); got != "33%" {
		t.Fatalf("pct(1,3) = %q, want 33%%", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Fatalf("truncate = %q, want short", got)
	}
	if got := truncate("this is a long string", 10); got != "this is a …" {
		t.Fatalf("truncate = %q, want this is a …", got)
	}
	// Multi-byte runes.
	if got := truncate("你好世界", 2); got != "你好…" {
		t.Fatalf("truncate CJK = %q, want 你好…", got)
	}
}

func TestPlural(t *testing.T) {
	if plural(0) != "s" {
		t.Fatalf(`plural(0) = %q, want "s"`, plural(0))
	}
	if plural(1) != "" {
		t.Fatalf(`plural(1) = %q, want ""`, plural(1))
	}
	if plural(2) != "s" {
		t.Fatalf(`plural(2) = %q, want "s"`, plural(2))
	}
}

func TestBreakdownWindowFallback(t *testing.T) {
	a := &Agent{
		session:     newSession(),
		contextWindow: 100000,
	}
	for _, m := range fakeMsgs() {
		a.session.Add(m)
	}

	b := a.Breakdown(nil, nil)
	if b.CompactPct <= 0 {
		t.Fatalf("CompactPct = %.2f, want > 0 (fallback from TotalEstimated)", b.CompactPct)
	}
}

func TestBreakdownWindowZero(t *testing.T) {
	a := &Agent{
		session: newSession(),
	}
	for _, m := range fakeMsgs() {
		a.session.Add(m)
	}

	b := a.Breakdown(nil, nil)
	if b.CompactPct != 0 {
		t.Fatalf("CompactPct = %.2f, want 0 when Window=0", b.CompactPct)
	}
}

func mockMsg(turn int, content string) checkpoint.Meta {
	return checkpoint.Meta{Turn: turn, Prompt: content}
}

func TestTurnsFromCheckpointsEmptySegment(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "a"},
		{Role: provider.RoleAssistant, Content: "b"},
		{Role: provider.RoleUser, Content: "c"},
	}
	cps := checkpoint.New("", "")
	cps.Begin(0, "turn0", 0)
	cps.Begin(1, "turn1", 2)

	got := turnsFromCheckpoints(msgs, cps)
	if len(got) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(got))
	}
}

// newSession is a test helper that returns an empty session.
func newSession() *Session {
	return NewSession("")
}

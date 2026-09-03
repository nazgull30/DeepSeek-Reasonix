package cli

import (
	"testing"

	"reasonix/internal/command"
)

func TestChatCommandNames(t *testing.T) {
	m := chatTUI{commands: []command.Command{{Name: "review"}, {Name: "git:commit"}}}
	if got := m.commandNames(); got != "/review · /git:commit" {
		t.Errorf("commandNames = %q", got)
	}

	if got := (&chatTUI{}).commandNames(); got != "" {
		t.Errorf("empty commandNames = %q, want \"\"", got)
	}
}

// TestParseSubtaskKeyedValue verifies the /subtask key=value prefix parser splits
// the value from the trailing description and trims surrounding whitespace.
func TestParseSubtaskKeyedValue(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantRest string
		wantVal  string
	}{
		{"value then description", "sa_123 fix the bugs", "fix the bugs", "sa_123"},
		{"value then tab", "sa_123\tcontinue", "continue", "sa_123"},
		{"leading space stripped", "  sa_456  next", "next", "sa_456"},
		{"value with dash chars", "sa_20260903_120000_000000000_aabbccdd verify", "verify", "sa_20260903_120000_000000000_aabbccdd"},
		{"value is whole input", "sa_123", "", "sa_123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest, val := parseSubtaskKeyedValue(tt.in)
			if rest != tt.wantRest || val != tt.wantVal {
				t.Errorf("parseSubtaskKeyedValue(%q) = (rest %q, val %q), want (rest %q, val %q)",
					tt.in, rest, val, tt.wantRest, tt.wantVal)
			}
		})
	}
}

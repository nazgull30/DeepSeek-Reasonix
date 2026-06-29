package agent

import (
	"strings"

	"reasonix/internal/provider"
)

// ForkPlaceholderTag is injected into fork children as the first user message
// prefix to enable cache sharing across fork children and to act as a recursion
// guard. All fork children from the same parent state share an identical message
// prefix up to (and including) this tag; only the task directive that follows in
// the next user message differs.
const ForkPlaceholderTag = "<fork-boilerplate>"

// IsForkChild checks whether the given messages contain the fork boilerplate tag,
// meaning the agent is already running inside a fork child. Used as a guard
// against recursive forking — a subagent that was itself created via fork cannot
// create another fork child (it should run the work directly instead).
func IsForkChild(msgs []provider.Message) bool {
	for _, m := range msgs {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, ForkPlaceholderTag) {
			return true
		}
	}
	return false
}

// lastCompleteRound returns the index of the first message in the last complete
// API round (an assistant message with tool_calls followed by its tool results).
// If no complete round exists, returns 0. Messages before this index form a
// byte-stable prefix for fork children.
func lastCompleteRound(msgs []provider.Message) int {
	lastComplete := 0
	for i := 0; i < len(msgs); {
		m := msgs[i]
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 {
			// Walk past tool results
			j := i + 1
			for j < len(msgs) && msgs[j].Role == provider.RoleTool {
				j++
			}
			// Only count as complete if every call has a result
			if len(m.ToolCalls) == j-i-1 {
				lastComplete = j
			}
			i = j
			continue
		}
		i++
	}
	return lastComplete
}

// BuildForkSession creates a new session that inherits all complete message
// rounds from the parent, enabling prompt cache sharing across multiple fork
// children. The returned session contains:
//
//   - All complete parent rounds (system → user → assistant with results → ...)
//     up to the last fully answered assistant message.
//   - A fork-guard user message containing ForkPlaceholderTag so IsForkChild can
//     detect recursive forking.
//
// The caller (sub.Run) will append the task prompt as a subsequent user message.
// Multiple children forked from the same parent state produce an identical prefix
// up to (and including) the fork-guard message; only the task directive differs.
//
// When parentMsgs is empty or contains only a system prompt, a fresh empty
// session is returned so the caller falls back to the standard (non-fork) path.
func BuildForkSession(parentMsgs []provider.Message) *Session {
	if len(parentMsgs) <= 1 {
		// Nothing useful to inherit — caller should use a fresh session.
		return NewSession("")
	}

	// Use all messages except any in-flight assistant-with-calls round.
	boundary := lastCompleteRound(parentMsgs)
	if boundary == 0 && len(parentMsgs) > 0 {
		// No complete rounds; use messages up to the last stable boundary.
		// This handles the case where the parent just started its first turn.
		boundary = 1 // keep system prompt
	}

	msgs := make([]provider.Message, 0, boundary+1)
	msgs = append(msgs, parentMsgs[:boundary]...)

	// Inject the fork-guard user message. This serves double duty:
	//   1. All children from this point share this message in their prefix,
	//      enabling server-side cache sharing.
	//   2. IsForkChild detects it to prevent recursive forking.
	msgs = append(msgs, provider.Message{
		Role:    provider.RoleUser,
		Content: ForkPlaceholderTag,
	})

	return &Session{Messages: msgs}
}

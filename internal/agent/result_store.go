package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"reasonix/internal/fileutil"
)

// maxToolResultPreviewChars is the maximum size of a tool result that is kept
// verbatim in the session. Results larger than this are persisted to disk and
// replaced with a preview stub, except for tools that return source code
// (CodeGraph tools, read_file) which are always kept inline regardless of size.
// This is smaller than maxToolOutputBytes (32KB) because multiple tool results
// per turn add up quickly; 8KB is roughly 2K tokens, enough for a meaningful
// preview.
const maxToolResultPreviewChars = 8 * 1024

// toolResultStorageDir is the subdirectory under the archive dir where
// persisted tool results are stored.
const toolResultStorageDir = "tool-results"

// ContentReplacementState tracks which tool results have been persisted to disk
// and what replacement text was used. The state is frozen: once a result's fate
// is decided (kept verbatim or replaced with a preview), the same decision is
// returned for every subsequent access. This guarantees byte-identical message
// prefixes across fork children, preserving the server-side prompt cache.
//
// The state does not need to be persisted across sessions because saved sessions
// already contain the final (replaced) tool result text. It is a run-time
// optimization that operates on raw tool output before it enters the session.
type ContentReplacementState struct {
	mu        sync.Mutex
	replaced  map[string]string // key: callID + "\x00" + contentHash → replacement text
	outputDir string            // base directory for persisted files
}

// NewContentReplacementState creates a replacement state that persists overflow
// tool results under archiveDir/toolResultStorageDir/.
func NewContentReplacementState(archiveDir string) *ContentReplacementState {
	outDir := filepath.Join(archiveDir, toolResultStorageDir)
	return &ContentReplacementState{
		replaced:  make(map[string]string),
		outputDir: outDir,
	}
}

// MaybeReplace checks whether a tool result exceeds the inline size budget and,
// if so, persists it to disk and returns a preview stub. The decision is frozen:
// subsequent calls with the same (callID, content) always return the same result.
//
// When the result fits within the budget, it is returned unchanged but still
// recorded so future access with identical bytes is a no-op (cache stability).
func (s *ContentReplacementState) MaybeReplace(callID, content, toolName string) (string, bool) {
	if s == nil {
		return content, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key := replacementKey(callID, content)

	// Already decided — return the frozen replacement.
	if replacement, ok := s.replaced[key]; ok {
		if replacement == content {
			return content, false
		}
		return replacement, true
	}

	// Source-code tools always stay inline regardless of size so the model
	// gets the full result without needing a second read from the archive.
	if isSourceCodeTool(toolName) {
		s.replaced[key] = content
		return content, false
	}

	// Within budget — keep verbatim and record the decision.
	if len(content) <= maxToolResultPreviewChars {
		s.replaced[key] = content
		return content, false
	}

	// Over budget — persist to disk and substitute with a preview.
	path, err := s.persist(callID, content, toolName)
	if err != nil {
		// Persist failed — keep as-is rather than silently dropping data.
		s.replaced[key] = content
		return content, false
	}
	preview := buildToolResultPreview(content, path, toolName)
	s.replaced[key] = preview
	return preview, true
}

// Clone returns an independent copy of the replacement state, preserving every
// frozen decision. Fork children use this to inherit the parent's replacements
// so their message prefix stays byte-identical.
func (s *ContentReplacementState) Clone() *ContentReplacementState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := &ContentReplacementState{
		replaced:  make(map[string]string, len(s.replaced)),
		outputDir: s.outputDir,
	}
	for k, v := range s.replaced {
		clone.replaced[k] = v
	}
	return clone
}

// persist writes the full tool result to a file and returns the file path.
func (s *ContentReplacementState) persist(callID, content, toolName string) (string, error) {
	if err := os.MkdirAll(s.outputDir, 0o755); err != nil {
		return "", err
	}
	hash := sha256Hex([]byte(content))
	name := fmt.Sprintf("%s-%s-%s.txt", sanitizeToolName(toolName), trimCallID(callID), hash[:12])
	path := filepath.Join(s.outputDir, name)
	tmp, err := os.CreateTemp(s.outputDir, ".tool-result-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := fileutil.ReplaceFile(tmpPath, path); err != nil {
		return "", err
	}
	return path, nil
}

// replacementKey builds a deterministic key from callID and content so the same
// tool result always produces the same key (and thus the same frozen decision).
func replacementKey(callID, content string) string {
	return callID + "\x00" + sha256Hex([]byte(content))
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// isSourceCodeTool returns true for tools whose output is source code that
// should never be archived — the model needs the full output inline to avoid
// an extra round-trip reading the archived file.
func isSourceCodeTool(name string) bool {
	return strings.Contains(name, "mcp__codegraph__") || name == "read_file"
}

func sanitizeToolName(name string) string {
	s := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
	return strings.Trim(s, "-_")
}

func trimCallID(id string) string {
	if len(id) > 16 {
		return id[:16]
	}
	return id
}

// buildToolResultPreview constructs the preview stub that replaces a persisted
// tool result in the session. It includes a brief location header and the first
// N bytes of the original output.
func buildToolResultPreview(content, path, toolName string) string {
	previewLen := 500
	if len(content) < previewLen {
		previewLen = len(content)
	}
	truncSuffix := ""
	if previewLen < len(content) {
		truncSuffix = "\n… (truncated)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<persisted-output>\n")
	fmt.Fprintf(&b, "Output too large (%.1f KB). Full output saved to: %s\n", float64(len(content))/1024, path)
	fmt.Fprintf(&b, "Tool: %s\n", toolName)
	if previewLen > 0 {
		fmt.Fprintf(&b, "\nPreview (first %d bytes):\n", previewLen)
		b.WriteString(content[:previewLen])
		b.WriteString(truncSuffix)
	}
	b.WriteString("\n</persisted-output>\n")
	return b.String()
}

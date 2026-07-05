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

const maxToolResultPreviewChars = 8 * 1024

const toolResultStorageDir = "tool-results"

type ContentReplacementState struct {
	mu        sync.Mutex
	replaced  map[string]string
	outputDir string
}

func NewContentReplacementState(archiveDir string) *ContentReplacementState {
	outDir := filepath.Join(archiveDir, toolResultStorageDir)
	return &ContentReplacementState{
		replaced:  make(map[string]string),
		outputDir: outDir,
	}
}

func (s *ContentReplacementState) MaybeReplace(callID, content, toolName string) (string, bool) {
	if s == nil {
		return content, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key := replacementKey(callID, content)

	if replacement, ok := s.replaced[key]; ok {
		if replacement == content {
			return content, false
		}
		return replacement, true
	}

	if isSourceCodeTool(toolName) {
		s.replaced[key] = content
		return content, false
	}

	if len(content) <= maxToolResultPreviewChars {
		s.replaced[key] = content
		return content, false
	}

	path, err := s.persist(callID, content, toolName)
	if err != nil {
		s.replaced[key] = content
		return content, false
	}
	preview := buildToolResultPreview(content, path, toolName)
	s.replaced[key] = preview
	return preview, true
}

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

func replacementKey(callID, content string) string {
	return callID + "\x00" + sha256Hex([]byte(content))
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

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

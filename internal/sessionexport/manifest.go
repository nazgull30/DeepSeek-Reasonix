package sessionexport

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/fileutil"
)

// Manifest is the machine-readable index of an exported session snapshot. It
// carries structured ids, models, turn counts and cumulative usage so a consumer
// (human, script, or a future Reasonix tool) can answer "which session, how much
// context, how cache-friendly" without decoding every transcript.
type Manifest struct {
	// ExportedAt is when the snapshot was taken.
	ExportedAt time.Time `json:"exportedAt"`
	// WorkspaceRoot is the workspace the snapshot was taken from.
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	// Main is the active session slot.
	Main MainEntry `json:"main"`
	// Subagents are the exported sub-agent lineage.
	Subagents []SubagentEntry `json:"subagents,omitempty"`
	// Sessions are the extra named sessions (e.g. orchestrator agents).
	Sessions []SessionEntry `json:"sessions,omitempty"`
	// ArchiveFiles are the exported compacted-history files (base names).
	ArchiveFiles []string `json:"archiveFiles,omitempty"`
	// ToolResults are the exported persisted tool-result files (base names).
	ToolResults []string `json:"toolResults,omitempty"`
	// Totals aggregates cumulative usage across every exported session.
	Totals Totals `json:"totals"`
}

// MainEntry describes the exported active session slot.
type MainEntry struct {
	ID      string                  `json:"id,omitempty"`
	File    string                  `json:"file,omitempty"`
	Model   string                  `json:"model,omitempty"`
	Turns   int                     `json:"turns,omitempty"`
	Preview string                  `json:"preview,omitempty"`
	Usage   *agent.SessionUsageMeta `json:"usage,omitempty"`
}

// SessionEntry describes one exported named/extra session.
type SessionEntry struct {
	Name    string                  `json:"name,omitempty"`
	ID      string                  `json:"id,omitempty"`
	File    string                  `json:"file,omitempty"`
	Model   string                  `json:"model,omitempty"`
	Turns   int                     `json:"turns,omitempty"`
	Preview string                  `json:"preview,omitempty"`
	Usage   *agent.SessionUsageMeta `json:"usage,omitempty"`
}

// SubagentEntry describes one exported sub-agent transcript.
type SubagentEntry struct {
	Ref     string                  `json:"ref"`
	File    string                  `json:"file"`
	Meta    string                  `json:"meta"`
	Name    string                  `json:"name,omitempty"`
	Kind    string                  `json:"kind,omitempty"`
	Status  string                  `json:"status,omitempty"`
	Model   string                  `json:"model,omitempty"`
	Turns   int                     `json:"turns,omitempty"`
	Preview string                  `json:"preview,omitempty"`
	Usage   *agent.SessionUsageMeta `json:"usage,omitempty"`
}

// Totals aggregates cumulative usage across an export.
type Totals struct {
	// Sessions is the count of exported main + extra named sessions.
	Sessions int `json:"sessions"`
	// Subagents is the count of exported sub-agent transcripts.
	Subagents        int     `json:"subagents"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	CacheHitTokens   int     `json:"cacheHitTokens"`
	CacheMissTokens  int     `json:"cacheMissTokens"`
	ReasoningTokens  int     `json:"reasoningTokens"`
	TotalTokens      int     `json:"totalTokens"`
	Cost             float64 `json:"cost"`
	Currency         string  `json:"currency,omitempty"`
}

// BuildManifest produces the export manifest from Options + Result.
func BuildManifest(opts Options, res *Result) Manifest {
	return Manifest{
		ExportedAt:    time.Now().UTC(),
		WorkspaceRoot: opts.WorkspaceRoot,
		Main:          res.Main,
		Subagents:     res.Subagents,
		Sessions:      res.Sessions,
		ArchiveFiles:  res.ArchiveFiles,
		ToolResults:   res.ToolResults,
		Totals:        res.Totals,
	}
}

// writeManifest marshals and atomically writes the manifest.
func writeManifest(path string, m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("sessionexport: encode manifest: %w", err)
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return fileutil.ReplaceFile(tmpPath, path)
}

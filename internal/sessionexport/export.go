// Package sessionexport snapshots the persisted session state of a Reasonix
// working session — the main transcript, its sub-agent lineage, orchestrator
// agent transcripts, and the compaction archive — into a self-contained
// directory for offline inspection and analysis. The export is read-only: live
// session files are copied (never moved), so an export can be shared or
// discarded without touching the original sessions.
//
// The exported tree carries enough structure to rebuild the working context and
// to answer "where does my prompt go?" questions:
//
//	<dest>/
//	  manifest.json   — session ids, models, turn counts, cumulative usage
//	  summary.md      — a human-readable cache/token report (see summary.go)
//	  main/           — the active session transcript + metadata + checkpoints
//	  subagents/      — transcripts and metadata of the sub-agent lineage
//	  sessions/       — extra named sessions (e.g. orchestrator agents)
//	  archive/        — compacted/pruned history (config.ArchiveDir)
//	  tool-results/   — persisted oversized tool outputs
package sessionexport

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/fileutil"
)

// NamedSession is a persisted session transcript (with optional sidecars) to
// copy by path — e.g. an orchestrator agent's orchestrator_<name>.jsonl.
type NamedSession struct {
	// Name is the display name used in the manifest and summary, e.g. the
	// orchestrator agent name.
	Name string
	// Path is the absolute path to the .jsonl transcript. Missing files are
	// skipped, so callers may pass deterministic paths unconditionally.
	Path string
}

// Options configures an export.
type Options struct {
	// WorkspaceRoot pins the export destination to
	// <root>/.reasonix/session-export/<timestamp>/. Empty falls back to the
	// current working directory.
	WorkspaceRoot string
	// SessionDir is the sessions directory that holds the main transcript and
	// the subagents/ tree (config.SessionDir()). Empty disables lineage lookup.
	SessionDir string
	// MainSession is the active session's .jsonl path. When empty, the main
	// slot (and its sub-agent lineage) is skipped.
	MainSession string
	// Sessions lists additional persisted transcripts to include (orchestrator
	// agents, resumed stores, etc.). Missing files are skipped.
	Sessions []NamedSession
	// Archive is the path of the compaction archive (config.ArchiveDir()).
	// When empty, archived history and tool results are skipped.
	Archive string
}

// Result describes a completed export.
type Result struct {
	// Dir is the export directory, rooted under
	// <WorkspaceRoot>/.reasonix/session-export/<timestamp>/.
	Dir string
	// Main is the exported main session slot (zero when no main session).
	Main MainEntry
	// Subagents are the exported sub-agent lineage.
	Subagents []SubagentEntry
	// Sessions are the exported extra named sessions.
	Sessions []SessionEntry
	// ArchiveFiles are the exported compacted-history files (base names).
	ArchiveFiles []string
	// ToolResults are the exported tool-result files (base names).
	ToolResults []string
	// Totals aggregates cumulative usage across every exported session.
	Totals Totals
}

// Export copies the configured session state into a fresh timestamped
// directory and writes manifest.json + summary.md alongside it.
func Export(opts Options) (*Result, error) {
	dest, err := newDestDir(opts.WorkspaceRoot)
	if err != nil {
		return nil, err
	}

	res := &Result{Dir: dest}
	dirs := map[string]string{
		"main":         filepath.Join(dest, "main"),
		"subagents":    filepath.Join(dest, "subagents"),
		"sessions":     filepath.Join(dest, "sessions"),
		"archive":      filepath.Join(dest, "archive"),
		"tool-results": filepath.Join(dest, "tool-results"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("sessionexport: create %s: %w", d, err)
		}
	}

	// Active main session: transcript + metadata sidecar + checkpoint dir.
	// An empty session may have no on-disk transcript yet; treat that as empty
	// rather than a hard failure.
	if opts.MainSession != "" {
		if _, err := os.Stat(opts.MainSession); err == nil {
			entry, err := copySession(dirs["main"], opts.MainSession)
			if err != nil {
				return nil, fmt.Errorf("sessionexport: export main session: %w", err)
			}
			res.Main = entry
		}
	}

	// Sub-agent lineage: direct children of the main session's branch. The task
	// tool is unavailable inside sub-agents, so the lineage is exactly one level
	// deep; workflow/spawned subtasks inherit the main branch id.
	if opts.MainSession != "" {
		parent := agent.BranchID(opts.MainSession)
		artifacts, err := agent.ListSubagentsByParent(opts.SessionDir, parent)
		if err != nil {
			return nil, fmt.Errorf("sessionexport: list subagents: %w", err)
		}
		sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Ref < artifacts[j].Ref })
		seen := map[string]bool{}
		for _, a := range artifacts {
			if seen[a.Ref] {
				continue
			}
			seen[a.Ref] = true
			entry, err := copySubagent(dirs["subagents"], a)
			if err != nil {
				return nil, fmt.Errorf("sessionexport: export subagent %s: %w", a.Ref, err)
			}
			res.Subagents = append(res.Subagents, entry)
		}
	}

	// Extra named sessions (orchestrator agents, …). Missing files are skipped.
	for _, s := range opts.Sessions {
		if strings.TrimSpace(s.Path) == "" {
			continue
		}
		if _, err := os.Stat(s.Path); err != nil {
			continue
		}
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = filepath.Base(s.Path)
		}
		entry, err := copySession(dirs["sessions"], s.Path)
		if err != nil {
			return nil, fmt.Errorf("sessionexport: export session %s: %w", name, err)
		}
		res.Sessions = append(res.Sessions, SessionEntry{
			Name:    name,
			ID:      entry.ID,
			File:    entry.File,
			Model:   entry.Model,
			Turns:   entry.Turns,
			Preview: entry.Preview,
			Usage:   entry.Usage,
		})
	}

	// Compaction archive + persisted tool results.
	if opts.Archive != "" {
		files, err := copyJSONLDir(dirs["archive"], opts.Archive)
		if err != nil {
			return nil, fmt.Errorf("sessionexport: export archive: %w", err)
		}
		res.ArchiveFiles = files
		toolResults, err := copyDirFlat(dirs["tool-results"], filepath.Join(opts.Archive, "tool-results"))
		if err != nil {
			return nil, fmt.Errorf("sessionexport: export tool results: %w", err)
		}
		res.ToolResults = toolResults
	}

	res.Totals = aggregateTotals(res)

	// Write manifest.json + summary.md.
	manifest := BuildManifest(opts, res)
	if err := writeManifest(filepath.Join(dest, "manifest.json"), manifest); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dest, "summary.md"), []byte(BuildSummary(manifest)), 0o644); err != nil {
		return nil, fmt.Errorf("sessionexport: write summary: %w", err)
	}

	return res, nil
}

// newDestDir creates and returns the timestamped export directory under
// <WorkspaceRoot>/.reasonix/session-export/ (or <cwd>/.reasonix/session-export/
// when WorkspaceRoot is empty).
func newDestDir(workspaceRoot string) (string, error) {
	base := strings.TrimSpace(workspaceRoot)
	if base == "" {
		var err error
		base, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	ts := time.Now().Format("20060102-150405")
	dest := filepath.Join(base, ".reasonix", "session-export", ts)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("sessionexport: create %s: %w", dest, err)
	}
	return dest, nil
}

// copySession copies a session transcript and its sidecars — the metadata file
// (<name>.meta), the checkpoint directory (<name>.ckpt/), and any persisted
// goal state (<name>.goal-state.json) — into dst and returns the manifest entry.
func copySession(dst, src string) (MainEntry, error) {
	rel := filepath.Base(src)
	if err := copyFile(filepath.Join(dst, rel), src); err != nil {
		return MainEntry{}, err
	}

	entry := MainEntry{File: rel}
	if meta, ok, err := agent.LoadBranchMeta(src); err == nil && ok {
		entry.ID = meta.ID
		entry.Model = meta.Model
		if meta.SessionUsage != nil {
			entry.Usage = meta.SessionUsage
		}
	}
	if entry.ID == "" {
		entry.ID = agent.BranchID(src)
	}

	// Metadata sidecar.
	metaPath := agent.BranchMetaPath(src)
	if err := copyFile(filepath.Join(dst, filepath.Base(metaPath)), metaPath); err != nil && !os.IsNotExist(err) {
		return MainEntry{}, err
	}

	// Checkpoint directory (best-effort: not every session has one).
	if err := copyDir(filepath.Join(dst, strings.TrimSuffix(rel, ".jsonl")+".ckpt"), strings.TrimSuffix(src, ".jsonl")+".ckpt"); err != nil && !os.IsNotExist(err) {
		return MainEntry{}, err
	}

	if preview, turns := agent.SessionPreview(src); turns > 0 {
		entry.Turns = turns
		entry.Preview = preview
	}
	return entry, nil
}

// copySubagent copies a persisted sub-agent transcript and metadata file into
// dst and builds the manifest entry.
func copySubagent(dst string, a agent.SubagentArtifact) (SubagentEntry, error) {
	entry := SubagentEntry{
		Ref:    a.Ref,
		File:   a.Ref + ".jsonl",
		Meta:   a.Ref + ".meta.json",
		Name:   a.Meta.Name,
		Kind:   a.Meta.Kind,
		Status: string(a.Meta.Status),
		Model:  a.Meta.Model,
	}
	if a.Meta.Usage != nil {
		entry.Usage = a.Meta.Usage
	}
	if err := copyFile(filepath.Join(dst, entry.File), a.SessionPath); err != nil {
		return SubagentEntry{}, err
	}
	if err := copyFile(filepath.Join(dst, entry.Meta), a.MetaPath); err != nil {
		return SubagentEntry{}, err
	}
	if preview, turns := agent.SessionPreview(a.SessionPath); turns > 0 {
		entry.Turns = turns
		entry.Preview = preview
	}
	return entry, nil
}

// copyJSONLDir copies every *.jsonl in src into dst and returns the copied base
// names, sorted. Missing src is a no-op.
func copyJSONLDir(dst, src string) ([]string, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		if err := copyFile(filepath.Join(dst, e.Name()), filepath.Join(src, e.Name())); err != nil {
			return nil, err
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)
	return files, nil
}

// copyDirFlat copies every file in src into dst (non-recursively) and returns
// the copied base names, sorted. Missing src is a no-op.
func copyDirFlat(dst, src string) ([]string, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(dst, e.Name()), filepath.Join(src, e.Name())); err != nil {
			return nil, err
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)
	return files, nil
}

// aggregateTotals sums cumulative usage across the main session, extra sessions
// and sub-agents.
func aggregateTotals(res *Result) Totals {
	var t Totals
	add := func(u *agent.SessionUsageMeta) {
		if u == nil {
			return
		}
		t.PromptTokens += u.PromptTokens
		t.CompletionTokens += u.CompletionTokens
		t.CacheHitTokens += u.CacheHitTokens
		t.CacheMissTokens += u.CacheMissTokens
		t.ReasoningTokens += u.ReasoningTokens
		t.TotalTokens += u.TotalTokens
		t.Cost += u.Cost
		if u.Currency != "" {
			t.Currency = u.Currency
		}
	}
	add(res.Main.Usage)
	for i := range res.Sessions {
		add(res.Sessions[i].Usage)
	}
	for i := range res.Subagents {
		add(res.Subagents[i].Usage)
	}
	t.Sessions = 1
	if res.Main.ID == "" {
		t.Sessions = 0
	}
	t.Sessions += len(res.Sessions)
	t.Subagents = len(res.Subagents)
	return t
}

// copyFile copies a file crash-safely: it writes to a sibling tmp then atomically
// renames over dst, so an interrupted export never leaves a partial transcript.
func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".copy-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := fileutil.ReplaceFile(tmpPath, dst); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func copyDir(dst, src string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", src)
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(target, path)
	})
}

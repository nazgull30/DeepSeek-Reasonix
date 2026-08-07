package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/agent"
)

// cliSessionRemoval is the Controller.SessionRemoval hook installed by the CLI:
// clearing a session moves it into the session dir's .trash instead of deleting
// it, so /clear is recoverable via /trash + /restore.
func cliSessionRemoval(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := agent.TrashSessionFiles(dir, path); err != nil {
		return err
	}
	return nil
}

// cliReconcileCleanupPending is the boot CleanupPendingReconciler for the CLI.
// Leftover delayed-cleanup markers (a previous process crashed mid-teardown) are
// moved to the trash rather than hard-deleted, matching /clear's semantics.
func cliReconcileCleanupPending(dir string) error {
	return agent.ReconcileCleanupPending(dir, func(item agent.CleanupPendingInfo) error {
		return agent.MoveSessionFilesToTrash(dir, item.SessionPath)
	})
}

// trashedSessionEntry is one recoverable session shown by /trash.
type trashedSessionEntry struct {
	Index int
	Path  string
	Key   string
	Title string
	At    time.Time
}

// listTrashedSessions enumerates the session dir's trash, newest deletion first.
func listTrashedSessions(sessionDir string) ([]trashedSessionEntry, error) {
	paths, err := agent.ListTrashedSessions(sessionDir)
	if err != nil {
		return nil, err
	}
	entries := make([]trashedSessionEntry, 0, len(paths))
	for i, path := range paths {
		key := filepath.Base(path)
		title := sessionPreview(path)
		deletedAt := agent.TrashedSessionDeletedAt(path)
		entries = append(entries, trashedSessionEntry{
			Index: i + 1,
			Path:  path,
			Key:   key,
			Title: title,
			At:    time.UnixMilli(deletedAt),
		})
	}
	return entries, nil
}

// sessionPreview returns a short human title for a transcript file by reading
// its first user message.
func sessionPreview(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, `"role":"user"`) && !strings.Contains(line, `"role": "user"`) {
			continue
		}
		// Best-effort: extract the content field of the first user message.
		start := strings.Index(line, `"content":`)
		if start < 0 {
			continue
		}
		rest := line[start+len(`"content":`):]
		rest = strings.TrimSpace(rest)
		rest = strings.Trim(rest, `"`)
		rest = strings.ReplaceAll(rest, `\n`, " ")
		rest = strings.TrimSpace(rest)
		if rest == "" {
			continue
		}
		if len(rest) > 80 {
			rest = rest[:77] + "..."
		}
		return rest
	}
	return ""
}

func (m chatTUI) runTrashCommand(input string) {
	m.echoLocalCommand(input)
	entries, err := listTrashedSessions(m.ctrl.SessionDir())
	if err != nil {
		m.notice("trash: " + err.Error())
		return
	}
	if len(entries) == 0 {
		m.notice("trash is empty")
		return
	}
	m.commitLine(dim("  ── trash ──"))
	for _, e := range entries {
		line := fmt.Sprintf("  %d. %s", e.Index, e.Key)
		if e.Title != "" {
			line += "  ·  " + e.Title
		}
		if !e.At.IsZero() {
			line += "  ·  " + e.At.Local().Format("2006-01-02 15:04")
		}
		m.commitLine(line)
	}
	m.notice("use /restore <n> to recover, /purge <n> or /trash-empty to delete permanently")
}

func (m chatTUI) runRestoreCommand(input string) {
	m.echoLocalCommand(input)
	rest := strings.TrimSpace(strings.TrimPrefix(input, "/restore"))
	if rest == "" {
		m.notice("usage: /restore <n>")
		return
	}
	n, err := parseIndex(rest)
	if err != nil {
		m.notice("restore: " + err.Error())
		return
	}
	entries, err := listTrashedSessions(m.ctrl.SessionDir())
	if err != nil {
		m.notice("restore: " + err.Error())
		return
	}
	if n < 1 || n > len(entries) {
		m.notice(fmt.Sprintf("restore: no trashed session %d (1-%d)", n, len(entries)))
		return
	}
	e := entries[n-1]
	if err := agent.RestoreTrashedSession(m.ctrl.SessionDir(), e.Path); err != nil {
		m.notice("restore: " + err.Error())
		return
	}
	m.notice(fmt.Sprintf("restored %s", e.Key))
}

func (m chatTUI) runPurgeCommand(input string) {
	m.echoLocalCommand(input)
	rest := strings.TrimSpace(strings.TrimPrefix(input, "/purge"))
	if rest == "" {
		m.notice("usage: /purge <n>")
		return
	}
	n, err := parseIndex(rest)
	if err != nil {
		m.notice("purge: " + err.Error())
		return
	}
	entries, err := listTrashedSessions(m.ctrl.SessionDir())
	if err != nil {
		m.notice("purge: " + err.Error())
		return
	}
	if n < 1 || n > len(entries) {
		m.notice(fmt.Sprintf("purge: no trashed session %d (1-%d)", n, len(entries)))
		return
	}
	e := entries[n-1]
	if err := agent.PurgeTrashedSession(m.ctrl.SessionDir(), e.Path); err != nil {
		m.notice("purge: " + err.Error())
		return
	}
	m.notice(fmt.Sprintf("permanently deleted %s", e.Key))
}

func (m chatTUI) runTrashEmptyCommand(input string) {
	m.echoLocalCommand(input)
	entries, err := listTrashedSessions(m.ctrl.SessionDir())
	if err != nil {
		m.notice("trash-empty: " + err.Error())
		return
	}
	for _, e := range entries {
		if err := agent.PurgeTrashedSession(m.ctrl.SessionDir(), e.Path); err != nil {
			m.notice("trash-empty: " + err.Error())
			return
		}
	}
	m.notice(fmt.Sprintf("trash emptied (%d session(s))", len(entries)))
}

func parseIndex(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err != nil || n < 1 {
		return 0, fmt.Errorf("expected a positive index, got %q", strings.TrimSpace(s))
	}
	return n, nil
}

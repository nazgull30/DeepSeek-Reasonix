package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/i18n"
)

// runSessionDeleteCommand handles "/session-delete": with no argument it lists
// the most recent saved sessions and opens an interactive picker; with an index
// it targets that session directly. Either way the destructive step only runs
// after an explicit confirmation. Orchestration child sessions
// (orchestrator_<name>.jsonl) are deleted by resetting the child controller and
// purging its artifacts, so the next SaveSessions does not resurrect them.
func (m *chatTUI) runSessionDeleteCommand(input string) {
	sessions := recentSessions(m.ctrl.SessionDir())
	if len(sessions) == 0 {
		m.notice(i18n.M.NoSessionToDelete)
		return
	}

	args := tokenizeArgs(input) // args[0] == "/session-delete"
	if len(args) < 2 {
		m.showSessions(sessions) // write list to scrollback (above input)
		m.openSessionDeletePicker()
		return
	}
	idx, err := strconv.Atoi(strings.TrimSpace(args[1]))
	if err != nil || idx < 1 || idx > len(sessions) {
		m.notice(fmt.Sprintf(i18n.M.ResumeBadIndexFmt, len(sessions)))
		return
	}
	m.promptSessionDelete(sessions[idx-1])
}

// promptSessionDelete moves a single confirmed selection into the destructive
// confirmation overlay, refusing the currently-active session up front.
func (m *chatTUI) promptSessionDelete(s agent.SessionInfo) {
	if s.Path == m.ctrl.SessionPath() {
		m.notice(i18n.M.SessionDeleteActive)
		return
	}
	m.deleteConfirm = &deleteConfirm{session: s.Path, label: filepath.Base(s.Path)}
}

// deleteSessionPath deletes one saved session by path. Orchestration child
// sessions reset the child controller first (so it cannot re-persist the file),
// then purge the transcript and its artifacts. Everything else goes through the
// controller's guarded DeleteSession.
func (m *chatTUI) deleteSessionPath(path string) error {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if strings.HasPrefix(name, "orchestrator_") && m.orc != nil {
		agentName := strings.TrimPrefix(name, "orchestrator_")
		if _, ok := m.orc.Agent(agentName); ok {
			return m.clearAgentSession(agentName)
		}
	}
	return m.ctrl.DeleteSession(path)
}

// clearAgentSession resets a managed agent's conversation in place: the child
// controller rotates to a fresh in-memory session, the old transcript and its
// artifacts are purged, and the session path is re-pinned to the canonical
// orchestrator file. Shared by /agent_clear and /session-delete so both stay
// consistent.
func (m *chatTUI) clearAgentSession(name string) error {
	if m.orc == nil {
		return fmt.Errorf("no orchestrator configured")
	}
	a, ok := m.orc.Agent(name)
	if !ok {
		return fmt.Errorf("agent %q not found", name)
	}
	if a.Ctrl.Running() {
		return control.ErrSessionRunning
	}
	oldPath := a.Ctrl.SessionPath()
	if err := a.Ctrl.NewSession(); err != nil {
		return err
	}
	if oldPath != "" {
		if err := control.DeleteSessionArtifacts(oldPath); err != nil {
			return err
		}
	}
	dir := m.orc.SessionDir()
	if dir != "" {
		a.Ctrl.SetSessionPath(filepath.Join(dir, "orchestrator_"+name+".jsonl"))
	}
	return nil
}

// sessionDeleteArgItems completes the index argument of "/session-delete <n>":
// once past the command word it lists recent sessions, inserting the 1-based
// index and showing timestamp + turn count + preview as the hint. Indices match
// showSessions because both window through recentSessions.
func (m *chatTUI) sessionDeleteArgItems(val string) ([]compItem, int, bool) {
	cmdEnd := strings.IndexAny(val, " \t")
	if cmdEnd < 0 || val[:cmdEnd] != "/session-delete" {
		return nil, 0, false
	}
	from := strings.LastIndexAny(val, " \t") + 1
	if len(strings.Fields(val[:from])) != 1 || m.ctrl == nil {
		return nil, from, true
	}
	cur := val[from:]
	var out []compItem
	for i, s := range recentSessions(m.ctrl.SessionDir()) {
		idx := strconv.Itoa(i + 1)
		if cur != "" && !strings.HasPrefix(idx, cur) {
			continue
		}
		hint := fmt.Sprintf("%s · %s", s.ModTime.Local().Format("01-02 15:04"), sessionSummary(s))
		out = append(out, compItem{label: idx, insert: idx, hint: hint})
	}
	return out, from, true
}

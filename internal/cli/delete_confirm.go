package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/i18n"
)

// deleteConfirm is the destructive "/session-delete" confirmation overlay. It is
// separate from clearConfirm because /session-delete removes a saved session
// from disk instead of discarding the current transcript.
type deleteConfirm struct {
	session string // absolute path of the session to delete
	label   string // display name (file basename) shown in the prompt
	confirm int    // 0 = delete, 1 = cancel
}

func (m chatTUI) handleDeleteConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "down", "left", "right", "j", "k", "tab", "shift+tab":
		if m.deleteConfirm.confirm == 0 {
			m.deleteConfirm.confirm = 1
		} else {
			m.deleteConfirm.confirm = 0
		}
	case "y", "Y":
		return m.confirmDeleteSession()
	case "n", "N", "esc", "ctrl+c":
		m.deleteConfirm = nil
	case "enter":
		if m.deleteConfirm.confirm == 0 {
			return m.confirmDeleteSession()
		}
		m.deleteConfirm = nil
	}
	return m, nil
}

func (m chatTUI) confirmDeleteSession() (tea.Model, tea.Cmd) {
	dc := m.deleteConfirm
	m.deleteConfirm = nil
	if dc == nil {
		return m, nil
	}
	if err := m.deleteSessionPath(dc.session); err != nil {
		m.notice(fmt.Sprintf("%s: %v", i18n.M.SessionDeleteFailed, err))
		return m, nil
	}
	m.notice(i18n.M.SessionDeleteDone)
	return m, nil
}

func (m chatTUI) renderDeleteConfirm() string {
	if m.deleteConfirm == nil {
		return ""
	}
	w := max(viewWidth(m.width), 40)
	var b strings.Builder
	b.WriteString(fmt.Sprintf(i18n.M.SessionDeletePromptFmt, m.deleteConfirm.label) + "\n")
	b.WriteString(viewMeta(i18n.M.SessionDeletePromptMeta) + "\n\n")
	b.WriteString(rowLine(m.deleteConfirm.confirm == 0, 1, "", "Delete", false) + "\n")
	b.WriteString(rowLine(m.deleteConfirm.confirm == 1, 2, "", "Cancel", false))
	return choicePanelStyle.Width(w).Render(b.String())
}

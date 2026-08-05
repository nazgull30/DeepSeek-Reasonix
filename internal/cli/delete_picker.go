package cli

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/agent"
	"reasonix/internal/i18n"
)

// sessionDeletePicker is an in-chat overlay for "/session-delete" that lets the
// user pick a saved session by navigating with ↑/↓ and confirming with Enter,
// which moves to the destructive confirmation instead of deleting directly. It
// mirrors the resumePicker pattern: keys route through
// handleSessionDeletePickerKey and it renders via renderSessionDeletePicker
// while m.sessionDeletePick is set.
type sessionDeletePicker struct {
	sessions []agent.SessionInfo
	sel      int
}

// openSessionDeletePicker populates the picker from the session directory and
// opens it. A no-op (with a notice) when there are no saved sessions.
func (m *chatTUI) openSessionDeletePicker() {
	sessions := recentSessions(m.ctrl.SessionDir())
	if len(sessions) == 0 {
		m.notice(i18n.M.NoSessionToDelete)
		return
	}
	m.sessionDeletePick = &sessionDeletePicker{sessions: sessions}
}

func (m chatTUI) handleSessionDeletePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.sessionDeletePick
	if p == nil {
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		if p.sel > 0 {
			p.sel--
		}
	case "down", "j":
		if p.sel < len(p.sessions)-1 {
			p.sel++
		}
	case "enter":
		m.sessionDeletePick = nil
		m.promptSessionDelete(p.sessions[p.sel])
	case "esc":
		m.sessionDeletePick = nil
	}
	return m, nil
}

func (m chatTUI) renderSessionDeletePicker() string {
	p := m.sessionDeletePick
	if p == nil {
		return ""
	}
	w := max(m.width, 10)
	var b strings.Builder
	b.WriteString(accent(i18n.M.SessionDeletePickTitle) + "\n")
	active := m.ctrl.SessionPath()
	for i, s := range p.sessions {
		label := sessionPickerLabel(s)
		if s.Path == active {
			label = dim(label) + " " + dim("(active)")
		}
		b.WriteString(rowLine(i == p.sel, i+1, "", label, false) + "\n")
	}
	b.WriteString(dim(i18n.M.SessionDeletePickHint))
	return choicePanelStyle.Width(w).Render(b.String())
}

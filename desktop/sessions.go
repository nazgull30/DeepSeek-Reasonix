package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/fileutil"
)

// sessions.go holds the desktop-only session-management state that the shared
// kernel doesn't model: custom display titles. A session on disk is just a JSONL
// transcript named by timestamp+model, with no title slot — so the history panel
// stores user-chosen names in a sidecar map (basename → title) next to the .jsonl
// files. The preview (first user message) is the default name; a title overrides
// it. Deleting a session also drops its title entry.

const sessionTitlesFile = ".titles.json"
const sessionDisplayFile = ".display.json"
const sessionTrashDir = ".trash"
const sessionTrashMetaFile = ".trash-meta.json"

func sessionTitlesPath(dir string) string  { return filepath.Join(dir, sessionTitlesFile) }
func sessionDisplayPath(dir string) string { return filepath.Join(dir, sessionDisplayFile) }
func sessionTrashPath(dir string) string   { return filepath.Join(dir, sessionTrashDir) }

func desktopSessionDir(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return config.SessionDir()
		}
		root = cwd
	}
	if dir := config.ProjectSessionDir(root); dir != "" {
		return dir
	}
	return config.SessionDir()
}

// loadSessionTitles reads the basename→title map (missing/corrupt → empty).
func loadSessionTitles(dir string) map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(sessionTitlesPath(dir))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

// saveSessionTitles writes the map atomically (temp file + rename).
func saveSessionTitles(dir string, m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".titles.*.tmp")
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
	return fileutil.ReplaceFile(tmpPath, sessionTitlesPath(dir))
}

// setSessionTitle sets (or, with an empty title, clears) a session's custom name.
func setSessionTitle(dir, sessionPath, title string) error {
	sessionPath, _, err := validateSessionPath(dir, sessionPath)
	if err != nil {
		return err
	}
	m := loadSessionTitles(dir)
	key := filepath.Base(sessionPath)
	if strings.TrimSpace(title) == "" {
		delete(m, key)
	} else {
		m[key] = strings.TrimSpace(title)
	}
	return saveSessionTitles(dir, m)
}

// deleteSessionFile moves a session's .jsonl and file sidecars into the local
// trash. Title/display sidecars stay in place so trash previews and restores can
// preserve the user's labels.
func deleteSessionFile(dir, sessionPath string) error {
	return agent.TrashSessionFiles(dir, sessionPath)
}

func trashSessionArtifacts(dir, sessionPath, key string) error {
	return agent.TrashSessionFiles(dir, sessionPath)
}

func reconcileDesktopCleanupPending(dir string) error {
	return agent.ReconcileCleanupPending(dir, func(item agent.CleanupPendingInfo) error {
		if strings.TrimSpace(item.Meta.Operation) == "delete" {
			return agent.MoveSessionFilesToTrash(dir, item.SessionPath)
		}
		return removeDesktopSessionArtifacts(item.SessionPath)
	})
}

func reconcileDesktopTrashSessionArtifacts(dir, sessionPath, key string) error {
	return agent.MoveSessionFilesToTrash(dir, sessionPath)
}

func validateSessionTrashTarget(dir, sessionPath, key string) error {
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	itemDir := filepath.Join(sessionTrashPath(dir), key)
	if _, err := os.Stat(itemDir); err == nil {
		return fmt.Errorf("session already exists in trash: %s", key)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func listTrashedSessionFiles(dir string) ([]string, error) {
	return agent.ListTrashedSessions(dir)
}

func trashedSessionDeletedAt(path string) int64 {
	return agent.TrashedSessionDeletedAt(path)
}

func restoreTrashedSessionFile(dir, path string) error {
	return agent.RestoreTrashedSession(dir, path)
}

func purgeTrashedSessionFile(dir, path string) error {
	if err := agent.PurgeTrashedSession(dir, path); err != nil {
		return err
	}
	_, key, _, err := agent.ValidateTrashedSessionPath(dir, path)
	if err != nil {
		return nil
	}
	m := loadSessionTitles(dir)
	if _, ok := m[key]; ok {
		delete(m, key)
		if err := saveSessionTitles(dir, m); err != nil {
			return err
		}
	}
	if dm := loadSessionDisplays(dir); dm[key] != nil {
		delete(dm, key)
		if err := saveSessionDisplays(dir, dm); err != nil {
			return err
		}
	}
	return nil
}

func validateSessionPath(dir, sessionPath string) (string, string, error) {
	return agent.ValidateSessionPath(dir, sessionPath)
}

func validateTrashedSessionPath(dir, sessionPath string) (string, string, string, error) {
	return agent.ValidateTrashedSessionPath(dir, sessionPath)
}

type sessionDisplayMap map[string]map[string]string

func messageDisplayKey(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", sum[:])
}

func loadSessionDisplays(dir string) sessionDisplayMap {
	m := sessionDisplayMap{}
	b, err := os.ReadFile(sessionDisplayPath(dir))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func saveSessionDisplays(dir string, m sessionDisplayMap) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".display.*.tmp")
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
	return fileutil.ReplaceFile(tmpPath, sessionDisplayPath(dir))
}

func recordSessionDisplay(dir, sessionPath, content, display string) error {
	if strings.TrimSpace(sessionPath) == "" || content == display || strings.TrimSpace(display) == "" {
		return nil
	}
	m := loadSessionDisplays(dir)
	key := filepath.Base(sessionPath)
	if m[key] == nil {
		m[key] = map[string]string{}
	}
	m[key][messageDisplayKey(content)] = display
	return saveSessionDisplays(dir, m)
}

// sessionDisplayResolver loads the sidecar once and returns a per-message
// resolver, so a transcript of N messages doesn't re-read .display.json N times.
func sessionDisplayResolver(dir, sessionPath string) func(content string) string {
	return sessionDisplayResolverFromMap(loadSessionDisplays(dir), sessionPath)
}

func sessionDisplayResolverFromMap(displays sessionDisplayMap, sessionPath string) func(content string) string {
	byHash := displays[filepath.Base(sessionPath)]
	return func(content string) string {
		if byHash != nil {
			if display := byHash[messageDisplayKey(content)]; strings.TrimSpace(display) != "" {
				return display
			}
		}
		return control.StripComposePrefixes(content)
	}
}

func resolveSessionDisplay(dir, sessionPath, content string) string {
	return sessionDisplayResolver(dir, sessionPath)(content)
}

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/jobs"
)

// SessionTrashDir is the per-session-dir folder that holds soft-deleted
// sessions. Each trashed session lives in <dir>/.trash/<key>/ where key is the
// basename of its transcript file.
const SessionTrashDir = ".trash"

const sessionTrashMetaFile = ".trash-meta.json"

// TrashedSessionMeta records when a session was moved to the trash.
type TrashedSessionMeta struct {
	Key       string `json:"key"`
	DeletedAt int64  `json:"deletedAt"`
}

// SessionTrashPath returns the trash directory for a session directory.
func SessionTrashPath(dir string) string {
	return filepath.Join(dir, SessionTrashDir)
}

// TrashSessionFiles moves a session's transcript and all its sidecars (branch
// meta, checkpoints, job artifacts, goal-state, subagents) into the session
// directory's .trash/<key>/ folder so the session can later be restored. A
// transcript that no longer exists is a no-op (already gone). It refuses to
// overwrite an existing trash item; cleanup-pending reconciliation should use
// MoveSessionFilesToTrash, which tolerates a pre-existing item directory.
func TrashSessionFiles(dir, sessionPath string) error {
	sessionPath, key, err := ValidateSessionPath(dir, sessionPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	itemDir := filepath.Join(SessionTrashPath(dir), key)
	if _, err := os.Stat(itemDir); err == nil {
		return fmt.Errorf("session already exists in trash: %s", key)
	} else if !os.IsNotExist(err) {
		return err
	}
	return MoveSessionFilesToTrash(dir, sessionPath)
}

// MoveSessionFilesToTrash moves a session's transcript and sidecars into the
// trash, creating (or reusing) the .trash/<key>/ item directory. Reuse of an
// existing item directory is intentional: delayed cleanup-pending reconciliation
// can pick up artifacts that a previous crash left behind.
func MoveSessionFilesToTrash(dir, sessionPath string) error {
	sessionPath, key, err := ValidateSessionPath(dir, sessionPath)
	if err != nil {
		return err
	}
	itemDir := filepath.Join(SessionTrashPath(dir), key)
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		return err
	}
	if err := movePathIfExists(sessionPath, filepath.Join(itemDir, key)); err != nil {
		return err
	}
	if err := movePathIfExists(sessionPath+".meta", filepath.Join(itemDir, key+".meta")); err != nil {
		return err
	}
	ckptName := strings.TrimSuffix(key, ".jsonl") + ".ckpt"
	if err := movePathIfExists(strings.TrimSuffix(sessionPath, ".jsonl")+".ckpt", filepath.Join(itemDir, ckptName)); err != nil {
		return err
	}
	jobsName := strings.TrimSuffix(key, ".jsonl") + ".jobs"
	if err := movePathIfExists(jobs.ArtifactDir(sessionPath), filepath.Join(itemDir, jobsName)); err != nil {
		return err
	}
	goalName := strings.TrimSuffix(key, ".jsonl") + ".goal-state.json"
	if err := movePathIfExists(GoalStateSidecar(sessionPath), filepath.Join(itemDir, goalName)); err != nil {
		return err
	}
	if err := trashSubagentArtifacts(dir, sessionPath, itemDir); err != nil {
		return err
	}
	meta := TrashedSessionMeta{Key: key, DeletedAt: time.Now().UnixMilli()}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(itemDir, sessionTrashMetaFile), b, 0o644); err != nil {
		return err
	}
	return ClearCleanupPending(sessionPath)
}

// ListTrashedSessions returns the transcript paths of sessions currently in the
// trash, newest deletion first.
func ListTrashedSessions(dir string) ([]string, error) {
	root := SessionTrashPath(dir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	paths := []string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		key := e.Name()
		if filepath.Ext(key) != ".jsonl" || filepath.Base(key) != key {
			continue
		}
		path := filepath.Join(root, key, key)
		validPath, _, _, err := ValidateTrashedSessionPath(dir, path)
		if err != nil {
			continue
		}
		if info, err := os.Stat(validPath); err == nil && !info.IsDir() {
			paths = append(paths, validPath)
		}
	}
	return paths, nil
}

// TrashedSessionDeletedAt returns the unix-millisecond timestamp at which a
// trashed session (a transcript path returned by ListTrashedSessions) was moved
// to the trash. Returns 0 when the metadata is missing.
func TrashedSessionDeletedAt(path string) int64 {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), sessionTrashMetaFile))
	if err != nil {
		return 0
	}
	var meta TrashedSessionMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return 0
	}
	return meta.DeletedAt
}

// RestoreTrashedSession moves a trashed session (a transcript path returned by
// ListTrashedSessions) back into the session directory.
func RestoreTrashedSession(dir, path string) error {
	trashPath, key, itemDir, err := ValidateTrashedSessionPath(dir, path)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, key)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("session already exists: %s", key)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := checkRestoreSubagentConflicts(dir, itemDir); err != nil {
		return err
	}
	if err := movePathIfExists(trashPath, target); err != nil {
		return err
	}
	if err := movePathIfExists(trashPath+".meta", target+".meta"); err != nil {
		return err
	}
	ckptName := strings.TrimSuffix(key, ".jsonl") + ".ckpt"
	if err := movePathIfExists(filepath.Join(itemDir, ckptName), filepath.Join(dir, ckptName)); err != nil {
		return err
	}
	jobsName := strings.TrimSuffix(key, ".jsonl") + ".jobs"
	if err := movePathIfExists(filepath.Join(itemDir, jobsName), filepath.Join(dir, jobsName)); err != nil {
		return err
	}
	goalName := strings.TrimSuffix(key, ".jsonl") + ".goal-state.json"
	if err := movePathIfExists(filepath.Join(itemDir, goalName), filepath.Join(dir, goalName)); err != nil {
		return err
	}
	if err := restoreSubagentArtifacts(dir, itemDir); err != nil {
		return err
	}
	return os.RemoveAll(itemDir)
}

// PurgeTrashedSession permanently deletes a trashed session (a transcript path
// returned by ListTrashedSessions) from the trash.
func PurgeTrashedSession(dir, path string) error {
	_, _, itemDir, err := ValidateTrashedSessionPath(dir, path)
	if err != nil {
		return err
	}
	return os.RemoveAll(itemDir)
}

func trashSubagentArtifacts(dir, sessionPath, itemDir string) error {
	artifacts, err := ListSubagentsByParent(dir, BranchID(sessionPath))
	if err != nil {
		return err
	}
	trashSubagentDir := filepath.Join(itemDir, "subagents")
	for _, artifact := range artifacts {
		if err := movePathIfExists(artifact.SessionPath, filepath.Join(trashSubagentDir, filepath.Base(artifact.SessionPath))); err != nil {
			return err
		}
		if err := movePathIfExists(artifact.MetaPath, filepath.Join(trashSubagentDir, filepath.Base(artifact.MetaPath))); err != nil {
			return err
		}
	}
	return nil
}

func checkRestoreSubagentConflicts(dir, itemDir string) error {
	trashSubagentDir := filepath.Join(itemDir, "subagents")
	entries, err := os.ReadDir(trashSubagentDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		target := filepath.Join(dir, "subagents", entry.Name())
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("subagent artifact already exists: %s", entry.Name())
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func restoreSubagentArtifacts(dir, itemDir string) error {
	trashSubagentDir := filepath.Join(itemDir, "subagents")
	entries, err := os.ReadDir(trashSubagentDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := movePathIfExists(filepath.Join(trashSubagentDir, entry.Name()), filepath.Join(dir, "subagents", entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func movePathIfExists(src, dst string) error {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// ValidateSessionPath resolves a session transcript path and enforces that it is
// an absolute .jsonl path inside dir (rejecting escapes and symlink redirects).
// Returns the resolved path and its basename (key).
func ValidateSessionPath(dir, sessionPath string) (string, string, error) {
	if strings.TrimSpace(sessionPath) == "" {
		return "", "", fmt.Errorf("empty session path")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	path := sessionPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(absDir, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	if filepath.Ext(absPath) != ".jsonl" {
		return "", "", fmt.Errorf("not a session file: %s", sessionPath)
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("session path outside session dir: %s", sessionPath)
	}
	if info, err := os.Lstat(absPath); err == nil {
		if info.IsDir() {
			return "", "", fmt.Errorf("not a session file: %s", sessionPath)
		}
		realDir, dirErr := filepath.EvalSymlinks(absDir)
		if dirErr != nil {
			realDir = absDir
		}
		realPath, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			return "", "", err
		}
		rel, err := filepath.Rel(realDir, realPath)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
			return "", "", fmt.Errorf("session path escapes session dir: %s", sessionPath)
		}
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	return absPath, filepath.Base(absPath), nil
}

// ValidateTrashedSessionPath resolves a trashed transcript path and enforces that
// it is an absolute .jsonl path inside <dir>/.trash/<key>/ (rejecting escapes and
// symlink redirects). Returns the resolved path, its basename (key), and the item
// directory.
func ValidateTrashedSessionPath(dir, sessionPath string) (string, string, string, error) {
	if strings.TrimSpace(sessionPath) == "" {
		return "", "", "", fmt.Errorf("empty session path")
	}
	root, err := filepath.Abs(SessionTrashPath(dir))
	if err != nil {
		return "", "", "", err
	}
	path := sessionPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", "", err
	}
	if filepath.Ext(absPath) != ".jsonl" {
		return "", "", "", fmt.Errorf("not a session file: %s", sessionPath)
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", "", "", fmt.Errorf("session path outside trash dir: %s", sessionPath)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || parts[0] != parts[1] {
		return "", "", "", fmt.Errorf("invalid trash session path: %s", sessionPath)
	}
	if info, err := os.Lstat(absPath); err == nil {
		if info.IsDir() {
			return "", "", "", fmt.Errorf("not a session file: %s", sessionPath)
		}
		realRoot, dirErr := filepath.EvalSymlinks(root)
		if dirErr != nil {
			realRoot = root
		}
		realPath, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			return "", "", "", err
		}
		rel, err := filepath.Rel(realRoot, realPath)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
			return "", "", "", fmt.Errorf("session path escapes trash dir: %s", sessionPath)
		}
	} else if !os.IsNotExist(err) {
		return "", "", "", err
	}
	return absPath, filepath.Base(absPath), filepath.Dir(absPath), nil
}

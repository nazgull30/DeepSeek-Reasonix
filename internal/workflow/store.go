// Package workflow loads and executes Starlark orchestration scripts
// ("workflows") discovered from .reasonix/workflows/. A workflow is a compact,
// user-authored script that coordinates sub-agents via hooks (agent, parallel,
// pipeline, phase, log) instead of a verbose prose playbook: loops, conditionals,
// fan-out, and data flow between steps live in code, not in a model's context.
// Discovery mirrors the skills store: files live under <root>/.reasonix/workflows/
// as *.star, the workflow name is the filename stem, and a leading string literal
// (module docstring) on the first statement is the description shown in the
// pinned index. Scripts execute sandboxed by the Starlark interpreter — no
// filesystem, no network, no imports, a bounded step count; the only side effects
// flow through the injected agent hook (see engine.go).
package workflow

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"reasonix/internal/config"
)

// Workflow is a discovered Starlark orchestration script.
type Workflow struct {
	Name        string // canonical identifier; equals the filename stem
	Description string // one-liner from the leading docstring, shown in the index
	Path        string // absolute path to the .star file
	Body        string // executable script (docstring stripped)
}

const (
	// Dirname is the directory under the project root that holds workflows.
	Dirname = "workflows"
	// Ext is the script filename extension.
	Ext = ".star"
)

// Store resolves workflows under a project's .reasonix/workflows/ directory.
type Store struct {
	projectRoot string
}

// NewStore builds a Store. A relative project root is made absolute; an empty
// root ("no workspace") yields no workflows.
func NewStore(projectRoot string) *Store {
	if projectRoot != "" {
		if abs, err := filepath.Abs(projectRoot); err == nil {
			projectRoot = abs
		}
	}
	return &Store{projectRoot: projectRoot}
}

// Dir returns the canonical project workflows directory.
func (s *Store) Dir() string {
	if s.projectRoot == "" {
		return ""
	}
	return filepath.Join(s.projectRoot, ".reasonix", Dirname)
}

// List returns every discoverable workflow, sorted by name so the prefix index
// stays stable and cacheable. Files with no executable body (a docstring only)
// are skipped.
func (s *Store) List() []Workflow {
	if s.projectRoot == "" {
		return nil
	}
	dir := s.Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]Workflow, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), Ext) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if !config.IsValidSkillName(name) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		desc, body := splitDocstring(string(raw))
		if strings.TrimSpace(body) == "" {
			continue
		}
		out = append(out, Workflow{
			Name:        name,
			Description: strings.TrimSpace(desc),
			Path:        path,
			Body:        body,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Read resolves one workflow by name. ok is false when no such file exists,
// the name is invalid, or the file carries no executable body.
func (s *Store) Read(name string) (Workflow, bool) {
	if !config.IsValidSkillName(name) {
		return Workflow{}, false
	}
	for _, wf := range s.List() {
		if wf.Name == name {
			return wf, true
		}
	}
	return Workflow{}, false
}

// splitDocstring splits a .star file's leading module docstring — the first
// statement, a single- or triple-quoted string literal that may span lines —
// from the executable body. Leading blanks and '# comments' are skipped before
// the literal is recognized; an unrecognized leading token yields an empty
// description and the full input as the body.
func splitDocstring(content string) (desc, body string) {
	content = strings.TrimPrefix(strings.ReplaceAll(content, "\r\n", "\n"), "\uFEFF")
	i := 0
	for i < len(content) {
		switch c := content[i]; {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case c == '#':
			for i < len(content) && content[i] != '\n' {
				i++
			}
		default:
			goto token
		}
	}
token:
	rest := content[i:]
	if len(rest) == 0 || (rest[0] != '"' && rest[0] != '\'') {
		return "", content
	}
	delim := string(rest[0])
	if len(rest) >= 3 && rest[0] == rest[1] && rest[1] == rest[2] {
		delim = rest[:3]
		rest = rest[3:]
	} else {
		rest = rest[1:]
	}
	idx := strings.Index(rest, delim)
	if idx < 0 {
		return "", content
	}
	return rest[:idx], rest[idx+len(delim):]
}

package plugin

import "strings"

// ApplyKnownReadOnlyOverrides fills compatibility read-only hints for MCP
// servers whose read surfaces are stable but older runtimes may omit MCP
// annotations. It does not make the server built-in or change startup behavior.
func ApplyKnownReadOnlyOverrides(s Spec) Spec {
	if isCodeGraphSpecName(s.Name) {
		s.ReadOnlyToolNames = mergeReadOnlyToolNames(s.ReadOnlyToolNames, codeGraphReadOnlyToolNames())
	}
	return s
}

func isCodeGraphSpecName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "codegraph")
}

func mergeReadOnlyToolNames(existing map[string]bool, extra map[string]bool) map[string]bool {
	out := make(map[string]bool, len(existing)+len(extra))
	for name, ok := range existing {
		out[name] = ok
	}
	for name, ok := range extra {
		if ok {
			out[name] = true
		}
	}
	return out
}

func codeGraphReadOnlyToolNames() map[string]bool {
	base := []string{
		"callees",
		"callers",
		"context",
		"explore",
		"files",
		"impact",
		"node",
		"search",
		"status",
		"trace",
	}
	out := make(map[string]bool, len(base)*2)
	for _, name := range base {
		out[name] = true
		out["codegraph_"+name] = true
	}
	return out
}

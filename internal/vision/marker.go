package vision

import "strings"

// MarkerPrefix opens the inline marker the read_image built-in tool returns and
// the agent's mid-turn injector looks for. The path placed between it and the
// closing ']' is the tool-resolved path the injector re-reads.
const MarkerPrefix = "[reasonix-image: "

// Marker wraps path in the read_image result marker format.
func Marker(path string) string {
	return MarkerPrefix + path + "]"
}

// ParseMarker extracts the image path from a read_image tool result. ok is
// false when the result carries no marker (a failure, or a tool result written
// without one — both leave the text as plain tool output).
func ParseMarker(text string) (path string, ok bool) {
	i := strings.Index(text, MarkerPrefix)
	if i < 0 {
		return "", false
	}
	start := i + len(MarkerPrefix)
	end := strings.IndexByte(text[start:], ']')
	if end < 0 {
		return "", false
	}
	path = text[start : start+end]
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	return path, true
}

package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"reasonix/internal/i18n"
)

// errCancelled is returned by selectOne when the user aborts (q or Ctrl-C).
var errCancelled = errors.New("selection cancelled")

// appendSearchText decodes printable UTF-8 runes from a raw stdin chunk and
// appends them to query, so search keeps working for multibyte input such as
// Cyrillic. It reports whether anything was appended and returns any trailing
// truncated sequence so the caller can prepend it to the next read (a rune may
// straddle two reads).
func appendSearchText(query string, b []byte) (next string, added bool, carry []byte) {
	next = query
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 {
			if rem := b[i:]; incompleteUTF8(rem) {
				return next, added, append([]byte(nil), rem...)
			}
			i++ // invalid byte: drop it
			continue
		}
		if unicode.IsPrint(r) {
			next += string(r)
			added = true
		}
		i += size
	}
	return next, added, nil
}

// incompleteUTF8 reports whether b is a valid but truncated UTF-8 sequence:
// a lead byte followed by fewer continuation bytes than it requires.
func incompleteUTF8(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	var want int
	switch c := b[0]; {
	case c&0x80 == 0x00 || c&0xC0 == 0x80:
		return false // ASCII or stray continuation byte
	case c&0xE0 == 0xC0:
		want = 2
	case c&0xF0 == 0xE0:
		want = 3
	case c&0xF8 == 0xF0:
		want = 4
	default:
		return false // invalid lead byte
	}
	if len(b) >= want {
		return false
	}
	for _, c := range b[1:] {
		if c&0xC0 != 0x80 {
			return false
		}
	}
	return true
}

// trimLastRune drops the final rune from s, keeping multibyte chars whole.
func trimLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

type menuItem struct {
	name string
	desc string
}

// termHeight returns the terminal's row count, falling back to 24 on error.
func termHeight(fd int) int {
	_, h, err := term.GetSize(fd)
	if err != nil || h <= 0 {
		return 24
	}
	return h
}

// fixedLines returns the number of non-item lines rendered each frame:
// header label, blank separator, scroll-up indicator, scroll-down indicator.
// When searching is true the search bar adds one more line.
func fixedLines(searching bool) int {
	n := 4 // header + blank + scroll-up + scroll-down
	if searching {
		n++ // search bar
	}
	return n
}

// maxViewport calculates how many menu item rows fit after subtracting the
// fixed lines from the available terminal rows, leaving at least 1 row.
func maxViewport(totalItems, termRows int, searching bool) int {
	avail := termRows - fixedLines(searching)
	if avail < 1 {
		avail = 1
	}
	if totalItems < avail {
		return totalItems
	}
	return avail
}

// renderSearchBar draws the search input line when searching is active.
func renderSearchBar(w *os.File, query string) {
	fmt.Fprintf(w, "\r\033[K%s %s\n", accent("🔍"), query+"_")
}

// filterMenuItems returns items whose name or desc contain the query (case-insensitive).
func filterMenuItems(items []menuItem, query string) []menuItem {
	if query == "" {
		return items
	}
	lq := strings.ToLower(query)
	var out []menuItem
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.name), lq) || strings.Contains(strings.ToLower(it.desc), lq) {
			out = append(out, it)
		}
	}
	return out
}

// selectOne renders an interactive single-choice menu navigated with the arrow
// keys (or j/k), confirmed with Enter, aborted with q or Ctrl-C. It puts the
// terminal in raw mode, so it requires a TTY (callers gate on isInteractive).
// When the item list exceeds the terminal height, only a viewport-sized window
// is shown, with scroll indicators. Pressing '/' enters search mode to filter
// items by keyword.
func selectOne(label string, items []menuItem) (int, error) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return 0, err
	}
	defer term.Restore(fd, old)

	w := os.Stdout
	th := termHeight(fd)

	// search state
	searching := false
	searchQuery := ""
	filtered := items
	filterIdx := make([]int, len(items))
	for i := range items {
		filterIdx[i] = i
	}

	sel := 0
	scroll := 0
	prevLines := 0 // lines printed in the previous frame; 0 = first frame

	render := func() {
		n := len(filtered)
		vp := maxViewport(n, th, searching)
		// adjust scroll to keep sel visible
		if sel < scroll {
			scroll = sel
		}
		if sel >= scroll+vp {
			scroll = sel - vp + 1
		}
		if scroll < 0 {
			scroll = 0
		}

		// scroll-up indicator (always 1 line)
		if n > 0 && scroll > 0 {
			fmt.Fprintf(w, "\r\033[K%s\n", dim(fmt.Sprintf(i18n.M.SelectMoreAboveFmt, scroll)))
		} else {
			fmt.Fprintf(w, "\r\033[K\r\n")
		}

		// menu rows
		end := scroll + vp
		if end > n {
			end = n
		}
		for i := scroll; i < end; i++ {
			it := filtered[i]
			name := fmt.Sprintf("%-10s", it.name)
			if i == sel {
				fmt.Fprintf(w, "\r\033[K%s\r\n", reverse(fmt.Sprintf(" ❯ %s %s ", name, it.desc)))
			} else {
				fmt.Fprintf(w, "\r\033[K   %s %s\r\n", name, dim(it.desc))
			}
		}
		// if fewer items than viewport, pad with blank lines so the frame
		// height stays constant
		for i := end - scroll; i < vp; i++ {
			fmt.Fprintf(w, "\r\033[K\r\n")
		}

		// scroll-down indicator (always 1 line)
		if n > 0 && end < n {
			fmt.Fprintf(w, "\r\033[K%s\n", dim(fmt.Sprintf(i18n.M.SelectMoreBelowFmt, n-end)))
		} else {
			fmt.Fprintf(w, "\r\033[K\r\n")
		}
	}

	drawHeader := func() {
		if searching {
			fmt.Fprintf(w, "\r\033[K%s %s  %s\r\n\r\n", accent("▌"), bold(label), dim(i18n.M.SelectSearchHint))
			renderSearchBar(w, searchQuery)
		} else {
			fmt.Fprintf(w, "\r\033[K%s %s  %s\r\n\r\n", accent("▌"), bold(label), dim(i18n.M.SelectOneHint))
		}
	}

	redraw := func() {
		if prevLines > 0 {
			fmt.Fprintf(w, "\033[%dA", prevLines)
		}
		drawHeader()
		render()
		// Clear everything below the current frame so stale rows from a taller
		// previous frame don't linger.
		fmt.Fprint(w, "\033[J")
		prevLines = fixedLines(searching) + maxViewport(len(filtered), th, searching)
	}

	redraw() // initial draw

	buf := make([]byte, 8)
	var pend []byte // carries a truncated UTF-8 rune between reads while searching
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return 0, err
		}
		k := buf[:n]
		if len(pend) > 0 {
			k = append(pend, k...)
			pend = nil
		}

		if searching {
			switch {
			case k[0] == 27: // Esc — exit search
				searching = false
				searchQuery = ""
				filtered = items
				filterIdx = make([]int, len(items))
				for i := range items {
					filterIdx[i] = i
				}
				sel = 0
				scroll = 0
				redraw()
			case k[0] == '\r' || k[0] == '\n':
				if len(filtered) > 0 {
					fmt.Fprint(w, "\r\n")
					return filterIdx[sel], nil
				}
			case k[0] == 127 || k[0] == 8: // backspace
				if len(searchQuery) > 0 {
					searchQuery = trimLastRune(searchQuery)
					filtered = filterMenuItems(items, searchQuery)
					filterIdx = filterIndices(items, searchQuery)
					sel = 0
					scroll = 0
					redraw()
				}
			case k[0] == 3: // Ctrl-C
				fmt.Fprint(w, "\r\n")
				return 0, errCancelled
			case k[0] >= 32 && k[0] < 127: // printable ASCII
				searchQuery += string(k[0])
				filtered = filterMenuItems(items, searchQuery)
				filterIdx = filterIndices(items, searchQuery)
				sel = 0
				scroll = 0
				redraw()
			default: // multibyte printable text (e.g. Cyrillic)
				q, added, carry := appendSearchText(searchQuery, k)
				if carry != nil {
					pend = carry
				}
				if added {
					searchQuery = q
					filtered = filterMenuItems(items, searchQuery)
					filterIdx = filterIndices(items, searchQuery)
					sel = 0
					scroll = 0
					redraw()
				}
			}
			continue
		}

		switch {
		case k[0] == '\r' || k[0] == '\n':
			fmt.Fprint(w, "\r\n")
			return filterIdx[sel], nil
		case k[0] == 3 || k[0] == 'q': // Ctrl-C or q
			fmt.Fprint(w, "\r\n")
			return 0, errCancelled
		case k[0] == '/': // enter search mode
			searching = true
			searchQuery = ""
			redraw()
		case len(k) >= 3 && k[0] == 27 && k[1] == '[' && k[2] == 'A': // up
			if sel > 0 {
				sel--
			}
		case len(k) >= 3 && k[0] == 27 && k[1] == '[' && k[2] == 'B': // down
			if sel < len(filtered)-1 {
				sel++
			}
		case k[0] == 'k':
			if sel > 0 {
				sel--
			}
		case k[0] == 'j':
			if sel < len(filtered)-1 {
				sel++
			}
		default:
			continue // ignore other keys, no redraw
		}
		redraw()
	}
}

// selectMany renders an interactive multi-choice menu: arrow keys (or j/k) move,
// Space toggles, Enter confirms (at least one required), q/Ctrl-C aborts. It
// returns the checked indices in order and requires a TTY. When the item list
// exceeds the terminal height, only a viewport-sized window is shown. Pressing
// '/' enters search mode to filter items by keyword.
func selectMany(label string, items []menuItem) ([]int, error) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	defer term.Restore(fd, old)

	w := os.Stdout
	th := termHeight(fd)

	// search state
	searching := false
	searchQuery := ""
	filtered := items
	filterIdx := make([]int, len(items))
	for i := range items {
		filterIdx[i] = i
	}

	cur := 0
	checked := make([]bool, len(items))
	scroll := 0
	prevLines := 0

	render := func() {
		n := len(filtered)
		vp := maxViewport(n, th, searching)
		if cur < scroll {
			scroll = cur
		}
		if cur >= scroll+vp {
			scroll = cur - vp + 1
		}
		if scroll < 0 {
			scroll = 0
		}

		if n > 0 && scroll > 0 {
			fmt.Fprintf(w, "\r\033[K%s\n", dim(fmt.Sprintf(i18n.M.SelectMoreAboveFmt, scroll)))
		} else {
			fmt.Fprintf(w, "\r\033[K\r\n")
		}

		end := scroll + vp
		if end > n {
			end = n
		}
		for i := scroll; i < end; i++ {
			it := filtered[i]
			origIdx := filterIdx[i]
			box := "[ ]"
			if checked[origIdx] {
				box = "[x]"
			}
			name := fmt.Sprintf("%-14s", it.name)
			if i == cur {
				fmt.Fprintf(w, "\r\033[K%s\r\n", reverse(fmt.Sprintf(" ❯ %s %s %s ", box, name, it.desc)))
			} else {
				fmt.Fprintf(w, "\r\033[K   %s %s %s\r\n", box, name, dim(it.desc))
			}
		}
		for i := end - scroll; i < vp; i++ {
			fmt.Fprintf(w, "\r\033[K\r\n")
		}

		if n > 0 && end < n {
			fmt.Fprintf(w, "\r\033[K%s\n", dim(fmt.Sprintf(i18n.M.SelectMoreBelowFmt, n-end)))
		} else {
			fmt.Fprintf(w, "\r\033[K\r\n")
		}
	}

	drawHeader := func() {
		if searching {
			fmt.Fprintf(w, "\r\033[K%s %s  %s\r\n\r\n", accent("▌"), bold(label), dim(i18n.M.SelectSearchHint))
			renderSearchBar(w, searchQuery)
		} else {
			fmt.Fprintf(w, "\r\033[K%s %s  %s\r\n\r\n", accent("▌"), bold(label), dim(i18n.M.SelectManyHint))
		}
	}

	redraw := func() {
		if prevLines > 0 {
			fmt.Fprintf(w, "\033[%dA", prevLines)
		}
		drawHeader()
		render()
		fmt.Fprint(w, "\033[J")
		prevLines = fixedLines(searching) + maxViewport(len(filtered), th, searching)
	}

	redraw()

	buf := make([]byte, 8)
	var pend []byte // carries a truncated UTF-8 rune between reads while searching
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return nil, err
		}
		k := buf[:n]
		if len(pend) > 0 {
			k = append(pend, k...)
			pend = nil
		}

		if searching {
			switch {
			case k[0] == 27: // Esc — exit search
				searching = false
				searchQuery = ""
				filtered = items
				filterIdx = make([]int, len(items))
				for i := range items {
					filterIdx[i] = i
				}
				cur = 0
				scroll = 0
				redraw()
			case k[0] == '\r' || k[0] == '\n':
				var out []int
				for i, c := range checked {
					if c {
						out = append(out, i)
					}
				}
				if len(out) == 0 {
					continue
				}
				fmt.Fprint(w, "\r\n")
				return out, nil
			case k[0] == ' ':
				if len(filtered) > 0 {
					origIdx := filterIdx[cur]
					checked[origIdx] = !checked[origIdx]
				}
			case k[0] == 127 || k[0] == 8: // backspace
				if len(searchQuery) > 0 {
					searchQuery = trimLastRune(searchQuery)
					filtered = filterMenuItems(items, searchQuery)
					filterIdx = filterIndices(items, searchQuery)
					cur = 0
					scroll = 0
				}
			case k[0] == 3: // Ctrl-C
				fmt.Fprint(w, "\r\n")
				return nil, errCancelled
			case k[0] >= 32 && k[0] < 127: // printable ASCII
				searchQuery += string(k[0])
				filtered = filterMenuItems(items, searchQuery)
				filterIdx = filterIndices(items, searchQuery)
				cur = 0
				scroll = 0
			case len(k) >= 3 && k[0] == 27 && k[1] == '[' && k[2] == 'A':
				if cur > 0 {
					cur--
				}
			case len(k) >= 3 && k[0] == 27 && k[1] == '[' && k[2] == 'B':
				if cur < len(filtered)-1 {
					cur++
				}
			case k[0] == 'k':
				if cur > 0 {
					cur--
				}
			case k[0] == 'j':
				if cur < len(filtered)-1 {
					cur++
				}
			default: // multibyte printable text (e.g. Cyrillic)
				q, added, carry := appendSearchText(searchQuery, k)
				if carry != nil {
					pend = carry
				}
				if !added {
					continue // ignore unmatched keys, no redraw
				}
				searchQuery = q
				filtered = filterMenuItems(items, searchQuery)
				filterIdx = filterIndices(items, searchQuery)
				cur = 0
				scroll = 0
			}
			redraw()
			continue
		}

		switch {
		case k[0] == '\r' || k[0] == '\n':
			var out []int
			for i, c := range checked {
				if c {
					out = append(out, i)
				}
			}
			if len(out) == 0 {
				continue // need at least one selection
			}
			fmt.Fprint(w, "\r\n")
			return out, nil
		case k[0] == 3 || k[0] == 'q':
			fmt.Fprint(w, "\r\n")
			return nil, errCancelled
		case k[0] == '/': // enter search mode
			searching = true
			searchQuery = ""
			redraw()
		case k[0] == ' ':
			if len(filtered) > 0 {
				origIdx := filterIdx[cur]
				checked[origIdx] = !checked[origIdx]
			}
		case len(k) >= 3 && k[0] == 27 && k[1] == '[' && k[2] == 'A':
			if cur > 0 {
				cur--
			}
		case len(k) >= 3 && k[0] == 27 && k[1] == '[' && k[2] == 'B':
			if cur < len(filtered)-1 {
				cur++
			}
		case k[0] == 'k':
			if cur > 0 {
				cur--
			}
		case k[0] == 'j':
			if cur < len(filtered)-1 {
				cur++
			}
		default:
			continue
		}
		redraw()
	}
}

// filterIndices returns the original indices of items matching query.
func filterIndices(items []menuItem, query string) []int {
	if query == "" {
		out := make([]int, len(items))
		for i := range items {
			out[i] = i
		}
		return out
	}
	lq := strings.ToLower(query)
	var out []int
	for i, it := range items {
		if strings.Contains(strings.ToLower(it.name), lq) || strings.Contains(strings.ToLower(it.desc), lq) {
			out = append(out, i)
		}
	}
	return out
}

// FrameLines is exported for testing. It returns the total number of terminal
// lines that selectOne/selectMany will print for the given state.
func FrameLines(filteredLen, termRows int, searching bool) int {
	return fixedLines(searching) + maxViewport(filteredLen, termRows, searching)
}

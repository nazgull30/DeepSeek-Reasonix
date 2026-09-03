package cli

import (
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	rw "github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// Mouse text selection over the composer (input lane).
//
// The transcript selection works purely on visual rows; the composer needs
// more: copied drafts must come back byte-faithful, so visual spans are mapped
// back to rune offsets in the textarea's Value(). To do that we replicate the
// bubbles v2 textarea's internal soft-wrap (wrapRuneSegs mirrors the textarea
// package's unexported wrap() rune for rune) and track each visual row's
// offset range within the logical value.

// inputRow is one visual row of the wrapped composer value. text is what the
// textarea renders (including any synthetic wrap padding), start/end bound the
// original runes behind it, and hard marks a break caused by a real newline
// rather than a soft word wrap.
type inputRow struct {
	text       string
	start, end int  // [start,end) rune range in Value(); end may trail on padded rows
	hard       bool // row starts a new logical line
}

// wrapInputRows splits value into visual rows at width using the same
// algorithm as the composer's textarea, so hit-testing and highlighting line
// up with what is on screen.
func wrapInputRows(value string, width int) []inputRow {
	if width < 1 {
		width = 1
	}
	var rows []inputRow
	base := 0
	for li, ln := range strings.Split(value, "\n") {
		rs := []rune(ln)
		for j, seg := range wrapRuneSegs(rs, width) {
			rows = append(rows, inputRow{
				text:  seg.text,
				start: base + seg.start,
				end:   base + seg.end,
				hard:  j == 0 && li > 0,
			})
		}
		base += len(rs) + 1 // +1 consumes the '\n' between logical lines
	}
	if len(rows) == 0 {
		rows = append(rows, inputRow{})
	}
	return rows
}

// inputSeg is one visual row produced by wrapRuneSegs: rendered text plus the
// [start,end) range of source runes it displays.
type inputSeg struct {
	text       string
	start, end int
}

// chunk accumulates rune indices destined for one visual row.
type chunk struct {
	idx []int
	syn int // synthetic display-only trailing spaces
}

// wrapRuneSegs ports the bubbles v2 textarea's wrap() with index tracking so
// every segment knows which source runes it renders. Keep in sync with
// textarea.wrap in charm.land/bubbles/v2.
func wrapRuneSegs(runes []rune, width int) []inputSeg {
	stringOf := func(c chunk) string {
		var b strings.Builder
		for _, i := range c.idx {
			b.WriteRune(runes[i])
		}
		return b.String()
	}

	lines := []chunk{{}}
	word := chunk{}
	spaces := chunk{}
	row := 0

	for i, r := range runes {
		if unicode.IsSpace(r) {
			spaces.idx = append(spaces.idx, i)
		} else {
			word.idx = append(word.idx, i)
		}

		if len(spaces.idx) > 0 { //nolint:nestif
			if uniseg.StringWidth(stringOf(lines[row]))+
				uniseg.StringWidth(stringOf(word))+len(spaces.idx) > width {
				row++
				lines = append(lines, chunk{})
			}
			lines[row].idx = append(lines[row].idx, word.idx...)
			lines[row].idx = append(lines[row].idx, spaces.idx...)
			word, spaces = chunk{}, chunk{}
		} else if len(word.idx) > 0 {
			// A double-width trailing rune may push the word past the width;
			// move the whole word to the next row when it does.
			lastW := rw.RuneWidth(runes[word.idx[len(word.idx)-1]])
			if uniseg.StringWidth(stringOf(word))+lastW > width {
				if len(lines[row].idx) > 0 {
					row++
					lines = append(lines, chunk{})
				}
				lines[row].idx = append(lines[row].idx, word.idx...)
				word = chunk{}
			}
		}
	}

	// Tail flush, mirroring upstream: content spilling past the width opens a
	// new row, and one display-only trailing space is always appended so the
	// soft-wrapped tail behaves like a regular editable row.
	if uniseg.StringWidth(stringOf(lines[row]))+
		uniseg.StringWidth(stringOf(word))+len(spaces.idx) >= width {
		lines = append(lines, chunk{})
		row++
	}
	lines[row].idx = append(lines[row].idx, word.idx...)
	lines[row].idx = append(lines[row].idx, spaces.idx...)
	lines[row].syn = 1

	segs := make([]inputSeg, 0, len(lines))
	consumed := 0
	for _, ln := range lines {
		start := consumed
		if start > len(runes) {
			start = len(runes)
		}
		end := start
		for _, i := range ln.idx {
			if i < len(runes) && i+1 > end {
				end = i + 1
			}
		}
		if end < start {
			end = start
		}
		consumed = end
		text := stringOf(ln) + strings.Repeat(" ", ln.syn)
		segs = append(segs, inputSeg{text: text, start: start, end: end})
	}
	return segs
}

// composerRegion returns the screen-row span covered by the composer's text
// content (inside its border rows), or ok=false when the composer is hidden.
// Row accounting must match View()'s frame assembly; both derive their counts
// from bottomPartsAboveBox.
func (m chatTUI) composerRegion() (top, height int, ok bool) {
	if m.hideComposer() {
		return 0, 0, false
	}
	base := 0
	if !m.nativeScrollback {
		base = m.viewport.Height()
	}
	return base + sumLines(m.bottomPartsAboveBox(m.frameWidth())) + 1, m.input.Height(), true
}

// sumLines counts terminal rows occupied by joined blocks.
func sumLines(parts []string) int {
	n := 0
	for _, p := range parts {
		n += strings.Count(p, "\n") + 1
	}
	return n
}

// bottomPartsAboveBox renders every pinned block that sits above the composer
// box, in View()'s exact order: panels, manager card, working spinner line
// (styled to boxW), footer, queue indicator. Both View() and composer hit-
// testing count rows from this single source so they can never drift apart.
func (m chatTUI) bottomPartsAboveBox(boxW int) []string {
	parts := make([]string, 0, 12)
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	add(m.renderTodoPanel())
	add(m.renderApprovalBanner())
	add(m.renderChooser())
	add(m.renderRewind())
	add(m.renderFlow())
	add(m.renderMCPImport())
	add(m.renderResumePicker())
	add(m.renderCopyPicker())
	add(m.renderCompletion())
	if m.nativeScrollback {
		add(m.renderMainManager())
	}
	if w := m.workingLine(); w != "" {
		add(workingStyle.Width(boxW).MaxWidth(boxW).Render(wrapStatusLine(w, boxW)))
	}
	add(m.renderMainManagerFooter())
	if !m.hideComposer() {
		add(m.renderQueueIndicator())
	}
	return parts
}

// inputCaret maps a screen position to a composer caret in visible-row
// coordinates (row 0 is the first visible composer row), clamped to content.
func (m chatTUI) inputCaret(x, y int) selPos {
	top, height, _ := m.composerRegion()
	rows := wrapInputRows(m.input.Value(), m.input.Width())
	yoff := m.input.ScrollYOffset()

	vis := y - top
	if vis < 0 {
		vis = 0
	}
	if vis > height-1 {
		vis = height - 1
	}
	abs := yoff + vis
	if abs >= len(rows) {
		abs = len(rows) - 1
	}
	if abs < 0 {
		abs = 0
	}
	col := x
	if w := ansi.StringWidth(ansi.Strip(rows[abs].text)); col > w {
		col = w
	}
	if col < 0 {
		col = 0
	}
	return selPos{line: vis, col: col}
}

// selectedInputText converts a composer selection to clipboard text by slicing
// the underlying Value(), so soft wraps never inject phantom newlines and
// multibyte characters survive intact.
func (m chatTUI) selectedInputText(sel selection) string {
	if !sel.active || sel.empty() {
		return ""
	}
	rows := wrapInputRows(m.input.Value(), m.input.Width())
	yoff := m.input.ScrollYOffset()
	start, end := sel.ordered()
	start.line += yoff
	end.line += yoff
	if start.line >= len(rows) {
		return ""
	}
	if end.line >= len(rows) {
		end = selPos{line: len(rows) - 1, col: ansi.StringWidth(rows[len(rows)-1].text)}
	}

	runes := []rune(m.input.Value())
	var b strings.Builder
	for idx := start.line; idx <= end.line; idx++ {
		r := rows[idx]
		lo, hi := r.start, r.end
		if idx == start.line {
			lo += cellsToRunes(r.text, start.col)
			if lo > r.end {
				lo = r.end
			}
		}
		if idx == end.line {
			hi = r.start + cellsToRunes(r.text, end.col)
			if hi > r.end {
				hi = r.end
			}
		}
		if lo < hi && hi <= len(runes) {
			b.WriteString(string(runes[lo:hi]))
		}
		if idx < end.line && rows[idx+1].hard {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// cellsToRunes converts a visual column to a rune index within a rendered
// row's text, snapping left into wide runes.
func cellsToRunes(text string, col int) int {
	rs := []rune(text)
	cells := 0
	for n, r := range rs {
		if cells >= col {
			return n
		}
		w := rw.RuneWidth(r)
		if cells+w > col {
			return n
		}
		cells += w
	}
	return len(rs)
}

// renderInputView returns the composer's view with the active input selection
// reverse-highlighted, mirroring renderTranscript's approach.
func (m chatTUI) renderInputView() string {
	view := m.input.View()
	if !m.inputSel.active || m.inputSel.empty() {
		return view
	}
	lines := strings.Split(view, "\n")
	rows := wrapInputRows(m.input.Value(), m.input.Width())
	yoff := m.input.ScrollYOffset()
	start, end := m.inputSel.ordered()
	start.line += yoff
	end.line += yoff
	for i := range lines {
		abs := yoff + i
		if abs >= len(rows) {
			break
		}
		if lo, hi, ok := selSpan(abs, start, end, ansi.StringWidth(ansi.Strip(lines[i]))); ok {
			lines[i] = lipgloss.StyleRanges(lines[i], lipgloss.NewRange(lo, hi, selStyle))
		}
	}
	return strings.Join(lines, "\n")
}

package cli

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/clipboard"
)

// newSelectionTestTUI builds a model whose composer sits at a known screen
// position: viewport of 10 rows, then the box border, then composer content.
func newSelectionTestTUI(value string) chatTUI {
	m := newTestChatTUI()
	m.viewport = viewport.New(viewport.WithWidth(76))
	m.viewport.SetHeight(10)
	m.input.SetValue(value)
	return m
}

func TestWrapInputRowsSingleLine(t *testing.T) {
	rows := wrapInputRows("привет мир", 80)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].start != 0 || rows[0].end != 10 || !rows[0].hard == false {
		t.Fatalf("unexpected offsets: %+v", rows[0])
	}
	if strings.TrimSpace(rows[0].text) != "привет мир" {
		t.Fatalf("row text %q", rows[0].text)
	}
}

func TestWrapInputRowsSoftWrapOffsets(t *testing.T) {
	long := strings.Repeat("ab ", 40) // 120 chars, wraps at width 80
	rows := wrapInputRows(long, 80)
	if len(rows) < 2 {
		t.Fatalf("expected soft wrap, got %d rows", len(rows))
	}
	for i, r := range rows {
		if r.hard {
			t.Fatalf("row %d marked hard in single-line value", i)
		}
		if r.start > r.end || r.end > len([]rune(long)) {
			t.Fatalf("row %d offsets out of range: %+v", i, r)
		}
	}
	// Rows must tile the value contiguously.
	for i := 1; i < len(rows); i++ {
		if rows[i].start != rows[i-1].end {
			t.Fatalf("gap between rows %d and %d: %d vs %d", i-1, i, rows[i-1].end, rows[i].start)
		}
	}
}

func TestWrapInputRowsMultiLine(t *testing.T) {
	rows := wrapInputRows("alpha beta\ngamma", 40)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if !rows[1].hard {
		t.Fatal("second logical line should be a hard break")
	}
	if rows[0].start != 0 || rows[0].end != 10 {
		t.Fatalf("first row offsets: %+v", rows[0])
	}
	if rows[1].start != 11 || rows[1].end != 16 { // skips the '\n'
		t.Fatalf("second row offsets: %+v", rows[1])
	}
}

func TestComposerSelectionDragAndCopy(t *testing.T) {
	var copied string
	clipboardWrite = func(text string) error { copied = text; return nil }
	defer func() { clipboardWrite = clipboard.Write }()

	m := newSelectionTestTUI("hello прекрасный мир")
	// Composer content starts at viewport height (10) + top border (1).
	click := tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 11}
	out, _ := m.Update(click)
	m2, ok := out.(chatTUI)
	if !ok {
		t.Fatalf("Update returned %T", out)
	}
	drag := tea.MouseMotionMsg{X: 7, Y: 11}
	out, _ = m2.Update(drag)
	m3 := out.(chatTUI)

	if !m3.inputSel.active || m3.inputSel.empty() {
		t.Fatal("drag did not produce a composer selection")
	}
	if got := m3.selectedInputText(m3.inputSel); got != "llo п" {
		t.Fatalf("selected %q, want %q", got, "llo п")
	}

	// Ctrl+C copies the selection.
	cc := tea.KeyPressMsg{Code: 'c', Mod: 4}
	out, cmd := m3.Update(cc)
	m4 := out.(chatTUI)
	if cmd == nil {
		t.Fatal("Ctrl+C with composer selection should return a clipboard cmd")
	}
	cmd()
	if copied != "llo п" {
		t.Fatalf("clipboard %q, want %q", copied, "llo п")
	}
	// Copying must not clear or alter the draft.
	if m4.input.Value() != "hello прекрасный мир" {
		t.Fatalf("draft changed: %q", m4.input.Value())
	}
}

func TestComposerSelectionSpansSoftWrap(t *testing.T) {
	long := strings.Repeat("ab ", 40)
	m := newSelectionTestTUI(long)
	rows := wrapInputRows(m.input.Value(), m.input.Width())
	if len(rows) < 2 {
		t.Skip("value did not wrap")
	}
	// Anchor two cells before the end of the first visual row and drag into
	// the second: the copy must stitch the rows back together without a
	// phantom newline. Cols are caret offsets, so [col,width-2) keeps the
	// row's final two characters.
	sel := selection{
		active: true,
		anchor: selPos{line: 0, col: ansi.StringWidth(rows[0].text) - 2},
		head:   selPos{line: 1, col: 3},
	}
	got := m.selectedInputText(sel)
	wantTail := []rune(long)[rows[0].start:rows[0].end]
	want := string(wantTail[len(wantTail)-2:]) + string([]rune(long)[rows[1].start:rows[1].start+3])
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "\n") {
		t.Fatal("soft wrap must not introduce a newline in copied text")
	}
}

func TestComposerSelectionFullRoundTrip(t *testing.T) {
	long := strings.Repeat("ab ", 40)
	m := newSelectionTestTUI(long)
	rows := wrapInputRows(m.input.Value(), m.input.Width())
	w := ansi.StringWidth(rows[len(rows)-1].text)
	sel := selection{
		active: true,
		anchor: selPos{line: 0, col: 0},
		head:   selPos{line: len(rows) - 1, col: w},
	}
	if got := m.selectedInputText(sel); got != long {
		t.Fatalf("full-span copy not byte-faithful:\n got %q\nwant %q", got, long)
	}
}

func TestComposerSelectionMultiLineKeepsNewlines(t *testing.T) {
	m := newSelectionTestTUI("line one\nline two\nline three")
	// Cols are caret offsets: col 5 of "line one" is between the space and
	// "one"; col 5 of "line three" is between the space and "three".
	sel := selection{active: true, anchor: selPos{line: 0, col: 5}, head: selPos{line: 2, col: 5}}
	got := m.selectedInputText(sel)
	if got != "one\nline two\nline " {
		t.Fatalf("got %q", got)
	}
}

func TestCtrlXPrefersComposerSelection(t *testing.T) {
	var copied string
	clipboardWrite = func(text string) error { copied = text; return nil }
	defer func() { clipboardWrite = clipboard.Write }()

	m := newSelectionTestTUI("keep this draft")
	m.inputSel = selection{
		active: true,
		anchor: selPos{line: 0, col: 5},
		head:   selPos{line: 0, col: 9},
	}

	ctrlX := tea.KeyPressMsg{Code: 'x', Mod: 4}
	out, cmd := m.Update(ctrlX)
	m2 := out.(chatTUI)
	if cmd == nil {
		t.Fatal("expected clipboard cmd")
	}
	cmd()
	if copied != "this" {
		t.Fatalf("clipboard %q, want %q", copied, "this")
	}
	if m2.input.Value() != "keep this draft" {
		t.Fatalf("draft changed: %q", m2.input.Value())
	}
}

func TestEscClearsComposerSelectionOnly(t *testing.T) {
	m := newSelectionTestTUI("some draft")
	m.inputSel = selection{
		active: true,
		anchor: selPos{line: 0, col: 0},
		head:   selPos{line: 0, col: 4},
	}

	out, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m2 := out.(chatTUI)
	if m2.inputSel.active {
		t.Fatal("Esc did not clear the composer selection")
	}
	if m2.input.Value() != "some draft" {
		t.Fatalf("Esc should deselect, not clear the draft: %q", m2.input.Value())
	}
}

func TestTranscriptClickClearsComposerSelection(t *testing.T) {
	m := newSelectionTestTUI("some draft")
	m.inputSel = selection{
		active: true,
		anchor: selPos{line: 0, col: 0},
		head:   selPos{line: 0, col: 4},
	}

	click := tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: 0} // transcript area
	out, _ := m.Update(click)
	m2 := out.(chatTUI)
	if m2.inputSel.active {
		t.Fatal("clicking the transcript should dismiss the composer selection")
	}
}

func TestRenderInputViewHighlightsSelection(t *testing.T) {
	m := newSelectionTestTUI("hello прекрасный мир")
	plain := m.renderInputView()
	m.inputSel = selection{
		active: true,
		anchor: selPos{line: 0, col: 6},
		head:   selPos{line: 0, col: 16},
	}
	got := m.renderInputView()
	if got == plain {
		t.Fatal("selection did not change the rendered composer")
	}
	if !strings.Contains(got, "\x1b[7m") {
		t.Fatalf("expected reverse-video highlight, got %q", got)
	}
	if ansi.StringWidth(ansi.Strip(got)) < ansi.StringWidth(ansi.Strip(plain)) {
		t.Fatal("highlight shrank the composer")
	}
}

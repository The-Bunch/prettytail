package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/The-Bunch/prettytail/internal/detect"
	"github.com/The-Bunch/prettytail/internal/tail"
)

// forceColour makes lipgloss emit ANSI regardless of the test environment so
// the styled status-bar assertions are deterministic.
func init() {
	lipgloss.SetColorProfile(0) // 0 = TrueColor; forces styling on in tests
}

// newTestModel builds a model wired to a nil-safe detector and a closed
// event channel, then sizes it with a WindowSizeMsg so the viewport exists.
func newTestModel(t *testing.T, det Detector) Model {
	t.Helper()
	ch := make(chan tail.Event)
	m := newModel(Config{
		Name:     "sample.md",
		Version:  "0.0.1-dev",
		Detector: det,
		Events:   ch,
	})
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return nm.(Model)
}

func TestHumaniseSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{3_500_000, "3.3 MB"},
		{2 << 30, "2.0 GB"},
	}
	for _, c := range cases {
		if got := humaniseSize(c.n); got != c.want {
			t.Errorf("humaniseSize(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestStatusBarFormatVerbatim(t *testing.T) {
	m := newTestModel(t, func([]byte) detect.Format { return detect.Markdown })
	m.format = detect.NDJSON
	m.size = 1536
	m.state = stateLive

	bar := m.statusBar()
	// The verbatim format is: <name> · <FORMAT> · <state> · <size> · v<version>
	for _, want := range []string{"sample.md", "NDJSON", "● live", "1.5 KB", "v0.0.1-dev"} {
		if !strings.Contains(bar, want) {
			t.Errorf("status bar missing %q; got:\n%q", want, bar)
		}
	}
	// Separators present (dim middot).
	if strings.Count(bar, "·") != 4 {
		t.Errorf("expected 4 separators, got %d in %q", strings.Count(bar, "·"), bar)
	}
}

func TestStatusBarStates(t *testing.T) {
	m := newTestModel(t, func([]byte) detect.Format { return detect.Markdown })
	cases := []struct {
		state followState
		want  string
	}{
		{stateLive, "● live"},
		{statePaused, "⏸ paused — F to follow"},
		{stateWaiting, "⌛ waiting for file"},
	}
	for _, c := range cases {
		m.state = c.state
		if got := m.stateText(); !strings.Contains(got, c.want) {
			t.Errorf("state %d text = %q, want to contain %q", c.state, got, c.want)
		}
	}
}

func TestAppendDataCapsToRenderWindow(t *testing.T) {
	m := newTestModel(t, func([]byte) detect.Format { return detect.Plain })
	// Feed well over the render window in newline-terminated chunks.
	chunk := strings.Repeat("x", 4096) + "\n"
	total := int64(0)
	for i := 0; i < (renderWindow/len(chunk))+50; i++ {
		m.appendData([]byte(chunk))
		total += int64(len(chunk))
	}
	if len(m.buf) > renderWindow {
		t.Errorf("buffer %d exceeds render window %d", len(m.buf), renderWindow)
	}
	// Cumulative size reflects everything seen, not just the retained window.
	if m.size != total {
		t.Errorf("size = %d, want cumulative %d", m.size, total)
	}
	// After a cap, the buffer must start on a clean line boundary.
	if len(m.buf) > 0 && m.buf[0] == '\n' {
		t.Errorf("buffer starts on a bare newline; cut-to-boundary failed")
	}
}

func TestAppendResetReDetects(t *testing.T) {
	calls := 0
	det := func(head []byte) detect.Format {
		calls++
		if strings.HasPrefix(string(head), "{") {
			return detect.NDJSON
		}
		return detect.Markdown
	}
	m := newTestModel(t, det)

	// A Reset with NDJSON content must reclassify the format badge.
	nm, _ := m.handleTail(tail.Event{Kind: tail.Reset, Data: []byte("{\"a\":1}\n{\"b\":2}\n")})
	m = nm.(Model)
	if m.format != detect.NDJSON {
		t.Errorf("after NDJSON Reset, format = %v, want NDJSON", m.format)
	}
	if calls == 0 {
		t.Error("detector was never called on Reset")
	}
}

func TestScrollUpUnpinsAppendRepins(t *testing.T) {
	m := newTestModel(t, func([]byte) detect.Format { return detect.Plain })
	// Fill with enough content that the viewport can scroll. Plain keeps one
	// line per input line, so 200 lines comfortably exceeds a 23-row view.
	m.format = detect.Plain
	m.buf = []byte(strings.Repeat("line\n", 200))
	m.rerender()
	if m.state != stateLive {
		t.Fatalf("expected live after render, got %d", m.state)
	}

	// Scroll up: unpins → paused.
	nm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	m = nm.(Model)
	if m.pinned {
		t.Error("expected unpinned after scroll up")
	}
	if m.state != statePaused {
		t.Errorf("expected paused state after scroll up, got %d", m.state)
	}

	// F re-pins and jumps to the bottom.
	nm, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	m = nm.(Model)
	if !m.pinned {
		t.Error("expected pinned after F")
	}
	if m.state != stateLive {
		t.Errorf("expected live after F, got %d", m.state)
	}
}

// TestFNeverTogglesOff pins the F-key contract: F is not
// a toggle. Pressing F while already live must leave the view pinned/live, and
// appended content while paused must be reachable by pressing F (re-pin snaps
// to the newest). A toggle regression (F unpins a live view) fails here.
func TestFNeverTogglesOff(t *testing.T) {
	m := newTestModel(t, func([]byte) detect.Format { return detect.Plain })
	m.format = detect.Plain
	m.buf = []byte(strings.Repeat("line\n", 200))
	m.rerender()
	if !m.pinned || m.state != stateLive {
		t.Fatalf("expected live/pinned after render, got pinned=%v state=%d", m.pinned, m.state)
	}

	// F while live: must STAY pinned/live (no toggle off).
	nm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	m = nm.(Model)
	if !m.pinned {
		t.Error("F while live unpinned the view; F must never toggle off")
	}
	if m.state != stateLive {
		t.Errorf("F while live changed state to %d, want stateLive", m.state)
	}
	if !m.viewport.AtBottom() {
		t.Error("F while live moved off the bottom; F should snap to newest")
	}

	// A second F: still pinned/live (idempotent).
	nm, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	m = nm.(Model)
	if !m.pinned || m.state != stateLive {
		t.Errorf("second F flipped state; want pinned/live, got pinned=%v state=%d", m.pinned, m.state)
	}
}

// TestPauseHoldsUnderAppend covers a pause-hold regression: once an upward
// scroll pauses the view, a subsequent append must not re-pin it. This is the
// unit-level guard for regression (ii): appends never mutate the pin while
// paused, and the debounce re-render does not drag a paused view to the bottom.
func TestPauseHoldsUnderAppend(t *testing.T) {
	m := newTestModel(t, func([]byte) detect.Format { return detect.Plain })
	m.format = detect.Plain
	m.buf = []byte(strings.Repeat("line\n", 200))
	m.rerender()

	// Scroll up → paused.
	nm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	m = nm.(Model)
	if m.pinned || m.state != statePaused {
		t.Fatalf("expected paused after scroll up, got pinned=%v state=%d", m.pinned, m.state)
	}
	offsetBefore := m.viewport.YOffset

	// Append while paused, then let the debounce re-render fire.
	nm, _ = m.handleTail(tail.Event{Kind: tail.Append, Data: []byte(strings.Repeat("more\n", 50))})
	m = nm.(Model)
	nm, _ = m.Update(renderTickMsg{})
	m = nm.(Model)

	if m.pinned {
		t.Error("append while paused re-pinned the view; pause must hold")
	}
	if m.state != statePaused {
		t.Errorf("state = %d after append while paused, want statePaused", m.state)
	}
	if m.viewport.AtBottom() {
		t.Error("paused viewport dragged to the bottom by the append re-render")
	}
	if m.viewport.YOffset != offsetBefore {
		t.Errorf("paused viewport YOffset moved from %d to %d on append", offsetBefore, m.viewport.YOffset)
	}
}

func TestWaitingThenAppendGoesLive(t *testing.T) {
	m := newTestModel(t, func([]byte) detect.Format { return detect.Markdown })

	nm, _ := m.handleTail(tail.Event{Kind: tail.Waiting})
	m = nm.(Model)
	if m.state != stateWaiting {
		t.Fatalf("expected waiting, got %d", m.state)
	}

	nm, _ = m.handleTail(tail.Event{Kind: tail.Append, Data: []byte("# hi\n")})
	m = nm.(Model)
	if m.state == stateWaiting {
		t.Error("expected to leave waiting after content arrived")
	}
	if !m.pinned {
		t.Error("expected pinned (live) after content arrived")
	}
}

func TestQuitKeys(t *testing.T) {
	m := newTestModel(t, func([]byte) detect.Format { return detect.Markdown })
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyCtrlC},
	} {
		_, cmd := m.handleKey(k)
		if cmd == nil {
			t.Errorf("key %v produced no command; expected Quit", k)
			continue
		}
		if msg := cmd(); msg == nil {
			t.Errorf("key %v command returned nil msg", k)
		}
	}
}

func TestTailErrQuits(t *testing.T) {
	m := newTestModel(t, func([]byte) detect.Format { return detect.Markdown })
	_, cmd := m.handleTail(tail.Event{Kind: tail.Err})
	if cmd == nil {
		t.Fatal("expected a Quit command on tail Err")
	}
}

// TestGUnpinsShortDocument covers spec §6's rule that g unpins even when
// the whole document fits on one screen (so the viewport still reports
// AtBottom and syncPin alone would wrongly keep it pinned).
func TestGUnpinsShortDocument(t *testing.T) {
	m := newTestModel(t, func([]byte) detect.Format { return detect.Plain })
	m.format = detect.Plain
	m.buf = []byte("one\ntwo\nthree\n") // fits well within a 23-row viewport
	m.rerender()
	if !m.pinned {
		t.Fatalf("expected pinned after render of short doc")
	}

	nm, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	m = nm.(Model)
	if m.pinned {
		t.Error("expected g to unpin even on a short document")
	}
	if m.state != statePaused {
		t.Errorf("expected paused after g, got %d", m.state)
	}
}

// TestRunRestoresOnCtxCancel exercises the real Run teardown: a cancelled
// context must make Run return promptly (bubbletea's deferred shutdown
// restores the terminal on that return) and the ctx-bridge goroutine must be
// released, not leaked. It drives the program with a pipe input and a
// disabled signal handler so it never touches the real terminal.
func TestRunRestoresOnCtxCancel(t *testing.T) {
	ch := make(chan tail.Event)
	prog := newProgram(
		Config{Name: "x.md", Version: "0.0.1-dev", Detector: func([]byte) detect.Format { return detect.Markdown }, Events: ch},
		tea.WithInput(strings.NewReader("")), // no keystrokes
		tea.WithOutput(&bytes.Buffer{}),
		tea.WithoutSignalHandler(),
		tea.WithoutRenderer(), // no ANSI to a real screen; teardown path still runs
	)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- prog.Run(ctx) }()

	// Let the program start, then cancel: the ctx bridge should Quit it.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("Run returned error on ctx cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after ctx cancel — teardown/bridge leak")
	}
}

func TestDebounceCoalesces(t *testing.T) {
	m := newTestModel(t, func([]byte) detect.Format { return detect.Markdown })

	// First append arms one debounce tick.
	nm, cmd1 := m.handleTail(tail.Event{Kind: tail.Append, Data: []byte("a\n")})
	m = nm.(Model)
	if !m.pending {
		t.Fatal("first append should arm a debounce tick")
	}
	if cmd1 == nil {
		t.Fatal("first append should return commands (tick + pump)")
	}

	// A second append while pending must NOT arm a second tick.
	pendingBefore := m.pending
	nm, _ = m.handleTail(tail.Event{Kind: tail.Append, Data: []byte("b\n")})
	m = nm.(Model)
	if !pendingBefore || !m.pending {
		t.Error("pending flag should stay set across a burst")
	}

	// The render tick clears pending and the dirty flag.
	nm, _ = m.Update(renderTickMsg{})
	m = nm.(Model)
	if m.pending {
		t.Error("render tick should clear pending")
	}
	if m.dirty {
		t.Error("render tick should clear dirty after rendering")
	}
}

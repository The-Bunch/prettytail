// Package ui implements prettytail's full-screen follow view:
// a bubbletea program with a scrolling viewport and a one-row status bar.
// It consumes tail Events, coalesces bursts of activity behind
// a 100ms debounce and re-renders the whole tail window via the render
// package so block-structured formats stay correct as the file grows.
//
// Terminal hygiene: bubbletea's alt-screen
// teardown restores the main screen and cursor on q/ctrl+c and on a normal
// program end; Run wraps the program so error and signal paths restore too.
package ui

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/The-Bunch/prettytail/internal/detect"
	"github.com/The-Bunch/prettytail/internal/render"
	"github.com/The-Bunch/prettytail/internal/tail"
)

// renderWindow caps how much of the document glamour ever sees on the TUI
// path: the last 512 KiB of the tail buffer.
const renderWindow = 512 << 10

// debounceInterval coalesces a burst of tail events into one re-render:
// dirty states within this window collapse to a single
// RenderDoc pass.
const debounceInterval = 100 * time.Millisecond

// statusBarHeight is the single row the status bar occupies at the bottom.
const statusBarHeight = 1

// followState is the current follow-pin state, surfaced in the status bar.
type followState int

const (
	stateLive    followState = iota // pinned to bottom, following
	statePaused                     // unpinned by an upward scroll
	stateWaiting                    // file absent, waiting for it to appear
)

// Detector resolves the format from the current content head. The caller
// (cli) supplies it so --format overrides and the binary guard live in one
// place; Reset re-runs it so a rotated file changing shape is reclassified.
type Detector func(head []byte) detect.Format

// headBytes is the detection sample size: first 8 KiB.
const headBytes = 8 * 1024

// Msgs pumped into the bubbletea program from the tail goroutine.
type (
	// tailMsg carries one tail Event into the update loop.
	tailMsg tail.Event
	// tailClosedMsg signals the tail channel closed (context ended).
	tailClosedMsg struct{}
	// renderTickMsg fires after the debounce window to trigger a re-render.
	renderTickMsg struct{}
)

// Model is the bubbletea model for the follow view.
type Model struct {
	name     string   // base filename for the status bar
	version  string   // cli version for the status bar stamp
	detect   Detector // format resolver (re-run on Reset)
	width    int
	height   int
	ready    bool // first WindowSizeMsg seen (viewport sized)
	viewport viewport.Model

	format detect.Format
	buf    []byte // accumulated tail-window bytes (capped to renderWindow)
	size   int64  // cumulative bytes seen (for the humanised status size)

	pinned  bool        // following the bottom
	state   followState // derived status-bar state
	dirty   bool        // content changed since last render
	pending bool        // a debounce tick is in flight

	events <-chan tail.Event // tail source
}

// Config carries the immutable inputs for a follow session.
type Config struct {
	Name    string
	Version string
	// Format is the initial format the follow view renders with, resolved
	// by the caller from the file head. The TUI never runs its
	// own initial detection — the first content arrives as an Append, which
	// does not detect — so without this seed the model would fall back to
	// its zero value (Markdown) and mis-render a console/log file until a
	// Reset or `r`. Detector re-runs on Reset/`r`; this seeds the first
	// render.
	Format   detect.Format
	Detector Detector
	Events   <-chan tail.Event
}

// newModel builds the initial model. The viewport is sized on the first
// WindowSizeMsg, so it starts zero-valued and !ready. m.format is seeded
// from cfg.Format so the very first render uses the format the
// caller detected from the head, not the zero-value Markdown.
func newModel(cfg Config) Model {
	return Model{
		name:    cfg.Name,
		version: cfg.Version,
		detect:  cfg.Detector,
		format:  cfg.Format,
		pinned:  true,
		state:   stateLive,
		events:  cfg.Events,
	}
}

// Init starts the tail pump.
func (m Model) Init() tea.Cmd {
	return waitForEvent(m.events)
}

// waitForEvent returns a command that blocks on the next tail event and
// wraps it as a bubbletea msg. A closed channel yields tailClosedMsg.
func waitForEvent(events <-chan tail.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return tailClosedMsg{}
		}
		return tailMsg(ev)
	}
}

// scheduleRender returns a command that fires renderTickMsg after the
// debounce window, coalescing bursts of dirty events into one re-render.
func scheduleRender() tea.Cmd {
	return tea.Tick(debounceInterval, func(time.Time) tea.Msg {
		return renderTickMsg{}
	})
}

// Update handles keys, resize, tail events and the debounce tick.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.WindowSizeMsg:
		return m.handleResize(msg)

	case tailMsg:
		return m.handleTail(tail.Event(msg))

	case tailClosedMsg:
		return m, tea.Quit

	case renderTickMsg:
		m.pending = false
		if m.dirty {
			m.rerender()
		}
		return m, nil
	}

	// Delegate anything else (e.g. mouse wheel) to the viewport, then
	// reconcile the follow-pin state from where the scroll landed.
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	m.syncPin()
	return m, cmd
}

// handleKey implements the keymap from spec §6.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		// Any upward scroll unpins immediately. We unpin on the
		// key itself rather than deriving from AtBottom() afterwards: under
		// live appends a one-line scroll from the bottom can leave the growing
		// doc still reporting AtBottom, which would fail to pause. Explicit
		// unpin makes pause engage the instant the user scrolls up.
		m.viewport.ScrollUp(1)
		m.unpin()
	case "down", "j":
		m.scrollDownSyncPin(func() { m.viewport.ScrollDown(1) })
	case "pgup":
		m.viewport.PageUp()
		m.unpin()
	case "pgdown", " ":
		m.scrollDownSyncPin(func() { m.viewport.PageDown() })
	case "g":
		// g is an explicit upward jump: it always unpins (spec §6 lists g
		// among the scrolls that unpin), even when the whole document fits on
		// one screen and the viewport still reports AtBottom.
		m.viewport.GotoTop()
		m.unpin()
	case "G":
		m.viewport.GotoBottom()
		m.repin()
	case "f", "F":
		// F ALWAYS re-pins and snaps to the newest content (spec §6:
		// "F or scrolling back to the bottom re-pins"; spec §5: "F snaps back
		// to live"). It is never a toggle: F while live is a harmless no-op
		// that stays pinned, F while paused pins and jumps to the bottom.
		m.repin()
		m.viewport.GotoBottom()
	case "r":
		// Force re-detect + re-render.
		m.redetect()
		m.rerender()
	}
	return m, nil
}

// handleResize sizes (or resizes) the viewport, invalidates the cached
// renderer for the new width and re-renders.
func (m Model) handleResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	vpHeight := msg.Height - statusBarHeight
	if vpHeight < 1 {
		vpHeight = 1
	}
	if !m.ready {
		m.viewport = viewport.New(msg.Width, vpHeight)
		m.ready = true
	} else {
		m.viewport.Width = msg.Width
		m.viewport.Height = vpHeight
	}
	render.InvalidateWidth(m.contentWidth())
	m.rerender()
	return m, nil
}

// handleTail folds one tail event into the model. Append/Reset mark the doc
// dirty and arm the debounce; Waiting flips the status bar; Err ends the
// program (the caller surfaces it). The pump command is re-issued so the
// next event is awaited.
func (m Model) handleTail(ev tail.Event) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch ev.Kind {
	case tail.Append:
		m.appendData(ev.Data)
		m.markDirty()
		if m.state == stateWaiting {
			m.state = stateLive
			m.pinned = true
		}
	case tail.Reset:
		m.buf = nil
		m.size = 0
		m.appendData(ev.Data)
		m.redetect()
		m.markDirty()
		if m.state == stateWaiting {
			m.pinned = true
		}
		m.refreshState()
	case tail.Waiting:
		m.state = stateWaiting
	case tail.Err:
		return m, tea.Quit
	}

	// Arm one debounce tick per burst; the tick coalesces dirty states.
	if m.dirty && !m.pending {
		m.pending = true
		cmds = append(cmds, scheduleRender())
	}
	cmds = append(cmds, waitForEvent(m.events))
	return m, tea.Batch(cmds...)
}

// appendData appends new bytes to the tail buffer, capping it to the last
// renderWindow bytes cut forward to a line boundary so glamour never sees
// unbounded input.
func (m *Model) appendData(data []byte) {
	if len(data) == 0 {
		return
	}
	m.size += int64(len(data))
	m.buf = append(m.buf, data...)
	if len(m.buf) > renderWindow {
		cut := m.buf[len(m.buf)-renderWindow:]
		if idx := indexByte(cut, '\n'); idx >= 0 {
			cut = cut[idx+1:]
		}
		// Copy so the retained slice does not pin the larger backing array.
		m.buf = append([]byte(nil), cut...)
	}
}

// markDirty flags the content for the next debounce tick.
func (m *Model) markDirty() { m.dirty = true }

// redetect re-runs format detection on the current head (spec §6: Reset
// re-detects; r forces it).
func (m *Model) redetect() {
	head := m.buf
	if len(head) > headBytes {
		head = head[:headBytes]
	}
	if m.detect != nil {
		m.format = m.detect(head)
	}
}

// rerender renders the tail window at the content width and pushes it into
// the viewport, staying pinned to the bottom if following. A renderer error
// falls back to the raw bytes — a display tool never dies on a
// styling hiccup.
func (m *Model) rerender() {
	m.dirty = false
	if !m.ready {
		return
	}
	out, err := render.For(m.format).RenderDoc(m.buf, m.contentWidth())
	if err != nil {
		out = string(m.buf)
	}
	m.viewport.SetContent(out)
	if m.pinned {
		m.viewport.GotoBottom()
	}
	m.refreshState()
}

// contentWidth is the width handed to renderers: the viewport width.
func (m Model) contentWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 80
}

// scrollDownSyncPin performs a downward scroll and re-pins ONLY if the user
// scrolled all the way back to the bottom (spec §6: "scrolling back to the
// bottom re-pins"). A downward scroll that stops short of the bottom leaves
// the view paused. This is the only viewport-position-derived re-pin, and it
// fires solely off a user scroll action — never off content growth, so a
// paused view never re-pins itself when the file appends.
func (m *Model) scrollDownSyncPin(scroll func()) {
	scroll()
	if m.state == stateWaiting {
		return
	}
	if m.viewport.AtBottom() {
		m.repin()
	}
}

// syncPin reconciles the pin flag with the viewport position after a
// user-driven scroll delegated to the viewport (e.g. the mouse wheel): landing
// at the bottom re-pins, scrolling up unpins. It is only ever
// called immediately after a user scroll msg, never as a side effect of a
// content append or re-render, so a paused view holds under live growth.
func (m *Model) syncPin() {
	if m.state == stateWaiting {
		return
	}
	if m.viewport.AtBottom() {
		m.pinned = true
	} else {
		m.pinned = false
	}
	m.refreshState()
}

// repin forces the follow pin on (used by G, which jumps to the bottom).
func (m *Model) repin() {
	if m.state == stateWaiting {
		return
	}
	m.pinned = true
	m.refreshState()
}

// unpin forces the follow pin off (used by g, which jumps to the top). It is
// distinct from syncPin because a short document leaves the viewport at the
// bottom even after GotoTop, which syncPin would (wrongly) treat as pinned.
func (m *Model) unpin() {
	if m.state == stateWaiting {
		return
	}
	m.pinned = false
	m.refreshState()
}

// refreshState derives the live/paused status from the pin flag, unless the
// file is currently absent (waiting wins until content returns).
func (m *Model) refreshState() {
	if m.state == stateWaiting {
		return
	}
	if m.pinned {
		m.state = stateLive
	} else {
		m.state = statePaused
	}
}

// View renders the viewport above the status bar.
func (m Model) View() string {
	if !m.ready {
		return "\n  initialising…"
	}
	return m.viewport.View() + "\n" + m.statusBar()
}

// Program wraps a bubbletea program so callers can Run it and be sure the
// terminal is restored on every exit path.
type Program struct {
	prog *tea.Program
}

// New builds a follow-view Program writing to out. Alt-screen and mouse
// cell-motion (wheel scroll) are enabled per spec §6.
func New(cfg Config, out io.Writer) *Program {
	return newProgram(cfg, tea.WithOutput(out), tea.WithAltScreen(), tea.WithMouseCellMotion())
}

// newProgram is the shared constructor behind New; it exists so tests can
// supply program options (e.g. a pipe input and a disabled signal handler) to
// exercise Run's teardown without touching the real terminal.
func newProgram(cfg Config, opts ...tea.ProgramOption) *Program {
	return &Program{prog: tea.NewProgram(newModel(cfg), opts...)}
}

// Run starts the program and blocks until it exits, always leaving the
// terminal on the main screen with the cursor visible (spec §6, a hard
// Accept item). bubbletea's Run defers an internal shutdown that calls
// restoreTerminalState on EVERY return — clean quit (q/ctrl+c, ctx-driven
// Quit), runtime error, and recovered panic alike — so the restore is
// guaranteed the moment Run returns; this wrapper therefore never needs to
// force a teardown itself (calling Kill after Run has returned would be
// wrong, not defensive). ctx cancellation (SIGINT/SIGTERM, wired via
// signal.NotifyContext in cli.run) is bridged to prog.Quit so signal-driven
// shutdown runs that same graceful teardown.
func (p *Program) Run(ctx context.Context) error {
	// Bridge context cancellation to a Quit so signal-driven shutdown runs
	// bubbletea's normal teardown (which restores the terminal). The done
	// channel releases this goroutine when Run returns for any other reason,
	// so it never leaks.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			p.prog.Quit()
		case <-done:
		}
	}()

	if _, err := p.prog.Run(); err != nil {
		// The terminal is already restored (bubbletea's deferred shutdown ran
		// before Run returned); just surface the error.
		return fmt.Errorf("ui: run follow view: %w", err)
	}
	return nil
}

// --- status bar -------------------------------------------------------------

// Status-bar colours. Adaptive dark/light; dark values are
// normative, light chosen to match tone.
var (
	sepStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#9A9A9A", Dark: "#626262"})
	nameStyle  = lipgloss.NewStyle().Bold(true)
	badgeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#005FAF", Dark: "#5FAFFF"})
	liveStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0A7D28", Dark: "#00D75F"})
	pausdStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B58900", Dark: "#FFD700"})
	waitStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#9A9A9A", Dark: "#8A8A8A"})
	barStyle   = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "#EEEEEE", Dark: "#303030"})
)

// stateText returns the styled status word for the current follow state,
// verbatim per spec §6.
func (m Model) stateText() string {
	switch m.state {
	case stateLive:
		return liveStyle.Render("● live")
	case statePaused:
		return pausdStyle.Render("⏸ paused — F to follow")
	default:
		return waitStyle.Render("⌛ waiting for file")
	}
}

// statusBar composes the verbatim status-bar format from spec §6:
//
//	<name> · <FORMAT> · <state> · <size> · v<version>
func (m Model) statusBar() string {
	sep := sepStyle.Render(" · ")
	parts := []string{
		nameStyle.Render(m.name),
		badgeStyle.Render(m.format.String()),
		m.stateText(),
		humaniseSize(m.size),
		"v" + m.version,
	}
	line := ""
	for i, p := range parts {
		if i > 0 {
			line += sep
		}
		line += p
	}
	return barStyle.Width(m.width).Render(line)
}

// humaniseSize formats a byte count as the status bar wants it (spec §6:
// "1.2 KB", "3.4 MB"). Below 1 KB it reads in bytes.
func humaniseSize(n int64) string {
	const (
		kb = 1 << 10
		mb = 1 << 20
		gb = 1 << 30
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/gb)
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/kb)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// indexByte is a tiny wrapper kept local so ui has no bytes import churn.
func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

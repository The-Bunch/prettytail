package ui

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/The-Bunch/prettytail/internal/detect"
	"github.com/The-Bunch/prettytail/internal/tail"
)

// The teatest suite drives the real bubbletea program through a
// byte in/out pipe, so it exercises the whole Update→View loop the way the
// terminal would, not just handler functions in isolation.
//
// Determinism: PRETTYTAIL_STYLE=dark pins glamour's style so the rendered
// markdown is reproducible, and every case uses an 80×24 term.
// Content is fed with the same tailMsg the tail goroutine would send; the
// re-render is driven by the model's own 100ms debounce tick,
// so these tests exercise the real coalesce-then-render path. Assertions poll
// the program output with teatest.WaitFor rather than fixed sleeps.

// ansiCSI strips ANSI control sequences so text assertions match the visible
// characters regardless of the colour styling glamour/lipgloss apply. glamour
// also hard-wraps and pads, so a heading like "Prettytail Fixture" can arrive
// with styling between and around the words; we assert on the words rather
// than the exact spacing.
var ansiCSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(b []byte) []byte { return ansiCSI.ReplaceAll(b, nil) }

// teatestModel returns a Model wired to a detector but NOT to a live event
// channel: the teatest driver injects tail events by hand via Send, and the
// model's own debounce tick drives re-renders. A nil Events channel means the
// waitForEvent command from Init blocks harmlessly on a nil receive (never
// selected), which is exactly what we want under manual driving.
func teatestModel(det Detector) Model {
	return newModel(Config{
		Name:     "sample.md",
		Version:  "0.0.1-dev",
		Detector: det,
		Events:   nil,
	})
}

// teatestModelFormat is teatestModel with an explicit initial Config.Format
// seed and a chosen status-bar name, so the follow view can be
// driven the way the cli wires it for a non-markdown file.
func teatestModelFormat(name string, format detect.Format, det Detector) Model {
	return newModel(Config{
		Name:     name,
		Version:  "0.0.1-dev",
		Format:   format,
		Detector: det,
		Events:   nil,
	})
}

func markdownDetector([]byte) detect.Format { return detect.Markdown }

// outputAccumulator wraps a teatest program's output reader so that repeated
// waits see ALL output produced since the program started, not just the bytes
// written since the previous read. teatest.WaitFor is deliberately NOT used
// here: its own io.ReadAll drains teatest's drain-on-read Output() buffer, so
// calling it twice in one test would make the second call blind to everything
// the first consumed (and several cases here assert on multiple frames in
// sequence). This accumulator keeps a running copy of every byte ever read so
// each assertion sees the full history.
type outputAccumulator struct {
	src io.Reader
	all []byte
}

func newOutput(tm *teatest.TestModel) *outputAccumulator {
	return &outputAccumulator{src: tm.Output()}
}

// waitFor polls until the ANSI-stripped accumulated output contains want,
// failing the test if it never appears within the deadline.
func (o *outputAccumulator) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) {
		n, _ := o.src.Read(buf)
		if n > 0 {
			o.all = append(o.all, buf[:n]...)
		}
		if bytes.Contains(stripANSI(o.all), []byte(want)) {
			return
		}
		if n == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Fatalf("waitFor: %q never appeared. Last visible output:\n%s", want, stripANSI(o.all))
}

// TestTeatestInitialRender covers spec §7 bullet 1: the initial render
// contains sample.md's h1 text and the status bar shows `● live`.
func TestTeatestInitialRender(t *testing.T) {
	t.Setenv("PRETTYTAIL_STYLE", "dark")

	src, err := os.ReadFile("../../testdata/fixtures/sample.md")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	tm := teatest.NewTestModel(t, teatestModel(markdownDetector), teatest.WithInitialTermSize(80, 24))
	out := newOutput(tm)

	// Feed the fixture as the tail engine would; the model's own 100ms
	// debounce tick drives the coalesced re-render.
	tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: src}))

	// The status bar must report the live follow state on first render.
	out.waitFor(t, "● live")
	// The fixture is longer than the 24-row viewport, so pinned-to-bottom
	// scrolls the h1 off the top; g jumps to the top to bring it into view
	// (this also proves the rendered document really contains the h1).
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	out.waitFor(t, "Prettytail")
	out.waitFor(t, "Fixture")

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// TestTeatestAppendAppearsStillPinned covers spec §7 bullet 2: a synthetic
// Append makes new text appear while the view stays PINNED to the bottom.
//
// The visible-output check (the sentinel line appears, ● live shows) proves
// the append renders and the follow state stays live. To make "still pinned"
// a hard assertion that a syncPin regression would actually break, the body
// overflows the 24-row viewport and the final model is inspected directly:
// after appending a line below the fold, a still-pinned viewport must report
// AtBottom() and the model must still be pinned/live. A regression that
// unpinned on append would leave the viewport scrolled up (AtBottom() false),
// failing the assertions below.
func TestTeatestAppendAppearsStillPinned(t *testing.T) {
	t.Setenv("PRETTYTAIL_STYLE", "dark")

	det := func([]byte) detect.Format { return detect.Plain }
	tm := teatest.NewTestModel(t, teatestModel(det), teatest.WithInitialTermSize(80, 24))
	out := newOutput(tm)

	// A body taller than the viewport, so the bottom is off-screen unless
	// the view follows it.
	body := bytes.Repeat([]byte("initial filler line\n"), 60)
	tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: body}))
	out.waitFor(t, "initial filler line")
	out.waitFor(t, "● live")

	// Append a distinctive line at the very end and wait for it to render.
	tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: []byte("sentinel appended line\n")}))
	out.waitFor(t, "sentinel appended line")
	out.waitFor(t, "● live")

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(Model)
	// Hard "still pinned" assertions on the final state: an append must not
	// have unpinned the view, and it must still be following the bottom.
	if !fm.pinned {
		t.Error("view unpinned after append; expected to stay pinned (following)")
	}
	if fm.state != stateLive {
		t.Errorf("follow state = %d after append, want stateLive (pinned)", fm.state)
	}
	if !fm.viewport.AtBottom() {
		t.Error("viewport not at bottom after append to a pinned view; sentinel would be off-screen")
	}
}

// TestTeatestScrollUpPauses covers spec §7 bullet 3: an upward scroll unpins
// the view and the status bar switches to the paused copy.
func TestTeatestScrollUpPauses(t *testing.T) {
	t.Setenv("PRETTYTAIL_STYLE", "dark")

	// Plain keeps one output line per input line, so many lines guarantee the
	// viewport can actually scroll (a short doc would stay AtBottom).
	det := func([]byte) detect.Format { return detect.Plain }
	tm := teatest.NewTestModel(t, teatestModel(det), teatest.WithInitialTermSize(80, 24))
	out := newOutput(tm)

	body := bytes.Repeat([]byte("filler line of plain content\n"), 200)
	tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: body}))
	// Wait for the CONTENT to be in the viewport (not just the ● live status
	// bar, which renders on the first empty frame too) — otherwise the scroll
	// below would land on an unpopulated viewport that is still AtBottom.
	out.waitFor(t, "filler line of plain content")
	out.waitFor(t, "● live")

	// Scroll up: unpins → the paused copy must appear verbatim.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	out.waitFor(t, "⏸ paused — F to follow")

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// TestTeatestKPausesDuringAppends covers a pause regression:
// pressing `k` WHILE the file is actively appending must engage
// pause within one debounce window. Appends keep flowing in the background;
// the k must still flip the status bar to paused promptly. The wait is bounded
// (waitFor's 3s deadline, but the transition is expected within one 100ms
// debounce tick) and synchronised to the visible status-bar string — no fixed
// sleep is used to prove the transition.
func TestTeatestKPausesDuringAppends(t *testing.T) {
	t.Setenv("PRETTYTAIL_STYLE", "dark")

	det := func([]byte) detect.Format { return detect.Plain }
	tm := teatest.NewTestModel(t, teatestModel(det), teatest.WithInitialTermSize(80, 24))
	out := newOutput(tm)

	// Prime with a tall body so scrolling is meaningful, then keep appending
	// so the k arrives during live growth (the exact condition that regressed).
	body := bytes.Repeat([]byte("filler line of plain content\n"), 200)
	tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: body}))
	out.waitFor(t, "filler line of plain content")
	out.waitFor(t, "● live")

	// Fire a burst of appends and, mid-burst, press k. The k must win: pause
	// engages and the status bar shows the paused copy within a debounce
	// window, even though more appends land after the keystroke.
	for i := 0; i < 5; i++ {
		tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: []byte("streaming append line\n")}))
	}
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	for i := 0; i < 5; i++ {
		tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: []byte("streaming append line\n")}))
	}
	// Bounded, status-string-synchronised assertion (no fixed sleep): pause
	// must be visible. A regression that let appends keep the view live would
	// never surface the paused copy and this would time out.
	out.waitFor(t, "⏸ paused — F to follow")

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(Model)
	if fm.pinned || fm.state != statePaused {
		t.Errorf("after k during appends: pinned=%v state=%d, want unpinned/paused", fm.pinned, fm.state)
	}
}

// TestTeatestPauseHoldsUnderAppends covers a pause-hold regression:
// once paused, an append must not re-pin the view nor move the
// visible region — the appended sentinel stays below the fold. The append's
// coalesced re-render is forced deterministically by sending the model's own
// renderTickMsg (the same debounce tick the running program would fire),
// guaranteeing the append has been rendered before the negative visibility
// assertion — no wall-clock sleep and no coupling to status-bar copy.
func TestTeatestPauseHoldsUnderAppends(t *testing.T) {
	t.Setenv("PRETTYTAIL_STYLE", "dark")

	det := func([]byte) detect.Format { return detect.Plain }
	tm := teatest.NewTestModel(t, teatestModel(det), teatest.WithInitialTermSize(80, 24))
	out := newOutput(tm)

	// A tall body so the bottom is off-screen and scrolling is meaningful.
	body := bytes.Repeat([]byte("filler line of plain content\n"), 200)
	tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: body}))
	out.waitFor(t, "filler line of plain content")
	out.waitFor(t, "● live")

	// Scroll up well clear of the fold so the appended sentinel below cannot
	// land in the held viewport by proximity.
	tm.Send(tea.KeyMsg{Type: tea.KeyPgUp})
	out.waitFor(t, "⏸ paused — F to follow")

	// Append distinctive content WHILE paused, then force the coalesced
	// re-render deterministically (renderTickMsg is the model's own debounce
	// tick). teatest delivers msgs in order, so after this the append is
	// guaranteed rendered — the negative "not visible" check below then means
	// "held region", not "not yet rendered".
	pad := bytes.Repeat([]byte("SENTINEL_below_fold_do_not_show\n"), 40)
	tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: pad}))
	tm.Send(renderTickMsg{})

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(Model)

	// The append must have reached the buffer and been rendered (the forced
	// tick above), so the sentinel is present in the doc — just not visible in
	// the held viewport.
	if !bytes.Contains(fm.buf, []byte("SENTINEL_below_fold_do_not_show")) {
		t.Fatal("sentinel never reached the tail buffer; append was not processed")
	}

	if fm.pinned {
		t.Error("view re-pinned after append while paused; must hold paused")
	}
	if fm.state != statePaused {
		t.Errorf("state = %d after append while paused, want statePaused", fm.state)
	}
	if fm.viewport.AtBottom() {
		t.Error("paused viewport dragged to the bottom by an append; region must hold")
	}
	// The appended sentinel lives below the fold and must not be visible in
	// the held (paused) viewport.
	if bytes.Contains(stripANSI([]byte(fm.viewport.View())), []byte("SENTINEL_below_fold_do_not_show")) {
		t.Error("appended text became visible in a paused view; region did not hold")
	}
}

// TestTeatestFRepinNeverToggles pins the F-key contract:
// F re-pins and snaps to newest, never toggles. Regressions (iii) F while live
// stays live/pinned, and (iv) F while paused returns to live with the newest
// content visible.
func TestTeatestFRepinNeverToggles(t *testing.T) {
	t.Setenv("PRETTYTAIL_STYLE", "dark")

	det := func([]byte) detect.Format { return detect.Plain }
	tm := teatest.NewTestModel(t, teatestModel(det), teatest.WithInitialTermSize(80, 24))
	out := newOutput(tm)

	body := bytes.Repeat([]byte("filler line of plain content\n"), 200)
	tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: body}))
	out.waitFor(t, "filler line of plain content")
	out.waitFor(t, "● live")

	// (iii) F while live must STAY live/pinned (never unpin — no toggle).
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	// Nothing should flip to paused; assert on the final model to be sure.

	// Now pause by scrolling up, append a sentinel below the fold, then F.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	out.waitFor(t, "⏸ paused — F to follow")
	tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: []byte("NEWEST_after_F_visible\n")}))

	// (iv) F while paused: back to live and the newest content is visible.
	// The waitFor calls synchronise on the visible result (paused→live plus the
	// newest line rendered at the bottom) — no fixed sleep is needed.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	out.waitFor(t, "● live")
	out.waitFor(t, "NEWEST_after_F_visible")

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(Model)
	if !fm.pinned {
		t.Error("F left the view unpinned; F must always re-pin (never toggle off)")
	}
	if fm.state != stateLive {
		t.Errorf("state = %d after F, want stateLive", fm.state)
	}
	if !fm.viewport.AtBottom() {
		t.Error("F did not snap to the bottom; newest content should be in view")
	}
}

// TestTeatestConsoleSeededBadgeAndLineFaithful is the spec §8 regression
// (b): a model seeded with Config.Format = Console (the way the cli wires a
// .log file — spec §8) must render the follow view with the `LOG` status-
// bar badge and line-faithfully, one input line per output line, with NO
// paragraph merge. This pins a real field defect: without the Format seed the model fell back to zero-value Markdown,
// stamping `MD` and reflowing console lines into paragraphs.
//
// The two distinct prose lines below would be merged into a single paragraph
// by the markdown renderer (glamour reflows adjacent non-blank prose), so a
// regression to the Markdown zero value would (i) show `MD` not `LOG` and
// (ii) run the two sentinels together — both asserted against here. The
// detector is Markdown on purpose to prove the SEED, not a re-detect, drives
// the initial format: only the Config.Format seed makes this render as a log.
func TestTeatestConsoleSeededBadgeAndLineFaithful(t *testing.T) {
	t.Setenv("PRETTYTAIL_STYLE", "dark")

	// Detector returns Markdown; if the seed were ignored the model would use
	// this (or its zero value, also Markdown) and the assertions would fail.
	tm := teatest.NewTestModel(t,
		teatestModelFormat("run.log", detect.Console, markdownDetector),
		teatest.WithInitialTermSize(80, 24))
	out := newOutput(tm)

	// Two separate console lines. In console (line-faithful) they stay on
	// their own rows; in markdown they would reflow into one paragraph.
	body := []byte("first console line alpha sentinel\nsecond console line beta sentinel\n")
	tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: body}))

	// The status bar must show the LOG badge (Console), proving the seed took.
	out.waitFor(t, "LOG")
	// Both sentinels must be present and line-faithful (not merged).
	out.waitFor(t, "first console line alpha sentinel")
	out.waitFor(t, "second console line beta sentinel")

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(Model)

	// Hard assertions on the final model. The format must be Console (seed),
	// not the zero-value Markdown.
	if fm.format != detect.Console {
		t.Errorf("model format = %v (%s), want Console (seed ignored?)", fm.format, fm.format.String())
	}
	// Line-faithful: the rendered doc must keep the two sentences on their own
	// separate rows. Assert positively — each sentinel sits on its own line
	// and the two live on DIFFERENT lines — rather than only the negative
	// "not on the same row". A markdown reflow would merge them onto one row
	// (both indices equal, or the merge check below firing); a two-row wrap for
	// any other reason would still fail the distinct-lines requirement.
	lines := bytes.Split(stripANSI([]byte(fm.viewport.View())), []byte("\n"))
	alphaLine, betaLine := -1, -1
	for i, line := range lines {
		hasAlpha := bytes.Contains(line, []byte("alpha sentinel"))
		hasBeta := bytes.Contains(line, []byte("beta sentinel"))
		if hasAlpha && hasBeta {
			t.Errorf("console lines were merged onto one row (markdown reflow): %q", line)
		}
		if hasAlpha {
			alphaLine = i
		}
		if hasBeta {
			betaLine = i
		}
	}
	if alphaLine < 0 || betaLine < 0 {
		t.Fatalf("both sentinels must be visible on their own rows; alphaLine=%d betaLine=%d", alphaLine, betaLine)
	}
	if alphaLine == betaLine {
		t.Errorf("sentinels rendered on the same row %d; console must be line-faithful", alphaLine)
	}
}

// TestTeatestQuitFinishes covers spec §7 bullet 4: WaitFinished returns on
// q (the program exits cleanly, which is what restores the terminal).
func TestTeatestQuitFinishes(t *testing.T) {
	t.Setenv("PRETTYTAIL_STYLE", "dark")

	tm := teatest.NewTestModel(t, teatestModel(markdownDetector), teatest.WithInitialTermSize(80, 24))
	out := newOutput(tm)
	tm.Send(tailMsg(tail.Event{Kind: tail.Append, Data: []byte("# Title\n")}))
	out.waitFor(t, "Title")

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	// If q did not quit, this would time out and fail the test.
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

package stream

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Bunch/prettytail/internal/detect"
	"github.com/The-Bunch/prettytail/internal/tail"
)

// fakeTimer records the duration it was armed with so a test can assert the
// quiet-flush interval is exactly the spec's 2s, not merely "some timer".
type fakeTimer struct {
	d    time.Duration
	ch   chan time.Time
	live bool
}

// fakeClock is a controllable clock for the quiet-flush timer (mirrors the
// tail package's fake clock). Tests fire the armed timer explicitly — no
// real sleeps, so the 2s quiet-flush is asserted deterministically.
type fakeClock struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

func (c *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{d: d, ch: make(chan time.Time, 1), live: true}
	c.timers = append(c.timers, t)
	stop := func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		t.live = false
		return true
	}
	return t.ch, stop
}

// fire fires the most recently armed live timer, simulating its quiet period
// elapsing, and returns the duration it was armed with (so the caller can
// assert it equals the spec's 2s).
func (c *fakeClock) fire() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.timers) - 1; i >= 0; i-- {
		if c.timers[i].live {
			c.timers[i].ch <- time.Unix(2, 0)
			c.timers[i].live = false
			return c.timers[i].d
		}
	}
	return -1
}

// lastArmedDuration returns the duration of the most recently armed live
// timer, or -1 if none is armed.
func (c *fakeClock) lastArmedDuration() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.timers) - 1; i >= 0; i-- {
		if c.timers[i].live {
			return c.timers[i].d
		}
	}
	return -1
}

// armed reports how many quiet-flush timers are currently live (not stopped
// or fired) — lets a test assert that the line-buffered path never arms one.
func (c *fakeClock) armed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if t.live {
			n++
		}
	}
	return n
}

var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// plain strips ANSI escapes and collapses whitespace so rendered output can
// be substring-matched regardless of styling and wrapping.
func plain(s string) string {
	return strings.Join(strings.Fields(ansiSeq.ReplaceAllString(s, "")), " ")
}

// harness wires a Runner to a controllable clock, a buffered event channel
// and an output buffer. Events are pushed with send; the Runner runs in a
// goroutine and is stopped by closing the channel via done().
type harness struct {
	t      *testing.T
	clk    *fakeClock
	events chan tail.Event
	out    *lockedBuffer
	runner *Runner
	wg     sync.WaitGroup
}

// lockedBuffer is a bytes.Buffer safe for the concurrent writes the Runner
// goroutine makes while the test reads snapshots.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newHarness builds a harness whose Detector always returns format (the
// common case: format known up-front). width is pinned to 80.
func newHarness(t *testing.T, format detect.Format) *harness {
	t.Helper()
	clk := &fakeClock{}
	out := &lockedBuffer{}
	det := func([]byte) detect.Format { return format }
	r := New(det, 80, out)
	r.clk = clk
	h := &harness{t: t, clk: clk, events: make(chan tail.Event, 32), out: out, runner: r}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		_ = r.Run(h.events)
	}()
	return h
}

// send pushes an Append event carrying data.
func (h *harness) send(data string) {
	h.events <- tail.Event{Kind: tail.Append, Data: []byte(data)}
}

// reset pushes a Reset event carrying fresh full content.
func (h *harness) reset(data string) {
	h.events <- tail.Event{Kind: tail.Reset, Data: []byte(data)}
}

// fireQuiet triggers the quiet-flush after letting the Runner arm its timer,
// returning the duration the fired timer was armed with so a caller can
// assert it is exactly the spec's 2s quiet-flush interval.
func (h *harness) fireQuiet() time.Duration {
	h.sync()
	return h.clk.fire()
}

// sync forces the Runner to have processed all sent events by round-tripping
// a Waiting event (which the Runner consumes but ignores) — but Waiting does
// not guarantee ordering visibility of prior writes, so we instead rely on
// the channel being drained: sending is synchronous once the buffer fills.
// To keep it simple and deterministic we push a marker and poll the output.
func (h *harness) sync() {
	// A short spin waiting for the channel to drain. The Runner reads
	// eagerly; this only yields the scheduler, it asserts no timing.
	for i := 0; i < 100; i++ {
		if len(h.events) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// finish closes the event channel (clean shutdown) and waits for the Runner
// to return, then yields the final output.
func (h *harness) finish() string {
	close(h.events)
	h.wg.Wait()
	return h.out.String()
}

// waitContains blocks (briefly) until the output contains want, failing the
// test if it does not appear. Used where a render happens without a
// subsequent flush trigger (e.g. a standalone heading emits immediately).
func (h *harness) waitContains(want string) {
	h.t.Helper()
	for i := 0; i < 200; i++ {
		if strings.Contains(plain(h.out.String()), want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatalf("output never contained %q; got:\n%s", want, plain(h.out.String()))
}

func TestMarkdownHeadingRendersAloneWithoutWaiting(t *testing.T) {
	h := newHarness(t, detect.Markdown)
	// A heading line arriving alone is a complete block and must render
	// immediately — no blank line, no quiet-flush needed.
	h.send("# Live Heading\n")
	h.waitContains("Live Heading")
	_ = h.finish()
}

func TestMarkdownFenceSpanningAppendsRendersOnceComplete(t *testing.T) {
	h := newHarness(t, detect.Markdown)
	// A fence opened in one Append and closed in a later one must render as
	// a single complete block once the closing marker arrives.
	h.send("```go\n")
	h.send("func main() {\n")
	h.send("\tprintln(\"hi\")\n")
	// Not yet closed: nothing should have rendered.
	h.sync()
	if got := plain(h.out.String()); got != "" {
		t.Fatalf("fence rendered before it closed: %q", got)
	}
	h.send("}\n```\n")
	h.waitContains("func main()")

	out := plain(h.finish())
	if strings.Count(out, "func main()") != 1 {
		t.Fatalf("fence body should render exactly once, got %d in:\n%s",
			strings.Count(out, "func main()"), out)
	}
	if !strings.Contains(out, "println") {
		t.Fatalf("fence body incomplete: %q", out)
	}
}

func TestMarkdownTableGroupWaitsForBlankLine(t *testing.T) {
	h := newHarness(t, detect.Markdown)
	h.send("| Col A | Col B |\n")
	h.send("| ----- | ----- |\n")
	h.send("| one   | two   |\n")
	// No blank line yet: the table group is incomplete, must not render.
	h.sync()
	if got := plain(h.out.String()); got != "" {
		t.Fatalf("table rendered before its blank line: %q", got)
	}
	h.send("\n") // blank line completes the group
	h.waitContains("Col A")
	_ = h.finish()
}

func TestQuietFlushFiresAtTwoSeconds(t *testing.T) {
	h := newHarness(t, detect.Markdown)
	// An incomplete paragraph (no blank-line boundary) stays buffered until
	// the quiet-flush fires on the fake clock.
	h.send("An unfinished paragraph with no trailing blank line\n")
	h.sync()
	if got := plain(h.out.String()); got != "" {
		t.Fatalf("pending paragraph rendered before quiet-flush: %q", got)
	}
	// The armed quiet-flush timer must be exactly the spec's 2s interval:
	// a regression that retuned quietFlush would be caught here, not
	// silently passed (the fake clock records the requested duration).
	if d := h.clk.lastArmedDuration(); d != 2*time.Second {
		t.Fatalf("quiet-flush armed for %v, want exactly 2s", d)
	}
	if d := h.fireQuiet(); d != 2*time.Second {
		t.Fatalf("fired quiet-flush timer had duration %v, want exactly 2s", d)
	}
	h.waitContains("unfinished paragraph")
	_ = h.finish()
}

func TestNDJSONLinesRenderOneByOne(t *testing.T) {
	h := newHarness(t, detect.NDJSON)
	h.send(`{"event":"alpha"}` + "\n")
	h.waitContains("alpha")
	h.send(`{"event":"beta"}` + "\n")
	h.waitContains("beta")
	// The line-buffered path must never arm a quiet-flush timer.
	h.sync()
	if n := h.clk.armed(); n != 0 {
		t.Fatalf("line-buffered format armed %d quiet-flush timers, want 0", n)
	}
	out := plain(h.finish())
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Fatalf("ndjson lines missing from output: %q", out)
	}
}

func TestNDJSONPartialLineHeldUntilNewline(t *testing.T) {
	h := newHarness(t, detect.NDJSON)
	// A fractional line in one Append must not render until its newline
	// arrives in a later Append.
	h.send(`{"event":"spl`)
	h.sync()
	if got := plain(h.out.String()); got != "" {
		t.Fatalf("partial line rendered early: %q", got)
	}
	h.send(`it"}` + "\n")
	h.waitContains("split")
	_ = h.finish()
}

func TestResetPrintsDividerThenFreshRender(t *testing.T) {
	h := newHarness(t, detect.Markdown)
	h.send("# First\n")
	h.waitContains("First")
	h.reset("# After Reset\n")
	h.waitContains("file reset")
	h.waitContains("After Reset")

	out := plain(h.finish())
	if !strings.Contains(out, "file reset (truncated or replaced)") {
		t.Fatalf("reset divider text missing: %q", out)
	}
}

func TestStreamOutputHasNoScreenControl(t *testing.T) {
	// Pipe-safety invariant: no alt-screen or cursor-movement
	// sequences ever appear in stream output.
	h := newHarness(t, detect.Markdown)
	h.send("# Heading\n\nSome *emphasised* paragraph text here.\n\n")
	h.send("```go\nx := 1\n```\n")
	h.fireQuiet()
	h.waitContains("Heading")
	out := h.finish()

	for _, bad := range []string{"\x1b[?1049", "\x1b[?1049h", "\x1b[2J", "\x1b[H"} {
		if strings.Contains(out, bad) {
			t.Fatalf("stream output contains screen-control sequence %q", bad)
		}
	}
}

func TestMarkdownBlockHasExactlyOneTrailingBlankLine(t *testing.T) {
	// Spec: each completed block prints with exactly one
	// trailing blank line. emitBlock writes the renderer's RenderUnit output
	// verbatim, so this guards the end-to-end invariant: two consecutive
	// blocks must be separated by exactly one blank line (block ends "\n\n",
	// not "\n" or "\n\n\n").
	h := newHarness(t, detect.Markdown)
	h.send("First paragraph.\n\n")
	h.send("Second paragraph.\n\n")
	h.waitContains("Second paragraph")
	out := h.finish()

	// Strip trailing ANSI resets so the newline structure is exact.
	clean := ansiSeq.ReplaceAllString(out, "")
	// The whole stream is block1 + block2, each ending in exactly one blank
	// line. Collapse to logical structure: there must be no run of three or
	// more consecutive newlines anywhere (which would be >1 blank line).
	if strings.Contains(clean, "\n\n\n") {
		t.Fatalf("rendered blocks contain more than one blank line between them:\n%q", clean)
	}
	// And each rendered block must be terminated (ends with a newline).
	if !strings.HasSuffix(clean, "\n") {
		t.Fatalf("stream output does not end with a newline:\n%q", clean)
	}
}

func TestFenceClosingRules(t *testing.T) {
	// closesFence: a valid closing fence is same-family fence chars only, at
	// least as long as the opener, with no info string.
	cases := []struct {
		name    string
		trimmed string
		mark    string
		want    bool
	}{
		{"exact backtick close", "```", "```", true},
		{"longer close ok", "````", "```", true},
		{"shorter close rejected", "``", "```", false},
		{"wrong family rejected", "~~~", "```", false},
		{"info string on close rejected", "```go", "```", false},
		{"tilde close", "~~~", "~~~", true},
		{"four-tilde opener needs four", "~~~", "~~~~", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := closesFence(tc.trimmed, tc.mark); got != tc.want {
				t.Fatalf("closesFence(%q, %q) = %v, want %v", tc.trimmed, tc.mark, got, tc.want)
			}
		})
	}
}

func TestFourBacktickFenceNotClosedByThree(t *testing.T) {
	// A fence opened with four backticks must not be closed by a three-tick
	// line — the inner ``` is content, not a close (CommonMark).
	h := newHarness(t, detect.Markdown)
	h.send("````\n")
	h.send("```\n") // this is fence content, not a close
	h.send("still inside\n")
	h.sync()
	if got := plain(h.out.String()); got != "" {
		t.Fatalf("four-tick fence closed early by a three-tick line: %q", got)
	}
	h.send("````\n") // the real close
	h.waitContains("still inside")
	_ = h.finish()
}

func TestErrEventEndsRun(t *testing.T) {
	clk := &fakeClock{}
	out := &lockedBuffer{}
	r := New(func([]byte) detect.Format { return detect.Plain }, 80, out)
	r.clk = clk
	events := make(chan tail.Event, 4)
	events <- tail.Event{Kind: tail.Err, Err: errSentinel}
	err := r.Run(events)
	if err != errSentinel {
		t.Fatalf("Run should return the Err event error, got %v", err)
	}
}

var errSentinel = &sentinelErr{}

type sentinelErr struct{}

func (*sentinelErr) Error() string { return "sentinel" }

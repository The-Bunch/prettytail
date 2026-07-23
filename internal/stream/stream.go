// Package stream implements prettytail's append-only stream mode (the spec
// §5): it consumes tail Events and writes rendered output that never
// contains ANSI screen-control sequences (no alt-screen, no cursor
// movement) — pipe-safe by construction.
//
// For markdown the interesting part is the block chunker: bytes are fed
// into a line buffer and a block is emitted only when it is complete
// (blank-line boundary, closed fence, a standalone ATX heading, or a 2s
// quiet-flush). Each completed block renders through the markdown renderer.
// For ndjson/logfmt/plain the stream is strictly line-buffered: each
// complete line renders through its renderer and prints immediately.
package stream

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/The-Bunch/prettytail/internal/detect"
	"github.com/The-Bunch/prettytail/internal/render"
	"github.com/The-Bunch/prettytail/internal/tail"
)

// quietFlush is the pending-content flush interval: incomplete
// markdown content with no new data for this long renders and prints as-is.
const quietFlush = 2 * time.Second

// fenceValve force-completes a fence left open past this many bytes, a
// safety valve so a runaway unterminated fence cannot buffer forever.
const fenceValve = 64 << 10 // 64 KiB

// resetDivider is printed (styled dim) before the fresh render when the
// tailed file is truncated or replaced.
const resetDivider = "─── file reset (truncated or replaced) ───"

// dimSGR/reset wrap the reset divider in a dim ANSI attribute. This is an
// inline text attribute (SGR), not a screen-control sequence, so it stays
// pipe-safe per §5's "never emits ANSI screen-control".
const (
	dimSGR   = "\x1b[2m"
	resetSGR = "\x1b[0m"
)

// reATXHeading matches an ATX heading line: one
// to six '#' followed by a space. A standalone heading line is itself a
// complete block.
var reATXHeading = regexp.MustCompile(`^#{1,6} `)

// clock is the seam that lets tests drive the quiet-flush timer without
// real sleeps (mirrors internal/tail's clock). Run uses realClock.
type clock interface {
	NewTimer(d time.Duration) (<-chan time.Time, func() bool)
}

type realClock struct{}

func (realClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

// Detector decides the format for freshly (re-)read content. cli supplies
// one that honours the --format override and re-detects on Reset from the
// content head, so a rotated file that changes shape is re-classified.
type Detector func(head []byte) detect.Format

// Runner drives stream mode over a channel of tail events. It is created
// with New and driven by Run; the zero value is not usable.
type Runner struct {
	detect Detector
	width  int
	out    io.Writer
	clk    clock

	format   detect.Format
	rdr      render.Renderer
	detected bool // whether format/rdr have been set for the current file

	// chunk is the markdown block chunker's line buffer: completed lines
	// (each ending in '\n') that have not yet formed an emitted block.
	chunk []byte
	// inFence tracks an open markdown fence; fenceMark is its opening
	// marker ("```" or "~~~") so only a matching marker closes it.
	inFence   bool
	fenceMark string

	// lineBuf holds a not-yet-complete line for the line-buffered formats
	// (ndjson/logfmt/plain). The tail engine already flushes trailing
	// partials, but a single Append may still carry a fractional final
	// line, so we hold it until its newline arrives.
	lineBuf []byte
}

// New builds a stream Runner. detect resolves the format for the current
// content (called once at start and again on each Reset); width is the
// terminal width (80 when not a TTY, per §5); out receives rendered bytes.
func New(det Detector, width int, out io.Writer) *Runner {
	return &Runner{detect: det, width: width, out: out, clk: realClock{}}
}

// Run consumes events until the channel closes or ctx-driven closure
// upstream ends it, writing rendered output to the Runner's writer. It
// returns the first fatal error observed (a tail Err event), or nil on
// clean completion. Renderer errors are never fatal: the source is printed
// raw and the stream carries on.
func (r *Runner) Run(events <-chan tail.Event) error {
	// The quiet-flush timer is armed only while markdown content is pending
	// in the chunk buffer; quietC is nil (a forever-blocked receive) otherwise.
	var quietC <-chan time.Time
	var stopQuiet func() bool
	arm := func() {
		if stopQuiet != nil {
			stopQuiet()
		}
		if len(r.chunk) > 0 && r.isMarkdown() {
			quietC, stopQuiet = r.clk.NewTimer(quietFlush)
		} else {
			quietC, stopQuiet = nil, nil
		}
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// Channel closed: flush whatever markdown remains so no
				// content is silently dropped at shutdown.
				r.flushPending()
				return nil
			}
			switch ev.Kind {
			case tail.Append:
				r.onData(ev.Data)
			case tail.Reset:
				r.onReset(ev.Data)
			case tail.Waiting:
				// Nothing to render; the follow modes surface waiting on
				// stderr at the cli layer. Stream stays silent on stdout.
			case tail.Err:
				return ev.Err
			}
			arm()
		case <-quietC:
			r.flushPending()
			arm()
		}
	}
}

// isMarkdown reports whether the current renderer is the block-chunked
// markdown path (the only path with a quiet-flush timer / pending blocks).
func (r *Runner) isMarkdown() bool {
	return r.detected && r.format == detect.Markdown
}

// ensureDetected resolves the format and renderer from content the first
// time data arrives for a file (and after a Reset clears it).
func (r *Runner) ensureDetected(head []byte) {
	if r.detected {
		return
	}
	r.format = r.detect(head)
	r.rdr = render.For(r.format)
	r.detected = true
}

// onData feeds appended bytes into the active path: the markdown chunker or
// the line-buffered renderer.
func (r *Runner) onData(data []byte) {
	if len(data) == 0 {
		// An empty initial Append (empty file) still establishes the
		// format from an empty head so subsequent data renders correctly.
		r.ensureDetected(nil)
		return
	}
	r.ensureDetected(data)
	if r.format == detect.Markdown {
		r.feedMarkdown(data)
		return
	}
	r.feedLines(data)
}

// onReset handles a truncate/rotate: print the styled divider, drop all
// buffered state, re-detect from the fresh content and render it.
func (r *Runner) onReset(data []byte) {
	// Flush any markdown still pending from the previous file first so its
	// content is not lost behind the divider.
	r.flushPending()
	fmt.Fprintf(r.out, "%s%s%s\n\n", dimSGR, resetDivider, resetSGR)
	r.resetState()
	r.onData(data)
}

// resetState clears all per-file buffers and detection so a new file starts
// clean.
func (r *Runner) resetState() {
	r.chunk = nil
	r.inFence = false
	r.fenceMark = ""
	r.lineBuf = nil
	r.detected = false
	r.rdr = nil
}

// feedLines is the ndjson/logfmt/plain path: strictly
// line-buffered, each complete line rendered and printed immediately, no
// chunker and no quiet-flush.
func (r *Runner) feedLines(data []byte) {
	r.lineBuf = append(r.lineBuf, data...)
	for {
		idx := bytes.IndexByte(r.lineBuf, '\n')
		if idx < 0 {
			break
		}
		line := r.lineBuf[:idx] // without the newline
		r.lineBuf = r.lineBuf[idx+1:]
		r.renderUnit(line)
	}
}

// feedMarkdown is the block-chunker path. It appends data to
// the line buffer and emits every block that is now complete.
func (r *Runner) feedMarkdown(data []byte) {
	buf := append(r.chunk, data...)
	r.chunk = nil

	// Walk complete lines (those ending in '\n'); hold any trailing
	// partial line for the next Append.
	for {
		idx := bytes.IndexByte(buf, '\n')
		if idx < 0 {
			// No complete line left: keep the remainder buffered.
			r.chunk = append(r.chunk, buf...)
			break
		}
		line := buf[:idx+1] // includes the trailing newline
		buf = buf[idx+1:]
		r.consumeMarkdownLine(line)
	}
	// Safety valve: a fence that never closes must not buffer unboundedly.
	if r.inFence && len(r.chunk) >= fenceValve {
		r.emitBlock(r.chunk)
		r.chunk = nil
		r.inFence = false
		r.fenceMark = ""
	}
}

// consumeMarkdownLine appends one complete line (with its newline) to the
// pending block and applies the completion rules.
func (r *Runner) consumeMarkdownLine(line []byte) {
	trimmed := strings.TrimSpace(string(line))

	if r.inFence {
		// Inside a fence nothing else completes blocks; only a matching
		// closing fence closes it. A CommonMark closing fence is a line of
		// only fence characters, same family (` or ~) as the opener, of
		// length ≥ the opener's, and carries no info string.
		r.chunk = append(r.chunk, line...)
		if closesFence(trimmed, r.fenceMark) {
			r.emitBlock(r.chunk)
			r.chunk = nil
			r.inFence = false
			r.fenceMark = ""
		}
		return
	}

	// A blank line following non-blank content completes the current block
	// (paragraph/list/table group).
	if trimmed == "" {
		if len(bytes.TrimSpace(r.chunk)) > 0 {
			r.emitBlock(r.chunk)
			r.chunk = nil
		} else {
			// Leading blank runs with no content: drop them.
			r.chunk = nil
		}
		return
	}

	// A fence opening: any prior pending block completes first, then the
	// fence starts. The opener's full run of fence characters is captured
	// (CommonMark: a closing fence must be at least as long).
	if mark := openFenceMarker(trimmed); mark != "" {
		if len(bytes.TrimSpace(r.chunk)) > 0 {
			r.emitBlock(r.chunk)
			r.chunk = nil
		}
		r.inFence = true
		r.fenceMark = mark
		r.chunk = append(r.chunk, line...)
		return
	}

	// An ATX heading line is itself a complete block: flush any prior
	// pending block, then emit the heading on its own.
	if reATXHeading.MatchString(trimmed) {
		if len(bytes.TrimSpace(r.chunk)) > 0 {
			r.emitBlock(r.chunk)
			r.chunk = nil
		}
		r.emitBlock(line)
		return
	}

	// Ordinary content line: accumulate into the current block.
	r.chunk = append(r.chunk, line...)
}

// flushPending renders and prints whatever is buffered without waiting for
// a natural boundary — the quiet-flush and shutdown path.
func (r *Runner) flushPending() {
	if r.isMarkdown() {
		if len(bytes.TrimSpace(r.chunk)) > 0 {
			r.emitBlock(r.chunk)
		}
		r.chunk = nil
		r.inFence = false
		r.fenceMark = ""
		return
	}
	// Line-buffered formats: a trailing partial line at shutdown renders
	// as its own unit so no content is dropped.
	if len(r.lineBuf) > 0 {
		r.renderUnit(r.lineBuf)
		r.lineBuf = nil
	}
}

// emitBlock renders one completed markdown block and prints it. A renderer
// error falls back to printing the raw source (dim) — never fatal (§1).
func (r *Runner) emitBlock(block []byte) {
	out, err := r.rdr.RenderUnit(block, r.width)
	if err != nil {
		fmt.Fprintf(r.out, "%s%s%s", dimSGR, string(block), resetSGR)
		return
	}
	io.WriteString(r.out, out)
}

// renderUnit renders one line for the line-buffered formats and prints it.
// A renderer error falls back to the raw line (dim) plus a newline.
func (r *Runner) renderUnit(line []byte) {
	out, err := r.rdr.RenderUnit(line, r.width)
	if err != nil {
		fmt.Fprintf(r.out, "%s%s%s\n", dimSGR, string(line), resetSGR)
		return
	}
	io.WriteString(r.out, out)
}

// fenceRun returns the leading run of a single fence character (` or ~) at
// the start of trimmed, or ("", 0) if trimmed does not begin with at least
// three of one such character.
func fenceRun(trimmed string) (ch byte, n int) {
	if len(trimmed) < 3 {
		return 0, 0
	}
	c := trimmed[0]
	if c != '`' && c != '~' {
		return 0, 0
	}
	i := 0
	for i < len(trimmed) && trimmed[i] == c {
		i++
	}
	if i < 3 {
		return 0, 0
	}
	return c, i
}

// openFenceMarker returns the opening fence marker (the leading run of
// backticks or tildes) for a line that opens a fence, or "" if it does not.
// An opener may carry an info string after the run (```go), so only the
// leading run is validated.
func openFenceMarker(trimmed string) string {
	ch, n := fenceRun(trimmed)
	if n == 0 {
		return ""
	}
	return strings.Repeat(string(ch), n)
}

// closesFence reports whether trimmed is a valid closing fence for an open
// fence whose opening marker was mark. Per CommonMark a closing fence is a
// line of only the same fence character, at least as long as the opener,
// with no trailing info string (only optional whitespace, already trimmed).
func closesFence(trimmed, mark string) bool {
	if mark == "" {
		return false
	}
	ch, n := fenceRun(trimmed)
	if n == 0 || ch != mark[0] || n < len(mark) {
		return false
	}
	// The line must be nothing but the fence run (no info string on close).
	return n == len(trimmed)
}

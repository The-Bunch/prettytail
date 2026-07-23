package render

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Console/log rendering. One input line becomes one or more
// output lines: content is NEVER reflowed or merged; lines wider than the
// render width hard-wrap, and continuation rows carry a dim "↪ " prefix.
// ANSI hygiene (sanitizeANSI, ansi.go) preserves the log's own SGR colouring
// and drops everything else; every rendered line ends with a reset so styles
// never bleed. Added styling (diff/timestamp/path) is applied ONLY to lines
// that carry no SGR of their own — self-styled lines get hygiene only.

// consoleContWidth is the display width of the "↪ " continuation marker
// (2 cells). Continuation rows reserve this many columns for the marker plus
// any hang indent.
const consoleContWidth = 2

// Console added-styling patterns and styles. Diff markers and
// hunk/file headers, a leading timestamp, and absolute paths / file:line:col
// refs. All applied ONLY to lines with no SGR of their own.
// Console added-styling built fresh per call so it binds to the current
// lipgloss default renderer (colour resolved at render time, matching how
// the tool adapts to its terminal). Diff markers/headers, and a shared dim
// for timestamps and paths.
func consoleDiffAdd() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Dark: "42", Light: "28"})
}
func consoleDiffDel() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Dark: "196", Light: "160"})
}
func consoleDiffHunk() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Dark: "44", Light: "30"})
}
func consoleDiffHeader() lipgloss.Style { return lipgloss.NewStyle().Bold(true) }
func consoleDim() lipgloss.Style        { return dimStyle }

var (
	// reDiffFileHeader matches unified-diff file headers "+++ " / "--- "
	// (checked before the single-char +/- diff rules). Bold.
	reDiffFileHeader = regexp.MustCompile(`^(\+\+\+ |--- )`)
	// reDiffHunk matches an "@@ … @@" hunk header. Cyan.
	reDiffHunk = regexp.MustCompile(`^@@`)

	// reLeadingTimestamp matches an ISO date (YYYY-MM-DD) or a clock time
	// (HH:MM:SS) at the very start of the line. The matched span is dimmed.
	reLeadingTimestamp = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?)?|\d{2}:\d{2}:\d{2}(\.\d+)?)`)

	// rePathRef matches an absolute path or a file:line[:col] reference
	// anywhere in the line. Each match is dimmed. The path body stops at
	// whitespace; a trailing :line[:col] suffix is captured when present.
	rePathRef = regexp.MustCompile(`(/[^\s:]+)+(:\d+(:\d+)?)?|[^\s:/]+\.[A-Za-z0-9]+:\d+(:\d+)?`)
)

// consoleRenderer renders console/tool output line-faithfully.
// It holds no state; the zero value is ready to use.
type consoleRenderer struct{}

// hunkState tracks whether the renderer is inside an open unified-diff hunk.
// Diff +/- styling applies ONLY inside an open hunk; outside
// one, a leading "- " is a bullet and a bare "+x"/"-x" line stays unstyled. A
// hunk opens at "@@ " (or a "+++ "/"--- " file-header pair) and closes at the
// first line starting with none of '+', '-', ' '. RenderDoc carries this
// state across lines; RenderUnit (stream mode) uses a fresh zero value per
// call — a per-line stream cannot reconstruct multi-line hunk context, so a
// standalone "+x" streamed line correctly stays unstyled.
type hunkState struct {
	open bool
}

// RenderDoc renders every line of src in order, carrying hunk
// context across lines for the diff/bullet collision fix.
func (c consoleRenderer) RenderDoc(src []byte, width int) (string, error) {
	lines := strings.Split(strings.TrimRight(string(src), "\n"), "\n")
	var b strings.Builder
	var hs hunkState
	for _, line := range lines {
		b.WriteString(c.renderLine(line, width, &hs))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// RenderUnit renders a single console line (stream mode). It uses
// a fresh hunk state, so diff +/- styling never fires on an isolated streamed
// line.
func (c consoleRenderer) RenderUnit(src []byte, width int) (string, error) {
	var hs hunkState
	return c.renderLine(strings.TrimRight(string(src), "\n"), width, &hs) + "\n", nil
}

// renderLine turns one input line into its rendered form (no trailing
// newline). Order per spec §9: ANSI hygiene (drop non-SGR escapes and bare
// CSI-shaped residue, §3 and §7) → inline markup (§9, on non-SGR lines) →
// diff/timestamp/path styling (§3, on non-SGR lines) → hard-wrap at width
// with the §9.7 wrap set-out — continuation rows hang-indented under the
// logical line's first content column with a dim "↪" at the indent —
// terminating every output row with a reset (§3). hs carries unified-diff
// hunk context across lines so diff +/- styling only fires inside an open
// hunk (§9.6). A line narrower than the width produces a single row.
func (consoleRenderer) renderLine(line string, width int, hs *hunkState) string {
	// CRLF is normalised upstream; strip any stray trailing \r.
	line = strings.TrimSuffix(line, "\r")

	hadSGR := containsSGR(line)
	clean := sanitizeANSI(line)
	// Drop bare CSI-shaped residue (SGR fragments with no ESC byte, e.g.
	// literal "[0m") left behind by tools that half-stripped their colours.
	// Console format only; escape-aware so preserved SGR is
	// untouched.
	clean = dropBareCSIResidue(clean)

	// The wrap set-out hangs continuation rows under the logical line's first
	// content column. That column is a property of the visible
	// text — leading indent plus any leader marker — so it must be measured
	// on the hygiene-clean line BEFORE styling replaces the leader glyphs and
	// markup markers with ANSI. Self-styled lines have no interpreted leader,
	// so their content column is simply the leading whitespace.
	hang := contentColumn(clean, hadSGR)

	// Added styling only on lines that carry no SGR of their own (the spec
	// §3, §9). Self-styled lines get hygiene only — and a self-styled line
	// never opens or closes a diff hunk, so leave hs untouched for them.
	if !hadSGR {
		clean = styleConsoleLine(clean, hs)
	}

	return wrapConsoleLine(clean, width, hang)
}

// contentColumn returns the visible column at which the logical line's
// content begins. Continuation rows are
// hang-indented to this column so wrapped bullets and prose read as blocks.
// It is measured on the hygiene-clean, pre-styling text: leading spaces/tabs
// (a tab counts as one column, matching how the leader indent is preserved),
// plus a leader marker width when the line carries an interpreted bullet or
// numbered leader (rules §9.3–9.4). Self-styled lines (hadSGR) get no leader
// interpretation, so only their leading whitespace counts.
func contentColumn(clean string, hadSGR bool) int {
	indent := leadingIndent(clean)
	col := len(indent) // spaces/tabs each occupy one column here.
	if hadSGR {
		return col
	}
	// A bullet ("- ", "* ", "• ") or numbered ("N. ", "N) ") leader shifts
	// the content column right by the marker's display width plus its space.
	if reBulletLeader.MatchString(clean) {
		// After interpretation the marker renders as a single "•" cell plus a
		// space, so the content column is indent + 2 regardless of which raw
		// marker char was used.
		return col + 2
	}
	if m := reNumberLeader.FindStringSubmatch(clean); m != nil {
		// m[2] is the whole number+delimiter token ("3." / "10)"), kept
		// verbatim; the +1 accounts for the space that follows it.
		return col + lipgloss.Width(m[2]) + 1
	}
	return col
}

// containsSGR reports whether line carries at least one well-formed SGR
// escape of its own. It
// reuses the escape scanner so a broken/non-SGR escape does not count.
func containsSGR(line string) bool {
	for i := 0; i < len(line); {
		if line[i] != esc {
			i++
			continue
		}
		_, keep, n := scanEscape(line[i:])
		if keep {
			return true
		}
		if n == 0 {
			n = 1
		}
		i += n
	}
	return false
}

// styleConsoleLine applies the spec §3 + §9 + §10 added styling to a
// hygiene-clean line that carries no SGR of its own. It first resolves the
// unified-diff hunk context: a hunk opens at "@@", a
// "+++ "/"--- " file header, or an "Index: " separator, and closes at the
// first line starting with none of '+', '-', ' '. Diff +/- styling fires
// ONLY inside an open hunk; the hunk-opening headers are always styled. A
// diff line's timestamp/paths ride the diff colour. Any non-diff line instead
// gets inline markup, then line-type styling,
// then — when no line
// type matched — a dimmed leading timestamp and dimmed path refs (§3).
func styleConsoleLine(line string, hs *hunkState) string {
	// Hunk-opening markers (always styled, and they open the hunk). An
	// "Index: " line is a dim separator that ALSO opens diff context
	// — the "some diffs unstyled" fix.
	switch {
	case reDiffFileHeader.MatchString(line):
		hs.open = true
		return consoleDiffHeader().Render(line)
	case reDiffHunk.MatchString(line):
		hs.open = true
		return consoleDiffHunk().Render(line)
	case reIndex.MatchString(line):
		hs.open = true
		return separatorStyle().Render(line)
	}

	// Inside an open hunk, the body lines carry diff colour. A line starting
	// with none of '+', '-', ' ' closes the hunk and falls
	// through to normal styling. Per spec §11.3 the embedded §9 inline
	// pairs (bold/code) are interpreted WITHIN the diff colour — the marker
	// is dropped, the bold/code nests inside, and the diff colour still owns
	// the whole line. ts/path dimming stays skipped for diff lines.
	if hs.open {
		switch {
		case strings.HasPrefix(line, "+"):
			return nestInDiff(applyInlineMarkup(line), consoleDiffAdd())
		case strings.HasPrefix(line, "-"):
			return nestInDiff(applyInlineMarkup(line), consoleDiffDel())
		case strings.HasPrefix(line, " "):
			// Context line inside the hunk: no added diff colour, but its
			// embedded §9 pairs are still interpreted (spec §11.3 — the
			// context row that quotes markdown must not keep its markers).
			return applyInlineMarkup(line)
		default:
			hs.open = false // this line closes the hunk; style it normally.
		}
	}

	// Non-diff line (outside any hunk, or the hunk-closing line): interpret
	// embedded inline markup, then classify by line type.
	// A matched line type styles the WHOLE line (heading,
	// banner, command, glyph, rule), so timestamp/path dimming is skipped —
	// the line-type colour owns the line. Only an unclassified line falls
	// through to the §3 timestamp/path dimming.
	line = applyInlineMarkup(line)
	if styled, matched := applyLineType(line); matched {
		return styled
	}

	// dimPathRefs is escape-aware, so it leaves any style inserted by inline
	// markup or the timestamp dim untouched and only styles still-plain runs.
	if loc := reLeadingTimestamp.FindStringIndex(line); loc != nil && loc[0] == 0 {
		line = consoleDim().Render(line[:loc[1]]) + line[loc[1]:]
	}
	return dimPathRefs(line)
}

// dimPathRefs dims every absolute-path or file:line[:col] reference in s.
// It skips any span that already sits inside an ANSI escape (e.g. a dimmed
// timestamp) by rebuilding around escapes: matching is done on the visible
// text only. To keep it simple and deterministic, it operates on the whole
// string but never matches inside an SGR sequence because those contain no
// '/' path bodies and are anchored by digits+'m'.
func dimPathRefs(s string) string {
	// Only touch segments outside escape sequences so we don't corrupt an
	// already-inserted dim timestamp. Split on escapes, style the plain runs.
	if !strings.ContainsRune(s, esc) {
		return rePathRef.ReplaceAllStringFunc(s, func(m string) string {
			return consoleDim().Render(m)
		})
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != esc {
			// Accumulate a plain run up to the next ESC.
			j := i
			for j < len(s) && s[j] != esc {
				j++
			}
			run := s[i:j]
			b.WriteString(rePathRef.ReplaceAllStringFunc(run, func(m string) string {
				return consoleDim().Render(m)
			}))
			i = j
			continue
		}
		_, _, n := scanEscape(s[i:])
		if n == 0 {
			n = 1
		}
		b.WriteString(s[i : i+n])
		i += n
	}
	return b.String()
}

// nestInDiff renders body (which may already contain inline SGR from §9 bold/
// code interpretation) under the given diff colour so the diff colour owns the
// whole line while nested bold/code still show. It opens the
// diff style's SGR, then re-asserts that opener after every inner reset
// ("\x1b[0m") the inline markup emitted — a plain lipgloss.Render around
// ANSI-bearing text would let those inner resets cancel the diff colour for
// the remainder of the line. A single trailing reset is added by the caller's
// wrap step, so none is appended here.
func nestInDiff(body string, style lipgloss.Style) string {
	// Derive the diff colour's opening SGR by rendering a sentinel and taking
	// everything before the sentinel text — deterministic and profile-aware.
	const sentinel = "\x00"
	rendered := style.Render(sentinel)
	idx := strings.Index(rendered, sentinel)
	if idx <= 0 {
		// No SGR emitted (colour profile off): interpret markup only.
		return body
	}
	opener := rendered[:idx]
	// Re-assert the diff opener after every inner reset so nested bold/code
	// spans return to the diff colour rather than plain text.
	body = strings.ReplaceAll(body, resetSeq, resetSeq+opener)
	return opener + body
}

// wrapConsoleLine hard-wraps a (possibly styled) line to the render width and
// sets out its continuation rows per spec §9.7: each continuation row is
// hang-indented so its content aligns under the logical line's first content
// column (hang), with a dim "↪" drawn at the indent (two cells before the
// content column) that replaces the old col-0 prefix. Every row ends with a
// reset so styles never bleed. ANSI-aware: SGR sequences are
// preserved and carried across wrap points by ansi.Hardwrap; the width budget
// counts visible cells only.
//
// hang is the visible content column measured by contentColumn. The "↪ "
// marker occupies two cells at column max(hang-2, 0); content on every row
// therefore begins at the same column. A plain line (hang 0) keeps the
// historic col-0 "↪ " prefix with content resuming at column 2.
func wrapConsoleLine(line string, width int, hang int) string {
	// A line that fits (visible width ≤ render width) is a single row.
	if width <= 0 || ansi.StringWidth(line) <= width {
		return line + resetSeq
	}

	// The marker sits two cells before the content column (but never left of
	// column 0). Content on continuation rows resumes at markerCol+2. The
	// clamp floors the marker at column 0: the "↪ " marker itself needs two
	// cells, so a line whose content column is < 2 (e.g. a plain line at
	// column 0) still gets its continuations set out at column 2 — the marker
	// width, not the logical content column, is the floor here.
	markerCol := hang - consoleContWidth
	if markerCol < 0 {
		markerCol = 0
	}
	contentCol := markerCol + consoleContWidth

	// Continuation rows have contentCol columns of set-out before content, so
	// their content budget is width-contentCol. Guard against a width so
	// small the set-out would not leave room for content. Every row (row 0
	// included) is wrapped at this budget: ansi.Hardwrap carries the open SGR
	// state across each wrap point, which a per-row re-cut would drop — so a
	// leader/indented line trades a little row-0 width for guaranteed style
	// continuity across continuations (a deliberate hygiene-first choice).
	contWidth := width - contentCol
	if contWidth < 1 {
		contWidth = 1
	}
	wrapped := ansi.Hardwrap(line, contWidth, false)
	rows := strings.Split(wrapped, "\n")

	// Precompute the continuation set-out: markerCol spaces, the dim "↪",
	// then a space — so content aligns under the first row's content column.
	prefix := strings.Repeat(" ", markerCol) + dimStyle.Render("↪") + " "

	// carry is the SGR state still open at the end of the previous row. It is
	// re-asserted after the (reset-terminating) dim prefix so a style opened
	// on the logical line — a bold heading, a diff colour, a preserved log
	// SGR — continues to paint the continuation rows rather than dropping to
	// plain text (spec §11.4: a wrapped heading's continuation rows are
	// also bold). ansi.Hardwrap does not re-open SGR at its wrap points, and
	// the dim "↪" prefix ends in a reset, so without this the remainder of a
	// wrapped styled line would render unstyled.
	var b strings.Builder
	carry := ""
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
			b.WriteString(prefix)
			b.WriteString(carry)
		}
		b.WriteString(row)
		b.WriteString(resetSeq)
		carry = activeSGR(carry + row)
	}
	return b.String()
}

// activeSGR returns the SGR opener still in effect at the end of s — the
// concatenation of the SGR parameters not yet cancelled by a reset. It scans
// only SGR escapes (CSI … 'm'); any reset ("\x1b[0m", or an "m" with no
// params) clears the accumulated state. Used by wrapConsoleLine to re-assert
// an open style (bold heading, diff colour, preserved log SGR) on each
// continuation row. The result is either "" (no style open)
// or one or more SGR sequences that reopen the active style.
func activeSGR(s string) string {
	var open []string
	for i := 0; i < len(s); {
		if s[i] != esc {
			i++
			continue
		}
		seq, keep, n := scanEscape(s[i:])
		if n == 0 {
			n = 1
		}
		if keep { // an SGR sequence (CSI … 'm')
			// A reset — "\x1b[0m" or a bare "\x1b[m" — clears all state.
			params := seq[2 : len(seq)-1] // between "\x1b[" and "m"
			if params == "" || params == "0" {
				open = open[:0]
			} else {
				open = append(open, seq)
			}
		}
		i += n
	}
	return strings.Join(open, "")
}

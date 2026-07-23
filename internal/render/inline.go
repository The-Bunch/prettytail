package render

import (
	"regexp"

	"github.com/charmbracelet/lipgloss"
)

// Inline markup interpretation for console lines. Console tool
// output frequently EMBEDS markdown-ish notation; interpret it per-line,
// staying line-faithful (never reflow, never merge). Applies ONLY to lines
// carrying no SGR of their own — self-styled lines stay hygiene-only, handled
// by the caller. Rule order within a line: bold/code-span (rules 1–2) →
// leader interpretation (bullets/numbered/checkboxes, rules 3–5). The
// diff/bullet collision fix (rule 6) lives in the console renderer's hunk
// tracking; the wrap set-out (rule 7) is the console renderer's wrapping.
//
// Non-goals (§9.8): leading-'#' headings, single-* / single-_ italics,
// tables, multi-line fences — deliberately not interpreted.

// Inline styles. Built fresh per call so they bind to the
// current lipgloss default renderer — the colour profile is resolved at
// render time, matching how the tool adapts to the terminal it runs in.
// Bold and a distinct code-span style (fg 222, no background); the leader
// styles use dim-cyan and status colours so bullets/numbers/checkboxes read
// as structure without shouting.
func inlineBold() lipgloss.Style { return lipgloss.NewStyle().Bold(true) }

// inlineCode is the code-span style: foreground 222, no background (§9.2).
func inlineCode() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("222"))
}

// inlineBulletStyle is the dim-cyan mark that replaces a bullet leader and
// the delimiter of a numbered leader (§9.3–9.4). Dim + cyan so it reads as
// structure, not content.
func inlineBulletStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Dark: "44", Light: "30"}).Faint(true)
}

// Checkbox glyph styles (§9.5): ☐ dim, ☑ green, ◐ yellow.
func inlineCheckboxTodo() lipgloss.Style { return dimStyle }
func inlineCheckboxDone() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Dark: "42", Light: "28"})
}
func inlineCheckboxProg() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Dark: "220", Light: "178"})
}

var (
	// reInlineBold matches a balanced **x** or __x__ pair with non-empty
	// content. Non-greedy so multiple pairs on one line each match. The
	// content may not itself contain the same marker run.
	reInlineBoldStar  = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reInlineBoldUnder = regexp.MustCompile(`__([^_]+)__`)
	// reInlineCode matches a balanced `x` code span with non-empty content
	// (no embedded backtick).
	reInlineCode = regexp.MustCompile("`([^`]+)`")

	// reBulletLeader matches a leading (after indent) bullet marker "- ",
	// "* " or "• ". Group 1 is the indent, group 2 the marker char, group 3
	// the trailing text (including its leading space consumed by the split).
	reBulletLeader = regexp.MustCompile(`^([ \t]*)([-*•]) (.*)$`)
	// reNumberLeader matches a leading numbered marker "N. " or "N) ". Group 1
	// indent, group 2 the number+delimiter (e.g. "3.", "10)"), group 3 rest.
	reNumberLeader = regexp.MustCompile(`^([ \t]*)(\d+[.)]) (.*)$`)
	// reCheckbox matches a checkbox token at the start of the (post-leader)
	// text: "[ ]", "[x]", "[X]", "[✓]" or "[•]", followed by a space or end.
	reCheckbox = regexp.MustCompile(`^\[( |x|X|✓|•)\]( |$)`)
)

// applyInlineMarkup interprets embedded markup in a hygiene-clean line that
// carries no SGR of its own. It returns the styled
// line. Bold and code spans are applied first (anywhere on the line), then a
// single leading leader (bullet or numbered) and an optional checkbox token
// are styled. Line-faithful: no characters are reordered or dropped except
// the balanced markup markers the spec says to drop (**, __, `).
func applyInlineMarkup(line string) string {
	line = applyBoldAndCode(line)
	line = applyLeader(line)
	return line
}

// applyBoldAndCode replaces balanced **x**/__x__ with bold and `x` with the
// code style, dropping the markers. Unbalanced markers are
// left literal because the regexes require a matching closing run. Bold is
// applied before code so a code span inside bold still styles; the ordering
// here is deterministic and sufficient for the corpus.
func applyBoldAndCode(line string) string {
	line = reInlineBoldStar.ReplaceAllString(line, inlineBold().Render("$1"))
	line = reInlineBoldUnder.ReplaceAllString(line, inlineBold().Render("$1"))
	line = reInlineCode.ReplaceAllStringFunc(line, func(m string) string {
		inner := m[1 : len(m)-1] // strip the surrounding backticks
		return inlineCode().Render(inner)
	})
	return line
}

// applyLeader styles a single leading bullet/numbered leader and an optional
// checkbox token that follows. Indentation is preserved so
// nesting reads naturally. If no leader matches, a checkbox at the very start
// of the line (after indent) is still honoured. Following text is left
// unchanged (any bold/code within it was already applied upstream).
func applyLeader(line string) string {
	if m := reBulletLeader.FindStringSubmatch(line); m != nil {
		indent, rest := m[1], m[3]
		return indent + inlineBulletStyle().Render("•") + " " + styleCheckboxPrefix(rest)
	}
	if m := reNumberLeader.FindStringSubmatch(line); m != nil {
		indent, delim, rest := m[1], m[2], m[3]
		return indent + inlineBulletStyle().Render(delim) + " " + styleCheckboxPrefix(rest)
	}
	// No bullet/number leader: a bare leading checkbox (after indent) may
	// still be present.
	indent := leadingIndent(line)
	rest := line[len(indent):]
	if styled, ok := tryCheckbox(rest); ok {
		return indent + styled
	}
	return line
}

// styleCheckboxPrefix styles a checkbox token at the start of rest if present,
// otherwise returns rest unchanged.
func styleCheckboxPrefix(rest string) string {
	if styled, ok := tryCheckbox(rest); ok {
		return styled
	}
	return rest
}

// tryCheckbox styles a leading checkbox token in s. It reports whether one was
// found. The token's trailing space (or end-of-string) is preserved (the spec
// §9.5): "[ ]"→dim ☐, "[x]"/"[X]"/"[✓]"→green ☑, "[•]"→yellow ◐.
func tryCheckbox(s string) (string, bool) {
	m := reCheckbox.FindStringSubmatch(s)
	if m == nil {
		return s, false
	}
	var glyph string
	switch m[1] {
	case " ":
		glyph = inlineCheckboxTodo().Render("☐")
	case "x", "X", "✓":
		glyph = inlineCheckboxDone().Render("☑")
	case "•":
		glyph = inlineCheckboxProg().Render("◐")
	}
	// m[0] is the full match including the trailing space (or empty at EOL);
	// keep the same trailing text after the token.
	trailer := s[len(m[0]):]
	sep := m[2] // " " or ""
	return glyph + sep + trailer, true
}

// leadingIndent returns the run of leading spaces/tabs in s.
func leadingIndent(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}

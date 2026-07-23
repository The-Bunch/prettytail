package render

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Line-type colour system for console lines. After ANSI hygiene
// and inline markup (§9), a line with no SGR of its own is classified by its
// leading token and coloured as a whole line-type: headings, turn banners,
// commands, tool-trace glyphs and separators. Line-faithful as ever — no
// characters are reordered and (deliberately) heading hashes
// are KEPT. Self-styled lines never reach here (the caller applies line-type
// only when the line carried no SGR).
//
// Order within styleConsoleLine: hygiene → inline markup (§9) → line-type
// (§10, here) → diff/timestamp/path. The Index:/diff-opener separators live
// in the caller because they also toggle unified-diff hunk context (§10.5,
// §9.6); this file styles the non-diff, non-separator line types.

// Line-type styles. Built fresh per call so they bind to the
// current lipgloss default renderer — colour is resolved at render time,
// matching how the tool adapts to the terminal it runs in.

// headingStyle bolds a whole heading line, hashes kept (§10.1, by design).
func headingStyle() lipgloss.Style { return lipgloss.NewStyle().Bold(true) }

// bannerStyle is the bold-cyan turn-banner line ("> agent · model", §10.2).
func bannerStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Dark: "44", Light: "30"})
}

// commandPromptStyle is the bold-green "$" of a shell command line (§10.3).
func commandPromptStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Dark: "42", Light: "28"})
}

// Tool-trace glyph colours (§10.4): → blue · ← magenta · ⚙ yellow · ✱ cyan ·
// ✓ green · ✗ red. Each styles only the leading glyph; the rest of the line
// text is untouched save for a trailing "[…]" metadata segment, which is
// dimmed.
func glyphStyle(glyph string) lipgloss.Style {
	var c lipgloss.TerminalColor
	switch glyph {
	case "→":
		c = lipgloss.AdaptiveColor{Dark: "39", Light: "26"} // blue
	case "←":
		c = lipgloss.AdaptiveColor{Dark: "170", Light: "127"} // magenta
	case "⚙":
		c = lipgloss.AdaptiveColor{Dark: "220", Light: "178"} // yellow
	case "✱":
		c = lipgloss.AdaptiveColor{Dark: "44", Light: "30"} // cyan
	case "✓":
		c = lipgloss.AdaptiveColor{Dark: "42", Light: "28"} // green
	case "✗":
		c = lipgloss.AdaptiveColor{Dark: "196", Light: "160"} // red
	default:
		c = lipgloss.NoColor{}
	}
	return lipgloss.NewStyle().Foreground(c)
}

// separatorStyle dims a whole separator line ("Index: …" or a rule of
// =/- characters, §10.5) and the trailing "[…]" metadata of a glyph line
// (§10.4). Built fresh per call so it binds to the current lipgloss default
// renderer (dim colour resolved at render time), matching the other §10
// styles — this lets the colour assertions genuinely prove dim ANSI is
// emitted under a colour renderer. Same adaptive value as the shared
// dimStyle.
func separatorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Dark: "244", Light: "247"})
}

var (
	// reHeading matches a leading run of 1–6 '#' followed by a space (§10.1).
	// A shell comment "# note" collides here — an accepted, documented trade-off.
	reHeading = regexp.MustCompile(`^#{1,6} `)
	// reBanner matches a leading "> " turn banner (§10.2).
	reBanner = regexp.MustCompile(`^> `)
	// reCommand matches a leading "$ " shell-prompt command (§10.3).
	reCommand = regexp.MustCompile(`^\$ `)
	// reIndex matches an "Index: " separator that also opens diff context
	// (§10.5). Handled by the console renderer's hunk tracking, not here.
	reIndex = regexp.MustCompile(`^Index: `)
	// reRule matches a whole-line run of ≥4 '=' or '-' characters, i.e. a
	// standalone separator rule (§10.5). Anchored end-to-end so a diff
	// file-header ("--- a/x") or a bullet ("- x") never matches.
	reRule = regexp.MustCompile(`^(={4,}|-{4,})$`)
	// reTrailingMeta matches a trailing "[…]" metadata segment (optionally
	// preceded by whitespace) at end of line — dimmed on glyph lines (§10.4).
	reTrailingMeta = regexp.MustCompile(`(\s*)(\[[^\]]*\])$`)
	// toolGlyphs is the ordered set of leading tool-trace glyphs (§10.4).
	toolGlyphs = []string{"→", "←", "⚙", "✱", "✓", "✗"}
)

// applyLineType classifies a hygiene-clean, inline-processed line (no SGR of
// its own) by its leading token and returns the whole-line-type styling,
// reporting whether a line type matched. It handles headings,
// banners, commands, tool-trace glyphs and standalone rules. The Index:
// separator and diff openers are NOT handled here — they toggle hunk context
// and are resolved by the caller. When no line type matches, the line is
// returned unchanged with false, so the caller falls through to timestamp and
// path dimming (§3).
func applyLineType(line string) (string, bool) {
	switch {
	case reHeading.MatchString(line):
		// Whole line bold, hashes kept (§10.1).
		return headingStyle().Render(line), true
	case reBanner.MatchString(line):
		// Whole line bold cyan (§10.2).
		return bannerStyle().Render(line), true
	case reCommand.MatchString(line):
		// "$" bold green, the rest of the line at bright default (§10.3).
		rest := line[len("$ "):]
		return commandPromptStyle().Render("$") + " " + rest, true
	case reRule.MatchString(line):
		// Standalone =/- rule → dim whole line (§10.5).
		return separatorStyle().Render(line), true
	}
	if styled, ok := styleGlyphLine(line); ok {
		return styled, true
	}
	return line, false
}

// styleGlyphLine colours a leading tool-trace glyph and dims a trailing "[…]"
// metadata segment. It reports whether the line began with a
// known glyph. The glyph must be followed by a space or be the whole line so
// an inline occurrence mid-text is never mistaken for a leader.
func styleGlyphLine(line string) (string, bool) {
	for _, g := range toolGlyphs {
		if !strings.HasPrefix(line, g) {
			continue
		}
		rest := line[len(g):]
		// A leader glyph is followed by a space (or is the entire line).
		if rest != "" && !strings.HasPrefix(rest, " ") {
			continue
		}
		// Dim a trailing "[…]" metadata segment on the remainder (§10.4).
		rest = dimTrailingMeta(rest)
		return glyphStyle(g).Render(g) + rest, true
	}
	return "", false
}

// dimTrailingMeta dims a trailing "[…]" metadata segment,
// preserving any whitespace that precedes it. If there is none, s is returned
// unchanged.
func dimTrailingMeta(s string) string {
	m := reTrailingMeta.FindStringSubmatchIndex(s)
	if m == nil {
		return s
	}
	// Groups: 1 = leading whitespace, 2 = the "[…]" segment.
	head := s[:m[2]]     // everything before the whitespace+bracket
	ws := s[m[2]:m[3]]   // preserved whitespace
	meta := s[m[4]:m[5]] // the "[…]" segment
	return head + ws + separatorStyle().Render(meta)
}

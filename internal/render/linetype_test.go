package render

import (
	"strings"
	"testing"
)

// TestLineTypeHeading covers spec §10.1: a leading "#{1,6} " makes the
// WHOLE line bold, hashes KEPT (by design). Text is otherwise unchanged.
func TestLineTypeHeading(t *testing.T) {
	pinColour(t)
	for _, line := range []string{"# top", "## Build report", "###### deep"} {
		got, matched := applyLineType(line)
		if !matched {
			t.Fatalf("heading %q not matched", line)
		}
		if plain := stripANSI(got); plain != line {
			t.Errorf("heading text changed: got %q, want %q (hashes kept)", plain, line)
		}
	}
	// Seven hashes is not a heading (only 1–6).
	if _, matched := applyLineType("####### not a heading"); matched {
		t.Errorf("7-hash line should not match heading rule")
	}
	// A '#' with no space is not a heading.
	if _, matched := applyLineType("#nospace"); matched {
		t.Errorf("hash-without-space should not match heading rule")
	}
	// Prove the heading path emits bold ANSI under a colour renderer.
	withColour(t, func() {
		got, _ := applyLineType("## Build report")
		if !strings.Contains(got, "\x1b[1m") {
			t.Errorf("heading carried no bold ANSI under colour: %q", got)
		}
		if stripANSI(got) != "## Build report" {
			t.Errorf("heading hashes not kept: %q", stripANSI(got))
		}
	})
}

// TestLineTypeBanner covers spec §10.2: a leading "> " turn banner is a
// bold-cyan whole line.
func TestLineTypeBanner(t *testing.T) {
	pinColour(t)
	got, matched := applyLineType("> assistant · claude-opus · turn 3")
	if !matched {
		t.Fatalf("banner not matched")
	}
	if plain := stripANSI(got); plain != "> assistant · claude-opus · turn 3" {
		t.Errorf("banner text changed: %q", plain)
	}
	withColour(t, func() {
		got, _ := applyLineType("> agent · model")
		if got != bannerStyle().Render("> agent · model") {
			t.Errorf("banner not bold-cyan styled: %q", got)
		}
	})
	// A quoted line without the trailing space is not a banner.
	if _, matched := applyLineType(">notabanner"); matched {
		t.Errorf(">x without space should not match banner rule")
	}
}

// TestLineTypeCommand covers spec §10.3: a leading "$ " command styles the
// "$" bold green with the rest of the line at bright default (text unchanged).
func TestLineTypeCommand(t *testing.T) {
	pinColour(t)
	got, matched := applyLineType("$ go build ./cmd/widgetd")
	if !matched {
		t.Fatalf("command not matched")
	}
	if plain := stripANSI(got); plain != "$ go build ./cmd/widgetd" {
		t.Errorf("command text changed: %q", plain)
	}
	withColour(t, func() {
		got, _ := applyLineType("$ ls -la")
		// The "$" must carry the bold-green command-prompt style; the rest
		// stays plain (bright default) — no styling on the command text.
		if !strings.HasPrefix(got, commandPromptStyle().Render("$")) {
			t.Errorf("command prompt not bold-green: %q", got)
		}
		if !strings.HasSuffix(got, " ls -la") {
			t.Errorf("command body should be unstyled bright default: %q", got)
		}
	})
}

// TestLineTypeGlyphColours covers spec §10.4: each leading tool-trace
// glyph is coloured per the map; the remaining text is untouched.
func TestLineTypeGlyphColours(t *testing.T) {
	pinColour(t)
	for _, g := range []string{"→", "←", "⚙", "✱", "✓", "✗"} {
		line := g + " some trace text"
		got, matched := applyLineType(line)
		if !matched {
			t.Fatalf("glyph line %q not matched", line)
		}
		if plain := stripANSI(got); plain != line {
			t.Errorf("glyph line text changed: got %q, want %q", plain, line)
		}
	}
	withColour(t, func() {
		got, _ := applyLineType("→ dispatch")
		if !strings.HasPrefix(got, glyphStyle("→").Render("→")) {
			t.Errorf("→ glyph not coloured: %q", got)
		}
		// The trace text after the glyph stays plain.
		if !strings.HasSuffix(got, " dispatch") {
			t.Errorf("glyph trace text should be unstyled: %q", got)
		}
	})
	// A glyph mid-line (not a leader) must NOT match.
	if _, matched := applyLineType("arrow → in the middle"); matched {
		t.Errorf("mid-line glyph should not match glyph rule")
	}
	// A glyph immediately followed by non-space is not a leader.
	if _, matched := applyLineType("→dispatch"); matched {
		t.Errorf("glyph with no trailing space should not match")
	}
}

// TestLineTypeGlyphTrailingMeta covers spec §10.4: a trailing "[…]"
// metadata segment on a glyph line is dimmed; the glyph is coloured; the
// middle text is untouched.
func TestLineTypeGlyphTrailingMeta(t *testing.T) {
	pinColour(t)
	withColour(t, func() {
		got, matched := applyLineType("→ dispatch worker pool [pool=4]")
		if !matched {
			t.Fatalf("glyph-with-meta line not matched")
		}
		if plain := stripANSI(got); plain != "→ dispatch worker pool [pool=4]" {
			t.Errorf("text changed: %q", plain)
		}
		if !strings.Contains(got, separatorStyle().Render("[pool=4]")) {
			t.Errorf("trailing [meta] not dimmed: %q", got)
		}
		if !strings.HasPrefix(got, glyphStyle("→").Render("→")) {
			t.Errorf("glyph not coloured: %q", got)
		}
	})
}

// TestLineTypeRuleSeparator covers spec §10.5: a whole-line run of ≥4
// '=' or '-' is a dim separator; shorter runs and mixed content do not match.
func TestLineTypeRuleSeparator(t *testing.T) {
	pinColour(t)
	for _, line := range []string{"====", "==========", "----", "--------"} {
		if _, matched := applyLineType(line); !matched {
			t.Errorf("rule %q not matched", line)
		}
	}
	// Below threshold, or not the whole line → no match.
	for _, line := range []string{"===", "---", "==== title", "text ===="} {
		if _, matched := applyLineType(line); matched {
			t.Errorf("%q should not match rule separator", line)
		}
	}
	withColour(t, func() {
		got, _ := applyLineType("==========")
		if got != separatorStyle().Render("==========") {
			t.Errorf("rule not dim-styled: %q", got)
		}
	})
}

// TestLineTypeNoMatchPassesThrough asserts a plain line is returned unchanged
// with matched=false, so the caller falls through to timestamp/path dimming.
func TestLineTypeNoMatchPassesThrough(t *testing.T) {
	pinColour(t)
	got, matched := applyLineType("just an ordinary log line")
	if matched {
		t.Errorf("ordinary line matched a line type: %q", got)
	}
	if got != "just an ordinary log line" {
		t.Errorf("ordinary line changed: %q", got)
	}
}

// TestConsoleIndexOpensDiff covers spec §10.5: an "Index: " line is a dim
// separator that ALSO opens diff context, so the following +/- lines are
// diff-coloured even though no "@@"/"+++"/"---" preceded them (the "some
// diffs unstyled" fix). The hunk closes on the first non +/-/space line.
func TestConsoleIndexOpensDiff(t *testing.T) {
	pinColour(t)
	src := "Index: pkg/foo.go\n-old line\n+new line\nsummary text\n- action item\n"
	out, err := consoleRenderer{}.RenderDoc([]byte(src), 80)
	if err != nil {
		t.Fatalf("RenderDoc: %v", err)
	}
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(rows) != 5 {
		t.Fatalf("expected 5 rows, got %d: %q", len(rows), out)
	}
	// Row 0: Index line is a dim separator (and opened the hunk).
	if !strings.Contains(rows[0], separatorStyle().Render("Index: pkg/foo.go")) {
		t.Errorf("Index line not dim-styled: %q", rows[0])
	}
	// Row 1/2: +/- lines inside the Index-opened hunk are diff-coloured.
	if !strings.Contains(rows[1], consoleDiffDel().Render("-old line")) {
		t.Errorf("Index-opened delete not diff-styled: %q", rows[1])
	}
	if !strings.Contains(rows[2], consoleDiffAdd().Render("+new line")) {
		t.Errorf("Index-opened add not diff-styled: %q", rows[2])
	}
	// Row 3 ("summary text") closes the hunk; row 4 ("- action item") is a
	// bullet again, NOT diff-delete red (§9.6 close rules apply to Index).
	if strings.Contains(rows[4], consoleDiffDel().Render("- action item")) {
		t.Errorf("post-Index-hunk dash styled as diff-delete: %q", rows[4])
	}
	if !strings.Contains(rows[4], inlineBulletStyle().Render("•")) {
		t.Errorf("post-Index-hunk dash not bulleted: %q", rows[4])
	}
}

// TestConsoleLineTypeSelfStyledUntouched asserts a line that carries its own
// SGR is NOT reclassified by the line-type layer: even
// a "> " banner or "$ " command keeps only its own styling.
func TestConsoleLineTypeSelfStyledUntouched(t *testing.T) {
	pinColour(t)
	line := "\x1b[35m> self-styled banner\x1b[0m"
	got := consoleRenderer{}.renderLine(line, 80, &hunkState{})
	if got != sanitizeANSI(line)+resetSeq {
		t.Errorf("self-styled banner got line-type styling:\n got %q\nwant %q", got, sanitizeANSI(line)+resetSeq)
	}
}

// TestConsoleHeadingViaRenderLine locks the full pipeline for a heading: the
// console renderLine path bolds a heading whole-line with hashes kept, and
// (because a line type matched) no path dimming is layered on.
func TestConsoleHeadingViaRenderLine(t *testing.T) {
	pinColour(t)
	got := consoleRenderer{}.renderLine("## /srv/app path in heading", 80, &hunkState{})
	if plain := stripANSI(got); plain != "## /srv/app path in heading" {
		t.Errorf("heading text changed: %q", plain)
	}
	withColour(t, func() {
		got := consoleRenderer{}.renderLine("## /srv/app path in heading", 80, &hunkState{})
		if !strings.Contains(got, "\x1b[1m") {
			t.Errorf("heading not bold: %q", got)
		}
		// A matched line type owns the line: the path must NOT be dimmed on
		// top.
		if strings.Contains(got, separatorStyle().Render("/srv/app")) {
			t.Errorf("path dimmed on a heading line (should be line-type only): %q", got)
		}
	})
}

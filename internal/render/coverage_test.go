package render

import (
	"strings"
	"testing"
)

// Interpretation-coverage regressions (spec §11 — the human-eye audit).
// Each test guards one of the coverage gaps found by rendering
// a real 400-line log tail at v0.1.3: inline pairs that survived inside diff
// lines (§11.3), leader remainders (§11.2), wrapped bold that dropped to plain
// (§11.4), and markers surviving into wrapped rows (§11.1). Colour is pinned so
// the SGR assertions are deterministic.

// boldSeq is the SGR opener lipgloss emits for bold under the pinned profile.
func boldSeq(t *testing.T) string {
	t.Helper()
	r := inlineBold().Render("\x00")
	i := strings.Index(r, "\x00")
	if i <= 0 {
		t.Fatalf("bold style emitted no SGR under pinned colour: %q", r)
	}
	return r[:i]
}

// TestCoverageDiffLineInterpretsPairs asserts §11.3: a diff +/- body line that
// quotes markdown has its **bold** and `code` pairs interpreted (markers
// dropped, bold/code SGR present) WHILE the diff colour still owns the line.
func TestCoverageDiffLineInterpretsPairs(t *testing.T) {
	pinColour(t)
	for _, c := range []struct {
		name  string
		line  string
		open  bool
		diffO string // expected diff opener
	}{
		{"add", "+quoting **bold** and `code` here", true, colourOpener(t, consoleDiffAdd())},
		{"del", "-quoting **bold** and `code` here", true, colourOpener(t, consoleDiffDel())},
	} {
		t.Run(c.name, func(t *testing.T) {
			hs := &hunkState{open: c.open}
			got := consoleRenderer{}.renderLine(c.line, 200, hs)
			plain := stripANSI(got)
			if strings.Contains(plain, "**") {
				t.Errorf("bold markers survived in diff line: %q", plain)
			}
			if strings.Contains(plain, "`") {
				t.Errorf("code backticks survived in diff line: %q", plain)
			}
			if !strings.Contains(plain, "bold") || !strings.Contains(plain, "code") {
				t.Errorf("interpreted text missing: %q", plain)
			}
			if !strings.HasPrefix(got, c.diffO) {
				t.Errorf("diff line does not open with diff colour %q: %q", c.diffO, got)
			}
			// The diff colour must be re-asserted after the nested bold's reset
			// so the remainder of the line stays diff-coloured.
			if !strings.Contains(got, resetSeq+c.diffO) {
				t.Errorf("diff colour not re-asserted after nested reset: %q", got)
			}
		})
	}
}

// TestCoverageDiffContextInterpretsPairs asserts §11.3 for a context line
// (leading space) inside an open hunk: its embedded pairs are interpreted
// (markers dropped) even though it carries no added diff colour.
func TestCoverageDiffContextInterpretsPairs(t *testing.T) {
	pinColour(t)
	hs := &hunkState{open: true}
	got := consoleRenderer{}.renderLine(" context with **bold** here", 200, hs)
	plain := stripANSI(got)
	if strings.Contains(plain, "**") {
		t.Errorf("bold markers survived in diff context line: %q", plain)
	}
	if !strings.Contains(got, boldSeq(t)) {
		t.Errorf("bold not interpreted in diff context line: %q", got)
	}
}

// TestCoverageLeaderRemainderPairs asserts §11.2: after a checkbox/bullet
// leader is glyphed, the remainder still gets full §9 treatment — a trailing
// **bold** and `code` are interpreted, markers gone.
func TestCoverageLeaderRemainderPairs(t *testing.T) {
	pinColour(t)
	got := consoleRenderer{}.renderLine("- [x] **done** ran `make ship`", 200, &hunkState{})
	plain := stripANSI(got)
	if strings.Contains(plain, "**") || strings.Contains(plain, "`") {
		t.Errorf("markers survived in leader remainder: %q", plain)
	}
	if !strings.Contains(plain, "☑") {
		t.Errorf("checkbox glyph missing: %q", plain)
	}
	if !strings.Contains(got, boldSeq(t)) {
		t.Errorf("bold not interpreted in leader remainder: %q", got)
	}
}

// TestCoverageWrappedHeadingStaysBold asserts §11.4: a heading wide enough to
// wrap keeps bold on EVERY rendered row, including the "↪" continuation rows.
func TestCoverageWrappedHeadingStaysBold(t *testing.T) {
	pinColour(t)
	long := "## " + strings.Repeat("wide heading words ", 12)
	got := consoleRenderer{}.renderLine(long, 40, &hunkState{})
	rows := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(rows) < 2 {
		t.Fatalf("expected the heading to wrap into >=2 rows, got %d: %q", len(rows), got)
	}
	bold := boldSeq(t)
	for i, row := range rows {
		if !strings.Contains(row, bold) {
			t.Errorf("wrapped heading row %d not bold: %q", i, row)
		}
	}
}

// TestCoverageNoMarkersSurviveWrap asserts §11.1: a wide bold-pair line drops
// its markers on every row (row 0 and every "↪" continuation), because
// interpretation happens before wrap and is width-independent.
func TestCoverageNoMarkersSurviveWrap(t *testing.T) {
	pinColour(t)
	long := "prose **" + strings.Repeat("bold ", 20) + "end** then more plain tail words to force wrapping across many rows"
	got := consoleRenderer{}.renderLine(long, 40, &hunkState{})
	plain := stripANSI(got)
	if strings.Contains(plain, "**") {
		t.Errorf("bold markers survived into wrapped rows: %q", plain)
	}
	if !strings.Contains(got, boldSeq(t)) {
		t.Errorf("bold not interpreted on wide line: %q", got)
	}
}

// TestActiveSGR locks the SGR-carry helper that re-asserts an open style on
// continuation rows. A reset clears state; an unmatched
// opener carries.
func TestActiveSGR(t *testing.T) {
	for _, c := range []struct {
		name, in, want string
	}{
		{"none", "plain text", ""},
		{"openNoClose", "\x1b[1mbold text", "\x1b[1m"},
		{"openThenReset", "\x1b[1mbold\x1b[0m plain", ""},
		{"bareReset", "\x1b[1mbold\x1b[m plain", ""},
		{"twoOpen", "\x1b[92m\x1b[1mgreen bold", "\x1b[92m\x1b[1m"},
		{"reopenAfterReset", "\x1b[92mg\x1b[0m\x1b[92mmore", "\x1b[92m"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := activeSGR(c.in); got != c.want {
				t.Errorf("activeSGR(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// colourOpener returns the leading SGR a style emits under the pinned profile,
// mirroring nestInDiff's derivation, so the diff-colour assertions above bind
// to the real opener rather than a hard-coded code.
func colourOpener(t *testing.T, style interface{ Render(...string) string }) string {
	t.Helper()
	r := style.Render("\x00")
	i := strings.Index(r, "\x00")
	if i <= 0 {
		t.Fatalf("style emitted no SGR under pinned colour: %q", r)
	}
	return r[:i]
}

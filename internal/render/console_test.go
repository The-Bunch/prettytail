package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/The-Bunch/prettytail/internal/detect"
)

func TestConsoleRenderDocGolden(t *testing.T) {
	pinDeterminism(t)
	pinColour(t)
	got := readFixtureRenderDoc(t, consoleRenderer{}, "sample.console.log")
	assertGolden(t, "sample_console", got)
}

// TestConsoleFixtureDetectsConsole locks that the committed fixture — which
// carries real ANSI escapes — is classified as console, never markdown.
// If this regresses, the renderer would never run.
func TestConsoleFixtureDetectsConsole(t *testing.T) {
	src := readFixture(t, "sample.console.log")
	if f := detect.Detect("sample.console.log", src); f != detect.Console {
		t.Errorf("fixture detected as %v, want Console", f)
	}
}

// TestConsoleOneLineOneRow asserts the renderer never merges or reflows:
// each input line (that fits the width) yields exactly one output row.
func TestConsoleOneLineOneRow(t *testing.T) {
	pinColour(t)
	src := "first line\nsecond line\nthird line\n"
	got, err := consoleRenderer{}.RenderDoc([]byte(src), 80)
	if err != nil {
		t.Fatalf("RenderDoc: %v", err)
	}
	rows := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(rows) != 3 {
		t.Fatalf("expected 3 output rows, got %d: %q", len(rows), got)
	}
	for i, want := range []string{"first line", "second line", "third line"} {
		if plain := stripANSI(rows[i]); plain != want {
			t.Errorf("row %d = %q, want %q", i, plain, want)
		}
	}
}

// TestConsoleResetAppended asserts every rendered line ends with the SGR
// reset so styles never bleed between lines.
func TestConsoleResetAppended(t *testing.T) {
	pinColour(t)
	got, err := consoleRenderer{}.RenderUnit([]byte("plain line"), 80)
	if err != nil {
		t.Fatalf("RenderUnit: %v", err)
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), resetSeq) {
		t.Errorf("line does not end with reset %q: %q", resetSeq, got)
	}
}

// TestConsoleKeepsSGRDropsBrokenEscape asserts hygiene: a well-formed SGR is
// preserved verbatim, while a broken/erase escape is dropped.
func TestConsoleKeepsSGRDropsBrokenEscape(t *testing.T) {
	pinColour(t)
	// A valid green SGR wrapping text, plus a cursor-erase CSI that must go.
	line := "\x1b[32mgreen\x1b[0m and \x1b[Kerased"
	got := consoleRenderer{}.renderLine(line, 80, &hunkState{})
	if !strings.Contains(got, "\x1b[32mgreen\x1b[0m") {
		t.Errorf("valid SGR not preserved: %q", got)
	}
	if strings.Contains(got, "\x1b[K") {
		t.Errorf("erase escape \\x1b[K should be dropped: %q", got)
	}
	if plain := stripANSI(got); plain != "green and erased" {
		t.Errorf("visible text = %q, want %q", plain, "green and erased")
	}
}

// TestConsoleSelfStyledGetsNoAddedStyling asserts a line that already
// carries SGR of its own receives hygiene only — no diff/timestamp/path
// colouring is layered on top.
func TestConsoleSelfStyledGetsNoAddedStyling(t *testing.T) {
	pinColour(t)
	// A diff-add line that is ALSO self-styled: the added green must not be
	// applied, only the line's own SGR survives.
	line := "\x1b[1m+ added but self-styled\x1b[0m"
	got := consoleRenderer{}.renderLine(line, 80, &hunkState{})
	// The rendered line must equal the sanitized input plus the trailing
	// hygiene reset — no diff-add colouring layered on. sanitizeANSI keeps
	// the own SGR verbatim; renderLine appends one reset.
	want := sanitizeANSI(line) + resetSeq
	if got != want {
		t.Errorf("self-styled line got added styling:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(got, "\x1b[1m") {
		t.Errorf("line's own bold SGR should survive: %q", got)
	}
}

// TestConsoleDropsBareCSIResidue covers spec §7: bare SGR-shaped
// fragments WITHOUT an ESC byte (e.g. literal "[0m", "[38;5;81m") are
// removed in console format, while legitimate literals like "array[0]" or
// "[INFO]" (no "m" after the digits) are left untouched.
func TestConsoleDropsBareCSIResidue(t *testing.T) {
	pinColour(t)
	cases := []struct {
		name string
		line string
		want string // expected visible text after hygiene
	}{
		{"bareReset", "[0mconfig loaded", "config loaded"},
		{"bare256", "value [38;5;42mgreen text", "value green text"},
		{"multiple", "[0mstart[1mmid[0mend", "startmidend"},
		{"arrayIndexUntouched", "array[0] = 42", "array[0] = 42"},
		{"logLevelUntouched", "[INFO] ready", "[INFO] ready"},
		{"bracketDigitNoM", "step[12] done", "step[12] done"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := consoleRenderer{}.renderLine(c.line, 80, &hunkState{})
			if plain := stripANSI(got); plain != c.want {
				t.Errorf("renderLine(%q) visible = %q, want %q", c.line, plain, c.want)
			}
		})
	}
}

// TestConsoleBareResidueDoesNotTouchRealSGR asserts that dropping bare
// residue is escape-aware: a genuine SGR sequence (with its ESC byte) is
// preserved verbatim, its "…m" tail never mistaken for residue.
func TestConsoleBareResidueDoesNotTouchRealSGR(t *testing.T) {
	pinColour(t)
	line := "\x1b[32mgreen\x1b[0m then [0m bare"
	got := consoleRenderer{}.renderLine(line, 80, &hunkState{})
	if !strings.Contains(got, "\x1b[32mgreen\x1b[0m") {
		t.Errorf("real SGR not preserved: %q", got)
	}
	if plain := stripANSI(got); plain != "green then  bare" {
		t.Errorf("visible text = %q, want %q", plain, "green then  bare")
	}
}

// TestConsoleDiffStyling covers spec §3 + §9.6's diff rules on lines with
// no SGR of their own: hunk headers (@@) cyan and file headers (+++/---) bold
// always; +/- body lines coloured ONLY inside an open hunk. Each case runs
// with a fresh hunk state, opened first where the rule requires it. The body
// lines (add/del) now flow through nestInDiff, which sets the
// diff colour as the line's base without its own trailing reset — the wrap
// step adds the single line-terminating reset. Headers keep their lipgloss
// Render form (with its own reset) plus the wrap reset.
func TestConsoleDiffStyling(t *testing.T) {
	pinColour(t)
	cases := []struct {
		name string
		open bool // pre-open the hunk before rendering the line
		line string
		want string // expected full output including trailing reset(s)
	}{
		{"hunk", false, "@@ -1,2 +1,2 @@", consoleDiffHunk().Render("@@ -1,2 +1,2 @@") + resetSeq},
		{"fileAdd", false, "+++ b/main.go", consoleDiffHeader().Render("+++ b/main.go") + resetSeq},
		{"fileDel", false, "--- a/main.go", consoleDiffHeader().Render("--- a/main.go") + resetSeq},
		{"addInHunk", true, "+added code", nestInDiff("+added code", consoleDiffAdd()) + resetSeq},
		{"delInHunk", true, "-removed code", nestInDiff("-removed code", consoleDiffDel()) + resetSeq},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hs := &hunkState{open: c.open}
			got := consoleRenderer{}.renderLine(c.line, 80, hs)
			if got != c.want {
				t.Errorf("renderLine(%q) = %q, want %q", c.line, got, c.want)
			}
		})
	}
}

// TestConsoleTimestampDimmed asserts a leading timestamp is dimmed (the spec
// §3).
func TestConsoleTimestampDimmed(t *testing.T) {
	pinColour(t)
	got := consoleRenderer{}.renderLine("09:12:05 compiling", 80, &hunkState{})
	if !strings.HasPrefix(got, consoleDim().Render("09:12:05")) {
		t.Errorf("leading timestamp not dimmed: %q (want prefix %q)", got, consoleDim().Render("09:12:05"))
	}
	got2 := consoleRenderer{}.renderLine("2026-03-14T09:12:04Z starting", 80, &hunkState{})
	if !strings.HasPrefix(got2, consoleDim().Render("2026-03-14T09:12:04Z")) {
		t.Errorf("leading ISO timestamp not dimmed: %q", got2)
	}
}

// TestConsolePathDimmed asserts an absolute path / file:line ref is dimmed
// on a line with no SGR of its own.
func TestConsolePathDimmed(t *testing.T) {
	pinColour(t)
	got := consoleRenderer{}.renderLine("linked /srv/app/main.go:88 ok", 80, &hunkState{})
	if !strings.Contains(got, consoleDim().Render("/srv/app/main.go:88")) {
		t.Errorf("path ref not dimmed: %q", got)
	}
}

// TestConsoleWrapNoMerge asserts an over-width line hard-wraps into multiple
// rows (never merging content), with a dim "↪ " prefix on continuation rows,
// and that stripping ANSI + the prefix reconstructs the original text in
// order.
func TestConsoleWrapNoMerge(t *testing.T) {
	pinColour(t)
	const width = 20
	line := strings.Repeat("abcde ", 10) // 60 chars, well over 20
	line = strings.TrimSpace(line)
	got := consoleRenderer{}.renderLine(line, width, &hunkState{})
	rows := strings.Split(got, "\n")
	if len(rows) < 2 {
		t.Fatalf("expected wrap into multiple rows at width %d, got %d: %q", width, len(rows), got)
	}
	// Every row's visible width (prefix included) must fit the render width.
	for i, row := range rows {
		if w := ansi.StringWidth(row); w > width {
			t.Errorf("row %d width %d exceeds render width %d: %q", i, w, width, stripANSI(row))
		}
	}
	// Continuation rows carry the dim "↪ " set-out marker. This is a plain
	// line (content column 0), so the marker sits at column 0 and content
	// resumes at column 2.
	for i := 1; i < len(rows); i++ {
		if !strings.HasPrefix(stripANSI(rows[i]), "↪ ") {
			t.Errorf("continuation row %d missing continuation marker: %q", i, stripANSI(rows[i]))
		}
	}
	// Reassemble visible text (strip ANSI, drop the "↪ " prefix marker) and
	// confirm no characters were lost or merged.
	var recon strings.Builder
	for i, row := range rows {
		plain := stripANSI(row)
		if i > 0 {
			plain = strings.TrimPrefix(plain, "↪ ")
		}
		recon.WriteString(plain)
	}
	if recon.String() != line {
		t.Errorf("wrapped text lost content:\n got %q\nwant %q", recon.String(), line)
	}
}

// TestConsoleWrapHangingIndent covers the spec §9.7 wrap set-out: a
// continuation row is hang-indented so its content aligns under the logical
// line's first content column, with a dim "↪" marker two cells before that
// column. It checks a bullet, a nested bullet and a numbered leader, plus a
// plain line's col-0 marker, and verifies content reconstructs in order with
// no merge.
func TestConsoleWrapHangingIndent(t *testing.T) {
	pinColour(t)
	const width = 24
	cases := []struct {
		name       string
		line       string
		contentCol int // expected column where content resumes on every row
	}{
		// "- " leader → "•" + space, content column 2.
		{"bullet", "- alpha beta gamma delta epsilon zeta", 2},
		// two-space indent + "- " → content column 4.
		{"nestedBullet", "  - alpha beta gamma delta epsilon zeta", 4},
		// "3. " leader → delimiter width 2 + space, content column 3.
		{"numbered", "3. alpha beta gamma delta epsilon zeta", 3},
		// plain line, content column 0 → marker at col 0, content at col 2.
		{"plain", "alpha beta gamma delta epsilon zeta eta", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := consoleRenderer{}.renderLine(c.line, width, &hunkState{})
			rows := strings.Split(got, "\n")
			if len(rows) < 2 {
				t.Fatalf("expected wrap into ≥2 rows at width %d, got %d: %q", width, len(rows), stripANSI(got))
			}
			for i := 1; i < len(rows); i++ {
				plain := stripANSI(rows[i])
				// The marker "↪" must sit exactly two cells before the
				// content column, i.e. at column contentCol-2 (never < 0).
				markerCol := c.contentCol - 2
				if markerCol < 0 {
					markerCol = 0
				}
				want := strings.Repeat(" ", markerCol) + "↪ "
				if !strings.HasPrefix(plain, want) {
					t.Errorf("cont row %d = %q, want prefix %q (content col %d)", i, plain, want, c.contentCol)
				}
				// Content on the continuation row must begin at contentCol.
				if len(plain) > markerCol+2 && plain[markerCol+2] == ' ' {
					t.Errorf("cont row %d content should start at col %d, got extra space: %q", i, c.contentCol, plain)
				}
			}
			// No row exceeds the render width (marker + hang included).
			for i, row := range rows {
				if w := ansi.StringWidth(row); w > width {
					t.Errorf("row %d width %d exceeds %d: %q", i, w, width, stripANSI(row))
				}
			}
		})
	}
}

// TestConsoleContentColumn locks the first-content-column computation that
// drives the hang indent, independent of wrapping.
func TestConsoleContentColumn(t *testing.T) {
	pinColour(t)
	cases := []struct {
		name   string
		clean  string
		hadSGR bool
		want   int
	}{
		{"plain", "just text", false, 0},
		{"indentedPlain", "    just text", false, 4},
		{"bulletDash", "- item", false, 2},
		{"bulletStar", "* item", false, 2},
		{"bulletDot", "• item", false, 2},
		{"nestedBullet", "  - item", false, 4},
		{"numberDot", "3. item", false, 3},
		{"numberParen", "10) item", false, 4},
		{"selfStyledIndent", "  coloured", true, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := contentColumn(c.clean, c.hadSGR); got != c.want {
				t.Errorf("contentColumn(%q, %v) = %d, want %d", c.clean, c.hadSGR, got, c.want)
			}
		})
	}
}

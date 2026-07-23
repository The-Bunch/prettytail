package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// withColour runs fn under a freshly-built ANSI256/dark colour renderer and
// restores the previous default renderer afterwards. The package-level style
// vars capture the process's init-time default renderer, which is colourless
// under `go test` (no TTY); the established golden regime pins STRUCTURE, not
// colour, and the real binary colours because its init-time
// renderer sees a TTY. To prove the inline-markup styling paths genuinely
// emit colour (not just transform text), the styling-colour assertions build
// their expected spans fresh inside this scope.
func withColour(t *testing.T, fn func()) {
	t.Helper()
	prev := lipgloss.DefaultRenderer()
	r := lipgloss.NewRenderer(nil)
	r.SetColorProfile(termenv.ANSI256)
	r.SetHasDarkBackground(true)
	lipgloss.SetDefaultRenderer(r)
	defer lipgloss.SetDefaultRenderer(prev)
	fn()
}

// TestInlineBold covers spec §9.1: balanced **x** and __x__ render bold
// with the markers dropped; multiple pairs per line are fine; unbalanced
// markers stay literal.
func TestInlineBold(t *testing.T) {
	pinColour(t)
	cases := []struct {
		name      string
		line      string
		wantPlain string // visible text after markers dropped
		bold      bool   // a bold span is expected
	}{
		{name: "star", line: "build **passed** cleanly", wantPlain: "build passed cleanly", bold: true},
		{name: "under", line: "build __passed__ cleanly", wantPlain: "build passed cleanly", bold: true},
		{name: "twoPairs", line: "**one** and **two**", wantPlain: "one and two", bold: true},
		{name: "unbalanced", line: "a **dangling bold marker", wantPlain: "a **dangling bold marker", bold: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := applyInlineMarkup(c.line)
			if plain := stripANSI(got); plain != c.wantPlain {
				t.Errorf("visible = %q, want %q", plain, c.wantPlain)
			}
			if !c.bold {
				if stripANSI(got) != got {
					t.Errorf("expected no styling, got ANSI in %q", got)
				}
			}
		})
	}
	// Prove the bold path emits real bold ANSI under a colour renderer
	// (the lazy styles bind to the current default renderer).
	withColour(t, func() {
		got := applyInlineMarkup("x **y** z")
		if !strings.Contains(got, "\x1b[1m") {
			t.Errorf("bold span carried no bold ANSI under colour: %q", got)
		}
		if stripANSI(got) != "x y z" {
			t.Errorf("visible = %q, want %q", stripANSI(got), "x y z")
		}
	})
}

// TestInlineCodeSpan covers spec §9.2: balanced `x` renders in the code
// style (fg 222, no bg), backticks dropped; unbalanced stays literal.
func TestInlineCodeSpan(t *testing.T) {
	pinColour(t)
	got := applyInlineMarkup("run `go build` now")
	if plain := stripANSI(got); plain != "run go build now" {
		t.Errorf("visible = %q, want %q", plain, "run go build now")
	}
	if !strings.Contains(got, inlineCode().Render("go build")) {
		t.Errorf("code span not styled: %q", got)
	}
	// Unbalanced backtick stays literal.
	got2 := applyInlineMarkup("a `dangling backtick")
	if stripANSI(got2) != got2 || stripANSI(got2) != "a `dangling backtick" {
		t.Errorf("unbalanced backtick should stay literal: %q", got2)
	}
}

// TestInlineBullet covers spec §9.3: leading "- ", "* ", "• " become a
// dim-cyan "•" with the text unchanged and indent preserved.
func TestInlineBullet(t *testing.T) {
	pinColour(t)
	cases := []struct {
		name string
		line string
	}{
		{"dash", "- first item"},
		{"star", "* second item"},
		{"glyph", "• third item"},
		{"indented", "  - nested item"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := applyInlineMarkup(c.line)
			plain := stripANSI(got)
			// The leader char becomes "•" and the text is unchanged; indent
			// preserved.
			indent := leadingIndent(c.line)
			text := strings.TrimSpace(c.line[len(indent):])
			text = strings.TrimPrefix(text, "- ")
			text = strings.TrimPrefix(text, "* ")
			text = strings.TrimPrefix(text, "• ")
			want := indent + "• " + text
			if plain != want {
				t.Errorf("visible = %q, want %q", plain, want)
			}
			if !strings.Contains(got, inlineBulletStyle().Render("•")) {
				t.Errorf("bullet mark not dim-cyan styled: %q", got)
			}
		})
	}
}

// TestInlineNumberedLeader covers spec §9.4: "N. " / "N) " leaders keep
// the number+delimiter, styled dim-cyan; text unchanged.
func TestInlineNumberedLeader(t *testing.T) {
	pinColour(t)
	for _, c := range []struct{ line, delim string }{
		{"1. first", "1."},
		{"10) tenth", "10)"},
	} {
		got := applyInlineMarkup(c.line)
		if !strings.Contains(got, inlineBulletStyle().Render(c.delim)) {
			t.Errorf("numbered delimiter %q not styled in %q", c.delim, got)
		}
		if plain := stripANSI(got); plain != c.line {
			t.Errorf("visible = %q, want %q (text unchanged)", plain, c.line)
		}
	}
}

// TestInlineCheckbox covers spec §9.5: checkbox glyphs and colours,
// standalone and after a bullet.
func TestInlineCheckbox(t *testing.T) {
	pinColour(t)
	cases := []struct {
		name      string
		line      string
		wantGlyph string // styled glyph that must be present
		wantPlain string // full visible text
	}{
		{"todo", "[ ] pending task", inlineCheckboxTodo().Render("☐"), "☐ pending task"},
		{"doneX", "[x] finished", inlineCheckboxDone().Render("☑"), "☑ finished"},
		{"doneCap", "[X] finished", inlineCheckboxDone().Render("☑"), "☑ finished"},
		{"doneCheck", "[✓] finished", inlineCheckboxDone().Render("☑"), "☑ finished"},
		{"progress", "[•] working on it", inlineCheckboxProg().Render("◐"), "◐ working on it"},
		{"afterBullet", "- [ ] bulleted todo", inlineCheckboxTodo().Render("☐"), "• ☐ bulleted todo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := applyInlineMarkup(c.line)
			if !strings.Contains(got, c.wantGlyph) {
				t.Errorf("glyph not styled as expected in %q", got)
			}
			if plain := stripANSI(got); plain != c.wantPlain {
				t.Errorf("visible = %q, want %q", plain, c.wantPlain)
			}
		})
	}
}

// TestInlineBulletWithBold covers the §9.9 collision case: a bullet leader
// whose text contains a bold span — both are interpreted, line-faithfully.
func TestInlineBulletWithBold(t *testing.T) {
	pinColour(t)
	got := applyInlineMarkup("- deploy **now** please")
	plain := stripANSI(got)
	if plain != "• deploy now please" {
		t.Errorf("visible = %q, want %q", plain, "• deploy now please")
	}
	if !strings.Contains(got, inlineBulletStyle().Render("•")) {
		t.Errorf("bullet not styled: %q", got)
	}
	if !strings.Contains(got, inlineBold().Render("now")) {
		t.Errorf("bold span not styled: %q", got)
	}
}

// TestConsoleDiffBulletCollision covers spec §9.6: outside an open hunk a
// leading "- " is a bullet (dim-cyan •), NOT diff-red; a bare "+x"/"-x" line
// outside a hunk stays unstyled; inside a hunk, +/- are diff-coloured.
func TestConsoleDiffBulletCollision(t *testing.T) {
	pinColour(t)

	// Outside any hunk: "- item" is a bullet, never diff-delete red.
	t.Run("dashSpaceIsBulletOutsideHunk", func(t *testing.T) {
		got := consoleRenderer{}.renderLine("- steering item", 80, &hunkState{})
		if strings.Contains(got, consoleDiffDel().Render("- steering item")) {
			t.Errorf("dash-space outside a hunk styled as diff-delete: %q", got)
		}
		if !strings.Contains(got, inlineBulletStyle().Render("•")) {
			t.Errorf("dash-space outside a hunk not bulleted: %q", got)
		}
	})

	// Outside any hunk: a bare "+x" line stays unstyled (no leading space,
	// so not a bullet either).
	t.Run("barePlusOutsideHunkUnstyled", func(t *testing.T) {
		got := consoleRenderer{}.renderLine("+experimental flag", 80, &hunkState{})
		if stripANSI(got) != "+experimental flag" {
			t.Errorf("visible text changed: %q", stripANSI(got))
		}
		// The only escape present must be the trailing hygiene reset — no
		// diff-add colour was layered on.
		if got != "+experimental flag"+resetSeq {
			t.Errorf("bare +x outside a hunk got added styling: %q", got)
		}
	})

	// Hunk-then-bullet: an @@ opens the hunk (its +/- are diff-coloured);
	// after the hunk closes, a "- " is a bullet again.
	t.Run("hunkThenBullet", func(t *testing.T) {
		src := "@@ -1,1 +1,1 @@\n-old line\n+new line\nsummary of change\n- action item\n"
		out, err := consoleRenderer{}.RenderDoc([]byte(src), 80)
		if err != nil {
			t.Fatalf("RenderDoc: %v", err)
		}
		rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(rows) != 5 {
			t.Fatalf("expected 5 rows, got %d: %q", len(rows), out)
		}
		// Row 1 ("-old line") is inside the hunk → diff-delete.
		if !strings.Contains(rows[1], consoleDiffDel().Render("-old line")) {
			t.Errorf("in-hunk delete not diff-styled: %q", rows[1])
		}
		// Row 2 ("+new line") is inside the hunk → diff-add.
		if !strings.Contains(rows[2], consoleDiffAdd().Render("+new line")) {
			t.Errorf("in-hunk add not diff-styled: %q", rows[2])
		}
		// Row 3 ("summary…") closes the hunk (no +/-/space prefix).
		// Row 4 ("- action item") is now a bullet, NOT diff-delete red.
		if strings.Contains(rows[4], consoleDiffDel().Render("- action item")) {
			t.Errorf("post-hunk dash styled as diff-delete: %q", rows[4])
		}
		if !strings.Contains(rows[4], inlineBulletStyle().Render("•")) {
			t.Errorf("post-hunk dash not bulleted: %q", rows[4])
		}
	})
}

// TestConsoleSelfStyledSkipsInlineMarkup asserts inline markup is NOT applied
// to a line carrying its own SGR: self-styled lines are
// hygiene-only.
func TestConsoleSelfStyledSkipsInlineMarkup(t *testing.T) {
	pinColour(t)
	line := "\x1b[36m- **bold** bullet\x1b[0m"
	got := consoleRenderer{}.renderLine(line, 80, &hunkState{})
	// The markers must survive literally (no bold applied, no bullet glyph).
	if !strings.Contains(stripANSI(got), "- **bold** bullet") {
		t.Errorf("self-styled line had inline markup applied: %q", stripANSI(got))
	}
}

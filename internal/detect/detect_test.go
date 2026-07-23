package detect

import (
	"os"
	"strings"
	"testing"
)

// readFixture reads a fixture relative to the repo root (this test file
// lives at internal/detect/, two levels below root).
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/fixtures/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestDetect_FixtureMatrix(t *testing.T) {
	cases := []struct {
		name string
		want Format
	}{
		{"sample.md", Markdown},
		{"sample.ndjson", NDJSON},
		{"sample.logfmt", Logfmt},
		// sample.plain is only 4 lines: the rule-4d ">=20 lines with zero
		// markdown signals" nuance for Plain does not trigger below that
		// threshold, so per spec §2 verbatim this falls to the rule-d
		// markdown default, not Plain. See task output notes.
		{"sample.plain", Markdown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			head := readFixture(t, tc.name)
			got := Detect("testdata/fixtures/"+tc.name, head)
			if got != tc.want {
				t.Errorf("Detect(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestIsBinary_Fixture(t *testing.T) {
	head := readFixture(t, "binary.bin")
	if !IsBinary(head) {
		t.Error("IsBinary(binary.bin) = false, want true")
	}
	// Sanity: none of the text fixtures should trip the NUL guard.
	for _, name := range []string{"sample.md", "sample.ndjson", "sample.logfmt", "sample.plain"} {
		if IsBinary(readFixture(t, name)) {
			t.Errorf("IsBinary(%s) = true, want false", name)
		}
	}
}

func TestDetect_JSONArrayPerLine(t *testing.T) {
	head := []byte("[1,2,3]\n[4,5,6]\n[7,8,9]\n")
	got := Detect("events.json", head)
	if got != NDJSON {
		t.Errorf("Detect(.json array-per-line) = %v, want NDJSON", got)
	}
}

func TestDetect_JSONExtensionFallsBackToMarkdown(t *testing.T) {
	// Only one non-empty line: fails the ">=2 lines" clause of rule 3, so
	// it falls to the markdown default rather than NDJSON.
	head := []byte(`{"only":"one line"}` + "\n")
	got := Detect("single.json", head)
	if got != Markdown {
		t.Errorf("Detect(single-line .json) = %v, want Markdown", got)
	}

	// Two lines but not every line is valid JSON: also falls back.
	head2 := []byte("{\"a\":1}\nnot json at all\n")
	got2 := Detect("mixed.json", head2)
	if got2 != Markdown {
		t.Errorf("Detect(mixed .json) = %v, want Markdown", got2)
	}
}

func TestDetect_SignalFreeProseIsPlain(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "This is an ordinary line of prose with no special formatting at all."
	}
	head := []byte(strings.Join(lines, "\n") + "\n")
	// Non-.log extension: the .log rule would force
	// Console, so use a neutral extension to isolate the rule-4d Plain path.
	got := Detect("service.out", head)
	if got != Plain {
		t.Errorf("Detect(30-line signal-free prose) = %v, want Plain", got)
	}
}

func TestDetect_AmbiguousShortFileIsMarkdown(t *testing.T) {
	// 3 lines, exactly one markdown signal (a link) - below the >=2
	// signal threshold for rule 4c, and below the 20-line threshold for
	// the rule-4d Plain nuance, so it falls to the markdown default.
	head := []byte("first line of notes\nsee [here](https://example.com) for more\nlast line\n")
	got := Detect("notes.out", head)
	if got != Markdown {
		t.Errorf("Detect(3-line ambiguous file) = %v, want Markdown", got)
	}
}

func TestDetect_EmptyFileIsMarkdown(t *testing.T) {
	// Neutral extension: a .log would be forced to Console (spec §2
	// rule 2), so use a non-.log path to exercise the empty-head fallback.
	if got := Detect("anything.txt", nil); got != Markdown {
		t.Errorf("Detect(empty head) = %v, want Markdown", got)
	}
	if got := Detect("anything.txt", []byte("   \n\n\t\n")); got != Markdown {
		t.Errorf("Detect(blank-only head) = %v, want Markdown", got)
	}
}

func TestDetect_EmptyLogIsConsole(t *testing.T) {
	// spec §2 rule 2: a .log file must NEVER reach the markdown
	// fallback, even when empty.
	if got := Detect("empty.log", nil); got != Console {
		t.Errorf("Detect(empty .log) = %v, want Console", got)
	}
}

func TestDetect_ExtensionMapCaseInsensitive(t *testing.T) {
	cases := map[string]Format{
		"README.MD":      Markdown,
		"notes.Markdown": Markdown,
		"doc.MKD":        Markdown,
		"doc.mdown":      Markdown,
		"events.NDJSON":  NDJSON,
		"events.JSONL":   NDJSON,
	}
	for path, want := range cases {
		// Content deliberately contradicts the extension so we know the
		// extension map (not the sniff) drove the result.
		head := []byte("plain prose that looks nothing like the format\nsecond line\n")
		if got := Detect(path, head); got != want {
			t.Errorf("Detect(%s) = %v, want %v", path, got, want)
		}
	}
}

func TestDetect_Console(t *testing.T) {
	// A representative sample: ANSI escapes, and enough plain lines that,
	// absent the ANSI, it might sniff as prose. Rule 3a fires regardless of
	// extension or other signals.
	ansi := []byte("build \x1b[32mOK\x1b[0m\nrunning tests\ndone\n")

	cases := []struct {
		name string
		path string
		head []byte
		want Format
	}{
		// Rule 3a: ANSI beats everything, on any path — including a .md
		// extension (rule 4's stated exception: ANSI-styled content is
		// never re-parsed as markdown).
		{"ansi in .md file", "notes.md", ansi, Console},
		{"ansi in .txt file", "out.txt", ansi, Console},
		{"ansi in .log file", "run.log", ansi, Console},
		// Rule 2: .log never falls through to markdown fallback.
		{"plain .log is console", "run.log",
			[]byte("starting up\nprocessing\nfinished\n"), Console},
		// Rule 2: ndjson positive still wins inside a .log.
		{"ndjson .log is ndjson", "events.log",
			[]byte("{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n"), NDJSON},
		// Rule 2: logfmt positive still wins inside a .log.
		{"logfmt .log is logfmt", "app.log",
			[]byte("level=info msg=up t=1\nlevel=warn msg=slow t=2\n"), Logfmt},
		// Rule 3b: >=3 diff-marker lines (no ext hint) → console.
		{"diff hunk sniff", "patch.out",
			[]byte("--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n"), Console},
		// Rule 3b: only 2 diff markers is below threshold → not console.
		{"two diff markers below threshold", "patch.out",
			[]byte("--- a/x.go\n+++ b/x.go\nsome body line\nanother line\n"), Markdown},
		// Rule 3c: >=2 tool-trace glyph lines → console.
		{"tool glyphs sniff", "trace.out",
			[]byte("→ calling tool\n✓ done\nmisc\n"), Console},
		// Rule 3c: shell prompt counts as a glyph.
		{"shell prompt glyphs", "trace.out",
			[]byte("$ make build\n$ make test\nok\n"), Console},
		// Rule 3c: one glyph line is below threshold → not console.
		{"one glyph below threshold", "trace.out",
			[]byte("→ single arrow line\njust prose here\nand more prose\n"), Markdown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(tc.path, tc.head)
			if got != tc.want {
				t.Errorf("Detect(%s) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestDetect_ANSIBeatsMarkdownPositive(t *testing.T) {
	// Rule 4 EXCEPTION: even a strong markdown-positive head must resolve
	// to Console when it carries its own ANSI styling (rule 3a). The ext
	// here is neutral so the ext map does not short-circuit.
	head := []byte("# Heading\n\n- a list item\n\n`code` and \x1b[31mred\x1b[0m text\n")
	if got := Detect("report.out", head); got != Console {
		t.Errorf("Detect(markdown+ANSI) = %v, want Console", got)
	}
}

func TestDetect_PlainMarkdownFileUnaffected(t *testing.T) {
	// A .md file with no ANSI is unchanged by the console rules (ext map).
	if got := Detect("guide.md", readFixture(t, "sample.md")); got != Markdown {
		t.Errorf("Detect(sample.md) = %v, want Markdown", got)
	}
	// A signal-free .log with no ANSI is Console (rule 2), NOT Markdown —
	// this is the field-report regression the phase exists to fix.
	longLog := make([]string, 30)
	for i := range longLog {
		longLog[i] = "ordinary log line with no markup"
	}
	head := []byte(strings.Join(longLog, "\n") + "\n")
	if got := Detect("run.log", head); got != Console {
		t.Errorf("Detect(signal-free .log) = %v, want Console", got)
	}
}

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in     string
		want   Format
		wantOK bool
	}{
		{"md", Markdown, true},
		{"ndjson", NDJSON, true},
		{"logfmt", Logfmt, true},
		{"plain", Plain, true},
		{"console", Console, true},
		{"log", Console, true}, // alias
		{"LOG", Console, true}, // case-insensitive alias
		{"auto", 0, false},
		{"MD", Markdown, true}, // case-insensitive
		{"bogus", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := ParseFormat(tc.in)
		if ok != tc.wantOK {
			t.Errorf("ParseFormat(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("ParseFormat(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFormat_String(t *testing.T) {
	cases := []struct {
		f    Format
		want string
	}{
		{Markdown, "MD"},
		{NDJSON, "NDJSON"},
		{Logfmt, "LOGFMT"},
		{Plain, "PLAIN"},
		{Console, "LOG"},
	}
	for _, tc := range cases {
		if got := tc.f.String(); got != tc.want {
			t.Errorf("Format(%d).String() = %q, want %q", tc.f, got, tc.want)
		}
	}
}

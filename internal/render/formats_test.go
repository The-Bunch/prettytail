package render

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// reANSI matches ANSI escape sequences so tests can assert on structure
// without depending on exact colour bytes.
var reANSI = regexp.MustCompile("\x1b\\[[0-9;]*m")

// stripANSI removes ANSI escape sequences from s.
func stripANSI(s string) string {
	return reANSI.ReplaceAllString(s, "")
}

// readFixture reads testdata/fixtures/<name>.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	src, err := os.ReadFile(fixturePath(name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return src
}

// pinColour pins the lipgloss colour profile and background so the adaptive
// styles used by the ndjson/logfmt renderers emit deterministic bytes,
// matching the spec §4 determinism regime (dark, forced colour, ANSI256
// — the same profile the markdown renderer uses). It restores the previous
// renderer on cleanup so it does not leak into other tests.
func pinColour(t *testing.T) {
	t.Helper()
	prev := lipgloss.DefaultRenderer()
	r := lipgloss.NewRenderer(nil)
	r.SetColorProfile(termenv.ANSI256)
	r.SetHasDarkBackground(true)
	lipgloss.SetDefaultRenderer(r)
	t.Cleanup(func() { lipgloss.SetDefaultRenderer(prev) })
}

func TestNDJSONRenderDocGolden(t *testing.T) {
	pinDeterminism(t)
	pinColour(t)
	got := readFixtureRenderDoc(t, ndjsonRenderer{}, "sample.ndjson")
	assertGolden(t, "sample_ndjson", got)
}

// TestNDJSONBadLineFallsBackRawDim asserts a non-JSON line is emitted raw
// and dim, never as an error. The fixture's line 4 is
// deliberately not JSON, so the golden already covers this in a document
// context; here we assert the single-line behaviour directly.
func TestNDJSONBadLineFallsBackRawDim(t *testing.T) {
	pinColour(t)
	const bad = "not json at all"
	got, err := ndjsonRenderer{}.RenderUnit([]byte(bad), 80)
	if err != nil {
		t.Fatalf("RenderUnit on bad line returned error: %v", err)
	}
	want := dimStyle.Render(bad) + "\n"
	if got != want {
		t.Errorf("bad NDJSON line = %q, want raw-dim %q", got, want)
	}
	if !strings.Contains(got, bad) {
		t.Errorf("bad NDJSON line should contain the raw text, got %q", got)
	}
}

// TestNDJSONPrettyIndentAndGutter asserts a valid object is pretty-printed
// with a 2-space indent and drawn under a dim "│ " continuation gutter.
func TestNDJSONPrettyIndentAndGutter(t *testing.T) {
	pinColour(t)
	got, err := ndjsonRenderer{}.RenderUnit([]byte(`{"a":1,"b":2}`), 80)
	if err != nil {
		t.Fatalf("RenderUnit: %v", err)
	}
	if !strings.Contains(got, ndjsonGutter) {
		t.Errorf("expected dim gutter %q on continuation lines, got %q", ndjsonGutter, got)
	}
	// Strip ANSI to check the 2-space structural indent survived. The first
	// key sits on a continuation line, so it carries the "│ " gutter then the
	// json.Indent 2-space indent: "│   "a": 1".
	plain := stripANSI(got)
	if !strings.Contains(plain, "│   \"a\": 1") {
		t.Errorf("expected gutter + 2-space indented key, got plain:\n%s", plain)
	}
}

func TestLogfmtRenderDocGolden(t *testing.T) {
	pinDeterminism(t)
	pinColour(t)
	got := readFixtureRenderDoc(t, logfmtRenderer{}, "sample.logfmt")
	assertGolden(t, "sample_logfmt", got)
}

// TestLogfmtLevelMapping asserts the level word is uppercased, padded to 5
// and colour-mapped per spec §5.
func TestLogfmtLevelMapping(t *testing.T) {
	pinColour(t)
	cases := map[string]struct {
		style lipgloss.Style
	}{
		"error": {logfmtLevelError},
		"fatal": {logfmtLevelError},
		"warn":  {logfmtLevelWarn},
		"info":  {logfmtLevelInfo},
		"debug": {logfmtLevelDebug},
		"trace": {logfmtLevelDebug},
	}
	for level, c := range cases {
		got := styleLevel(level)
		wantWord := padRight(strings.ToUpper(level), 5)
		if want := c.style.Render(wantWord); got != want {
			t.Errorf("styleLevel(%q) = %q, want %q", level, got, want)
		}
		if plain := stripANSI(got); len(plain) < 5 {
			t.Errorf("styleLevel(%q) not padded to 5: plain %q (len %d)", level, plain, len(plain))
		}
	}
}

// TestLogfmtTSDimmed locks spec §5's "ts/time/timestamp value dim":
// the ts value must be wrapped in dimStyle.
func TestLogfmtTSDimmed(t *testing.T) {
	pinColour(t)
	got := logfmtRenderer{}.renderLine(`ts=T1 level=info msg="x"`)
	if !strings.HasPrefix(got, dimStyle.Render("T1")) {
		t.Errorf("ts value not dim-styled: %q (want prefix %q)", got, dimStyle.Render("T1"))
	}
}

// TestLogfmtUnknownLevel locks the default branch: an unmapped level is
// uppercased and padded to 5 but left uncoloured (spec §5 prescribes
// colours only for the known levels).
func TestLogfmtUnknownLevel(t *testing.T) {
	pinColour(t)
	got := styleLevel("notice")
	if stripANSI(got) != got {
		t.Errorf("unknown level should be uncoloured, got ANSI in %q", got)
	}
	if got != "NOTICE" {
		t.Errorf("styleLevel(notice) = %q, want %q (upper, no pad needed at len 6)", got, "NOTICE")
	}
}

// TestLogfmtFieldOrderAndSort asserts ts→level→msg ordering, remaining keys
// sorted, and the "key=" prefix present.
func TestLogfmtFieldOrderAndSort(t *testing.T) {
	pinColour(t)
	line := `ts=T1 level=info msg="hello" zeta=1 alpha=2`
	got := stripANSI(logfmtRenderer{}.renderLine(line))
	// ts value, then padded level, then msg, then sorted extra keys.
	if !strings.HasPrefix(got, "T1 INFO  hello ") {
		t.Errorf("field order wrong: %q", got)
	}
	ai := strings.Index(got, "alpha=")
	zi := strings.Index(got, "zeta=")
	if ai == -1 || zi == -1 || ai > zi {
		t.Errorf("remaining keys not sorted (alpha before zeta): %q", got)
	}
}

// TestLogfmtBadLineFallsBackRawDim asserts an unparseable logfmt line is
// emitted raw and dim, never an error.
func TestLogfmtBadLineFallsBackRawDim(t *testing.T) {
	pinColour(t)
	// A bare '=' with no key is a logfmt syntax error.
	bad := `="orphan value"`
	got, err := logfmtRenderer{}.RenderUnit([]byte(bad), 80)
	if err != nil {
		t.Fatalf("RenderUnit returned error: %v", err)
	}
	want := dimStyle.Render(bad) + "\n"
	if got != want {
		t.Errorf("bad logfmt line = %q, want raw-dim %q", got, want)
	}
}

func TestPlainRenderDocGolden(t *testing.T) {
	pinDeterminism(t)
	got := readFixtureRenderDoc(t, plainRenderer{}, "sample.plain")
	assertGolden(t, "sample_plain", got)
}

// TestPlainTrimsTrailingWhitespaceOnly asserts plain output trims trailing
// spaces/tabs but applies no styling and preserves leading whitespace and
// content.
func TestPlainTrimsTrailingWhitespaceOnly(t *testing.T) {
	src := "  leading kept   \ntabs\ttrimmed\t\nplain"
	got, err := plainRenderer{}.RenderDoc([]byte(src), 80)
	if err != nil {
		t.Fatalf("RenderDoc: %v", err)
	}
	want := "  leading kept\ntabs\ttrimmed\nplain\n"
	if got != want {
		t.Errorf("plain RenderDoc = %q, want %q", got, want)
	}
	if stripANSI(got) != got {
		t.Errorf("plain output must contain no ANSI styling: %q", got)
	}
}

// TestPlainRenderUnit asserts a streamed plain line is trailing-trimmed and
// newline-terminated.
func TestPlainRenderUnit(t *testing.T) {
	got, err := plainRenderer{}.RenderUnit([]byte("hello world   "), 80)
	if err != nil {
		t.Fatalf("RenderUnit: %v", err)
	}
	if got != "hello world\n" {
		t.Errorf("plain RenderUnit = %q, want %q", got, "hello world\n")
	}
}

// readFixtureRenderDoc reads a fixture and renders it whole with r.
func readFixtureRenderDoc(t *testing.T, r Renderer, fixture string) string {
	t.Helper()
	src := readFixture(t, fixture)
	got, err := r.RenderDoc(src, 80)
	if err != nil {
		t.Fatalf("RenderDoc(%s): %v", fixture, err)
	}
	if got == "" {
		t.Fatalf("RenderDoc(%s) returned empty output", fixture)
	}
	return got
}

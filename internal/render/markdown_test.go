package render

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/The-Bunch/prettytail/internal/detect"
)

// update regenerates the golden files: go test ./internal/render -update.
var update = flag.Bool("update", false, "update golden files")

// pinDeterminism applies the spec §4 determinism pins to a test: dark
// style, forced colour, fixed chroma theme (compiled in), width 80 at the
// call site. It also clears the renderer cache so each test builds fresh
// under its own pinned environment. A golden test that passes only
// sometimes is a defect — these pins are the guard against that.
func pinDeterminism(t *testing.T) {
	t.Helper()
	t.Setenv("PRETTYTAIL_STYLE", "dark")
	t.Setenv("CLICOLOR_FORCE", "1")
	resetCache()
}

// goldenPath returns testdata/golden/<name>.golden.
func goldenPath(name string) string {
	return filepath.Join("..", "..", "testdata", "golden", name+".golden")
}

func fixturePath(name string) string {
	return filepath.Join("..", "..", "testdata", "fixtures", name)
}

// assertGolden compares got against the named golden file, writing it first
// when -update is set.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := goldenPath(name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", name, err)
	}
	if got != string(want) {
		t.Errorf("golden %s mismatch:\n--- got ---\n%q\n--- want ---\n%q", name, got, string(want))
	}
}

func TestMarkdownRenderDocGolden(t *testing.T) {
	pinDeterminism(t)
	src, err := os.ReadFile(fixturePath("sample.md"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, err := markdownRenderer{}.RenderDoc(src, 80)
	if err != nil {
		t.Fatalf("RenderDoc: %v", err)
	}
	if got == "" {
		t.Fatal("RenderDoc returned empty output")
	}
	assertGolden(t, "sample_md", got)
}

func TestMarkdownRenderUnitCollapsesBlankRuns(t *testing.T) {
	pinDeterminism(t)
	// The SURROUNDING blank runs glamour adds must collapse: the unit begins
	// on its first content line (no leading blank) and ends with exactly one
	// trailing newline pair. The leading trim is
	// what keeps stream mode's between-block spacing at one blank line.
	got, err := markdownRenderer{}.RenderUnit([]byte("# Heading\n\n\n"), 80)
	if err != nil {
		t.Fatalf("RenderUnit: %v", err)
	}
	if strings.HasPrefix(got, "\n") {
		t.Errorf("RenderUnit should not begin with a blank line, got %q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("RenderUnit should end with exactly one trailing newline pair, got %q", got)
	}
	if strings.HasSuffix(got, "\n\n\n") {
		t.Errorf("RenderUnit left more than one trailing newline pair: %q", got)
	}
}

func TestWidthClamp(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{10, minWidth},
		{minWidth, minWidth},
		{80, 80},
		{maxWidth, maxWidth},
		{500, maxWidth},
	}
	for _, c := range cases {
		if got := clampWidth(c.in); got != c.want {
			t.Errorf("clampWidth(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestResolveStyle(t *testing.T) {
	cases := map[string]string{
		"dark":    "dark",
		"DARK":    "dark",
		"light":   "light",
		"notty":   "notty",
		"":        "",
		"bogus":   "",
		"unknown": "",
	}
	for in, want := range cases {
		t.Setenv("PRETTYTAIL_STYLE", in)
		if got := resolveStyle(); got != want {
			t.Errorf("resolveStyle() with PRETTYTAIL_STYLE=%q = %q, want %q", in, got, want)
		}
	}
}

// TestRawFallbackRenderer asserts the raw-passthrough renderer (used for the
// not-yet-styled formats and as the shape callers fall back to on a renderer
// error) returns the source unstyled and never errors.
func TestRawFallbackRenderer(t *testing.T) {
	src := []byte("plain line with no markup")
	doc, err := rawRenderer{}.RenderDoc(src, 80)
	if err != nil {
		t.Fatalf("rawRenderer.RenderDoc: %v", err)
	}
	if doc != string(src) {
		t.Errorf("rawRenderer.RenderDoc = %q, want %q", doc, string(src))
	}
	unit, err := rawRenderer{}.RenderUnit(src, 80)
	if err != nil {
		t.Fatalf("rawRenderer.RenderUnit: %v", err)
	}
	if unit != string(src)+"\n" {
		t.Errorf("rawRenderer.RenderUnit = %q, want %q", unit, string(src)+"\n")
	}
}

// TestRenderDocConcurrent exercises the renderer cache from many goroutines
// at once: the follow TUI calls RenderDoc concurrently with
// event handling, so a data race here would be a real defect. Run under
// `go test -race`.
func TestRenderDocConcurrent(t *testing.T) {
	pinDeterminism(t)
	src := []byte("# Title\n\nsome *text* and `code`.\n")
	const goroutines = 16
	done := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func(w int) {
			_, err := markdownRenderer{}.RenderDoc(src, 40+w)
			done <- err
		}(i)
	}
	for i := 0; i < goroutines; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent RenderDoc: %v", err)
		}
	}
}

// TestForRegistry confirms the registry maps each format to its renderer per
// spec §1: markdown for Markdown (and unknown), and the colour-spec
// renderers for NDJSON, Logfmt and Plain.
func TestForRegistry(t *testing.T) {
	if _, ok := For(detect.Markdown).(markdownRenderer); !ok {
		t.Error("For(Markdown) should return markdownRenderer")
	}
	if _, ok := For(detect.NDJSON).(ndjsonRenderer); !ok {
		t.Error("For(NDJSON) should return ndjsonRenderer")
	}
	if _, ok := For(detect.Logfmt).(logfmtRenderer); !ok {
		t.Error("For(Logfmt) should return logfmtRenderer")
	}
	if _, ok := For(detect.Plain).(plainRenderer); !ok {
		t.Error("For(Plain) should return plainRenderer")
	}
	// Unknown Format values fall back to markdown (the safety default).
	if _, ok := For(detect.Format(999)).(markdownRenderer); !ok {
		t.Error("For(unknown) should fall back to markdownRenderer")
	}
}

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/The-Bunch/prettytail/internal/detect"
)

// fixture returns the path to a repo testdata fixture.
func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", "fixtures", name)
}

// ansiSeq matches SGR/CSI escape sequences so tests can assert on the plain
// text content of rendered output regardless of where glamour splits colour
// runs.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// stripANSI removes escape sequences, then collapses runs of whitespace to
// single spaces so wrapped/padded rendered text can be substring-matched.
func stripANSI(s string) string {
	plain := ansiSeq.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(plain), " ")
}

// expectedUsageText is a literal copy of the verbatim block from the spec
// §3, kept independent of the usageText constant so a future accidental
// edit to usageText is caught here rather than silently matching itself.
const expectedUsageText = `prettytail — pretty, live tail for structured text files

usage: prettytail [flags] <file>

  -f, --format    auto|md|ndjson|logfmt|console|plain   (default: auto)
  -s, --stream    append-only rendered output (no full-screen UI)
  -n, --no-follow render current content once and exit
      --version   print version and exit
  -h, --help      this help

examples:
  prettytail notes.md              follow notes.md in a live pretty view
  prettytail -s agent.log | less   pipe-safe rendered stream
  prettytail -n README.md          render once, like a pager-less glow
`

func TestUsageTextIsVerbatim(t *testing.T) {
	if usageText != expectedUsageText {
		t.Fatalf("usageText drifted from spec §3 verbatim block\ngot:\n%q\nwant:\n%q", usageText, expectedUsageText)
	}
}

func TestRunVersion(t *testing.T) {
	origVersion, origCommit := Version, Commit
	Version, Commit = "1.2.3", "abc1234"
	defer func() { Version, Commit = origVersion, origCommit }()

	var out, errBuf bytes.Buffer
	code := run([]string{"--version"}, &out, &errBuf)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	want := "prettytail v1.2.3 (abc1234)\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
	if errBuf.String() != "" {
		t.Fatalf("stderr = %q, want empty", errBuf.String())
	}
}

func TestRunHelp(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		t.Run(flag, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			code := run([]string{flag}, &out, &errBuf)

			if code != exitOK {
				t.Fatalf("exit code = %d, want %d", code, exitOK)
			}
			if out.String() != expectedUsageText {
				t.Fatalf("stdout = %q, want verbatim usage text %q", out.String(), expectedUsageText)
			}
			if errBuf.String() != "" {
				t.Fatalf("stderr = %q, want empty", errBuf.String())
			}
		})
	}
}

// assertErrorLine checks the spec §3 error contract: usage errors emit
// exactly one leading `prettytail: <message>\n` line to stderr, followed by
// the verbatim usage block. wantMsgSubstr must appear in that first line.
// A regression that dropped the `prettytail:` prefix, split the message
// across lines, or changed the wording would fail here.
func assertErrorLine(t *testing.T, stderr, wantMsgSubstr string) {
	t.Helper()
	if !strings.HasSuffix(stderr, expectedUsageText) {
		t.Fatalf("stderr = %q, want it to end with verbatim usage text", stderr)
	}
	prefix := strings.TrimSuffix(stderr, expectedUsageText)
	lines := strings.SplitAfter(prefix, "\n")
	// The message portion (everything before the usage block) must be a
	// single line: one entry plus a trailing empty split element.
	if len(lines) != 2 || lines[1] != "" {
		t.Fatalf("error message before usage is not a single line: %q", prefix)
	}
	firstLine := lines[0]
	if !strings.HasPrefix(firstLine, "prettytail: ") {
		t.Fatalf("error line = %q, want it to start with %q", firstLine, "prettytail: ")
	}
	if !strings.Contains(firstLine, wantMsgSubstr) {
		t.Fatalf("error line = %q, want it to contain %q", firstLine, wantMsgSubstr)
	}
}

func TestRunNoArgs(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run(nil, &out, &errBuf)

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if out.String() != "" {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
	assertErrorLine(t, errBuf.String(), "expected exactly one file argument")
}

func TestRunTwoArgs(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"a.md", "b.md"}, &out, &errBuf)

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	assertErrorLine(t, errBuf.String(), "expected exactly one file argument")
}

func TestRunUnknownFlag(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"--bogus", "x.md"}, &out, &errBuf)

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	assertErrorLine(t, errBuf.String(), "not defined")
}

func TestRunBadFormat(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"-f", "bogus", "x.md"}, &out, &errBuf)

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	assertErrorLine(t, errBuf.String(), "unknown format")
}

func TestRunValidFormats(t *testing.T) {
	// Every valid --format value must be accepted (not a usage error) and
	// carry through to a clean run. Stream mode follows forever, so drive it
	// under a cancellable context over a real temp file.
	t.Setenv("PRETTYTAIL_STYLE", "dark")
	streamFile := filepath.Join(t.TempDir(), "fmt.txt")
	if err := os.WriteFile(streamFile, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, format := range []string{"auto", "md", "ndjson", "logfmt", "plain", "console", "log"} {
		t.Run(format, func(t *testing.T) {
			code, stderr := runCancelledAfterFirstOutput(t, []string{"-f", format, streamFile})

			if code != exitOK {
				t.Fatalf("exit code = %d, want %d (stderr=%q)", code, exitOK, stderr)
			}
		})
	}
}

func TestRunShortAndLongFlagsAlias(t *testing.T) {
	// Stream mode now really follows the file, so its cases run under
	// a context we cancel to end the follow loop deterministically, pointed
	// at a real temp file so the initial content is consumed cleanly. The
	// no-follow cases hit runOnce over a fixture. Both prove the short/long
	// flag spellings alias to the same behaviour, which this test guards.
	t.Setenv("PRETTYTAIL_STYLE", "dark")
	sample := fixture("sample.md")

	streamFile := filepath.Join(t.TempDir(), "stream.md")
	if err := os.WriteFile(streamFile, []byte("# Streamed heading\n\nSome text.\n\n"), 0o644); err != nil {
		t.Fatalf("seed stream file: %v", err)
	}

	cases := []struct {
		name   string
		args   []string
		follow bool // stream mode: run under a cancellable context
	}{
		{"format long", []string{"--format", "ndjson", "--no-follow", sample}, false},
		{"format short", []string{"-f", "ndjson", "-n", sample}, false},
		{"stream long", []string{"--stream", streamFile}, true},
		{"stream short", []string{"-s", streamFile}, true},
		{"no-follow long", []string{"--no-follow", sample}, false},
		{"no-follow short", []string{"-n", sample}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			var code int
			if tc.follow {
				code, _ = runCancelledAfterFirstOutput(t, tc.args)
			} else {
				code = run(tc.args, &out, &errBuf)
			}
			if code != exitOK {
				t.Fatalf("exit code = %d, want %d (stderr=%q)", code, exitOK, errBuf.String())
			}
		})
	}
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes the runStream
// goroutine makes while the driver goroutine polls Len().
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runCancelledAfterFirstOutput drives a follow-mode invocation under a
// context that is cancelled once the first rendered bytes appear (or after a
// short grace period), so stream mode exits cleanly for the test. It asserts
// no timing — cancellation is the loop's normal SIGINT termination path. It
// returns the exit code and the captured stderr text (read only after the
// run goroutine has returned, so no race).
func runCancelledAfterFirstOutput(t *testing.T, args []string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := &syncBuffer{}
	errBuf := &syncBuffer{}
	done := make(chan int, 1)
	go func() { done <- runCtx(ctx, args, out, errBuf) }()

	// Cancel once content has been rendered, or after a bounded wait.
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if out.Len() > 0 {
				cancel()
			}
		case <-deadline:
			cancel()
		case code := <-done:
			return code, errBuf.String()
		}
	}
}

func TestRunSingleValidArgExitsClean(t *testing.T) {
	// Under `go test`, os.Stdout is not a TTY, so mode selection lands on
	// stream mode. Stream mode follows forever, so cancel the context once
	// the initial (empty) read completes; a clean cancellation exits 0.
	t.Setenv("PRETTYTAIL_STYLE", "dark")
	streamFile := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(streamFile, []byte("# Notes\n\nBody.\n\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	code, stderr := runCancelledAfterFirstOutput(t, []string{streamFile})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, exitOK, stderr)
	}
}

func TestNoFollowRendersSampleMarkdown(t *testing.T) {
	t.Setenv("PRETTYTAIL_STYLE", "dark")
	t.Setenv("CLICOLOR_FORCE", "1")
	var out, errBuf bytes.Buffer
	// Flag after the positional, per the spec §3 invocation shape.
	code := run([]string{fixture("sample.md"), "--no-follow"}, &out, &errBuf)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, exitOK, errBuf.String())
	}
	if out.Len() == 0 {
		t.Fatal("stdout empty, want rendered document")
	}
	plain := stripANSI(out.String())
	if !strings.Contains(plain, "Prettytail Fixture") {
		t.Errorf("rendered output missing the h1 text; plain=%q", plain)
	}
	// Output must actually be styled (ANSI present) — otherwise the golden
	// determinism pins would be meaningless.
	if !ansiSeq.MatchString(out.String()) {
		t.Error("rendered output contains no ANSI escapes; expected styled text")
	}
	if errBuf.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errBuf.String())
	}
}

func TestNoFollowMissingFileExitsOne(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"-n", filepath.Join(t.TempDir(), "absent.md")}, &out, &errBuf)
	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
	line := errBuf.String()
	if !strings.HasPrefix(line, "prettytail: cannot read ") {
		t.Errorf("stderr = %q, want it to start with %q", line, "prettytail: cannot read ")
	}
}

func TestNoFollowBinaryGuardRefusesWithoutFormat(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"-n", fixture("binary.bin")}, &out, &errBuf)
	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(errBuf.String(), "looks like a binary file (use --format to force)") {
		t.Errorf("stderr = %q, want the binary-guard message", errBuf.String())
	}
}

func TestNoFollowBinaryGuardBypassedWhenFormatForced(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"-n", "-f", "md", fixture("binary.bin")}, &out, &errBuf)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, exitOK, errBuf.String())
	}
	if strings.Contains(errBuf.String(), "binary file") {
		t.Errorf("binary guard fired despite --format md: stderr=%q", errBuf.String())
	}
}

func TestNoFollowUnreadableFileReportsOSError(t *testing.T) {
	// A file that exists but cannot be read (perms) must exit 1 with the
	// verbatim "cannot read <path>: <os error>" line from spec §6, distinct
	// from the missing-file case. Skip if running as root (perms ignored).
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission bits are not enforced")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "locked.md")
	if err := os.WriteFile(path, []byte("# secret\n"), 0o000); err != nil {
		t.Fatalf("seed unreadable file: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) }) // let TempDir clean up

	var out, errBuf bytes.Buffer
	code := run([]string{"-n", path}, &out, &errBuf)
	if code != exitError {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, exitError, errBuf.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
	line := strings.TrimRight(errBuf.String(), "\n")
	if !strings.HasPrefix(line, "prettytail: cannot read "+path+": ") {
		t.Errorf("stderr = %q, want prefix %q", line, "prettytail: cannot read "+path+": ")
	}
	// The OS error must be appended, not the doubled path/op of the raw
	// *os.PathError — unwrapPathErr strips that.
	if strings.Contains(line, "open "+path) {
		t.Errorf("stderr = %q leaked the *os.PathError op/path; want unwrapped os error", line)
	}
}

func TestReorderArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"flag after positional", []string{"file.md", "--no-follow"}, []string{"--no-follow", "file.md"}},
		{"value flag after positional", []string{"file.md", "-f", "md"}, []string{"-f", "md", "file.md"}},
		{"already flags first", []string{"-n", "file.md"}, []string{"-n", "file.md"}},
		{"double dash guards positional", []string{"-n", "--", "-weird.md"}, []string{"-n", "-weird.md"}},
		{"value flag with equals", []string{"file.md", "-f=md"}, []string{"-f=md", "file.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reorderArgs(tc.in)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf("reorderArgs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSelectMode(t *testing.T) {
	tests := []struct {
		name     string
		noFollow bool
		stream   bool
		isTTY    bool
		want     runMode
	}{
		{"no-follow wins over stream+tty", true, true, true, modeOnce},
		{"no-follow wins over non-tty", true, false, false, modeOnce},
		{"no-follow alone", true, false, true, modeOnce},
		{"stream flag forces stream even on tty", false, true, true, modeStream},
		{"non-tty forces stream", false, false, false, modeStream},
		{"stream and non-tty both", false, true, false, modeStream},
		{"tty with no flags is tui", false, false, true, modeTUI},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectMode(tc.noFollow, tc.stream, tc.isTTY)
			if got != tc.want {
				t.Fatalf("selectMode(%v, %v, %v) = %v, want %v", tc.noFollow, tc.stream, tc.isTTY, got, tc.want)
			}
		})
	}
}

// TestTUIFormatSeedFromHead is the spec §8 regression (a): the follow
// (TUI) branch must resolve the format from the file head and hand it to
// ui.Config.Format, so the very first render uses the detected format rather
// than the model's zero-value Markdown. It exercises the exact wiring runTUI
// uses — readHead → detector(path, forced, isForced)(head) — so a regression
// that stopped seeding the head (reverting to Markdown) would fail here.
//
// The auto (unforced) case over a .log fixture must resolve Console: the
// fixture carries its own ANSI escapes and the .log
// extension is console-preferred (rule 2). The forced case confirms an
// explicit --format still short-circuits. A .md path stays Markdown, pinning
// the other path so the fix does not over-reach.
func TestTUIFormatSeedFromHead(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		format   string // as the --format flag would be parsed
		wantSeed detect.Format
	}{
		{"auto over .log yields console", "sample.console.log", "auto", detect.Console},
		{"forced console short-circuits", "sample.console.log", "console", detect.Console},
		{"auto over .md stays markdown", "sample.md", "auto", detect.Markdown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := fixture(tc.file)
			// Assert on the ui.Config runTUI actually builds (tuiConfig), so
			// the Format-seed WIRING is unit-covered: a regression that drops
			// the Format field from the struct literal fails here, not only in
			// teatest/e2e. tuiConfig internally does readHead's head → det →
			// Config.Format, the same path runTUI uses.
			head, err := readHead(path)
			if err != nil {
				t.Fatalf("readHead(%s): %v", path, err)
			}
			forced, isForced := detect.ParseFormat(tc.format)
			det := detector(path, forced, isForced)
			cfg := tuiConfig(path, "0.0.1-dev", det, head, nil)
			if cfg.Format != tc.wantSeed {
				t.Fatalf("ui.Config.Format for %s (--format %s) = %v (%s), want %v (%s)",
					tc.file, tc.format, cfg.Format, cfg.Format.String(), tc.wantSeed, tc.wantSeed.String())
			}
		})
	}
}

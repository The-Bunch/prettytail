// Package cli implements the prettytail command-line contract: flag
// parsing, usage text, exit codes and mode selection, per spec §3.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/term"

	"github.com/The-Bunch/prettytail/internal/detect"
	"github.com/The-Bunch/prettytail/internal/render"
	"github.com/The-Bunch/prettytail/internal/stream"
	"github.com/The-Bunch/prettytail/internal/tail"
	"github.com/The-Bunch/prettytail/internal/ui"
)

// Version and Commit are stamped via ldflags at build time (see Makefile).
var (
	Version = "dev"
	Commit  = "none"
)

// usageText is the verbatim CLI usage block from spec §3. Printed to
// stdout for --help and to stderr on any usage error.
const usageText = `prettytail — pretty, live tail for structured text files

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

// validFormats is the closed set of accepted --format values.
var validFormats = map[string]bool{
	"auto":    true,
	"md":      true,
	"ndjson":  true,
	"logfmt":  true,
	"plain":   true,
	"console": true, // spec §2 rule 1
	"log":     true, // alias for console
}

// Exit codes per spec §3.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// runMode is the selected mode of operation.
type runMode int

const (
	modeOnce runMode = iota
	modeStream
	modeTUI
)

// config holds the parsed CLI options for a single invocation.
type config struct {
	format   string
	stream   bool
	noFollow bool
	path     string
}

// valueFlags are the flags that consume the following token as their value
// when given in the separate `-f value` form (as opposed to `-f=value`).
// Every other flag in the set is boolean and takes no value.
var valueFlags = map[string]bool{
	"-f": true, "--f": true, "-format": true, "--format": true,
}

// reorderArgs moves recognised flag tokens (and the value that follows a
// value-flag) ahead of positional arguments, so stdlib flag — which stops
// at the first non-flag — still sees flags placed after the file path.
// Parsing stops permuting
// at a literal `--` (everything after is positional) and unknown tokens are
// left in place so flag.Parse still reports them as errors.
func reorderArgs(args []string) []string {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			// Everything after `--` is positional, verbatim.
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			// A value-flag in `-f value` form (no `=`) pulls its value too.
			if valueFlags[a] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}
		positionals = append(positionals, a)
		i++
	}
	return append(flags, positionals...)
}

// selectMode implements the mode-selection decision table from spec §3,
// in order: --no-follow wins outright; otherwise --stream or a non-TTY
// stdout selects stream mode; otherwise the full-screen TUI. It is a pure
// function of its three inputs so the decision table can be unit-tested
// deterministically without a real terminal.
func selectMode(noFollow, stream, isTTY bool) runMode {
	switch {
	case noFollow:
		return modeOnce
	case stream || !isTTY:
		return modeStream
	default:
		return modeTUI
	}
}

// Run is the CLI entrypoint called from main. args is the full process
// argument list (os.Args, including the program name at index 0). Run
// never calls os.Exit; main.go exits with the returned code.
func Run(args []string) int {
	if len(args) > 0 {
		args = args[1:]
	}
	return run(args, os.Stdout, os.Stderr)
}

// run is the testable core of the CLI: it parses args and dispatches,
// writing to the given writers instead of the global os.Stdout/os.Stderr
// so tests can assert exact output without capturing process streams. It
// installs a signal-cancelled context for the follow modes (stream/TUI),
// which run until interrupted; tests drive those modes via runCtx with a
// context they can cancel deterministically.
func run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCtx(ctx, args, stdout, stderr)
}

// runCtx is run with an injectable context so the follow modes can be
// exercised (and cancelled) deterministically in tests. ctx cancellation
// ends stream/TUI mode cleanly; the non-follow paths ignore it.
func runCtx(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prettytail", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we own all usage/error output ourselves

	var cfg config
	fs.StringVar(&cfg.format, "format", "auto", "")
	fs.StringVar(&cfg.format, "f", "auto", "")
	fs.BoolVar(&cfg.stream, "stream", false, "")
	fs.BoolVar(&cfg.stream, "s", false, "")
	fs.BoolVar(&cfg.noFollow, "no-follow", false, "")
	fs.BoolVar(&cfg.noFollow, "n", false, "")
	var showVersion bool
	fs.BoolVar(&showVersion, "version", false, "")
	var showHelp bool
	fs.BoolVar(&showHelp, "help", false, "")
	fs.BoolVar(&showHelp, "h", false, "")

	// stdlib flag stops parsing at the first non-flag argument, but the
	// spec §3 invocation puts the file path
	// before flags: `prettytail sample.md --no-follow`. Reorder so every
	// recognised flag is parsed regardless of where the positional sits.
	if err := fs.Parse(reorderArgs(args)); err != nil {
		fmt.Fprintf(stderr, "prettytail: %s\n", err)
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}

	// --version and --help take effect even alongside other flags.
	if showHelp {
		fmt.Fprint(stdout, usageText)
		return exitOK
	}
	if showVersion {
		fmt.Fprintf(stdout, "prettytail v%s (%s)\n", Version, Commit)
		return exitOK
	}

	if !validFormats[cfg.format] {
		fmt.Fprintf(stderr, "prettytail: unknown format %q\n", cfg.format)
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}

	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintf(stderr, "prettytail: expected exactly one file argument\n")
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	cfg.path = rest[0]

	isTTY := term.IsTerminal(int(os.Stdout.Fd()))
	switch selectMode(cfg.noFollow, cfg.stream, isTTY) {
	case modeOnce:
		return runOnce(cfg, stdout, stderr)
	case modeStream:
		return runStream(ctx, cfg, stdout, stderr)
	default:
		return runTUI(ctx, cfg, stdout, stderr)
	}
}

// headBytes is the detection/binary-guard sample size (spec §2, the spec
// §6): the first 8 KiB, or the whole file if smaller.
const headBytes = 8 * 1024

// runOnce implements --no-follow: read the file once, detect
// its format (honouring --format and the binary guard), render the whole
// document and print it, then exit 0. A missing/unreadable file exits 1; a
// binary file without --format exits 1 with the guard message. A renderer
// error is never fatal here — per spec §1 the caller falls back to
// printing the source raw and still succeeds, because rendering-truth says
// a display tool carries on rather than dying on a styling hiccup.
func runOnce(cfg config, stdout, stderr io.Writer) int {
	content, err := os.ReadFile(cfg.path)
	if err != nil {
		fmt.Fprintf(stderr, "prettytail: cannot read %s: %v\n", cfg.path, unwrapPathErr(err))
		return exitError
	}

	head := content
	if len(head) > headBytes {
		head = head[:headBytes]
	}

	forced, isForced := detect.ParseFormat(cfg.format)

	// Binary guard: NUL byte in the head, and no explicit --format, refuses
	// the file.
	if !isForced && detect.IsBinary(head) {
		fmt.Fprintf(stderr, "prettytail: %s looks like a binary file (use --format to force)\n", cfg.path)
		return exitError
	}

	format := forced
	if !isForced {
		format = detect.Detect(cfg.path, head)
	}

	width := 80
	if term.IsTerminal(int(os.Stdout.Fd())) {
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
			width = w
		}
	}

	out, err := render.For(format).RenderDoc(content, width)
	if err != nil {
		// Raw fallback: never fatal — show the source unstyled
		// and still exit clean.
		fmt.Fprint(stdout, string(content))
		return exitOK
	}
	fmt.Fprint(stdout, out)
	return exitOK
}

// unwrapPathErr returns the underlying error from an *os.PathError so the
// stderr line reads "cannot read <path>: <os error>" without repeating the
// path and operation twice.
func unwrapPathErr(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// runStream implements append-only stream mode: it follows the
// file via the tail engine and writes rendered, pipe-safe output (no
// screen-control sequences). Format is resolved per-read via a Detector
// that honours --format and the binary guard, re-detecting on Reset. A
// missing file prints the waiting notice once to stderr and holds (tail -F
// semantics); the loop ends when the context is cancelled (SIGINT/SIGTERM).
func runStream(ctx context.Context, cfg config, stdout, stderr io.Writer) int {
	forced, isForced := detect.ParseFormat(cfg.format)

	// Binary guard on the head of the current content, mirroring runOnce:
	// a NUL byte with no explicit --format refuses the file.
	if !isForced {
		if head, err := readHead(cfg.path); err == nil && detect.IsBinary(head) {
			fmt.Fprintf(stderr, "prettytail: %s looks like a binary file (use --format to force)\n", cfg.path)
			return exitError
		}
	}

	// Detector: --format short-circuits; otherwise detect from the content
	// head. Re-run on every Reset so a rotated file changing shape is
	// re-classified.
	det := detector(cfg.path, forced, isForced)

	width := 80
	if term.IsTerminal(int(os.Stdout.Fd())) {
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
			width = w
		}
	}

	events, err := tail.Follow(ctx, cfg.path)
	if err != nil {
		fmt.Fprintf(stderr, "prettytail: %v\n", err)
		return exitError
	}

	// Missing file at start: the tail engine emits an empty initial Append
	// then Waiting. Surface the waiting notice once on stderr,
	// then let stream mode keep following until the file appears.
	if _, statErr := os.Stat(cfg.path); statErr != nil && os.IsNotExist(statErr) {
		fmt.Fprintf(stderr, "prettytail: waiting for %s to appear…\n", cfg.path)
	}

	runner := stream.New(det, width, stdout)
	if err := runner.Run(events); err != nil {
		fmt.Fprintf(stderr, "prettytail: %v\n", err)
		return exitError
	}
	return exitOK
}

// detector builds the ui.Detector / stream detector used by the follow and
// stream modes: a forced --format short-circuits to
// that format; otherwise it detects from the supplied head. Factored out so
// the same resolution runs for the initial Config.Format seed, per-Reset
// re-detection, and can be unit-tested without starting the tail engine or
// the bubbletea program.
func detector(path string, forced detect.Format, isForced bool) func(head []byte) detect.Format {
	return func(head []byte) detect.Format {
		if isForced {
			return forced
		}
		return detect.Detect(path, head)
	}
}

// readHead reads the first headBytes of path for the binary guard and
// initial detection. A read error is returned to the caller, which treats
// an unreadable head as "not obviously binary" and lets the tail engine
// surface any real error.
func readHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, headBytes)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}

// runTUI implements the full-screen follow view: it resolves
// the format (honouring --format and the binary guard, re-detecting on
// Reset), follows the file via the tail engine and runs the bubbletea
// program over the resulting event stream. The program owns the terminal
// and restores it on every exit path; ctx cancellation (SIGINT/SIGTERM)
// quits it cleanly. A watcher-creation failure or a runtime UI error exits 1
// with a message on stderr, after the terminal has been restored.
func runTUI(ctx context.Context, cfg config, stdout, stderr io.Writer) int {
	forced, isForced := detect.ParseFormat(cfg.format)

	// Read the head once, up front: it drives BOTH the binary guard (the spec
	// §6) and the INITIAL format seed for the follow view. The
	// TUI never runs its own initial detection — the first content arrives as
	// an Append, which does not detect — so we must resolve the format here
	// and hand it to ui.Config.Format, or the model would render the whole
	// file with its zero-value Markdown until a Reset/`r` redetect.
	//
	// A readHead error is intentionally deferred, not surfaced here: an
	// unreadable/absent head yields an empty slice, which the binary guard
	// treats as "not obviously binary" (mirroring readHead's own contract)
	// and the detector resolves by extension. Any real read/open failure is
	// then surfaced authoritatively by tail.Follow below, so this path never
	// swallows an error the user should see.
	head, _ := readHead(cfg.path)

	// Binary guard, mirroring runOnce/runStream: a NUL byte with
	// no explicit --format refuses the file before we take over the screen.
	if !isForced && detect.IsBinary(head) {
		fmt.Fprintf(stderr, "prettytail: %s looks like a binary file (use --format to force)\n", cfg.path)
		return exitError
	}

	det := detector(cfg.path, forced, isForced)

	events, err := tail.Follow(ctx, cfg.path)
	if err != nil {
		fmt.Fprintf(stderr, "prettytail: %v\n", err)
		return exitError
	}

	prog := ui.New(tuiConfig(cfg.path, Version, det, head, events), stdout)

	if err := prog.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "prettytail: %v\n", err)
		return exitError
	}
	return exitOK
}

// tuiConfig assembles the ui.Config the follow view runs with, seeding
// Config.Format from det(head) so the very first render uses the
// format detected from the file head rather than the model's zero-value
// Markdown. Extracted so the seed wiring — once a real field defect — is
// covered by a unit test, not only transitively via teatest/e2e: a future edit
// that dropped the Format field would fail TestTUIFormatSeedFromHead directly.
func tuiConfig(path, version string, det ui.Detector, head []byte, events <-chan tail.Event) ui.Config {
	return ui.Config{
		Name:     filepath.Base(path),
		Version:  version,
		Format:   det(head), // spec §8: seed the first render's format
		Detector: det,
		Events:   events,
	}
}

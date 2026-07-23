package render

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"
)

// Width clamp for readability: a viewport narrower than 40 or
// wider than 120 renders at the clamped bound, left-aligned.
const (
	minWidth = 40
	maxWidth = 120
)

// chromaTheme pins code-fence syntax highlighting to a fixed chroma style.
// We override glamour's built-in per-style chroma config with
// this named theme so goldens are deterministic across environments.
const chromaTheme = "monokai"

// markdownRenderer renders markdown to ANSI via glamour. It is
// stateless; the (width, style) → *glamour.TermRenderer cache it consults
// is package-level and mutex-guarded, so the zero value is safe to share
// and to call concurrently.
type markdownRenderer struct{}

// cacheKey identifies a built glamour renderer by the two inputs that
// change its output: the clamped word-wrap width and the resolved style
// name (spec §3 — "built once per (width, style) pair and cached").
type cacheKey struct {
	width int
	style string
}

var (
	rendererMu    sync.Mutex
	rendererCache = map[cacheKey]*glamour.TermRenderer{}
)

// resetCache clears the renderer cache. It exists for tests, which pin a
// fresh environment (style, colour) and must not read a renderer built
// under a previous pin.
func resetCache() {
	rendererMu.Lock()
	rendererCache = map[cacheKey]*glamour.TermRenderer{}
	rendererMu.Unlock()
}

// invalidateWidth drops every cached renderer built for the given clamped
// width, so the next RenderDoc at that width rebuilds. The follow TUI calls
// this on resize: the viewport width changed, so any renderer
// cached for the old width is stale for the new one.
func invalidateWidth(width int) {
	clamped := clampWidth(width)
	rendererMu.Lock()
	defer rendererMu.Unlock()
	for k := range rendererCache {
		if k.width == clamped {
			delete(rendererCache, k)
		}
	}
}

// clampWidth applies the [40, 120] readability clamp.
func clampWidth(width int) int {
	switch {
	case width < minWidth:
		return minWidth
	case width > maxWidth:
		return maxWidth
	default:
		return width
	}
}

// resolveStyle returns the glamour standard-style name to use. PRETTYTAIL_STYLE
// (dark|light|notty) overrides auto-detection; any other value —
// including unset — means "let glamour auto-detect" and resolveStyle returns
// the empty string to signal that.
func resolveStyle() string {
	switch strings.ToLower(os.Getenv("PRETTYTAIL_STYLE")) {
	case "dark":
		return "dark"
	case "light":
		return "light"
	case "notty":
		return "notty"
	default:
		return ""
	}
}

// colorProfile fixes the termenv colour profile so output does not depend on
// the ambient terminal during tests or when piped. notty → Ascii (no colour);
// otherwise ANSI256, matching the terminal256 chroma formatter glamour uses.
func colorProfile(style string) termenv.Profile {
	if style == "notty" {
		return termenv.Ascii
	}
	return termenv.ANSI256
}

// build constructs (and does not cache) a glamour renderer for the given
// clamped width and resolved style. It pins the code-fence chroma theme to
// chromaTheme for determinism.
func build(width int, style string) (*glamour.TermRenderer, error) {
	styleCfg, err := standardStyleConfig(style)
	if err != nil {
		return nil, err
	}
	// Force our chroma theme: dropping the per-style Chroma config and naming
	// a Theme makes glamour highlight fences via that named chroma style
	// directly (see glamour/ansi/codeblock.go), instead of its "charm" theme.
	styleCfg.CodeBlock.Chroma = nil
	styleCfg.CodeBlock.Theme = chromaTheme

	opts := []glamour.TermRendererOption{
		glamour.WithStyles(styleCfg),
		glamour.WithColorProfile(colorProfile(style)),
		glamour.WithWordWrap(width),
		glamour.WithEmoji(),
	}
	r, err := glamour.NewTermRenderer(opts...)
	if err != nil {
		return nil, fmt.Errorf("render: build glamour renderer: %w", err)
	}
	return r, nil
}

// get returns a cached renderer for (width, style), building and caching one
// on first use. Concurrency-safe: the follow TUI renders from a goroutine.
func get(width int, style string) (*glamour.TermRenderer, error) {
	key := cacheKey{width: width, style: style}
	rendererMu.Lock()
	defer rendererMu.Unlock()
	if r, ok := rendererCache[key]; ok {
		return r, nil
	}
	r, err := build(width, style)
	if err != nil {
		return nil, err
	}
	rendererCache[key] = r
	return r, nil
}

// RenderDoc renders a whole markdown document at the clamped width. Callers
// apply any tail-window cap before calling — glamour never sees
// unbounded input from us.
func (markdownRenderer) RenderDoc(src []byte, width int) (string, error) {
	r, err := get(clampWidth(width), resolveStyle())
	if err != nil {
		return "", err
	}
	out, err := r.Render(string(src))
	if err != nil {
		return "", fmt.Errorf("render: markdown: %w", err)
	}
	return out, nil
}

// RenderUnit renders a single completed markdown block. It renders like
// RenderDoc, then collapses the surrounding blank runs glamour adds so the
// unit begins on its first content line and ends with exactly one trailing
// blank line. Trimming both ends keeps the
// stream's between-block spacing at exactly one blank line, never two.
func (m markdownRenderer) RenderUnit(src []byte, width int) (string, error) {
	out, err := m.RenderDoc(src, width)
	if err != nil {
		return "", err
	}
	return strings.Trim(out, "\n") + "\n\n", nil
}

// standardStyleConfig returns a fresh copy of the glamour standard style for
// name, or the auto-detected style when name is empty. The
// returned struct is a shallow copy of glamour's package-level style var, so
// build may safely reassign CodeBlock.Chroma/Theme on it without mutating the
// shared original (Chroma is a pointer we only ever nil out; Theme is a
// string field of the copied CodeBlock struct).
func standardStyleConfig(name string) (ansi.StyleConfig, error) {
	if name == "" {
		return autoStyleConfig(), nil
	}
	cfg, ok := styles.DefaultStyles[name]
	if !ok {
		return ansi.StyleConfig{}, fmt.Errorf("render: unknown style %q", name)
	}
	return *cfg, nil
}

// autoStyleConfig picks dark/light/notty the way glamour's WithAutoStyle
// does: notty when stdout is not a terminal, otherwise dark or light by the
// terminal's background. Product behaviour is adaptive; tests pin it via
// PRETTYTAIL_STYLE so this branch is never exercised deterministically.
func autoStyleConfig() ansi.StyleConfig {
	if !term.IsTerminal(uintptr(os.Stdout.Fd())) {
		return *styles.DefaultStyles[styles.NoTTYStyle]
	}
	if termenv.HasDarkBackground() {
		return *styles.DefaultStyles[styles.DarkStyle]
	}
	return *styles.DefaultStyles[styles.LightStyle]
}

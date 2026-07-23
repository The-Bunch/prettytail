// Package render turns detected content into pretty ANSI strings. It
// exposes a small Renderer interface and a registry that maps
// a detect.Format to the renderer that knows how to style it. Renderers
// never print — they return strings; callers own output and own the
// raw-fallback policy (§1): a renderer error never kills the process, the
// caller prints the source raw (dim) and carries on.
package render

import (
	"github.com/The-Bunch/prettytail/internal/detect"
)

// Renderer styles content for the terminal. Implementations must be safe
// for concurrent use — the follow TUI calls RenderDoc from a
// goroutine while events keep arriving.
type Renderer interface {
	// RenderDoc renders a whole document (TUI + --no-follow paths).
	RenderDoc(src []byte, width int) (string, error)
	// RenderUnit renders one streaming unit: a completed markdown block, or
	// one ndjson/logfmt/plain line (stream mode).
	RenderUnit(src []byte, width int) (string, error)
}

// For returns the Renderer for a format: markdown for
// Markdown, the colour-spec renderers for NDJSON, Logfmt and Plain (the spec
// §5), and the line-faithful console renderer for Console.
// Markdown is also the safety default for any unknown Format value.
func For(format detect.Format) Renderer {
	switch format {
	case detect.Markdown:
		return markdownRenderer{}
	case detect.NDJSON:
		return ndjsonRenderer{}
	case detect.Logfmt:
		return logfmtRenderer{}
	case detect.Plain:
		return plainRenderer{}
	case detect.Console:
		return consoleRenderer{}
	default:
		return markdownRenderer{}
	}
}

// InvalidateWidth drops any cached markdown renderer built for width (after
// the clamp in spec §3), so the next RenderDoc at that width rebuilds.
// The follow TUI calls this on resize. Formats without a width
// cache are unaffected.
func InvalidateWidth(width int) {
	invalidateWidth(width)
}

// rawRenderer is the shape the raw-fallback path (§1) wants: it emits the
// source unchanged (RenderDoc) or the line plus a newline (RenderUnit) — no
// styling, never errors. Callers use it when a renderer returns an error
// mid-follow so the process keeps running with unstyled output.
type rawRenderer struct{}

func (rawRenderer) RenderDoc(src []byte, _ int) (string, error) {
	return string(src), nil
}

func (rawRenderer) RenderUnit(src []byte, _ int) (string, error) {
	return string(src) + "\n", nil
}

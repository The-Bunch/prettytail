package render

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
)

// dimStyle is the shared adaptive "dim" style used by the ndjson/logfmt
// raw-fallback paths and by ndjson's continuation gutter. The
// dark value is normative; the light variant matches tone.
var dimStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{
	Dark:  "244",
	Light: "247",
})

// ndjsonGutter is the dim "│ " left gutter drawn on continuation lines of a
// pretty-printed JSON block.
var ndjsonGutter = dimStyle.Render("│ ")

// ndjsonRenderer renders NDJSON: each line is parsed, re-marshalled with a
// 2-space indent and syntax-highlighted (chroma json/monokai/terminal256),
// then indented under a dim "│ " gutter. A line that fails to parse is
// emitted raw and dim — never an error.
type ndjsonRenderer struct{}

// RenderDoc renders every line of src in order.
func (n ndjsonRenderer) RenderDoc(src []byte, width int) (string, error) {
	lines := strings.Split(strings.TrimRight(string(src), "\n"), "\n")
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(n.renderLine(line))
	}
	return b.String(), nil
}

// RenderUnit renders a single NDJSON line (stream mode).
func (n ndjsonRenderer) RenderUnit(src []byte, width int) (string, error) {
	return n.renderLine(strings.TrimRight(string(src), "\n")), nil
}

// renderLine turns one NDJSON line into its rendered block (trailing
// newline included). Parse failure → the raw line, dim.
func (ndjsonRenderer) renderLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return "\n"
	}

	// json.Indent (not json.Marshal) implements spec §5's "re-marshal
	// pretty with 2-space indent": it reformats the source with indentation
	// while preserving the original key order, which keeps goldens
	// deterministic. It also validates, so a malformed line errors here.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(trimmed), "", "  "); err != nil {
		// Parse failure: raw line, dim.
		return dimStyle.Render(line) + "\n"
	}

	highlighted := highlightJSON(pretty.String())

	// Draw the dim "│ " gutter on continuation lines; the first line sits
	// flush so the block reads as one unit under the object's opening brace.
	out := strings.Split(strings.TrimRight(highlighted, "\n"), "\n")
	var b strings.Builder
	for i, l := range out {
		if i > 0 {
			b.WriteString(ndjsonGutter)
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

// highlightJSON syntax-highlights pretty-printed JSON via the chroma json
// lexer, monokai style and terminal256 formatter. On any
// highlighter error it returns the input unchanged so rendering never fails.
func highlightJSON(pretty string) string {
	lexer := lexers.Get("json")
	if lexer == nil {
		return pretty
	}
	iterator, err := lexer.Tokenise(nil, pretty)
	if err != nil {
		return pretty
	}
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		return pretty
	}
	style := styles.Get(chromaTheme)
	if style == nil {
		return pretty
	}
	var b strings.Builder
	if err := formatter.Format(&b, style, iterator); err != nil {
		return pretty
	}
	return b.String()
}

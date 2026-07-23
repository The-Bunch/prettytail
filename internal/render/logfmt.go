package render

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/go-logfmt/logfmt"
)

// Logfmt colour styles. Dark values normative; light variants
// chosen to match tone. Levels: error/fatal → red bold · warn → yellow ·
// info → green · debug/trace → dim.
var (
	logfmtLevelError = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Dark: "196", Light: "160"})
	logfmtLevelWarn  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Dark: "214", Light: "130"})
	logfmtLevelInfo  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Dark: "42", Light: "28"})
	logfmtLevelDebug = dimStyle
	// logfmtKey renders remaining "key=" prefixes bold-dim.
	logfmtKey = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Dark: "244", Light: "247"})
)

// logfmtRenderer renders logfmt lines per spec §5: ts/time/timestamp dim,
// level colour-mapped + uppercased + padded to 5, msg/message after the
// level in the default colour, remaining keys sorted and rendered "key="
// bold-dim + value default. A tokeniser error → the raw line, dim.
type logfmtRenderer struct{}

// RenderDoc renders every line of src in order.
func (r logfmtRenderer) RenderDoc(src []byte, width int) (string, error) {
	lines := strings.Split(strings.TrimRight(string(src), "\n"), "\n")
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(r.renderLine(line))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// RenderUnit renders a single logfmt line (stream mode).
func (r logfmtRenderer) RenderUnit(src []byte, width int) (string, error) {
	return r.renderLine(strings.TrimRight(string(src), "\n")) + "\n", nil
}

// renderLine renders one logfmt line (no trailing newline). Tokeniser error
// or malformed record → the raw line, dim.
func (logfmtRenderer) renderLine(line string) string {
	if strings.TrimSpace(line) == "" {
		return ""
	}

	pairs, ok := decodeLogfmt(line)
	if !ok {
		return dimStyle.Render(line)
	}

	var (
		ts       string
		hasTS    bool
		level    string
		hasLevel bool
		msg      string
		hasMsg   bool
		rest     = make(map[string]string)
	)
	for _, kv := range pairs {
		switch strings.ToLower(kv.key) {
		case "ts", "time", "timestamp":
			if !hasTS {
				ts, hasTS = kv.val, true
				continue
			}
		case "level", "lvl":
			if !hasLevel {
				level, hasLevel = kv.val, true
				continue
			}
		case "msg", "message":
			if !hasMsg {
				msg, hasMsg = kv.val, true
				continue
			}
		}
		// Duplicate reserved keys beyond the first spill into rest; duplicate
		// rest keys keep the last value (last-wins) — logfmt has no defined
		// duplicate-key semantics and this is a display tool.
		rest[kv.key] = kv.val
	}

	var parts []string
	if hasTS {
		parts = append(parts, dimStyle.Render(ts))
	}
	if hasLevel {
		parts = append(parts, styleLevel(level))
	}
	if hasMsg {
		parts = append(parts, msg)
	}

	keys := make([]string, 0, len(rest))
	for k := range rest {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, logfmtKey.Render(k+"=")+rest[k])
	}

	return strings.Join(parts, " ")
}

// styleLevel uppercases the level word, pads it to width 5 and applies the
// colour mapping. Unknown levels get no colour but are still
// uppercased and padded so columns line up.
func styleLevel(level string) string {
	word := padRight(strings.ToUpper(level), 5)
	switch strings.ToLower(level) {
	case "error", "fatal":
		return logfmtLevelError.Render(word)
	case "warn":
		return logfmtLevelWarn.Render(word)
	case "info":
		return logfmtLevelInfo.Render(word)
	case "debug", "trace":
		return logfmtLevelDebug.Render(word)
	default:
		// spec §5 prescribes colours only for the known levels; an
		// unrecognised level is still uppercased and padded so columns align,
		// but left uncoloured.
		return word
	}
}

// padRight pads s with spaces to at least width runes (never truncates).
// The level words it pads (ERROR/WARN/INFO/DEBUG/TRACE/FATAL) are ASCII, so
// utf8.RuneCountInString is used for correctness should a non-ASCII level
// ever arrive.
func padRight(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

// logfmtPair is one decoded key/value.
type logfmtPair struct {
	key string
	val string
}

// decodeLogfmt tokenises a single logfmt line, preserving key order. ok is
// false on a tokeniser error, so the caller can fall back to raw-dim.
func decodeLogfmt(line string) ([]logfmtPair, bool) {
	dec := logfmt.NewDecoder(strings.NewReader(line))
	var pairs []logfmtPair
	for dec.ScanRecord() {
		for dec.ScanKeyval() {
			pairs = append(pairs, logfmtPair{
				key: string(dec.Key()),
				val: string(dec.Value()),
			})
		}
	}
	if dec.Err() != nil {
		return nil, false
	}
	return pairs, true
}

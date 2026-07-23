// Package detect implements prettytail's format detection: the extension
// map, content sniff and markdown-fallback rules from spec §2. Detect
// itself does no I/O — callers read the file and hand in the head window
// (first 8 KiB, or the whole file if smaller); the --format override
// (rule 1) and the binary guard (rule 2) are caller concerns, but this
// package exports the machinery both need: ParseFormat and IsBinary.
package detect

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// Format is a detected (or forced) content format.
type Format int

const (
	Markdown Format = iota
	NDJSON
	Logfmt
	Plain
	Console
)

// String returns the status-bar badge for f: MD, NDJSON, LOGFMT or PLAIN,
// or LOG for console/tool output.
func (f Format) String() string {
	switch f {
	case Markdown:
		return "MD"
	case NDJSON:
		return "NDJSON"
	case Logfmt:
		return "LOGFMT"
	case Plain:
		return "PLAIN"
	case Console:
		return "LOG"
	default:
		return "PLAIN"
	}
}

// IsBinary reports whether head looks like binary content. Per spec §2
// rule 2, a NUL byte anywhere in the sampled window disqualifies the file
// from text rendering.
func IsBinary(head []byte) bool {
	return bytes.IndexByte(head, 0) != -1
}

// ParseFormat maps a cli --format flag value
// (md|ndjson|logfmt|plain|console|log|auto) to a Format. "console" and its
// alias "log" both force Console. ok is false for
// "auto" (no override requested) and for any unrecognised value; callers
// should treat !ok as "let Detect decide".
func ParseFormat(s string) (Format, bool) {
	switch strings.ToLower(s) {
	case "md":
		return Markdown, true
	case "ndjson":
		return NDJSON, true
	case "logfmt":
		return Logfmt, true
	case "plain":
		return Plain, true
	case "console", "log":
		return Console, true
	default:
		return 0, false
	}
}

// extMap is the case-insensitive extension table from spec §2 rule 3.
var extMap = map[string]Format{
	".md":       Markdown,
	".markdown": Markdown,
	".mdown":    Markdown,
	".mkd":      Markdown,
	".ndjson":   NDJSON,
	".jsonl":    NDJSON,
}

const maxSniffLines = 200

// Console signal patterns, spec §2 rule 3.
var (
	// reANSI matches a CSI (\x1b[…) or OSC (\x1b]…) escape introducer —
	// rule 3a. Presence of any real escape sequence marks self-styled
	// content that must never be re-parsed as markdown (rule 4).
	reANSI = regexp.MustCompile(`\x1b[\[\]]`)
	// reDiffMarker matches a unified-diff line start — rule 3b. `@@`
	// hunk headers and `+++ `/`--- ` file headers count.
	reDiffMarker = regexp.MustCompile(`^(\+\+\+ |--- |@@)`)
	// reToolGlyph matches a tool-trace glyph or shell prompt at line
	// start — rule 3c.
	reToolGlyph = regexp.MustCompile(`^(\x{2192}|\x{2190}|\x{2699}|\x{2731}|\x{2713}|\x{2717}|\x{2022}|\$ )`)
)

// Markdown signal patterns, spec §2 rule 4c. Each is checked per line;
// countMarkdownSignals counts how many distinct categories fire anywhere
// in the head, not how many lines match.
var (
	reHeading    = regexp.MustCompile(`^#{1,6} `)
	reListDash   = regexp.MustCompile(`^\s*[-*+] `)
	reListNum    = regexp.MustCompile(`^\s*\d+\. `)
	reTable      = regexp.MustCompile(`^\s*\|.*\|`)
	reBlockquote = regexp.MustCompile(`^> `)
	reLink       = regexp.MustCompile(`\[.+\]\(.+\)`)
	reEmphStar   = regexp.MustCompile(`\*[^*\n]+\*`)
	reEmphUnder  = regexp.MustCompile(`_[^_\n]+_`)
)

// reLogfmtToken matches a key immediately followed by "=" with no spaces
// before it: key chars are [A-Za-z0-9_.-]+.
var reLogfmtToken = regexp.MustCompile(`[A-Za-z0-9_.-]+=`)

// nonEmptyLines splits head into lines, trims a trailing \r (in case CRLF
// slipped through before normalisation), drops blank lines and caps the
// result at maxSniffLines per spec §2 rule 4's "max 200".
func nonEmptyLines(head []byte) []string {
	raw := strings.Split(string(head), "\n")
	lines := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimSuffix(l, "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		lines = append(lines, l)
		if len(lines) >= maxSniffLines {
			break
		}
	}
	return lines
}

// Detect implements spec §2 rules 3-5: the extension map, then the
// content sniff, then the markdown fallback. Rule 1 (--format override)
// and rule 2 (binary guard) are the caller's responsibility — Detect
// assumes it is only ever handed text the caller intends to render.
func Detect(path string, head []byte) Format {
	lines := nonEmptyLines(head)

	// spec §2 rule 3a takes precedence over every other rule: content
	// carrying its own ANSI escapes must never be re-parsed as markdown,
	// and this beats even a positive md/ndjson/logfmt detection (rule 4).
	if reANSI.Match(head) {
		return Console
	}

	ext := strings.ToLower(filepath.Ext(path))
	if f, ok := extMap[ext]; ok {
		return f
	}
	if ext == ".json" {
		if len(lines) >= 2 && allJSONValues(lines) {
			return NDJSON
		}
		return Markdown // rule 3's "else Markdown-fallback rule 5"
	}

	// spec §2 rule 2: a .log file is console-preferred — an ndjson or
	// logfmt positive sniff may still win (existing rules), but it must
	// NEVER fall through to the markdown fallback.
	if ext == ".log" {
		if len(lines) > 0 {
			if isNDJSON(lines) {
				return NDJSON
			}
			if isLogfmt(lines) {
				return Logfmt
			}
		}
		return Console
	}

	if len(lines) == 0 {
		return Markdown // rule 5: empty file / empty head
	}

	return sniff(lines)
}

// allJSONValues reports whether every line parses as a JSON value (object,
// array, string, number, bool or null) — spec §2 rule 3's .json test.
func allJSONValues(lines []string) bool {
	for _, l := range lines {
		if !json.Valid([]byte(strings.TrimSpace(l))) {
			return false
		}
	}
	return true
}

// sniff implements spec §2 rule 4: NDJSON, then logfmt (only if NDJSON
// failed), then the markdown-signal count, then the rule-d default/plain
// nuance. lines is always non-empty (Detect handles the empty case).
func sniff(lines []string) Format {
	if isNDJSON(lines) {
		return NDJSON
	}
	if isLogfmt(lines) {
		return Logfmt
	}

	// spec §2 rules 3b/3c + rule 4: after the ndjson/logfmt positives
	// (which beat console) but before the markdown-signal count, the
	// diff-marker and tool-glyph console sniffs fire. (Rule 3a, ANSI, is
	// handled ahead of everything in Detect.) Console always beats the
	// markdown FALLBACK; explicit markdown signals (>=2 categories) still
	// win, since those are a positive md detection.
	if countMarkdownSignals(lines) >= 2 {
		return Markdown
	}
	if isConsole(lines) {
		return Console
	}
	if len(lines) >= 20 {
		return Plain
	}
	return Markdown
}

// isConsole implements spec §2 rules 3b and 3c: >=3 lines starting with
// a unified-diff marker, or >=2 lines starting with a tool-trace glyph.
func isConsole(lines []string) bool {
	diff, glyph := 0, 0
	for _, l := range lines {
		if reDiffMarker.MatchString(l) {
			diff++
		}
		if reToolGlyph.MatchString(l) {
			glyph++
		}
	}
	return diff >= 3 || glyph >= 2
}

// isNDJSON implements rule 4a: >=80% of lines parse as a JSON object or
// array (json.Valid, first non-space byte '{' or '[').
func isNDJSON(lines []string) bool {
	matched := 0
	for _, l := range lines {
		t := strings.TrimSpace(l)
		first := t[0]
		if (first == '{' || first == '[') && json.Valid([]byte(t)) {
			matched++
		}
	}
	return float64(matched)/float64(len(lines)) >= 0.8
}

// isLogfmt implements rule 4b: >=60% of lines contain >=2 key=value
// tokens. Only reached once isNDJSON has failed.
func isLogfmt(lines []string) bool {
	matched := 0
	for _, l := range lines {
		if len(reLogfmtToken.FindAllString(l, -1)) >= 2 {
			matched++
		}
	}
	return float64(matched)/float64(len(lines)) >= 0.6
}

// countMarkdownSignals implements rule 4c: the number of distinct markdown
// signal categories present anywhere across lines (not occurrence count).
func countMarkdownSignals(lines []string) int {
	var heading, fence, list, table, quote, link, emphasis bool
	for _, l := range lines {
		if reHeading.MatchString(l) {
			heading = true
		}
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = true
		}
		if reListDash.MatchString(l) || reListNum.MatchString(l) {
			list = true
		}
		if reTable.MatchString(l) {
			table = true
		}
		if reBlockquote.MatchString(l) {
			quote = true
		}
		if reLink.MatchString(l) {
			link = true
		}
		if reEmphStar.MatchString(l) || reEmphUnder.MatchString(l) {
			emphasis = true
		}
	}
	count := 0
	for _, b := range []bool{heading, fence, list, table, quote, link, emphasis} {
		if b {
			count++
		}
	}
	return count
}

package render

import "strings"

// plainRenderer passes content through with only trailing-whitespace trim
// per line — no styling. CRLF is already normalised upstream,
// so only trailing spaces/tabs are stripped here.
type plainRenderer struct{}

// RenderDoc trims trailing whitespace on each line and rejoins with a single
// trailing newline. No styling is applied.
func (plainRenderer) RenderDoc(src []byte, width int) (string, error) {
	lines := strings.Split(strings.TrimRight(string(src), "\n"), "\n")
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(strings.TrimRight(line, " \t"))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// RenderUnit renders one plain line: the line (trailing whitespace trimmed)
// plus a newline.
func (plainRenderer) RenderUnit(src []byte, width int) (string, error) {
	return strings.TrimRight(strings.TrimRight(string(src), "\n"), " \t") + "\n", nil
}

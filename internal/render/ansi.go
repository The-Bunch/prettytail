package render

import (
	"regexp"
	"strings"
)

// ANSI hygiene for console/tool output. The console renderer
// drives these. The contract: PRESERVE valid SGR sequences (\x1b[…m) — the
// log's own colouring is meaningful — and DROP everything else (cursor
// movement, erase, alt-screen, OSC, and partial/broken sequences). A caller
// terminates each rendered line with resetSeq so styles never bleed.

// resetSeq is the SGR reset appended to every rendered console line so a
// style opened on one line cannot bleed into the next.
const resetSeq = "\x1b[0m"

// reBareCSIResidue matches a bare SGR-shaped fragment with NO leading ESC
// byte — e.g. a literal "[0m" or "[38;5;81m" left behind by an upstream tool
// that half-stripped its own colours. Only "[", then 1–16
// parameter/separator characters ([0-9;]), then a literal "m" qualifies, so
// legitimate literals like "array[0]" or "[INFO]" (no "m" after the digits)
// are never touched.
var reBareCSIResidue = regexp.MustCompile(`\[[0-9;]{1,16}m`)

// dropBareCSIResidue removes bare CSI-shaped residue (no ESC byte) from the
// plain runs of line, at any position. It is escape-aware: real
// ANSI escape sequences (introduced by ESC) are copied through untouched so
// the "[…m" tail of a preserved SGR is never mistaken for residue.
func dropBareCSIResidue(line string) string {
	if !strings.ContainsRune(line, esc) {
		return reBareCSIResidue.ReplaceAllString(line, "")
	}
	var b strings.Builder
	b.Grow(len(line))
	for i := 0; i < len(line); {
		if line[i] != esc {
			j := i
			for j < len(line) && line[j] != esc {
				j++
			}
			b.WriteString(reBareCSIResidue.ReplaceAllString(line[i:j], ""))
			i = j
			continue
		}
		_, _, n := scanEscape(line[i:])
		if n == 0 {
			n = 1
		}
		b.WriteString(line[i : i+n])
		i += n
	}
	return b.String()
}

// esc is the escape byte that introduces every ANSI control sequence.
const esc = 0x1b

// sanitizeANSI returns line with every ANSI escape sequence stripped EXCEPT
// well-formed SGR sequences (CSI … 'm'), which are preserved verbatim.
// Cursor/erase/mode CSI sequences, OSC strings, other escape
// forms, and any partial or broken sequence (an ESC with no valid
// terminator before end-of-line) are dropped. A bare ESC is dropped.
//
// It performs no wrapping, styling or reset — that is the caller's job; this
// is purely the "keep SGR, drop the rest" filter.
func sanitizeANSI(line string) string {
	if !strings.ContainsRune(line, esc) {
		return line
	}
	var b strings.Builder
	b.Grow(len(line))
	for i := 0; i < len(line); {
		c := line[i]
		if c != esc {
			b.WriteByte(c)
			i++
			continue
		}
		// At an ESC. Classify what follows. scanEscape always consumes at
		// least one byte (the ESC itself), so the loop always progresses.
		seq, keep, n := scanEscape(line[i:])
		if keep {
			b.WriteString(seq)
		}
		i += n
	}
	return b.String()
}

// scanEscape inspects s, which begins at an ESC byte, and reports the full
// escape sequence text, whether it is a keepable SGR sequence, and the
// number of bytes it consumes. A partial/broken sequence (no valid
// terminator before end of string) is dropped and its whole remaining span
// consumed so no stray bytes leak through.
func scanEscape(s string) (seq string, keep bool, n int) {
	// s[0] == esc. Need at least one more byte to classify.
	if len(s) < 2 {
		return "", false, len(s) // lone trailing ESC: drop, consume it.
	}
	switch {
	case s[1] == '[':
		return scanCSI(s)
	case s[1] == ']':
		return scanOSC(s)
	case s[1] >= 0x20 && s[1] <= 0x2f:
		// nF / intermediate escape forms (e.g. ESC ( B charset designator):
		// zero or more intermediate bytes (0x20-0x2f) then a final byte
		// (0x30-0x7e). Consume the whole run so no designator byte leaks.
		i := 1
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) { // consume the final byte too
			i++
		}
		return "", false, i
	default:
		// Other two-byte escapes (e.g. ESC = , ESC 7): drop ESC + 1 byte.
		return "", false, 2
	}
}

// scanCSI scans a CSI sequence: ESC '[' , parameter bytes (0x30-0x3f),
// intermediate bytes (0x20-0x2f), then a final byte (0x40-0x7e). Only a
// sequence whose final byte is 'm' (SGR) is kept; all other CSI finals
// (cursor movement, erase, modes, etc.) are dropped. A sequence with no
// valid final byte before end of string is treated as broken: dropped, and
// the whole remaining span consumed.
func scanCSI(s string) (seq string, keep bool, n int) {
	// s[0]=ESC, s[1]='['. Parameters/intermediates then a final byte.
	i := 2
	for i < len(s) {
		c := s[i]
		if c >= 0x30 && c <= 0x3f { // parameter byte
			i++
			continue
		}
		if c >= 0x20 && c <= 0x2f { // intermediate byte
			i++
			continue
		}
		if c >= 0x40 && c <= 0x7e { // final byte
			i++
			isSGR := c == 'm'
			return s[:i], isSGR, i
		}
		// Any other byte breaks the sequence: drop up to and including it
		// so the stray byte does not leak.
		return "", false, i + 1
	}
	// Ran off the end with no final byte: broken/partial. Drop the whole
	// remaining span.
	return "", false, len(s)
}

// scanOSC scans an OSC string: ESC ']' … terminated by BEL (0x07) or ST
// (ESC '\'). OSC strings are always dropped. A sequence with no
// terminator before end of string is broken: dropped, whole span consumed.
func scanOSC(s string) (seq string, keep bool, n int) {
	// s[0]=ESC, s[1]=']'.
	i := 2
	for i < len(s) {
		if s[i] == 0x07 { // BEL terminator
			return "", false, i + 1
		}
		if s[i] == esc && i+1 < len(s) && s[i+1] == '\\' { // ST terminator
			return "", false, i + 2
		}
		i++
	}
	// No terminator: broken/partial. Drop the whole remaining span.
	return "", false, len(s)
}

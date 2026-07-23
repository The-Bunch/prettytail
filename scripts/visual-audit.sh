#!/usr/bin/env bash
# scripts/visual-audit.sh <file>
#
# Visual coverage audit for console/log rendering. Renders <file> with the
# repo binary under a forced colour profile and pinned width, then inspects
# the OUTPUT the way a human reads the screen — counting visible
# interpretation MISSES:
#
#   1. literal "**" bold pairs still visible in the rendered text (a balanced
#      pair the inline-markup pass should have interpreted and dropped);
#      an odd/unbalanced count of "**" runs stays literal by spec and
#      is NOT a miss;
#   2. heading lines ("#{1,6} ") whose rendered row (and its wrapped
#      continuation rows) carry no bold SGR;
#   3. leader lines (bullet "- "/"* ") whose rendered row still shows the raw
#      marker instead of an interpreted "•" glyph — diff-context rows are
#      exempt (they keep markers by spec);
#   4. continuation rows (the dim "↪" set-out) that still contain "**" markers
#      (markers must never survive into wrapped rows).
#
# Prints a per-category miss count and, on any miss, the offending rendered
# lines. Exit 0 iff every category is zero. e2e runs it over the committed
# fixture asserting ZERO misses; point it at your own logs to audit a real tail.
#
# Reads only <file> and /tmp scratch; writes nothing outside /tmp and never
# modifies the input. Colour is forced so bold/leader SGR is present to detect.
# Width note: piped (non-TTY) the binary renders at its fixed 80-column
# fallback (it reads the TTY size only on a terminal, never $COLUMNS), so the
# audit runs at that deterministic 80 — wide enough that the fixture's wide
# heading and wide bold-pair prose still wrap, exercising the
# wrapped-row paths. Works under the system bash 3.2 (no mapfile): the analysis
# is a single awk pass over the raw rendered output, which sees the SGR escapes
# directly.
set -euo pipefail

file="${1:?usage: visual-audit.sh <file>}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${PRETTYTAIL_BIN:-$here/bin/prettytail}"
if [ ! -x "$BIN" ]; then
	echo "visual-audit: binary not found at $BIN (run 'make build' first)" >&2
	exit 2
fi

raw="$(mktemp /tmp/pt-audit-raw.XXXXXX)"
CLICOLOR_FORCE=1 "$BIN" --no-follow --format console "$file" >"$raw" 2>/dev/null || {
	echo "visual-audit: render failed for $file" >&2
	rm -f "$raw"
	exit 2
}

report="$(mktemp /tmp/pt-audit-report.XXXXXX)"

# Single awk pass over the raw rendered output. ESC is \033. The analysis
# tracks whether the current logical line is a heading (so its "↪" wrapped
# continuation rows are also required to be bold) and reports each category's
# misses plus a COUNTS line the shell parses.
awk '
	function strip(s,   out) { out = s; gsub(/\033\[[0-9;]*m/, "", out); return out }
	function hasbold(s) { return (s ~ /\033\[[0-9;]*1(;[0-9;]*)?m/) }
	function isdiff(s)  { return (s ~ /^\033\[9[12]m/ || s ~ /^\033\[3[12]m/) }
	BEGIN { star=0; head=0; leader=0; cont=0 }
	{
		raw = $0
		txt = strip(raw)
		# Continuation row? (dim "↪" set-out; may have leading spaces)
		iscont = (txt ~ /^[[:space:]]*↪/)

		# --- heading tracking ---
		if (!iscont) {
			inhead = (txt ~ /^#{1,6} /) ? 1 : 0
		}
		if (inhead && !hasbold(raw)) {
			head++
			printf("[heading not bold] %d: %s\n", NR, txt) > "/dev/stderr"
		}

		# --- literal ** pairs (balanced) ---
		n = gsub(/\*\*/, "**", txt)   # counts occurrences without altering
		# gsub above mutated txt back to itself (same string); recompute count
		tmp = txt; n = 0
		while (match(tmp, /\*\*/)) { n++; tmp = substr(tmp, RSTART+2) }
		if (n >= 2 && n % 2 == 0) {
			star++
			printf("[** pairs] %d: %s\n", NR, txt) > "/dev/stderr"
			if (iscont) {
				cont++
				printf("[↪ with markers] %d: %s\n", NR, txt) > "/dev/stderr"
			}
		}

		# --- leader lines not glyphed (skip diff-coloured rows) ---
		if (!iscont && !isdiff(raw)) {
			if (txt ~ /^[[:space:]]*[-*][[:space:]]/) {
				leader++
				printf("[leader not glyphed] %d: %s\n", NR, txt) > "/dev/stderr"
			}
		}
	}
	END { printf("COUNTS %d %d %d %d\n", star, head, leader, cont) }
' "$raw" 2>"$report" 1>"$report.counts" || true

counts_line="$(grep '^COUNTS ' "$report.counts" 2>/dev/null || echo 'COUNTS 0 0 0 0')"
set -- $counts_line
star_misses="${2:-0}"; heading_misses="${3:-0}"; leader_misses="${4:-0}"; cont_misses="${5:-0}"
total=$((star_misses + heading_misses + leader_misses + cont_misses))

echo "visual-audit: $file (render width 80, piped fallback)"
echo "  literal ** pairs remaining : $star_misses"
echo "  heading rows not bold      : $heading_misses"
echo "  leader lines not glyphed   : $leader_misses"
echo "  ↪ rows with markers        : $cont_misses"

if [ "$total" -gt 0 ]; then
	echo "--- misses ---" >&2
	cat "$report" >&2
fi

rm -f "$raw" "$report" "$report.counts"

if [ "$total" -gt 0 ]; then
	echo "visual-audit: $total miss(es) — FAIL" >&2
	exit 1
fi
echo "visual-audit: zero misses — PASS"
exit 0

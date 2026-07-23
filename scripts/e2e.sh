#!/usr/bin/env bash
# scripts/e2e.sh — end-to-end harness.
#
# Scenarios: render-matrix, stream-live, tui-smoke, and install-check
# (the install check only runs when E2E_INSTALL=1).
#
# Every scenario prints "PASS <name>" or "FAIL <name> — <reason>"; the script
# exits non-zero if any scenario failed. e2e proves wiring; unit tests prove
# timing and exact bytes (golden byte-compares stay unit-level).
set -euo pipefail

cd "$(dirname "$0")/.."

make build

BIN=bin/prettytail
FIX=testdata/fixtures
FAILED=0

pass() { echo "PASS $1"; }
fail() { echo "FAIL $1 — $2"; FAILED=1; }

# has_sgr <file> — true if the file contains at least one ANSI SGR escape
# (ESC-[). Uses a NUL-safe grep so binary escape bytes are matched.
has_sgr() { LC_ALL=C grep -q "$(printf '\033')\[" "$1"; }

# line_has_sgr <file> <text-substr> <sgr-code> — true if some line of <file>
# both contains the literal <text-substr> AND carries the ANSI SGR sequence
# ESC[<sgr-code>m on that same line. Used by the console line-type colour
# asserts: the render is colour-forced so the added styling is
# present, then we prove a specific line type wears its specific colour. The
# codes are the basic-ANSI forms termenv resolves the line-type adaptive values to
# (bold 1, bold-cyan 1;96, blue 94, magenta 95, cyan 96, red 91, green 92,
# dim 90) — stable across TERM per the twice-checked binary output.
line_has_sgr() {
  LC_ALL=C grep "$2" "$1" | LC_ALL=C grep -q "$(printf '\033')\[$3m"
}

# strip_ansi — drop ANSI escape sequences from stdin so text assertions can
# match the rendered structure (which interleaves SGR spans between tokens)
# rather than raw source bytes. Distinguishes a real renderer from a plain
# passthrough: e.g. ndjson pretty-prints "key": "value" (spaced) where the
# source is compact "key":"value"; logfmt reformats level=warn → WARN.
strip_ansi() { LC_ALL=C sed "s/$(printf '\033')\[[0-9;?]*[a-zA-Z]//g"; }

# render <fixture> — run the tool render-once at a pinned width, capturing
# stdout to $OUT and the exit code to $RC (never aborts the harness).
OUT=$(mktemp /tmp/pt-e2e-render.XXXXXX)
render() {
  RC=0
  COLUMNS=80 "$BIN" -n "$FIX/$1" >"$OUT" 2>/dev/null || RC=$?
}

# ── render-matrix ────────────────────────────────────────────────────
# For each fixture × its expected format, assert exit 0 and a format-
# distinctive rendered marker. Markdown additionally asserts an ANSI SGR
# sequence. binary.bin asserts the binary-guard refusal.

# sample.md → markdown: h1 text present AND at least one SGR sequence.
render sample.md
if [ "$RC" -ne 0 ]; then
  fail render-matrix/md "expected exit 0, got $RC"
elif ! grep -q "Prettytail Fixture" "$OUT"; then
  fail render-matrix/md "h1 text 'Prettytail Fixture' not found"
elif ! has_sgr "$OUT"; then
  fail render-matrix/md "no ANSI SGR sequence in rendered markdown"
else
  pass render-matrix/md
fi

# sample.ndjson → ndjson: JSON pretty-printed. The renderer reindents the
# compact source object, so the rendered form has a space after the colon
# ("event": "startup") that the compact source ("event":"startup") lacks —
# a plain passthrough would fail this. Assert on the ANSI-stripped text.
render sample.ndjson
STRIPPED=$(strip_ansi <"$OUT")
if [ "$RC" -ne 0 ]; then
  fail render-matrix/ndjson "expected exit 0, got $RC"
elif ! printf '%s' "$STRIPPED" | grep -q '"event": "startup"'; then
  fail render-matrix/ndjson "expected pretty-printed JSON ('\"event\": \"startup\"') — not raw passthrough"
elif printf '%s' "$STRIPPED" | grep -q '"event":"startup"'; then
  fail render-matrix/ndjson "found compact source JSON — ndjson renderer did not run"
else
  pass render-matrix/ndjson
fi

# sample.logfmt → logfmt: the renderer reformats each record, uppercasing the
# level into its own column (level=warn → WARN) and dropping the ts=/level=
# key prefixes. A plain passthrough would keep 'level=warn'. Assert on the
# ANSI-stripped text.
render sample.logfmt
STRIPPED=$(strip_ansi <"$OUT")
if [ "$RC" -ne 0 ]; then
  fail render-matrix/logfmt "expected exit 0, got $RC"
elif ! printf '%s' "$STRIPPED" | grep -qE '(^|[^A-Z])WARN([^A-Z]|$)'; then
  fail render-matrix/logfmt "expected uppercased level 'WARN' — logfmt reformat did not run"
elif printf '%s' "$STRIPPED" | grep -q 'level=warn'; then
  fail render-matrix/logfmt "found source 'level=warn' — logfmt renderer did not run"
else
  pass render-matrix/logfmt
fi

# sample.plain → plain: the documented escape-hatch renderer. It is the
# near-passthrough baseline (word-wrapped prose), so the distinctive property
# is the *absence* of colourisation — unlike md/ndjson, plain emits no ANSI
# SGR — plus the prose content surviving intact.
render sample.plain
if [ "$RC" -ne 0 ]; then
  fail render-matrix/plain "expected exit 0, got $RC"
elif ! grep -q "Widget API server" "$OUT"; then
  fail render-matrix/plain "expected prose 'Widget API server' in rendered output"
elif has_sgr "$OUT"; then
  fail render-matrix/plain "plain output unexpectedly contains ANSI SGR (not the plain renderer)"
else
  pass render-matrix/plain
fi

# sample.console.log → console. Distinctive properties of the
# line-faithful console renderer: (a) the over-width line hard-wraps with the
# dim "↪ " continuation marker — no other renderer emits it; (b) ANSI hygiene
# drops the fixture's broken erase escape, so no raw "[K" artefact survives;
# (c) the log's OWN SGR colouring is preserved (the fixture's escapes are
# literal file bytes, re-emitted regardless of colour profile). Assert on
# ANSI-stripped text for (a)/(b) and on raw bytes for (c).
render sample.console.log
STRIPPED=$(strip_ansi <"$OUT")
if [ "$RC" -ne 0 ]; then
  fail render-matrix/console "expected exit 0, got $RC"
elif ! printf '%s' "$STRIPPED" | grep -q '↪ '; then
  fail render-matrix/console "expected dim continuation marker '↪ ' from the wrapped over-width line — console renderer did not wrap"
elif printf '%s' "$STRIPPED" | grep -q '\[K'; then
  fail render-matrix/console "found raw '[K' erase-escape artefact — ANSI hygiene did not drop the broken escape"
elif ! has_sgr "$OUT"; then
  fail render-matrix/console "expected the log's own preserved SGR colouring — console hygiene dropped it"
else
  pass render-matrix/console
fi

# Determinism: sample.console.log rendered twice must be byte-identical.
CDET_A=$(mktemp /tmp/pt-e2e-cdetA.XXXXXX)
CDET_B=$(mktemp /tmp/pt-e2e-cdetB.XXXXXX)
COLUMNS=80 "$BIN" -n "$FIX/sample.console.log" >"$CDET_A" 2>/dev/null || true
COLUMNS=80 "$BIN" -n "$FIX/sample.console.log" >"$CDET_B" 2>/dev/null || true
if cmp -s "$CDET_A" "$CDET_B"; then
  pass render-matrix/console-determinism
else
  fail render-matrix/console-determinism "two sample.console.log renders differ"
fi
rm -f "$CDET_A" "$CDET_B"

# Inline markup + wrap set-out. Re-render the console
# fixture and assert the embedded-markup interpretation:
# (a) bold — "**safe floor**" markers dropped, "safe floor" survives as text;
# (b) code span — backticks around "go test ./..." dropped; (c) checkbox
# glyphs ☐/☑/◐ present (the "[ ]"/"[x]"/"[•]" tokens interpreted); (d) the
# leading "- verify the safe floor…" bullet is NOT diff-red — it must NOT carry
# the del-red SGR (196/160 or the 31 basic red) the diff path would apply;
# (e) hanging-indent wrap — a continuation row is indented (marker not at col
# 0) so a wrapped bullet reads as a block. Glyph/text asserts run on stripped
# text; the colour assert inspects raw SGR on the bullet line.
render sample.console.log
CSTRIPPED=$(strip_ansi <"$OUT")
# The raw bytes of the bullet line ("verify the safe floor"): must not carry a
# red foreground. grep the raw file for a line matching the bullet text and a
# red SGR on the same line.
BULLET_RED=$(LC_ALL=C grep 'verify the' "$OUT" | LC_ALL=C grep -E "$(printf '\033')\[(1;)?31m|38;5;(196|160)m" || true)
if [ "$RC" -ne 0 ]; then
  fail render-matrix/console-inline "expected exit 0, got $RC"
elif printf '%s' "$CSTRIPPED" | grep -q '\*\*safe floor\*\*'; then
  fail render-matrix/console-inline "bold markers '**safe floor**' survived — inline bold not interpreted"
elif ! printf '%s' "$CSTRIPPED" | grep -q 'safe floor'; then
  fail render-matrix/console-inline "bold text 'safe floor' missing after marker drop"
elif printf '%s' "$CSTRIPPED" | grep -q '`go test'; then
  fail render-matrix/console-inline "code-span backticks around 'go test ./...' survived"
elif ! printf '%s' "$CSTRIPPED" | grep -q '☐'; then
  fail render-matrix/console-inline "checkbox glyph '☐' for '[ ]' missing"
elif ! printf '%s' "$CSTRIPPED" | grep -q '☑'; then
  fail render-matrix/console-inline "checkbox glyph '☑' for '[x]' missing"
elif ! printf '%s' "$CSTRIPPED" | grep -q '◐'; then
  fail render-matrix/console-inline "in-progress glyph '◐' for '[•]' missing"
elif [ -n "$BULLET_RED" ]; then
  fail render-matrix/console-inline "bullet 'verify the safe floor' rendered diff-red — bullet mis-styled as a diff deletion"
elif ! printf '%s' "$CSTRIPPED" | grep -qE '^ +↪ '; then
  fail render-matrix/console-inline "no hang-indented continuation row (↪ preceded by indent) — hanging-indent wrap not applied"
elif ! printf '%s' "$CSTRIPPED" | grep -qE '^↪ '; then
  fail render-matrix/console-inline "no col-0 continuation row (top-level bullet/plain wrap) — column-0 wrap regressed"
else
  pass render-matrix/console-inline
fi

# Line-type colour system. The pipe suppresses added
# styling by default (only the log's own literal escapes survive), so render
# once more with colour forced — mirroring the golden pinning — and assert the
# whole-line-type colours. The unit goldens prove exact
# bytes; here we prove the distinctive markers survive end-to-end through the
# real binary: (a) headings bold WITH the hashes kept —
# the "## Build report" line wears bold ESC[1m and the "##" survives in the
# stripped text; (b) turn banner "> assistant …" wears bold-cyan ESC[1;96m;
# (c) tool glyphs are tinted on their own line — "→ dispatch" blue (94),
# "← reply" magenta (95), "✱ spawning" cyan (96) — with the trailing "[pool=4]"
# metadata dimmed (90); (d) the "Index:" separator is dim (90) AND opens diff
# context, so the "- old reset path"/"+ new reset path" lines beneath it wear
# red (91)/green (92). A regression that dropped line-type styling (or mis-coloured a type)
# fails the matching assert.
COUT=$(mktemp /tmp/pt-e2e-console-lt.XXXXXX)
COLUMNS=80 CLICOLOR_FORCE=1 PRETTYTAIL_STYLE=dark "$BIN" -n "$FIX/sample.console.log" >"$COUT" 2>/dev/null || true
CLTSTRIP=$(strip_ansi <"$COUT")
if ! printf '%s' "$CLTSTRIP" | grep -q '## Build report for widget-factory'; then
  fail render-matrix/console-linetype "heading hashes not kept — '## Build report for widget-factory' missing from stripped text"
elif ! line_has_sgr "$COUT" 'Build report for widget-factory' '1'; then
  fail render-matrix/console-linetype "heading line not bold (ESC[1m) — heading styling absent"
elif ! line_has_sgr "$COUT" '> assistant' '1;96'; then
  fail render-matrix/console-linetype "turn banner '> assistant …' not bold-cyan (ESC[1;96m)"
elif ! line_has_sgr "$COUT" 'dispatch worker pool' '94'; then
  fail render-matrix/console-linetype "'→ dispatch' glyph not blue (ESC[94m)"
elif ! line_has_sgr "$COUT" 'dispatch worker pool' '90'; then
  fail render-matrix/console-linetype "trailing '[pool=4]' metadata not dimmed (ESC[90m) on the glyph line"
elif ! line_has_sgr "$COUT" 'reply received' '95'; then
  fail render-matrix/console-linetype "'← reply' glyph not magenta (ESC[95m)"
elif ! line_has_sgr "$COUT" 'spawning integration' '96'; then
  fail render-matrix/console-linetype "'✱ spawning' glyph not cyan (ESC[96m)"
elif ! line_has_sgr "$COUT" 'Index: internal/gadget/spinner.go' '90'; then
  fail render-matrix/console-linetype "'Index:' separator not dimmed (ESC[90m)"
elif ! line_has_sgr "$COUT" 'old reset path' '91'; then
  fail render-matrix/console-linetype "diff deletion under 'Index:' not red (ESC[91m) — Index: did not open diff context"
elif ! line_has_sgr "$COUT" 'new reset path' '92'; then
  fail render-matrix/console-linetype "diff addition under 'Index:' not green (ESC[92m) — Index: did not open diff context"
else
  pass render-matrix/console-linetype
fi
rm -f "$COUT"

# Visual coverage audit. Run scripts/visual-audit.sh over
# the committed console fixture — which carries the known miss-shape lines
# (leader+bold, wide bold-pair prose that wraps, diff lines quoting markdown,
# --lead prose with pairs, a wide heading that wraps) — and assert ZERO
# interpretation misses: no literal "**" pairs survive, headings (incl. wrapped
# continuation rows) are bold, leaders are glyphed, and no "↪" continuation row
# carries a marker. This is the human-eye audit wired against a
# deterministic fixture (rendered at the binary's piped 80-column fallback, at
# which the fixture's wide lines wrap).
AUDIT_RC=0
bash scripts/visual-audit.sh "$FIX/sample.console.log" >/dev/null 2>&1 || AUDIT_RC=$?
if [ "$AUDIT_RC" -ne 0 ]; then
  fail render-matrix/console-audit "visual-audit reported interpretation misses"
else
  pass render-matrix/console-audit
fi

# binary.bin → refused: exit 1 with the binary-guard message on stderr
# (verbatim: "<path> looks like a binary file (use --format to
# force)"). One invocation drives both the exit-code and message checks:
# stderr → its own temp file, stdout discarded, RC captured from this run.
ERR=$(mktemp /tmp/pt-e2e-err.XXXXXX)
RC=0
COLUMNS=80 "$BIN" -n "$FIX/binary.bin" 2>"$ERR" >/dev/null || RC=$?
if [ "$RC" -ne 1 ]; then
  fail render-matrix/binary "expected exit 1, got $RC"
elif grep -q "looks like a binary file (use --format to force)" "$ERR"; then
  pass render-matrix/binary
else
  fail render-matrix/binary "binary-guard message not found on stderr"
fi
rm -f "$ERR"

# Determinism: sample.md rendered twice must be byte-identical.
DET_A=$(mktemp /tmp/pt-e2e-detA.XXXXXX)
DET_B=$(mktemp /tmp/pt-e2e-detB.XXXXXX)
COLUMNS=80 "$BIN" -n "$FIX/sample.md" >"$DET_A" 2>/dev/null || true
COLUMNS=80 "$BIN" -n "$FIX/sample.md" >"$DET_B" 2>/dev/null || true
if cmp -s "$DET_A" "$DET_B"; then
  pass render-matrix/determinism
else
  fail render-matrix/determinism "two sample.md renders differ"
fi
rm -f "$DET_A" "$DET_B"

rm -f "$OUT"

# ── stream-live ──────────────────────────────────────────────────────
# Drive stream mode against a growing file, then truncate it, to prove the
# live append + reset path end-to-end. The file is seeded
# non-empty before launch so the only reset divider is the truncation one
# (an initially-absent file would emit a Waiting→Reset divider up front).
# Timing is deliberately generous around the 2s quiet-flush — e2e proves
# wiring, unit tests prove exact timing.
#
# Assertions (all on ANSI-stripped text, since the renderer interleaves SGR
# spans within tokens): rendered seed heading present · fence content present
# exactly once · reset divider present (matched as its full decorated literal,
# box glyphs included, so a mangled divider still fails) · post-reset content
# present · and, on the RAW bytes, no alt-screen sequence anywhere (stream
# mode is pipe-safe by construction).
SLIVE=$(mktemp /tmp/pt-e2e-stream.XXXXXX)
SOUT=$(mktemp /tmp/pt-e2e-stream-out.XXXXXX)
# Seed with a heading + paragraph before the tool starts following.
printf '# Stream Heading\n\nA paragraph of prose.\n\n' >"$SLIVE"
COLUMNS=80 "$BIN" -s "$SLIVE" >"$SOUT" 2>/dev/null &
SPID=$!
sleep 1
# Append a fenced go block across the follow window (sub-2s gaps).
printf '```go\n' >>"$SLIVE"
sleep 0.5
printf 'fmt.Println("stream-live")\n' >>"$SLIVE"
sleep 0.5
printf '```\n\n' >>"$SLIVE"
sleep 4          # comfortably past the 2s quiet-flush
: >"$SLIVE"      # truncate → Reset
sleep 1
printf '# After Stream Reset\n\n' >>"$SLIVE"
sleep 4          # let the post-reset block flush
kill "$SPID" 2>/dev/null || true
wait "$SPID" 2>/dev/null || true

SSTRIP=$(strip_ansi <"$SOUT")
if ! printf '%s' "$SSTRIP" | grep -q 'Stream Heading'; then
  fail stream-live "rendered seed heading 'Stream Heading' not found"
elif [ "$(printf '%s' "$SSTRIP" | grep -c 'fmt.Println("stream-live")')" -ne 1 ]; then
  fail stream-live "fence content not present exactly once (got $(printf '%s' "$SSTRIP" | grep -c 'fmt.Println("stream-live")'))"
elif ! printf '%s' "$SSTRIP" | grep -qF '─── file reset (truncated or replaced) ───'; then
  fail stream-live "reset divider (full decorated literal) not present"
elif ! printf '%s' "$SSTRIP" | grep -q 'After Stream Reset'; then
  fail stream-live "post-reset content 'After Stream Reset' not found"
elif LC_ALL=C grep -q "$(printf '\033')\[?1049" "$SOUT"; then
  fail stream-live "alt-screen sequence found in stream output (must be pipe-safe)"
else
  pass stream-live
fi
rm -f "$SLIVE" "$SOUT"

# ── tui-smoke ─────────────────────────────────
# Drive the real binary in its full-screen follow TUI through a pseudo-terminal
# and assert the terminal-control envelope and rendered content are all present:
# alt-screen ENTER and LEAVE
# bytes (terminal hygiene — the TUI restores the main screen on exit), the
# status-bar file name and format badge, and the rendered content.
#
# The command run inside the PTY is exactly the spec form:
#   script -q /dev/null bin/prettytail <fixture>
# `script` gives prettytail a real tty so it selects the TUI (not stream mode).
# A `q` is sent after ~2s (past bubbletea's terminal-query settle) to quit; the
# capture then contains the full session.
#
# Why a Python pty.fork wrapper rather than bare `script`: run headless (no
# controlling terminal — e.g. in CI or an agent shell), BSD
# `script`'s child pty inherits a 0×0 window size, so the viewport has no rows
# and the rendered content never paints (only the one-row status bar does).
# Allocating the outer pty with a fixed 40×100 winsize makes the smoke
# deterministic and headless-safe — the pattern Charm-style TUI e2e suites
# converge on. TERM is set to screen-256color so termenv skips its blocking OSC
# background-colour query (which has no responder on a bare pty and otherwise
# stalls startup ~5s per the termenv OSCTimeout); PRETTYTAIL_STYLE/CLICOLOR_FORCE
# pin deterministic styling. python3 ships with macOS; if it is somehow absent
# both scenarios SKIP (loudly) rather than failing — teatest proves the rendered content at unit level regardless. (Note: a SKIP
# means the alt-screen-restore byte assertion does not run on this host —
# python3 ships with macOS, so in practice it always runs there.)
#
# A second scenario over sample.console.log pins the follow view's
# initial-detection seed end-to-end: the .md scenario
# stays (badge MD) and the console scenario asserts badge LOG, zero bare CSI
# residue on the stripped capture, and one long input line rendered without a
# paragraph merge (line-faithful).
skip_tui() { echo "SKIP $1 — $2"; }

# tui_capture <fixture-path> <cap-file> — run prettytail in a sized pty over the
# fixture, quit after ~2s, and write the raw session bytes to <cap-file>.
tui_capture() {
  local fixture="$1" cap="$2"
  local drv
  drv=$(mktemp /tmp/pt-e2e-tui-drv.XXXXXX.py)
  cat >"$drv" <<PYEOF
import os, pty, sys, time, select, fcntl, termios, struct

cap_path = "${cap}"
fixture = "${fixture}"

def set_winsize(fd, rows, cols):
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

captured = bytearray()
pid, master = pty.fork()
if pid == 0:
    # Child: run the exact spec command form under \`script\` so prettytail sees a
    # tty and selects the TUI. Deterministic styling + query-free terminal.
    os.environ["PRETTYTAIL_STYLE"] = "dark"
    os.environ["CLICOLOR_FORCE"] = "1"
    os.environ["TERM"] = "screen-256color"
    os.execvp("script", ["script", "-q", "/dev/null", "bin/prettytail", fixture])
else:
    set_winsize(master, 40, 100)
    start = time.time()
    sent_quit = False
    while True:
        try:
            r, _, _ = select.select([master], [], [], 0.3)
        except (OSError, ValueError):
            break
        if r:
            try:
                data = os.read(master, 65536)
            except OSError:
                break
            if not data:
                break
            captured.extend(data)
        now = time.time()
        if not sent_quit and now - start > 2.0:
            try:
                os.write(master, b"q")
            except OSError:
                pass
            sent_quit = True
        if now - start > 8.0:  # generous hard cap; e2e proves wiring, not timing
            break
    try:
        os.close(master)
    except OSError:
        pass
    # Reap the child so no zombie/stray process survives the scenario. Give the
    # quit a short grace with a blocking wait; SIGTERM/SIGKILL if it lingers.
    reaped = False
    for _ in range(20):  # up to ~2s
        try:
            wpid, _ = os.waitpid(pid, os.WNOHANG)
        except OSError:
            reaped = True
            break
        if wpid == pid:
            reaped = True
            break
        time.sleep(0.1)
    if not reaped:
        for sig in (15, 9):
            try:
                os.kill(pid, sig)
                os.waitpid(pid, 0)
            except OSError:
                pass
    with open(cap_path, "wb") as fh:
        fh.write(captured)
PYEOF
  python3 "$drv" >/dev/null 2>&1 || true
  # Belt-and-braces: never leave a stray follow process behind.
  pkill -9 -f "bin/prettytail ${fixture}" >/dev/null 2>&1 || true
  rm -f "$drv"
}

if ! command -v python3 >/dev/null 2>&1; then
  skip_tui tui-smoke "python3 not found (needed to allocate a sized pty headlessly)"
  skip_tui tui-smoke/console "python3 not found (needed to allocate a sized pty headlessly)"
else
  # Scenario 1: markdown fixture — badge MD, rendered h1 present.
  TCAP=$(mktemp /tmp/pt-e2e-tui.XXXXXX)
  tui_capture "testdata/fixtures/sample.md" "$TCAP"
  TSTRIP=$(strip_ansi <"$TCAP" | tr -d '\r')
  if ! LC_ALL=C grep -q "$(printf '\033')\[?1049h" "$TCAP"; then
    fail tui-smoke "alt-screen ENTER (ESC[?1049h) not found — TUI did not start"
  elif ! LC_ALL=C grep -q "$(printf '\033')\[?1049l" "$TCAP"; then
    fail tui-smoke "alt-screen LEAVE (ESC[?1049l) not found — terminal not restored on exit"
  elif ! printf '%s' "$TSTRIP" | grep -q 'sample.md'; then
    fail tui-smoke "status-bar file name 'sample.md' not found in TUI capture"
  elif ! printf '%s' "$TSTRIP" | grep -qE '(^|[^A-Za-z])MD([^A-Za-z]|$)'; then
    fail tui-smoke "status-bar format badge 'MD' not found — markdown seed missing"
  elif ! printf '%s' "$TSTRIP" | grep -q 'Prettytail Fixture'; then
    fail tui-smoke "rendered h1 'Prettytail Fixture' not found in TUI capture"
  else
    pass tui-smoke
  fi
  rm -f "$TCAP"

  # Scenario 2: console fixture — the follow view's INITIAL
  # detection seed must classify a .log/ANSI file as console. Asserts badge
  # LOG (not MD — the regressed default), zero bare CSI residue on the stripped
  # capture (console hygiene), and one long input line rendered
  # without a paragraph merge: the fixture's 200-col line 14 must hard-wrap with
  # the dim continuation marker '↪ ' (console line-faithful), which markdown
  # reflow would never emit. A regression to the MD zero value would stamp MD,
  # reflow the line and drop the continuation marker — all three caught here.
  CCAP=$(mktemp /tmp/pt-e2e-tui-console.XXXXXX)
  tui_capture "testdata/fixtures/sample.console.log" "$CCAP"
  CSTRIP=$(strip_ansi <"$CCAP" | tr -d '\r')
  if ! LC_ALL=C grep -q "$(printf '\033')\[?1049h" "$CCAP"; then
    fail tui-smoke/console "alt-screen ENTER (ESC[?1049h) not found — TUI did not start"
  elif ! LC_ALL=C grep -q "$(printf '\033')\[?1049l" "$CCAP"; then
    fail tui-smoke/console "alt-screen LEAVE (ESC[?1049l) not found — terminal not restored on exit"
  elif ! printf '%s' "$CSTRIP" | grep -qE '(^|[^A-Za-z])LOG([^A-Za-z]|$)'; then
    fail tui-smoke/console "status-bar badge 'LOG' not found — follow view mis-detected console as MD"
  elif printf '%s' "$CSTRIP" | grep -qE '\[[0-9;]{1,16}m'; then
    fail tui-smoke/console "bare CSI residue (e.g. '[0m') survived in console TUI output — hygiene did not drop it"
  elif ! printf '%s' "$CSTRIP" | grep -q '↪ '; then
    fail tui-smoke/console "dim continuation marker '↪ ' not found — over-width line was reflowed (markdown), not line-faithful"
  else
    pass tui-smoke/console
  fi
  rm -f "$CCAP"
fi

# ── install-check ────────────────────────────────────────────────────
# Proves the sanctioned install path end-to-end: the
# only write outside the repo is `make install` copying to
# /opt/homebrew/bin/prettytail. Runs ONLY when E2E_INSTALL=1 — plain
# `verify-full` must never write outside the repo, so this scenario is skipped
# by default; release checks invoke it explicitly. It runs `make install`, then
# from /tmp (a directory unrelated to the repo, proving the binary is on PATH
# and self-contained) asserts `prettytail --version` prints the exact stamp
# `prettytail v<VERSION> (<commit>)`, value-pinned to the release
# VERSION so a stale install (old binary still on PATH) is caught, not just a
# well-shaped one. The installed binary is left in place on purpose: install is
# the one sanctioned persistent write outside the repo — the check proves the
# install path, it does not uninstall it.
if [ "${E2E_INSTALL:-}" = "1" ]; then
  IOUT=$(mktemp /tmp/pt-e2e-install.XXXXXX)
  IERR=$(mktemp /tmp/pt-e2e-install-err.XXXXXX)
  # The release version this build stamps in: read the Makefile's `VERSION ?=`
  # assignment line directly, so the pin tracks the same default `make install`
  # uses (no `make -p` introspection, no env-override divergence).
  WANT_VERSION=$(sed -n 's/^VERSION ?= //p' Makefile | head -1)
  IRC=0
  make install >/dev/null 2>"$IERR" || IRC=$?
  if [ "$IRC" -ne 0 ]; then
    fail install-check "make install failed (exit $IRC): $(tr '\n' ' ' <"$IERR")"
  elif ! command -v prettytail >/dev/null 2>&1; then
    fail install-check "prettytail not on PATH after make install"
  else
    IVRC=0
    # Capture stdout only; --version prints its stamp to stdout, so keeping
    # stderr off the match keeps the strict ^…$ assertion clean.
    ( cd /tmp && prettytail --version ) >"$IOUT" 2>/dev/null || IVRC=$?
    if [ "$IVRC" -ne 0 ]; then
      fail install-check "installed prettytail --version exited $IVRC"
    elif grep -qE "^prettytail v${WANT_VERSION} \([^)]+\)$" "$IOUT"; then
      pass install-check
    else
      fail install-check "installed --version not 'prettytail v${WANT_VERSION} (<commit>)' — stale or mis-stamped install"
    fi
  fi
  rm -f "$IOUT" "$IERR"
fi

if [ "$FAILED" -ne 0 ]; then
  echo "e2e: one or more scenarios FAILED"
  exit 1
fi
echo "e2e: all scenarios passed"

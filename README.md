<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/The-Bunch/.github/main/profile/assets/hellobill-logo-inverse.svg">
  <img src="https://raw.githubusercontent.com/The-Bunch/.github/main/profile/assets/hellobill-logo-primary.svg" alt="hellobill" width="320">
</picture>

### Your logs, sorted.

A pretty `tail -F` for structured text. Point prettytail at a file and watch it<br>
rendered beautifully in your terminal — live, as the file grows.

![release](https://img.shields.io/badge/release-v0.1.5-00725e)
![go](https://img.shields.io/badge/go-%E2%89%A5%201.26-6ecd96)
![platform](https://img.shields.io/badge/macOS-arm64-00a081)
![licence](https://img.shields.io/badge/licence-MIT-00725e)

[hellobill.com](https://hellobill.com) · [Demo walkthrough](DEMO.md)

</div>

## Why this exists

We spend our days watching files grow — AI agents writing markdown, build tools
streaming output, services emitting NDJSON. Raw `tail -f` on structured text is
technically readable, in the way a phone book is technically readable. So we
built prettytail while building [hellobill](https://hellobill.com), and it stuck.
It's not our product — just a tool from the journey, released in case it's
useful to you too.

Markdown is the flagship (headings, lists, fenced code, tables, all in ANSI via
[glamour](https://github.com/charmbracelet/glamour)); prettytail also detects and
formats NDJSON, logfmt, console/tool logs and plain text. In a terminal it opens
a full-screen follow view pinned to the bottom; piped or with `--stream` it emits
append-only rendered blocks that are safe for pipes.

Fully local — no network, no accounts, no secrets, and it never writes to the
file it is tailing.

## Install

With Go 1.26+:

```bash
go install github.com/The-Bunch/prettytail@latest
```

Or clone and build:

```bash
git clone https://github.com/The-Bunch/prettytail.git
cd prettytail
make install          # builds bin/prettytail, copies to $(PREFIX)/bin (default /opt/homebrew)
make build            # or just build locally → bin/prettytail (version-stamped)
```

`make install` is the only thing that writes outside the repo, and only ever to
`$(PREFIX)/bin/prettytail` (override with `make install PREFIX=/usr/local`).

Built and tested on macOS (Apple Silicon). It's plain Go, so it should build
anywhere Go 1.26+ runs — but macOS is what's tested.

## Usage

```
prettytail [flags] <file>
```

Exactly one file argument. Examples:

```bash
prettytail notes.md              # follow notes.md in a live full-screen pretty view
prettytail -s agent.log | less   # pipe-safe rendered stream
prettytail -n README.md          # render the current content once and exit
```

Try the self-feeding demo:

```bash
scripts/feed.sh /tmp/demo.md & prettytail /tmp/demo.md
```

See [DEMO.md](DEMO.md) for a full step-by-step walkthrough.

### Follow-view keys

| Key                              | Action                                                       |
| -------------------------------- | ------------------------------------------------------------ |
| scroll up (`↑`/`k`/`PgUp`/wheel) | pause following (view holds; new content no longer drags it) |
| `F`                              | re-pin to the bottom and resume live following               |
| `q` / `Ctrl-C`                   | quit (terminal is always restored on exit)                   |

The status bar shows the file name, detected format, follow state (`● live` /
`⏸ paused — F to follow`), file size and the version stamp.

## Flags

| Flag              | Meaning                                                        |
| ----------------- | -------------------------------------------------------------- |
| `-f, --format`    | `auto` \| `md` \| `ndjson` \| `logfmt` \| `console` (alias `log`) \| `plain` (default `auto`) |
| `-s, --stream`    | append-only rendered output, no full-screen UI (pipe-safe)     |
| `-n, --no-follow` | render the current content once and exit                       |
| `--version`       | print `prettytail v<version> (<commit>)` and exit              |
| `-h, --help`      | show usage and exit                                            |

Mode is chosen automatically: `--no-follow` renders once; otherwise `--stream` or a
non-terminal stdout selects stream mode; otherwise the full-screen follow view.

## Formats

Detection order: file extension → content sniff → **markdown as the final fallback**
(the sensible default for arbitrary text). Override any time with `--format`.

| Format   | Detected from                                     | Rendered as                                   |
| -------- | ------------------------------------------------- | --------------------------------------------- |
| markdown | `.md`/`.markdown`, or markdown signals in content | glamour ANSI: headings, lists, fences, tables |
| ndjson   | `.ndjson`/`.jsonl`, or lines that parse as JSON   | key/value coloured per line                   |
| logfmt   | `.logfmt`, or `key=value` log lines               | level-coloured `ts level msg …` per line      |
| console  | `.log`, ANSI escapes, diff hunks or tool glyphs   | line-faithful; keeps the log's own colour, styles diffs/timestamps/paths |
| plain    | anything else, or `--format plain`                | passed through, minimally styled              |

Heavily symbolic plain logs can occasionally over-style under the markdown fallback —
`--format plain` is the documented escape hatch.

### Console/tool logs

Agent, build and tool output (an AI coding agent's run log, `go build` output, a
`git diff`) is rendered **line-for-line** — never reflowed into paragraphs. The log's
own ANSI colouring is preserved verbatim; broken or non-colour escape sequences (cursor
moves, screen erases, OSC) are stripped so nothing leaks as literal `[0m`/`[K`
fragments. On lines that carry no colour of their own, prettytail adds a light touch:
unified-diff `+`/`-` lines go green/red, `@@` hunks cyan, `+++`/`---` headers bold, and
leading timestamps and file paths are dimmed.

Embedded markdown-ish notation is interpreted per line (again only on lines with no
colour of their own): `**bold**`/`__bold__` render bold, `` `code` `` gets a distinct
code style, `- `/`* `/`• ` bullets become a cyan `•`, `N.`/`N)` numbers are tinted, and
`[ ]`/`[x]`/`[•]` checkboxes become `☐`/`☑`/`◐`. Diff `+`/`-` colouring is confined to an
open diff hunk, so a bullet like `- do the thing` reads as a bullet, not a
deletion. Unbalanced markers stay literal; headings, italics, tables and multi-line
fences are deliberately left alone.

Whole-line types are coloured to break up a wall of console text: `#`–`######` headings
render **bold with the hashes kept** (there is no font size in a terminal, so keeping the
hashes is the honest form), `> ` turn banners go bold cyan, a `$ ` command prompt is bold
green, and leading tool-trace glyphs are tinted — `→` blue, `←` magenta, `⚙` yellow,
`✱` cyan, `✓` green, `✗` red — with any trailing `[…]` metadata dimmed. `Index:` lines and
standalone `====`/`----` rules are dimmed, and an `Index:` line also opens diff context so
the hunk beneath it is coloured.

Over-width lines hard-wrap with a hanging indent: continuation rows line up under the
line's first content column (under the bullet text, past the number, etc.) with a dim
`↪` at the indent, so wrapped bullets and prose read as blocks. Interpretation happens
**before** wrapping and is width-independent, so markers never survive into a
wrapped row. Detection prefers console for `.log` files and for any content that
carries its own escapes, so a coloured log never falls through to the markdown
renderer. Force it with `--format console` (alias `--format log`).

## Behaviour notes

- **Live updates** use fsnotify with a 500ms stat-poll fallback, so growth, truncation
  and rotation are all caught promptly.
- **Missing file:** in a follow mode prettytail prints
  `prettytail: waiting for <path> to appear…` once and waits (like `tail -F`); with
  `--no-follow` it exits 1.
- **Binary files** are refused with a hint unless you force a format:
  `prettytail: <path> looks like a binary file (use --format to force)`.
- **Unreadable files** exit 1 with `prettytail: cannot read <path>: <os error>`.
- **Stream-mode caveat:** a markdown block that goes quiet for 2s is flushed as-is; if
  it later continues it renders as two blocks.
- Files are assumed UTF-8 text. prettytail never writes to the file it is tailing.

## Exit codes

`0` clean · `1` runtime error (unreadable file, binary refused, render failure) ·
`2` usage error (wrong arguments, unknown flag or format).

## Development

```bash
make build         # → bin/prettytail
make verify        # gofmt + go vet + go test ./...
make verify-full   # verify + scripts/e2e.sh (end-to-end harness)
make clean         # remove bin/
```

`scripts/visual-audit.sh <file>` renders a file with the binary (colour forced), strips
the ANSI back off, and reports any interpretation misses — literal `**` pairs left over,
headings not bolded, leader lines not glyphed, or wrapped rows still carrying markers.
The end-to-end harness runs it over the console fixture and fails on any miss.

Deterministic golden tests pin width 80 and a dark style; adaptive behaviour stays in
the product, never in the tests. Contributions are welcome — keep `make verify-full`
green and bring a test with any new behaviour.

## Licence

[MIT](LICENSE) © The Bunch Group.

---

<div align="center">
<sub>Made by <a href="https://hellobill.com">hellobill</a> · your life admin, sorted · operated by The Bunch Group</sub>
</div>

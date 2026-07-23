# prettytail — demo walkthrough

A runnable, step-by-step tour of prettytail. Every command below works against
artefacts that already ship in this repo, and the walkthrough doubles as the
manual smoke-test for the follow experience. Run it from the repository root.

Each step states the command and **what you should see**.

---

## 1. Install the binary

```bash
make install
```

**Expect:** a `go build …` line, then the binary copied to
`$(PREFIX)/bin/prettytail` (default `/opt/homebrew/bin`) with no error.
`make install` is the only thing that writes outside the repo, and only ever
to that one sanctioned path.

## 2. Confirm the version stamp

```bash
prettytail --version
```

**Expect:** exactly one line of the shape `prettytail v<version> (<short-hash>)`
— the version and the git short-commit, stamped in at build time. This is the
visible version stamp the tool carries (it also shows in the TUI status bar).

## 3. Render a document once (pager-less glow)

```bash
prettytail -n testdata/fixtures/sample.md
```

**Expect:** the fixture printed as a formatted document and then an immediate
exit back to the prompt (no follow): a styled `Prettytail Fixture` heading,
sub-headings, a bulleted and a numbered list, a syntax-tinted ```go fenced block,
a bordered table, **bold**/*italic*/`inline code`, a blockquote and a link — all
in ANSI colour. `-n` is `--no-follow`: render the current content and stop.

## 3a. Render a console/tool log (line-faithful)

```bash
prettytail -n testdata/fixtures/sample.console.log
```

**Expect:** the tool auto-detects `console` (status badge `LOG`) and renders
the log **line-for-line** — never reflowed into paragraphs the way the markdown
renderer would. The log's own ANSI colouring is preserved; a unified-diff hunk
shows `+` lines green, `-` lines red, `@@` headers cyan and `+++`/`---` headers
bold; leading timestamps and file paths are dimmed; and the deliberately broken
erase escape in the fixture is dropped (no stray `[K` artefact). Embedded
markup is interpreted too: `**safe floor**` renders bold, `` `go test ./...` ``
gets a code style, the `- `/`* `/`•` bullets show a cyan `•` (and are **not**
reddened as diff deletions), `1.`/`2)` numbers are tinted, and the
`[ ]`/`[x]`/`[•]` checkboxes become `☐`/`☑`/`◐`. Over-width bullets and prose
hard-wrap with a hanging indent — continuation rows line up under the first
content column with a dim `↪` at the indent. The committed fixture is synthetic
— the real driver for this format is your own tool/agent run logs, which render
cleanly in follow mode instead of as mangled markdown.

## 4. Follow a growing file live

Start the demo feeder in the background, then follow the file it writes to:

```bash
scripts/feed.sh /tmp/demo.md & prettytail /tmp/demo.md
```

**Expect:** the full-screen follow view opens. The feeder appends a fresh
markdown fragment every 2 seconds (heading, paragraph, bullet list, fenced Go
code, table, blockquote — rotating), and within a blink each new fragment
appears at the bottom, formatted, with the view pinned live. The status bar at
the foot reads roughly `demo.md · MD · ● live · <size> · v<version>`. Watch at
least three fragments arrive.

## 5. Pause, resume, quit

While the follow view from step 4 is still running:

- Scroll up (mouse wheel, `↑`/`k`, or `PgUp`). **Expect:** following pauses —
  the status bar switches to `⏸ paused — F to follow` (yellow) and new content
  no longer drags the view down.
- Press `F`. **Expect:** the view snaps back to the bottom and the status bar
  returns to `● live` (green).
- Press `q`. **Expect:** the program exits cleanly, the alt-screen tears down
  and you are back at your normal prompt with the cursor visible — the terminal
  is fully restored.

## 6. Pipe-safe rendered stream

```bash
prettytail -s /tmp/demo.md | head -30
```

**Expect:** rendered markdown blocks printed to stdout as plain text (no
full-screen UI, no cursor jumps, no alt-screen escape sequences) — safe to pipe
into `head`, `less`, or a file. `-s` is `--stream`; it is also selected
automatically whenever stdout is not a terminal.

## 7. Stop the feeder

Run this in the **same shell as step 4** (where the feeder is job `%1`):

```bash
kill %1
```

**Expect:** the background `scripts/feed.sh` from step 4 stops (it traps the
signal and exits cleanly). `/tmp/demo.md` can be removed with `rm /tmp/demo.md`.

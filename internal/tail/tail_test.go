package tail

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeClock is a controllable clock for the partial-line quiet timer. Tests
// fire the timer explicitly via firePartial — no real sleeps.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(_ time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, ch)
	idx := len(c.timers) - 1
	stop := func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if idx < len(c.timers) {
			c.timers[idx] = nil
		}
		return true
	}
	return ch, stop
}

// firePartial fires the most recently armed live timer, simulating the quiet
// period elapsing.
func (c *fakeClock) firePartial() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.timers) - 1; i >= 0; i-- {
		if c.timers[i] != nil {
			select {
			case c.timers[i] <- c.now:
			default:
			}
			c.timers[i] = nil
			return
		}
	}
}

// recvEvent waits for the next event with a generous timeout. Waiting on an
// async channel is not a timing assertion — the 500ms poll fallback means an
// on-disk change surfaces within ~500ms.
func recvEvent(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("event channel closed unexpectedly")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

// recvKind waits for the next event of a specific kind, skipping others
// (e.g. an interleaved poll producing nothing). Fails on timeout.
func recvKind(t *testing.T, ch <-chan Event, want Kind) Event {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed while waiting for %v", want)
			}
			if ev.Kind == want {
				return ev
			}
			if ev.Kind == Err {
				t.Fatalf("unexpected Err while waiting for %v: %v", want, ev.Err)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for kind %v", want)
			return Event{}
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	fp, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open-append %s: %v", path, err)
	}
	defer fp.Close()
	if _, err := fp.WriteString(content); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}

func TestInitialContentEmit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	writeFile(t, path, "hello\nworld\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Follow(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	ev := recvEvent(t, ch)
	if ev.Kind != Append {
		t.Fatalf("first event kind = %v, want Append", ev.Kind)
	}
	if got, want := string(ev.Data), "hello\nworld\n"; got != want {
		t.Fatalf("initial data = %q, want %q", got, want)
	}
}

func TestInitialEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	writeFile(t, path, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Follow(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	ev := recvEvent(t, ch)
	if ev.Kind != Append {
		t.Fatalf("first event kind = %v, want Append", ev.Kind)
	}
	if len(ev.Data) != 0 {
		t.Fatalf("initial data = %q, want empty", ev.Data)
	}
}

func TestAppendWhileFollowing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	writeFile(t, path, "line1\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Follow(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if ev := recvEvent(t, ch); string(ev.Data) != "line1\n" {
		t.Fatalf("initial = %q", ev.Data)
	}

	appendFile(t, path, "line2\nline3\n")
	ev := recvKind(t, ch, Append)
	if got, want := string(ev.Data), "line2\nline3\n"; got != want {
		t.Fatalf("append data = %q, want %q", got, want)
	}
}

func TestTruncateReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	writeFile(t, path, "aaaa\nbbbb\ncccc\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Follow(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	recvEvent(t, ch) // initial

	// Truncate to shorter fresh content.
	writeFile(t, path, "fresh\n")
	ev := recvKind(t, ch, Reset)
	if got, want := string(ev.Data), "fresh\n"; got != want {
		t.Fatalf("reset data = %q, want %q", got, want)
	}
}

func TestRenameAwayRecreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	writeFile(t, path, "original\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Follow(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	recvEvent(t, ch) // initial

	// Rotate: rename away then recreate.
	if err := os.Rename(path, filepath.Join(dir, "log.txt.1")); err != nil {
		t.Fatal(err)
	}
	recvKind(t, ch, Waiting)

	writeFile(t, path, "rotated\n")
	ev := recvKind(t, ch, Reset)
	if got, want := string(ev.Data), "rotated\n"; got != want {
		t.Fatalf("reset data = %q, want %q", got, want)
	}
}

func TestPartialLineFlushedByNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	writeFile(t, path, "done\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Follow(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	recvEvent(t, ch) // initial "done\n"

	// Write a partial line (no newline) — it should be held, not emitted.
	appendFile(t, path, "partial")
	// Complete it with a newline — now it should emit.
	appendFile(t, path, " complete\n")
	ev := recvKind(t, ch, Append)
	if got, want := string(ev.Data), "partial complete\n"; got != want {
		t.Fatalf("data = %q, want %q", got, want)
	}
}

func TestPartialLineFlushedByQuiet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	writeFile(t, path, "start\n")

	clk := newFakeClock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := followWithClock(ctx, path, clk)
	if err != nil {
		t.Fatal(err)
	}
	recvEvent(t, ch) // initial "start\n"

	// Append a partial line with no newline; the engine holds it.
	appendFile(t, path, "no-newline-yet")

	// The partial timer arms after the poll reads the bytes. Give the poll a
	// window to run, then fire the quiet timer via the fake clock.
	deadline := time.After(3 * time.Second)
	for {
		clk.firePartial()
		select {
		case ev := <-ch:
			if ev.Kind == Err {
				t.Fatalf("unexpected err: %v", ev.Err)
			}
			if ev.Kind == Append && string(ev.Data) == "no-newline-yet" {
				return
			}
		case <-time.After(50 * time.Millisecond):
			// no event yet; loop and retry firing after next poll
		case <-deadline:
			t.Fatal("partial line was never quiet-flushed")
		}
	}
}

func TestCRLFNormalised(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	writeFile(t, path, "one\r\ntwo\r\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Follow(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	ev := recvEvent(t, ch)
	if got, want := string(ev.Data), "one\ntwo\n"; got != want {
		t.Fatalf("data = %q, want %q (CRLF should be LF)", got, want)
	}
}

func TestReadCapEngages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")

	// Build a file just over the 5 MiB cap out of fixed-length lines.
	fp, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	line := "the quick brown fox jumps over the lazy dog and keeps on running\n" // 65 bytes
	total := 0
	for total <= initialCap+windowCap {
		if _, err := fp.WriteString(line); err != nil {
			t.Fatal(err)
		}
		total += len(line)
	}
	fp.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Follow(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	ev := recvEvent(t, ch)
	if ev.Kind != Append {
		t.Fatalf("kind = %v, want Append", ev.Kind)
	}
	if len(ev.Data) < len(truncationNotice) || string(ev.Data[:len(truncationNotice)]) != truncationNotice {
		t.Fatalf("expected truncation notice prefix, got %q…", firstN(ev.Data, 60))
	}
	// The emitted window must be far smaller than the whole file.
	if int64(len(ev.Data)) > windowCap+int64(len(truncationNotice)) {
		t.Fatalf("emitted %d bytes, expected <= window+notice", len(ev.Data))
	}
	// It must start (after the notice) on a clean line boundary.
	rest := ev.Data[len(truncationNotice):]
	if len(rest) > 0 && string(rest[:len(line)]) != line {
		t.Fatalf("windowed data does not begin on a line boundary: %q", firstN(rest, len(line)))
	}

	// Offset must be the true EOF, not len(data): an append after the capped
	// read must surface exactly the appended bytes (proves offset alignment).
	appendFile(t, path, "APPENDED-AFTER-CAP\n")
	ev2 := recvKind(t, ch, Append)
	if got, want := string(ev2.Data), "APPENDED-AFTER-CAP\n"; got != want {
		t.Fatalf("post-cap append = %q, want %q (offset misaligned)", got, want)
	}
}

func TestInPlaceRewriteReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	writeFile(t, path, "aaaaaa\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Follow(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if ev := recvEvent(t, ch); string(ev.Data) != "aaaaaa\n" {
		t.Fatalf("initial = %q", ev.Data)
	}

	// Overwrite in place with equal-length but different content and a bumped
	// mtime. Same size + same inode → detected via mtime as a rewrite → Reset.
	future := time.Now().Add(2 * time.Second)
	writeFile(t, path, "bbbbbb\n")
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	ev := recvKind(t, ch, Reset)
	if got, want := string(ev.Data), "bbbbbb\n"; got != want {
		t.Fatalf("rewrite reset data = %q, want %q", got, want)
	}
}

func TestContextCancelClosesChannel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	writeFile(t, path, "x\n")

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := Follow(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	recvEvent(t, ch) // initial
	cancel()

	// Drain until closed; must close within the timeout (no goroutine leak).
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // closed — success
			}
		case <-deadline:
			t.Fatal("channel not closed after context cancel")
		}
	}
}

func firstN(b []byte, n int) string {
	if len(b) < n {
		return string(b)
	}
	return string(b[:n])
}

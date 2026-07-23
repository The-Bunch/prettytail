// Package tail follows a file and reports its content as it grows. It
// performs an initial full read, then watches for growth, truncation,
// rotation and disappearance (tail -F semantics), emitting Events on a
// channel until the context ends. Rendering lives elsewhere; this
// package only owns bytes up to "hand the new content to the consumer".
package tail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Kind classifies a tail Event.
type Kind int

const (
	// Append carries new bytes appended to the file. The very first event of
	// a Follow is always an Append with the whole current content (possibly
	// empty).
	Append Kind = iota
	// Reset means the file was truncated or rotated: a fresh full re-read has
	// been performed and Data carries the new full content. Consumers should
	// re-detect and re-render from scratch.
	Reset
	// Waiting is emitted once when the file disappears; a Reset follows when
	// it reappears.
	Waiting
	// Err carries a fatal watching error in Err; the channel closes after it.
	Err
)

// Event is a single tail observation.
type Event struct {
	Kind Kind   // which of the four kinds this is
	Data []byte // Append/Reset: the bytes; nil otherwise
	Err  error  // Err only
}

// Read caps. On files larger than initialCap only the final
// windowCap bytes are read, cut forward to a line boundary, with a notice
// line prepended.
const (
	initialCap = 5 << 20 // 5 MiB
	windowCap  = 1 << 20 // 1 MiB
)

// truncationNotice is prepended (verbatim) when the read cap engages.
const truncationNotice = "… earlier content truncated by prettytail …\n\n"

// Timing constants. partialQuiet flushes a buffered trailing
// line that has no newline; pollInterval is the stat-poll fallback.
const (
	partialQuiet = 750 * time.Millisecond
	pollInterval = 500 * time.Millisecond
)

// clock is the seam that lets tests drive the partial-line timer without
// real sleeps. Follow uses realClock; followWithClock takes any clock.
type clock interface {
	// Now reports the current time.
	Now() time.Time
	// NewTimer returns a timer channel that fires once after d, plus a stop
	// func that releases it.
	NewTimer(d time.Duration) (<-chan time.Time, func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

// Follow watches path and returns a channel of Events. It emits an initial
// Append with the current content (possibly empty), then events until ctx is
// cancelled, at which point the channel is closed. An error is returned only
// if the watcher cannot be created.
func Follow(ctx context.Context, path string) (<-chan Event, error) {
	return followWithClock(ctx, path, realClock{})
}

// fileState snapshots the identity of the tailed file for change detection.
type fileState struct {
	size  int64
	mtime time.Time
	ino   uint64
	valid bool // false when the file is currently absent
}

func statState(path string) (fileState, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileState{valid: false}, nil
		}
		return fileState{}, err
	}
	st := fileState{size: fi.Size(), mtime: fi.ModTime(), valid: true}
	if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
		st.ino = uint64(sys.Ino)
	}
	return st, nil
}

func (a fileState) sameFile(b fileState) bool {
	return a.valid && b.valid && a.ino == b.ino
}

// changed reports whether b differs from a across the full dedup tuple the
// spec prescribes — (size, mtime, inode). Size and inode already
// drive the growth/rotate/truncate decisions; mtime folds in here so an
// in-place rewrite that keeps the same size and inode but bumps mtime is still
// seen as activity worth re-reading rather than silently ignored.
func (a fileState) changed(b fileState) bool {
	return a.size != b.size || a.ino != b.ino || !a.mtime.Equal(b.mtime)
}

// follower holds the mutable state of one Follow loop.
type follower struct {
	path string
	clk  clock
	out  chan Event

	offset  int64     // bytes already consumed from the current file
	last    fileState // last observed identity
	partial []byte    // buffered trailing bytes with no final newline
	present bool      // whether the file currently exists (for Waiting dedupe)
}

func followWithClock(ctx context.Context, path string, clk clock) (<-chan Event, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("tail: create watcher: %w", err)
	}
	// Watch the parent dir for create/rename detection; also try the file
	// itself (best-effort — it may not exist yet).
	dir := filepath.Dir(path)
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("tail: watch dir %s: %w", dir, err)
	}
	_ = watcher.Add(path) // ignore: absent file is legal (waiting semantics)

	f := &follower{
		path: path,
		clk:  clk,
		out:  make(chan Event, 16),
	}

	go f.run(ctx, watcher)
	return f.out, nil
}

// run is the single event loop. It owns the watcher and the out channel and
// closes both on exit.
func (f *follower) run(ctx context.Context, watcher *fsnotify.Watcher) {
	defer close(f.out)
	defer watcher.Close()

	// Initial read: emit the whole current content (or empty), establishing
	// offset and identity.
	f.initialRead(ctx)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// partialTimer fires when a buffered partial line has been quiet for
	// partialQuiet. It is armed only while f.partial is non-empty.
	var partialC <-chan time.Time
	var stopPartial func() bool
	armPartial := func() {
		if stopPartial != nil {
			stopPartial()
		}
		if len(f.partial) > 0 {
			partialC, stopPartial = f.clk.NewTimer(partialQuiet)
		} else {
			partialC, stopPartial = nil, nil
		}
	}
	armPartial()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Any event on the dir or file is a hint to re-check; the poll
			// path does the real work so kqueue misses are covered anyway.
			_ = ev
			f.poll(ctx)
			armPartial()
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			f.emit(ctx, Event{Kind: Err, Err: fmt.Errorf("tail: watch %s: %w", f.path, err)})
			return
		case <-ticker.C:
			f.poll(ctx)
			armPartial()
		case <-partialC:
			// Quiet period elapsed: flush the buffered partial line as-is.
			if len(f.partial) > 0 {
				data := normaliseCRLF(f.partial)
				f.partial = nil
				f.emit(ctx, Event{Kind: Append, Data: data})
			}
			armPartial()
		}
	}
}

// initialRead performs the first full read and emits the initial Append.
func (f *follower) initialRead(ctx context.Context) {
	st, err := statState(f.path)
	if err != nil {
		f.emit(ctx, Event{Kind: Err, Err: fmt.Errorf("tail: stat %s: %w", f.path, err)})
		return
	}
	if !st.valid {
		// File not present yet: emit an empty initial Append, then Waiting so
		// the consumer knows we are holding for it.
		f.present = false
		f.emit(ctx, Event{Kind: Append, Data: []byte{}})
		f.emit(ctx, Event{Kind: Waiting})
		return
	}
	f.present = true
	f.last = st
	data, newOffset, err := readCapped(f.path)
	if err != nil {
		f.emit(ctx, Event{Kind: Err, Err: err})
		return
	}
	f.offset = newOffset
	emit, held := splitTrailingPartial(data)
	f.partial = append(f.partial, held...)
	f.emit(ctx, Event{Kind: Append, Data: normaliseCRLF(emit)})
}

// poll re-checks the file and emits any resulting events.
func (f *follower) poll(ctx context.Context) {
	st, err := statState(f.path)
	if err != nil {
		f.emit(ctx, Event{Kind: Err, Err: fmt.Errorf("tail: stat %s: %w", f.path, err)})
		return
	}

	if !st.valid {
		if f.present {
			f.present = false
			f.partial = nil
			f.emit(ctx, Event{Kind: Waiting})
		}
		return
	}

	// File exists. Detect reappearance, rotation, truncation or growth.
	reappeared := !f.present
	rotated := f.last.valid && !st.sameFile(f.last)
	truncated := st.valid && st.size < f.offset
	// Same size and inode but a fresh mtime means an in-place overwrite of
	// equal length — the bytes changed under us, so re-read from scratch
	// (folds mtime into the prescribed (size, mtime, inode) tuple).
	rewritten := f.last.valid && st.sameFile(f.last) && st.size == f.offset && !st.mtime.Equal(f.last.mtime)

	if reappeared || rotated || truncated || rewritten {
		f.reset(ctx, st)
		return
	}

	if st.size > f.offset {
		f.grow(ctx, st)
		return
	}
	// size == offset, same file, same mtime: nothing new.
	if f.last.changed(st) {
		f.last = st
	}
}

// reset re-reads the file from scratch and emits a Reset.
func (f *follower) reset(ctx context.Context, st fileState) {
	f.present = true
	f.last = st
	f.partial = nil
	data, newOffset, err := readCapped(f.path)
	if err != nil {
		f.emit(ctx, Event{Kind: Err, Err: err})
		return
	}
	f.offset = newOffset
	emit, held := splitTrailingPartial(data)
	f.partial = append(f.partial, held...)
	f.emit(ctx, Event{Kind: Reset, Data: normaliseCRLF(emit)})
}

// grow reads the appended region [offset, size) and emits an Append.
func (f *follower) grow(ctx context.Context, st fileState) {
	fp, err := os.Open(f.path)
	if err != nil {
		f.emit(ctx, Event{Kind: Err, Err: fmt.Errorf("tail: open %s: %w", f.path, err)})
		return
	}
	defer fp.Close()

	if _, err := fp.Seek(f.offset, io.SeekStart); err != nil {
		f.emit(ctx, Event{Kind: Err, Err: fmt.Errorf("tail: seek %s: %w", f.path, err)})
		return
	}
	n := st.size - f.offset
	buf := make([]byte, n)
	read, err := io.ReadFull(fp, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		f.emit(ctx, Event{Kind: Err, Err: fmt.Errorf("tail: read %s: %w", f.path, err)})
		return
	}
	buf = buf[:read]
	f.offset += int64(read)
	f.last = st

	// Prepend any previously buffered partial line, then re-split.
	combined := append(f.partial, buf...)
	f.partial = nil
	emit, held := splitTrailingPartial(combined)
	f.partial = append(f.partial, held...)
	if len(emit) > 0 {
		f.emit(ctx, Event{Kind: Append, Data: normaliseCRLF(emit)})
	}
}

// emit sends an event unless the context has ended.
func (f *follower) emit(ctx context.Context, ev Event) {
	select {
	case <-ctx.Done():
	case f.out <- ev:
	}
}

// readCapped reads the whole file, applying the 5 MiB cap: files larger than
// initialCap return only the final windowCap bytes, cut forward to the first
// line boundary, with truncationNotice prepended. The returned offset is the
// true end-of-file position so subsequent growth reads are correct.
func readCapped(path string) (data []byte, offset int64, err error) {
	fp, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("tail: open %s: %w", path, err)
	}
	defer fp.Close()

	fi, err := fp.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("tail: stat %s: %w", path, err)
	}
	size := fi.Size()

	if size <= initialCap {
		b, err := io.ReadAll(fp)
		if err != nil {
			return nil, 0, fmt.Errorf("tail: read %s: %w", path, err)
		}
		return b, int64(len(b)), nil
	}

	// Over the cap: read the final windowCap bytes.
	start := size - windowCap
	if _, err := fp.Seek(start, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("tail: seek %s: %w", path, err)
	}
	b, err := io.ReadAll(fp)
	if err != nil {
		return nil, 0, fmt.Errorf("tail: read %s: %w", path, err)
	}
	// Cut forward to the first line boundary so we start on a clean line. If
	// the whole 1 MiB window is a single unterminated line (no newline), there
	// is no clean boundary to cut to, so we emit the window uncut — a giant
	// single line is acceptable and the alternative (dropping it) would lose
	// content.
	if idx := bytes.IndexByte(b, '\n'); idx >= 0 {
		b = b[idx+1:]
	}
	out := append([]byte(truncationNotice), b...)
	return out, size, nil
}

// splitTrailingPartial separates a byte slice into the part ending on the
// last newline (emit) and any trailing bytes with no newline (held). If the
// input ends in a newline, held is empty.
func splitTrailingPartial(b []byte) (emit, held []byte) {
	idx := bytes.LastIndexByte(b, '\n')
	if idx < 0 {
		return nil, b
	}
	return b[:idx+1], b[idx+1:]
}

// normaliseCRLF rewrites CRLF to LF in place-safe fashion.
func normaliseCRLF(b []byte) []byte {
	if !bytes.Contains(b, []byte("\r\n")) {
		return b
	}
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

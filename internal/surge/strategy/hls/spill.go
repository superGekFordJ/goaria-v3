package hls

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"goaria-v3/internal/surge/types"
)

// errOffsetOverflow guards against int64 tail/offset arithmetic wrapping.
var errOffsetOverflow = errors.New("hls: file offset overflow")

// storageError wraps an arena/window I/O failure with operation context. The
// cause keeps its *os.PathError/errno identity (ErrInsufficientDiskSpace is
// attached via AnnotateInsufficientDiskSpace where the errno matches) and
// never wraps a permanent HLS sentinel. Retryable marks transient session
// I/O (EINTR/EAGAIN, Windows sharing/lock violations): a freshly prepared
// session may retry, but the current instance never retries in place — the
// window latches the first storage fault and stops accepting input.
type storageError struct {
	Op        string
	Path      string
	Err       error
	Retryable bool
}

func (e *storageError) Error() string {
	return fmt.Sprintf("hls storage %s %q: %v", e.Op, e.Path, e.Err)
}

func (e *storageError) Unwrap() error { return e.Err }

func newStorageError(op, path string, err error) *storageError {
	return &storageError{
		Op:        op,
		Path:      path,
		Err:       types.AnnotateInsufficientDiskSpace(err),
		Retryable: isTransientSessionIO(err),
	}
}

// rangeCopyFn attempts an optimized in-kernel copy of [srcOff, srcOff+length)
// from src to dst at dstOff, via explicit offsets (fd positions untouched).
// unsupported=true means the fast path cannot serve this call and the caller
// must finish the remainder portably — n>0 bytes may already have landed and
// must NOT be recopied. A non-nil err aborts the replay; partial bytes copied
// into dst stay unacknowledged (the segment is not committed).
type rangeCopyFn func(dst, src *os.File, dstOff, srcOff, length int64) (n int64, unsupported bool, err error)

// fileOps is the instance-level I/O indirection shared by the reorder window
// and its spill arena. Every nil field falls back to the real syscall; tests
// swap single funcs on the instance — never package-level globals.
type fileOps struct {
	open      func(name string, flag int, perm os.FileMode) (*os.File, error)
	seek      func(f *os.File, offset int64, whence int) (int64, error)
	write     func(f *os.File, b []byte) (int, error)
	readAt    func(f *os.File, b []byte, off int64) (int, error)
	truncate  func(f *os.File, size int64) error
	syncFn    func(f *os.File) error
	close     func(f *os.File) error
	rangeCopy rangeCopyFn
}

func defaultFileOps() *fileOps {
	return &fileOps{
		open:      os.OpenFile,
		seek:      (*os.File).Seek,
		write:     (*os.File).Write,
		readAt:    (*os.File).ReadAt,
		truncate:  (*os.File).Truncate,
		syncFn:    (*os.File).Sync,
		close:     (*os.File).Close,
		rangeCopy: platformRangeCopy,
	}
}

func (o *fileOps) fill() {
	if o.open == nil {
		o.open = os.OpenFile
	}
	if o.seek == nil {
		o.seek = (*os.File).Seek
	}
	if o.write == nil {
		o.write = (*os.File).Write
	}
	if o.readAt == nil {
		o.readAt = (*os.File).ReadAt
	}
	if o.truncate == nil {
		o.truncate = (*os.File).Truncate
	}
	if o.syncFn == nil {
		o.syncFn = (*os.File).Sync
	}
	if o.close == nil {
		o.close = (*os.File).Close
	}
	if o.rangeCopy == nil {
		o.rangeCopy = platformRangeCopy
	}
}

// writeFull loops until b is fully written; a zero-progress or out-of-bounds
// short write fails as a storage error instead of being silently accepted.
func writeFull(ops *fileOps, f *os.File, op, path string, b []byte) error {
	for off := 0; off < len(b); {
		n, err := ops.write(f, b[off:])
		if n > 0 {
			if n > len(b)-off {
				return newStorageError(op, path, io.ErrShortWrite)
			}
			off += n
		}
		if err != nil {
			return newStorageError(op, path, err)
		}
		if n <= 0 {
			return newStorageError(op, path, io.ErrShortWrite)
		}
	}
	return nil
}

// opsReaderAt routes arena source reads through the injected ops.
type opsReaderAt struct {
	f   *os.File
	ops *fileOps
}

func (r opsReaderAt) ReadAt(b []byte, off int64) (int, error) {
	n, err := r.ops.readAt(r.f, b, off)
	if n == 0 && err == nil && len(b) > 0 {
		return 0, io.ErrNoProgress
	}
	return n, err
}

// opsWriter routes destination writes through the injected ops.
type opsWriter struct {
	f   *os.File
	ops *fileOps
}

func (w opsWriter) Write(b []byte) (int, error) { return w.ops.write(w.f, b) }

// writerOnly hides ReadFrom so io.CopyBuffer takes the bounded scratch path
// instead of an os.File splice shortcut.
type writerOnly struct {
	w io.Writer
}

func (wo writerOnly) Write(b []byte) (int, error) { return wo.w.Write(b) }

// spillScratchSize bounds the portable replay buffer (lazy, reused, freed on
// Close).
const spillScratchSize = 1 << 20

// spillArena is the single hidden overflow file ".<id>.spill" sitting next to
// the destination. It is lazily created on first real overflow, appended at a
// confirmed tail offset, replayed back in index order, and truncated to zero
// as soon as the last entry drains. Every method runs under the owning
// ReorderWindow mutex — the arena never self-locks.
type spillArena struct {
	path          string
	file          *os.File // nil until the first overflow creates it
	tail          int64    // confirmed append offset == acknowledged file size
	entries       map[int]types.HLSSpillEntry
	scratch       []byte
	rangeCopyDown bool // fast copy reported unsupported; go straight portable
	ops           *fileOps
}

func newSpillArena(path string, ops *fileOps) *spillArena {
	return &spillArena{path: path, entries: make(map[int]types.HLSSpillEntry), ops: ops}
}

// validateSpillID rejects download IDs that could place the arena outside the
// destination directory or form an illegal filename.
func validateSpillID(id string) error {
	if id == "" {
		return errors.New("empty download id")
	}
	if len(id) > 200 {
		return fmt.Errorf("download id %d bytes too long", len(id))
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("download id %q contains traversal", id)
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:?"<>|*`, r) {
			return fmt.Errorf("download id %q has illegal filename character %q", id, r)
		}
	}
	if id[len(id)-1] == '.' || id[len(id)-1] == ' ' || id[0] == ' ' {
		return fmt.Errorf("download id %q has trailing/leading dot-space form", id)
	}
	return nil
}

func spillPathFor(destPath, id string) string {
	return filepath.Join(filepath.Dir(destPath), "."+id+".spill")
}

func (a *spillArena) has(index int) bool {
	_, ok := a.entries[index]
	return ok
}

// append lazily creates the arena (O_EXCL — an existing file is never taken
// over or truncated) and writes the full payload at the confirmed tail. Only
// a complete write registers the entry and advances tail; a partial write
// leaves a dirty region past tail that later appends overwrite, and the
// caller never commits it.
func (a *spillArena) append(index int, payload []byte) (types.HLSSpillEntry, error) {
	entry := types.HLSSpillEntry{SegmentIndex: index}
	if a.file == nil {
		f, err := a.ops.open(a.path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return entry, newStorageError("spill create", a.path, err)
		}
		a.file = f
	}
	if _, err := a.ops.seek(a.file, a.tail, io.SeekStart); err != nil {
		return entry, newStorageError("spill seek", a.path, err)
	}
	if err := writeFull(a.ops, a.file, "spill append", a.path, payload); err != nil {
		return entry, err
	}
	if int64(len(payload)) > math.MaxInt64-a.tail {
		return entry, newStorageError("spill append", a.path, errOffsetOverflow)
	}
	entry.Offset, entry.Length = a.tail, int64(len(payload))
	a.entries[index] = entry
	a.tail += entry.Length
	return entry, nil
}

// replayEntry copies one entry's bytes to dst at dstOff: first through the
// platform fast path (unless it already reported unsupported), then the
// bounded portable copy for whatever remains. Only a complete copy commits;
// partial failures leave dst's tail unacknowledged.
func (a *spillArena) replayEntry(dst *os.File, dstOff int64, e types.HLSSpillEntry) error {
	done := int64(0)
	if !a.rangeCopyDown {
		n, unsupported, err := a.ops.rangeCopy(dst, a.file, dstOff, e.Offset, e.Length)
		if n < 0 || n > e.Length {
			return newStorageError("spill replay", a.path, fmt.Errorf("range copy returned %d of %d", n, e.Length))
		}
		done = n
		switch {
		case err != nil:
			return newStorageError("spill replay", a.path, err)
		case unsupported:
			a.rangeCopyDown = true
		}
	}
	if done < e.Length {
		if err := a.replayPortable(dst, dstOff+done, e.Offset+done, e.Length-done); err != nil {
			return err
		}
	}
	return nil
}

// replayPortable copies [srcOff, srcOff+length) of the arena to dst at dstOff
// through a fixed reused scratch buffer. A short source — even with a nil
// error — fails as an unexpected EOF instead of committing partial bytes.
func (a *spillArena) replayPortable(dst *os.File, dstOff, srcOff, length int64) error {
	if _, err := a.ops.seek(dst, dstOff, io.SeekStart); err != nil {
		return newStorageError("replay seek", dst.Name(), err)
	}
	if a.scratch == nil {
		a.scratch = make([]byte, spillScratchSize)
	}
	src := io.NewSectionReader(opsReaderAt{f: a.file, ops: a.ops}, srcOff, length)
	n, err := io.CopyBuffer(writerOnly{w: opsWriter{f: dst, ops: a.ops}}, src, a.scratch)
	if err != nil {
		return newStorageError("spill replay", a.path, err)
	}
	if n != length {
		return newStorageError("spill replay", a.path, io.ErrUnexpectedEOF)
	}
	return nil
}

// truncate empties the drained arena. Only a successful truncate resets the
// tail to 0 (the next append reuses offset 0); a failed truncate keeps the
// real high-water mark so snapshots never fake a reclaimed file.
func (a *spillArena) truncate() error {
	if a.file == nil {
		return nil
	}
	if err := a.ops.truncate(a.file, 0); err != nil {
		return newStorageError("spill truncate", a.path, err)
	}
	a.tail = 0
	return nil
}

func (a *spillArena) sync() error {
	if a.file == nil {
		return nil
	}
	if err := a.ops.syncFn(a.file); err != nil {
		return newStorageError("spill sync", a.path, err)
	}
	return nil
}

// close releases the arena handle and scratch; it never deletes the file.
func (a *spillArena) close() error {
	if a.file == nil {
		return nil
	}
	f := a.file
	a.file = nil
	a.scratch = nil
	if err := a.ops.close(f); err != nil {
		return newStorageError("spill close", a.path, err)
	}
	return nil
}

// sortedEntries returns the live spill index ordered by SegmentIndex; the
// arena is the single source of truth so snapshots rebuild on demand rather
// than maintaining a redundant slice on every Ingest.
func (a *spillArena) sortedEntries() []types.HLSSpillEntry {
	if len(a.entries) == 0 {
		return nil
	}
	out := make([]types.HLSSpillEntry, 0, len(a.entries))
	for _, e := range a.entries {
		out = append(out, e)
	}
	slices.SortFunc(out, func(x, y types.HLSSpillEntry) int {
		return x.SegmentIndex - y.SegmentIndex
	})
	return out
}

package hls

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"sync"

	"goaria-v3/internal/surge/types"
)

// In-memory out-of-order retention budget: anything beyond either limit goes
// to the single hidden spill arena instead of unbounded buffering.
const (
	reorderPendingSegmentMax = 16
	reorderPendingBytesMax   = 32 << 20 // 32 MiB
)

var (
	// errWindowClosed rejects calls after Close.
	errWindowClosed = errors.New("hls: reorder window closed")
	// errDirtySeed marks a resume snapshot that is not a prepared contiguous
	// prefix — the constructor refuses it rather than resetting silently.
	errDirtySeed = errors.New("hls: resume state is not a prepared prefix")
)

// pendingSegment is an owned copy of an out-of-order payload.
type pendingSegment struct {
	index   int
	payload []byte // make+copy at ingest; exactly len bytes, never the caller's buffer
}

// IngestResult reports where this call's payload landed. Accepted means the
// payload was newly admitted (prefix/pending/spill); a later drain step may
// still fail, so Accepted=true can pair with a non-nil error — independent
// segment successes are never rolled back.
type IngestResult struct {
	Accepted  bool
	Duplicate bool
	Direct    bool // appended to the merged prefix immediately
	Buffered  bool // retained as an owned pending copy
	Spilled   bool // appended to the spill arena
}

// ReorderWindow serializes segment arrival into one ordered .surge stream.
// A single mutex guards Ingest, drains, spill append/index/truncate, Sync,
// snapshots and Close — all I/O happens inside the lock; there are no
// background goroutines. out is borrowed (read-write, seekable, NOT
// O_APPEND) and is never Seeked/Written/Closed by the owner while the window
// lives; every append seeks to the logical merged offset first.
type ReorderWindow struct {
	mu sync.Mutex

	out     *os.File
	outPath string
	ops     *fileOps
	arena   *spillArena

	seed       *types.HLSResumeState // cloned manifest/init/segment metadata for projections
	total      int
	mergedSegs int   // == nextAppendIndex
	merged     int64 // bytes committed to out, InitBytes included
	verified   int64 // merged + live spill lengths

	pending      map[int]pendingSegment
	pendingBytes int64

	bitmap []byte // packed bits: merged-prefix ∪ spilled (never pending)

	closed   bool
	storeErr error // first storage fault, latched — the window never retries in place
}

// NewReorderWindow prepares a window over the prepared .surge file out at
// destPath. seed must be a clean contiguous prefix; see the validation rules
// on the internal constructor. The download ID names the hidden spill arena
// file in destPath's directory only — never trust a stored spill path.
func NewReorderWindow(out *os.File, destPath, downloadID string, seed *types.HLSResumeState) (*ReorderWindow, error) {
	return newReorderWindow(out, destPath, downloadID, seed, nil)
}

func newReorderWindow(out *os.File, destPath, downloadID string, seed *types.HLSResumeState, ops *fileOps) (*ReorderWindow, error) {
	if out == nil {
		return nil, errors.New("hls: nil output file")
	}
	if destPath == "" {
		return nil, errors.New("hls: empty destination path")
	}
	if err := validateSpillID(downloadID); err != nil {
		return nil, err
	}
	if err := validateSeed(seed); err != nil {
		return nil, err
	}
	fi, err := out.Stat()
	if err != nil {
		return nil, newStorageError("output stat", destPath, err)
	}
	if fi.Size() != seed.MergedBytes {
		return nil, fmt.Errorf("%w: output size %d, merged prefix is %d", errDirtySeed, fi.Size(), seed.MergedBytes)
	}
	if ops == nil {
		ops = defaultFileOps()
	}
	ops.fill()
	w := &ReorderWindow{
		out:        out,
		outPath:    destPath,
		ops:        ops,
		arena:      newSpillArena(spillPathFor(destPath, downloadID), ops),
		seed:       seed.Clone(),
		total:      seed.TotalSegments,
		mergedSegs: seed.MergedSegments,
		merged:     seed.MergedBytes,
		verified:   seed.VerifiedBytes,
		pending:    make(map[int]pendingSegment),
		bitmap:     slices.Clone(seed.CompletedBitmap),
	}
	return w, nil
}

// validateSeed accepts only a prepared contiguous prefix: complete
// Index-ordered segment table, exact bitmap length with clean tail bits,
// set bits covering precisely [0, MergedSegments), VerifiedBytes==MergedBytes,
// no spill bookkeeping (a fresh window never adopts old spill state), and a
// merged byte range consistent with the init prefix.
func validateSeed(seed *types.HLSResumeState) error {
	if seed == nil {
		return fmt.Errorf("%w: nil state", errDirtySeed)
	}
	if seed.TotalSegments <= 0 {
		return fmt.Errorf("%w: total segments %d", errDirtySeed, seed.TotalSegments)
	}
	if len(seed.Segments) != seed.TotalSegments {
		return fmt.Errorf("%w: %d segment rows for %d total", errDirtySeed, len(seed.Segments), seed.TotalSegments)
	}
	for i, seg := range seed.Segments {
		if seg.Index != i {
			return fmt.Errorf("%w: segments[%d].Index=%d", errDirtySeed, i, seg.Index)
		}
	}
	if seed.MergedSegments < 0 || seed.MergedSegments > seed.TotalSegments {
		return fmt.Errorf("%w: merged segments %d", errDirtySeed, seed.MergedSegments)
	}
	bitmapLen := (seed.TotalSegments + 7) / 8
	if len(seed.CompletedBitmap) != bitmapLen {
		return fmt.Errorf("%w: bitmap %d bytes for %d segments", errDirtySeed, len(seed.CompletedBitmap), seed.TotalSegments)
	}
	for i := 0; i < seed.TotalSegments; i++ {
		bit := seed.CompletedBitmap[i>>3]&(1<<uint(i&7)) != 0
		if bit != (i < seed.MergedSegments) {
			return fmt.Errorf("%w: bitmap bit %d outside merged prefix %d", errDirtySeed, i, seed.MergedSegments)
		}
	}
	if tail := seed.CompletedBitmap[bitmapLen-1] >> uint(seed.TotalSegments&7); seed.TotalSegments&7 != 0 && tail != 0 {
		return fmt.Errorf("%w: bitmap tail bits set", errDirtySeed)
	}
	if seed.InitSegmentURI == "" {
		if seed.InitCompleted || seed.InitBytes != 0 {
			return fmt.Errorf("%w: init counters without init segment", errDirtySeed)
		}
	} else {
		if !seed.InitCompleted || seed.InitBytes <= 0 {
			return fmt.Errorf("%w: init declared but not written", errDirtySeed)
		}
	}
	if seed.InitBytes < 0 || seed.MergedBytes < 0 {
		return fmt.Errorf("%w: negative byte counter", errDirtySeed)
	}
	if seed.MergedBytes < seed.InitBytes {
		return fmt.Errorf("%w: merged %d below init %d", errDirtySeed, seed.MergedBytes, seed.InitBytes)
	}
	if seed.MergedSegments == 0 {
		if seed.MergedBytes != seed.InitBytes {
			return fmt.Errorf("%w: merged %d != init %d at zero merged segments", errDirtySeed, seed.MergedBytes, seed.InitBytes)
		}
	} else {
		if seed.MergedBytes <= seed.InitBytes {
			return fmt.Errorf("%w: merged %d with %d segments past init %d", errDirtySeed, seed.MergedBytes, seed.MergedSegments, seed.InitBytes)
		}
		// Plaintext never exceeds source bytes; bound the merged range when
		// every merged segment declares a byte-range length.
		var upper int64
		known := true
		for i := 0; i < seed.MergedSegments; i++ {
			l := seed.Segments[i].ByteLength
			if l <= 0 {
				known = false
				break
			}
			if l > math.MaxInt64-upper {
				return fmt.Errorf("%w: segment length sum overflow", errDirtySeed)
			}
			upper += l
		}
		if known && seed.MergedBytes > seed.InitBytes+upper {
			return fmt.Errorf("%w: merged %d exceeds source range %d", errDirtySeed, seed.MergedBytes, seed.InitBytes+upper)
		}
	}
	if seed.VerifiedBytes != seed.MergedBytes {
		return fmt.Errorf("%w: verified %d != merged %d", errDirtySeed, seed.VerifiedBytes, seed.MergedBytes)
	}
	if seed.SpillPath != "" || seed.SpillFileSize != 0 || len(seed.SpillEntries) != 0 {
		return fmt.Errorf("%w: residual spill bookkeeping", errDirtySeed)
	}
	return nil
}

func (w *ReorderWindow) hasBit(i int) bool { return w.bitmap[i>>3]&(1<<uint(i&7)) != 0 }
func (w *ReorderWindow) setBit(i int)      { w.bitmap[i>>3] |= 1 << uint(i&7) }

// writeOut appends payload at the logical merged offset via explicit Seek —
// never WriteAt, never fd-position assumptions.
func (w *ReorderWindow) writeOut(payload []byte) error {
	if _, err := w.ops.seek(w.out, w.merged, io.SeekStart); err != nil {
		return newStorageError("output seek", w.outPath, err)
	}
	return writeFull(w.ops, w.out, "output write", w.outPath, payload)
}

// Ingest borrows payload for the duration of the call: the window never keeps
// the caller's buffer, so it is safe to reuse or mutate after return. Dedup
// order is prefix/spill bitmap then the pending map; duplicates are
// (Accepted=false, nil). A latched storage fault rejects everything.
func (w *ReorderWindow) Ingest(index int, payload []byte) (IngestResult, error) {
	var res IngestResult
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return res, errWindowClosed
	}
	if w.storeErr != nil {
		return res, w.storeErr
	}
	if index < 0 || index >= w.total {
		return res, fmt.Errorf("hls: segment index %d out of range [0,%d)", index, w.total)
	}
	if len(payload) == 0 {
		return res, fmt.Errorf("hls: empty payload for segment %d", index)
	}
	if w.hasBit(index) {
		res.Duplicate = true
		return res, nil
	}
	if _, ok := w.pending[index]; ok {
		res.Duplicate = true
		return res, nil
	}
	l := int64(len(payload))
	if index == w.mergedSegs {
		if l > math.MaxInt64-w.merged || l > math.MaxInt64-w.verified {
			return res, fmt.Errorf("hls: %w growing counters by %d", errOffsetOverflow, l)
		}
		if err := w.writeOut(payload); err != nil {
			w.storeErr = err
			return res, err
		}
		w.setBit(index)
		w.mergedSegs++
		w.merged += l
		w.verified += l
		res.Accepted, res.Direct = true, true
		if err := w.drainLocked(); err != nil {
			w.storeErr = err
			return res, err
		}
		return res, nil
	}
	// Out-of-order: prefer an owned pending copy while under both budgets.
	if l <= reorderPendingBytesMax-w.pendingBytes && len(w.pending) < reorderPendingSegmentMax {
		cp := make([]byte, len(payload))
		copy(cp, payload)
		w.pending[index] = pendingSegment{index: index, payload: cp}
		w.pendingBytes += l
		res.Accepted, res.Buffered = true, true
		return res, nil
	}
	// Budget exceeded — spill the full payload to the arena.
	if l > math.MaxInt64-w.verified {
		return res, fmt.Errorf("hls: %w growing verified by %d", errOffsetOverflow, l)
	}
	entry, err := w.arena.append(index, payload)
	if err != nil {
		w.storeErr = err
		return res, err
	}
	w.setBit(index)
	w.verified += entry.Length
	res.Accepted, res.Spilled = true, true
	return res, nil
}

// drainLocked merges whatever became contiguous at the frontier: pending
// payloads are written and only then counted (bit/merged/verified), spilled
// payloads are replayed and only advance merged — never double-counted —
// and the arena truncates to zero under the same lock once it is empty.
func (w *ReorderWindow) drainLocked() error {
	for {
		if p, ok := w.pending[w.mergedSegs]; ok {
			l := int64(len(p.payload))
			if l > math.MaxInt64-w.merged || l > math.MaxInt64-w.verified {
				return fmt.Errorf("hls: %w growing counters by %d", errOffsetOverflow, l)
			}
			if err := w.writeOut(p.payload); err != nil {
				return err
			}
			w.setBit(p.index)
			w.mergedSegs++
			w.merged += l
			w.verified += l
			w.pendingBytes -= l
			delete(w.pending, p.index)
			continue
		}
		if e, ok := w.arena.entries[w.mergedSegs]; ok {
			if e.Length > math.MaxInt64-w.merged {
				return fmt.Errorf("hls: %w growing merged by %d", errOffsetOverflow, e.Length)
			}
			if err := w.arena.replayEntry(w.out, w.merged, e); err != nil {
				return err
			}
			w.merged += e.Length
			w.mergedSegs++
			delete(w.arena.entries, e.SegmentIndex)
			if len(w.arena.entries) == 0 {
				if err := w.arena.truncate(); err != nil {
					return err
				}
			}
			continue
		}
		return nil
	}
}

// Snapshot returns the live on-disk truth under the lock (merged prefix plus
// durable spill, never pending) with physically isolated slices. It is a
// read-out, not a crash-safe checkpoint.
func (w *ReorderWindow) Snapshot() *types.HLSResumeState {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.seed.Clone()
	s.MergedSegments = w.mergedSegs
	s.MergedBytes = w.merged
	s.VerifiedBytes = w.verified
	s.CompletedBitmap = slices.Clone(w.bitmap)
	s.SpillEntries = w.arena.sortedEntries()
	if w.arena.tail > 0 {
		s.SpillPath = w.arena.path
		s.SpillFileSize = w.arena.tail
	}
	return s
}

// SnapshotMergedPrefix returns the conservative projection for a persist
// boundary: prefix bits only, VerifiedBytes=MergedBytes, all spill fields
// cleared, manifest/init metadata kept. Live state is untouched.
func (w *ReorderWindow) SnapshotMergedPrefix() *types.HLSResumeState {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.seed.Clone()
	s.MergedSegments = w.mergedSegs
	s.MergedBytes = w.merged
	s.VerifiedBytes = w.merged
	s.CompletedBitmap = slices.Clone(w.bitmap)
	for i := w.mergedSegs; i < w.total; i++ {
		s.CompletedBitmap[i>>3] &^= 1 << uint(i&7)
	}
	return s
}

// Sync fsyncs out and (when open) the spill file under the same lock,
// reporting any sync failure — never flushing pending, never pretending a
// store-level commit happened.
func (w *ReorderWindow) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errWindowClosed
	}
	var first error
	if err := w.ops.syncFn(w.out); err != nil {
		first = newStorageError("output sync", w.outPath, err)
	}
	if err := w.arena.sync(); err != nil && first == nil {
		first = err
	}
	if first != nil {
		w.storeErr = first
	}
	return first
}

// Close is idempotent: it drops pending buffers and scratch and releases the
// arena handle, but never closes out, never deletes the spill file, and never
// flushes pending data.
func (w *ReorderWindow) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	w.pending = nil
	w.pendingBytes = 0
	return w.arena.close()
}

// SpillPath exposes the deterministic arena path for tests and diagnostics;
// the file may not exist (lazy creation).
func (w *ReorderWindow) SpillPath() string { return w.arena.path }

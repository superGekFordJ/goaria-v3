package hls

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/types"
)

// newArenaFixture opens an out file and a fresh arena in a temp dir; the
// fixture's ops are shared so single funcs can be fault-injected.
func newArenaFixture(t *testing.T, id string) (*spillArena, *os.File, string, *fileOps) {
	t.Helper()
	dir := t.TempDir()
	outPath := filepath.Join(dir, "video.surge")
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ops := defaultFileOps()
	a := newSpillArena(spillPathFor(outPath, id), ops)
	t.Cleanup(func() {
		a.close()
		out.Close()
	})
	return a, out, dir, ops
}

func TestSpillArena_LazyCreateAndAppendOffsets(t *testing.T) {
	a, out, dir, _ := newArenaFixture(t, "id-9")
	sp := filepath.Join(dir, ".id-9.spill")
	if _, err := os.Lstat(sp); !os.IsNotExist(err) {
		t.Fatalf("arena eager: %v", err)
	}
	e1, err := a.append(5, []byte("hello"))
	if err != nil || e1.Offset != 0 || e1.Length != 5 {
		t.Fatalf("append1: %+v %v", e1, err)
	}
	e2, err := a.append(3, []byte("world!!"))
	if err != nil || e2.Offset != 5 || e2.Length != 7 {
		t.Fatalf("append2: %+v %v", e2, err)
	}
	if fi, _ := os.Lstat(sp); fi.Size() != 12 {
		t.Fatalf("spill size = %d", fi.Size())
	}
	if a.tail != 12 {
		t.Fatalf("tail = %d", a.tail)
	}
	// Index-ordered snapshot regardless of append order.
	got := a.sortedEntries()
	if len(got) != 2 || got[0].SegmentIndex != 3 || got[1].SegmentIndex != 5 {
		t.Fatalf("sorted = %+v", got)
	}
	// Replay both into out in index order.
	if err := a.replayEntry(out, 0, got[0]); err != nil {
		t.Fatal(err)
	}
	if err := a.replayEntry(out, 7, got[1]); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out.Name())
	if !bytes.Equal(b, []byte("world!!hello")) {
		t.Fatalf("out = %q", b)
	}
}

func TestSpillArena_AppendWriteFailureKeepsTail(t *testing.T) {
	a, _, _, ops := newArenaFixture(t, "id-9")
	if _, err := a.append(1, []byte("good")); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("disk boom")
	ops.write = func(f *os.File, b []byte) (int, error) { return 2, boom }
	e, err := a.append(2, []byte("partial-fail"))
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("append err = %v", err)
	}
	if a.has(2) {
		t.Fatal("partial write committed entry")
	}
	if a.tail != 4 {
		t.Fatalf("tail moved to %d", a.tail)
	}
	// The next append rewrites over the uncommitted dirty region.
	ops.write = (*os.File).Write
	e, err = a.append(2, []byte("clean"))
	if err != nil || e.Offset != 4 || e.Length != 5 {
		t.Fatalf("append retry: %+v %v", e, err)
	}
	if fi, _ := os.Lstat(a.path); fi.Size() != 9 {
		t.Fatalf("size = %d", fi.Size())
	}
	b, _ := os.ReadFile(a.path)
	if !bytes.Equal(b, []byte("goodclean")) {
		t.Fatalf("arena = %q", b)
	}
}

func TestSpillArena_ReplayShortSource(t *testing.T) {
	a, out, _, ops := newArenaFixture(t, "id-9")
	a.ops.rangeCopy = func(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
		return 0, true, nil
	}
	if _, err := a.append(1, []byte("abcd")); err != nil {
		t.Fatal(err)
	}
	// Source ends early without an error -> unexpected EOF, not silent commit.
	ops.readAt = func(f *os.File, b []byte, off int64) (int, error) {
		if off >= 2 {
			return 0, io.EOF
		}
		return f.ReadAt(b[:2], off)
	}
	err := a.replayEntry(out, 0, types.HLSSpillEntry{SegmentIndex: 1, Offset: 0, Length: 4})
	if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("replay err = %v", err)
	}
	// Zero-progress source must not spin the copy loop.
	ops.readAt = func(f *os.File, b []byte, off int64) (int, error) { return 0, nil }
	err = a.replayEntry(out, 0, types.HLSSpillEntry{SegmentIndex: 1, Offset: 0, Length: 4})
	if err == nil {
		t.Fatal("zero-progress replay accepted")
	}
}

func TestSpillArena_PortableOnlyWhenRangeDown(t *testing.T) {
	a, out, _, ops := newArenaFixture(t, "id-9")
	var rangeCalls int
	ops.rangeCopy = func(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
		rangeCalls++
		return 0, true, nil
	}
	if _, err := a.append(1, []byte("xy")); err != nil {
		t.Fatal(err)
	}
	if err := a.replayEntry(out, 0, types.HLSSpillEntry{SegmentIndex: 1, Offset: 0, Length: 2}); err != nil {
		t.Fatal(err)
	}
	if rangeCalls != 1 || !a.rangeCopyDown {
		t.Fatalf("rangeCopy calls=%d down=%v", rangeCalls, a.rangeCopyDown)
	}
	if _, err := a.append(2, []byte("zw")); err != nil {
		t.Fatal(err)
	}
	// Once unsupported, later replays go straight to the portable path.
	if err := a.replayEntry(out, 2, types.HLSSpillEntry{SegmentIndex: 2, Offset: 2, Length: 2}); err != nil {
		t.Fatal(err)
	}
	if rangeCalls != 1 {
		t.Fatalf("rangeCopy retried after downgrade: %d", rangeCalls)
	}
	b, _ := os.ReadFile(out.Name())
	if !bytes.Equal(b, []byte("xyzw")) {
		t.Fatalf("out = %q", b)
	}
}

func TestSpillArena_TruncateAndReuse(t *testing.T) {
	a, _, _, _ := newArenaFixture(t, "id-9")
	a.append(1, []byte("aaaa"))
	a.append(2, []byte("bb"))
	if err := a.truncate(); err != nil {
		t.Fatal(err)
	}
	if a.tail != 0 {
		t.Fatalf("tail = %d", a.tail)
	}
	if fi, _ := os.Lstat(a.path); fi.Size() != 0 {
		t.Fatalf("size = %d", fi.Size())
	}
	a.entries = map[int]types.HLSSpillEntry{}
	e, err := a.append(3, []byte("cc"))
	if err != nil || e.Offset != 0 {
		t.Fatalf("reuse append: %+v %v", e, err)
	}
}

func TestSpillArena_TruncateFailureKeepsTail(t *testing.T) {
	a, _, _, ops := newArenaFixture(t, "id-9")
	a.append(1, []byte("aaaa"))
	boom := errors.New("truncate boom")
	ops.truncate = func(f *os.File, size int64) error { return boom }
	if err := a.truncate(); !errors.Is(err, boom) {
		t.Fatalf("truncate err = %v", err)
	}
	if a.tail != 4 {
		t.Fatalf("tail reset on failure: %d", a.tail)
	}
}

func TestSpillArena_SyncAndENOSPC(t *testing.T) {
	a, _, _, ops := newArenaFixture(t, "id-9")
	// Sync on a lazily-unopened arena is a no-op.
	if err := a.sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.append(1, []byte("x")); err != nil {
		t.Fatal(err)
	}
	enospc := &os.PathError{Op: "sync", Path: a.path, Err: diskFullErrno()}
	ops.syncFn = func(f *os.File) error { return enospc }
	err := a.sync()
	if !errors.Is(err, types.ErrInsufficientDiskSpace) {
		t.Fatalf("sync err not annotated: %v", err)
	}
	if _, ok := errors.AsType[*storageError](err); !ok {
		t.Fatalf("no storageError: %v", err)
	}
	pe, ok := errors.AsType[*os.PathError](err)
	if !ok || !errors.Is(pe.Err, diskFullErrno()) {
		t.Fatalf("PathError lost: %v", err)
	}
}

func TestSpillArena_CloseIdempotentKeepsFile(t *testing.T) {
	a, _, dir, _ := newArenaFixture(t, "id-9")
	a.append(1, []byte("data"))
	if err := a.close(); err != nil {
		t.Fatal(err)
	}
	if err := a.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".id-9.spill")); err != nil {
		t.Fatalf("arena deleted on close: %v", err)
	}
	if a.scratch != nil || a.file != nil {
		t.Fatal("close did not release resources")
	}
}

func TestSpillArena_CreateDeniedOnce(t *testing.T) {
	a, _, dir, ops := newArenaFixture(t, "id-9")
	sp := filepath.Join(dir, ".id-9.spill")
	if err := os.WriteFile(sp, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.append(1, []byte("new")); err == nil {
		t.Fatal("existing arena adopted")
	}
	b, _ := os.ReadFile(sp)
	if string(b) != "old" {
		t.Fatalf("existing arena truncated: %q", b)
	}
	if a.file != nil || a.tail != 0 {
		t.Fatal("failed create left state")
	}
	boom := &os.PathError{Op: "open", Path: sp, Err: diskFullErrno()}
	ops.open = func(name string, flag int, perm os.FileMode) (*os.File, error) { return nil, boom }
	if _, err := a.append(1, []byte("x")); !errors.Is(err, types.ErrInsufficientDiskSpace) {
		t.Fatalf("create ENOSPC not annotated: %v", err)
	}
}

func TestReorderWindow_OutSeekFailure(t *testing.T) {
	fx := newWindowFixture(t, 1, 0, 0, []int64{4}, "test-dl")
	boom := errors.New("seek boom")
	fx.ops.seek = func(f *os.File, off int64, whence int) (int64, error) { return 0, boom }
	res, err := fx.w.Ingest(0, segPayload('0', 4))
	if res.Accepted || !errors.Is(err, boom) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	se, ok := errors.AsType[*storageError](err)
	if !ok || se.Op != "output seek" {
		t.Fatalf("err = %v", err)
	}
}

func TestReorderWindow_PendingThenSpillThenMergeMix(t *testing.T) {
	fx := newWindowFixture(t, 4, 0, 0, []int64{3, 5, 7, 2}, "test-dl")
	fx.ops.rangeCopy = func(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
		return 0, true, nil
	}
	fx.w.Ingest(1, segPayload('1', 5)) // pending
	fx.w.pendingBytes = reorderPendingBytesMax
	fx.w.Ingest(3, segPayload('3', 2)) // spilled
	fx.w.Ingest(2, segPayload('2', 7)) // spilled (budget still forced)
	if _, err := fx.w.Ingest(0, segPayload('0', 3)); err != nil {
		t.Fatal(err)
	}
	want := append(segPayload('0', 3), segPayload('1', 5)...)
	want = append(want, segPayload('2', 7)...)
	want = append(want, segPayload('3', 2)...)
	if got := fx.readOut(t); !bytes.Equal(got, want) {
		t.Fatalf("mix out = %v want %v", got, want)
	}
	snap := fx.w.Snapshot()
	if snap.VerifiedBytes != snap.MergedBytes || snap.MergedBytes != 17 {
		t.Fatalf("double count: %+v", snap)
	}
}

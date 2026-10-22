package hls

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

// testSeed builds a prepared contiguous-prefix resume state: merged leading
// segments are declared with ByteLength equal to their written plaintext, so
// the constructor's source-range bound stays satisfied.
func testSeed(total, merged int, initBytes int64, segLens []int64) *types.HLSResumeState {
	segs := make([]types.HLSSegmentState, total)
	for i := range segs {
		l := int64(1000 + i)
		if segLens != nil {
			l = segLens[i]
		}
		segs[i] = types.HLSSegmentState{
			Index:      i,
			Sequence:   int64(100 + i),
			URI:        fmt.Sprintf("seg%d.ts", i),
			ByteLength: l,
		}
	}
	bitmap := make([]byte, (total+7)/8)
	for i := range merged {
		bitmap[i>>3] |= 1 << uint(i&7)
	}
	mergedBytes := initBytes
	for i := range merged {
		mergedBytes += segs[i].ByteLength
	}
	s := &types.HLSResumeState{
		ManifestURL:         "https://cdn.example/v.m3u8",
		MediaPlaylistURL:    "https://cdn.example/v.m3u8",
		ManifestFingerprint: strings.Repeat("ab", 32),
		ContainerFormat:     "mpegts",
		RecordEligible:      true,
		InitBytes:           initBytes,
		TotalSegments:       total,
		CompletedBitmap:     bitmap,
		MergedSegments:      merged,
		MergedBytes:         mergedBytes,
		VerifiedBytes:       mergedBytes,
		Keys:                []types.HLSKeyInfo{{Method: "AES-128", URI: "https://cdn.example/k", IVHex: strings.Repeat("00", 16)}},
		Segments:            segs,
	}
	if initBytes > 0 {
		s.InitSegmentURI = "init.mp4"
		s.InitCompleted = true
	}
	return s
}

type windowFixture struct {
	w    *ReorderWindow
	out  *os.File
	dir  string
	path string
	ops  *fileOps
	seed *types.HLSResumeState
}

// newWindowFixture lays out init + merged-prefix bytes on disk and opens the
// window over them. initBytes>0 declares a completed init in the seed.
func newWindowFixture(t *testing.T, total, merged int, initBytes int64, segLens []int64, id string) *windowFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "video.surge")
	seed := testSeed(total, merged, initBytes, segLens)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create out: %v", err)
	}
	var layout []byte
	layout = append(layout, bytes.Repeat([]byte{'I'}, int(initBytes))...)
	for i := range merged {
		layout = append(layout, bytes.Repeat([]byte{byte('a' + i)}, int(seed.Segments[i].ByteLength))...)
	}
	if _, err := f.WriteAt(layout, 0); err != nil {
		t.Fatalf("lay prefix: %v", err)
	}
	if err := f.Truncate(int64(len(layout))); err != nil {
		t.Fatalf("size out: %v", err)
	}
	ops := defaultFileOps()
	w, err := newReorderWindow(f, path, id, seed, ops)
	if err != nil {
		f.Close()
		t.Fatalf("newReorderWindow: %v", err)
	}
	fx := &windowFixture{w: w, out: f, dir: dir, path: path, ops: ops, seed: seed}
	t.Cleanup(func() {
		w.Close()
		f.Close()
	})
	return fx
}

func (fx *windowFixture) readOut(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(fx.path)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	return b
}

func (fx *windowFixture) spillFile() string {
	return filepath.Join(fx.dir, ".test-dl.spill")
}

func segPayload(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }

func TestReorderWindow_SeedValidation(t *testing.T) {
	dir := t.TempDir()
	mkOut := func(t *testing.T, size int64) (*os.File, string) {
		p := filepath.Join(dir, fmt.Sprintf("o%d.surge", size))
		f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f, p
	}
	good := func() *types.HLSResumeState { return testSeed(4, 1, 0, []int64{50, 60, 70, 80}) }

	t.Run("DirtySeeds", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(*types.HLSResumeState)
		}{
			{"zero total", func(s *types.HLSResumeState) { s.TotalSegments = 0 }},
			{"segment rows mismatch", func(s *types.HLSResumeState) { s.Segments = s.Segments[:3] }},
			{"unordered index", func(s *types.HLSResumeState) { s.Segments[2].Index = 9 }},
			{"merged segments over total", func(s *types.HLSResumeState) { s.MergedSegments = 5 }},
			{"bitmap wrong length", func(s *types.HLSResumeState) { s.CompletedBitmap = make([]byte, 2) }},
			{"bit outside prefix", func(s *types.HLSResumeState) { s.CompletedBitmap[0] |= 1 << 3 }},
			{"missing prefix bit", func(s *types.HLSResumeState) { s.CompletedBitmap[0] &^= 1 }},
			{"tail bits set", func(s *types.HLSResumeState) {
				s.TotalSegments = 7
				s.Segments = make([]types.HLSSegmentState, 7)
				for i := range s.Segments {
					s.Segments[i] = types.HLSSegmentState{Index: i, ByteLength: 10}
				}
				s.CompletedBitmap = []byte{0xff}
				s.MergedSegments = 7
				s.MergedBytes = 70
				s.VerifiedBytes = 70
			}},
			{"init counters without init", func(s *types.HLSResumeState) {
				s.InitBytes = 5
				s.MergedBytes = 55
				s.VerifiedBytes = 55
			}},
			{"init incomplete", func(s *types.HLSResumeState) {
				s.InitSegmentURI = "init.mp4"
			}},
			{"merged below init", func(s *types.HLSResumeState) {
				s.InitSegmentURI = "init.mp4"
				s.InitCompleted = true
				s.InitBytes = 60
			}},
			{"merged not init at zero", func(s *types.HLSResumeState) {
				s.MergedSegments = 0
				s.CompletedBitmap = make([]byte, 1)
			}},
			{"merged equals init with segments", func(s *types.HLSResumeState) {
				s.InitSegmentURI = "init.mp4"
				s.InitCompleted = true
				s.InitBytes = 50
				s.MergedBytes = 50
				s.VerifiedBytes = 50
			}},
			{"merged over source range", func(s *types.HLSResumeState) {
				s.MergedBytes = 51
				s.VerifiedBytes = 51
			}},
			{"verified mismatch", func(s *types.HLSResumeState) { s.VerifiedBytes = 49 }},
			{"spill path seeded", func(s *types.HLSResumeState) { s.SpillPath = "x.spill" }},
			{"spill size seeded", func(s *types.HLSResumeState) { s.SpillFileSize = 4 }},
			{"spill entries seeded", func(s *types.HLSResumeState) {
				s.SpillEntries = []types.HLSSpillEntry{{SegmentIndex: 2, Offset: 0, Length: 4}}
			}},
		}
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				seed := good()
				tt.mutate(seed)
				out, p := mkOut(t, seed.MergedBytes)
				if _, err := newReorderWindow(out, p, "id-1", seed, nil); !errors.Is(err, errDirtySeed) {
					t.Fatalf("err = %v, want errDirtySeed", err)
				}
			})
		}
	})

	t.Run("SizeMismatch", func(t *testing.T) {
		out, p := mkOut(t, 49)
		if _, err := newReorderWindow(out, p, "id-1", good(), nil); !errors.Is(err, errDirtySeed) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("BadArgs", func(t *testing.T) {
		out, p := mkOut(t, 50)
		if _, err := newReorderWindow(nil, p, "id-1", good(), nil); err == nil {
			t.Fatal("nil out accepted")
		}
		if _, err := newReorderWindow(out, "", "id-1", good(), nil); err == nil {
			t.Fatal("empty destPath accepted")
		}
		if _, err := newReorderWindow(out, p, "id-1", nil, nil); err == nil {
			t.Fatal("nil seed accepted")
		}
	})

	t.Run("BadDownloadIDs", func(t *testing.T) {
		out, p := mkOut(t, 50)
		for _, id := range []string{"", "..", "a/b", `a\b`, "a:b", "a?b", `a"b`, "a<b", "a>b", "a|b", "a*b", "a.b ", "a.", " a", "a\x00b", strings.Repeat("x", 201)} {
			if _, err := newReorderWindow(out, p, id, good(), nil); err == nil {
				t.Fatalf("id %q accepted", id)
			}
		}
	})

	t.Run("HappySeed", func(t *testing.T) {
		out, p := mkOut(t, 50)
		w, err := newReorderWindow(out, p, "id-1", good(), nil)
		if err != nil {
			t.Fatalf("seed rejected: %v", err)
		}
		w.Close()
	})
}

func TestReorderWindow_InOrderDirectWrites(t *testing.T) {
	fx := newWindowFixture(t, 3, 0, 4, []int64{5, 6, 7}, "test-dl")
	if _, err := os.Lstat(fx.spillFile()); !os.IsNotExist(err) {
		t.Fatalf("spill file created eagerly: %v", err)
	}
	for i, n := range []int{5, 6, 7} {
		res, err := fx.w.Ingest(i, segPayload(byte('0'+i), n))
		if err != nil || !res.Accepted || !res.Direct {
			t.Fatalf("ingest %d: res=%+v err=%v", i, res, err)
		}
	}
	want := append([]byte("IIII"), segPayload('0', 5)...)
	want = append(want, segPayload('1', 6)...)
	want = append(want, segPayload('2', 7)...)
	if got := fx.readOut(t); !bytes.Equal(got, want) {
		t.Fatalf("out mismatch: got %d bytes", len(got))
	}
	snap := fx.w.Snapshot()
	if snap.MergedSegments != 3 || snap.MergedBytes != 22 || snap.VerifiedBytes != 22 {
		t.Fatalf("snap merged=%d/%d verified=%d", snap.MergedSegments, snap.MergedBytes, snap.VerifiedBytes)
	}
	if snap.CompletedBitmap[0] != 0x07 {
		t.Fatalf("bitmap = %08b", snap.CompletedBitmap[0])
	}
	if len(snap.SpillEntries) != 0 || snap.SpillFileSize != 0 || snap.SpillPath != "" {
		t.Fatalf("spill fields populated: %+v", snap)
	}
}

func TestReorderWindow_ChainDrainFromPending(t *testing.T) {
	fx := newWindowFixture(t, 3, 0, 0, []int64{4, 5, 6}, "test-dl")
	res, err := fx.w.Ingest(2, segPayload('2', 6))
	if err != nil || !res.Accepted || !res.Buffered {
		t.Fatalf("ingest 2: %+v %v", res, err)
	}
	res, err = fx.w.Ingest(1, segPayload('1', 5))
	if err != nil || !res.Accepted || !res.Buffered {
		t.Fatalf("ingest 1: %+v %v", res, err)
	}
	res, err = fx.w.Ingest(0, segPayload('0', 4))
	if err != nil || !res.Accepted || !res.Direct {
		t.Fatalf("ingest 0: %+v %v", res, err)
	}
	want := append(segPayload('0', 4), segPayload('1', 5)...)
	want = append(want, segPayload('2', 6)...)
	if got := fx.readOut(t); !bytes.Equal(got, want) {
		t.Fatalf("drain wrote %v", got)
	}
	if len(fx.w.pending) != 0 || fx.w.pendingBytes != 0 {
		t.Fatalf("pending not drained: %d %d", len(fx.w.pending), fx.w.pendingBytes)
	}
}

func TestReorderWindow_HoleBlocksDrain(t *testing.T) {
	fx := newWindowFixture(t, 4, 0, 0, nil, "test-dl")
	fx.w.Ingest(3, segPayload('3', 10))
	fx.w.Ingest(1, segPayload('1', 10))
	res, err := fx.w.Ingest(0, segPayload('0', 10))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Direct {
		t.Fatal("0 should direct-write")
	}
	snap := fx.w.Snapshot()
	if snap.MergedSegments != 2 {
		t.Fatalf("contiguous pending must drain: %d", snap.MergedSegments)
	}
	if snap.MergedBytes != 20 {
		t.Fatalf("merged bytes = %d", snap.MergedBytes)
	}
	if _, ok := fx.w.pending[3]; !ok || len(fx.w.pending) != 1 {
		t.Fatal("segment 3 must remain pending over the hole")
	}
}

func TestReorderWindow_DuplicatesAcrossSets(t *testing.T) {
	fx := newWindowFixture(t, 5, 0, 0, nil, "test-dl")
	fx.w.Ingest(0, segPayload('0', 10)) // merged
	fx.w.Ingest(3, segPayload('3', 10)) // pending
	fx.ops.rangeCopy = func(a, b *os.File, c, d, e int64) (int64, bool, error) {
		return 0, true, nil
	}
	fx.w.pendingBytes = reorderPendingBytesMax      // force spill path for next
	res, err := fx.w.Ingest(4, segPayload('4', 10)) // spilled
	if err != nil || !res.Spilled {
		t.Fatalf("spill ingest: %+v %v", res, err)
	}
	for i, name := range map[int]string{0: "merged", 3: "pending", 4: "spilled"} {
		res, err := fx.w.Ingest(i, segPayload('z', 10))
		if err != nil || res.Accepted || !res.Duplicate {
			t.Fatalf("dup %s: %+v %v", name, res, err)
		}
	}
	// Verified counts durable bytes only: merged prefix + live spill, never
	// pending memory.
	if fx.w.Snapshot().VerifiedBytes != 20 {
		t.Fatalf("verified = %d", fx.w.Snapshot().VerifiedBytes)
	}
}

func TestReorderWindow_PendingBudgets(t *testing.T) {
	t.Run("16SegmentThreshold", func(t *testing.T) {
		fx := newWindowFixture(t, 20, 0, 0, nil, "test-dl")
		for i := 1; i <= 16; i++ {
			res, err := fx.w.Ingest(i, segPayload(byte(i), 8))
			if err != nil || !res.Buffered {
				t.Fatalf("pending %d: %+v %v", i, res, err)
			}
		}
		res, err := fx.w.Ingest(17, segPayload('x', 8))
		if err != nil || !res.Accepted || !res.Spilled {
			t.Fatalf("17th segment: %+v %v", res, err)
		}
		if _, err := os.Lstat(fx.spillFile()); err != nil {
			t.Fatalf("spill file missing: %v", err)
		}
	})

	t.Run("Exact32MiBStays", func(t *testing.T) {
		fx := newWindowFixture(t, 4, 0, 0, nil, "test-dl")
		res, err := fx.w.Ingest(1, segPayload('a', 16<<20))
		if err != nil || !res.Buffered {
			t.Fatalf("first 16MiB: %+v %v", res, err)
		}
		res, err = fx.w.Ingest(2, segPayload('b', 16<<20))
		if err != nil || !res.Buffered {
			t.Fatalf("exact-32MiB segment must stay pending: %+v %v", res, err)
		}
		res, err = fx.w.Ingest(3, segPayload('c', 1))
		if err != nil || !res.Spilled {
			t.Fatalf("33rd MiB must spill: %+v %v", res, err)
		}
		if len(fx.w.pending) != 2 {
			t.Fatalf("pending=%d", len(fx.w.pending))
		}
	})

	t.Run("OversizedGoesSpill", func(t *testing.T) {
		fx := newWindowFixture(t, 3, 0, 0, nil, "test-dl")
		res, err := fx.w.Ingest(2, segPayload('b', reorderPendingBytesMax+1))
		if err != nil || !res.Spilled {
			t.Fatalf("oversized pending: %+v %v", res, err)
		}
		if fx.w.Snapshot().SpillFileSize != reorderPendingBytesMax+1 {
			t.Fatalf("spill size = %d", fx.w.Snapshot().SpillFileSize)
		}
	})
}

func TestReorderWindow_BufferOwnership(t *testing.T) {
	fx := newWindowFixture(t, 3, 0, 0, []int64{4, 4, 4}, "test-dl")
	buf := make([]byte, 4, 1<<20)
	copy(buf, []byte("WXYZ"))
	res, err := fx.w.Ingest(1, buf)
	if err != nil || !res.Buffered {
		t.Fatalf("ingest: %+v %v", res, err)
	}
	// Mutate the caller buffer beyond recognition, including spare capacity.
	for i := range buf[:cap(buf)] {
		buf[:cap(buf)][i] = '!'
	}
	fx.w.Ingest(0, []byte("0000"))
	fx.w.Ingest(2, []byte("2222"))
	want := append([]byte("0000"), []byte("WXYZ")...)
	want = append(want, []byte("2222")...)
	if got := fx.readOut(t); !bytes.Equal(got, want) {
		t.Fatalf("out = %q want %q", got, want)
	}
}

func TestReorderWindow_SpillLifecycle(t *testing.T) {
	fx := newWindowFixture(t, 3, 0, 0, []int64{4, 5, 6}, "test-dl")
	fx.ops.rangeCopy = func(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
		return 0, true, nil
	}
	fx.w.pendingBytes = reorderPendingBytesMax
	res, err := fx.w.Ingest(2, segPayload('2', 6))
	if err != nil || !res.Spilled {
		t.Fatalf("spill: %+v %v", res, err)
	}
	sp := fx.spillFile()
	if filepath.Dir(sp) != fx.dir || filepath.Base(sp) != ".test-dl.spill" {
		t.Fatalf("spill path %q", sp)
	}
	fi, err := os.Lstat(sp)
	if err != nil || fi.Size() != 6 {
		t.Fatalf("spill file: %v size=%d", err, fi.Size())
	}
	snap := fx.w.Snapshot()
	if len(snap.SpillEntries) != 1 || snap.SpillEntries[0] != (types.HLSSpillEntry{SegmentIndex: 2, Offset: 0, Length: 6}) {
		t.Fatalf("entries = %+v", snap.SpillEntries)
	}
	if snap.VerifiedBytes != snap.MergedBytes+6 || snap.SpillFileSize != 6 || snap.SpillPath != sp {
		t.Fatalf("snap: %+v", snap)
	}
	if snap.CompletedBitmap[0]&0x04 == 0 {
		t.Fatal("spilled bit not set")
	}
	// Second out-of-order segment appends at the confirmed tail.
	res, err = fx.w.Ingest(1, segPayload('1', 5))
	if err != nil || !res.Spilled {
		t.Fatalf("spill 2: %+v %v", res, err)
	}
	snap = fx.w.Snapshot()
	if len(snap.SpillEntries) != 2 ||
		snap.SpillEntries[0] != (types.HLSSpillEntry{SegmentIndex: 1, Offset: 6, Length: 5}) {
		t.Fatalf("entries = %+v", snap.SpillEntries)
	}
	// Merged frontier drains both spilled segments in order.
	if _, err := fx.w.Ingest(0, segPayload('0', 4)); err != nil {
		t.Fatalf("frontier: %v", err)
	}
	want := append(segPayload('0', 4), segPayload('1', 5)...)
	want = append(want, segPayload('2', 6)...)
	if got := fx.readOut(t); !bytes.Equal(got, want) {
		t.Fatalf("replayed out = %v", got)
	}
	snap = fx.w.Snapshot()
	if len(snap.SpillEntries) != 0 || snap.SpillFileSize != 0 {
		t.Fatalf("arena not reclaimed: %+v", snap)
	}
	if fi, err := os.Lstat(sp); err != nil || fi.Size() != 0 {
		t.Fatalf("arena not truncated: %v %d", err, fi.Size())
	}
	if snap.VerifiedBytes != snap.MergedBytes || snap.MergedBytes != 15 {
		t.Fatalf("verified %d merged %d", snap.VerifiedBytes, snap.MergedBytes)
	}
	// Reuse: a fresh spill must start again at offset 0.
	res, err = fx.w.Ingest(0, segPayload('z', 1))
	if err != nil || !res.Duplicate {
		t.Fatalf("dup after merge: %+v %v", res, err)
	}
}

func TestReorderWindow_SpillReuseOffsetZero(t *testing.T) {
	fx := newWindowFixture(t, 4, 0, 0, []int64{4, 4, 4, 4}, "test-dl")
	fx.ops.rangeCopy = func(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
		return 0, true, nil
	}
	fx.w.pendingBytes = reorderPendingBytesMax
	if _, err := fx.w.Ingest(1, segPayload('1', 4)); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.w.Ingest(0, segPayload('0', 4)); err != nil {
		t.Fatal(err)
	}
	// Arena drained and truncated; a new out-of-order segment reuses offset 0.
	fx.w.pendingBytes = reorderPendingBytesMax
	if _, err := fx.w.Ingest(3, segPayload('3', 4)); err != nil {
		t.Fatal(err)
	}
	snap := fx.w.Snapshot()
	if len(snap.SpillEntries) != 1 || snap.SpillEntries[0].Offset != 0 {
		t.Fatalf("reuse entries = %+v", snap.SpillEntries)
	}
}

func TestReorderWindow_PartialRangeCopyTail(t *testing.T) {
	fx := newWindowFixture(t, 2, 0, 0, []int64{4, 9}, "test-dl")
	payload := []byte("123456789")
	fx.w.pendingBytes = reorderPendingBytesMax
	if _, err := fx.w.Ingest(1, payload); err != nil {
		t.Fatal(err)
	}
	var calls int
	fx.ops.rangeCopy = func(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
		calls++
		if dstOff != 4 || srcOff != 0 || length != 9 {
			t.Fatalf("rangeCopy args %d %d %d", dstOff, srcOff, length)
		}
		b := make([]byte, 5)
		if _, err := src.ReadAt(b, srcOff); err != nil {
			t.Fatal(err)
		}
		if _, err := dst.WriteAt(b, dstOff); err != nil {
			t.Fatal(err)
		}
		return 5, true, nil // partial + unsupported: portable must finish the tail
	}
	if _, err := fx.w.Ingest(0, segPayload('0', 4)); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if calls != 1 {
		t.Fatalf("rangeCopy called %d times", calls)
	}
	want := append(segPayload('0', 4), payload...)
	if got := fx.readOut(t); !bytes.Equal(got, want) {
		t.Fatalf("out = %q want %q", got, want)
	}
}

func TestReorderWindow_RangeCopyErrorKeepsEntry(t *testing.T) {
	fx := newWindowFixture(t, 2, 0, 0, []int64{4, 9}, "test-dl")
	fx.w.pendingBytes = reorderPendingBytesMax
	if _, err := fx.w.Ingest(1, segPayload('1', 9)); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("copy boom")
	fx.ops.rangeCopy = func(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
		return 3, false, boom
	}
	res, err := fx.w.Ingest(0, segPayload('0', 4))
	if !res.Accepted || err == nil {
		t.Fatalf("own write succeeded but drain must report: %+v %v", res, err)
	}
	if _, ok := errors.AsType[*storageError](err); !ok || !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	snap := fx.w.Snapshot()
	if snap.MergedSegments != 1 || len(snap.SpillEntries) != 1 {
		t.Fatalf("state corrupted: %+v", snap)
	}
	if _, err := fx.w.Ingest(1, segPayload('q', 9)); !errors.Is(err, boom) {
		t.Fatalf("latched err = %v", err)
	}
}

func TestReorderWindow_WriteFaultLatch(t *testing.T) {
	fx := newWindowFixture(t, 2, 0, 0, []int64{4, 4}, "test-dl")
	boom := &os.PathError{Op: "write", Path: fx.path, Err: diskFullErrno()}
	var n int
	fx.ops.write = func(f *os.File, b []byte) (int, error) {
		n++
		return 0, boom
	}
	res, err := fx.w.Ingest(0, segPayload('0', 4))
	if res.Accepted || err == nil {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !errors.Is(err, types.ErrInsufficientDiskSpace) {
		t.Fatalf("ENOSPC not annotated: %v", err)
	}
	if _, ok := errors.AsType[*os.PathError](err); !ok {
		t.Fatalf("PathError lost: %v", err)
	}
	se, ok := errors.AsType[*storageError](err)
	if !ok || se.Retryable {
		t.Fatalf("storageError = %+v", se)
	}
	if _, err := fx.w.Ingest(1, segPayload('1', 4)); !errors.Is(err, boom) {
		t.Fatalf("latch err = %v", err)
	}
	if n != 1 {
		t.Fatalf("write attempted %d times", n)
	}
	if fi, _ := os.Lstat(fx.path); fi.Size() != 0 {
		t.Fatalf("bytes committed: %d", fi.Size())
	}
	if fx.w.Snapshot().MergedSegments != 0 {
		t.Fatal("partial commit leaked")
	}
}

func TestReorderWindow_ShortWriteLoop(t *testing.T) {
	fx := newWindowFixture(t, 1, 0, 0, []int64{8}, "test-dl")
	real := (*os.File).Write
	var calls int
	fx.ops.write = func(f *os.File, b []byte) (int, error) {
		calls++
		if len(b) > 3 {
			return real(f, b[:3])
		}
		return real(f, b)
	}
	if _, err := fx.w.Ingest(0, segPayload('0', 8)); err != nil {
		t.Fatalf("short writes must be completed: %v", err)
	}
	if calls < 3 {
		t.Fatalf("write calls = %d", calls)
	}
	// A zero-progress writer must fail rather than spin.
	fx2 := newWindowFixture(t, 1, 0, 0, []int64{8}, "test-dl2")
	fx2.ops.write = func(f *os.File, b []byte) (int, error) { return 0, nil }
	if _, err := fx2.w.Ingest(0, segPayload('0', 8)); err == nil || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress err = %v", err)
	}
}

func TestReorderWindow_StaleSpillProtected(t *testing.T) {
	fx := newWindowFixture(t, 2, 0, 0, []int64{4, 4}, "test-dl")
	sp := fx.spillFile()
	if err := os.WriteFile(sp, []byte("stale-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	fx.w.pendingBytes = reorderPendingBytesMax
	res, err := fx.w.Ingest(1, segPayload('1', 4))
	if res.Accepted || err == nil {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	b, _ := os.ReadFile(sp)
	if string(b) != "stale-bytes" {
		t.Fatalf("stale file modified: %q", b)
	}
	se, ok := errors.AsType[*storageError](err)
	if !ok || se.Op != "spill create" {
		t.Fatalf("err = %v", err)
	}
}

func TestReorderWindow_TruncateFailurePreservesHighWater(t *testing.T) {
	fx := newWindowFixture(t, 2, 0, 0, []int64{4, 4}, "test-dl")
	fx.ops.rangeCopy = func(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
		return 0, true, nil
	}
	fx.w.pendingBytes = reorderPendingBytesMax
	if _, err := fx.w.Ingest(1, segPayload('1', 4)); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("truncate boom")
	fx.ops.truncate = func(f *os.File, size int64) error { return boom }
	res, err := fx.w.Ingest(0, segPayload('0', 4))
	if !res.Accepted || !errors.Is(err, boom) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	snap := fx.w.Snapshot()
	if snap.MergedSegments != 2 || len(snap.SpillEntries) != 0 {
		t.Fatalf("prefix rolled back: %+v", snap)
	}
	if snap.SpillFileSize != 4 {
		t.Fatalf("faked reclamation: size=%d", snap.SpillFileSize)
	}
	if fi, _ := os.Lstat(fx.spillFile()); fi.Size() != 4 {
		t.Fatalf("physical size = %d", fi.Size())
	}
}

func TestReorderWindow_SnapshotIsolation(t *testing.T) {
	fx := newWindowFixture(t, 3, 1, 4, []int64{4, 4, 4}, "test-dl")
	fx.ops.rangeCopy = func(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
		return 0, true, nil
	}
	fx.w.pendingBytes = reorderPendingBytesMax
	fx.w.Ingest(2, segPayload('2', 4))
	snap := fx.w.Snapshot()
	if snap.MergedSegments != 1 || snap.VerifiedBytes != snap.MergedBytes+4 {
		t.Fatalf("snap counters: %+v", snap)
	}
	// Mutate every published slice; live state must not move.
	snap.CompletedBitmap[0] = 0
	snap.SpillEntries[0].Length = 999
	snap.Segments[0].URI = "mutated"
	snap.Keys[0].URI = "mutated"
	snap2 := fx.w.Snapshot()
	if snap2.CompletedBitmap[0]&0x05 != 0x05 {
		t.Fatalf("live bitmap touched: %08b", snap2.CompletedBitmap[0])
	}
	if snap2.SpillEntries[0].Length != 4 || snap2.Segments[0].URI == "mutated" || snap2.Keys[0].URI == "mutated" {
		t.Fatal("live slices aliased through snapshot")
	}
	// Prefix projection: prefix bits only, spill cleared, live untouched.
	prefix := fx.w.SnapshotMergedPrefix()
	if prefix.VerifiedBytes != prefix.MergedBytes || len(prefix.SpillEntries) != 0 ||
		prefix.SpillFileSize != 0 || prefix.SpillPath != "" {
		t.Fatalf("projection: %+v", prefix)
	}
	if prefix.CompletedBitmap[0]&0x04 != 0 {
		t.Fatal("projection kept spill bit")
	}
	live := fx.w.Snapshot()
	if len(live.SpillEntries) != 1 || live.CompletedBitmap[0]&0x04 == 0 {
		t.Fatal("projection mutated live state")
	}
	// Manifest/init metadata preserved in projection.
	if prefix.ManifestURL != fx.seed.ManifestURL || !prefix.InitCompleted || prefix.InitBytes != 4 {
		t.Fatalf("metadata lost: %+v", prefix)
	}
}

func TestReorderWindow_ConcurrentIngestAndSnapshot(t *testing.T) {
	const total = 64
	fx := newWindowFixture(t, total, 0, 0, nil, "test-dl")
	var accepted int64
	var wg sync.WaitGroup
	order := make([]int, total)
	for i := range order {
		order[i] = (i*37 + 11) % total
	}
	for _, idx := range order {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := fx.w.Ingest(i, segPayload(byte(i%251+1), 64))
			if err != nil {
				t.Errorf("ingest %d: %v", i, err)
				return
			}
			if res.Accepted {
				atomic.AddInt64(&accepted, 1)
			}
		}(idx)
	}
	// Interleave snapshots; they must never race or panic.
	for range 8 {
		fx.w.Snapshot()
		fx.w.SnapshotMergedPrefix()
	}
	wg.Wait()
	if atomic.LoadInt64(&accepted) != total {
		t.Fatalf("accepted = %d", accepted)
	}
	got := fx.readOut(t)
	var want []byte
	for i := range total {
		want = append(want, segPayload(byte(i%251+1), 64)...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ordered payload mismatch: got %d bytes", len(got))
	}
	snap := fx.w.Snapshot()
	if snap.MergedSegments != total || snap.VerifiedBytes != snap.MergedBytes {
		t.Fatalf("final snap: %+v", snap)
	}
}

func TestReorderWindow_DuplicateRaceSingleAccept(t *testing.T) {
	fx := newWindowFixture(t, 2, 0, 0, nil, "test-dl")
	var accepted int64
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			res, err := fx.w.Ingest(0, segPayload('0', 8))
			if err == nil && res.Accepted {
				atomic.AddInt64(&accepted, 1)
			}
		})
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("accepted = %d", accepted)
	}
}

func TestReorderWindow_SyncAndClose(t *testing.T) {
	fx := newWindowFixture(t, 2, 0, 0, []int64{4, 4}, "test-dl")
	fx.ops.rangeCopy = func(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
		return 0, true, nil
	}
	fx.w.pendingBytes = reorderPendingBytesMax
	fx.w.Ingest(1, segPayload('1', 4))
	var synced int
	realSync := (*os.File).Sync
	fx.ops.syncFn = func(f *os.File) error {
		synced++
		return realSync(f)
	}
	if err := fx.w.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if synced != 2 {
		t.Fatalf("synced %d files", synced)
	}
	// Out sync failure propagates and latches; arena sync error also reports.
	boom := errors.New("sync boom")
	fx.ops.syncFn = func(f *os.File) error { return boom }
	if err := fx.w.Sync(); !errors.Is(err, boom) {
		t.Fatalf("sync err = %v", err)
	}
	if _, ok := errors.AsType[*storageError](fx.w.storeErr); !ok {
		t.Fatal("sync failure not latched")
	}
	// A latched storage fault rejects subsequent input with the same error.
	if res, err := fx.w.Ingest(0, segPayload('q', 4)); res.Accepted || !errors.Is(err, boom) {
		t.Fatalf("ingest after latched sync = %+v %v", res, err)
	}
	fx.ops.syncFn = func(f *os.File) error { return nil }

	// Close: idempotent, out untouched, spill file preserved.
	if err := fx.w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := fx.w.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := fx.out.WriteAt([]byte{'x'}, 0); err != nil {
		t.Fatalf("out closed by window: %v", err)
	}
	if _, err := os.Lstat(fx.spillFile()); err != nil {
		t.Fatalf("spill deleted: %v", err)
	}
	if _, err := fx.w.Ingest(0, segPayload('0', 4)); !errors.Is(err, errWindowClosed) {
		t.Fatalf("ingest after close = %v", err)
	}
	if err := fx.w.Sync(); !errors.Is(err, errWindowClosed) {
		t.Fatalf("sync after close = %v", err)
	}
	if len(fx.w.pending) != 0 {
		t.Fatal("pending not released")
	}
}

func TestReorderWindow_InitPrefixAccounting(t *testing.T) {
	fx := newWindowFixture(t, 2, 0, 8, []int64{4, 4}, "test-dl")
	if fx.w.merged != 8 {
		t.Fatalf("merged seed = %d", fx.w.merged)
	}
	if _, err := fx.w.Ingest(0, segPayload('0', 4)); err != nil {
		t.Fatal(err)
	}
	snap := fx.w.Snapshot()
	if snap.MergedBytes != 12 || snap.MergedSegments != 1 {
		t.Fatalf("snap: %+v", snap)
	}
	if snap.CompletedBitmap[0] != 0x01 {
		t.Fatalf("init must not occupy media bit: %08b", snap.CompletedBitmap[0])
	}
	got := fx.readOut(t)
	if !bytes.Equal(got[:8], bytes.Repeat([]byte{'I'}, 8)) {
		t.Fatalf("init overwritten: %q", got[:8])
	}
}

func TestReorderWindow_IngestBoundsAndEmpty(t *testing.T) {
	fx := newWindowFixture(t, 2, 0, 0, nil, "test-dl")
	for _, i := range []int{-1, 2, math.MaxInt32} {
		res, err := fx.w.Ingest(i, segPayload('x', 1))
		if err == nil || res.Accepted {
			t.Fatalf("index %d: %+v %v", i, res, err)
		}
		if derr, ok := errors.AsType[*segmentDataError](err); ok || derr != nil {
			t.Fatalf("index %d classified as segmentDataError", i)
		}
	}
	res, err := fx.w.Ingest(0, nil)
	if err == nil || res.Accepted {
		t.Fatalf("empty payload: %+v %v", res, err)
	}
	if _, ok := errors.AsType[*segmentDataError](err); !ok {
		t.Fatalf("empty payload not segmentDataError: %v", err)
	}
	if fx.w.storeErr != nil {
		t.Fatal("input errors latched storage fault")
	}
}

// The single mutex makes replay atomic with respect to other callers; this
// pins the interleave deterministically instead of hoping a scheduler lands
// it — a blocked range copy must park Ingest and Snapshot alike until it
// returns.
func TestReorderWindow_ReplayBarrierBlocksIngestAndSnapshot(t *testing.T) {
	fx := newWindowFixture(t, 2, 0, 0, []int64{4, 6}, "test-dl")
	fx.w.pendingBytes = reorderPendingBytesMax
	if _, err := fx.w.Ingest(1, segPayload('1', 6)); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	fx.ops.rangeCopy = func(dst, src *os.File, dstOff, srcOff, length int64) (int64, bool, error) {
		close(entered)
		<-release
		return 0, true, nil
	}
	ingestDone := make(chan error, 1)
	go func() {
		_, err := fx.w.Ingest(0, segPayload('0', 4))
		ingestDone <- err
	}()
	select {
	case <-entered: // drain is inside the lock, replay blocked mid-copy
	case <-time.After(2 * time.Second):
		t.Fatal("rangeCopy never entered")
	}

	snapDone := make(chan *types.HLSResumeState, 1)
	go func() { snapDone <- fx.w.Snapshot() }()
	dupDone := make(chan IngestResult, 1)
	go func() {
		res, _ := fx.w.Ingest(0, segPayload('z', 4))
		dupDone <- res
	}()
	select {
	case s := <-snapDone:
		t.Fatalf("snapshot returned merged=%d while replay blocked", s.MergedSegments)
	case r := <-dupDone:
		t.Fatalf("duplicate ingest returned %+v while replay blocked", r)
	case <-time.After(60 * time.Millisecond):
	}

	once.Do(func() { close(release) })
	if err := <-ingestDone; err != nil {
		t.Fatalf("drain ingest: %v", err)
	}
	if snap := <-snapDone; snap.MergedSegments != 2 || snap.MergedBytes != 10 {
		t.Fatalf("post-release snap: %+v", snap)
	}
	if dup := <-dupDone; !dup.Duplicate || dup.Accepted {
		t.Fatalf("post-release dup = %+v", dup)
	}
	want := append(segPayload('0', 4), segPayload('1', 6)...)
	if got := fx.readOut(t); !bytes.Equal(got, want) {
		t.Fatalf("out = %v want %v", got, want)
	}
}

// A merged-prefix projection captured mid-generation stays a conservative,
// still-valid reseed even after the arena drains, truncates, and a later
// spill generation reuses the same offsets.
func TestReorderWindow_ABAProjectionReseed(t *testing.T) {
	fx := newWindowFixture(t, 8, 0, 0, []int64{3, 3, 3, 3, 3, 3, 3, 3}, "test-dl")
	if _, err := fx.w.Ingest(0, segPayload('a', 3)); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.w.Ingest(1, segPayload('b', 3)); err != nil {
		t.Fatal(err)
	}

	// Generation 1: seg 3 spills at arena offset 0.
	fx.w.pendingBytes = reorderPendingBytesMax
	if _, err := fx.w.Ingest(3, segPayload('d', 3)); err != nil {
		t.Fatal(err)
	}
	p1 := fx.w.SnapshotMergedPrefix()
	if p1.MergedSegments != 2 || p1.MergedBytes != 6 || len(p1.SpillEntries) != 0 || p1.SpillFileSize != 0 {
		t.Fatalf("projection = %+v", p1)
	}

	// Fill the hole: seg 2 merges, drain replays seg 3, arena truncates to 0.
	if _, err := fx.w.Ingest(2, segPayload('c', 3)); err != nil {
		t.Fatal(err)
	}
	if got := fx.w.Snapshot(); got.MergedSegments != 4 || len(got.SpillEntries) != 0 {
		t.Fatalf("post-drain = %+v", got)
	}

	// Generation 2: seg 5 spills and reuses offset 0 (ABA on the arena file).
	if _, err := fx.w.Ingest(5, segPayload('f', 3)); err != nil {
		t.Fatal(err)
	}
	live := fx.w.Snapshot()
	if len(live.SpillEntries) != 1 || live.SpillEntries[0].SegmentIndex != 5 || live.SpillEntries[0].Offset != 0 {
		t.Fatalf("gen2 spill = %+v", live.SpillEntries)
	}

	// The old projection still claims no spill and its prefix bytes are intact.
	if len(p1.SpillEntries) != 0 || p1.SpillFileSize != 0 || p1.MergedSegments != 2 {
		t.Fatalf("stale projection mutated: %+v", p1)
	}
	wantPrefix := append(segPayload('a', 3), segPayload('b', 3)...)
	if got := fx.readOut(t)[:p1.MergedBytes]; !bytes.Equal(got, wantPrefix) {
		t.Fatalf("prefix bytes = %q", got)
	}
	if p1.CompletedBitmap[0] != 0b11 {
		t.Fatalf("projection bitmap = %08b", p1.CompletedBitmap[0])
	}

	// A new window seeded from the projection re-accepts spilled segments —
	// the projection never claimed them.
	fx2 := newWindowFixture(t, 8, 2, 0, []int64{3, 3, 3, 3, 3, 3, 3, 3}, "test-dl2")
	w2, err := newReorderWindow(fx2.out, fx2.path, "test-dl2", p1, defaultFileOps())
	if err != nil {
		t.Fatalf("reseed: %v", err)
	}
	t.Cleanup(func() { w2.Close() })
	for _, idx := range []int{3, 5} {
		res, err := w2.Ingest(idx, segPayload('x', 3))
		if err != nil || !res.Accepted || res.Duplicate {
			t.Fatalf("reseeded ingest(%d) = %+v err=%v", idx, res, err)
		}
	}
}

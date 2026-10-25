package hls

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/types"
)

// spillBackedSeed builds a ledger-valid checkpoint seed: merged prefix of
// `merged` segments plus sorted spill claims for segIdx with the given
// lengths, laid out consecutively in the arena. Returns the seed and the
// physical spill file contents.
func spillBackedSeed(total, merged int, segLens []int64, spills map[int]int64) (*types.HLSResumeState, []byte) {
	seed := testSeed(total, merged, 0, segLens)
	var file []byte
	var off int64
	idxs := make([]int, 0, len(spills))
	for idx := range spills {
		idxs = append(idxs, idx)
	}
	for i := 0; i < len(idxs); i++ {
		for j := i + 1; j < len(idxs); j++ {
			if idxs[j] < idxs[i] {
				idxs[i], idxs[j] = idxs[j], idxs[i]
			}
		}
	}
	for _, idx := range idxs {
		l := spills[idx]
		seed.SpillEntries = append(seed.SpillEntries, types.HLSSpillEntry{SegmentIndex: idx, Offset: off, Length: l})
		seed.CompletedBitmap[idx>>3] |= 1 << uint(idx&7)
		file = append(file, segPayload(byte('a'+idx), int(l))...)
		off += l
	}
	seed.SpillPath = "x.spill"
	seed.SpillFileSize = off
	seed.VerifiedBytes = seed.MergedBytes + off
	return seed, file
}

// writeSpillFile lays the physical arena bytes down at the canonical path.
func writeSpillFile(t *testing.T, dir, id string, content []byte) string {
	t.Helper()
	p := filepath.Join(dir, "."+id+".spill")
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatalf("write spill: %v", err)
	}
	return p
}

func TestValidateCheckpointSeed_Ledger(t *testing.T) {
	segLens := []int64{10, 20, 30, 40, 50}
	good := func() *types.HLSResumeState {
		s, _ := spillBackedSeed(5, 1, segLens, map[int]int64{2: 30, 4: 50})
		return s
	}
	t.Run("Valid", func(t *testing.T) {
		s := good()
		if err := ValidateCheckpointSeed(s); err != nil {
			t.Fatalf("valid seed rejected: %v", err)
		}
		if s.SpillEntries == nil || s.VerifiedBytes != 10+80 {
			t.Fatal("validator mutated the seed")
		}
	})
	t.Run("ValidPrefixOnly", func(t *testing.T) {
		if err := ValidateCheckpointSeed(testSeed(4, 1, 0, []int64{5, 6, 7, 8})); err != nil {
			t.Fatalf("prefix seed rejected: %v", err)
		}
	})
	cases := []struct {
		name   string
		mutate func(*types.HLSResumeState)
	}{
		{"entry missing path", func(s *types.HLSResumeState) { s.SpillPath = "" }},
		{"entry missing size", func(s *types.HLSResumeState) { s.SpillFileSize = 0 }},
		{"entry before frontier", func(s *types.HLSResumeState) { s.SpillEntries[0].SegmentIndex = 0 }},
		{"entry past total", func(s *types.HLSResumeState) { s.SpillEntries[1].SegmentIndex = 5 }},
		{"entry unsorted", func(s *types.HLSResumeState) {
			s.SpillEntries[0], s.SpillEntries[1] = s.SpillEntries[1], s.SpillEntries[0]
		}},
		{"entry duplicate", func(s *types.HLSResumeState) {
			s.SpillEntries[1].SegmentIndex = 2
		}},
		{"entry without bit", func(s *types.HLSResumeState) {
			s.CompletedBitmap[0] &^= 1 << 2
		}},
		{"bit without entry", func(s *types.HLSResumeState) {
			s.CompletedBitmap[0] |= 1 << 3
		}},
		{"entry negative offset", func(s *types.HLSResumeState) { s.SpillEntries[0].Offset = -1 }},
		{"entry zero length", func(s *types.HLSResumeState) { s.SpillEntries[0].Length = 0 }},
		{"entry past file", func(s *types.HLSResumeState) { s.SpillEntries[1].Offset = 70 }},
		{"entry overflow end", func(s *types.HLSResumeState) { s.SpillEntries[1].Length = 51 }},
		{"entries overlap", func(s *types.HLSResumeState) { s.SpillEntries[1].Offset = 29 }},
		{"verified overcounted", func(s *types.HLSResumeState) { s.VerifiedBytes++ }},
		{"verified undercounted", func(s *types.HLSResumeState) { s.VerifiedBytes-- }},
		{"bitmap stray bit in prefix range", func(s *types.HLSResumeState) {
			s.CompletedBitmap[0] |= 1 << 1
		}},
		{"merged counters wrong", func(s *types.HLSResumeState) { s.MergedBytes++ }},
		{"bad prefix", func(s *types.HLSResumeState) { s.CompletedBitmap[0] &^= 1 }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := good()
			tt.mutate(s)
			if err := ValidateCheckpointSeed(s); !errors.Is(err, errDirtySeed) {
				t.Fatalf("err = %v, want errDirtySeed", err)
			}
		})
	}
	// Empty-entries residual bookkeeping is rejected as well.
	for _, mutate := range []func(*types.HLSResumeState){
		func(s *types.HLSResumeState) { s.SpillPath = "x" },
		func(s *types.HLSResumeState) { s.SpillFileSize = 1 },
		func(s *types.HLSResumeState) { s.VerifiedBytes++ },
		func(s *types.HLSResumeState) { s.CompletedBitmap[0] |= 1 << 3 },
	} {
		s := testSeed(4, 1, 0, []int64{5, 6, 7, 8})
		mutate(s)
		if err := ValidateCheckpointSeed(s); !errors.Is(err, errDirtySeed) {
			t.Fatalf("residual bookkeeping accepted: %v", err)
		}
	}
}

func TestFlushPending_SpillsInIndexOrder(t *testing.T) {
	fx := newWindowFixture(t, 5, 1, 0, []int64{10, 20, 30, 40, 50}, "test-dl")
	// Pending out-of-order payloads for segments 4, 2, 3 (insertion order
	// scrambled — flush must emit arena entries in index order).
	for _, seq := range []struct {
		idx int
		b   byte
	}{{4, 'e'}, {2, 'c'}, {3, 'd'}} {
		res, err := fx.w.Ingest(seq.idx, segPayload(seq.b, int(fx.seed.Segments[seq.idx].ByteLength)))
		if err != nil || !res.Buffered {
			t.Fatalf("ingest %d: res=%+v err=%v", seq.idx, res, err)
		}
	}
	if err := fx.w.FlushPending(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	snap := fx.w.Snapshot()
	wantEntries := []types.HLSSpillEntry{
		{SegmentIndex: 2, Offset: 0, Length: 30},
		{SegmentIndex: 3, Offset: 30, Length: 40},
		{SegmentIndex: 4, Offset: 70, Length: 50},
	}
	if len(snap.SpillEntries) != len(wantEntries) {
		t.Fatalf("entries = %+v", snap.SpillEntries)
	}
	for i, e := range snap.SpillEntries {
		if e != wantEntries[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, e, wantEntries[i])
		}
	}
	if snap.SpillFileSize != 120 || snap.SpillPath == "" {
		t.Fatalf("spill bookkeeping = %q %d", snap.SpillPath, snap.SpillFileSize)
	}
	if snap.VerifiedBytes != snap.MergedBytes+120 {
		t.Fatalf("verified %d != merged %d + spill", snap.VerifiedBytes, snap.MergedBytes)
	}
	for _, idx := range []int{2, 3, 4} {
		if snap.CompletedBitmap[idx>>3]&(1<<uint(idx&7)) == 0 {
			t.Fatalf("bit %d not set after flush", idx)
		}
	}
	if err := ValidateCheckpointSeed(snap); err != nil {
		t.Fatalf("flushed snapshot fails validation: %v", err)
	}
	// Physical bytes must land in index order too.
	raw, err := os.ReadFile(fx.spillFile())
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	want := append(append(segPayload('c', 30), segPayload('d', 40)...), segPayload('e', 50)...)
	if !bytes.Equal(raw, want) {
		t.Fatal("spill file content not in index order")
	}
	// Idempotent second flush keeps the snapshot identical.
	if err := fx.w.FlushPending(); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if snap2 := fx.w.Snapshot(); snap2.VerifiedBytes != snap.VerifiedBytes ||
		len(snap2.SpillEntries) != len(snap.SpillEntries) {
		t.Fatal("second flush changed the snapshot")
	}
}

func TestFlushPending_DrainsAfterFrontierFills(t *testing.T) {
	fx := newWindowFixture(t, 4, 1, 0, []int64{10, 20, 30, 40}, "test-dl")
	if res, err := fx.w.Ingest(2, segPayload('c', 30)); err != nil || !res.Buffered {
		t.Fatalf("ingest 2: %+v %v", res, err)
	}
	if res, err := fx.w.Ingest(3, segPayload('d', 40)); err != nil || !res.Buffered {
		t.Fatalf("ingest 3: %+v %v", res, err)
	}
	if err := fx.w.FlushPending(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	// Filling the frontier gap (segment 1) drains the whole spilled chain.
	if res, err := fx.w.Ingest(1, segPayload('b', 20)); err != nil || !res.Direct {
		t.Fatalf("ingest 1: %+v %v", res, err)
	}
	snap := fx.w.Snapshot()
	if snap.MergedSegments != 4 || snap.MergedBytes != 100 || snap.VerifiedBytes != 100 {
		t.Fatalf("after drain: %+v", snap)
	}
	if len(snap.SpillEntries) != 0 || snap.SpillFileSize != 0 || snap.SpillPath != "" {
		t.Fatal("arena not fully drained")
	}
	out := fx.readOut(t)
	want := append(append(append(segPayload('a', 10), segPayload('b', 20)...), segPayload('c', 30)...), segPayload('d', 40)...)
	if !bytes.Equal(out, want) {
		t.Fatal("merged output mismatch")
	}
}

func TestFlushPending_ClosedAndLatched(t *testing.T) {
	fx := newWindowFixture(t, 3, 1, 0, []int64{10, 20, 30}, "test-dl")
	fx.w.Close()
	if err := fx.w.FlushPending(); !errors.Is(err, errWindowClosed) {
		t.Fatalf("closed flush = %v", err)
	}

	// A latched storage fault rejects the flush and keeps the payloads.
	fx2 := newWindowFixture(t, 3, 1, 0, []int64{10, 20, 30}, "test-dl")
	boom := errors.New("disk gone")
	fx2.ops.open = func(string, int, os.FileMode) (*os.File, error) { return nil, boom }
	if res, err := fx2.w.Ingest(2, segPayload('c', 30)); err != nil || !res.Buffered {
		t.Fatalf("ingest: %+v %v", res, err)
	}
	// Force the arena path by overflowing the pending byte budget instead —
	// simpler: latch storeErr via a failed flush attempt with a bad opener.
	fx2.ops.open = func(string, int, os.FileMode) (*os.File, error) { return nil, boom }
	if err := fx2.w.FlushPending(); err == nil {
		t.Fatal("flush succeeded with broken opener")
	}
	if err := fx2.w.FlushPending(); err == nil {
		t.Fatal("second flush not latched")
	}
}

func TestNewReorderWindow_AdoptsSpillLedger(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.surge")
	segLens := []int64{10, 20, 30, 40, 50}
	seed, spillBytes := spillBackedSeed(5, 1, segLens, map[int]int64{2: 30, 4: 50})
	writeSpillFile(t, dir, "dl-adopt", spillBytes)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.WriteAt(segPayload('a', 10), 0); err != nil {
		t.Fatal(err)
	}
	if err := out.Truncate(10); err != nil {
		t.Fatal(err)
	}
	w, err := newReorderWindow(out, path, "dl-adopt", seed, nil)
	if err != nil {
		t.Fatalf("adopt rejected: %v", err)
	}
	defer w.Close()
	defer out.Close()
	snap := w.Snapshot()
	if snap.VerifiedBytes != 10+80 || len(snap.SpillEntries) != 2 || snap.SpillFileSize != 80 {
		t.Fatalf("adopted snapshot = %+v", snap)
	}
	if snap.SpillPath != filepath.Join(dir, ".dl-adopt.spill") {
		t.Fatalf("spill path not canonicalized: %q", snap.SpillPath)
	}
	// Claimed segments dedup; the remaining gap fills and drains the arena.
	for _, idx := range []int{2, 4} {
		res, err := w.Ingest(idx, segPayload('z', 1))
		if err != nil || !res.Duplicate {
			t.Fatalf("claimed segment %d not deduped: %+v %v", idx, res, err)
		}
	}
	if res, err := w.Ingest(1, segPayload('b', 20)); err != nil || !res.Direct {
		t.Fatalf("ingest 1: %+v %v", res, err)
	}
	snap = w.Snapshot()
	// 2 drains; 3 unclaimed; 4 still spilled.
	if snap.MergedSegments != 3 || snap.MergedBytes != 60 || snap.VerifiedBytes != 60+50 {
		t.Fatalf("post-drain snapshot = %+v", snap)
	}
	if len(snap.SpillEntries) != 1 || snap.SpillEntries[0].SegmentIndex != 4 {
		t.Fatalf("remaining entries = %+v", snap.SpillEntries)
	}
	out2, _ := os.ReadFile(path)
	want := append(append(segPayload('a', 10), segPayload('b', 20)...), segPayload('c', 30)...)
	if !bytes.Equal(out2, want) {
		t.Fatal("replayed bytes do not match original payload")
	}
	if _, err := w.Ingest(3, segPayload('d', 40)); err != nil {
		t.Fatal(err)
	}
	snap = w.Snapshot()
	if snap.MergedSegments != 5 || snap.VerifiedBytes != 150 || len(snap.SpillEntries) != 0 {
		t.Fatalf("final = %+v", snap)
	}
}

func TestNewReorderWindow_FrontierEntryReplaysAtOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.surge")
	segLens := []int64{10, 20, 30}
	seed, spillBytes := spillBackedSeed(3, 1, segLens, map[int]int64{1: 20, 2: 30})
	writeSpillFile(t, dir, "dl-frontier", spillBytes)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.WriteAt(segPayload('a', 10), 0); err != nil {
		t.Fatal(err)
	}
	if err := out.Truncate(10); err != nil {
		t.Fatal(err)
	}
	w, err := newReorderWindow(out, path, "dl-frontier", seed, nil)
	if err != nil {
		t.Fatalf("adopt rejected: %v", err)
	}
	defer w.Close()
	defer out.Close()
	snap := w.Snapshot()
	// The entire chain replayed during construction — no entries remain.
	if snap.MergedSegments != 3 || snap.MergedBytes != 60 || snap.VerifiedBytes != 60 {
		t.Fatalf("post-replay = %+v", snap)
	}
	if len(snap.SpillEntries) != 0 || snap.SpillFileSize != 0 {
		t.Fatalf("arena should be empty: %+v", snap)
	}
	raw, _ := os.ReadFile(path)
	want := append(append(segPayload('a', 10), segPayload('b', 20)...), segPayload('c', 30)...)
	if !bytes.Equal(raw, want) {
		t.Fatal("frontier replay content mismatch")
	}
}

func TestNewReorderWindow_MissingSpillDemotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.surge")
	segLens := []int64{10, 20, 30}
	seed, _ := spillBackedSeed(3, 1, segLens, map[int]int64{2: 30})
	out, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.WriteAt(segPayload('a', 10), 0); err != nil {
		t.Fatal(err)
	}
	if err := out.Truncate(10); err != nil {
		t.Fatal(err)
	}
	w, err := newReorderWindow(out, path, "dl-miss", seed, nil)
	if err != nil {
		t.Fatalf("missing arena must demote, not fail: %v", err)
	}
	defer w.Close()
	defer out.Close()
	snap := w.Snapshot()
	if snap.MergedSegments != 1 || snap.VerifiedBytes != 10 || len(snap.SpillEntries) != 0 {
		t.Fatalf("demoted snapshot = %+v", snap)
	}
	if snap.CompletedBitmap[0]&(1<<2) != 0 {
		t.Fatal("demotion left a stale claim bit")
	}
	// The dropped claim is now fetchable — no false dedup.
	res, err := w.Ingest(2, segPayload('c', 30))
	if err != nil || !res.Accepted || res.Duplicate {
		t.Fatalf("refetch rejected: %+v %v", res, err)
	}
}

func TestNewReorderWindow_ShortSpillDemotesAndRemoves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.surge")
	segLens := []int64{10, 20, 30}
	seed, spillBytes := spillBackedSeed(3, 1, segLens, map[int]int64{2: 30})
	spill := writeSpillFile(t, dir, "dl-short", spillBytes[:10]) // truncated physical file
	out, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.WriteAt(segPayload('a', 10), 0); err != nil {
		t.Fatal(err)
	}
	if err := out.Truncate(10); err != nil {
		t.Fatal(err)
	}
	w, err := newReorderWindow(out, path, "dl-short", seed, nil)
	if err != nil {
		t.Fatalf("short arena must demote, not fail: %v", err)
	}
	defer w.Close()
	defer out.Close()
	if snap := w.Snapshot(); snap.VerifiedBytes != 10 || len(snap.SpillEntries) != 0 {
		t.Fatalf("demoted snapshot = %+v", snap)
	}
	if _, err := os.Lstat(spill); !os.IsNotExist(err) {
		t.Fatalf("short arena not removed: %v", err)
	}
}

func TestNewReorderWindow_LongSpillTruncatesDirtyTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.surge")
	segLens := []int64{10, 20, 30}
	seed, spillBytes := spillBackedSeed(3, 1, segLens, map[int]int64{2: 30})
	spill := writeSpillFile(t, dir, "dl-long", append(spillBytes, 0xAA, 0xBB)) // crash-dirty tail
	out, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.WriteAt(segPayload('a', 10), 0); err != nil {
		t.Fatal(err)
	}
	if err := out.Truncate(10); err != nil {
		t.Fatal(err)
	}
	w, err := newReorderWindow(out, path, "dl-long", seed, nil)
	if err != nil {
		t.Fatalf("dirty tail should truncate and adopt: %v", err)
	}
	defer w.Close()
	defer out.Close()
	if snap := w.Snapshot(); snap.VerifiedBytes != 40 || len(snap.SpillEntries) != 1 {
		t.Fatalf("adopted snapshot = %+v", snap)
	}
	fi, _ := os.Stat(spill)
	if fi.Size() != 30 {
		t.Fatalf("dirty tail not truncated: %d", fi.Size())
	}
}

func TestNewReorderWindow_NonRegularSpillFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.surge")
	segLens := []int64{10, 20, 30}
	seed, _ := spillBackedSeed(3, 1, segLens, map[int]int64{2: 30})
	if err := os.Mkdir(filepath.Join(dir, ".dl-dir.spill"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.WriteAt(segPayload('a', 10), 0); err != nil {
		t.Fatal(err)
	}
	if err := out.Truncate(10); err != nil {
		t.Fatal(err)
	}
	if _, err := newReorderWindow(out, path, "dl-dir", seed, nil); err == nil {
		t.Fatal("non-regular spill adopted")
	} else {
		if _, ok := errors.AsType[*storageError](err); !ok {
			t.Fatalf("want storageError, got %v", err)
		}
	}
	out.Close()
}

func TestSnapshot_IsolatesArenaAndBitmap(t *testing.T) {
	fx := newWindowFixture(t, 3, 1, 0, []int64{10, 20, 30}, "test-dl")
	if res, err := fx.w.Ingest(2, segPayload('c', 30)); err != nil || !res.Buffered {
		t.Fatalf("ingest: %+v %v", res, err)
	}
	if err := fx.w.FlushPending(); err != nil {
		t.Fatal(err)
	}
	snap := fx.w.Snapshot()
	snap.SpillEntries[0].Length = 9999
	snap.CompletedBitmap[0] = 0
	snap2 := fx.w.Snapshot()
	if snap2.SpillEntries[0].Length != 30 || snap2.CompletedBitmap[0]&(1<<2) == 0 {
		t.Fatal("snapshot shares mutable state with the window")
	}
}

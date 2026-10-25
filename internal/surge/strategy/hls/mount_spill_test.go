package hls

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/types"
)

// mountSpillFixture lays a merged prefix on the .surge plus a physical
// canonical spill arena, and returns a seed whose ledger claims them.
func mountSpillFixture(t *testing.T, o *runOrigin, id string, mergedPrefix []byte, spills map[int][]byte) (*types.HLSResumeState, string, string) {
	t.Helper()
	dir := t.TempDir()
	dest := filepath.Join(dir, "v.ts")
	surgePath := dest + types.IncompleteSuffix
	if err := os.WriteFile(surgePath, mergedPrefix, 0o644); err != nil {
		t.Fatal(err)
	}
	seed := &types.HLSResumeState{
		ManifestURL:      o.url("/media.m3u8"),
		MediaPlaylistURL: o.url("/media.m3u8"),
		TotalSegments:    3,
		CompletedBitmap:  make([]byte, 1),
		MergedSegments:   1,
		MergedBytes:      int64(len(mergedPrefix)),
		VerifiedBytes:    int64(len(mergedPrefix)),
		Segments: []types.HLSSegmentState{
			{Index: 0, URI: o.url("/s0.ts")},
			{Index: 1, URI: o.url("/s1.ts")},
			{Index: 2, URI: o.url("/s2.ts")},
		},
	}
	seed.CompletedBitmap[0] |= 1
	var arena []byte
	// Emit claims in segment-index order — the same determinism
	// SortedEntries guarantees at checkpoint time.
	for _, idx := range []int{1, 2} {
		payload, ok := spills[idx]
		if !ok {
			continue
		}
		seed.SpillEntries = append(seed.SpillEntries, types.HLSSpillEntry{
			SegmentIndex: idx, Offset: int64(len(arena)), Length: int64(len(payload)),
		})
		seed.CompletedBitmap[0] |= 1 << uint(idx)
		arena = append(arena, payload...)
		seed.VerifiedBytes += int64(len(payload))
	}
	var spillPath string
	if len(seed.SpillEntries) > 0 {
		seed.SpillFileSize = int64(len(arena))
		seed.SpillPath = "/forged/elsewhere.spill" // must be ignored — canonical path only
		spillPath = spillPathFor(surgePath, id)
		if err := os.WriteFile(spillPath, arena, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return seed, dest, spillPath
}

func TestHLSMount_AdoptsValidatedLedger(t *testing.T) {
	o := newRunOrigin(t)
	prefix := []byte("seg0-bytes")
	seed, dest, spillPath := mountSpillFixture(t, o, "mnt-adopt", prefix, map[int][]byte{2: []byte("seg2-bytes!")})
	d := mountDownloader(t, "mnt-adopt", o.srv)
	mnt, err := d.mount(context.Background(), dest, seed)
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if len(mnt.seed.SpillEntries) != 1 || mnt.seed.SpillEntries[0].SegmentIndex != 2 {
		t.Fatalf("ledger not adopted: %+v", mnt.seed)
	}
	if mnt.seed.SpillPath != spillPath {
		t.Fatalf("stored path %q must be replaced by canonical %q", mnt.seed.SpillPath, spillPath)
	}
	if mnt.seed.VerifiedBytes != int64(len(prefix)+len("seg2-bytes!")) {
		t.Fatalf("verified = %d", mnt.seed.VerifiedBytes)
	}
	// No fetch happened this run — the whole verified set is history.
	if mnt.resumeBaseline != mnt.seed.VerifiedBytes {
		t.Fatalf("baseline = %d, want adopted history %d", mnt.resumeBaseline, mnt.seed.VerifiedBytes)
	}
	if _, err := os.Stat(spillPath); err != nil {
		t.Fatalf("adopted arena missing: %v", err)
	}
}

func TestHLSMount_LedgerWithoutFileDemotesLocally(t *testing.T) {
	o := newRunOrigin(t)
	prefix := []byte("seg0-bytes")
	seed, dest, spillPath := mountSpillFixture(t, o, "mnt-gone", prefix, map[int][]byte{2: []byte("seg2-bytes!")})
	if err := os.Remove(spillPath); err != nil {
		t.Fatal(err)
	}
	d := mountDownloader(t, "mnt-gone", o.srv)
	mnt, err := d.mount(context.Background(), dest, seed)
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	// Prefix preserved, only the unverifiable claims dropped.
	if mnt.seed.MergedSegments != 1 || mnt.seed.VerifiedBytes != int64(len(prefix)) {
		t.Fatalf("demoted seed = %+v", mnt.seed)
	}
	if len(mnt.seed.SpillEntries) != 0 || mnt.seed.SpillPath != "" || mnt.seed.SpillFileSize != 0 {
		t.Fatalf("dead claims survived: %+v", mnt.seed)
	}
	if mnt.seed.CompletedBitmap[0]&(1<<2) != 0 {
		t.Fatal("spilled bit survived physical demotion")
	}
	// And the file prefix is untouched — the segment re-fetches normally.
	got, _ := os.ReadFile(dest + types.IncompleteSuffix)
	if !bytes.Equal(got, prefix) {
		t.Fatal("prefix corrupted by demotion")
	}
}

func TestHLSMount_InvalidLedgerDemotesAndRemoves(t *testing.T) {
	o := newRunOrigin(t)
	prefix := []byte("seg0-bytes")
	seed, dest, spillPath := mountSpillFixture(t, o, "mnt-badledger", prefix, map[int][]byte{2: []byte("seg2-bytes!")})
	// Corrupt the ledger: verified count no longer matches the entries.
	seed.VerifiedBytes -= 1
	d := mountDownloader(t, "mnt-badledger", o.srv)
	mnt, err := d.mount(context.Background(), dest, seed)
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if len(mnt.seed.SpillEntries) != 0 || mnt.seed.SpillPath != "" {
		t.Fatalf("invalid ledger adopted: %+v", mnt.seed)
	}
	if mnt.seed.MergedSegments != 1 || mnt.seed.VerifiedBytes != int64(len(prefix)) {
		t.Fatalf("prefix lost during demotion: %+v", mnt.seed)
	}
	if _, err := os.Stat(spillPath); !os.IsNotExist(err) {
		t.Fatal("stale arena survived an invalid ledger")
	}
}

func TestHLSMount_FrontierEntryReplaysBeforeEngineSeesSeed(t *testing.T) {
	o := newRunOrigin(t)
	prefix := []byte("seg0-bytes")
	// Entry at the merged frontier (segment 1) must be replayed at open —
	// otherwise a queue that skips claimed bits would finish with a hole.
	seed, dest, _ := mountSpillFixture(t, o, "mnt-frontier", prefix, map[int][]byte{1: []byte("seg1-bytes!"), 2: []byte("seg2-bytes!")})
	d := mountDownloader(t, "mnt-frontier", o.srv)
	mnt, err := d.mount(context.Background(), dest, seed)
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if mnt.seed.MergedSegments != 3 || mnt.seed.VerifiedBytes != int64(len(prefix))+22 {
		t.Fatalf("frontier replay not reflected in seed: %+v", mnt.seed)
	}
	if len(mnt.seed.SpillEntries) != 0 || mnt.seed.SpillFileSize != 0 {
		t.Fatalf("arena should be drained: %+v", mnt.seed)
	}
	got, _ := os.ReadFile(dest + types.IncompleteSuffix)
	want := append(append([]byte{}, prefix...), []byte("seg1-bytes!seg2-bytes!")...)
	if !bytes.Equal(got, want) {
		t.Fatal("replayed arena bytes wrong")
	}
}

func TestHLSMount_UnclaimedGapLeavesEntryPending(t *testing.T) {
	o := newRunOrigin(t)
	prefix := []byte("seg0-bytes")
	// Segment 2 spilled while 1 is still unclaimed: the frontier cannot
	// advance, so the arena keeps the entry for the later drain.
	seed, dest, _ := mountSpillFixture(t, o, "mnt-gap", prefix, map[int][]byte{2: []byte("seg2-bytes!")})
	d := mountDownloader(t, "mnt-gap", o.srv)
	mnt, err := d.mount(context.Background(), dest, seed)
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if len(mnt.seed.SpillEntries) != 1 || mnt.seed.SpillEntries[0].SegmentIndex != 2 {
		t.Fatalf("entry lost: %+v", mnt.seed)
	}
	if mnt.seed.VerifiedBytes != int64(len(prefix))+11 {
		t.Fatalf("verified = %d", mnt.seed.VerifiedBytes)
	}
}

package hls

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/types"
)

// mountDownloader builds a minimal downloader for direct mount() calls —
// run() normally populates runCfg/taskHeaders.
func mountDownloader(t *testing.T, id string, srv *httptest.Server) *HlsDownloader {
	t.Helper()
	d := NewHlsDownloader(id, nil, nil, nil)
	d.runCfg = types.RuntimeConfig{}
	if srv != nil {
		d.Client = srv.Client()
	}
	return d
}

// mountSeed builds a 2-segment table whose URIs point at the origin;
// tweak adjusts progress claims before the call.
func mountSeed(t *testing.T, o *runOrigin, tweak func(*types.HLSResumeState)) *types.HLSResumeState {
	t.Helper()
	s := &types.HLSResumeState{
		ManifestURL:      o.url("/media.m3u8"),
		MediaPlaylistURL: o.url("/media.m3u8"),
		TotalSegments:    2,
		CompletedBitmap:  []byte{0x00},
		Segments: []types.HLSSegmentState{
			{Index: 0, URI: o.url("/s0.ts")},
			{Index: 1, URI: o.url("/s1.ts")},
		},
	}
	if tweak != nil {
		tweak(s)
	}
	return s
}

func TestHLSMount_FreshRunTruncatesDirtyTail(t *testing.T) {
	o := newRunOrigin(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "v.ts")
	if err := os.WriteFile(dest+types.IncompleteSuffix, []byte("stale garbage tail"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := mountDownloader(t, "mnt-fresh", o.srv)
	mnt, err := d.mount(context.Background(), dest, mountSeed(t, o, nil))
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if fi, _ := mnt.out.Stat(); fi.Size() != 0 {
		t.Fatalf("file size = %d, want dirty tail truncated to 0", fi.Size())
	}
	if mnt.seed.MergedSegments != 0 || mnt.seed.MergedBytes != 0 {
		t.Fatalf("seed advanced on a fresh mount: %+v", mnt.seed)
	}
}

func TestHLSMount_PrefixPreservedAndTailDiscarded(t *testing.T) {
	o := newRunOrigin(t)
	prefix := []byte("seg0-bytes")
	dir := t.TempDir()
	dest := filepath.Join(dir, "v.ts")
	// File holds the claimed prefix plus an uncommitted tail — the tail is
	// never trusted, only the provable merged bytes survive.
	if err := os.WriteFile(dest+types.IncompleteSuffix, append(append([]byte{}, prefix...), []byte("junk-tail")...), 0o644); err != nil {
		t.Fatal(err)
	}
	d := mountDownloader(t, "mnt-prefix", o.srv)
	mnt, err := d.mount(context.Background(), dest, mountSeed(t, o, func(s *types.HLSResumeState) {
		s.CompletedBitmap = []byte{0x01}
		s.MergedSegments = 1
		s.MergedBytes = int64(len(prefix))
		s.VerifiedBytes = s.MergedBytes
	}))
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if fi, _ := mnt.out.Stat(); fi.Size() != int64(len(prefix)) {
		t.Fatalf("file size = %d, want prefix %d", fi.Size(), len(prefix))
	}
	if mnt.seed.MergedSegments != 1 || mnt.seed.VerifiedBytes != int64(len(prefix)) {
		t.Fatalf("prefix claim dropped: %+v", mnt.seed)
	}
	got, _ := os.ReadFile(dest + types.IncompleteSuffix)
	if string(got) != string(prefix) {
		t.Fatalf("file head = %q, want %q", got, prefix)
	}
}

func TestHLSMount_ShortFileResetsClaimedPrefix(t *testing.T) {
	o := newRunOrigin(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "v.ts")
	// Claim says 10 merged bytes; the file only backs 4 — unprovable.
	if err := os.WriteFile(dest+types.IncompleteSuffix, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := mountDownloader(t, "mnt-short", o.srv)
	mnt, err := d.mount(context.Background(), dest, mountSeed(t, o, func(s *types.HLSResumeState) {
		s.CompletedBitmap = []byte{0x01}
		s.MergedSegments = 1
		s.MergedBytes = 10
		s.VerifiedBytes = 10
	}))
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if mnt.seed.MergedSegments != 0 || mnt.seed.MergedBytes != 0 || mnt.seed.VerifiedBytes != 0 {
		t.Fatalf("claim survived a short file: %+v", mnt.seed)
	}
	if mnt.seed.CompletedBitmap[0] != 0 {
		t.Fatalf("bitmap not cleared: %#x", mnt.seed.CompletedBitmap[0])
	}
	if fi, _ := mnt.out.Stat(); fi.Size() != 0 {
		t.Fatalf("file size = %d, want 0", fi.Size())
	}
}

func TestHLSMount_SpillClaimsDemotedAndArenaRemoved(t *testing.T) {
	o := newRunOrigin(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "v.ts")
	surgePath := dest + types.IncompleteSuffix
	if err := os.WriteFile(surgePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	arena := spillPathFor(surgePath, "mnt-spill")
	if err := os.WriteFile(arena, []byte("spill data"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := mountDownloader(t, "mnt-spill", o.srv)
	mnt, err := d.mount(context.Background(), dest, mountSeed(t, o, func(s *types.HLSResumeState) {
		// Legacy spill claims: a bit beyond the merged frontier plus the
		// bookkeeping fields — all untrusted in this slice.
		s.CompletedBitmap = []byte{0x02}
		s.SpillPath = arena
		s.SpillFileSize = 10
		s.SpillEntries = []types.HLSSpillEntry{{SegmentIndex: 1, Offset: 0, Length: 10}}
	}))
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if mnt.seed.SpillPath != "" || mnt.seed.SpillFileSize != 0 || len(mnt.seed.SpillEntries) != 0 {
		t.Fatalf("spill bookkeeping survived mount: %+v", mnt.seed)
	}
	if mnt.seed.CompletedBitmap[0]&0x02 != 0 {
		t.Fatalf("spilled bit still set: %#x", mnt.seed.CompletedBitmap[0])
	}
	if _, err := os.Stat(arena); !os.IsNotExist(err) {
		t.Fatalf("stale arena still present (O_EXCL lockout on next run)")
	}
}

func TestHLSMount_ZeroProgressStillRemovesStaleArena(t *testing.T) {
	o := newRunOrigin(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "v.ts")
	arena := spillPathFor(dest+types.IncompleteSuffix, "mnt-arena")
	if err := os.WriteFile(arena, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := mountDownloader(t, "mnt-arena", o.srv)
	mnt, err := d.mount(context.Background(), dest, mountSeed(t, o, nil))
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if _, err := os.Stat(arena); !os.IsNotExist(err) {
		t.Fatal("stale arena survived a zero-progress mount")
	}
}

func TestHLSMount_DirtySeedSingleControlledFallback(t *testing.T) {
	o := newRunOrigin(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "v.ts")
	// 100 bytes on disk, sane-looking counters, but the merged claim exceeds
	// every declared segment bound — sanity passes, the window contract
	// refuses, and exactly one fallback resets it.
	if err := os.WriteFile(dest+types.IncompleteSuffix, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	d := mountDownloader(t, "mnt-dirty", o.srv)
	mnt, err := d.mount(context.Background(), dest, mountSeed(t, o, func(s *types.HLSResumeState) {
		s.CompletedBitmap = []byte{0x01}
		s.MergedSegments = 1
		s.MergedBytes = 100
		s.VerifiedBytes = 100
		s.Segments[0].ByteLength = 10
	}))
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if mnt.seed.MergedSegments != 0 || mnt.seed.MergedBytes != 0 {
		t.Fatalf("dirty seed kept a prefix: %+v", mnt.seed)
	}
	if fi, _ := mnt.out.Stat(); fi.Size() != 0 {
		t.Fatalf("file size = %d after fallback, want 0", fi.Size())
	}
	if mnt.resumeBaseline != 0 {
		t.Fatalf("resumeBaseline = %d after dirty fallback — a fresh start must not look like a resume", mnt.resumeBaseline)
	}
}

func TestHLSMount_BadSeedIsContractError(t *testing.T) {
	o := newRunOrigin(t)
	d := mountDownloader(t, "mnt-badseed", o.srv)
	dest := filepath.Join(t.TempDir(), "v.ts")
	if _, err := d.mount(context.Background(), dest, nil); err == nil {
		t.Fatal("mount accepted a nil seed")
	}
	bad := mountSeed(t, o, func(s *types.HLSResumeState) {
		s.Segments[0].Index = 7 // descriptor out of order
	})
	if _, err := d.mount(context.Background(), dest, bad); err == nil {
		t.Fatal("mount accepted an out-of-order segment table")
	}
}

func TestHLSMount_StorageErrorPropagates(t *testing.T) {
	o := newRunOrigin(t)
	d := mountDownloader(t, "mnt-io", o.srv)
	// Destination inside a directory that does not exist — OpenFile fails.
	dest := filepath.Join(t.TempDir(), "missing", "v.ts")
	_, err := d.mount(context.Background(), dest, mountSeed(t, o, nil))
	if err == nil {
		t.Fatal("mount succeeded on an unreachable destination")
	}
	if _, ok := errors.AsType[*storageError](err); !ok {
		t.Fatalf("err = %v (%T), want a storage error", err, err)
	}
}

func TestHLSMount_InitFetchedAndAnchored(t *testing.T) {
	o := newRunOrigin(t)
	init := []byte("init-head")
	o.serveBody("/init.mp4", init)
	dir := t.TempDir()
	dest := filepath.Join(dir, "v.mp4")
	d := mountDownloader(t, "mnt-init", o.srv)
	mnt, err := d.mount(context.Background(), dest, mountSeed(t, o, func(s *types.HLSResumeState) {
		s.InitSegmentURI = o.url("/init.mp4")
		s.ContainerFormat = "fmp4"
	}))
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if n := o.requests.count("/init.mp4"); n != 1 {
		t.Fatalf("init fetched %d times, want 1", n)
	}
	if !mnt.seed.InitCompleted || mnt.seed.InitBytes != int64(len(init)) ||
		mnt.seed.MergedBytes != int64(len(init)) {
		t.Fatalf("init accounting = %+v", mnt.seed)
	}
	got, _ := os.ReadFile(dest + types.IncompleteSuffix)
	if string(got) != string(init) {
		t.Fatalf("file head = %q, want %q", got, init)
	}
	if mnt.resumeBaseline != 0 {
		t.Fatalf("resumeBaseline = %d, want 0 (fresh init proves nothing prior)", mnt.resumeBaseline)
	}
}

func TestHLSMount_InitPrefixReusedWithoutRefetch(t *testing.T) {
	o := newRunOrigin(t)
	init := []byte("init-head")
	seg0 := []byte("seg0")
	o.serveBody("/init.mp4", init)
	dir := t.TempDir()
	dest := filepath.Join(dir, "v.mp4")
	if err := os.WriteFile(dest+types.IncompleteSuffix, append(append([]byte{}, init...), seg0...), 0o644); err != nil {
		t.Fatal(err)
	}
	d := mountDownloader(t, "mnt-initok", o.srv)
	mnt, err := d.mount(context.Background(), dest, mountSeed(t, o, func(s *types.HLSResumeState) {
		s.InitSegmentURI = o.url("/init.mp4")
		s.InitCompleted = true
		s.InitBytes = int64(len(init))
		s.CompletedBitmap = []byte{0x01}
		s.MergedSegments = 1
		s.MergedBytes = int64(len(init) + len(seg0))
		s.VerifiedBytes = s.MergedBytes
	}))
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if n := o.requests.count("/init.mp4"); n != 0 {
		t.Fatalf("backed init refetched %d times", n)
	}
	if mnt.seed.MergedSegments != 1 || mnt.resumeBaseline != int64(len(init)+len(seg0)) {
		t.Fatalf("prefix/baseline lost: seed=%+v baseline=%d", mnt.seed, mnt.resumeBaseline)
	}
}

func TestHLSMount_UnbackedInitRefetched(t *testing.T) {
	o := newRunOrigin(t)
	init := []byte("re-init")
	o.serveBody("/init.mp4", init)
	dir := t.TempDir()
	dest := filepath.Join(dir, "v.mp4")
	// Claimed init+seg0 prefix but the file is short — everything resets,
	// then init is refetched cleanly onto a zeroed file.
	if err := os.WriteFile(dest+types.IncompleteSuffix, []byte("xy"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := mountDownloader(t, "mnt-initre", o.srv)
	mnt, err := d.mount(context.Background(), dest, mountSeed(t, o, func(s *types.HLSResumeState) {
		s.InitSegmentURI = o.url("/init.mp4")
		s.InitCompleted = true
		s.InitBytes = 7
		s.CompletedBitmap = []byte{0x01}
		s.MergedSegments = 1
		s.MergedBytes = 11
		s.VerifiedBytes = 11
	}))
	if err != nil {
		t.Fatalf("mount = %v", err)
	}
	defer func() {
		_ = mnt.window.Close()
		_ = mnt.out.Close()
	}()
	if n := o.requests.count("/init.mp4"); n != 1 {
		t.Fatalf("init refetched %d times, want 1", n)
	}
	if mnt.seed.MergedSegments != 0 || mnt.seed.MergedBytes != int64(len(init)) {
		t.Fatalf("unbacked prefix survived: %+v", mnt.seed)
	}
	got, _ := os.ReadFile(dest + types.IncompleteSuffix)
	if string(got) != string(init) {
		t.Fatalf("file = %q, want only the refetched init %q", got, init)
	}
}

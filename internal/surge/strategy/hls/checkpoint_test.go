package hls

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

func TestHLSCheckpoint_FirstSegmentPersisted(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	gate := make(chan struct{})
	seg0, seg1 := []byte("seg-0"), []byte("seg-1")
	o.serveBody("/s0.ts", seg0)
	o.mux.HandleFunc("/s1.ts", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write(seg1)
	})
	manifestURL := o.url("/media.m3u8")
	o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts", "s1.ts"})

	const id = "ckpt-first"
	d := newTestDownloader(id, nil, nil)
	d.Client = o.srv.Client()
	dest := filepath.Join(t.TempDir(), "v.ts")
	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background(), manifestURL, dest) }()

	// The hard checkpoint lands before fan-out opens: poll the detail store
	// for the durable record while s1 is still blocked.
	deadline := time.Now().Add(5 * time.Second)
	var rec *types.DownloadRecord
	for {
		states, _ := store.LoadStates([]string{id})
		rec = states[id]
		if rec != nil && rec.HLSResume != nil && rec.HLSResume.MergedSegments >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("durable checkpoint never persisted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rec.Strategy != types.StrategyHLS {
		t.Fatalf("checkpoint Strategy = %q", rec.Strategy)
	}
	if rec.URL != manifestURL {
		t.Fatalf("checkpoint URL = %q", rec.URL)
	}
	if rec.DestPath != dest {
		t.Fatalf("checkpoint DestPath = %q", rec.DestPath)
	}
	if rec.Downloaded != int64(len(seg0)) {
		t.Fatalf("checkpoint Downloaded = %d, want %d", rec.Downloaded, len(seg0))
	}
	if rec.TotalSize != 0 {
		t.Fatalf("checkpoint TotalSize = %d, want 0 (unknown until completion)", rec.TotalSize)
	}
	if rec.FileHash != "" {
		t.Fatalf("HLS checkpoint stored a byte-range file hash %q", rec.FileHash)
	}
	hr := rec.HLSResume
	if err := validateSeed(hr); err != nil {
		t.Fatalf("persisted checkpoint is not a valid prefix seed: %v", err)
	}
	if hr.MergedSegments != 1 || hr.CompletedBitmap[0]&0x01 == 0 {
		t.Fatalf("checkpoint prefix = %+v", hr)
	}
	if hr.VerifiedBytes != hr.MergedBytes {
		t.Fatalf("checkpoint verified %d != merged %d", hr.VerifiedBytes, hr.MergedBytes)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}

func TestHLSCheckpoint_ErrorPathSavesDurablePrefix(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	o.serveBody("/s0.ts", []byte("seg-0"))
	// An encoded segment body is a permanent protocol violation — the run
	// fails at the second segment while the first is already merged.
	o.mux.HandleFunc("/s1.ts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write([]byte("seg-1"))
	})
	o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts", "s1.ts"})

	const id = "ckpt-err"
	d := newTestDownloader(id, nil, nil)
	d.Client = o.srv.Client()
	err := d.Run(context.Background(), o.url("/media.m3u8"), filepath.Join(t.TempDir(), "v.ts"))
	if err == nil {
		t.Fatal("run succeeded on an invalid segment response")
	}
	snap := d.ResumeSnapshot()
	if snap == nil || snap.MergedSegments != 1 {
		t.Fatalf("durable snapshot = %+v, want a 1-segment prefix", snap)
	}
	states, _ := store.LoadStates([]string{id})
	rec := states[id]
	if rec == nil || rec.HLSResume == nil || rec.HLSResume.MergedSegments != 1 {
		t.Fatalf("error-path checkpoint missing: %+v", rec)
	}
	if rec.Downloaded != int64(len("seg-0")) {
		t.Fatalf("checkpoint Downloaded = %d", rec.Downloaded)
	}
}

func TestHLSCheckpoint_StoreFailureIsFatal(t *testing.T) {
	// No store.Configure — the first accepted segment cannot checkpoint, so
	// fan-out must never open: s1 stays unfetched and no FirstByte fires.
	store.CloseDB()
	t.Cleanup(store.CloseDB)
	o := newRunOrigin(t)
	o.serveBody("/s0.ts", []byte("s0"))
	o.serveBody("/s1.ts", []byte("s1"))
	o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts", "s1.ts"})

	ch := make(chan types.DownloadEvent, 8)
	d := NewHlsDownloader("ckpt-nostore", ch, progress.New("ckpt-nostore", 0), types.DefaultRuntimeConfig())
	d.Client = o.srv.Client()
	err := d.Run(context.Background(), o.url("/media.m3u8"), filepath.Join(t.TempDir(), "v.ts"))
	if err == nil {
		t.Fatal("run succeeded without a checkpoint store")
	}
	if n := o.requests.count("/s1.ts"); n != 0 {
		t.Fatalf("fan-out opened after checkpoint failure: s1 fetched %d times", n)
	}
	if n := countEvent(collectEvents(ch, "ckpt-nostore", 50*time.Millisecond), types.EventFirstByte); n != 0 {
		t.Fatalf("FirstByte emitted %d times though the hard checkpoint failed", n)
	}
}

func TestHLSCheckpoint_ResumeSnapshotIsIsolated(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	o.serveBody("/s0.ts", []byte("s0"))
	o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts"})
	d := newTestDownloader("ckpt-clone", nil, nil)
	d.Client = o.srv.Client()
	if err := d.Run(context.Background(), o.url("/media.m3u8"), filepath.Join(t.TempDir(), "v.ts")); err != nil {
		t.Fatalf("Run = %v", err)
	}
	snap := d.ResumeSnapshot()
	if snap == nil {
		t.Fatal("nil snapshot after success")
	}
	snap.Segments[0].URI = "mutated"
	snap.CompletedBitmap[0] = 0
	snap2 := d.ResumeSnapshot()
	if snap2.Segments[0].URI == "mutated" || snap2.CompletedBitmap[0] == 0 {
		t.Fatal("ResumeSnapshot aliased the internal durable state")
	}
}

func TestHLSCheckpoint_CancelKeepsOnlySyncedPrefix(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	gate := make(chan struct{})
	defer close(gate)
	o.serveBody("/s0.ts", []byte("s0"))
	o.mux.HandleFunc("/s1.ts", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("s1"))
	})
	o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts", "s1.ts", "s2.ts"})
	o.serveBody("/s2.ts", []byte("s2"))

	const id = "ckpt-cancel"
	d := newTestDownloader(id, nil, nil)
	d.Client = o.srv.Client()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, o.url("/media.m3u8"), filepath.Join(t.TempDir(), "v.ts")) }()

	deadline := time.Now().Add(3 * time.Second)
	for o.requests.count("/s1.ts") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("s1 never started")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	// Cancel is not an error path — only the already-synced first-segment
	// checkpoint survives as durable state.
	snap := d.ResumeSnapshot()
	if snap == nil || snap.MergedSegments != 1 {
		t.Fatalf("post-cancel durable = %+v, want the synced 1-segment prefix", snap)
	}
}

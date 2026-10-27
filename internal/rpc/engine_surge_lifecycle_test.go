package rpc

import (
	"context"
	"testing"
	"time"

	"goaria-v3/internal/surge/service"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

// The production persistence feed must ride SubscribeLifecycle: reliable
// subscriptions are excluded from HasObservers, so a wired engine with no UI
// consumer reports no ordinary observers (and progress work stays gated off).
func TestSurgeEngine_LifecycleFeedNotCountedAsObserver(t *testing.T) {
	testutil.SetupStateDB(t)
	engine := NewSurgeEngine()
	t.Cleanup(engine.Close)

	bus := engine.manager.GetEventBus()
	if bus == nil {
		t.Fatal("event bus missing")
	}
	if bus.HasObservers() {
		t.Fatal("persistence feed must be a reliable subscription, not an ordinary observer")
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream, cleanup, err := engine.StreamEvents(ctx)
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}
	if stream == nil {
		t.Fatal("StreamEvents returned nil stream")
	}
	if !bus.HasObservers() {
		t.Fatal("ordinary stream must count as an observer")
	}
	cancel()
	cleanup()
	if bus.HasObservers() {
		t.Fatal("observer must drop out after stream cleanup")
	}
}

// Events accepted before Close must be drained to the persistence worker: a
// reliable feed delivers every accepted lifecycle event before its stream
// closes, so Close can be issued immediately after Publish without loss.
func TestSurgeEngine_LifecycleWorkerDrainsOnClose(t *testing.T) {
	testutil.SetupStateDB(t)
	engine := NewSurgeEngine()

	ids := []string{"lc_seed_a", "lc_seed_b", "lc_seed_c"}
	for _, id := range ids {
		testutil.SeedMasterList(t, types.DownloadRecord{
			ID:       id,
			URL:      "https://example.com/" + id,
			DestPath: "/tmp/" + id + ".bin",
			Filename: id + ".bin",
			Status:   "queued",
		})
	}
	for _, id := range ids {
		if err := engine.service.Publish(types.DownloadEvent{
			Type:       types.EventStarted,
			DownloadID: id,
			URL:        "https://example.com/" + id,
			DestPath:   "/tmp/" + id + ".bin",
			Filename:   id + ".bin",
			Total:      1024,
		}); err != nil {
			t.Fatalf("Publish(%s): %v", id, err)
		}
	}

	done := make(chan struct{})
	go func() {
		engine.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close deadlocked — reliable feed drain may be broken")
	}

	for _, id := range ids {
		rec, err := store.GetDownload(id)
		if err != nil || rec == nil {
			t.Fatalf("GetDownload(%s): rec=%v err=%v", id, rec, err)
		}
		if rec.Status != "downloading" {
			t.Fatalf("GetDownload(%s).Status = %q, want downloading — EventStarted lost on close", id, rec.Status)
		}
	}
}

// A nil-bus service must fail StreamLifecycleEvents cleanly instead of
// panicking, keeping the production constructor's error path log-only.
func TestSurgeEngine_StreamLifecycleEvents_NoBus(t *testing.T) {
	svc := service.NewLocalDownloadService(nil)
	if stream, cleanup, err := svc.StreamLifecycleEvents(context.Background()); err == nil || stream != nil || cleanup != nil {
		t.Fatalf("StreamLifecycleEvents without bus = (%v,%T,%v), want error only", stream, cleanup, err)
	}
}

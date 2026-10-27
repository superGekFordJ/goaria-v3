package orchestrator

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/config"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

// EventQueued is published after AddWithPreparation returns, outside the
// scheduler lock. With the reliable feed wedged, Publish blocks on a full
// ingress queue — the queued row must already be durable and p.mu must stay
// responsive while the publish is parked.
func TestEnqueue_PublishBackpressureDoesNotHoldSchedulerLock(t *testing.T) {
	dir := testutil.SetupStateDB(t)
	bus := NewEventBus()
	pool, err := scheduler.NewManaged(bus.InputCh, 1)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewLifecycleManager(pool, bus, config.DefaultSettings())

	lifecycle, stopLifecycle := bus.SubscribeLifecycle()
	t.Cleanup(func() {
		// Detach the deliberately unread reliable feed first — an unconsumed
		// lifecycle subscription stalls the bus drain on Shutdown.
		stopLifecycle()
		mgr.Shutdown()
	})

	// The reliable queue absorbs eventQueueCapacity plus one in-flight event,
	// the ingress queue absorbs eventQueueCapacity more; the publish after
	// 2*cap+2 accepted sends must park. Counted sends, not sleeps.
	fill := types.DownloadEvent{Type: types.EventStarted, DownloadID: "filler"}
	var published atomic.Int64
	go func() {
		for {
			if err := bus.Publish(fill); err != nil {
				return
			}
			published.Add(1)
		}
	}()
	waitForCond(t, 10*time.Second, "ingress saturation", func() bool {
		return published.Load() >= int64(2*eventQueueCapacity+2)
	})

	const id = "backpressure-id"
	supportsRange := true
	req := &DownloadRequest{
		URL: "https://example.invalid/file.bin", Path: dir, Filename: "blocked.bin",
		FileSize: 1024, SupportsRange: &supportsRange, IsExplicitCategory: true,
	}
	enqDone := make(chan error, 1)
	go func() {
		_, _, err := mgr.EnqueueWithID(context.Background(), req, id)
		enqDone <- err
	}()

	// The master row is seeded inside prepare, before the parked publish.
	waitForCond(t, 5*time.Second, "queued master row", func() bool {
		e, _ := store.GetDownload(id)
		return e != nil && e.Status == "queued"
	})

	// The scheduler lock stays responsive while Publish is parked.
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		if st := pool.GetStatus(id); st == nil || st.Status != "queued" {
			t.Errorf("GetStatus = %+v, want queued %s", st, id)
		}
		if all := pool.GetAll(); len(all) != 1 || all[0].ID != id {
			t.Errorf("GetAll = %+v, want the queued task", all)
		}
	}()
	select {
	case <-probeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler lock held across a blocked publish")
	}

	// Enqueue is still parked in Publish — it must not have returned.
	select {
	case err := <-enqDone:
		t.Fatalf("enqueue returned while ingress was saturated: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	// A duplicate-ID rejection resolves promptly without touching Publish:
	// no extra persisted row and no second reservation is left behind.
	if _, _, err := mgr.EnqueueWithID(context.Background(), req, id); !errors.Is(err, types.ErrIDExists) {
		t.Fatalf("duplicate enqueue error = %v, want ErrIDExists", err)
	}
	if all := pool.GetAll(); len(all) != 1 {
		t.Fatalf("rejected enqueue left pool entries: %+v", all)
	}

	// Drain the reliable feed: parked publishes complete and enqueue returns.
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range lifecycle {
		}
	}()
	select {
	case err := <-enqDone:
		if err != nil {
			t.Fatalf("enqueue failed after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue did not finish after the reliable feed drained")
	}
}

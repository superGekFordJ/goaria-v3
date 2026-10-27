package orchestrator

import (
	"context"
	"testing"
	"time"

	"goaria-v3/internal/surge/config"
	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/types"
)

// rawObserverBus builds an EventBus without a dispatch loop: anything the
// aggregator publishes accumulates in InputCh where the test can observe it.
func rawObserverBus() *EventBus {
	ctx, cancel := context.WithCancel(context.Background())
	bus := &EventBus{
		InputCh:   make(chan types.DownloadEvent, 16),
		listeners: make(map[*eventSubscription]struct{}),
		ctx:       ctx,
		cancel:    cancel,
	}
	return bus
}

func queuedObserverCfg(id string) types.DownloadRecord {
	return types.DownloadRecord{
		ID:            id,
		URL:           "https://example.invalid/file",
		Filename:      "file.bin",
		ProgressState: progress.New(id, 1024),
		TotalSize:     1024,
		Runtime:       types.DefaultRuntimeConfig(),
	}
}

// With only a reliable lifecycle consumer attached, the aggregator must skip
// batch production entirely — HasObservers counts ordinary subscribers only.
func TestProgressAggregator_LifecycleOnlySubscriberProducesNoBatch(t *testing.T) {
	// Managed workers never self-claim, so the queued task stays put and keeps
	// ActiveCount > 0 without running a real download.
	pool, err := scheduler.NewManaged(make(chan types.DownloadEvent, 16), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.GracefulShutdown()
	bus := rawObserverBus()
	defer bus.cancel()
	agg := NewProgressAggregator(pool, bus, config.DefaultSettings())
	defer agg.Shutdown()

	// A queued task keeps ActiveCount > 0 without running a real download.
	pool.Add(queuedObserverCfg("obs-gate"))

	reliable := newEventSubscription(true)
	bus.listenerMu.Lock()
	bus.listeners[reliable] = struct{}{}
	bus.listenerMu.Unlock()
	if bus.HasObservers() {
		t.Fatal("reliable lifecycle subscription counted as an observer")
	}

	// Several ticks pass with active work but no observer: nothing is produced.
	time.Sleep(400 * time.Millisecond)
	if n := len(bus.InputCh); n != 0 {
		t.Fatalf("batch progress produced with no observer: %d events", n)
	}

	// An ordinary observer restores batch production on the next ticks.
	ordinary := newEventSubscription(false)
	bus.listenerMu.Lock()
	bus.listeners[ordinary] = struct{}{}
	bus.listenerMu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for len(bus.InputCh) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(bus.InputCh) == 0 {
		t.Fatal("no batch progress after an ordinary observer joined")
	}
	if msg := <-bus.InputCh; msg.Type != types.EventBatchProgress {
		t.Fatalf("first observer-gated event = %v, want batch progress", msg.Type)
	}
}

// The idle backoff still wins: no active downloads means no batch and no busy
// looping even when an ordinary observer is attached.
func TestProgressAggregator_IdlePoolStaysQuiet(t *testing.T) {
	pool, err := scheduler.NewManaged(make(chan types.DownloadEvent, 16), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.GracefulShutdown()
	bus := rawObserverBus()
	defer bus.cancel()
	agg := NewProgressAggregator(pool, bus, config.DefaultSettings())

	ordinary := newEventSubscription(false)
	bus.listenerMu.Lock()
	bus.listeners[ordinary] = struct{}{}
	bus.listenerMu.Unlock()

	time.Sleep(400 * time.Millisecond)
	if n := len(bus.InputCh); n != 0 {
		t.Fatalf("idle pool produced %d events", n)
	}

	// Shutdown during the idle sleep returns promptly.
	done := make(chan struct{})
	go func() { defer close(done); agg.Shutdown() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("aggregator shutdown stalled in idle backoff")
	}
}

// A nil event bus must be safe: the loop treats it like no observers and exits
// cleanly on shutdown instead of panicking.
func TestProgressAggregator_NilBusSafe(t *testing.T) {
	pool, err := scheduler.NewManaged(make(chan types.DownloadEvent, 16), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.GracefulShutdown()
	agg := NewProgressAggregator(pool, nil, config.DefaultSettings())
	pool.Add(queuedObserverCfg("nil-bus"))
	time.Sleep(300 * time.Millisecond)
	done := make(chan struct{})
	go func() { defer close(done); agg.Shutdown() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("aggregator shutdown stalled with nil event bus")
	}
}

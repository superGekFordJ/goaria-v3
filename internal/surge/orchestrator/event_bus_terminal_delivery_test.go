package orchestrator

import (
	"testing"
	"testing/synctest"
	"time"

	"goaria-v3/internal/surge/types"
)

// Lifecycle delivery regressions for the subscription-model EventBus: ordinary
// observers are bounded and replaceable, while the reliable lifecycle feed must
// keep receiving every persistence event even when another observer saturates.

func TestEventBus_TerminalEventDeliveredWhenBufferFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus := NewEventBus()
		defer bus.Shutdown()

		// Ordinary observer that never reads: it saturates its critical queue
		// and gets disconnected, but that must not impede the reliable feed.
		saturated, stopSaturated := bus.Subscribe()
		defer stopSaturated()

		lifecycle, stopLifecycle := bus.SubscribeLifecycle()
		defer stopLifecycle()

		collected := make(chan []types.DownloadEvent, 1)
		go func() {
			var events []types.DownloadEvent
			for ev := range lifecycle {
				events = append(events, ev)
			}
			collected <- events
		}()

		// More than cap+1 lifecycle events: the unread ordinary observer
		// overflows (one in-flight on out plus a full critical queue).
		for i := range 2 * eventQueueCapacity {
			if err := bus.Publish(types.DownloadEvent{Type: types.EventStarted, DownloadID: "t"}); err != nil {
				t.Fatalf("publish started %d: %v", i, err)
			}
		}
		// Publish returns at ingress; the disconnect lands only after the
		// dispatcher drains InputCh and one enqueue fails. Wait settles every
		// goroutine so the assertion below is deterministic — an eager read
		// would instead drain out and postpone the overflow indefinitely.
		synctest.Wait()
		select {
		case _, ok := <-saturated:
			if ok {
				t.Fatal("saturated observer stream still open after overflow")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("saturated observer was not disconnected")
		}

		// The remaining persistence whitelist events still reach the reliable feed.
		for _, et := range []types.EventType{
			types.EventQueued, types.EventPaused, types.EventComplete,
			types.EventError, types.EventRemoved,
		} {
			if err := bus.Publish(types.DownloadEvent{Type: et, DownloadID: "t"}); err != nil {
				t.Fatalf("publish %v: %v", et, err)
			}
		}
		// Transient events never enter the reliable feed.
		transient := []types.EventType{
			types.EventProgress, types.EventBatchProgress, types.EventSystem,
			types.EventFirstByte, types.EventResumed,
		}
		for _, et := range transient {
			if err := bus.Publish(types.DownloadEvent{Type: et, DownloadID: "t"}); err != nil {
				t.Fatalf("publish transient %v: %v", et, err)
			}
		}

		// Shutdown drains the reliable queue before closing the stream.
		bus.Shutdown()
		events := <-collected

		seen := make(map[types.EventType]int)
		for _, ev := range events {
			seen[ev.Type]++
		}
		for _, et := range transient {
			if seen[et] != 0 {
				t.Fatalf("reliable feed received transient %v x%d", et, seen[et])
			}
		}
		for _, et := range []types.EventType{
			types.EventStarted, types.EventQueued, types.EventPaused,
			types.EventComplete, types.EventError, types.EventRemoved,
		} {
			if seen[et] == 0 {
				t.Fatalf("reliable feed missing persistence event %v", et)
			}
		}
		if seen[types.EventStarted] != 2*eventQueueCapacity {
			t.Fatalf("reliable feed saw %d started events, want %d", seen[types.EventStarted], 2*eventQueueCapacity)
		}
		if seen[types.EventComplete] != 1 || seen[types.EventRemoved] != 1 {
			t.Fatalf("terminal events lost: complete=%d removed=%d", seen[types.EventComplete], seen[types.EventRemoved])
		}
	})
}

func TestEventBus_DropsProgressWhenBufferFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sub := newEventSubscription(false)
		eb := &EventBus{listeners: map[*eventSubscription]struct{}{sub: {}}}

		// The cap-1 progress slot keeps only the latest frame.
		for i := int64(1); i <= 5; i++ {
			eb.broadcastMsg(types.DownloadEvent{Type: types.EventProgress, Downloaded: i})
		}
		if got := (<-sub.progress).Downloaded; got != 5 {
			t.Fatalf("progress slot kept %d, want latest 5", got)
		}

		// A lifecycle transition evicts a still-pending progress frame.
		eb.broadcastMsg(types.DownloadEvent{Type: types.EventProgress, Downloaded: 6})
		eb.broadcastMsg(types.DownloadEvent{Type: types.EventPaused})
		select {
		case ev := <-sub.progress:
			t.Fatalf("stale progress survived lifecycle transition: %+v", ev)
		default:
		}
		if got := (<-sub.critical).Type; got != types.EventPaused {
			t.Fatalf("critical event = %v, want paused", got)
		}
	})
}

func TestEventBus_TerminalEventDeliveredToFastListenerConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus := NewEventBus()
		defer bus.Shutdown()

		slow, stopSlow := bus.Subscribe()
		defer stopSlow()
		fast, stopFast := bus.Subscribe()
		defer stopFast()

		// Overflow the slow observer while draining the fast one per event:
		// the slow critical queue fills and the bus disconnects it.
		for range 2 * eventQueueCapacity {
			if err := bus.Publish(types.DownloadEvent{Type: types.EventQueued, DownloadID: "fill"}); err != nil {
				t.Fatal(err)
			}
			if _, ok := <-fast; !ok {
				t.Fatal("fast observer stream closed during fill")
			}
		}
		select {
		case _, ok := <-slow:
			if ok {
				t.Fatal("slow observer was not disconnected")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("slow observer stream did not close")
		}

		// A terminal event reaches the healthy observer with no added wait:
		// inside synctest any blocking would show up as elapsed time.
		start := time.Now()
		if err := bus.Publish(types.DownloadEvent{Type: types.EventComplete, DownloadID: "task-seq"}); err != nil {
			t.Fatal(err)
		}
		select {
		case ev := <-fast:
			if ev.Type != types.EventComplete || ev.DownloadID != "task-seq" {
				t.Fatalf("fast observer got %+v, want complete task-seq", ev)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("fast observer did not receive complete")
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("slow observer added latency to fast delivery: %v", elapsed)
		}
	})
}

func TestEventBus_ShutdownDoesNotDeadlockWithBlockedListener(t *testing.T) {
	bus := NewEventBus()
	defer bus.Shutdown()

	// Ordinary observer left unread; shutdown force-stops it.
	_, stopUnread := bus.Subscribe()
	defer stopUnread()

	// Reliable consumer that keeps draining until the stream closes.
	lifecycle, stopLifecycle := bus.SubscribeLifecycle()
	defer stopLifecycle()
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range lifecycle {
		}
	}()

	for i := range eventQueueCapacity {
		if err := bus.Publish(types.DownloadEvent{Type: types.EventStarted, DownloadID: "x"}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	done := make(chan struct{})
	go func() {
		bus.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown deadlocked with a blocked listener")
	}
	select {
	case <-drainDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reliable consumer did not observe stream close")
	}
}

func TestEventBus_InputChBufferAbsorbsBurst(t *testing.T) {
	bus := NewEventBus()
	defer bus.Shutdown()

	sub, cleanup := bus.Subscribe()
	defer cleanup()
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range sub {
		}
	}()

	// Progress publishes never wait on the consumer: a burst below the ingress
	// capacity must return promptly even though most frames are coalesced.
	start := time.Now()
	for i := range 80 {
		if err := bus.Publish(types.DownloadEvent{Type: types.EventProgress, DownloadID: "burst"}); err != nil {
			t.Fatalf("publish burst %d: %v", i, err)
		}
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("progress burst stalled publish for %v", elapsed)
	}

	// A lifecycle publish must not error while the loop keeps draining.
	if err := bus.Publish(types.DownloadEvent{Type: types.EventComplete, DownloadID: "burst"}); err != nil {
		t.Fatalf("lifecycle publish after burst: %v", err)
	}

	bus.Shutdown()
	select {
	case <-drainDone:
	case <-time.After(5 * time.Second):
		t.Fatal("observer drain did not observe stream close")
	}
}

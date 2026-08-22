package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"goaria-v3/internal/surge/types"
)

// fillSubscriber publishes non-progress events until the Subscribe buffer is full.
func fillSubscriber(t *testing.T, eb *EventBus, sub <-chan types.DownloadEvent, target int) {
	t.Helper()
	for i := 0; i < target; i++ {
		err := eb.Publish(types.DownloadEvent{
			Type:       types.EventStarted,
			DownloadID: fmt.Sprintf("fill-%d", i),
		})
		if err != nil {
			t.Fatalf("fillSubscriber: Publish %d failed: %v (len=%d)", i, err, len(sub))
		}
	}
	// Wait for broadcastLoop to fill the subscriber buffer.
	deadline := time.Now().Add(2 * time.Second)
	for len(sub) < target && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(sub) < target {
		t.Fatalf("fillSubscriber: sub has %d events, want %d", len(sub), target)
	}
}

func TestEventBus_BasicPubSub(t *testing.T) {
	eb := NewEventBus()
	defer eb.Shutdown()

	sub, cleanup := eb.Subscribe()
	defer cleanup()

	msg := types.DownloadEvent{Message: "test message"}
	err := eb.Publish(msg)
	if err != nil {
		t.Fatalf("expected nil error on publish, got %v", err)
	}

	select {
	case received := <-sub:
		if received.Message != msg.Message {
			t.Errorf("expected %v, got %v", msg, received)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	eb := NewEventBus()
	defer eb.Shutdown()

	sub1, cleanup1 := eb.Subscribe()
	defer cleanup1()

	sub2, cleanup2 := eb.Subscribe()
	defer cleanup2()

	msg := types.DownloadEvent{Message: "broadcast"}
	_ = eb.Publish(msg)

	for i, sub := range []<-chan types.DownloadEvent{sub1, sub2} {
		select {
		case received := <-sub:
			if received.Message != msg.Message {
				t.Errorf("subscriber %d expected %v, got %v", i+1, msg, received)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("subscriber %d timed out", i+1)
		}
	}
}

func TestEventBus_ProgressMsgDropBehavior(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener := make(chan types.DownloadEvent, 1)
		listener <- types.DownloadEvent{Type: types.EventStarted, DownloadID: "sentinel"}
		eb := &EventBus{listeners: []chan types.DownloadEvent{listener}}

		start := time.Now()
		eb.broadcastMsg(types.DownloadEvent{Type: types.EventProgress, DownloadID: "progress"})
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("progress event wait = %v, want 0", elapsed)
		}

		got := <-listener
		if got.Type != types.EventStarted || got.DownloadID != "sentinel" {
			t.Fatalf("listener event = (%v, %q), want sentinel", got.Type, got.DownloadID)
		}
		select {
		case extra := <-listener:
			t.Fatalf("unexpected queued progress event: %+v", extra)
		default:
		}
	})
}

// TestEventBus_TerminalEventBlocksUntilDrainedOrShutdown replaces the upstream
// CriticalMsgWaitBehavior that asserted a ≥1s wait-then-drop. Fork blocks until
// deliver or ctx cancel; upstream still waits/drops after 1s.
func TestEventBus_TerminalEventBlocksUntilDrainedOrShutdown(t *testing.T) {
	eb := NewEventBus()

	sub, cleanup := eb.Subscribe()
	defer cleanup()

	fillSubscriber(t, eb, sub, 100)

	pubDone := make(chan error, 1)
	go func() {
		pubDone <- eb.Publish(types.DownloadEvent{
			Type:       types.EventComplete,
			DownloadID: "terminal-block",
		})
	}()

	// Publish should enqueue quickly (InputCh buffered); delivery may still block.
	select {
	case err := <-pubDone:
		if err != nil {
			t.Fatalf("Publish returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Publish to InputCh timed out")
	}

	// Give broadcastLoop time to start the blocking per-listener send.
	time.Sleep(100 * time.Millisecond)

	found := make(chan struct{})
	go func() {
		for {
			select {
			case msg, ok := <-sub:
				if !ok {
					return
				}
				if msg.Type == types.EventComplete && msg.DownloadID == "terminal-block" {
					close(found)
					return
				}
			case <-time.After(5 * time.Second):
				return
			}
		}
	}()

	select {
	case <-found:
		// Delivered after drain — not silently dropped at 1s.
	case <-time.After(3 * time.Second):
		t.Fatal("expected EventComplete to be delivered after draining, timed out")
	}

	shutdownDone := make(chan struct{})
	go func() {
		eb.Shutdown()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return within 5s")
	}
}

func TestEventBus_ShutdownCleanly(t *testing.T) {
	eb := NewEventBus()

	sub, cleanup := eb.Subscribe()
	defer cleanup()

	eb.Shutdown()

	// Should not be able to publish after shutdown
	err := eb.Publish(types.DownloadEvent{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled on publish after shutdown, got %v", err)
	}

	// Channels should be closed
	select {
	case _, ok := <-sub:
		if ok {
			t.Error("expected channel to be closed after shutdown")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timed out waiting for subscriber channel to close")
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	eb := NewEventBus()
	defer eb.Shutdown()

	_, cleanup := eb.Subscribe()

	eb.listenerMu.Lock()
	count := len(eb.listeners)
	eb.listenerMu.Unlock()
	if count != 1 {
		t.Fatalf("expected 1 listener, got %d", count)
	}

	cleanup()

	// Wait for the asynchronous unsubscribe to be processed by broadcastLoop
	for i := 0; i < 10; i++ {
		eb.listenerMu.Lock()
		count = len(eb.listeners)
		eb.listenerMu.Unlock()
		if count == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if count != 0 {
		t.Fatalf("expected 0 listeners after cleanup, got %d", count)
	}

	// Should be safe to call cleanup multiple times (sync.Once)
	cleanup()
}

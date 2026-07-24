package orchestrator

import (
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

// Terminal delivery regressions adapted for EventBus Subscribe (buffered 100).
// Upstream broadcastMsg waits/drops critical events after 1s; fork blocks until
// deliver or ctx cancel, with concurrent per-listener sends.

func TestEventBus_TerminalEventDeliveredWhenBufferFull(t *testing.T) {
	eb := NewEventBus()
	defer eb.Shutdown()

	sub, cleanup := eb.Subscribe()
	defer cleanup()

	fillSubscriber(t, eb, sub, 100)

	completeFound := make(chan struct{})
	go func() {
		for msg := range sub {
			if msg.Type == types.EventComplete && msg.DownloadID == "task-complete" {
				close(completeFound)
				return
			}
		}
	}()

	if err := eb.Publish(types.DownloadEvent{
		Type:       types.EventComplete,
		DownloadID: "task-complete",
	}); err != nil {
		t.Fatalf("Publish complete failed: %v", err)
	}

	select {
	case <-completeFound:
	case <-time.After(3 * time.Second):
		t.Fatal("expected EventComplete to be delivered after draining, timed out")
	}
}

func TestEventBus_DropsProgressWhenBufferFull(t *testing.T) {
	eb := NewEventBus()
	defer eb.Shutdown()

	sub, cleanup := eb.Subscribe()
	defer cleanup()

	fillSubscriber(t, eb, sub, 100)

	for i := 0; i < 50; i++ {
		if err := eb.Publish(types.DownloadEvent{
			Type:       types.EventProgress,
			DownloadID: "extra-prog",
			Downloaded: int64(i + 1000),
		}); err != nil {
			t.Fatalf("Publish progress %d failed: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	extraFound := 0
	for {
		select {
		case msg := <-sub:
			if msg.Type == types.EventProgress && msg.Downloaded >= 1000 {
				extraFound++
			}
		default:
			goto done
		}
	}
done:
	if extraFound > 0 {
		t.Errorf("expected 0 extra progress events (non-blocking drop), got %d", extraFound)
	}
}

func TestEventBus_TerminalEventDeliveredToFastListenerConcurrent(t *testing.T) {
	eb := NewEventBus()
	sub1, cleanup1 := eb.Subscribe()
	sub2, cleanup2 := eb.Subscribe()
	// Shutdown before cleanup so blocked sends exit via ctx.Done before
	// unsubscribe closes listener channels (avoids send-on-closed race).
	defer func() {
		eb.Shutdown()
		cleanup1()
		cleanup2()
	}()

	fillSubscriber(t, eb, sub1, 100)

	// Drain fill events from sub2 so it stays ready for the complete event.
	drainCtxDone := make(chan struct{})
	defer close(drainCtxDone)
	completeCh := make(chan types.DownloadEvent, 1)
	var nonCompleteCount atomic.Int64
	go func() {
		for {
			select {
			case <-drainCtxDone:
				return
			case msg, ok := <-sub2:
				if !ok {
					return
				}
				if msg.Type == types.EventComplete && msg.DownloadID == "task-seq" {
					completeCh <- msg
					return
				}
				nonCompleteCount.Add(1)
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	if err := eb.Publish(types.DownloadEvent{
		Type:       types.EventComplete,
		DownloadID: "task-seq",
	}); err != nil {
		t.Fatalf("Publish complete failed: %v", err)
	}

	select {
	case <-completeCh:
		elapsed := time.Since(start)
		if elapsed >= 200*time.Millisecond {
			t.Errorf("expected <200ms delivery to fast listener (no HOL), got %v", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Errorf("fast listener did not receive complete within 3s (non-complete drained: %d)", nonCompleteCount.Load())
	}
}

func TestEventBus_ShutdownDoesNotDeadlockWithBlockedListener(t *testing.T) {
	eb := NewEventBus()

	sub, cleanup := eb.Subscribe()
	defer cleanup()

	fillSubscriber(t, eb, sub, 100)

	pubDone := make(chan error, 1)
	go func() {
		pubDone <- eb.Publish(types.DownloadEvent{
			Type:       types.EventComplete,
			DownloadID: "task-shutdown",
		})
	}()
	select {
	case err := <-pubDone:
		if err != nil {
			t.Fatalf("Publish complete failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Publish to InputCh timed out")
	}
	time.Sleep(100 * time.Millisecond)

	shutdownDone := make(chan struct{})
	go func() {
		eb.Shutdown()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown deadlocked — ctx.Done did not unblock terminal send")
	}
}

func TestEventBus_CtxCancelledMidSend(t *testing.T) {
	eb := NewEventBus()

	sub, cleanup := eb.Subscribe()
	defer cleanup()

	fillSubscriber(t, eb, sub, 100)

	pubDone := make(chan error, 1)
	go func() {
		pubDone <- eb.Publish(types.DownloadEvent{
			Type:       types.EventComplete,
			DownloadID: "task-cancel",
		})
	}()
	select {
	case err := <-pubDone:
		if err != nil {
			t.Fatalf("Publish complete failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Publish to InputCh timed out")
	}
	time.Sleep(100 * time.Millisecond)

	// Cancel mid-send (same step Shutdown uses before closing InputCh).
	eb.cancel()
	close(eb.InputCh)

	finished := make(chan struct{})
	go func() {
		eb.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("broadcastLoop did not finish after ctx cancel")
	}
}

func TestEventBus_InputChBufferAbsorbsBurst(t *testing.T) {
	eb := NewEventBus()
	defer eb.Shutdown()

	sub, cleanup := eb.Subscribe()
	defer cleanup()

	go func() {
		for range sub {
		}
	}()

	start := time.Now()
	for i := 0; i < 80; i++ {
		if err := eb.Publish(types.DownloadEvent{
			Type:       types.EventProgress,
			DownloadID: "burst",
		}); err != nil {
			t.Fatalf("Publish burst %d failed: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Logf("burst send took %v — InputCh may have briefly blocked", elapsed)
	}
}

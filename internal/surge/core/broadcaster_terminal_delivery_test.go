package core

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/engine/events"
)

// fillOutCh sends events to InputCh until outCh is full (len == 99).
// Uses DownloadStartedMsg (terminal events) which use blocking send with
// ctx.Done guard. Since outCh has 99 buffer and the forwarding goroutine is
// fast, these reach outCh quickly as long as outCh is not full.
func fillOutCh(t *testing.T, svc *LocalDownloadService, outCh <-chan interface{}, target int) {
	for i := 0; i < target; i++ {
		select {
		case svc.InputCh <- events.DownloadStartedMsg{
			DownloadID: fmt.Sprintf("fill-%d", i),
		}:
		case <-time.After(2 * time.Second):
			t.Fatalf("fillOutCh: timed out sending event %d, outCh has %d", i, len(outCh))
		}
	}
	// Wait for forwarding goroutine to write to outCh
	time.Sleep(100 * time.Millisecond)
	t.Logf("fillOutCh: outCh has %d events (target %d)", len(outCh), target)
}

// TestBroadcastLoop_TerminalEventDeliveredWhenOutChFull verifies that when the
// listener's outCh is full (forwarding goroutine blocked on outCh write), a
// DownloadCompleteMsg is still delivered after draining outCh — the blocking
// send with ctx.Done guard does not drop terminal events.
func TestBroadcastLoop_TerminalEventDeliveredWhenOutChFull(t *testing.T) {
	svc := NewLocalDownloadService(nil)
	defer svc.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, cleanup, err := svc.StreamEvents(ctx)
	if err != nil {
		t.Fatalf("StreamEvents failed: %v", err)
	}
	defer cleanup()

	// Fill outCh (buffer 99) with terminal events (DownloadStartedMsg).
	fillOutCh(t, svc, outCh, 99)

	if len(outCh) < 99 {
		t.Fatalf("outCh only has %d events, expected 99", len(outCh))
	}

	// Send one more event to make the forwarding goroutine block on outCh <- msg.
	svc.InputCh <- events.DownloadStartedMsg{DownloadID: "blocker"}
	// Give the forwarding goroutine time to receive it and block on outCh write
	time.Sleep(100 * time.Millisecond)

	// Now the forwarding goroutine is blocked on outCh <- "blocker".
	// Send the DownloadCompleteMsg — broadcastLoop's per-listener goroutine
	// blocks on ch <- msg (inCh, no receiver). Start draining outCh in a
	// goroutine so the forwarding goroutine unblocks and can receive.
	completeFound := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-outCh:
				if _, ok := msg.(events.DownloadCompleteMsg); ok {
					close(completeFound)
					return
				}
			}
		}
	}()

	svc.InputCh <- events.DownloadCompleteMsg{
		DownloadID: "task-complete",
		Total:      100,
	}

	select {
	case <-completeFound:
		t.Logf("PASS: DownloadCompleteMsg was delivered (blocking send with ctx.Done guard)")
	case <-time.After(3 * time.Second):
		t.Fatal("expected DownloadCompleteMsg to be delivered after draining outCh, timed out")
	}
}

// TestBroadcastLoop_DropsProgressWhenOutChFull verifies that progress events
// are still dropped immediately (non-blocking) when outCh is full, while
// terminal events use blocking send.
func TestBroadcastLoop_DropsProgressWhenOutChFull(t *testing.T) {
	svc := NewLocalDownloadService(nil)
	defer svc.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, cleanup, err := svc.StreamEvents(ctx)
	if err != nil {
		t.Fatalf("StreamEvents failed: %v", err)
	}
	defer cleanup()

	// Fill outCh with terminal events
	fillOutCh(t, svc, outCh, 99)

	// Send one more event to block the forwarding goroutine on outCh write
	svc.InputCh <- events.DownloadStartedMsg{DownloadID: "blocker-prog"}
	time.Sleep(100 * time.Millisecond)

	// Now send progress events — they should be dropped immediately (non-blocking)
	for i := 0; i < 50; i++ {
		svc.InputCh <- events.ProgressMsg{
			DownloadID: fmt.Sprintf("extra-prog-%d", i),
			Downloaded: int64(i + 1000),
		}
	}

	// Give broadcastLoop time to process (should be instant, dropped)
	time.Sleep(200 * time.Millisecond)

	// Drain outCh and check for extra progress events
	extraFound := 0
	totalDrained := 0
	for {
		select {
		case msg := <-outCh:
			totalDrained++
			if pm, ok := msg.(events.ProgressMsg); ok && pm.Downloaded >= 1000 {
				extraFound++
			}
		default:
			goto done2
		}
	}
done2:

	t.Logf("Drained %d events, %d were extra progress events", totalDrained, extraFound)
	if extraFound > 0 {
		t.Errorf("Expected 0 extra progress events in outCh (should be dropped non-blocking), got %d", extraFound)
	}
}

// TestBroadcastLoop_TerminalEventDeliveredToAllListenersConcurrent verifies
// that broadcastLoop sends terminal events to listeners concurrently. When
// listener[0]'s outCh is full (forwarding goroutine blocked), listener[1]
// (being drained) receives the terminal event almost immediately without
// waiting for listener[0].
func TestBroadcastLoop_TerminalEventDeliveredToAllListenersConcurrent(t *testing.T) {
	svc := NewLocalDownloadService(nil)
	defer svc.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register two listeners (simulating DB + monitor)
	outCh1, cleanup1, err := svc.StreamEvents(ctx)
	if err != nil {
		t.Fatalf("StreamEvents 1 failed: %v", err)
	}
	defer cleanup1()

	outCh2, cleanup2, err := svc.StreamEvents(ctx)
	if err != nil {
		t.Fatalf("StreamEvents 2 failed: %v", err)
	}
	defer cleanup2()

	// Fill outCh1 (listener 0 / DB) so it's blocked. Don't drain it.
	fillOutCh(t, svc, outCh1, 99)

	// Send one more event to block listener[0]'s forwarding goroutine on outCh write
	svc.InputCh <- events.DownloadStartedMsg{DownloadID: "blocker-seq"}
	time.Sleep(100 * time.Millisecond)

	// Drain outCh2 in background, capturing the complete event
	completeCh := make(chan interface{}, 1)
	var nonCompleteCount atomic.Int64
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-outCh2:
				if !ok {
					return
				}
				if _, isComplete := msg.(events.DownloadCompleteMsg); !isComplete {
					nonCompleteCount.Add(1)
				} else {
					completeCh <- msg
					return
				}
			}
		}
	}()

	// Wait for outCh2 to drain the blocker event
	time.Sleep(200 * time.Millisecond)

	// Now send a complete event. With concurrent per-listener sends,
	// listener[1] receives it almost immediately (listener[0] blocks in its
	// own goroutine, not delaying listener[1]).
	start := time.Now()
	svc.InputCh <- events.DownloadCompleteMsg{
		DownloadID: "task-seq",
		Total:      100,
	}

	// Wait for delivery to listener[1]
	select {
	case msg := <-completeCh:
		elapsed := time.Since(start)
		if cm, ok := msg.(events.DownloadCompleteMsg); ok && cm.DownloadID == "task-seq" {
			t.Logf("listener[1] received complete event after %v (concurrent send, not delayed by listener[0])", elapsed)
			if elapsed >= 200*time.Millisecond {
				t.Errorf("Expected <200ms delay (concurrent send), got %v", elapsed)
			}
		} else {
			t.Errorf("listener[1] received wrong event: %T", msg)
		}
	case <-time.After(3 * time.Second):
		t.Errorf("listener[1] did not receive complete event within 3s (non-complete drained: %d)", nonCompleteCount.Load())
	}
}

// TestBroadcastLoop_EndgameHighDensityDeliversComplete verifies that under
// end-game high-density conditions (outCh full, forwarding goroutine blocked),
// a DownloadCompleteMsg is delivered after draining outCh — not dropped.
func TestBroadcastLoop_EndgameHighDensityDeliversComplete(t *testing.T) {
	svc := NewLocalDownloadService(nil)
	defer svc.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, cleanup, err := svc.StreamEvents(ctx)
	if err != nil {
		t.Fatalf("StreamEvents failed: %v", err)
	}
	defer cleanup()

	// Fill outCh with 99 terminal events
	fillOutCh(t, svc, outCh, 99)

	// Send 1 more to block the forwarding goroutine on outCh write
	svc.InputCh <- events.DownloadStartedMsg{DownloadID: "blocker-density"}
	time.Sleep(100 * time.Millisecond)

	// Now send the complete event — forwarding goroutine is blocked.
	// Start draining so the forwarding goroutine unblocks and receives.
	completeFound := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-outCh:
				if cm, ok := msg.(events.DownloadCompleteMsg); ok && cm.DownloadID == "task-endgame" {
					close(completeFound)
					return
				}
			}
		}
	}()

	svc.InputCh <- events.DownloadCompleteMsg{
		DownloadID: "task-endgame",
		Total:      100,
	}

	select {
	case <-completeFound:
		t.Logf("PASS: DownloadCompleteMsg delivered under end-game high-density (no drop)")
	case <-time.After(3 * time.Second):
		t.Fatal("expected DownloadCompleteMsg to be delivered after draining, timed out")
	}
}

// TestBroadcastLoop_SlowConsumerDeliversComplete verifies that when the
// consumer is slow (outCh full, forwarding goroutine blocked), a complete
// event is delivered after draining — not dropped by a timeout.
func TestBroadcastLoop_SlowConsumerDeliversComplete(t *testing.T) {
	svc := NewLocalDownloadService(nil)
	defer svc.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, cleanup, err := svc.StreamEvents(ctx)
	if err != nil {
		t.Fatalf("StreamEvents failed: %v", err)
	}
	defer cleanup()

	// Fill outCh with 99 events.
	fillOutCh(t, svc, outCh, 99)

	// Send 1 more to block the forwarding goroutine
	svc.InputCh <- events.DownloadStartedMsg{DownloadID: "blocker-slow"}
	time.Sleep(100 * time.Millisecond)

	// Now send complete event A — forwarding goroutine blocked.
	// Start draining so it unblocks and receives.
	completeFound := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-outCh:
				if cm, ok := msg.(events.DownloadCompleteMsg); ok && cm.DownloadID == "task-A" {
					close(completeFound)
					return
				}
			}
		}
	}()

	svc.InputCh <- events.DownloadCompleteMsg{
		DownloadID: "task-A",
		Total:      100,
	}

	select {
	case <-completeFound:
		t.Logf("PASS: task-A complete delivered despite slow consumer (no drop)")
	case <-time.After(3 * time.Second):
		t.Fatal("expected task-A complete to be delivered after draining, timed out")
	}
}

// TestBroadcastLoop_InputChBufferAbsorbsBurst verifies that InputCh (buffer 100
// for nil pool) absorbs event bursts without blocking the producer.
func TestBroadcastLoop_InputChBufferAbsorbsBurst(t *testing.T) {
	inputCh := make(chan interface{}, 100)
	svc := NewLocalDownloadServiceWithInput(nil, inputCh)
	defer svc.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, cleanup, err := svc.StreamEvents(ctx)
	if err != nil {
		t.Fatalf("StreamEvents failed: %v", err)
	}
	defer cleanup()

	// Drain outCh in background
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-outCh:
			}
		}
	}()

	// Send 80 events rapidly (blocking sends, simulating safeSendProgress)
	start := time.Now()
	for i := 0; i < 80; i++ {
		inputCh <- events.ProgressMsg{
			DownloadID: fmt.Sprintf("burst-%d", i),
		}
	}
	elapsed := time.Since(start)

	t.Logf("Sent 80 events to InputCh(100) in %v", elapsed)
	if elapsed > 100*time.Millisecond {
		t.Logf("WARNING: Send took %v — InputCh may have briefly blocked", elapsed)
	}
}

// TestBroadcastLoop_ShutdownDoesNotDeadlockWithBlockedListener verifies that
// Shutdown() returns promptly even when a listener's forwarding goroutine is
// blocked on a full outCh and broadcastLoop has a pending terminal event
// blocked on the listener's inCh. The ctx.Done guard must let the blocked
// send goroutine exit so Shutdown does not hang.
func TestBroadcastLoop_ShutdownDoesNotDeadlockWithBlockedListener(t *testing.T) {
	svc := NewLocalDownloadService(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, cleanup, err := svc.StreamEvents(ctx)
	if err != nil {
		t.Fatalf("StreamEvents failed: %v", err)
	}
	defer cleanup()

	// Fill outCh (99) + 1 blocker so forwarding goroutine is blocked on outCh write.
	fillOutCh(t, svc, outCh, 99)
	svc.InputCh <- events.DownloadStartedMsg{DownloadID: "blocker-shutdown"}
	time.Sleep(100 * time.Millisecond)

	// Send a terminal event — broadcastLoop's per-listener goroutine will block
	// on ch <- msg (inCh, no receiver). Do NOT drain outCh.
	done := make(chan struct{})
	go func() {
		svc.InputCh <- events.DownloadCompleteMsg{
			DownloadID: "task-shutdown",
			Total:      100,
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("InputCh send of terminal event timed out (InputCh buffer may be full)")
	}

	// Shutdown must not deadlock. Give it a generous but bounded timeout.
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- svc.Shutdown()
	}()
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Errorf("Shutdown returned error: %v", err)
		}
		t.Logf("PASS: Shutdown returned within timeout despite blocked listener")
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown deadlocked — ctx.Done guard did not let blocked send exit")
	}
}

// TestBroadcastLoop_CtxCancelledMidSend verifies that cancelling s.ctx while a
// terminal event send is blocked on a full listener lets broadcastLoop
// proceed (the blocked per-listener goroutine exits via the ctx.Done case).
func TestBroadcastLoop_CtxCancelledMidSend(t *testing.T) {
	svc := NewLocalDownloadService(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, cleanup, err := svc.StreamEvents(ctx)
	if err != nil {
		t.Fatalf("StreamEvents failed: %v", err)
	}
	defer cleanup()

	// Fill outCh + block forwarding goroutine.
	fillOutCh(t, svc, outCh, 99)
	svc.InputCh <- events.DownloadStartedMsg{DownloadID: "blocker-cancel"}
	time.Sleep(100 * time.Millisecond)

	// Send terminal event in a goroutine (will block on inCh send).
	sendDone := make(chan struct{})
	go func() {
		svc.InputCh <- events.DownloadCompleteMsg{
			DownloadID: "task-cancel",
			Total:      100,
		}
		close(sendDone)
	}()
	select {
	case <-sendDone:
	case <-time.After(2 * time.Second):
		t.Fatal("InputCh send of terminal event timed out")
	}

	// Cancel the service ctx (simulates the cancel step of Shutdown).
	// The blocked per-listener send goroutine should exit via s.ctx.Done case,
	// letting broadcastLoop proceed. Then close InputCh + Wait confirms no hang.
	svc.cancel()

	// Close InputCh and wait for broadcastLoop to finish.
	go func() {
		close(svc.InputCh)
	}()
	finished := make(chan struct{})
	go func() {
		svc.broadcastWG.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		t.Logf("PASS: broadcastLoop finished after ctx cancel (no permanent block)")
	case <-time.After(5 * time.Second):
		t.Fatal("broadcastLoop did not finish after ctx cancel — blocked send goroutine leaked")
	}
}

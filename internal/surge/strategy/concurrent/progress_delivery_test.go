package concurrent

import (
	"context"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

func newDeliveryDownloader(ch chan types.DownloadEvent) *ConcurrentDownloader {
	return &ConcurrentDownloader{ID: "delivery-id", ProgressChan: ch}
}

func pauseSnapshot() *types.DownloadRecord {
	return &types.DownloadRecord{
		ID:         "delivery-id",
		Downloaded: 42,
		TotalSize:  100,
		Tasks:      []types.Task{{Offset: 42, Length: 58}},
	}
}

// A full event channel must not drop the pause snapshot: the send waits, and a
// consumer that resumes still receives the complete State. The download ctx is
// already cancelled by Pause, so delivery waits on a fresh bounded context.
func TestConcurrentProgressDelivery_PausedSnapshotWaitsForConsumer(t *testing.T) {
	ch := make(chan types.DownloadEvent, 1)
	ch <- types.DownloadEvent{Type: types.EventProgress} // occupy the only slot
	d := newDeliveryDownloader(ch)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Sites deliver pause events on context.Background() precisely so the
		// cancelled dlCtx cannot strand the snapshot.
		d.sendProgressEvent(context.Background(), types.DownloadEvent{
			Type:       types.EventPaused,
			DownloadID: d.ID,
			State:      pauseSnapshot(),
			Downloaded: 42,
		})
	}()

	// The send is waiting, not dropped: the sentinel is still the only entry.
	select {
	case <-done:
		t.Fatal("send returned while the channel was still full")
	case <-time.After(200 * time.Millisecond):
	}
	if ev := <-ch; ev.Type != types.EventProgress {
		t.Fatalf("sentinel evicted by %+v", ev)
	}

	select {
	case ev := <-ch:
		if ev.Type != types.EventPaused || ev.State == nil {
			t.Fatalf("pause event = %+v, want State snapshot", ev)
		}
		if ev.State.Downloaded != 42 || len(ev.State.Tasks) != 1 || ev.State.Tasks[0].Offset != 42 {
			t.Fatalf("snapshot corrupted: %+v", ev.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pause snapshot not delivered after consumer recovered")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("send did not return after delivery")
	}
}

// A consumer that never resumes is a stop-loss case: the bounded wait gives up
// after the delivery timeout instead of parking the caller forever.
func TestConcurrentProgressDelivery_WedgedConsumerWaitIsBounded(t *testing.T) {
	ch := make(chan types.DownloadEvent, 1)
	ch <- types.DownloadEvent{Type: types.EventProgress}
	d := newDeliveryDownloader(ch)

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		d.sendProgressEvent(context.Background(), types.DownloadEvent{
			Type:       types.EventPaused,
			DownloadID: d.ID,
			State:      pauseSnapshot(),
		})
	}()

	select {
	case <-done:
	case <-time.After(progressSendTimeout + 2*time.Second):
		t.Fatal("wedged consumer parked the send past its bound")
	}
	if elapsed := time.Since(start); elapsed < progressSendTimeout {
		t.Fatalf("send gave up after %v, want the full %v wait", elapsed, progressSendTimeout)
	}
	// Nothing was delivered: the sentinel is still the only entry.
	if got := <-ch; got.Type != types.EventProgress {
		t.Fatalf("unexpected delivery after timeout: %+v", got)
	}
}

// A cancelled delivery context gives up immediately — this is why the pause
// sites pass context.Background() instead of the cancelled download ctx.
func TestConcurrentProgressDelivery_CancelledDeliveryCtxGivesUp(t *testing.T) {
	ch := make(chan types.DownloadEvent, 1)
	ch <- types.DownloadEvent{Type: types.EventProgress}
	d := newDeliveryDownloader(ch)

	dlCtx, cancel := context.WithCancel(context.Background())
	cancel() // Pause cancels the download context before the pause event sends.

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.sendProgressEvent(dlCtx, types.DownloadEvent{Type: types.EventPaused, DownloadID: d.ID, State: pauseSnapshot()})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled delivery ctx did not give up")
	}
	if got := <-ch; got.Type != types.EventProgress {
		t.Fatalf("event delivered through a cancelled ctx: %+v", got)
	}
}

// First-byte delivery waits on the live worker ctx: a running cancel must
// interrupt the bounded wait so wedged consumers cannot stall worker exit.
func TestConcurrentProgressDelivery_FirstByteCancelledRunCtxExits(t *testing.T) {
	ch := make(chan types.DownloadEvent, 1)
	ch <- types.DownloadEvent{Type: types.EventProgress}
	d := newDeliveryDownloader(ch)
	runCtx, cancel := context.WithCancel(context.Background())
	d.workerDepsPtr.Store(&workerDeps{ctx: runCtx})

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.sendFirstByteOnce(time.Now(), &ActiveTask{})
	}()

	// It is parked in the bounded wait, not skipped.
	select {
	case <-done:
		t.Fatal("first-byte send returned while the channel was still full")
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("running cancel did not interrupt the first-byte send")
	}
}

// Degenerate channels must never panic: a nil channel is a no-op and a closed
// channel is recovered and logged rather than crashing the worker.
func TestConcurrentProgressDelivery_NilAndClosedChannelSafe(t *testing.T) {
	nilDL := &ConcurrentDownloader{ID: "nil-chan"}
	nilDL.sendProgressEvent(context.Background(), types.DownloadEvent{Type: types.EventPaused})

	closed := make(chan types.DownloadEvent, 1)
	d := newDeliveryDownloader(closed)
	close(closed)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.sendProgressEvent(context.Background(), types.DownloadEvent{Type: types.EventPaused})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("closed-channel send did not recover")
	}
}

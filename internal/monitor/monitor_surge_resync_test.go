package monitor

import (
	"sync/atomic"
	"testing"
	"time"

	surgeEvents "goaria-v3/internal/surge/types"
)

// A ResyncRequired system event must flag the single-slot resync channel so
// the poll loop reconciles immediately — the handler itself must not
// reconcile or spawn a goroutine.
func TestSurgeResync_SystemEventNotifiesPollLoop(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	reader.setLists(nil, nil, nil)
	m.surgePollStopChan = make(chan struct{})
	m.surgeResyncChan = make(chan struct{}, 1)
	m.surgePollInterval = time.Hour // ticker must not fire during the test

	var pollCount int64
	m.surgePollReader = &countingReader{surgeListReader: reader, count: &pollCount}

	done := make(chan struct{})
	go func() {
		m.surgePollLoop()
		close(done)
	}()

	// Startup reconcile runs once before the event arrives.
	requireCondition(t, func() bool {
		return atomic.LoadInt64(&pollCount) >= 1
	}, 2*time.Second, "startup reconcile did not run")

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:           surgeEvents.EventSystem,
		ResyncRequired: true,
		Message:        "Live updates fell behind",
	})

	requireCondition(t, func() bool {
		return atomic.LoadInt64(&pollCount) >= 2
	}, 2*time.Second, "resync notification did not trigger reconcile")

	close(m.surgePollStopChan)
	<-done
}

// Repeated resync events collapse to one pending flag on the cap-1 channel;
// without a consumer the handler stays non-blocking.
func TestSurgeResync_CoalescesToSinglePending(t *testing.T) {
	m, _, _, _ := newReconcileTestMonitor(t)
	m.surgeResyncChan = make(chan struct{}, 1)

	ev := surgeEvents.DownloadEvent{Type: surgeEvents.EventSystem, ResyncRequired: true}
	for i := 0; i < 5; i++ {
		m.handleSurgeEvent(ev)
	}
	if got := len(m.surgeResyncChan); got != 1 {
		t.Fatalf("pending resync flags = %d, want coalesced 1", got)
	}
}

// Non-resync system events are informational: no notification, no reconcile.
func TestSurgeResync_NonResyncSystemEventIgnored(t *testing.T) {
	m, _, _, _ := newReconcileTestMonitor(t)
	m.surgeResyncChan = make(chan struct{}, 1)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:    surgeEvents.EventSystem,
		Message: "informational",
	})
	if got := len(m.surgeResyncChan); got != 0 {
		t.Fatalf("non-resync system event queued %d flags, want 0", got)
	}
}

// A monitor without the resync channel (zero value / Aria2-only wiring) must
// swallow resync events without blocking or panicking.
func TestSurgeResync_NilChannelSafe(t *testing.T) {
	m := &Monitor{}
	done := make(chan struct{})
	go func() {
		m.handleSurgeEvent(surgeEvents.DownloadEvent{
			Type:           surgeEvents.EventSystem,
			ResyncRequired: true,
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("resync handling blocked on nil channel")
	}
}

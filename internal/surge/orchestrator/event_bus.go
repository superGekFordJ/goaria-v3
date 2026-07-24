package orchestrator

import (
	"context"
	"sync"
	"time"

	"goaria-v3/internal/surge/types"
)

// EventBus handles broadcasting events from the orchestrator to all listeners.
type EventBus struct {
	InputCh       chan types.DownloadEvent
	listeners     []chan types.DownloadEvent
	listenerMu    sync.Mutex
	unsubscribeCh chan chan types.DownloadEvent
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	// sendWG tracks in-flight per-listener terminal-event sends so cleanup
	// does not close listener channels while a send is still in flight.
	sendWG       sync.WaitGroup
	pubMu        sync.RWMutex
	pubWg        sync.WaitGroup
	shutdownOnce sync.Once
}

func NewEventBus() *EventBus {
	ctx, cancel := context.WithCancel(context.Background())
	eb := &EventBus{
		InputCh:       make(chan types.DownloadEvent, 100),
		listeners:     make([]chan types.DownloadEvent, 0),
		unsubscribeCh: make(chan chan types.DownloadEvent, 10),
		ctx:           ctx,
		cancel:        cancel,
	}
	eb.wg.Add(1)
	go eb.broadcastLoop()
	return eb
}

func (eb *EventBus) broadcastLoop() {
	defer eb.wg.Done()
	for {
		select {
		case msg, ok := <-eb.InputCh:
			if !ok {
				// Wait for in-flight terminal sends before closing listeners.
				// Shutdown cancels ctx first, so blocked sends exit via ctx.Done.
				eb.sendWG.Wait()
				eb.listenerMu.Lock()
				for _, ch := range eb.listeners {
					close(ch)
				}
				eb.listeners = nil
				eb.listenerMu.Unlock()
				return
			}
			eb.broadcastMsg(msg)

		case chToClose := <-eb.unsubscribeCh:
			eb.listenerMu.Lock()
			for i, listener := range eb.listeners {
				if listener == chToClose {
					eb.listeners = append(eb.listeners[:i], eb.listeners[i+1:]...)
					break
				}
			}
			eb.listenerMu.Unlock()
			// Close after removal. recover covers a send that lost the race
			// between copy-listeners and this close (send-on-closed).
			func() {
				defer func() { _ = recover() }()
				close(chToClose)
			}()
		}
	}
}

func (eb *EventBus) broadcastMsg(msg types.DownloadEvent) {
	eb.listenerMu.Lock()
	listenersCopy := make([]chan types.DownloadEvent, len(eb.listeners))
	copy(listenersCopy, eb.listeners)
	eb.listenerMu.Unlock()

	isProgress := msg.Type == types.EventProgress || msg.Type == types.EventBatchProgress

	if isProgress {
		for _, ch := range listenersCopy {
			func() {
				defer func() { _ = recover() }()
				select {
				case ch <- msg:
				default:
				}
			}()
		}
		return
	}

	// FORK-PATCH: Non-progress events block until deliver or ctx.Done (not a
	// 1s timer drop). Concurrent per-listener sends avoid head-of-line blocking.
	var wg sync.WaitGroup
	for _, ch := range listenersCopy {
		eb.sendWG.Add(1)
		wg.Add(1)
		go func(ch chan types.DownloadEvent) {
			defer eb.sendWG.Done()
			defer wg.Done()
			defer func() { _ = recover() }()
			select {
			case ch <- msg:
			case <-eb.ctx.Done():
			}
		}(ch)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-eb.ctx.Done():
	}
}

// Publish emits an event into the bus.
func (eb *EventBus) Publish(msg types.DownloadEvent) error {
	eb.pubMu.RLock()
	if eb.ctx.Err() != nil {
		eb.pubMu.RUnlock()
		return context.Canceled
	}
	eb.pubWg.Add(1)
	eb.pubMu.RUnlock()

	defer eb.pubWg.Done()

	select {
	case <-eb.ctx.Done():
		return context.Canceled
	case eb.InputCh <- msg:
		return nil
	case <-time.After(1 * time.Second):
		return context.DeadlineExceeded
	}
}

// Subscribe returns a channel that receives events.
func (eb *EventBus) Subscribe() (<-chan types.DownloadEvent, func()) {
	outCh := make(chan types.DownloadEvent, 100)
	eb.listenerMu.Lock()
	eb.listeners = append(eb.listeners, outCh)
	eb.listenerMu.Unlock()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			select {
			case eb.unsubscribeCh <- outCh:
			case <-eb.ctx.Done():
			}
		})
	}
	return outCh, cleanup
}

func (eb *EventBus) Shutdown() {
	eb.shutdownOnce.Do(func() {
		eb.pubMu.Lock()
		eb.cancel()
		eb.pubMu.Unlock()

		eb.pubWg.Wait()   // wait for all active Publish calls to return
		close(eb.InputCh) // safely close to trigger drain
	})
	eb.wg.Wait()
}

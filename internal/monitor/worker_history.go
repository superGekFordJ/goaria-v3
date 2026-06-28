package monitor

import (
	"sort"
	"sync"
	"time"

	"goaria-v3/internal/surge/engine/types"
)

// timedSnapshot pairs a worker telemetry snapshot with its sample time.
type timedSnapshot struct {
	types.WorkerSnapshot
	at time.Time
}

// ring is a fixed-capacity circular buffer of timed samples (oldest overwritten).
type ring struct {
	buf  []timedSnapshot
	head int // index of next write
	full bool
}

func newRing(capacity int) *ring {
	if capacity < 1 {
		capacity = 1
	}
	return &ring{buf: make([]timedSnapshot, 0, capacity)}
}

func (r *ring) push(s timedSnapshot) {
	if len(r.buf) < cap(r.buf) {
		r.buf = append(r.buf, s)
		r.head = len(r.buf) % cap(r.buf)
		return
	}
	r.buf[r.head] = s
	r.head = (r.head + 1) % cap(r.buf)
	r.full = true
}

// window returns samples in chronological order (oldest first).
func (r *ring) window() []timedSnapshot {
	if !r.full || len(r.buf) < cap(r.buf) {
		out := make([]timedSnapshot, len(r.buf))
		copy(out, r.buf)
		return out
	}
	n := cap(r.buf)
	out := make([]timedSnapshot, n)
	copy(out, r.buf[r.head:])
	copy(out[n-r.head:], r.buf[:r.head])
	return out
}

// WorkerHistory stores per-(gid, workerID) sliding-window telemetry history
// with worker-absence eviction. It is concurrency-safe via its own RWMutex and
// does not nest with monitor/convergence locks.
type WorkerHistory struct {
	mu        sync.RWMutex
	windowCap int
	data      map[string]map[int]*ring // gid -> workerID -> ring
	absent    map[string]map[int]int   // gid -> workerID -> consecutive absent ticks
}

// NewWorkerHistory creates a history container with the given ring capacity
// (samples per worker).
func NewWorkerHistory(windowCap int) *WorkerHistory {
	if windowCap < 1 {
		windowCap = 1
	}
	return &WorkerHistory{
		windowCap: windowCap,
		data:      make(map[string]map[int]*ring),
		absent:    make(map[string]map[int]int),
	}
}

// Observe records a telemetry sample for each worker in stats under gid at the
// given time. Workers not present in stats are not touched here; use
// EvictAbsent to age out absent workers.
func (h *WorkerHistory) Observe(gid string, stats []types.WorkerSnapshot, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	workers, ok := h.data[gid]
	if !ok {
		workers = make(map[int]*ring)
		h.data[gid] = workers
		h.absent[gid] = make(map[int]int)
	}
	for _, s := range stats {
		r, ok := workers[s.WorkerID]
		if !ok {
			r = newRing(h.windowCap)
			workers[s.WorkerID] = r
		}
		r.push(timedSnapshot{WorkerSnapshot: s, at: now})
	}
}

// EvictAbsent ages out workers under gid that are not in presentIDs. A worker
// absent for more than evictAfterTicks consecutive calls is removed. Present
// workers have their absent counter reset.
func (h *WorkerHistory) EvictAbsent(gid string, presentIDs map[int]bool, evictAfterTicks int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	workers, ok := h.data[gid]
	if !ok {
		return
	}
	absent := h.absent[gid]
	for wid := range workers {
		if presentIDs[wid] {
			absent[wid] = 0
			continue
		}
		absent[wid]++
		if absent[wid] > evictAfterTicks {
			delete(workers, wid)
			delete(absent, wid)
		}
	}
	if len(workers) == 0 {
		delete(h.data, gid)
		delete(h.absent, gid)
	}
}

// Window returns the chronological sample window for (gid, workerID), or nil.
func (h *WorkerHistory) Window(gid string, workerID int) []timedSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	workers, ok := h.data[gid]
	if !ok {
		return nil
	}
	r, ok := workers[workerID]
	if !ok {
		return nil
	}
	return r.window()
}

// RemoveGID drops all history for a gid.
func (h *WorkerHistory) RemoveGID(gid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.data, gid)
	delete(h.absent, gid)
}

// ActiveGIDs returns the gids currently holding history, sorted for stability.
func (h *WorkerHistory) ActiveGIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	gids := make([]string, 0, len(h.data))
	for gid := range h.data {
		gids = append(gids, gid)
	}
	sort.Strings(gids)
	return gids
}

// WorkerIDs returns the workerIDs with history under gid, sorted for stability.
func (h *WorkerHistory) WorkerIDs(gid string) []int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	workers, ok := h.data[gid]
	if !ok {
		return nil
	}
	ids := make([]int, 0, len(workers))
	for wid := range workers {
		ids = append(ids, wid)
	}
	sort.Ints(ids)
	return ids
}

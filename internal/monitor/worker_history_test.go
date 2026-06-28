package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/surge/engine/types"
)

func TestWorkerHistory_ObserveAndWindow(t *testing.T) {
	h := NewWorkerHistory(4)
	now := time.Now()
	h.Observe("sg_a", []types.WorkerSnapshot{
		{WorkerID: 1, EMASpeed: 100},
		{WorkerID: 2, EMASpeed: 200},
	}, now)

	w1 := h.Window("sg_a", 1)
	if len(w1) != 1 || w1[0].EMASpeed != 100 {
		t.Fatalf("window(1) = %+v, want single 100", w1)
	}
	w2 := h.Window("sg_a", 2)
	if len(w2) != 1 || w2[0].EMASpeed != 200 {
		t.Fatalf("window(2) = %+v, want single 200", w2)
	}
	if h.Window("sg_a", 99) != nil {
		t.Error("missing worker should return nil window")
	}
}

func TestWorkerHistory_RingOverflow(t *testing.T) {
	h := NewWorkerHistory(3)
	base := time.Now()
	for i := 0; i < 5; i++ {
		h.Observe("sg_a", []types.WorkerSnapshot{{WorkerID: 1, EMASpeed: float64(i)}}, base.Add(time.Duration(i)*time.Second))
	}
	w := h.Window("sg_a", 1)
	if len(w) != 3 {
		t.Fatalf("window len = %d, want 3 (ring capacity)", len(w))
	}
	// Should contain the last 3 samples (2,3,4) in chronological order.
	want := []float64{2, 3, 4}
	for i, s := range w {
		if s.EMASpeed != want[i] {
			t.Errorf("window[%d].EMASpeed = %v, want %v", i, s.EMASpeed, want[i])
		}
	}
}

func TestWorkerHistory_EvictAbsent(t *testing.T) {
	h := NewWorkerHistory(4)
	now := time.Now()
	h.Observe("sg_a", []types.WorkerSnapshot{{WorkerID: 1}, {WorkerID: 2}}, now)

	// Worker 1 keeps appearing; worker 2 goes absent.
	for tick := 0; tick < cdnWorkerEvictTicks+1; tick++ {
		now = now.Add(time.Second)
		h.Observe("sg_a", []types.WorkerSnapshot{{WorkerID: 1}}, now)
		h.EvictAbsent("sg_a", map[int]bool{1: true}, cdnWorkerEvictTicks)
	}
	if h.Window("sg_a", 2) != nil {
		t.Error("absent worker 2 should have been evicted")
	}
	if h.Window("sg_a", 1) == nil {
		t.Error("present worker 1 should still have history")
	}
}

func TestWorkerHistory_RemoveGID(t *testing.T) {
	h := NewWorkerHistory(4)
	h.Observe("sg_a", []types.WorkerSnapshot{{WorkerID: 1}}, time.Now())
	h.Observe("sg_b", []types.WorkerSnapshot{{WorkerID: 1}}, time.Now())
	h.RemoveGID("sg_a")
	if h.Window("sg_a", 1) != nil {
		t.Error("sg_a history should be removed")
	}
	if h.Window("sg_b", 1) == nil {
		t.Error("sg_b history should remain")
	}
}

func TestWorkerHistory_ActiveGIDsSorted(t *testing.T) {
	h := NewWorkerHistory(4)
	h.Observe("sg_c", []types.WorkerSnapshot{{WorkerID: 1}}, time.Now())
	h.Observe("sg_a", []types.WorkerSnapshot{{WorkerID: 1}}, time.Now())
	h.Observe("sg_b", []types.WorkerSnapshot{{WorkerID: 1}}, time.Now())
	got := h.ActiveGIDs()
	want := []string{"sg_a", "sg_b", "sg_c"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("ActiveGIDs = %v, want %v", got, want)
	}
}

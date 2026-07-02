package monitor

import (
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

func TestCurrentTickInterval_SurgeOnly_UsesHeadless(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// SurgeEngine with nil service → IsSurgeActive() returns false
	// So !IsSurgeActive() is true → returns windowInterval (1s)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)

	m := &Monitor{
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{},
		prevWaitingGids:  map[string]bool{},
		engine:           hybrid,
	}

	// With nil service, IsSurgeActive()=false, so it uses windowInterval
	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s when surge not active, got %v", d)
	}
}

func TestCurrentTickInterval_HasAria2Tasks_UsesWindow(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)

	m := &Monitor{
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{"ar_123": true},
		prevWaitingGids:  map[string]bool{},
		engine:           hybrid,
	}

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s with Aria2 tasks, got %v", d)
	}
}

func TestCurrentTickInterval_Aria2InWaiting_UsesWindow(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)

	m := &Monitor{
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{},
		prevWaitingGids:  map[string]bool{"ar_456": true},
		engine:           hybrid,
	}

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s with Aria2 waiting tasks, got %v", d)
	}
}

func TestCurrentTickInterval_NoWindow_UsesHeadless(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(false)
	defer State.SetWindowExists(prevWindow)

	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)

	m := &Monitor{
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{"ar_123": true},
		prevWaitingGids:  map[string]bool{},
		engine:           hybrid,
	}

	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Errorf("expected 5s no window, got %v", d)
	}
}

func TestCurrentTickInterval_SurgeActiveWithAria2Tasks_Active_UsesWindow(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{"ar_123": true},
		prevWaitingGids:  map[string]bool{},
		engine:           engine,
	}

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s with Aria2 active tasks even when surge is active, got %v", d)
	}
}

func TestCurrentTickInterval_SurgeActiveWithAria2Tasks_Waiting_UsesWindow(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{},
		prevWaitingGids:  map[string]bool{"ar_456": true},
		engine:           engine,
	}

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s with Aria2 waiting tasks even when surge is active, got %v", d)
	}
}

func TestCurrentTickInterval_SurgeActiveOnlySurgeTasks_UsesHeadless(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{"sg_001": true},
		prevWaitingGids:  map[string]bool{},
		engine:           engine,
	}

	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Errorf("expected 5s with only Surge tasks, got %v", d)
	}
}

func TestCurrentTickInterval_NoPendingComplete_SurgeOnly_Headless(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{"sg_001": true},
		prevWaitingGids:  map[string]bool{},
		engine:           engine,
	}

	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Errorf("expected 5s with only Surge tasks (no pending complete), got %v", d)
	}
}

func TestCurrentTickInterval_ShouldFetchStoppedUntil_UsesWindow(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:          1 * time.Second,
		headlessInterval:        5 * time.Second,
		prevActiveGids:          map[string]bool{"sg_001": true},
		prevWaitingGids:         map[string]bool{},
		engine:                  engine,
		shouldFetchStoppedUntil: time.Now().Add(10 * time.Second),
	}

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s during shouldFetchStoppedUntil window, got %v", d)
	}
}

func TestCurrentTickInterval_ShouldFetchStoppedExpired_UsesHeadless(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:          1 * time.Second,
		headlessInterval:        5 * time.Second,
		prevActiveGids:          map[string]bool{"sg_001": true},
		prevWaitingGids:         map[string]bool{},
		engine:                  engine,
		shouldFetchStoppedUntil: time.Now().Add(-1 * time.Second), // expired
	}

	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Errorf("expected 5s after shouldFetchStoppedUntil expired, got %v", d)
	}
}

func TestCurrentTickInterval_ShouldFetchStoppedUntil_LifecycleTransition(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{"sg_001": true},
		prevWaitingGids:  map[string]bool{},
		engine:           engine,
		mu:               sync.Mutex{},
	}

	// Phase 1: Before any complete event — 5s headless
	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Fatalf("phase 1: expected 5s (no shouldFetchStoppedUntil), got %v", d)
	}

	// Phase 2: Simulate complete event setting shouldFetchStoppedUntil
	m.mu.Lock()
	m.shouldFetchStoppedUntil = time.Now().Add(1500 * time.Millisecond)
	m.mu.Unlock()

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Fatalf("phase 2: expected 1s (shouldFetchStoppedUntil active), got %v", d)
	}

	// Phase 3: Simulate window expiry
	m.mu.Lock()
	m.shouldFetchStoppedUntil = time.Now().Add(-1 * time.Millisecond)
	m.mu.Unlock()

	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Fatalf("phase 3: expected 5s (shouldFetchStoppedUntil expired), got %v", d)
	}
}

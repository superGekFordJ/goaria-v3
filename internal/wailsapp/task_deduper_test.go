package wailsapp

import "testing"

// Every Service the App hands out — UI adds, the extension single-item adder,
// and snapshot-fixed extractor commit services — must coalesce engine adds
// through the one registry the App owns.

func TestAppAddDeduper_TaskServiceInjectsSharedRegistry(t *testing.T) {
	app := NewApp(Options{})
	if app.deduper == nil {
		t.Fatal("NewApp must own a non-nil deduper")
	}
	svc1 := app.taskService()
	svc2 := app.taskService()
	if svc1.Deduper != app.deduper || svc2.Deduper != app.deduper {
		t.Fatal("taskService must inject the App-owned deduper")
	}
	if svc1.Deduper != svc2.Deduper {
		t.Fatal("taskService instances from one App must share one registry")
	}
}

func TestAppAddDeduper_DistinctAppsIsolated(t *testing.T) {
	a := NewApp(Options{})
	b := NewApp(Options{})
	if a.deduper == nil || b.deduper == nil {
		t.Fatal("each App must own a deduper")
	}
	if a.deduper == b.deduper || a.taskService().Deduper == b.taskService().Deduper {
		t.Fatal("dedup claims must not leak across App instances")
	}
}

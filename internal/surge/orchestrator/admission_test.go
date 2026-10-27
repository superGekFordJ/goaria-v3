package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

// A refused enqueue must not persist a master row, leave its .surge
// reservation, or publish EventQueued.
func TestEnqueue_RejectedAdmissionDoesNotPersistOrPublish(t *testing.T) {
	dir := testutil.SetupStateDB(t)
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
	}))
	t.Cleanup(server.Close)
	pool := scheduler.New(make(chan types.DownloadEvent, 16), 1)
	pool.GracefulShutdown()
	bus := NewEventBus()
	// Reliable feed: Shutdown drains every accepted lifecycle event before the
	// stream closes, so "no EventQueued" below cannot pass on a dropped tail.
	// The drain must run concurrently — a reliable out channel is unbuffered,
	// so Shutdown would wedge waiting for a consumer that has not started.
	events, _ := bus.SubscribeLifecycle()
	drained := make(chan []types.DownloadEvent, 1)
	go func() {
		var got []types.DownloadEvent
		for event := range events {
			got = append(got, event)
		}
		drained <- got
	}()
	mgr := NewLifecycleManager(pool, bus, nil)
	defer mgr.Shutdown()
	const id = "rejected-admission"
	_, _, err := mgr.EnqueueWithID(context.Background(), &DownloadRequest{
		URL: server.URL, Path: dir, Filename: "rejected.bin",
		IsExplicitCategory: true, // Pin finalPath == dir regardless of settings.
		Headers:            map[string]string{"Authorization": "test"},
	}, id)
	if !errors.Is(err, types.ErrIDExists) {
		t.Fatalf("error=%v, want ErrIDExists", err)
	}
	master, err := store.LoadMasterList()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range master.Downloads {
		if record.ID == id {
			t.Fatal("rejected task left persisted record")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "rejected.bin") + types.IncompleteSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected working file remains: %v", err)
	}
	mgr.Shutdown() // Drain publications before inspecting the subscriber.
	for _, event := range <-drained {
		if event.Type == types.EventQueued && event.DownloadID == id {
			t.Fatal("rejected task published EventQueued")
		}
	}
}

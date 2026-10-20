package scheduler

import (
	"testing"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
)

// FORK-PATCH: managed Add runtime materialization + Admit copy-on-write.
// Replaces the retired TightenOnPickup hook coverage — runtime authorization
// now happens only through Admit, never in the worker pickup path.

func managedCandidateFor(t *testing.T, pool *Scheduler, id string) AdmissionCandidate {
	t.Helper()
	for _, c := range pool.AdmissionSnapshot().Candidates {
		if c.Key.ID == id {
			return c
		}
	}
	t.Fatalf("snapshot missing candidate %s", id)
	return AdmissionCandidate{}
}

func TestScheduler_ManagedAddRuntimeReady(t *testing.T) {
	cases := []struct {
		name      string
		runtime   *types.RuntimeConfig
		rangeMode types.RangeAcquisitionMode
		strategy  types.DownloadStrategy
		wantWork  int
		wantChunk int64
	}{
		{
			name:      "nil runtime gets positive default ceiling",
			runtime:   nil,
			wantWork:  types.PerDownloadMax,
			wantChunk: types.MinChunk,
		},
		{
			name:      "auto workers materialized",
			runtime:   &types.RuntimeConfig{Workers: 0},
			wantWork:  types.PerDownloadMax,
			wantChunk: 0,
		},
		{
			name:      "explicit workers preserved, never widened",
			runtime:   &types.RuntimeConfig{Workers: 4, MinChunkSize: 8 << 20},
			wantWork:  4,
			wantChunk: 8 << 20,
		},
		{
			name:      "sequential stays single-threaded",
			runtime:   &types.RuntimeConfig{Workers: 16, SequentialDownload: true},
			wantWork:  1,
			wantChunk: 0,
		},
		{
			name:      "range-unsupported stays single-threaded",
			runtime:   &types.RuntimeConfig{Workers: 8},
			rangeMode: types.RangeAcquireRangeUnsupported,
			wantWork:  1,
			wantChunk: 0,
		},
		{
			// HLS is exempt only from the range_unsupported single-thread
			// lock; a positive caller ceiling still bounds the workers.
			name:      "hls exempt from range-unsupported lock",
			runtime:   &types.RuntimeConfig{Workers: 8},
			rangeMode: types.RangeAcquireRangeUnsupported,
			strategy:  types.StrategyHLS,
			wantWork:  8,
			wantChunk: 0,
		},
		{
			name:     "hls auto workers materialized",
			runtime:  &types.RuntimeConfig{Workers: 0},
			strategy: types.StrategyHLS,
			wantWork: types.PerDownloadMax,
		},
		{
			name:      "hls sequential still single-threaded",
			runtime:   &types.RuntimeConfig{Workers: 16, SequentialDownload: true},
			strategy:  types.StrategyHLS,
			wantWork:  1,
			wantChunk: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan types.DownloadEvent, 8)
			pool, err := NewManaged(ch, 4)
			if err != nil {
				t.Fatalf("NewManaged: %v", err)
			}
			t.Cleanup(pool.GracefulShutdown)

			id := "rt-" + tc.name
			if !pool.Add(types.DownloadRecord{
				ID:                   id,
				URL:                  "http://example.com/f",
				OutputPath:           t.TempDir(),
				Filename:             "f.bin",
				ProgressState:        progress.New(id, 0),
				Runtime:              tc.runtime,
				RangeAcquisitionMode: tc.rangeMode,
				Strategy:             tc.strategy,
			}) {
				t.Fatal("managed Add refused")
			}

			cand := managedCandidateFor(t, pool, id)
			if !cand.RuntimeReady || cand.Workers != tc.wantWork || cand.MinChunkSize != tc.wantChunk {
				t.Fatalf("candidate = %+v, want RuntimeReady/Workers=%d/MinChunk=%d", cand, tc.wantWork, tc.wantChunk)
			}
			if res := pool.Admit(cand.Key, tc.wantWork, tc.wantChunk); res != AdmitAccepted {
				t.Fatalf("Admit = %v, want Accepted", res)
			}
		})
	}
}

// Admit must patch a fresh Runtime copy — the caller's original ceiling stays
// untouched and only Workers/MinChunkSize may tighten.
func TestScheduler_ManagedAdmitRuntimeCOW(t *testing.T) {
	ch := make(chan types.DownloadEvent, 8)
	pool, err := NewManaged(ch, 4)
	if err != nil {
		t.Fatalf("NewManaged: %v", err)
	}
	t.Cleanup(pool.GracefulShutdown)

	const id = "cow-1"
	orig := &types.RuntimeConfig{
		Workers:            8,
		MinChunkSize:       4 << 20,
		UserAgent:          "ua-mark",
		SequentialDownload: false,
	}
	if !pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           "http://example.com/f",
		OutputPath:    t.TempDir(),
		Filename:      "f.bin",
		ProgressState: progress.New(id, 0),
		Runtime:       orig,
	}) {
		t.Fatal("managed Add refused")
	}

	cand := managedCandidateFor(t, pool, id)
	if res := pool.Admit(cand.Key, 2, 1<<20); res != AdmitAccepted {
		t.Fatalf("Admit = %v, want Accepted", res)
	}

	pool.mu.RLock()
	rt := pool.queued[id].cfg.Runtime
	pool.mu.RUnlock()
	if rt == nil || rt == orig {
		t.Fatal("Admit did not copy-on-write the runtime ceiling")
	}
	if rt.Workers != 2 || rt.MinChunkSize != 1<<20 || rt.UserAgent != "ua-mark" {
		t.Fatalf("patched runtime = %+v", rt)
	}
	if orig.Workers != 8 || orig.MinChunkSize != 4<<20 {
		t.Fatalf("caller runtime mutated: %+v", orig)
	}
}

// A legacy (non-managed) pool keeps queueing without runtime materialization:
// candidates report RuntimeReady=false and the worker's own nil-Runtime
// fallback still applies at claim.
func TestScheduler_LegacyAddKeepsAutoRuntime(t *testing.T) {
	ch := make(chan types.DownloadEvent, 8)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "legacy-auto"
	if !pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           "http://example.com/f",
		OutputPath:    t.TempDir(),
		Filename:      "f.bin",
		ProgressState: progress.New(id, 0),
	}) {
		t.Fatal("legacy Add refused")
	}

	snap := pool.AdmissionSnapshot()
	if snap.Managed {
		t.Fatal("legacy pool reported managed")
	}
	for _, c := range snap.Candidates {
		if c.Key.ID == id && c.RuntimeReady {
			t.Fatal("legacy candidate runtime was materialized")
		}
	}
}

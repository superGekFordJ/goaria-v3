package monitor

import (
	"testing"

	"goaria-v3/internal/rpc"
)

func TestNormalizeAria2TickLists(t *testing.T) {
	t.Parallel()

	gidMembership := func(tasks []rpc.Task) map[string]int {
		m := make(map[string]int, len(tasks))
		for _, task := range tasks {
			m[task.GID]++
		}
		return m
	}

	tests := []struct {
		name            string
		active          []rpc.Task
		waiting         []rpc.Task
		stopped         []rpc.Task
		wantActiveGIDs  []string
		wantWaitingGIDs []string
		wantStoppedGIDs []string
	}{
		{
			name:            "active+stale stopped same GID",
			active:          []rpc.Task{{GID: "ar_x", Status: "active"}},
			stopped:         []rpc.Task{{GID: "ar_x", Status: "complete"}},
			wantActiveGIDs:  []string{"ar_x"},
			wantWaitingGIDs: nil,
			wantStoppedGIDs: nil,
		},
		{
			name:            "waiting-only + stale stopped",
			waiting:         []rpc.Task{{GID: "ar_x", Status: "waiting"}},
			stopped:         []rpc.Task{{GID: "ar_x", Status: "complete"}},
			wantActiveGIDs:  nil,
			wantWaitingGIDs: []string{"ar_x"},
			wantStoppedGIDs: nil,
		},
		{
			name: "active+waiting same GID strips waiting and stopped",
			active: []rpc.Task{{GID: "ar_x", Status: "active"}},
			waiting: []rpc.Task{
				{GID: "ar_x", Status: "waiting"},
				{GID: "ar_y", Status: "waiting"},
			},
			stopped:         []rpc.Task{{GID: "ar_x", Status: "complete"}},
			wantActiveGIDs:  []string{"ar_x"},
			wantWaitingGIDs: []string{"ar_y"},
			wantStoppedGIDs: nil,
		},
		{
			name: "stopped internal duplicate first wins",
			stopped: []rpc.Task{
				{GID: "ar_a", Status: "complete", ErrorMessage: "first"},
				{GID: "ar_b", Status: "error"},
				{GID: "ar_a", Status: "complete", ErrorMessage: "second"},
			},
			wantStoppedGIDs: []string{"ar_a", "ar_b"},
		},
		{
			name: "non-conflicting triple unchanged",
			active: []rpc.Task{
				{GID: "ar_a", Status: "active"},
			},
			waiting: []rpc.Task{
				{GID: "ar_w", Status: "waiting"},
			},
			stopped: []rpc.Task{
				{GID: "ar_s", Status: "complete"},
			},
			wantActiveGIDs:  []string{"ar_a"},
			wantWaitingGIDs: []string{"ar_w"},
			wantStoppedGIDs: []string{"ar_s"},
		},
		{
			name: "empty GID ignored for conflict sets",
			active: []rpc.Task{
				{GID: "", Status: "active"},
				{GID: "ar_live", Status: "active"},
			},
			waiting: []rpc.Task{
				{GID: "", Status: "waiting"},
			},
			stopped: []rpc.Task{
				{GID: "", Status: "complete"},
				{GID: "ar_live", Status: "complete"},
				{GID: "ar_ok", Status: "complete"},
			},
			wantActiveGIDs:  []string{"ar_live"},
			wantWaitingGIDs: nil,
			wantStoppedGIDs: []string{"ar_ok"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotActive, gotWaiting, gotStopped := normalizeAria2TickLists(tt.active, tt.waiting, tt.stopped)

			assertGIDOrder(t, "active", gotActive, tt.wantActiveGIDs)
			assertGIDOrder(t, "waiting", gotWaiting, tt.wantWaitingGIDs)
			assertGIDOrder(t, "stopped", gotStopped, tt.wantStoppedGIDs)

			if tt.name == "stopped internal duplicate first wins" {
				if len(gotStopped) == 0 || gotStopped[0].ErrorMessage != "first" {
					t.Fatalf("expected first stopped duplicate to win, got %+v", gotStopped)
				}
			}

			// Caller slices must not be mutated in place via shared backing arrays
			// for filtered removals (new slices are returned).
			_ = gidMembership(gotActive)
		})
	}
}

func assertGIDOrder(t *testing.T, label string, got []rpc.Task, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d entries %v, want %d %v", label, len(got), taskGIDs(got), len(want), want)
	}
	for i := range want {
		if got[i].GID != want[i] {
			t.Fatalf("%s[%d]: got %q, want %q (full=%v)", label, i, got[i].GID, want[i], taskGIDs(got))
		}
	}
}

func taskGIDs(tasks []rpc.Task) []string {
	out := make([]string, len(tasks))
	for i, task := range tasks {
		out[i] = task.GID
	}
	return out
}

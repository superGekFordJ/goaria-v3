package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

func TestNormalizeAria2TickLists(t *testing.T) {
	t.Parallel()

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
			name:   "active+waiting same GID strips waiting and stopped",
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

			activeIn := append([]rpc.Task(nil), tt.active...)
			waitingIn := append([]rpc.Task(nil), tt.waiting...)
			stoppedIn := append([]rpc.Task(nil), tt.stopped...)
			activeLen, waitingLen, stoppedLen := len(activeIn), len(waitingIn), len(stoppedIn)
			var activeSentinel, waitingSentinel, stoppedSentinel string
			if activeLen > 0 {
				activeSentinel = activeIn[0].ErrorMessage + "|sentinel-a"
				activeIn[0].ErrorMessage = activeSentinel
			}
			if waitingLen > 0 {
				waitingSentinel = waitingIn[0].ErrorMessage + "|sentinel-w"
				waitingIn[0].ErrorMessage = waitingSentinel
			}
			if stoppedLen > 0 && tt.name != "stopped internal duplicate first wins" {
				stoppedSentinel = stoppedIn[0].ErrorMessage + "|sentinel-s"
				stoppedIn[0].ErrorMessage = stoppedSentinel
			}

			gotActive, gotWaiting, gotStopped := normalizeAria2TickLists(activeIn, waitingIn, stoppedIn)

			assertGIDOrder(t, "active", gotActive, tt.wantActiveGIDs)
			assertGIDOrder(t, "waiting", gotWaiting, tt.wantWaitingGIDs)
			assertGIDOrder(t, "stopped", gotStopped, tt.wantStoppedGIDs)

			if tt.name == "stopped internal duplicate first wins" {
				if len(gotStopped) == 0 || gotStopped[0].ErrorMessage != "first" {
					t.Fatalf("expected first stopped duplicate to win, got %+v", gotStopped)
				}
				// Mutate a later duplicate field and ensure caller slice length stays intact.
				stoppedSentinel = stoppedIn[0].ErrorMessage
			}

			if len(activeIn) != activeLen || len(waitingIn) != waitingLen || len(stoppedIn) != stoppedLen {
				t.Fatalf("caller slice lengths mutated: active %d→%d waiting %d→%d stopped %d→%d",
					activeLen, len(activeIn), waitingLen, len(waitingIn), stoppedLen, len(stoppedIn))
			}
			if activeLen > 0 && activeIn[0].ErrorMessage != activeSentinel {
				t.Fatal("caller active slice content mutated")
			}
			if waitingLen > 0 && waitingIn[0].ErrorMessage != waitingSentinel {
				t.Fatal("caller waiting slice content mutated")
			}
			if stoppedLen > 0 && stoppedSentinel != "" && stoppedIn[0].ErrorMessage != stoppedSentinel {
				t.Fatal("caller stopped slice content mutated")
			}
			if tt.name == "stopped internal duplicate first wins" && stoppedIn[0].ErrorMessage != "first" {
				t.Fatal("caller stopped slice content mutated for first-wins case")
			}
		})
	}
}

func TestDropDeletedStopped(t *testing.T) {
	t.Parallel()

	deleted := map[string]time.Time{
		"ar_gone": time.Now(),
	}
	in := []rpc.Task{
		{GID: "ar_keep", Status: "complete"},
		{GID: "ar_gone", Status: "complete"},
		{GID: "", Status: "complete"},
		{GID: "ar_other", Status: "error"},
	}
	got := dropDeletedStopped(deleted, in)
	assertGIDOrder(t, "stopped", got, []string{"ar_keep", "ar_other"})

	unchanged := dropDeletedStopped(nil, in)
	if len(unchanged) != len(in) {
		t.Fatalf("empty deleted map should leave stopped intact, got %d want %d", len(unchanged), len(in))
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

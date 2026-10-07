package tasks

import (
	"reflect"
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

// seedChunkMonitor installs a test monitor carrying a chunk snapshot cache
// and returns it; State.Monitor and State.Tracker are restored on cleanup.
func seedChunkMonitor(t *testing.T, f taskDetailFixture) *monitor.Monitor {
	t.Helper()
	origMon := monitor.State.GetMonitor()
	mon := monitor.NewMonitorForTest(events.NewHub(nil))
	monitor.State.SetMonitor(mon)
	// NewMonitorForTest re-seeds the global tracker; restore the fixture one.
	monitor.State.SetTracker(f.tracker)
	t.Cleanup(func() { monitor.State.SetMonitor(origMon) })
	return mon
}

func TestTaskDetail_SgChunkSnapshotExposed(t *testing.T) {
	f := setupTaskDetailTest(t)
	mon := seedChunkMonitor(t, f)
	gid := "sg_chunk1"
	monitor.Cache.AddSgTask(rpc.Task{GID: gid, Status: "active"}, "active")

	// packed: byte0 chunks 0..3 = {0,1,2,0} -> 4+32 = 36;
	//         byte1 chunks 4..7 = {2,2,1,0} -> 2+8+16 = 26.
	mon.GetChunkSnapshots().Set(gid, []byte{36, 26}, 8, 1<<20)

	d := detailFor(t, f.svc, gid)
	if want := []int{0, 1, 2, 0, 2, 2, 1, 0}; !reflect.DeepEqual(d.ChunkStates, want) {
		t.Fatalf("ChunkStates = %v, want %v", d.ChunkStates, want)
	}
	if d.ChunkCount != 8 {
		t.Fatalf("ChunkCount = %d, want 8", d.ChunkCount)
	}
	if d.ChunkSize != 1<<20 {
		t.Fatalf("ChunkSize = %d, want %d", d.ChunkSize, 1<<20)
	}
}

func TestTaskDetail_ChunkFieldsAbsentWithoutSnapshot(t *testing.T) {
	f := setupTaskDetailTest(t)
	mon := seedChunkMonitor(t, f)

	// ar_ never exposes chunk fields, even if a stray entry exists.
	monitor.Cache.UpdateFromAria2([]rpc.Task{arTask("ar_c", "active", "http://a/x")}, nil, nil)
	mon.GetChunkSnapshots().Set("ar_c", []byte{36}, 4, 512)
	d := detailFor(t, f.svc, "ar_c")
	if d.ChunkStates != nil || d.ChunkCount != 0 || d.ChunkSize != 0 {
		t.Fatalf("ar_ leaked chunk fields: %+v", d)
	}

	// sg_ without a snapshot stays silent (no fabrication on cold start).
	monitor.Cache.AddSgTask(rpc.Task{GID: "sg_nosnap", Status: "active"}, "active")
	d = detailFor(t, f.svc, "sg_nosnap")
	if d.ChunkStates != nil || d.ChunkCount != 0 || d.ChunkSize != 0 {
		t.Fatalf("sg_ without snapshot fabricated chunk fields: %+v", d)
	}
}

// Terminal clears the snapshot; a later pull must omit the fields even though
// the task itself remains addressable (stopped/history).
func TestTaskDetail_ChunkFieldsAbsentAfterClear(t *testing.T) {
	f := setupTaskDetailTest(t)
	mon := seedChunkMonitor(t, f)
	gid := "sg_gone"
	monitor.Cache.AddSgTask(rpc.Task{GID: gid, Status: "complete"}, "stopped")

	mon.GetChunkSnapshots().Set(gid, []byte{0x55}, 4, 512)
	if d := detailFor(t, f.svc, gid); d.ChunkStates == nil {
		t.Fatal("setup: expected chunk fields before clear")
	}

	mon.GetChunkSnapshots().Remove(gid)
	d := detailFor(t, f.svc, gid)
	if d.ChunkStates != nil || d.ChunkCount != 0 || d.ChunkSize != 0 {
		t.Fatalf("cleared snapshot still surfaced: %+v", d)
	}
}

func TestUnpackChunkStates(t *testing.T) {
	if got := unpackChunkStates(nil, 4); got != nil {
		t.Fatalf("nil packed = %v, want nil", got)
	}
	if got := unpackChunkStates([]byte{1}, 0); got != nil {
		t.Fatalf("count<=0 = %v, want nil", got)
	}
	if got := unpackChunkStates([]byte{}, 4); got != nil {
		t.Fatalf("empty packed = %v, want nil", got)
	}
	// Short bitmap pads the tail with 0 (pending), matching engine restore.
	if got, want := unpackChunkStates([]byte{0x55}, 8), []int{1, 1, 1, 1, 0, 0, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("short packed = %v, want %v", got, want)
	}
	// All three states plus the unused 3 value pass through verbatim.
	if got, want := unpackChunkStates([]byte{0b11_10_01_00}, 4), []int{0, 1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("packed = %v, want %v", got, want)
	}
}

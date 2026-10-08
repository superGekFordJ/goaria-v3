package tasks

import (
	"strings"

	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	surgetypes "goaria-v3/internal/surge/types"
)

// GetTaskDetails aggregates on-demand task facts from in-memory sources only
// (monitor.Cache, history, tracker, Surge masterCache). It must never issue
// engine RPC, call store.*, or touch disk: it runs on user pull, not the
// delta hot path.
func (s *Service) GetTaskDetails(gids []string) map[string]TaskDetailEnvelope {
	out := make(map[string]TaskDetailEnvelope, len(gids))
	tracker := monitor.State.GetTracker()
	surgeEng := s.surgeEngineRef()
	var snaps *monitor.ChunkSnapshotCache
	if mon := monitor.State.GetMonitor(); mon != nil {
		snaps = mon.GetChunkSnapshots()
	}

	for _, raw := range gids {
		gid := strings.TrimSpace(raw)
		if gid == "" {
			continue
		}
		if _, seen := out[gid]; seen {
			continue
		}
		out[gid] = buildTaskDetail(gid, tracker, surgeEng, snaps)
	}
	return out
}

func (s *Service) surgeEngineRef() *rpc.SurgeEngine {
	if s == nil {
		return nil
	}
	hybrid, ok := s.Engine.(*rpc.HybridEngine)
	if !ok || hybrid == nil {
		return nil
	}
	se, ok := hybrid.SurgeEngineRef()
	if !ok {
		return nil
	}
	return se
}

func buildTaskDetail(gid string, tracker *monitor.TaskTracker, surgeEng *rpc.SurgeEngine, snaps *monitor.ChunkSnapshotCache) TaskDetailEnvelope {
	cached, _, inCache := monitor.Cache.GetTask(gid)
	hist, inHistory := history.Get(gid)
	var tracked monitor.TrackedTask
	inTracker := false
	if tracker != nil {
		tracked, inTracker = tracker.GetTrackedTask(gid)
	}
	var master surgetypes.DownloadRecord
	inMaster := false
	isSurge := monitor.IsSgGid(gid)
	if isSurge && surgeEng != nil {
		master, inMaster = surgeEng.GetMasterCacheEntry(strings.TrimPrefix(gid, "sg_"))
	}

	if !inCache && !inHistory && !inTracker && !inMaster {
		return TaskDetailEnvelope{Found: false}
	}

	// The four sources above are read under independent locks, so the snapshot
	// can mix epochs; the per-field zero checks below degrade a mixed read to
	// hidden cells rather than wrong values.
	d := &TaskDetail{GID: gid}
	// Master completion fields are only meaningful once Surge marked it done.
	masterDone := inMaster && master.Status == "completed"

	if inMaster && master.CreatedAt > 0 {
		d.AddedAt = master.CreatedAt
	} else if inTracker && !tracked.AddedAt.IsZero() {
		d.AddedAt = tracked.AddedAt.Unix()
	}

	if masterDone && master.CompletedAt > 0 {
		d.CompletedAt = master.CompletedAt
	} else if inHistory {
		d.CompletedAt = hist.CompletedAt
	}

	if masterDone && master.TimeTaken > 0 {
		// master TimeTaken is already milliseconds.
		d.TimeTakenMs = master.TimeTaken
		d.AvgSpeed = int64(master.AvgSpeed)
	} else if inHistory {
		d.TimeTakenMs = hist.DurationMs
		d.AvgSpeed = hist.AvgSpeed
	}

	// The live tracker entry holds the real peak; only terminal copies ever
	// receive the AvgSpeed substitute.
	if inTracker && tracked.PeakSpeed > 0 {
		d.PeakSpeed = tracked.PeakSpeed
	} else if inHistory {
		d.PeakSpeed = hist.PeakSpeed
	}

	if inMaster {
		d.URIs = dedupeURIs(append([]string{master.URL}, master.Mirrors...))
	}
	if len(d.URIs) == 0 {
		d.URIs = fallbackURIs(gid, cached, inCache, tracked, inTracker, hist, inHistory)
	}

	// A snapshot presence alone fills the chunk fields: no live/active gate.
	// Paused tasks legitimately serve their last (frozen) bitmap.
	if isSurge && snaps != nil {
		if snap, ok := snaps.Get(gid); ok {
			d.ChunkStates = unpackChunkStates(snap.Bitmap, snap.ChunkCount)
			d.ChunkCount = snap.ChunkCount
			d.ChunkSize = snap.ChunkSize
			if len(snap.ChunkProgress) > 0 {
				d.ChunkProgress = snap.ChunkProgress
			}
		}
	}

	return TaskDetailEnvelope{Found: true, Detail: d}
}

// unpackChunkStates decodes the engine's packed chunk bitmap (2 bits per
// chunk, 4 chunks per byte, LSB-first) into a per-chunk status array. Short
// bitmaps pad with 0 (pending), matching the engine's restore default.
func unpackChunkStates(packed []byte, count int) []int {
	if count <= 0 || len(packed) == 0 {
		return nil
	}
	states := make([]int, count)
	for i := range count {
		byteIndex := i / 4
		if byteIndex >= len(packed) {
			break
		}
		states[i] = int((packed[byteIndex] >> ((i % 4) * 2)) & 3)
	}
	return states
}

func fallbackURIs(gid string, cached rpc.Task, inCache bool, tracked monitor.TrackedTask, inTracker bool, hist history.HistoryEntry, inHistory bool) []string {
	if inCache && len(cached.Files) > 0 {
		uris := make([]string, 0, len(cached.Files[0].Uris))
		for _, u := range cached.Files[0].Uris {
			uris = append(uris, u.Uri)
		}
		if out := dedupeURIs(uris); len(out) > 0 {
			return out
		}
	}
	if meta := monitor.Cache.GetMetadata(gid); meta != nil {
		if out := dedupeURIs([]string{meta.SourceURL}); len(out) > 0 {
			return out
		}
	}
	if inTracker {
		if out := dedupeURIs([]string{tracked.SourceURL}); len(out) > 0 {
			return out
		}
	}
	if inHistory {
		return dedupeURIs([]string{hist.Source})
	}
	return nil
}

// dedupeURIs keeps first-seen order so the primary source stays first.
func dedupeURIs(uris []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(uris))
	for _, u := range uris {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

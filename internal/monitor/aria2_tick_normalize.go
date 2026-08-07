package monitor

import "goaria-v3/internal/rpc"

// normalizeAria2TickLists canonicalizes Aria2 tick list triples with precedence
// active > waiting > stopped. Waiting entries that collide with active are
// dropped; stopped entries that collide with the live set are dropped; stopped
// is also deduped by GID (first occurrence wins). Empty GIDs are skipped.
// Returns new slices; caller slices are not mutated.
func normalizeAria2TickLists(active, waiting, stopped []rpc.Task) (normActive, normWaiting, normStopped []rpc.Task) {
	activeGIDs := make(map[string]struct{}, len(active))
	normActive = make([]rpc.Task, 0, len(active))
	for _, t := range active {
		if t.GID == "" {
			continue
		}
		normActive = append(normActive, t)
		activeGIDs[t.GID] = struct{}{}
	}

	normWaiting = make([]rpc.Task, 0, len(waiting))
	waitingGIDs := make(map[string]struct{}, len(waiting))
	for _, t := range waiting {
		if t.GID == "" {
			continue
		}
		if _, inActive := activeGIDs[t.GID]; inActive {
			continue
		}
		normWaiting = append(normWaiting, t)
		waitingGIDs[t.GID] = struct{}{}
	}

	live := make(map[string]struct{}, len(activeGIDs)+len(waitingGIDs))
	for gid := range activeGIDs {
		live[gid] = struct{}{}
	}
	for gid := range waitingGIDs {
		live[gid] = struct{}{}
	}

	seenStopped := make(map[string]struct{}, len(stopped))
	normStopped = make([]rpc.Task, 0, len(stopped))
	for _, t := range stopped {
		if t.GID == "" {
			continue
		}
		if _, isLive := live[t.GID]; isLive {
			continue
		}
		if _, seen := seenStopped[t.GID]; seen {
			continue
		}
		seenStopped[t.GID] = struct{}{}
		normStopped = append(normStopped, t)
	}
	return normActive, normWaiting, normStopped
}

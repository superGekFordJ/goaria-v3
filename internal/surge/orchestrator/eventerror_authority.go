package orchestrator

import "goaria-v3/internal/surge/types"

// isTaskBackedResumeSnapshot reports whether rec carries a valid remaining-task
// set for Downloaded authority on EventError and EventPaused merges. Empty or
// invalid Tasks are treated as taskless; Downloaded/Tasks accounting equality is
// not required (saveStateSnapshot uses max(VerifiedProgress, TotalSize-remaining)).
func isTaskBackedResumeSnapshot(rec types.DownloadRecord) bool {
	if len(rec.Tasks) == 0 || rec.TotalSize <= 0 {
		return false
	}
	for _, task := range rec.Tasks {
		if task.Offset < 0 || task.Length <= 0 {
			return false
		}
		// Overflow-safe end check: Offset+Length <= TotalSize without wrap.
		if task.Length > rec.TotalSize || task.Offset > rec.TotalSize-task.Length {
			return false
		}
	}
	return true
}

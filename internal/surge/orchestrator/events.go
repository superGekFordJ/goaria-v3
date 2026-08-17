package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"goaria-v3/internal/surge/config"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

var (
	renameCompletedFile = retryRename
	copyCompletedFile   = utils.CopyFile
	notify              = utils.Notify
)

// FORK-PATCH: Debounced runtime.GC + FreeOSMemory after lifecycle terminal events.
var (
	gcTimer   *time.Timer
	gcTimerMu sync.Mutex
)

func triggerGC() {
	gcTimerMu.Lock()
	defer gcTimerMu.Unlock()

	if gcTimer != nil {
		gcTimer.Stop()
	}

	gcTimer = time.AfterFunc(2*time.Second, func() {
		runtime.GC()
		debug.FreeOSMemory()
	})
}

func stopPendingGC() {
	gcTimerMu.Lock()
	defer gcTimerMu.Unlock()
	if gcTimer != nil {
		gcTimer.Stop()
		gcTimer = nil
	}
}

// advanceRemainingTasks keeps saved chunk boundaries aligned when pause
// recovery only knows aggregate downloaded bytes, not per-task progress.
func advanceRemainingTasks(tasks []types.Task, consumed int64) []types.Task {
	if consumed <= 0 || len(tasks) == 0 {
		return tasks
	}

	out := make([]types.Task, 0, len(tasks))
	left := consumed
	for _, task := range tasks {
		if left <= 0 {
			out = append(out, task)
			continue
		}
		if task.Length <= left {
			left -= task.Length
			continue
		}
		task.Offset += left
		task.Length -= left
		left = 0
		out = append(out, task)
	}
	return out
}

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

// FORK-PATCH: AddToMasterList replaces the whole record. Sparse lifecycle
// writes must copy Range acquisition fields or enqueue-persisted mode is wiped.
func copyRangeAcquisition(dst *types.DownloadRecord, src *types.DownloadRecord) {
	if dst == nil || src == nil {
		return
	}
	if dst.RangeAcquisitionMode == "" {
		dst.RangeAcquisitionMode = src.RangeAcquisitionMode
	}
	if !dst.SkipServerProbe {
		dst.SkipServerProbe = src.SkipServerProbe
	}
}

func finalizeCompletedFile(finalPath string) error {
	if finalPath == "" {
		return fmt.Errorf("missing destination path for completed download")
	}

	surgePath := finalPath + types.IncompleteSuffix
	if err := renameCompletedFile(surgePath, finalPath); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			if err := copyCompletedFile(surgePath, finalPath); err != nil {
				_ = os.Remove(finalPath)
				return fmt.Errorf("copy completed file: %w", err)
			}
			if err := retryRemove(surgePath); err != nil {
				return fmt.Errorf("remove copied working file: %w", err)
			}
			return nil
		}
		if _, statErr := os.Stat(finalPath); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

// StartEventWorker listens to engine events and handles database persistence
// and file cleanup, ensuring the core engine remains stateless.
func (mgr *LifecycleManager) StartEventWorker(ch <-chan types.DownloadEvent) {
	for msg := range ch {
		m := msg
		switch m.Type {
		case types.EventStarted:
			// Persist the started record immediately so crash recovery and later lifecycle
			// events have a stable destination record even before the first pause snapshot.
			entry := types.DownloadRecord{
				ID:           m.DownloadID,
				URL:          m.URL,
				URLHash:      store.URLHash(m.URL),
				DestPath:     m.DestPath,
				Filename:     m.Filename,
				Status:       "downloading",
				TotalSize:    m.Total,
				Downloaded:   0,
				RateLimit:    m.RateLimit,
				RateLimitSet: m.RateLimitSet,
				Workers:      m.Workers,
				MinChunkSize: m.MinChunkSize,
			}
			existing, _ := store.GetDownload(m.DownloadID)
			if existing != nil {
				entry.Mirrors = append([]string(nil), existing.Mirrors...)
				if existing.Downloaded > 0 {
					entry.Downloaded = existing.Downloaded
				}
				if existing.TimeTaken > 0 {
					entry.TimeTaken = existing.TimeTaken
				}
				copyRangeAcquisition(&entry, existing)
			}
			if entry.RangeAcquisitionMode == "" {
				copyRangeAcquisition(&entry, m.State)
			}
			// FORK-PATCH: skip gob-zero insert only when no master row exists.
			// ProbeAtEnqueue queued rows (empty mode) must still move to downloading.
			if entry.RangeAcquisitionMode == "" && existing == nil {
				break
			}
			if err := store.AddToMasterList(entry); err != nil {
				utils.Debug("Lifecycle: Failed to save initial download state: %v", err)
			}

		case types.EventPaused:
			if m.State == nil {
				existing, _ := store.GetDownload(m.DownloadID)
				if existing == nil {
					utils.Debug("Lifecycle: Skipping paused fallback for %s: no persisted entry yet", m.DownloadID)
					break
				}

				entry := *existing
				entry.Status = "paused"
				if m.Downloaded > 0 {
					entry.Downloaded = m.Downloaded
				}
				if err := store.AddToMasterList(entry); err != nil {
					utils.Debug("Lifecycle: Failed to persist paused fallback entry: %v", err)
				}

				if existing.URL != "" && existing.DestPath != "" {
					saved, err := store.LoadState(existing.URL, existing.DestPath)
					if err == nil && saved != nil {
						prevDownloaded := saved.Downloaded
						prevElapsed := saved.Elapsed

						if m.Downloaded > saved.Downloaded {
							delta := m.Downloaded - saved.Downloaded
							saved.Tasks = advanceRemainingTasks(saved.Tasks, delta)
							saved.Downloaded = m.Downloaded
						}
						if existing.TimeTaken > 0 {
							candidateElapsed := existing.TimeTaken * int64(time.Millisecond)
							if candidateElapsed > saved.Elapsed {
								saved.Elapsed = candidateElapsed
							}
						}
						if saved.Downloaded > prevDownloaded && saved.Elapsed <= prevElapsed {
							saved.Elapsed = prevElapsed + int64(time.Millisecond)
						}

						if err := store.SaveStateWithOptions(existing.URL, existing.DestPath, saved, store.SaveStateOptions{SkipFileHash: true}); err != nil {
							utils.Debug("Lifecycle: Failed to persist paused fallback state: %v", err)
						}
					}
				}
				break
			}

			// Sparse EventPaused.State must not clobber master metadata or blank the
			// resume key. Field authority is pause-specific (not EventError symmetry).
			stateSnapshot := m.State
			snapshot := *stateSnapshot
			destPath := stateSnapshot.DestPath
			url := stateSnapshot.URL

			existing, _ := store.GetDownload(m.DownloadID)
			if existing != nil {
				if destPath == "" {
					destPath = existing.DestPath
				}
				if url == "" {
					url = existing.URL
				}
				candidateElapsed := existing.TimeTaken * int64(time.Millisecond)
				if candidateElapsed > snapshot.Elapsed {
					snapshot.Elapsed = candidateElapsed
				}
				if snapshot.Downloaded > existing.Downloaded && snapshot.Elapsed <= candidateElapsed {
					snapshot.Elapsed = candidateElapsed + int64(time.Millisecond)
				}
			}
			if destPath == "" {
				destPath = m.DestPath
			}
			if url == "" {
				url = m.URL
			}

			if m.Filename != "" {
				snapshot.Filename = m.Filename
			}
			if m.Workers != 0 {
				snapshot.Workers = m.Workers
			}
			if m.MinChunkSize != 0 {
				snapshot.MinChunkSize = m.MinChunkSize
			}
			if existing != nil {
				if snapshot.Filename == "" {
					snapshot.Filename = existing.Filename
				}
				if snapshot.TotalSize == 0 {
					snapshot.TotalSize = existing.TotalSize
				}
				if snapshot.Workers == 0 {
					snapshot.Workers = existing.Workers
				}
				if snapshot.MinChunkSize == 0 {
					snapshot.MinChunkSize = existing.MinChunkSize
				}
				copyRangeAcquisition(&snapshot, existing)
			}

			// Downloaded: task-backed keeps snapshot exactly (incl. first-pause 0);
			// taskless/invalid with master uses max so sparse zeros cannot wipe progress.
			if !isTaskBackedResumeSnapshot(snapshot) && existing != nil {
				if existing.Downloaded > snapshot.Downloaded {
					snapshot.Downloaded = existing.Downloaded
				}
			}

			// RateLimit: event RateLimitSet=false is omission vs a still-set master
			// override (intentional clear already persisted RateLimitSet=false on master).
			rateLimit := m.RateLimit
			rateLimitSet := m.RateLimitSet
			if !m.RateLimitSet && existing != nil && existing.RateLimitSet {
				rateLimit = existing.RateLimit
				rateLimitSet = existing.RateLimitSet
			}
			snapshot.RateLimit = rateLimit
			snapshot.RateLimitSet = rateLimitSet

			// Materialize identity before SaveStateWithOptions — store writes
			// state.URL/DestPath/ID, not the function args.
			if m.DownloadID != "" {
				snapshot.ID = m.DownloadID
			}
			snapshot.URL = url
			snapshot.DestPath = destPath

			entryFilename := m.Filename
			if entryFilename == "" {
				entryFilename = snapshot.Filename
			}
			if entryFilename == "" && existing != nil {
				entryFilename = existing.Filename
			}

			entry := types.DownloadRecord{
				ID:           m.DownloadID,
				Status:       "paused",
				Downloaded:   snapshot.Downloaded,
				DestPath:     destPath,
				Filename:     entryFilename,
				TotalSize:    snapshot.TotalSize,
				TimeTaken:    snapshot.Elapsed / int64(time.Millisecond),
				RateLimit:    rateLimit,
				RateLimitSet: rateLimitSet,
				Workers:      snapshot.Workers,
				MinChunkSize: snapshot.MinChunkSize,
			}
			if existing != nil {
				entry.URL = existing.URL
				entry.URLHash = existing.URLHash
				copyRangeAcquisition(&entry, &snapshot)
				copyRangeAcquisition(&entry, existing)
			} else {
				entry.URL = url
				entry.URLHash = store.URLHash(url)
				copyRangeAcquisition(&entry, &snapshot)
			}
			if err := store.AddToMasterList(entry); err != nil {
				utils.Debug("Lifecycle: Failed to persist paused state: %v", err)
			}

			// Persist enough chunk metadata for resume, but only once we have the same
			// destPath/url pair used everywhere else as the state DB key.
			if destPath != "" && url != "" {
				// Keep pause persistence fast so lifecycle events don't back up and get dropped.
				if err := store.SaveStateWithOptions(url, destPath, &snapshot, store.SaveStateOptions{
					SkipFileHash: true,
				}); err != nil {
					utils.Debug("Lifecycle: Failed to save pause state: %v", err)
				}
			} else {
				utils.Debug("Lifecycle: Skipping SaveState for %s: destPath=%q url=%q", m.DownloadID, destPath, url)
			}

		case types.EventComplete:
			var avgSpeed float64
			if m.Elapsed.Seconds() > 0 {
				avgSpeed = float64(m.Total) / m.Elapsed.Seconds()
			}

			destPath := ""
			// DownloadCompleteMsg does not carry destPath, so we recover the stable final
			// location from the DB entry written earlier on this same serialized event stream.
			existing, _ := store.GetDownload(m.DownloadID)
			var url, urlHash string
			filename := m.Filename
			if existing != nil {
				destPath = existing.DestPath
				url = existing.URL
				urlHash = existing.URLHash
				if filename == "" {
					filename = existing.Filename
				}
			}

			// Completion only becomes durable once the working file is promoted, so a
			// finalization failure must stay retryable instead of being recorded as done.
			if err := finalizeCompletedFile(destPath); err != nil {
				utils.Debug("Lifecycle: Failed to finalize completed file at %s: %v", destPath, err)
				errEntry := types.DownloadRecord{
					ID:           m.DownloadID,
					URL:          url,
					URLHash:      urlHash,
					DestPath:     destPath,
					Filename:     filename,
					Error:        err.Error(),
					Status:       "error",
					TotalSize:    m.Total,
					Downloaded:   m.Total,
					TimeTaken:    m.Elapsed.Milliseconds(),
					AvgSpeed:     avgSpeed,
					RateLimit:    m.RateLimit,
					RateLimitSet: m.RateLimitSet,
				}
				if existing != nil {
					errEntry.Workers = existing.Workers
					errEntry.MinChunkSize = existing.MinChunkSize
					copyRangeAcquisition(&errEntry, existing)
				}
				if err := store.AddToMasterList(errEntry); err != nil {
					utils.Debug("Lifecycle: Failed to persist finalization error state: %v", err)
				}
				if filename == "" {
					filename = m.DownloadID
				}
				msg := "Download failed"
				if err != nil {
					msg = err.Error()
				}
				if settings := mgr.GetSettings(); settings != nil && config.Resolve[bool](settings.General.DownloadCompleteNotification) {
					notify(fmt.Sprintf("Download failed: %s", filename), msg)
				}
				triggerGC()
				break
			}

			entry := types.DownloadRecord{
				ID:           m.DownloadID,
				URL:          url,
				URLHash:      urlHash,
				DestPath:     destPath,
				Filename:     filename,
				Status:       "completed",
				TotalSize:    m.Total,
				Downloaded:   m.Total,
				CompletedAt:  time.Now().Unix(),
				TimeTaken:    m.Elapsed.Milliseconds(),
				AvgSpeed:     avgSpeed,
				RateLimit:    m.RateLimit,
				RateLimitSet: m.RateLimitSet,
			}
			if existing != nil {
				entry.Workers = existing.Workers
				entry.MinChunkSize = existing.MinChunkSize
				copyRangeAcquisition(&entry, existing)
			}
			if err := store.AddToMasterList(entry); err != nil {
				utils.Debug("Lifecycle: Failed to persist completed download: %v", err)
			}
			if err := store.DeleteTasks(m.DownloadID); err != nil {
				utils.Debug("Lifecycle: Failed to delete completed tasks: %v", err)
			}
			if settings := mgr.GetSettings(); settings != nil && config.Resolve[bool](settings.General.DownloadCompleteNotification) {
				if filename == "" {
					filename = m.Filename
				}
				if filename == "" {
					filename = m.DownloadID
				}

				title := fmt.Sprintf("Download Complete: %s", filename)

				if m.Elapsed.Seconds() <= 0 {
					notify(title, "Download complete!")
				} else {
					notify(title, fmt.Sprintf("Download complete in %s (%.2f MiB/s)", m.Elapsed.Truncate(time.Second), avgSpeed/float64(utils.MiB)))
				}
			}
			triggerGC()

		case types.EventError:
			existing, _ := store.GetDownload(m.DownloadID)
			if m.State != nil {
				stateSnapshot := m.State
				snapshot := *stateSnapshot
				destPath := stateSnapshot.DestPath
				url := stateSnapshot.URL
				if existing != nil {
					if destPath == "" {
						destPath = existing.DestPath
					}
					if url == "" {
						url = existing.URL
					}
					candidateElapsed := existing.TimeTaken * int64(time.Millisecond)
					if candidateElapsed > snapshot.Elapsed {
						snapshot.Elapsed = candidateElapsed
					}
					if snapshot.Downloaded > existing.Downloaded && snapshot.Elapsed <= candidateElapsed {
						snapshot.Elapsed = candidateElapsed + int64(time.Millisecond)
					}
				}
				if destPath == "" {
					destPath = m.DestPath
				}
				if url == "" {
					url = m.URL
				}

				// Filename: event wins over a poorer snapshot name, then existing.
				if m.Filename != "" {
					snapshot.Filename = m.Filename
				}
				if existing != nil {
					if snapshot.Filename == "" {
						snapshot.Filename = existing.Filename
					}
					if snapshot.TotalSize == 0 {
						snapshot.TotalSize = existing.TotalSize
					}
					if snapshot.Workers == 0 {
						snapshot.Workers = existing.Workers
					}
					if snapshot.MinChunkSize == 0 {
						snapshot.MinChunkSize = existing.MinChunkSize
					}
					if !snapshot.RateLimitSet && existing.RateLimitSet {
						snapshot.RateLimit = existing.RateLimit
						snapshot.RateLimitSet = existing.RateLimitSet
					}
					copyRangeAcquisition(&snapshot, existing)
				}

				// Resume-authority Downloaded: task-backed keeps snapshot exactly
				// (including 0 / values below master); taskless/invalid uses max.
				if !isTaskBackedResumeSnapshot(snapshot) && existing != nil {
					if existing.Downloaded > snapshot.Downloaded {
						snapshot.Downloaded = existing.Downloaded
					}
				}

				// Materialize identity into snapshot before SaveStateWithOptions —
				// the store writes state.URL/DestPath/ID, not the function args.
				if m.DownloadID != "" {
					snapshot.ID = m.DownloadID
				}
				snapshot.URL = url
				snapshot.DestPath = destPath

				entryFilename := m.Filename
				if entryFilename == "" {
					entryFilename = snapshot.Filename
				}
				if entryFilename == "" && existing != nil {
					entryFilename = existing.Filename
				}

				entry := types.DownloadRecord{
					ID:           m.DownloadID,
					URL:          url,
					Status:       "error",
					Downloaded:   snapshot.Downloaded,
					DestPath:     destPath,
					Filename:     entryFilename,
					TotalSize:    snapshot.TotalSize,
					TimeTaken:    snapshot.Elapsed / int64(time.Millisecond),
					Workers:      snapshot.Workers,
					MinChunkSize: snapshot.MinChunkSize,
					RateLimit:    snapshot.RateLimit,
					RateLimitSet: snapshot.RateLimitSet,
				}
				if m.Err != nil {
					entry.Error = m.Err.Error()
				}
				if existing != nil {
					entry.URLHash = existing.URLHash
					entry.Mirrors = append([]string(nil), existing.Mirrors...)
					copyRangeAcquisition(&entry, &snapshot)
					copyRangeAcquisition(&entry, existing)
				} else if url != "" {
					entry.URLHash = store.URLHash(url)
					copyRangeAcquisition(&entry, &snapshot)
				}
				if err := store.AddToMasterList(entry); err != nil {
					utils.Debug("Lifecycle: Failed to persist error state: %v", err)
				}

				if destPath != "" && url != "" {
					if err := store.SaveStateWithOptions(url, destPath, &snapshot, store.SaveStateOptions{
						SkipFileHash: true,
					}); err != nil {
						utils.Debug("Lifecycle: Failed to save error state: %v", err)
					}
				} else {
					utils.Debug("Lifecycle: Skipping SaveState on error for %s: destPath=%q url=%q", m.DownloadID, destPath, url)
				}
			} else if existing != nil {
				existing.Status = "error"
				if m.Err != nil {
					existing.Error = m.Err.Error()
				}
				if err := store.AddToMasterList(*existing); err != nil {
					utils.Debug("Lifecycle: Failed to persist error state: %v", err)
				}
			} else {
				// nil-existing + nil-State: persist a minimal error record so an
				// early failure (before scheduler produces State, no master entry)
				// is not lost from master.gob.
				entry := types.DownloadRecord{
					ID:     m.DownloadID,
					Status: "error",
				}
				if m.Err != nil {
					entry.Error = m.Err.Error()
				}
				if m.Filename != "" {
					entry.Filename = m.Filename
				}
				if m.DestPath != "" {
					entry.DestPath = m.DestPath
				}
				if m.URL != "" {
					entry.URL = m.URL
					entry.URLHash = store.URLHash(m.URL)
				}
				if err := store.AddToMasterList(entry); err != nil {
					utils.Debug("Lifecycle: Failed to persist error state: %v", err)
				}
			}
			if settings := mgr.GetSettings(); settings != nil && config.Resolve[bool](settings.General.DownloadCompleteNotification) {
				filename := m.Filename
				if filename == "" && existing != nil {
					filename = existing.Filename
				}
				if filename == "" {
					filename = m.DownloadID
				}

				msg := "Download failed"
				if m.Err != nil {
					msg = m.Err.Error()
				}

				notify(fmt.Sprintf("Download failed: %s", filename), msg)
			}
			triggerGC()

		case types.EventRemoved:
			// Remove resume metadata before touching files so a deleted download does not
			// come back during startup recovery. DeleteState atomically removes both the
			// detail gob and the master list entry, so no separate RemoveFromMasterList call
			// is needed.
			if err := store.DeleteState(m.DownloadID); err != nil {
				utils.Debug("Lifecycle: Failed to delete state: %v", err)
			}

			// Only incomplete working files should be removed here; completed files have
			// already been promoted to their final name by the completion path.
			if m.DestPath != "" && !m.Completed {
				if err := RemoveIncompleteFile(m.DestPath); err != nil {
					utils.Debug("Lifecycle: Failed to remove incomplete file: %v", err)
				}
			}
			triggerGC()

		case types.EventQueued:
			// enqueueResolved already persisted this record synchronously.
			// A sparse insert here would write a row without Range mode; skip
			// when the master entry is absent rather than inventing one.

		case types.EventResumed, types.EventRequest, types.EventBatchRequest, types.EventSystem:
			// These events require no persistence in the lifecycle worker.

		case types.EventBatchProgress, types.EventProgress:
			// Progress ticks are intentionally transient; persisting them would add
			// file I/O churn without improving resume or history recovery.
		}
	}
}

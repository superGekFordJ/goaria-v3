package types

// WorkerSnapshot is a concurrency-safe, copy-on-read snapshot of a single
// worker's runtime state. Built by strategy/concurrent checkWorkerHealth()
// under activeMu, then published to progress.DownloadProgress for external
// consumption.
//
// FORK-PATCH: GoAria-specific telemetry type for runtime feedback loops.
// Upstream Surge does not expose per-worker statistics.
type WorkerSnapshot struct {
	WorkerID         int
	EMASpeed         float64 // bytes/sec, EMA-smoothed with decay (from ActiveTask.GetSpeed())
	LastActivityUnix int64   // Unix nano timestamp of last data received
	ChunkStart       int64   // Original start offset of the chunk (absolute file position)
	ChunkOffset      int64   // Current read position (absolute file position)
	ChunkLength      int64   // Effective chunk length (StopAt - ChunkStart, may shrink from work stealing)

	// FORK-PATCH: per-worker session inputs for CDN throttle fingerprinting.
	// WorkerStartUnix/SessionBytes are connection-granularity (survive chunk
	// switches), sourced from ConcurrentDownloader.workerSessions — NOT from
	// ActiveTask, which is rebuilt every chunk.
	WorkerStartUnix int64 // Unix nano when the worker goroutine (connection) started
	SessionBytes    int64 // Cumulative bytes downloaded by this worker across all its chunks

	RetryCount int32 // Number of failed retries for the current task
	HTTPStatus int32 // Most recent HTTP response status code (0 if none yet)

	WaitingOnLimiter bool // True if worker is blocked on rate limiter
	Hedged           bool // True if this task is a hedged (racing) request
}

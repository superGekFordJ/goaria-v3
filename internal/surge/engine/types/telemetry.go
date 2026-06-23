package types

// WorkerSnapshot is a concurrency-safe, copy-on-read snapshot of a single
// worker's runtime state. Built by ConcurrentDownloader.checkWorkerHealth()
// under activeMu, then published to ProgressState for external consumption.
//
// FORK-PATCH: GoAria-specific telemetry type for runtime feedback loops
//  . Upstream Surge does not expose per-worker statistics.
type WorkerSnapshot struct {
	WorkerID         int
	EMASpeed         float64 // bytes/sec, EMA-smoothed with decay (from ActiveTask.GetSpeed())
	LastActivityUnix int64   // Unix nano timestamp of last data received
	RetryCount       int32   // Number of failed retries for the current task
	ChunkStart       int64   // Original start offset of the chunk (absolute file position)
	ChunkOffset      int64   // Current read position (absolute file position)
	ChunkLength      int64   // Effective chunk length (StopAt - ChunkStart, may shrink from work stealing)
	WaitingOnLimiter bool    // True if worker is blocked on rate limiter
	Hedged           bool    // True if this task is a hedged (racing) request
}

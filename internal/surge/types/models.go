package types

import (
	"encoding/binary"
	"fmt"
	"slices"
	"sync/atomic"
)

// Task represents a byte range to download.
// FORK-PATCH: fieldalignment pointer-first layout minimizes GC scan prefix (24B -> 8B).
type Task struct {
	SharedMaxOffset *atomic.Int64 `json:"-"`
	Offset          int64         `json:"offset"`
	Length          int64         `json:"length"`
}

func (t *Task) GobEncode() ([]byte, error) {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint64(b[0:8], uint64(t.Offset))
	binary.LittleEndian.PutUint64(b[8:16], uint64(t.Length))
	return b, nil
}

func (t *Task) GobDecode(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) < 16 {
		return fmt.Errorf("corrupt task data: expected 16 bytes, got %d", len(data))
	}
	t.Offset = int64(binary.LittleEndian.Uint64(data[0:8]))
	t.Length = int64(binary.LittleEndian.Uint64(data[8:16]))
	return nil
}

// DownloadRecord is the canonical representation of a download's static configuration,
// persistent state, and runtime options. It replaces DownloadState, DownloadEntry, and DownloadConfig.
type DownloadRecord struct {
	// Identity & Core Info
	ID         string `json:"id"`
	URLHash    string `json:"url_hash"`
	URL        string `json:"url"`
	Filename   string `json:"filename"`
	OutputPath string `json:"output_path"`
	DestPath   string `json:"dest_path"`
	TotalSize  int64  `json:"total_size"`
	Downloaded int64  `json:"downloaded"`

	// Status
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`

	// Lifecycle Timestamps & Stats
	CreatedAt   int64   `json:"created_at"`
	PausedAt    int64   `json:"paused_at,omitempty"`
	CompletedAt int64   `json:"completed_at,omitempty"`
	TimeTaken   int64   `json:"time_taken,omitempty"`
	Elapsed     int64   `json:"elapsed,omitempty"`
	AvgSpeed    float64 `json:"avg_speed,omitempty"`

	// Resume State (Persistent)
	Tasks           []Task `json:"tasks,omitempty"`
	ChunkBitmap     []byte `json:"chunk_bitmap,omitempty"`
	ActualChunkSize int64  `json:"actual_chunk_size,omitempty"`
	FileHash        string `json:"file_hash,omitempty"`

	// Configuration Options (Persistent)
	Mirrors      []string `json:"mirrors,omitempty"`
	RateLimit    int64    `json:"rate_limit,omitempty"`
	RateLimitSet bool     `json:"rate_limit_set,omitempty"`
	Workers      int      `json:"workers,omitempty"`
	MinChunkSize int64    `json:"min_chunk_size,omitempty"`

	// FORK-PATCH: persisted Range acquisition policy, kept inside the detail
	// whitelist projection. Empty is ProbeAtEnqueue (gob zero-value); cold
	// resume cannot reconstruct it from SupportsRange.
	RangeAcquisitionMode RangeAcquisitionMode `json:"range_acquisition_mode,omitempty"`
	// FORK-PATCH: sticky skip-origin so resume HEAD does not burn presigned URLs
	// after mode promotes to RangeSupported. Persisted via the detail whitelist.
	SkipServerProbe bool `json:"skip_server_probe,omitempty"`

	// FORK-PATCH: strategy discriminator. Empty is StrategyAuto; StrategyHLS
	// routes the task to the HLS downloader. Persisted in both the detail
	// whitelist and master.gob.
	Strategy DownloadStrategy `json:"strategy,omitempty"`
	// FORK-PATCH: immutable HLS resume snapshot. Persisted only in the detail
	// whitelist — master.gob scrubs it. Every cross-boundary handoff must go
	// through Clone() so the four inner slices stay physically isolated.
	HLSResume *HLSResumeState `json:"hls_resume,omitempty"`

	// Runtime / Transient fields. Persistence is controlled by the store
	// layer's explicit whitelist projection plus master-index scrubbing —
	// gob ignores struct tags, so the old gob:"-" markers were dead letters.
	// Headers is the deliberate exception: it persists inside per-task
	// detail state only (the credential store), never in master.gob.
	IsResume           bool                 `json:"-"`
	ProgressCh         chan<- DownloadEvent `json:"-"`
	ProgressState      any                  `json:"-"` // typically *progress.DownloadProgress
	Runtime            *RuntimeConfig       `json:"-"`
	Headers            map[string]string    `json:"-"`
	Limiter            ByteLimiter          `json:"-"`
	IsExplicitCategory bool                 `json:"-"`
	SupportsRange      bool                 `json:"-"`
}

// DownloadStrategy selects the downloader backend for a task.
// FORK-PATCH: empty is the default byte-range strategy; "hls" routes the
// task to the HLS segment downloader.
type DownloadStrategy string

const (
	StrategyAuto DownloadStrategy = ""    // byte-range: concurrent or single per RangeAcquisitionMode / SupportsRange
	StrategyHLS  DownloadStrategy = "hls" // HLS segment strategy
)

// HLSSegmentState is the persisted per-segment checkpoint of an HLS download.
type HLSSegmentState struct {
	Index          int    `json:"index"`
	Sequence       int64  `json:"sequence"`
	URI            string `json:"uri"`
	DurationMicros int64  `json:"duration_micros,omitempty"`
	ByteOffset     int64  `json:"byte_offset,omitempty"`
	ByteLength     int64  `json:"byte_length,omitempty"`
	// KeyIndex is 1-based: 0 means the segment is unencrypted, while
	// 1..len(Keys) maps to Keys[KeyIndex-1].
	KeyIndex int    `json:"key_index,omitempty"`
	IVHex    string `json:"iv_hex,omitempty"`
}

// HLSKeyInfo is one EXT-X-KEY decryption descriptor.
type HLSKeyInfo struct {
	Method string `json:"method"`
	URI    string `json:"uri"`
	IVHex  string `json:"iv_hex,omitempty"`
}

// HLSSpillEntry locates one segment payload inside the spill file.
type HLSSpillEntry struct {
	SegmentIndex int   `json:"segment_index"`
	Offset       int64 `json:"offset"`
	Length       int64 `json:"length"`
}

// HLSResumeState is the immutable resume snapshot of an HLS download,
// persisted inside details/<id>.gob only (never master.gob).
type HLSResumeState struct {
	ManifestURL         string            `json:"manifest_url"`
	MediaPlaylistURL    string            `json:"media_playlist_url"`
	SegmentRefURL       string            `json:"segment_ref_url,omitempty"`
	ManifestFingerprint string            `json:"manifest_fingerprint"`
	ContainerFormat     string            `json:"container_format"`
	EstimatedBytes      int64             `json:"estimated_bytes,omitempty"`
	RecordEligible      bool              `json:"record_eligible"`
	InitSegmentURI      string            `json:"init_segment_uri,omitempty"`
	InitCompleted       bool              `json:"init_completed,omitempty"`
	InitBytes           int64             `json:"init_bytes,omitempty"`
	TotalSegments       int               `json:"total_segments"`
	CompletedBitmap     []byte            `json:"completed_bitmap"`
	MergedSegments      int               `json:"merged_segments"`
	MergedBytes         int64             `json:"merged_bytes"`
	VerifiedBytes       int64             `json:"verified_bytes"`
	SpillPath           string            `json:"spill_path,omitempty"`
	SpillFileSize       int64             `json:"spill_file_size,omitempty"`
	SpillEntries        []HLSSpillEntry   `json:"spill_entries,omitempty"`
	Keys                []HLSKeyInfo      `json:"keys,omitempty"`
	Segments            []HLSSegmentState `json:"segments,omitempty"`
}

// Clone returns a physically isolated copy: scalar fields are copied by value
// and all four inner slices are deep-copied so a clone can never share
// backing arrays with the source. Nil-safe.
func (s *HLSResumeState) Clone() *HLSResumeState {
	if s == nil {
		return nil
	}
	cp := *s
	cp.CompletedBitmap = slices.Clone(s.CompletedBitmap)
	cp.SpillEntries = slices.Clone(s.SpillEntries)
	cp.Keys = slices.Clone(s.Keys)
	cp.Segments = slices.Clone(s.Segments)
	return &cp
}

// MasterList holds all tracked downloads.
type MasterList struct {
	Downloads []DownloadRecord `json:"downloads"`
}

// DownloadStatus is the transient view returned to the TUI and API clients.
type DownloadStatus struct {
	ID           string  `json:"id"`
	URL          string  `json:"url"`
	Filename     string  `json:"filename"`
	DestPath     string  `json:"dest_path,omitempty"`
	TotalSize    int64   `json:"total_size"`
	Downloaded   int64   `json:"downloaded"`
	Progress     float64 `json:"progress"`
	Speed        float64 `json:"speed"`
	Status       string  `json:"status"`
	Error        string  `json:"error,omitempty"`
	ETA          int64   `json:"eta"`
	Connections  int     `json:"connections"`
	AddedAt      int64   `json:"added_at"`
	TimeTaken    int64   `json:"time_taken"`
	AvgSpeed     float64 `json:"avg_speed"`
	RateLimit    int64   `json:"rate_limit,omitempty"`
	RateLimitSet bool    `json:"rate_limit_set,omitempty"`
}

// CancelResult carries enough metadata for callers to emit lifecycle events
// without creating an import cycle back to the worker pool.
type CancelResult struct {
	Found     bool
	Filename  string
	DestPath  string
	Completed bool
	WasQueued bool
}

type MirrorStatus struct {
	URL    string
	Active bool
	Error  bool
}

// ChunkStatus represents the status of a visualization chunk
type ChunkStatus int

const (
	ChunkPending     ChunkStatus = 0 // 00
	ChunkDownloading ChunkStatus = 1 // 01
	ChunkCompleted   ChunkStatus = 2 // 10 (Bit 2 set)
)

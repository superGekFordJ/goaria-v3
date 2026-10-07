package monitor

import (
	"sync"
	"time"
)

// ChunkSnapshot is the latest per-GID engine chunk bitmap, kept in packed
// wire form (2 bits per chunk, LSB-first). Consumers unpack on demand.
type ChunkSnapshot struct {
	Bitmap     []byte    // packed 2-bit/chunk LSB-first; len == ceil(ChunkCount/4)
	ChunkCount int       // total chunk count (engine BitmapWidth)
	ChunkSize  int64     // bytes per chunk (engine actualChunkSize)
	UpdatedAt  time.Time // host-side write time; diagnostics only, never emitted
}

// ChunkSnapshotCache stores per-GID chunk bitmap snapshots written by the
// Surge batch progress event loop. It is a leaf lock: callers must not hold
// it while acquiring tracker/lifecycle locks.
type ChunkSnapshotCache struct {
	mu   sync.RWMutex
	data map[string]ChunkSnapshot
}

// NewChunkSnapshotCache creates an empty ChunkSnapshotCache.
func NewChunkSnapshotCache() *ChunkSnapshotCache {
	return &ChunkSnapshotCache{data: make(map[string]ChunkSnapshot)}
}

// Set stores the latest snapshot for the given GID, copying the bitmap so
// callers may reuse their buffer. Empty gid, empty bitmap, or non-positive
// count are ignored.
func (c *ChunkSnapshotCache) Set(gid string, bitmap []byte, count int, chunkSize int64) {
	if c == nil || gid == "" || len(bitmap) == 0 || count <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	stored := make([]byte, len(bitmap))
	copy(stored, bitmap)
	c.data[gid] = ChunkSnapshot{
		Bitmap:     stored,
		ChunkCount: count,
		ChunkSize:  chunkSize,
		UpdatedAt:  time.Now(),
	}
}

// Get returns a copy of the snapshot for the given GID, or false if absent.
func (c *ChunkSnapshotCache) Get(gid string) (ChunkSnapshot, bool) {
	if c == nil {
		return ChunkSnapshot{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap, ok := c.data[gid]
	if !ok {
		return ChunkSnapshot{}, false
	}
	out := snap
	out.Bitmap = append([]byte(nil), snap.Bitmap...)
	return out, true
}

// Remove deletes the snapshot for the given GID.
func (c *ChunkSnapshotCache) Remove(gid string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, gid)
}

// UpdatedAt returns the last write time for the given GID.
func (c *ChunkSnapshotCache) UpdatedAt(gid string) (time.Time, bool) {
	if c == nil {
		return time.Time{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap, ok := c.data[gid]
	if !ok {
		return time.Time{}, false
	}
	return snap.UpdatedAt, true
}

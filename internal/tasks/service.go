package tasks

import "goaria-v3/internal/rpc"

type Service struct {
	Adapter ExtractorAdapter
	Engine  rpc.DownloadEngine
	// Deduper, when non-nil, coalesces concurrent adds for the same dedup
	// target across Service instances. Nil keeps the legacy per-batch
	// serialization and preserves independent behavior between Services.
	Deduper *AddDeduper
}

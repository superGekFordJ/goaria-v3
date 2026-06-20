package surge

import (
	"log"
	"path/filepath"

	"goaria-v3/internal/surge/engine/state"
)

// Initialize configures the Surge database file path and runs startup self-healing
func Initialize(dataDir string) {
	dbPath := filepath.Join(dataDir, "surge.db")
	log.Printf("[Surge] Initializing session database at %s", dbPath)
	state.Configure(dbPath)

	// 1. Normalize stale "downloading" status tasks left from sudden crash/kill
	if n, err := state.NormalizeStaleDownloads(); err != nil {
		log.Printf("[Surge] Failed to normalize stale downloads: %v", err)
	} else if n > 0 {
		log.Printf("[Surge] Normalized %d stale downloading tasks to paused state", n)
	}

	// 2. Validate database/file integrity
	if n, err := state.ValidateIntegrity(); err != nil {
		log.Printf("[Surge] Failed to validate download database integrity: %v", err)
	} else if n > 0 {
		log.Printf("[Surge] Cleaned up %d orphaned database entries or invalid files during integrity check", n)
	}
}

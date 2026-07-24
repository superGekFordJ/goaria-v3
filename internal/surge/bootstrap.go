package surge

import (
	"log"
	"os"
	"path/filepath"

	"goaria-v3/internal/surge/store"
)

// Initialize configures the Surge state directory and runs startup self-healing
func Initialize(dataDir string) {
	dbPath := filepath.Join(dataDir, "surge.db")
	log.Printf("[Surge] Initializing state directory at %s", dataDir)
	store.Configure(dbPath)

	// Gob migration: remove legacy SQLite database (upstream no longer reads it)
	if err := os.Remove(dbPath); err == nil {
		log.Printf("[Surge] Removed legacy SQLite database (paused tasks from previous versions need to be re-added)")
	}

	// 1. Normalize stale "downloading" status tasks left from sudden crash/kill
	if n, err := store.NormalizeStaleDownloads(); err != nil {
		log.Printf("[Surge] Failed to normalize stale downloads: %v", err)
	} else if n > 0 {
		log.Printf("[Surge] Normalized %d stale downloading tasks to paused state", n)
	}

	// 2. Validate state/file integrity
	if n, err := store.ValidateIntegrity(); err != nil {
		log.Printf("[Surge] Failed to validate state integrity: %v", err)
	} else if n > 0 {
		log.Printf("[Surge] Cleaned up %d orphaned state entries or invalid files during integrity check", n)
	}
}

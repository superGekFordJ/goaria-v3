package testutil

import (
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// SuppressNotificationsInTests disables desktop notifications for any test
// that imports this package. Call this from a per-package init() or TestMain.
func SuppressNotificationsInTests() {
	utils.SuppressNotifications = true
}

func init() {
	SuppressNotificationsInTests()
}

// SetupStateDB configures a fresh temp backend for tests that exercise state persistence.
func SetupStateDB(t *testing.T) string {
	t.Helper()

	tempDir := t.TempDir()
	store.CloseDB()
	store.Configure(filepath.Join(tempDir, "surge.db"))

	t.Cleanup(store.CloseDB)
	return tempDir
}

// SeedMasterList inserts a DownloadRecord into the master list for test setups.
func SeedMasterList(t *testing.T, entry types.DownloadRecord) {
	t.Helper()
	if err := store.AddToMasterList(entry); err != nil {
		t.Fatalf("SeedMasterList failed: %v", err)
	}
}

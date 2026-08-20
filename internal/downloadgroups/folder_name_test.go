package downloadgroups

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/rpc"
)

func TestIsWindowsReservedName_MatchesFolderSanitizer(t *testing.T) {
	t.Parallel()

	if !IsWindowsReservedName("CON.txt") {
		t.Fatal(`IsWindowsReservedName("CON.txt") = false, want true`)
	}
	for _, name := range []string{"CON::$DATA", "COM1:", "NUL:"} {
		if !IsWindowsReservedName(name) {
			t.Fatalf("IsWindowsReservedName(%q) = false, want true", name)
		}
	}
	if IsWindowsReservedName("Album") {
		t.Fatal(`IsWindowsReservedName("Album") = true, want false`)
	}
}

func TestSanitizeDownloadGroupFolderName_ReservedWindowsNames(t *testing.T) {
	t.Parallel()

	reserved := []string{
		"CON", "CON.txt", "CON .txt", "CON\u00a0.txt", "CON\uFF0Etxt",
		"Prn", "COM1.foo", "NUL.", "CON.", "con.txt.", "LPT9", "AUX", "CONIN$", "CONOUT$",
	}
	for _, name := range reserved {
		if got := SanitizeDownloadGroupFolderName(name); got != "" {
			t.Fatalf("SanitizeDownloadGroupFolderName(%q) = %q, want empty", name, got)
		}
	}
}

func TestSanitizeDownloadGroupFolderName_KeepsSafeCustomNames(t *testing.T) {
	t.Parallel()

	if got := SanitizeDownloadGroupFolderName("Album"); got != "Album" {
		t.Fatalf("SanitizeDownloadGroupFolderName(Album) = %q, want Album", got)
	}
	if got := SanitizeDownloadGroupFolderName("Album."); got != "Album" {
		t.Fatalf("SanitizeDownloadGroupFolderName(Album.) = %q, want Album", got)
	}
	if got := SanitizeDownloadGroupFolderName("Album "); got != "Album" {
		t.Fatalf("SanitizeDownloadGroupFolderName(Album ) = %q, want Album", got)
	}
}

func TestResolveDownloadGroupDir_ReservedNameErrors(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	_, err := ResolveDownloadGroupDir(base, "CON.txt")
	if err == nil {
		t.Fatal("ResolveDownloadGroupDir(CON.txt) error = nil, want error after reserved-name sanitize")
	}
}

func TestNewDownloadGroupPlanWithFolderName_UsesSanitizedStableName(t *testing.T) {
	tempBaseDir := t.TempDir()
	original := config.Get()
	config.SetTestConfig(&config.AppConfig{DownloadDir: tempBaseDir})
	t.Cleanup(func() { config.SetTestConfig(original) })

	now := time.Date(2026, 8, 19, 16, 0, 0, 0, time.UTC)
	plan, err := NewDownloadGroupPlanWithFolderName(DownloadGroupKindCollection, 2, now, "Album.")
	if err != nil {
		t.Fatalf("NewDownloadGroupPlanWithFolderName() error = %v", err)
	}
	group := plan.GroupCopy()
	if group.FolderName != "Album" {
		t.Fatalf("FolderName = %q, want Album", group.FolderName)
	}
	if group.Name != "Album" {
		t.Fatalf("Name = %q, want Album", group.Name)
	}
	if group.NameStatus != rpc.DownloadGroupNameStatusStable {
		t.Fatalf("NameStatus = %q, want %q", group.NameStatus, rpc.DownloadGroupNameStatusStable)
	}
	if group.Kind != DownloadGroupKindCollection {
		t.Fatalf("Kind = %q, want %q", group.Kind, DownloadGroupKindCollection)
	}
	if group.ItemCount != 2 {
		t.Fatalf("ItemCount = %d, want 2", group.ItemCount)
	}
	if group.Dir == "" {
		t.Fatal("Dir is empty, want planned path")
	}
	if _, statErr := os.Stat(group.Dir); !os.IsNotExist(statErr) {
		t.Fatalf("constructor must not mkdir, Stat(%q) err = %v", group.Dir, statErr)
	}
}

func TestNewDownloadGroupPlanWithFolderName_ReservedFallsBackToTimestamp(t *testing.T) {
	tempBaseDir := t.TempDir()
	original := config.Get()
	config.SetTestConfig(&config.AppConfig{DownloadDir: tempBaseDir})
	t.Cleanup(func() { config.SetTestConfig(original) })

	now := time.Date(2026, 8, 19, 16, 0, 0, 0, time.UTC)
	plan, err := NewDownloadGroupPlanWithFolderName(DownloadGroupKindCollection, 2, now, "CON.txt")
	if err != nil {
		t.Fatalf("NewDownloadGroupPlanWithFolderName() error = %v", err)
	}
	group := plan.GroupCopy()
	if group.FolderName == "CON.txt" || strings.EqualFold(group.FolderName, "CON") {
		t.Fatalf("FolderName = %q, want timestamp fallback not reserved name", group.FolderName)
	}
	if !strings.Contains(group.FolderName, "2026-08-19") {
		t.Fatalf("FolderName = %q, want timestamp fallback containing 2026-08-19", group.FolderName)
	}
	if group.NameStatus != rpc.DownloadGroupNameStatusFallback {
		t.Fatalf("NameStatus = %q, want %q", group.NameStatus, rpc.DownloadGroupNameStatusFallback)
	}
	if !strings.HasPrefix(group.Name, "Collection ") {
		t.Fatalf("Name = %q, want Collection timestamp fallback", group.Name)
	}
	if _, statErr := os.Stat(filepath.Join(tempBaseDir, "CON.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("reserved folder must not be created, Stat err = %v", statErr)
	}
}

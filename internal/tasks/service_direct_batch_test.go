package tasks

import (
	"context"
	"os"
	"strings"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/history"
)

func TestAddPreparedDirectItems_TwoIDsOneURLNoCollection(t *testing.T) {
	service, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sameURL := "https://download.fixture.invalid/files/a.bin"
	result, err := service.AddPreparedDirectItems(context.Background(), PreparedDirectAddRequest{
		CreateGroup: true,
		FolderName:  "Album",
		Items: []PreparedDirectAddItem{
			{ClientItemID: idA, URL: sameURL, Filename: "a.bin"},
			{ClientItemID: idB, URL: sameURL, Filename: "a.bin"},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedDirectItems() error = %v", err)
	}
	if len(result.Succeeded) != 1 || result.Succeeded[0] != idA {
		t.Fatalf("succeeded = %#v, want [%q]", result.Succeeded, idA)
	}
	if len(result.Duplicates) != 1 || result.Duplicates[0] != idB {
		t.Fatalf("duplicates = %#v, want [%q]", result.Duplicates, idB)
	}
	if len(result.Groups) != 0 {
		t.Fatalf("Groups = %#v, want none", result.Groups)
	}
	if got := counter.addURICount(); got != 1 {
		t.Fatalf("addUri calls = %d, want 1", got)
	}
	baseDir := config.Get().DownloadDir
	entries, _ := os.ReadDir(baseDir)
	for _, entry := range entries {
		if strings.Contains(strings.ToLower(entry.Name()), "album") {
			t.Fatalf("unexpected group dir %q", entry.Name())
		}
	}
}

func TestAddPreparedDirectItems_TwoUniqueCreateCollection(t *testing.T) {
	service, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	result, err := service.AddPreparedDirectItems(context.Background(), PreparedDirectAddRequest{
		CreateGroup: true,
		FolderName:  "Album",
		Items: []PreparedDirectAddItem{
			{ClientItemID: idA, URL: "https://download.fixture.invalid/files/a.bin", Filename: "a.bin"},
			{ClientItemID: idB, URL: "https://files.alpha.test/downloads/b.bin", Filename: "b.bin"},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedDirectItems() error = %v", err)
	}
	assertBatchAddStringsUnordered(t, "succeeded", result.Succeeded, []string{idA, idB})
	if len(result.Groups) != 1 {
		t.Fatalf("Groups = %d, want 1", len(result.Groups))
	}
	if result.Groups[0].Kind != downloadgroups.DownloadGroupKindCollection {
		t.Fatalf("Kind = %q", result.Groups[0].Kind)
	}
	if got := counter.addURICount(); got != 2 {
		t.Fatalf("addUri calls = %d, want 2", got)
	}
}

func TestAddPreparedDirectItems_AllDuplicateNoMkdir(t *testing.T) {
	service, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	sameURL := "https://download.fixture.invalid/files/a.bin"
	history.Add(history.HistoryEntry{GID: "gid-history", Source: sameURL})
	baseDir := config.Get().DownloadDir
	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	result, err := service.AddPreparedDirectItems(context.Background(), PreparedDirectAddRequest{
		CreateGroup: true,
		FolderName:  "Album",
		Items: []PreparedDirectAddItem{
			{ClientItemID: idA, URL: sameURL},
			{ClientItemID: idB, URL: sameURL},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedDirectItems() error = %v", err)
	}
	assertBatchAddStringsUnordered(t, "duplicates", result.Duplicates, []string{idA, idB})
	if len(result.Succeeded) != 0 {
		t.Fatalf("succeeded = %#v, want none", result.Succeeded)
	}
	if got := counter.addURICount(); got != 0 {
		t.Fatalf("addUri calls = %d, want 0", got)
	}
	entries, _ := os.ReadDir(baseDir)
	for _, entry := range entries {
		if strings.Contains(strings.ToLower(entry.Name()), "album") {
			t.Fatalf("unexpected group dir %q", entry.Name())
		}
	}
}

func TestAddPreparedDirectItems_OwnerFailDoesNotSubmitDuplicates(t *testing.T) {
	service, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	counter.failAll = true
	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sameURL := "https://download.fixture.invalid/files/a.bin"
	result, err := service.AddPreparedDirectItems(context.Background(), PreparedDirectAddRequest{
		Items: []PreparedDirectAddItem{
			{ClientItemID: idA, URL: sameURL},
			{ClientItemID: idB, URL: sameURL},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedDirectItems() error = %v", err)
	}
	if _, ok := result.Errors[idA]; !ok {
		t.Fatalf("owner should be in errors, got %#v", result.Errors)
	}
	if len(result.Duplicates) != 1 || result.Duplicates[0] != idB {
		t.Fatalf("duplicates = %#v, want [%q]", result.Duplicates, idB)
	}
	if got := counter.addURICount(); got != 1 {
		t.Fatalf("addUri calls = %d, want 1", got)
	}
}

func TestAddPreparedDirectItems_ReservedFilenameIsRenamed(t *testing.T) {
	cap := &capturingAddURIEngine{}
	service := setupDiskSpaceAddService(t, cap)
	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	result, err := service.AddPreparedDirectItems(context.Background(), PreparedDirectAddRequest{
		Items: []PreparedDirectAddItem{
			{ClientItemID: idA, URL: "https://files.alpha.test/downloads/b.bin", Filename: "CON.txt"},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedDirectItems() error = %v", err)
	}
	if len(result.Succeeded) != 1 || result.Succeeded[0] != idA {
		t.Fatalf("succeeded = %#v, want [%q]", result.Succeeded, idA)
	}
	opts := cap.snapshotOptions()
	if len(opts) != 1 {
		t.Fatalf("AddUri calls = %d, want 1", len(opts))
	}
	if opts[0].Out == "CON.txt" || downloadgroups.IsWindowsReservedName(opts[0].Out) {
		t.Fatalf("Out = %q, want renamed non-reserved name", opts[0].Out)
	}
}

func TestAddPreparedDirectItems_DoesNotCallBatchAddUri(t *testing.T) {
	service, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	_, err := service.AddPreparedDirectItems(context.Background(), PreparedDirectAddRequest{
		Items: []PreparedDirectAddItem{
			{ClientItemID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", URL: "https://download.fixture.invalid/a.bin"},
			{ClientItemID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", URL: "https://files.alpha.test/b.bin"},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedDirectItems() error = %v", err)
	}
	if got := counter.addURICount(); got != 2 {
		t.Fatalf("addUri calls = %d, want 2", got)
	}
}

func TestAddPreparedDirectItems_RefererFromDownloadPage(t *testing.T) {
	cap := &capturingAddURIEngine{}
	service := setupDiskSpaceAddService(t, cap)
	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err := service.AddPreparedDirectItems(context.Background(), PreparedDirectAddRequest{
		Items: []PreparedDirectAddItem{
			{
				ClientItemID: idA,
				URL:          "https://files.alpha.test/downloads/b.bin",
				DownloadPage: "https://page.fixture.invalid/view",
				Headers:      []string{"User-Agent: GoAriaTest/1.0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedDirectItems() error = %v", err)
	}
	opts := cap.snapshotOptions()
	if len(opts) != 1 {
		t.Fatalf("AddUri calls = %d, want 1", len(opts))
	}
	found := false
	for _, h := range opts[0].Headers {
		name, value, ok := strings.Cut(h, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "Referer") && strings.Contains(value, "https://page.fixture.invalid/view") {
			found = true
		}
	}
	if !found {
		t.Fatalf("headers = %#v, want Referer from download_page", opts[0].Headers)
	}
}

func TestAddPreparedDirectItems_ExistingReferrerSkipsInject(t *testing.T) {
	cap := &capturingAddURIEngine{}
	service := setupDiskSpaceAddService(t, cap)
	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err := service.AddPreparedDirectItems(context.Background(), PreparedDirectAddRequest{
		Items: []PreparedDirectAddItem{
			{
				ClientItemID: idA,
				URL:          "https://files.alpha.test/downloads/b.bin",
				DownloadPage: "https://page.fixture.invalid/view",
				Headers:      []string{"Referrer: https://keep.fixture.invalid/"},
			},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedDirectItems() error = %v", err)
	}
	opts := cap.snapshotOptions()
	if len(opts) != 1 {
		t.Fatalf("AddUri calls = %d, want 1", len(opts))
	}
	refererCount := 0
	for _, h := range opts[0].Headers {
		name, _, ok := strings.Cut(h, ":")
		if ok && (strings.EqualFold(strings.TrimSpace(name), "Referer") || strings.EqualFold(strings.TrimSpace(name), "Referrer")) {
			refererCount++
		}
	}
	if refererCount != 1 {
		t.Fatalf("headers = %#v, want a single Referrer/Referer", opts[0].Headers)
	}
}

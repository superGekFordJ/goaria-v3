package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/rpc"
)

type countingPreparedAdapter struct {
	fakePortAdapter
	resolveCalls atomic.Int32
}

func (a *countingPreparedAdapter) Resolve(ctx context.Context, rawURL string) (Resolution, error) {
	a.resolveCalls.Add(1)
	return a.fakePortAdapter.Resolve(ctx, rawURL)
}

func TestAddPreparedExtractorItems_TwoItemsCreateCollectionGroup(t *testing.T) {
	adapter := &countingPreparedAdapter{}
	service, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	service.Adapter = adapter

	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	result, err := service.AddPreparedExtractorItems(context.Background(), PreparedAddRequest{
		CreateGroup: true,
		FolderName:  "Album",
		Items: []PreparedAddItem{
			{Item: ResolvedItem{URL: "https://download.fixture.invalid/files/a.bin", Filename: "a.bin"}, DisplayKey: idA},
			{Item: ResolvedItem{URL: "https://files.alpha.test/downloads/b.bin", Filename: "b.bin"}, DisplayKey: idB},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedExtractorItems() error = %v", err)
	}
	assertBatchAddStringsUnordered(t, "succeeded", result.Succeeded, []string{idA, idB})
	if containsString(result.Succeeded, "https://download.fixture.invalid/files/a.bin") {
		t.Fatal("result succeeded keys must be item ids, not URLs")
	}
	if len(result.Groups) != 1 {
		t.Fatalf("Groups = %d, want 1", len(result.Groups))
	}
	group := result.Groups[0]
	if group.Kind != downloadgroups.DownloadGroupKindCollection {
		t.Fatalf("Kind = %q, want %q", group.Kind, downloadgroups.DownloadGroupKindCollection)
	}
	if group.FolderName != "Album" {
		t.Fatalf("FolderName = %q, want Album", group.FolderName)
	}
	if group.NameStatus != rpc.DownloadGroupNameStatusStable {
		t.Fatalf("NameStatus = %q, want stable", group.NameStatus)
	}
	if adapter.resolveCalls.Load() != 0 {
		t.Fatalf("Adapter.Resolve calls = %d, want 0", adapter.resolveCalls.Load())
	}
	if got := counter.addURICount(); got != 2 {
		t.Fatalf("addUri calls = %d, want 2", got)
	}
}

func TestAddPreparedExtractorItems_OneUniqueURLSkipsGroup(t *testing.T) {
	service, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	service.Adapter = &countingPreparedAdapter{}
	baseDir := config.Get().DownloadDir

	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sameURL := "https://download.fixture.invalid/files/a.bin"
	result, err := service.AddPreparedExtractorItems(context.Background(), PreparedAddRequest{
		CreateGroup: true,
		FolderName:  "Album",
		Items: []PreparedAddItem{
			{Item: ResolvedItem{URL: sameURL, Filename: "a.bin"}, DisplayKey: idA},
			{Item: ResolvedItem{URL: sameURL, Filename: "a.bin"}, DisplayKey: idB},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedExtractorItems() error = %v", err)
	}
	if len(result.Groups) != 0 {
		t.Fatalf("Groups = %#v, want none for one unique URL", result.Groups)
	}
	if got := counter.addURICount(); got != 1 {
		t.Fatalf("addUri calls = %d, want 1 (second handle is duplicate URL)", got)
	}
	entries, _ := os.ReadDir(baseDir)
	for _, entry := range entries {
		if strings.Contains(strings.ToLower(entry.Name()), "album") {
			t.Fatalf("unexpected group dir %q", entry.Name())
		}
	}
}

func TestAddPreparedExtractorItems_AllAddUriFailRemovesDir(t *testing.T) {
	service, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	service.Adapter = &countingPreparedAdapter{}
	counter.failAll = true
	baseDir := config.Get().DownloadDir

	_, err := service.AddPreparedExtractorItems(context.Background(), PreparedAddRequest{
		CreateGroup: true,
		FolderName:  "Album",
		Items: []PreparedAddItem{
			{Item: ResolvedItem{URL: "https://download.fixture.invalid/files/a.bin"}, DisplayKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{Item: ResolvedItem{URL: "https://files.alpha.test/downloads/b.bin"}, DisplayKey: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedExtractorItems() error = %v", err)
	}
	albumDir := filepath.Join(baseDir, "Album")
	if _, statErr := os.Stat(albumDir); !os.IsNotExist(statErr) {
		t.Fatalf("group dir %q should be removed after all failures, stat err = %v", albumDir, statErr)
	}
}

func TestAddPreparedExtractorItems_RejectsEmptyAndOversize(t *testing.T) {
	service, _ := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	if _, err := service.AddPreparedExtractorItems(context.Background(), PreparedAddRequest{}); !errors.Is(err, ErrInvalidPreparedAdd) {
		t.Fatalf("empty items error = %v, want ErrInvalidPreparedAdd", err)
	}

	items := make([]PreparedAddItem, 129)
	for i := range items {
		items[i] = PreparedAddItem{
			Item:       ResolvedItem{URL: "https://download.fixture.invalid/files/a.bin"},
			DisplayKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}
	}
	if _, err := service.AddPreparedExtractorItems(context.Background(), PreparedAddRequest{Items: items}); !errors.Is(err, ErrInvalidPreparedAdd) {
		t.Fatalf("oversize error = %v, want ErrInvalidPreparedAdd", err)
	}

	dup := []PreparedAddItem{
		{Item: ResolvedItem{URL: "https://download.fixture.invalid/files/a.bin"}, DisplayKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{Item: ResolvedItem{URL: "https://files.alpha.test/downloads/b.bin"}, DisplayKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	if _, err := service.AddPreparedExtractorItems(context.Background(), PreparedAddRequest{Items: dup}); !errors.Is(err, ErrInvalidPreparedAdd) {
		t.Fatalf("duplicate DisplayKey error = %v, want ErrInvalidPreparedAdd", err)
	}

	padded := []PreparedAddItem{
		{Item: ResolvedItem{URL: "https://download.fixture.invalid/files/a.bin"}, DisplayKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{Item: ResolvedItem{URL: "https://files.alpha.test/downloads/b.bin"}, DisplayKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa "},
	}
	if _, err := service.AddPreparedExtractorItems(context.Background(), PreparedAddRequest{Items: padded}); !errors.Is(err, ErrInvalidPreparedAdd) {
		t.Fatalf("padded DisplayKey error = %v, want ErrInvalidPreparedAdd", err)
	}
}

func TestAddPreparedExtractorItems_ReservedFilenameIsRenamed(t *testing.T) {
	cap := &capturingAddURIEngine{}
	service := setupDiskSpaceAddService(t, cap)
	service.Adapter = &countingPreparedAdapter{}

	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	result, err := service.AddPreparedExtractorItems(context.Background(), PreparedAddRequest{
		Items: []PreparedAddItem{
			{Item: ResolvedItem{URL: "https://files.alpha.test/downloads/b.bin", Filename: "CON.txt"}, DisplayKey: idA},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedExtractorItems() error = %v", err)
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

func TestAddPreparedExtractorItems_EmptyFilenameDeviceURLUsesDownloadBin(t *testing.T) {
	cap := &capturingAddURIEngine{}
	service := setupDiskSpaceAddService(t, cap)
	service.Adapter = &countingPreparedAdapter{}

	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	result, err := service.AddPreparedExtractorItems(context.Background(), PreparedAddRequest{
		Items: []PreparedAddItem{
			{Item: ResolvedItem{URL: "https://files.alpha.test/NUL", Filename: ""}, DisplayKey: idA},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedExtractorItems() error = %v", err)
	}
	if len(result.Succeeded) != 1 || result.Succeeded[0] != idA {
		t.Fatalf("succeeded = %#v, want [%q]", result.Succeeded, idA)
	}
	opts := cap.snapshotOptions()
	if len(opts) != 1 {
		t.Fatalf("AddUri calls = %d, want 1", len(opts))
	}
	if opts[0].Out == "" || opts[0].Out == "NUL" || downloadgroups.IsWindowsReservedName(opts[0].Out) {
		t.Fatalf("Out = %q, want download.bin or other non-device name", opts[0].Out)
	}
}

func TestAddPreparedExtractorItems_EmptyFilenameQueryDeviceDoesNotLeaveEmptyOut(t *testing.T) {
	cap := &capturingAddURIEngine{}
	service := setupDiskSpaceAddService(t, cap)
	service.Adapter = &countingPreparedAdapter{}

	idA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	result, err := service.AddPreparedExtractorItems(context.Background(), PreparedAddRequest{
		Items: []PreparedAddItem{
			{Item: ResolvedItem{URL: "https://files.alpha.test/ok.bin?filename=CON.txt", Filename: ""}, DisplayKey: idA},
		},
	})
	if err != nil {
		t.Fatalf("AddPreparedExtractorItems() error = %v", err)
	}
	if len(result.Succeeded) != 1 || result.Succeeded[0] != idA {
		t.Fatalf("succeeded = %#v, want [%q]", result.Succeeded, idA)
	}
	opts := cap.snapshotOptions()
	if len(opts) != 1 {
		t.Fatalf("AddUri calls = %d, want 1", len(opts))
	}
	if opts[0].Out == "" || opts[0].Out == "CON.txt" || downloadgroups.IsWindowsReservedName(opts[0].Out) {
		t.Fatalf("Out = %q, want non-empty non-device name", opts[0].Out)
	}
}

func TestAddPreparedExtractorItems_ColonDeviceNamesAreRenamed(t *testing.T) {
	cap := &capturingAddURIEngine{}
	service := setupDiskSpaceAddService(t, cap)
	service.Adapter = &countingPreparedAdapter{}

	cases := []string{"CON::$DATA", "COM1:", "NUL:"}
	items := make([]PreparedAddItem, 0, len(cases))
	for i, name := range cases {
		items = append(items, PreparedAddItem{
			Item: ResolvedItem{
				URL:      "https://files.alpha.test/downloads/" + string(rune('a'+i)) + ".bin",
				Filename: name,
			},
			DisplayKey: strings.Repeat(string(rune('a'+i)), 32),
		})
	}
	result, err := service.AddPreparedExtractorItems(context.Background(), PreparedAddRequest{Items: items})
	if err != nil {
		t.Fatalf("AddPreparedExtractorItems() error = %v", err)
	}
	if len(result.Succeeded) != len(cases) {
		t.Fatalf("succeeded = %#v, want %d ids", result.Succeeded, len(cases))
	}
	opts := cap.snapshotOptions()
	if len(opts) != len(cases) {
		t.Fatalf("AddUri calls = %d, want %d", len(opts), len(cases))
	}
	outs := make(map[string]struct{}, len(opts))
	for _, opt := range opts {
		if opt.Out == "" || downloadgroups.IsWindowsReservedName(opt.Out) {
			t.Fatalf("Out = %q, want renamed non-device name", opt.Out)
		}
		outs[opt.Out] = struct{}{}
	}
	for _, name := range cases {
		if _, ok := outs[name]; ok {
			t.Fatalf("reserved Filename %q still appeared as Out", name)
		}
	}
}

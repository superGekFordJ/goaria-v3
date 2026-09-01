package downloadgroups

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

func setupDownloadGroupsTest(t *testing.T) {
	t.Helper()

	originalCache := monitor.Cache
	originalSaveEnabled := history.SaveEnabled
	originalConfig := config.Get()

	monitor.ResetDownloadGroupNamerForTest()
	monitor.ResetTaskGroupStoreForTest(filepath.Join(t.TempDir(), "download_groups.json"), true)
	monitor.Cache = monitor.NewTaskCacheForTest()
	history.DisableSaveForTest()
	history.Clear()
	config.SetTestConfig(&config.AppConfig{ShowHistory: true})

	t.Cleanup(func() {
		monitor.ResetDownloadGroupNamerForTest()
		history.Clear()
		monitor.ResetTaskGroupStoreForTest("", true)
		history.SetSaveEnabled(originalSaveEnabled)
		monitor.Cache = originalCache
		config.SetTestConfig(originalConfig)
	})
}

type fullSnapshotForTest struct {
	Tasks struct {
		Active  []rpc.Task
		Waiting []rpc.Task
	}
}

func getFullSnapshotForTest() fullSnapshotForTest {
	snapshot := fullSnapshotForTest{}
	snapshot.Tasks.Active = monitor.Cache.GetActive()
	snapshot.Tasks.Waiting = monitor.Cache.GetWaiting()
	monitor.HydrateTaskGroups(snapshot.Tasks.Active)
	monitor.HydrateTaskGroups(snapshot.Tasks.Waiting)
	return snapshot
}

func groupReadTestGroup(id string, itemCount int) rpc.DownloadGroup {
	return rpc.DownloadGroup{
		ID:         id,
		Kind:       "batch",
		Name:       "Batch 2026-05-18 10-00-00",
		NameStatus: rpc.DownloadGroupNameStatusFallback,
		FolderName: "Batch 2026-05-18 10-00-00 " + id,
		Dir:        filepath.Join("downloads", id),
		ItemCount:  itemCount,
		CreatedAt:  1779098400,
	}
}

func groupReadTask(gid string, status string, group *rpc.DownloadGroup, total string, completed string, speed string) rpc.Task {
	task := rpc.Task{
		GID:             gid,
		Status:          status,
		TotalLength:     total,
		CompletedLength: completed,
		DownloadSpeed:   speed,
		Dir:             filepath.Join("downloads", "safe"),
		Files: []rpc.File{{
			Path: filepath.Join("downloads", "safe", gid+".bin"),
			Uris: []rpc.Uri{{Uri: "https://example.invalid/" + gid}},
		}},
	}
	if group != nil {
		task.DownloadGroup = copyDownloadGroup(group)
	}
	return task
}

func groupReadHistoryEntry(gid string, group *rpc.DownloadGroup, total string, completed string) history.HistoryEntry {
	return history.HistoryEntry{
		GID:             gid,
		Dir:             filepath.Join("history", group.ID),
		Path:            filepath.Join("history", group.ID, gid+".bin"),
		Source:          "https://example.invalid/history/" + gid,
		TotalLength:     total,
		CompletedLength: completed,
		CompletedAt:     1779098500,
		DownloadGroup:   copyDownloadGroup(group),
	}
}

func copyDownloadGroup(group *rpc.DownloadGroup) *rpc.DownloadGroup {
	if group == nil {
		return nil
	}
	copy := *group
	return &copy
}

func findDownloadGroupCard(t *testing.T, cards []DownloadGroupCard, groupKey string) DownloadGroupCard {
	t.Helper()
	for _, card := range cards {
		if card.GroupKey == groupKey {
			return card
		}
	}
	t.Fatalf("expected card %q in %#v", groupKey, cards)
	return DownloadGroupCard{}
}

func warningByCode(warnings []DownloadGroupWarning, code string) (DownloadGroupWarning, bool) {
	for _, warning := range warnings {
		if warning.Code == code {
			return warning, true
		}
	}
	return DownloadGroupWarning{}, false
}

func requireDownloadGroupWarning(t *testing.T, warnings []DownloadGroupWarning, code string) DownloadGroupWarning {
	t.Helper()
	warning, ok := warningByCode(warnings, code)
	if !ok {
		t.Fatalf("expected warning %q in %#v", code, warnings)
	}
	return warning
}

func requireNoDownloadGroupWarning(t *testing.T, warnings []DownloadGroupWarning, code string) {
	t.Helper()
	if warning, ok := warningByCode(warnings, code); ok {
		t.Fatalf("expected no warning %q, got %#v", code, warning)
	}
}

func assertDownloadGroupProgress(t *testing.T, got float64, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.000001 {
		t.Fatalf("expected progress %.6f, got %.6f", want, got)
	}
}

func TestGetDownloadGroups_BuildsAggregateCardsFromCacheStoreAndHistory(t *testing.T) {
	setupDownloadGroupsTest(t)
	group := groupReadTestGroup("dg-aggregate", 4)

	active := groupReadTask("gid-active", "active", nil, "100", "25", "10")
	waiting := groupReadTask("gid-waiting", "waiting", &group, "200", "0", "0")
	stopped := groupReadTask("gid-stopped", "complete", nil, "300", "300", "0")
	monitor.RegisterTaskGroup(active.GID, group)
	monitor.RegisterTaskGroup(stopped.GID, group)
	monitor.Cache.UpdateFromAria2([]rpc.Task{active}, []rpc.Task{waiting}, []rpc.Task{stopped})
	history.Add(groupReadHistoryEntry("gid-history-only", &group, "400", "400"))

	envelope := GetDownloadGroups()
	if envelope.Degraded {
		t.Fatalf("expected aggregate envelope not to be degraded: %#v", envelope)
	}
	if len(envelope.Groups) != 1 {
		t.Fatalf("expected one aggregate card, got %#v", envelope.Groups)
	}
	card := envelope.Groups[0]
	if card.GroupKey != group.ID {
		t.Fatalf("expected group_key %q, got %q", group.ID, card.GroupKey)
	}
	if card.DownloadGroup == nil || card.DownloadGroup.ID != group.ID {
		t.Fatalf("expected embedded download_group %q, got %#v", group.ID, card.DownloadGroup)
	}
	if card.DisplayName != group.Name || card.NameStatus != DownloadGroupNameStatusFallback || card.FallbackName == "" {
		t.Fatalf("expected generic name fields, got display=%q fallback=%q status=%q", card.DisplayName, card.FallbackName, card.NameStatus)
	}
	if card.Status != DownloadGroupStatusActive {
		t.Fatalf("expected active status, got %q", card.Status)
	}
	if card.Counts.Expected != 4 || card.Counts.Resolved != 4 || card.Counts.Missing != 0 {
		t.Fatalf("unexpected member counts: %#v", card.Counts)
	}
	if card.Counts.Active != 1 || card.Counts.Waiting != 1 || card.Counts.Complete != 2 || card.Counts.HistoryOnly != 1 {
		t.Fatalf("unexpected status counts: %#v", card.Counts)
	}
	if card.TotalLength != "1000" || card.CompletedLength != "725" || card.DownloadSpeed != "10" {
		t.Fatalf("unexpected byte sums: total=%s completed=%s speed=%s", card.TotalLength, card.CompletedLength, card.DownloadSpeed)
	}
	assertDownloadGroupProgress(t, card.Progress, 0.725)
	requireDownloadGroupWarning(t, card.Warnings, DownloadGroupWarningMixedStatus)
	requireNoDownloadGroupWarning(t, card.Warnings, DownloadGroupWarningMissingMembers)
	requireNoDownloadGroupWarning(t, card.Warnings, DownloadGroupWarningMissingMetadata)
}

func TestGetDownloadGroups_StatusWarningMatrix(t *testing.T) {
	testCases := []struct {
		name         string
		active       []string
		waiting      []string
		stopped      []string
		wantStatus   string
		wantWarnings []string
		wantDegraded bool
	}{
		{
			name:         "active priority",
			active:       []string{"active"},
			stopped:      []string{"error"},
			wantStatus:   DownloadGroupStatusActive,
			wantWarnings: []string{DownloadGroupWarningMixedStatus, DownloadGroupWarningPartialError},
		},
		{
			name:       "paused fallback",
			waiting:    []string{"paused", "complete"},
			wantStatus: DownloadGroupStatusPaused,
		},
		{
			name:         "waiting fallback",
			waiting:      []string{"waiting"},
			stopped:      []string{"complete"},
			wantStatus:   DownloadGroupStatusWaiting,
			wantWarnings: []string{DownloadGroupWarningMixedStatus},
		},
		{
			name:       "all complete",
			stopped:    []string{"complete", "complete"},
			wantStatus: DownloadGroupStatusComplete,
		},
		{
			name:       "all error",
			stopped:    []string{"error", "error"},
			wantStatus: DownloadGroupStatusError,
		},
		{
			name:         "mixed complete error partial",
			stopped:      []string{"complete", "error"},
			wantStatus:   DownloadGroupStatusError,
			wantWarnings: []string{DownloadGroupWarningMixedStatus, DownloadGroupWarningPartialError},
		},
		{
			name:         "live terminal mixed",
			active:       []string{"active"},
			stopped:      []string{"complete"},
			wantStatus:   DownloadGroupStatusActive,
			wantWarnings: []string{DownloadGroupWarningMixedStatus},
		},
	}

	for index, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			setupDownloadGroupsTest(t)
			group := groupReadTestGroup("dg-matrix-"+strings.ReplaceAll(tc.name, " ", "-"), 2)
			makeTasks := func(statuses []string, prefix string) []rpc.Task {
				tasks := make([]rpc.Task, 0, len(statuses))
				for i, status := range statuses {
					tasks = append(tasks, groupReadTask(prefix+"-"+strconv.Itoa(index)+"-"+strconv.Itoa(i), status, &group, "100", "50", "1"))
				}
				return tasks
			}
			monitor.Cache.UpdateFromAria2(makeTasks(tc.active, "active"), makeTasks(tc.waiting, "waiting"), makeTasks(tc.stopped, "stopped"))

			card := findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
			if card.Status != tc.wantStatus {
				t.Fatalf("expected status %q, got %q", tc.wantStatus, card.Status)
			}
			if card.Degraded != tc.wantDegraded {
				t.Fatalf("expected degraded=%v, got %v warnings=%#v", tc.wantDegraded, card.Degraded, card.Warnings)
			}
			for _, code := range tc.wantWarnings {
				requireDownloadGroupWarning(t, card.Warnings, code)
			}
		})
	}
}

func TestGetDownloadGroups_DegradesMissingMembersAndStaleStoredGroups(t *testing.T) {
	setupDownloadGroupsTest(t)
	group := groupReadTestGroup("dg-degraded", 4)
	monitor.Cache.UpdateFromAria2([]rpc.Task{groupReadTask("gid-resolved", "active", &group, "100", "50", "5")}, nil, nil)
	monitor.RegisterTaskGroup("gid-stale", group)

	card := findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if !card.Degraded {
		t.Fatalf("expected degraded card, got %#v", card)
	}
	if card.Counts.Expected != 4 || card.Counts.Resolved != 1 || card.Counts.Missing != 3 {
		t.Fatalf("unexpected degraded counts: %#v", card.Counts)
	}
	missing := requireDownloadGroupWarning(t, card.Warnings, DownloadGroupWarningMissingMembers)
	if missing.Severity != "warning" || missing.Count != 3 {
		t.Fatalf("unexpected missing_members warning: %#v", missing)
	}
	stale := requireDownloadGroupWarning(t, card.Warnings, DownloadGroupWarningStaleGroup)
	if stale.Severity != "warning" || stale.Count != 1 {
		t.Fatalf("unexpected stale_group warning: %#v", stale)
	}
}

func TestGetDownloadGroups_HistoryOnlyResidueWarnsWithoutInventingMembership(t *testing.T) {
	setupDownloadGroupsTest(t)
	group := groupReadTestGroup("dg-history-only-residue", 2)
	history.Add(groupReadHistoryEntry("gid-history-one", &group, "100", "100"))
	history.Add(groupReadHistoryEntry("gid-history-two", &group, "200", "200"))

	card := findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if card.Status != DownloadGroupStatusComplete {
		t.Fatalf("expected complete history-only status, got %q", card.Status)
	}
	if card.Counts.Resolved != 2 || card.Counts.HistoryOnly != 2 || card.Counts.Missing != 0 {
		t.Fatalf("unexpected history-only counts: %#v", card.Counts)
	}
	warning := requireDownloadGroupWarning(t, card.Warnings, DownloadGroupWarningHistoryOnly)
	if warning.Severity != "info" || warning.Count != 2 {
		t.Fatalf("unexpected history_only warning: %#v", warning)
	}
	if card.Degraded {
		t.Fatalf("history_only warning should not degrade card: %#v", card.Warnings)
	}
}

func TestGetDownloadGroupDetail_ReturnsBackendResolvedSplitEnvelope(t *testing.T) {
	setupDownloadGroupsTest(t)
	group := groupReadTestGroup("dg-detail", 4)
	active := groupReadTask("gid-detail-active", "active", &group, "100", "20", "5")
	waiting := groupReadTask("gid-detail-waiting", "waiting", &group, "100", "0", "0")
	stopped := groupReadTask("gid-detail-stopped", "complete", &group, "100", "100", "0")
	monitor.Cache.UpdateFromAria2([]rpc.Task{active}, []rpc.Task{waiting}, []rpc.Task{stopped})
	history.Add(groupReadHistoryEntry("gid-detail-history", &group, "100", "100"))

	detail := GetDownloadGroupDetail("  " + group.ID + "  ")
	if !detail.Found || detail.GroupKey != group.ID || detail.Group.GroupKey != group.ID {
		t.Fatalf("expected found detail for %q, got %#v", group.ID, detail)
	}
	if len(detail.Tasks.Active) != 1 || len(detail.Tasks.Waiting) != 1 || len(detail.Tasks.Stopped) != 2 {
		t.Fatalf("unexpected split task lists: %#v", detail.Tasks)
	}
	for _, tasks := range [][]rpc.Task{detail.Tasks.Active, detail.Tasks.Waiting, detail.Tasks.Stopped} {
		for _, task := range tasks {
			if task.DownloadGroup == nil || task.DownloadGroup.ID != group.ID {
				t.Fatalf("expected task-level download_group on %q, got %#v", task.GID, task.DownloadGroup)
			}
		}
	}
	if detail.Group.Counts.Resolved != 4 || detail.Group.Counts.Active != 1 || detail.Group.Counts.Waiting != 1 || detail.Group.Counts.Complete != 2 {
		t.Fatalf("unexpected detail card counts: %#v", detail.Group.Counts)
	}
}

func TestGetDownloadGroupDetail_UnknownGroupReturnsDegradedEnvelope(t *testing.T) {
	setupDownloadGroupsTest(t)

	detail := GetDownloadGroupDetail("  missing-group  ")
	if detail.Found {
		t.Fatalf("expected unknown group not found, got %#v", detail)
	}
	if detail.GroupKey != "missing-group" || detail.Group.GroupKey != "missing-group" {
		t.Fatalf("expected normalized group key, got detail=%q card=%q", detail.GroupKey, detail.Group.GroupKey)
	}
	if !detail.Degraded || !detail.Group.Degraded {
		t.Fatalf("expected degraded unknown group envelope, got %#v", detail)
	}
	if len(detail.Tasks.Active) != 0 || len(detail.Tasks.Waiting) != 0 || len(detail.Tasks.Stopped) != 0 {
		t.Fatalf("expected empty split lists, got %#v", detail.Tasks)
	}
	warning := requireDownloadGroupWarning(t, detail.Warnings, DownloadGroupWarningGroupNotFound)
	if warning.Severity != "warning" {
		t.Fatalf("unexpected group_not_found warning: %#v", warning)
	}
	requireDownloadGroupWarning(t, detail.Group.Warnings, DownloadGroupWarningGroupNotFound)
}

func TestGetDownloadGroups_AfterFullSnapshotRefetchHydratesGroupsForWindowRestore(t *testing.T) {
	setupDownloadGroupsTest(t)
	group := groupReadTestGroup("dg-window-restore", 2)
	active := groupReadTask("gid-window-active", "active", nil, "100", "10", "1")
	waiting := groupReadTask("gid-window-waiting", "waiting", nil, "100", "0", "0")
	monitor.RegisterTaskGroup(active.GID, group)
	monitor.RegisterTaskGroup(waiting.GID, group)
	monitor.Cache.UpdateFromAria2([]rpc.Task{active}, []rpc.Task{waiting}, nil)

	snapshot := getFullSnapshotForTest()
	if len(snapshot.Tasks.Active) != 1 || len(snapshot.Tasks.Waiting) != 1 {
		t.Fatalf("expected full task snapshot before group refetch, got %#v", snapshot.Tasks)
	}

	card := findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if card.GroupKey != group.ID || card.Counts.Resolved != 2 || card.Status != DownloadGroupStatusActive {
		t.Fatalf("expected post-snapshot group refetch to define master data, got %#v", card)
	}
}

func TestGetDownloadGroups_UsesOpaqueGroupKeyAndSuppressesUnsafeFolderHints(t *testing.T) {
	setupDownloadGroupsTest(t)
	group := groupReadTestGroup("opaque-dg-12345", 2)
	group.Name = ""
	group.FolderName = "Safe Folder"
	group.Dir = "https://source.example.invalid/file?token=secret"
	taskOne := groupReadTask("gid-opaque-one", "active", &group, "100", "50", "1")
	taskOne.Files = []rpc.File{{
		Path: "source.example.invalid-file.bin",
		Uris: []rpc.Uri{{Uri: "https://source.example.invalid/file?token=secret"}},
	}}
	taskTwo := groupReadTask("gid-opaque-two", "waiting", &group, "100", "0", "0")
	monitor.Cache.UpdateFromAria2([]rpc.Task{taskOne}, []rpc.Task{taskTwo}, nil)

	card := findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if card.GroupKey != group.ID {
		t.Fatalf("expected opaque group key to equal download_group.id %q, got %q", group.ID, card.GroupKey)
	}
	for _, value := range []string{card.DisplayName, card.FallbackName} {
		lower := strings.ToLower(value)
		if strings.Contains(lower, "source.example") || strings.Contains(lower, "token") || strings.Contains(lower, "file.bin") {
			t.Fatalf("expected generic card names, got %q", value)
		}
	}
	if card.FolderPathHint != "" {
		t.Fatalf("expected unsafe folder path hint suppressed, got %q", card.FolderPathHint)
	}
	if card.FolderLabel != "Safe Folder" || !card.HasFolder {
		t.Fatalf("expected safe folder label and has_folder=true, got label=%q has=%v", card.FolderLabel, card.HasFolder)
	}
}

func TestGetDownloadGroups_MapsNamePendingAndDegradedWarnings(t *testing.T) {
	testCases := []struct {
		name         string
		status       string
		groupName    string
		wantStatus   string
		wantWarning  string
		wantSeverity string
		wantDegraded bool
	}{
		{
			name:         "pending emits info warning",
			status:       rpc.DownloadGroupNameStatusPending,
			groupName:    "Batch 2026-05-18 10-00-00",
			wantStatus:   rpc.DownloadGroupNameStatusPending,
			wantWarning:  DownloadGroupWarningNamePending,
			wantSeverity: "info",
		},
		{
			name:         "degraded emits warning",
			status:       rpc.DownloadGroupNameStatusDegraded,
			groupName:    "Batch 2026-05-18 10-00-00",
			wantStatus:   rpc.DownloadGroupNameStatusDegraded,
			wantWarning:  DownloadGroupWarningNameDegraded,
			wantSeverity: "warning",
			wantDegraded: true,
		},
		{
			name:         "unknown status degrades",
			status:       "mystery",
			groupName:    "Batch 2026-05-18 10-00-00",
			wantStatus:   rpc.DownloadGroupNameStatusDegraded,
			wantWarning:  DownloadGroupWarningNameDegraded,
			wantSeverity: "warning",
			wantDegraded: true,
		},
		{
			name:         "unsafe stable name degrades",
			status:       rpc.DownloadGroupNameStatusStable,
			groupName:    "https://example.invalid/file?token=secret",
			wantStatus:   rpc.DownloadGroupNameStatusDegraded,
			wantWarning:  DownloadGroupWarningNameDegraded,
			wantSeverity: "warning",
			wantDegraded: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			setupDownloadGroupsTest(t)
			group := groupReadTestGroup("dg-name-"+strings.ReplaceAll(tc.name, " ", "-"), 2)
			group.Name = tc.groupName
			group.NameStatus = tc.status
			monitor.Cache.UpdateFromAria2([]rpc.Task{
				groupReadTask("gid-one", "active", &group, "100", "50", "1"),
				groupReadTask("gid-two", "waiting", &group, "100", "0", "0"),
			}, nil, nil)

			card := findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
			if card.NameStatus != tc.wantStatus {
				t.Fatalf("expected name_status %q, got %q", tc.wantStatus, card.NameStatus)
			}
			warning := requireDownloadGroupWarning(t, card.Warnings, tc.wantWarning)
			if warning.Severity != tc.wantSeverity {
				t.Fatalf("expected warning severity %q, got %#v", tc.wantSeverity, warning)
			}
			if card.Degraded != tc.wantDegraded {
				t.Fatalf("expected degraded=%v, got card=%#v", tc.wantDegraded, card)
			}
		})
	}
}

func TestGetDownloadGroups_UsesStableSmartNameFromBackendMetadata(t *testing.T) {
	setupDownloadGroupsTest(t)
	group := groupReadTestGroup("dg-smart-name", 2)
	group.Name = "Project Alpha"
	group.NameStatus = rpc.DownloadGroupNameStatusStable
	monitor.Cache.UpdateFromAria2([]rpc.Task{
		groupReadTask("gid-stable-one", "active", &group, "100", "50", "1"),
		groupReadTask("gid-stable-two", "waiting", &group, "100", "0", "0"),
	}, nil, nil)

	card := findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if card.DisplayName != "Project Alpha" || card.NameStatus != rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected stable smart display name, got display=%q status=%q", card.DisplayName, card.NameStatus)
	}
	requireNoDownloadGroupWarning(t, card.Warnings, DownloadGroupWarningNamePending)
	requireNoDownloadGroupWarning(t, card.Warnings, DownloadGroupWarningNameDegraded)
}

func TestGetDownloadGroups_DoesNotUseDisplayNameAsFolderLabel(t *testing.T) {
	setupDownloadGroupsTest(t)
	group := groupReadTestGroup("dg-display-folder-separation", 2)
	group.Name = "Project Display Name"
	group.NameStatus = rpc.DownloadGroupNameStatusStable
	group.FolderName = ""
	group.Dir = ""
	monitor.Cache.UpdateFromAria2([]rpc.Task{
		groupReadTask("gid-display-folder-one", "active", &group, "100", "50", "1"),
		groupReadTask("gid-display-folder-two", "waiting", &group, "100", "0", "0"),
	}, nil, nil)

	card := findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if card.DisplayName != "Project Display Name" {
		t.Fatalf("expected stable display name, got %q", card.DisplayName)
	}
	if card.FolderLabel != "" {
		t.Fatalf("expected no folder_label fallback from display name, got %q", card.FolderLabel)
	}
}

func TestGetDownloadGroupDetail_ProjectsHistoriedErrorStatus(t *testing.T) {
	setupDownloadGroupsTest(t)
	group := groupReadTestGroup("dg-history-error", 2)
	entry := groupReadHistoryEntry("gid-hist-err", &group, "100", "10")
	entry.Status = "error"
	history.Add(entry)
	history.Add(groupReadHistoryEntry("gid-hist-ok", &group, "100", "100"))

	detail := GetDownloadGroupDetail(group.ID)
	if !detail.Found {
		t.Fatal("expected group detail found")
	}
	found := false
	for _, task := range detail.Tasks.Stopped {
		if task.GID == entry.GID {
			found = true
			if task.Status != "error" {
				t.Fatalf("expected history-only stopped status error, got %q", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected history-only error task in stopped members")
	}
}

func TestGetDownloadGroups_ExcludesLiveActiveGIDFromHistoryOnly(t *testing.T) {
	setupDownloadGroupsTest(t)
	group := groupReadTestGroup("dg-live-exclude", 2)
	monitor.Cache.UpdateFromAria2([]rpc.Task{
		groupReadTask("gid-live-twin", "active", &group, "100", "50", "1"),
		groupReadTask("gid-live-sibling", "waiting", &group, "100", "0", "0"),
	}, nil, nil)
	entry := groupReadHistoryEntry("gid-live-twin", &group, "100", "50")
	entry.Status = "error"
	history.Add(entry)

	detail := GetDownloadGroupDetail(group.ID)
	if !detail.Found {
		t.Fatal("expected group detail found")
	}
	for _, task := range detail.Tasks.Stopped {
		if task.GID == "gid-live-twin" {
			t.Fatal("expected live active GID excluded from stopped/history members")
		}
	}
	foundActive := false
	for _, task := range detail.Tasks.Active {
		if task.GID == "gid-live-twin" {
			foundActive = true
		}
	}
	if !foundActive {
		t.Fatal("expected GID present in active members")
	}
}

func TestGetDownloadGroups_TerminalUnknownSizeGroup_AggregatesToFull(t *testing.T) {
	setupDownloadGroupsTest(t)
	group := groupReadTestGroup("dg-unknown-size-terminal", 2)

	// Task 1: cache stopped complete 0/1000
	t1 := groupReadTask("gid-dg-t1", "complete", &group, "0", "1000", "0")
	monitor.Cache.UpdateFromAria2(nil, nil, []rpc.Task{t1})

	// Task 2: history-only complete 0/2000
	e2 := groupReadHistoryEntry("gid-dg-t2", &group, "0", "2000")
	e2.Status = "complete"
	history.Add(e2)

	card := findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if card.CompletedLength != "3000" {
		t.Errorf("card.CompletedLength = %q, want 3000", card.CompletedLength)
	}
	if card.TotalLength != "3000" {
		t.Errorf("card.TotalLength = %q, want 3000", card.TotalLength)
	}
	if math.Abs(card.Progress-1.0) > 1e-6 {
		t.Errorf("card.Progress = %f, want 1.0", card.Progress)
	}

	detail := GetDownloadGroupDetail(group.ID)
	if !detail.Found {
		t.Fatal("expected group detail found")
	}
	for _, task := range detail.Tasks.Stopped {
		if task.GID == "gid-dg-t1" {
			if task.TotalLength != "1000" || task.CompletedLength != "1000" {
				t.Errorf("gid-dg-t1 lengths = (%q, %q), want (1000, 1000)", task.TotalLength, task.CompletedLength)
			}
		}
		if task.GID == "gid-dg-t2" {
			if task.TotalLength != "2000" || task.CompletedLength != "2000" {
				t.Errorf("gid-dg-t2 lengths = (%q, %q), want (2000, 2000)", task.TotalLength, task.CompletedLength)
			}
		}
	}
}

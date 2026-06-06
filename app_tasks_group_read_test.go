package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

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
	app := setupAppTaskHistoryTest(t)
	group := groupReadTestGroup("dg-aggregate", 4)

	active := groupReadTask("gid-active", "active", nil, "100", "25", "10")
	waiting := groupReadTask("gid-waiting", "waiting", &group, "200", "0", "0")
	stopped := groupReadTask("gid-stopped", "complete", nil, "300", "300", "0")
	monitor.RegisterTaskGroup(active.GID, group)
	monitor.RegisterTaskGroup(stopped.GID, group)
	monitor.Cache.UpdateFromAria2([]rpc.Task{active}, []rpc.Task{waiting}, []rpc.Task{stopped})
	history.Add(groupReadHistoryEntry("gid-history-only", &group, "400", "400"))

	envelope := app.GetDownloadGroups()
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
	if card.DisplayName != group.Name || card.NameStatus != downloadGroupNameStatusFallback || card.FallbackName == "" {
		t.Fatalf("expected generic name fields, got display=%q fallback=%q status=%q", card.DisplayName, card.FallbackName, card.NameStatus)
	}
	if card.Status != downloadGroupStatusActive {
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
	requireDownloadGroupWarning(t, card.Warnings, downloadGroupWarningMixedStatus)
	requireNoDownloadGroupWarning(t, card.Warnings, downloadGroupWarningMissingMembers)
	requireNoDownloadGroupWarning(t, card.Warnings, downloadGroupWarningMissingMetadata)
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
			wantStatus:   downloadGroupStatusActive,
			wantWarnings: []string{downloadGroupWarningMixedStatus, downloadGroupWarningPartialError},
		},
		{
			name:       "paused fallback",
			waiting:    []string{"paused", "complete"},
			wantStatus: downloadGroupStatusPaused,
		},
		{
			name:         "waiting fallback",
			waiting:      []string{"waiting"},
			stopped:      []string{"complete"},
			wantStatus:   downloadGroupStatusWaiting,
			wantWarnings: []string{downloadGroupWarningMixedStatus},
		},
		{
			name:       "all complete",
			stopped:    []string{"complete", "complete"},
			wantStatus: downloadGroupStatusComplete,
		},
		{
			name:       "all error",
			stopped:    []string{"error", "error"},
			wantStatus: downloadGroupStatusError,
		},
		{
			name:         "mixed complete error partial",
			stopped:      []string{"complete", "error"},
			wantStatus:   downloadGroupStatusError,
			wantWarnings: []string{downloadGroupWarningMixedStatus, downloadGroupWarningPartialError},
		},
		{
			name:         "live terminal mixed",
			active:       []string{"active"},
			stopped:      []string{"complete"},
			wantStatus:   downloadGroupStatusActive,
			wantWarnings: []string{downloadGroupWarningMixedStatus},
		},
	}

	for index, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			app := setupAppTaskHistoryTest(t)
			group := groupReadTestGroup("dg-matrix-"+strings.ReplaceAll(tc.name, " ", "-"), 2)
			makeTasks := func(statuses []string, prefix string) []rpc.Task {
				tasks := make([]rpc.Task, 0, len(statuses))
				for i, status := range statuses {
					tasks = append(tasks, groupReadTask(prefix+"-"+strconv.Itoa(index)+"-"+strconv.Itoa(i), status, &group, "100", "50", "1"))
				}
				return tasks
			}
			monitor.Cache.UpdateFromAria2(makeTasks(tc.active, "active"), makeTasks(tc.waiting, "waiting"), makeTasks(tc.stopped, "stopped"))

			card := findDownloadGroupCard(t, app.GetDownloadGroups().Groups, group.ID)
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
	app := setupAppTaskHistoryTest(t)
	group := groupReadTestGroup("dg-degraded", 4)
	monitor.Cache.UpdateFromAria2([]rpc.Task{groupReadTask("gid-resolved", "active", &group, "100", "50", "5")}, nil, nil)
	monitor.RegisterTaskGroup("gid-stale", group)

	card := findDownloadGroupCard(t, app.GetDownloadGroups().Groups, group.ID)
	if !card.Degraded {
		t.Fatalf("expected degraded card, got %#v", card)
	}
	if card.Counts.Expected != 4 || card.Counts.Resolved != 1 || card.Counts.Missing != 3 {
		t.Fatalf("unexpected degraded counts: %#v", card.Counts)
	}
	missing := requireDownloadGroupWarning(t, card.Warnings, downloadGroupWarningMissingMembers)
	if missing.Severity != "warning" || missing.Count != 3 {
		t.Fatalf("unexpected missing_members warning: %#v", missing)
	}
	stale := requireDownloadGroupWarning(t, card.Warnings, downloadGroupWarningStaleGroup)
	if stale.Severity != "warning" || stale.Count != 1 {
		t.Fatalf("unexpected stale_group warning: %#v", stale)
	}
}

func TestGetDownloadGroups_HistoryOnlyResidueWarnsWithoutInventingMembership(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	group := groupReadTestGroup("dg-history-only-residue", 2)
	history.Add(groupReadHistoryEntry("gid-history-one", &group, "100", "100"))
	history.Add(groupReadHistoryEntry("gid-history-two", &group, "200", "200"))

	card := findDownloadGroupCard(t, app.GetDownloadGroups().Groups, group.ID)
	if card.Status != downloadGroupStatusComplete {
		t.Fatalf("expected complete history-only status, got %q", card.Status)
	}
	if card.Counts.Resolved != 2 || card.Counts.HistoryOnly != 2 || card.Counts.Missing != 0 {
		t.Fatalf("unexpected history-only counts: %#v", card.Counts)
	}
	warning := requireDownloadGroupWarning(t, card.Warnings, downloadGroupWarningHistoryOnly)
	if warning.Severity != "info" || warning.Count != 2 {
		t.Fatalf("unexpected history_only warning: %#v", warning)
	}
	if card.Degraded {
		t.Fatalf("history_only warning should not degrade card: %#v", card.Warnings)
	}
}

func TestGetDownloadGroupDetail_ReturnsBackendResolvedSplitEnvelope(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	group := groupReadTestGroup("dg-detail", 4)
	active := groupReadTask("gid-detail-active", "active", &group, "100", "20", "5")
	waiting := groupReadTask("gid-detail-waiting", "waiting", &group, "100", "0", "0")
	stopped := groupReadTask("gid-detail-stopped", "complete", &group, "100", "100", "0")
	monitor.Cache.UpdateFromAria2([]rpc.Task{active}, []rpc.Task{waiting}, []rpc.Task{stopped})
	history.Add(groupReadHistoryEntry("gid-detail-history", &group, "100", "100"))

	detail := app.GetDownloadGroupDetail("  " + group.ID + "  ")
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
	app := setupAppTaskHistoryTest(t)

	detail := app.GetDownloadGroupDetail("  missing-group  ")
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
	warning := requireDownloadGroupWarning(t, detail.Warnings, downloadGroupWarningGroupNotFound)
	if warning.Severity != "warning" {
		t.Fatalf("unexpected group_not_found warning: %#v", warning)
	}
	requireDownloadGroupWarning(t, detail.Group.Warnings, downloadGroupWarningGroupNotFound)
}

func TestGetDownloadGroups_AfterFullSnapshotRefetchHydratesGroupsForWindowRestore(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	group := groupReadTestGroup("dg-window-restore", 2)
	active := groupReadTask("gid-window-active", "active", nil, "100", "10", "1")
	waiting := groupReadTask("gid-window-waiting", "waiting", nil, "100", "0", "0")
	monitor.RegisterTaskGroup(active.GID, group)
	monitor.RegisterTaskGroup(waiting.GID, group)
	monitor.Cache.UpdateFromAria2([]rpc.Task{active}, []rpc.Task{waiting}, nil)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch req.Method {
		case "aria2.tellActive":
			_ = json.NewEncoder(w).Encode(snapshotRPCResponse([]map[string]any{{
				"gid":             "gid-window-active",
				"status":          "active",
				"totalLength":     "100",
				"completedLength": "10",
				"downloadSpeed":   "1",
				"dir":             active.Dir,
				"files": []map[string]any{{
					"path": active.Files[0].Path,
					"uris": []map[string]any{{"uri": active.Files[0].Uris[0].Uri}},
				}},
			}}))
		case "aria2.tellWaiting":
			_ = json.NewEncoder(w).Encode(snapshotRPCResponse([]map[string]any{{
				"gid":             "gid-window-waiting",
				"status":          "waiting",
				"totalLength":     "100",
				"completedLength": "0",
				"downloadSpeed":   "0",
				"dir":             waiting.Dir,
				"files": []map[string]any{{
					"path": waiting.Files[0].Path,
					"uris": []map[string]any{{"uri": waiting.Files[0].Uris[0].Uri}},
				}},
			}}))
		default:
			_ = json.NewEncoder(w).Encode(snapshotRPCResponse([]any{}))
		}
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	rpc.Init(parts[len(parts)-1], "secret")

	snapshot := app.GetFullSnapshot()
	if len(snapshot.Tasks.Active) != 1 || len(snapshot.Tasks.Waiting) != 1 {
		t.Fatalf("expected full task snapshot before group refetch, got %#v", snapshot.Tasks)
	}

	card := findDownloadGroupCard(t, app.GetDownloadGroups().Groups, group.ID)
	if card.GroupKey != group.ID || card.Counts.Resolved != 2 || card.Status != downloadGroupStatusActive {
		t.Fatalf("expected post-snapshot group refetch to define master data, got %#v", card)
	}
}

func TestGetDownloadGroups_UsesOpaqueGroupKeyAndSuppressesUnsafeFolderHints(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
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

	card := findDownloadGroupCard(t, app.GetDownloadGroups().Groups, group.ID)
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
			wantWarning:  downloadGroupWarningNamePending,
			wantSeverity: "info",
		},
		{
			name:         "degraded emits warning",
			status:       rpc.DownloadGroupNameStatusDegraded,
			groupName:    "Batch 2026-05-18 10-00-00",
			wantStatus:   rpc.DownloadGroupNameStatusDegraded,
			wantWarning:  downloadGroupWarningNameDegraded,
			wantSeverity: "warning",
			wantDegraded: true,
		},
		{
			name:         "unknown status degrades",
			status:       "mystery",
			groupName:    "Batch 2026-05-18 10-00-00",
			wantStatus:   rpc.DownloadGroupNameStatusDegraded,
			wantWarning:  downloadGroupWarningNameDegraded,
			wantSeverity: "warning",
			wantDegraded: true,
		},
		{
			name:         "unsafe stable name degrades",
			status:       rpc.DownloadGroupNameStatusStable,
			groupName:    "https://example.invalid/file?token=secret",
			wantStatus:   rpc.DownloadGroupNameStatusDegraded,
			wantWarning:  downloadGroupWarningNameDegraded,
			wantSeverity: "warning",
			wantDegraded: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			app := setupAppTaskHistoryTest(t)
			group := groupReadTestGroup("dg-name-"+strings.ReplaceAll(tc.name, " ", "-"), 2)
			group.Name = tc.groupName
			group.NameStatus = tc.status
			monitor.Cache.UpdateFromAria2([]rpc.Task{
				groupReadTask("gid-one", "active", &group, "100", "50", "1"),
				groupReadTask("gid-two", "waiting", &group, "100", "0", "0"),
			}, nil, nil)

			card := findDownloadGroupCard(t, app.GetDownloadGroups().Groups, group.ID)
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
	app := setupAppTaskHistoryTest(t)
	group := groupReadTestGroup("dg-smart-name", 2)
	group.Name = "Project Alpha"
	group.NameStatus = rpc.DownloadGroupNameStatusStable
	monitor.Cache.UpdateFromAria2([]rpc.Task{
		groupReadTask("gid-stable-one", "active", &group, "100", "50", "1"),
		groupReadTask("gid-stable-two", "waiting", &group, "100", "0", "0"),
	}, nil, nil)

	card := findDownloadGroupCard(t, app.GetDownloadGroups().Groups, group.ID)
	if card.DisplayName != "Project Alpha" || card.NameStatus != rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected stable smart display name, got display=%q status=%q", card.DisplayName, card.NameStatus)
	}
	requireNoDownloadGroupWarning(t, card.Warnings, downloadGroupWarningNamePending)
	requireNoDownloadGroupWarning(t, card.Warnings, downloadGroupWarningNameDegraded)
}

func TestGetDownloadGroups_DoesNotUseDisplayNameAsFolderLabel(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	group := groupReadTestGroup("dg-display-folder-separation", 2)
	group.Name = "Project Display Name"
	group.NameStatus = rpc.DownloadGroupNameStatusStable
	group.FolderName = ""
	group.Dir = ""
	monitor.Cache.UpdateFromAria2([]rpc.Task{
		groupReadTask("gid-display-folder-one", "active", &group, "100", "50", "1"),
		groupReadTask("gid-display-folder-two", "waiting", &group, "100", "0", "0"),
	}, nil, nil)

	card := findDownloadGroupCard(t, app.GetDownloadGroups().Groups, group.ID)
	if card.DisplayName != "Project Display Name" {
		t.Fatalf("expected stable display name, got %q", card.DisplayName)
	}
	if card.FolderLabel != "" {
		t.Fatalf("expected no folder_label fallback from display name, got %q", card.FolderLabel)
	}
}

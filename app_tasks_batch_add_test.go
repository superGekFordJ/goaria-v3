package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

type batchAddRPCRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

type batchAddRPCSnapshots struct {
	active  []rpc.Task
	waiting []rpc.Task
	stopped []rpc.Task
}

type batchAddRPCCounter struct {
	mu      sync.Mutex
	methods map[string]int
	addURIs []string
	options []map[string]any
	failAll bool
}

func newBatchAddRPCCounter() *batchAddRPCCounter {
	return &batchAddRPCCounter{
		methods: make(map[string]int),
		addURIs: []string{},
	}
}

func (c *batchAddRPCCounter) recordMethod(method string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.methods[method]++
}

func (c *batchAddRPCCounter) recordAddURI(uri string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addURIs = append(c.addURIs, uri)
}

func (c *batchAddRPCCounter) recordOptions(options map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.options = append(c.options, options)
}

func (c *batchAddRPCCounter) count(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.methods[method]
}

func (c *batchAddRPCCounter) addURICount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.addURIs)
}

func (c *batchAddRPCCounter) addURIsSnapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]string, len(c.addURIs))
	copy(result, c.addURIs)
	return result
}

func (c *batchAddRPCCounter) optionsSnapshot() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]map[string]any, len(c.options))
	copy(result, c.options)
	return result
}

func batchAddSuccessResponse(result any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      "goaria",
		"result":  result,
	}
}

func batchAddTaskListResult(tasks []rpc.Task) []rpc.Task {
	if tasks == nil {
		return []rpc.Task{}
	}
	return tasks
}

func setupAppTaskBatchAddTest(t *testing.T, snapshots batchAddRPCSnapshots) (*App, *batchAddRPCCounter) {
	t.Helper()

	originalConfig := config.Current
	originalSaveEnabled := history.SaveEnabled

	monitor.ResetTaskGroupStoreForTest(filepath.Join(t.TempDir(), "download_groups.json"), true)
	history.DisableSaveForTest()
	history.Clear()
	config.Current = &config.AppConfig{
		DownloadDir:     t.TempDir(),
		SmartThreadMode: false,
	}

	counter := newBatchAddRPCCounter()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var req batchAddRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		counter.recordMethod(req.Method)
		switch req.Method {
		case "aria2.tellActive":
			_ = json.NewEncoder(w).Encode(batchAddSuccessResponse(batchAddTaskListResult(snapshots.active)))
		case "aria2.tellWaiting":
			_ = json.NewEncoder(w).Encode(batchAddSuccessResponse(batchAddTaskListResult(snapshots.waiting)))
		case "aria2.tellStopped":
			_ = json.NewEncoder(w).Encode(batchAddSuccessResponse(batchAddTaskListResult(snapshots.stopped)))
		case "aria2.addUri":
			uri, options := batchAddParams(req.Params)
			counter.recordAddURI(uri)
			counter.recordOptions(options)
			if counter.failAll {
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "error": map[string]any{"code": 1, "message": "mock add failure"}})
				return
			}
			_ = json.NewEncoder(w).Encode(batchAddSuccessResponse(fmt.Sprintf("gid-%d", counter.addURICount())))
		case "aria2.saveSession":
			_ = json.NewEncoder(w).Encode(batchAddSuccessResponse("OK"))
		default:
			_ = json.NewEncoder(w).Encode(batchAddSuccessResponse("OK"))
		}
	}))

	parsedURL, err := url.Parse(server.URL)
	if err != nil {
		server.Close()
		t.Fatalf("failed to parse httptest server URL: %v", err)
	}
	_, port, err := net.SplitHostPort(parsedURL.Host)
	if err != nil {
		server.Close()
		t.Fatalf("failed to parse httptest server port: %v", err)
	}
	rpc.Init(port, "secret")

	t.Cleanup(func() {
		server.Close()
		history.Clear()
		monitor.ResetTaskGroupStoreForTest("", true)
		history.SetSaveEnabled(originalSaveEnabled)
		config.Current = originalConfig
	})

	return NewApp(), counter
}

func batchAddParams(params []json.RawMessage) (string, map[string]any) {
	for _, param := range params {
		var urls []string
		if err := json.Unmarshal(param, &urls); err == nil && len(urls) > 0 {
			options := map[string]any{}
			if len(params) > 0 {
				_ = json.Unmarshal(params[len(params)-1], &options)
			}
			return urls[0], options
		}
	}
	return "", nil
}

func taskWithSourceURL(gid string, source string) rpc.Task {
	return rpc.Task{
		GID: gid,
		Files: []rpc.File{{
			Uris: []rpc.Uri{{Uri: source}},
		}},
	}
}

func assertBatchAddStrings(t *testing.T, name string, got []string, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %s %#v, got %#v", name, want, got)
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func assertGroupNameIsGeneric(t *testing.T, group rpc.DownloadGroup) {
	t.Helper()
	for _, value := range []string{group.ID, group.Name, group.FolderName} {
		lower := strings.ToLower(value)
		for _, forbidden := range []string{"gofile", "ibb", "example.com", "token", "share", "?", "http://", "https://"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("group metadata %q contains forbidden marker %q: %#v", value, forbidden, group)
			}
		}
	}
}

func TestBatchAddUri_DeduplicatesHistorySourceWithoutAddUri(t *testing.T) {
	app, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	historyURL := "https://example.com/history.iso"
	history.Add(history.HistoryEntry{GID: "gid-history", Source: historyURL})

	result := app.BatchAddUri([]string{" " + historyURL + " "})

	assertBatchAddStrings(t, "duplicates", result.Duplicates, []string{historyURL})
	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{})
	if len(result.Errors) != 0 {
		t.Fatalf("expected no errors, got %#v", result.Errors)
	}
	if got := counter.addURICount(); got != 0 {
		t.Fatalf("expected no aria2.addUri calls for history duplicate, got %d", got)
	}
}

func TestBatchAddUri_PreservesExistingAndBatchDuplicateOrder(t *testing.T) {
	activeURL := "https://example.com/active.iso"
	waitingURL := "https://example.com/waiting.iso"
	stoppedURL := "https://example.com/stopped.iso"
	historyURL := "https://example.com/history.iso"
	newURL := "https://example.com/new.iso"

	app, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{
		active:  []rpc.Task{taskWithSourceURL("gid-active", " "+activeURL+" ")},
		waiting: []rpc.Task{taskWithSourceURL("gid-waiting", waitingURL)},
		stopped: []rpc.Task{taskWithSourceURL("gid-stopped", stoppedURL)},
	})
	history.Add(history.HistoryEntry{GID: "gid-history", Source: historyURL})

	result := app.BatchAddUri([]string{
		" " + activeURL + " ",
		"\t" + waitingURL + "\n",
		" " + historyURL + " ",
		newURL,
		" " + newURL + " ",
		"   ",
		" " + stoppedURL + " ",
	})

	assertBatchAddStrings(t, "duplicates", result.Duplicates, []string{activeURL, waitingURL, historyURL, newURL, stoppedURL})
	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{newURL})
	if len(result.Errors) != 0 {
		t.Fatalf("expected no errors, got %#v", result.Errors)
	}
	if got := counter.addURIsSnapshot(); !reflect.DeepEqual(got, []string{newURL}) {
		t.Fatalf("expected one aria2.addUri call with %q, got %#v", newURL, got)
	}
	if got := counter.count("aria2.saveSession"); got != 1 {
		t.Fatalf("expected one saveSession call for successful add, got %d", got)
	}
}

func TestBatchAddUri_TruncatesAt100BeforeProcessing(t *testing.T) {
	app, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	urls := make([]string, 101)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://example.com/%03d.iso", i)
	}

	result := app.BatchAddUri(urls)

	assertBatchAddStrings(t, "succeeded", result.Succeeded, urls[:100])
	assertBatchAddStrings(t, "duplicates", result.Duplicates, []string{})
	if len(result.Errors) != 0 {
		t.Fatalf("expected no errors, got %#v", result.Errors)
	}
	if got := counter.addURICount(); got != 100 {
		t.Fatalf("expected 100 aria2.addUri calls, got %d", got)
	}
	if got := counter.count("aria2.saveSession"); got != 100 {
		t.Fatalf("expected 100 saveSession calls, got %d", got)
	}
	if containsString(result.Succeeded, urls[100]) || containsString(result.Duplicates, urls[100]) {
		t.Fatalf("expected 101st URL %q to be absent from result slices", urls[100])
	}
	if _, ok := result.Errors[urls[100]]; ok {
		t.Fatalf("expected 101st URL %q to be absent from errors", urls[100])
	}
}

func TestBatchAddUri_FourUniqueAddableDirectURLsDoNotCreateGroup(t *testing.T) {
	app, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	baseDir := config.Current.DownloadDir
	urls := []string{
		"https://example.com/one.bin",
		"https://example.com/two.bin",
		"https://example.com/three.bin",
		"https://example.com/four.bin",
	}

	result := app.BatchAddUri(urls)

	assertBatchAddStrings(t, "succeeded", result.Succeeded, urls)
	if len(result.Groups) != 0 {
		t.Fatalf("expected no groups, got %#v", result.Groups)
	}
	for _, options := range counter.optionsSnapshot() {
		if options["dir"] != baseDir {
			t.Fatalf("expected direct dir %q, got %#v", baseDir, options["dir"])
		}
	}
}

func TestBatchAddUri_FiveUniqueAddableDirectURLsCreateBatchGroupFolder(t *testing.T) {
	app, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	baseDir := config.Current.DownloadDir
	urls := []string{
		"https://example.com/one.bin",
		"https://example.com/two.bin",
		"https://example.com/three.bin",
		"https://example.com/four.bin",
		"https://example.com/five.bin",
	}

	result := app.BatchAddUri(urls)

	assertBatchAddStrings(t, "succeeded", result.Succeeded, urls)
	if len(result.Groups) != 1 {
		t.Fatalf("expected one batch group, got %#v", result.Groups)
	}
	group := result.Groups[0]
	if group.Kind != "batch" || group.ItemCount != 5 {
		t.Fatalf("unexpected group metadata: %#v", group)
	}
	if filepath.Dir(group.Dir) != baseDir {
		t.Fatalf("expected group under base dir %q, got %q", baseDir, group.Dir)
	}
	if info, err := os.Stat(group.Dir); err != nil || !info.IsDir() {
		t.Fatalf("expected group dir to exist, info=%#v err=%v", info, err)
	}
	assertGroupNameIsGeneric(t, group)
	for _, options := range counter.optionsSnapshot() {
		if options["dir"] != group.Dir {
			t.Fatalf("expected grouped dir %q, got %#v", group.Dir, options["dir"])
		}
	}
}

func TestBatchAddUri_DuplicatesDoNotCountTowardBatchGroupThreshold(t *testing.T) {
	activeURL := "https://example.com/active.bin"
	historyURL := "https://example.com/history.bin"
	duplicateURL := "https://example.com/duplicate.bin"
	newOne := "https://example.com/new-one.bin"
	newTwo := "https://example.com/new-two.bin"
	newThree := "https://example.com/new-three.bin"
	app, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{active: []rpc.Task{taskWithSourceURL("gid-active", activeURL)}})
	history.Add(history.HistoryEntry{GID: "gid-history", Source: historyURL})
	baseDir := config.Current.DownloadDir

	result := app.BatchAddUri([]string{activeURL, historyURL, duplicateURL, duplicateURL, newOne, newTwo, newThree})

	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{duplicateURL, newOne, newTwo, newThree})
	assertBatchAddStrings(t, "duplicates", result.Duplicates, []string{activeURL, historyURL, duplicateURL})
	if len(result.Groups) != 0 {
		t.Fatalf("expected no group when unique addable non-duplicates below threshold, got %#v", result.Groups)
	}
	for _, options := range counter.optionsSnapshot() {
		if options["dir"] != baseDir {
			t.Fatalf("expected direct dir %q, got %#v", baseDir, options["dir"])
		}
	}
}

func TestBatchAddUri_DuplicateOnlyBatchDoesNotCreateFolder(t *testing.T) {
	historyURL := "https://example.com/history-only.bin"
	app, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	baseDir := config.Current.DownloadDir
	history.Add(history.HistoryEntry{GID: "gid-history", Source: historyURL})

	result := app.BatchAddUri([]string{historyURL, " " + historyURL + " "})

	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{})
	assertBatchAddStrings(t, "duplicates", result.Duplicates, []string{historyURL, historyURL})
	if len(result.Groups) != 0 {
		t.Fatalf("expected no groups, got %#v", result.Groups)
	}
	if got := counter.addURICount(); got != 0 {
		t.Fatalf("expected no addUri calls, got %d", got)
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", baseDir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no group folders, got %#v", entries)
	}
}

func TestBatchAddUri_AllGroupedAddsFailCleansEmptyGroupFolderAndStore(t *testing.T) {
	app, counter := setupAppTaskBatchAddTest(t, batchAddRPCSnapshots{})
	counter.failAll = true
	urls := []string{
		"https://example.com/fail-one.bin",
		"https://example.com/fail-two.bin",
		"https://example.com/fail-three.bin",
		"https://example.com/fail-four.bin",
		"https://example.com/fail-five.bin",
	}
	baseDir := config.Current.DownloadDir

	result := app.BatchAddUri(urls)

	if len(result.Succeeded) != 0 || len(result.Groups) != 0 {
		t.Fatalf("expected no successes/groups, got %#v", result)
	}
	if len(result.Errors) != len(urls) {
		t.Fatalf("expected one error per URL, got %#v", result.Errors)
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", baseDir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected failed group folder cleanup, got %#v", entries)
	}
	for i := 1; i <= len(urls); i++ {
		if got := monitor.GetStoredTaskGroup(fmt.Sprintf("gid-%d", i)); got != nil {
			t.Fatalf("expected no persisted group for failed gid-%d, got %#v", i, got)
		}
	}
}

func TestDownloadGroupPathSafetyCollisionAndInvalidBase(t *testing.T) {
	originalConfig := config.Current
	t.Cleanup(func() { config.Current = originalConfig })
	baseDir := t.TempDir()
	config.Current = &config.AppConfig{DownloadDir: baseDir}

	name, err := safeDownloadGroupFolderName("collection", "2026-05-07 15:04:05", "dg-a/b\\c?token")
	if err != nil {
		t.Fatalf("safeDownloadGroupFolderName() error = %v", err)
	}
	if strings.ContainsAny(name, `<>:"/\|?*`) {
		t.Fatalf("unsafe folder name after sanitization: %q", name)
	}
	if _, err := resolveDownloadGroupDir(baseDir, "..\\escape"); err != nil {
		t.Fatalf("sanitized traversal-like name should remain contained, got %v", err)
	}
	if _, err := resolveDownloadGroupDir("", "Batch 2026 dg-a1b2c3"); err == nil {
		t.Fatal("expected invalid empty base dir error")
	}

	group := rpc.DownloadGroup{FolderName: "Batch 2026-05-07 15-04-05 dg-collision"}
	firstDir := filepath.Join(baseDir, group.FolderName)
	if err := os.MkdirAll(firstDir, 0o755); err != nil {
		t.Fatalf("MkdirAll collision dir error = %v", err)
	}
	if err := ensureDownloadGroupDir(baseDir, &group); err != nil {
		t.Fatalf("ensureDownloadGroupDir() error = %v", err)
	}
	if group.FolderName != "Batch 2026-05-07 15-04-05 dg-collision-02" {
		t.Fatalf("expected deterministic -02 collision suffix, got %q", group.FolderName)
	}
	if filepath.Dir(group.Dir) != baseDir {
		t.Fatalf("expected collision dir under base %q, got %q", baseDir, group.Dir)
	}
}

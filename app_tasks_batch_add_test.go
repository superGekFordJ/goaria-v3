package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/history"
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
			counter.recordAddURI(batchAddURLParam(req.Params))
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
		history.SetSaveEnabled(originalSaveEnabled)
		config.Current = originalConfig
	})

	return NewApp(), counter
}

func batchAddURLParam(params []json.RawMessage) string {
	for _, param := range params {
		var urls []string
		if err := json.Unmarshal(param, &urls); err == nil && len(urls) > 0 {
			return urls[0]
		}
	}
	return ""
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

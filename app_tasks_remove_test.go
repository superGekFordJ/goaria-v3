package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

type appTaskRPCRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

type appTaskMulticall struct {
	MethodName string            `json:"methodName"`
	Params     []json.RawMessage `json:"params"`
}

type appTaskRPCCounter struct {
	mu           sync.Mutex
	methods      map[string]int
	nestedMethod map[string]int
}

func newAppTaskRPCCounter() *appTaskRPCCounter {
	return &appTaskRPCCounter{
		methods:      make(map[string]int),
		nestedMethod: make(map[string]int),
	}
}

func (c *appTaskRPCCounter) record(method string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.methods[method]++
}

func (c *appTaskRPCCounter) recordNested(method string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nestedMethod[method]++
}

func (c *appTaskRPCCounter) count(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.methods[method]
}

func (c *appTaskRPCCounter) nestedCount(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nestedMethod[method]
}

func appTaskSuccessResponse(result any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      "goaria",
		"result":  result,
	}
}

func setupAppTaskRemoveTest(t *testing.T, handler func(req appTaskRPCRequest, counter *appTaskRPCCounter) map[string]any) *appTaskRPCCounter {
	t.Helper()

	originalCache := monitor.Cache
	originalTracker := monitor.State.GetTracker()
	originalMonitor := monitor.State.GetMonitor()
	originalSaveEnabled := history.SaveEnabled

	monitor.Cache = &monitor.TaskCache{}
	monitor.State.SetTracker(nil)
	monitor.State.SetMonitor(nil)
	history.DisableSaveForTest()
	history.Clear()

	counter := newAppTaskRPCCounter()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var req appTaskRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		counter.record(req.Method)
		if req.Method == "system.multicall" && len(req.Params) == 1 {
			var calls []appTaskMulticall
			if err := json.Unmarshal(req.Params[0], &calls); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			for _, call := range calls {
				counter.recordNested(call.MethodName)
			}
		}

		response := handler(req, counter)
		if response == nil {
			response = appTaskSuccessResponse("OK")
		}
		_ = json.NewEncoder(w).Encode(response)
	}))

	parts := strings.Split(server.URL, ":")
	rpc.Init(parts[len(parts)-1], "secret")

	t.Cleanup(func() {
		server.Close()
		history.Clear()
		history.SetSaveEnabled(originalSaveEnabled)
		monitor.Cache = originalCache
		monitor.State.SetTracker(originalTracker)
		monitor.State.SetMonitor(originalMonitor)
	})

	return counter
}

func TestBatchRemove_UsesCachedSnapshotsWithoutLiveListRPC(t *testing.T) {
	counter := setupAppTaskRemoveTest(t, func(req appTaskRPCRequest, counter *appTaskRPCCounter) map[string]any {
		switch req.Method {
		case "aria2.remove", "aria2.removeDownloadResult", "aria2.saveSession":
			return appTaskSuccessResponse("OK")
		case "aria2.tellActive", "aria2.tellWaiting", "aria2.tellStopped", "aria2.tellStatus", "system.multicall":
			return appTaskSuccessResponse([]any{})
		default:
			return appTaskSuccessResponse("OK")
		}
	})

	baseDir := t.TempDir()
	gids := []string{"gid-active", "gid-waiting", "gid-stopped"}
	monitor.Cache.UpdateFromAria2(
		[]rpc.Task{{GID: gids[0], Status: "active", Dir: baseDir, Files: []rpc.File{{Path: filepath.Join(baseDir, "active.bin")}}}},
		[]rpc.Task{{GID: gids[1], Status: "waiting", Dir: baseDir, Files: []rpc.File{{Path: filepath.Join(baseDir, "waiting.bin")}}}},
		[]rpc.Task{{GID: gids[2], Status: "complete", Dir: baseDir, Files: []rpc.File{{Path: filepath.Join(baseDir, "stopped.bin")}}}},
	)

	app := NewApp()
	app.BatchRemove(gids, false)

	if got := counter.count("aria2.tellActive"); got != 0 {
		t.Fatalf("expected no tellActive calls, got %d", got)
	}
	if got := counter.count("aria2.tellWaiting"); got != 0 {
		t.Fatalf("expected no tellWaiting calls, got %d", got)
	}
	if got := counter.count("aria2.tellStopped"); got != 0 {
		t.Fatalf("expected no tellStopped calls, got %d", got)
	}
	if got := counter.count("aria2.tellStatus"); got != 0 {
		t.Fatalf("expected no tellStatus calls when cache is warm, got %d", got)
	}
	if got := counter.count("system.multicall"); got != 0 {
		t.Fatalf("expected no multicall fallback when cache is warm, got %d", got)
	}
	if got := counter.count("aria2.remove"); got != len(gids) {
		t.Fatalf("expected %d remove calls, got %d", len(gids), got)
	}
}

func TestBatchRemove_FallsBackWithSingleTellStatusMultiForUnresolvedTargets(t *testing.T) {
	gids := []string{"gid-1", "gid-2", "gid-3"}
	counter := setupAppTaskRemoveTest(t, func(req appTaskRPCRequest, counter *appTaskRPCCounter) map[string]any {
		switch req.Method {
		case "system.multicall":
			if counter.count("system.multicall") > 1 {
				t.Fatalf("expected a single multicall fallback, got %d", counter.count("system.multicall"))
			}

			var calls []appTaskMulticall
			if err := json.Unmarshal(req.Params[0], &calls); err != nil {
				t.Fatalf("failed to decode multicall payload: %v", err)
			}
			if len(calls) != len(gids) {
				t.Fatalf("expected %d multicall entries, got %d", len(gids), len(calls))
			}

			result := make([]any, 0, len(calls))
			for _, call := range calls {
				if call.MethodName != "aria2.tellStatus" {
					t.Fatalf("unexpected multicall method %q", call.MethodName)
				}

				var token string
				if err := json.Unmarshal(call.Params[0], &token); err != nil {
					t.Fatalf("failed to decode token: %v", err)
				}
				if token != "token:secret" {
					t.Fatalf("unexpected token %q", token)
				}

				var gid string
				if err := json.Unmarshal(call.Params[1], &gid); err != nil {
					t.Fatalf("failed to decode gid: %v", err)
				}

				result = append(result, []any{map[string]any{
					"gid":             gid,
					"status":          "complete",
					"totalLength":     "100",
					"completedLength": "100",
					"downloadSpeed":   "0",
					"errorCode":       "",
					"errorMessage":    "",
					"dir":             `D:/Downloads`,
					"files": []map[string]any{{
						"path": `D:/Downloads/` + gid + `.bin`,
						"uris": []map[string]any{{"uri": `https://example.com/` + gid}},
					}},
				}})
			}

			return appTaskSuccessResponse(result)
		case "aria2.tellActive", "aria2.tellWaiting", "aria2.tellStopped":
			return appTaskSuccessResponse([]any{})
		case "aria2.tellStatus":
			var gid string
			if len(req.Params) > 1 {
				_ = json.Unmarshal(req.Params[1], &gid)
			}
			return appTaskSuccessResponse(map[string]any{
				"gid":             gid,
				"status":          "complete",
				"totalLength":     "100",
				"completedLength": "100",
				"downloadSpeed":   "0",
				"errorCode":       "",
				"errorMessage":    "",
				"dir":             `D:/Downloads`,
				"files": []map[string]any{{
					"path": `D:/Downloads/` + gid + `.bin`,
					"uris": []map[string]any{{"uri": `https://example.com/` + gid}},
				}},
			})
		case "aria2.remove", "aria2.removeDownloadResult", "aria2.saveSession":
			return appTaskSuccessResponse("OK")
		default:
			return appTaskSuccessResponse("OK")
		}
	})

	monitor.Cache.UpdateFromAria2(
		[]rpc.Task{{GID: gids[0], Status: "active"}},
		[]rpc.Task{{GID: gids[1], Status: "waiting"}},
		[]rpc.Task{{GID: gids[2], Status: "complete"}},
	)

	app := NewApp()
	app.BatchRemove(gids, false)

	if got := counter.count("aria2.tellActive"); got != 0 {
		t.Fatalf("expected no tellActive calls, got %d", got)
	}
	if got := counter.count("aria2.tellWaiting"); got != 0 {
		t.Fatalf("expected no tellWaiting calls, got %d", got)
	}
	if got := counter.count("aria2.tellStopped"); got != 0 {
		t.Fatalf("expected no tellStopped calls, got %d", got)
	}
	if got := counter.count("aria2.tellStatus"); got != 0 {
		t.Fatalf("expected no per-gid tellStatus calls, got %d", got)
	}
	if got := counter.count("system.multicall"); got != 1 {
		t.Fatalf("expected exactly one multicall fallback, got %d", got)
	}
	if got := counter.nestedCount("aria2.tellStatus"); got != len(gids) {
		t.Fatalf("expected %d nested tellStatus calls, got %d", len(gids), got)
	}
}

func TestBatchRemove_RemovesUniqueHistoryEntries(t *testing.T) {
	counter := setupAppTaskRemoveTest(t, func(req appTaskRPCRequest, counter *appTaskRPCCounter) map[string]any {
		switch req.Method {
		case "aria2.remove", "aria2.removeDownloadResult", "aria2.saveSession":
			return appTaskSuccessResponse("OK")
		case "aria2.tellActive", "aria2.tellWaiting", "aria2.tellStopped", "aria2.tellStatus", "system.multicall":
			return appTaskSuccessResponse([]any{})
		default:
			return appTaskSuccessResponse("OK")
		}
	})

	baseDir := t.TempDir()
	history.Add(history.HistoryEntry{GID: "gid-1", Dir: baseDir, Path: filepath.Join(baseDir, "one.bin"), Source: "https://example.com/one"})
	history.Add(history.HistoryEntry{GID: "gid-2", Dir: baseDir, Path: filepath.Join(baseDir, "two.bin"), Source: "https://example.com/two"})
	history.Add(history.HistoryEntry{GID: "gid-keep", Dir: baseDir, Path: filepath.Join(baseDir, "keep.bin"), Source: "https://example.com/keep"})

	app := NewApp()
	app.BatchRemove([]string{"gid-1", "gid-2", "gid-1", "", "gid-missing"}, false)

	if _, ok := history.Get("gid-1"); ok {
		t.Fatalf("expected gid-1 history entry to be removed")
	}
	if _, ok := history.Get("gid-2"); ok {
		t.Fatalf("expected gid-2 history entry to be removed")
	}
	if _, ok := history.Get("gid-keep"); !ok {
		t.Fatalf("expected unrelated history entry to remain")
	}
	if got := counter.count("aria2.remove"); got != 3 {
		t.Fatalf("expected one remove call per unique requested gid including missing, got %d", got)
	}
	if got := counter.count("aria2.tellActive"); got != 0 {
		t.Fatalf("expected no tellActive calls, got %d", got)
	}
	if got := counter.count("aria2.tellWaiting"); got != 0 {
		t.Fatalf("expected no tellWaiting calls, got %d", got)
	}
	if got := counter.count("aria2.tellStopped"); got != 0 {
		t.Fatalf("expected no tellStopped calls, got %d", got)
	}
}

func TestRemoveTask_PrefersHistoryBeforeRPCFallback(t *testing.T) {
	gid := "gid-history"
	counter := setupAppTaskRemoveTest(t, func(req appTaskRPCRequest, counter *appTaskRPCCounter) map[string]any {
		switch req.Method {
		case "aria2.tellActive", "aria2.tellWaiting", "aria2.tellStopped":
			return appTaskSuccessResponse([]any{})
		case "aria2.tellStatus":
			return appTaskSuccessResponse(map[string]any{
				"gid":             gid,
				"status":          "complete",
				"totalLength":     "100",
				"completedLength": "100",
				"downloadSpeed":   "0",
				"errorCode":       "",
				"errorMessage":    "",
				"dir":             `D:/Downloads`,
				"files": []map[string]any{{
					"path": `D:/Downloads/` + gid + `.bin`,
				}},
			})
		case "aria2.remove", "aria2.removeDownloadResult", "aria2.saveSession":
			return appTaskSuccessResponse("OK")
		default:
			return appTaskSuccessResponse("OK")
		}
	})

	baseDir := t.TempDir()
	history.Add(history.HistoryEntry{
		GID:             gid,
		Dir:             baseDir,
		Path:            filepath.Join(baseDir, "history.bin"),
		Source:          "https://example.com/history.bin",
		TotalLength:     "100",
		CompletedLength: "100",
	})

	app := NewApp()
	app.RemoveTask(gid, false)

	if got := counter.count("aria2.tellActive"); got != 0 {
		t.Fatalf("expected no tellActive calls, got %d", got)
	}
	if got := counter.count("aria2.tellWaiting"); got != 0 {
		t.Fatalf("expected no tellWaiting calls, got %d", got)
	}
	if got := counter.count("aria2.tellStopped"); got != 0 {
		t.Fatalf("expected no tellStopped calls, got %d", got)
	}
	if got := counter.count("aria2.tellStatus"); got != 0 {
		t.Fatalf("expected no tellStatus fallback when history has the path, got %d", got)
	}
	if got := counter.count("system.multicall"); got != 0 {
		t.Fatalf("expected no multicall fallback when history has the path, got %d", got)
	}
	if _, ok := history.Get(gid); ok {
		t.Fatalf("expected history entry %q to be removed", gid)
	}
}

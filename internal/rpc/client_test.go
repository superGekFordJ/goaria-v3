package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func countMethodCalls(methods []string, target string) int {
	count := 0
	for _, method := range methods {
		if method == target {
			count++
		}
	}
	return count
}

func TestGetGlobalStat(t *testing.T) {
	// Mock Aria2 server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result": map[string]string{
				"downloadSpeed": "1024",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	Init(port, "secret")

	speed, err := GetGlobalStat()
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if speed != "1024" {
		t.Errorf("Expected speed 1024, got %s", speed)
	}
}

func BenchmarkGetGlobalStat(b *testing.B) {
	// Mock Aria2 server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request if needed, but for benchmark we just want speed
		// We can return a static response mimicking aria2.getGlobalStat
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result": map[string]string{
				"downloadSpeed": "1024",
				"uploadSpeed":   "0",
				"numActive":     "1",
				"numWaiting":    "0",
				"numStopped":    "0",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Parse port from server URL
	// server.URL is like "http://127.0.0.1:12345"
	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	// Init rpc client to point to mock server
	Init(port, "secret")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := GetGlobalStat()
			if err != nil {
				b.Error(err)
			}
		}
	})
}

func TestTellActive(t *testing.T) {
	// Mock Aria2 server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.Method == "aria2.tellActive" {
			response := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      "goaria",
				"result": []interface{}{
					map[string]interface{}{
						"gid":             "12345",
						"status":          "active",
						"totalLength":     "1000",
						"completedLength": "500",
						"downloadSpeed":   "10",
					},
				},
			}
			json.NewEncoder(w).Encode(response)
		} else {
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	Init(port, "secret")

	tasks, err := TellActive()
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(tasks))
	}
	if tasks[0].GID != "12345" {
		t.Errorf("Expected task GID 12345, got %s", tasks[0].GID)
	}
}

func TestTellActiveError(t *testing.T) {
	// Mock Aria2 server returning error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"error": map[string]interface{}{
				"code":    1,
				"message": "some error",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	Init(port, "secret")

	_, err := TellActive()
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
	// Error message format depends on implementation, but typically "rpc error 1: some error"
	expected := "rpc error 1: some error"
	if !strings.Contains(err.Error(), expected) {
		t.Errorf("Expected error to contain %q, got %q", expected, err.Error())
	}
}

func TestTellStatusMulti(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.Method != "system.multicall" {
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}

		if len(req.Params) != 1 {
			http.Error(w, "unexpected top-level params", http.StatusBadRequest)
			return
		}

		var calls []struct {
			MethodName string            `json:"methodName"`
			Params     []json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(req.Params[0], &calls); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if len(calls) != 2 {
			http.Error(w, "unexpected call count", http.StatusBadRequest)
			return
		}

		for i, call := range calls {
			if call.MethodName != "aria2.tellStatus" {
				http.Error(w, "unexpected nested method", http.StatusBadRequest)
				return
			}
			if len(call.Params) != 3 {
				http.Error(w, "unexpected nested param count", http.StatusBadRequest)
				return
			}

			var token string
			if err := json.Unmarshal(call.Params[0], &token); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if token != "token:secret" {
				http.Error(w, "unexpected nested token", http.StatusBadRequest)
				return
			}

			var gid string
			if err := json.Unmarshal(call.Params[1], &gid); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if gid != []string{"gid-1", "gid-2"}[i] {
				http.Error(w, "unexpected gid", http.StatusBadRequest)
				return
			}
		}

		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result": []interface{}{
				[]interface{}{map[string]interface{}{
					"gid":             "gid-1",
					"status":          "active",
					"totalLength":     "100",
					"completedLength": "10",
					"downloadSpeed":   "1",
					"errorCode":       "",
					"errorMessage":    "",
					"files": []interface{}{map[string]interface{}{
						"path": "D:/Downloads/a.zip",
						"uris": []interface{}{map[string]interface{}{"uri": "https://example.com/a.zip"}},
					}},
					"dir": "D:/Downloads",
				}},
				[]interface{}{map[string]interface{}{
					"gid":             "gid-2",
					"status":          "waiting",
					"totalLength":     "200",
					"completedLength": "20",
					"downloadSpeed":   "2",
					"errorCode":       "",
					"errorMessage":    "",
					"files": []interface{}{map[string]interface{}{
						"path": "D:/Downloads/b.zip",
						"uris": []interface{}{map[string]interface{}{"uri": "https://example.com/b.zip"}},
					}},
					"dir": "D:/Downloads",
				}},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	Init(port, "secret")

	tasks, err := TellStatusMulti([]string{"gid-1", "gid-2"})
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].GID != "gid-1" || tasks[1].GID != "gid-2" {
		t.Fatalf("Unexpected gids returned: %v %v", tasks[0].GID, tasks[1].GID)
	}
	if len(tasks[0].Files) != 1 || tasks[0].Files[0].Path != "D:/Downloads/a.zip" {
		t.Fatalf("Unexpected first task files: %+v", tasks[0].Files)
	}
}

func TestTellStatusMultiError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"error": map[string]interface{}{
				"code":    1,
				"message": "multicall failed",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	Init(port, "secret")

	_, err := TellStatusMulti([]string{"gid-1"})
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
	if !strings.Contains(err.Error(), "multicall failed") {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestPauseMulti(t *testing.T) {
	var topLevelMethods []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		topLevelMethods = append(topLevelMethods, req.Method)

		switch req.Method {
		case "system.multicall":
			if len(req.Params) != 1 {
				http.Error(w, "unexpected top-level params", http.StatusBadRequest)
				return
			}

			var calls []struct {
				MethodName string            `json:"methodName"`
				Params     []json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(req.Params[0], &calls); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			if len(calls) != 2 {
				http.Error(w, "unexpected call count", http.StatusBadRequest)
				return
			}

			for i, call := range calls {
				if call.MethodName != "aria2.pause" {
					http.Error(w, "unexpected nested method", http.StatusBadRequest)
					return
				}
				if len(call.Params) != 2 {
					http.Error(w, "unexpected nested param count", http.StatusBadRequest)
					return
				}

				var token string
				if err := json.Unmarshal(call.Params[0], &token); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if token != "token:secret" {
					http.Error(w, "unexpected nested token", http.StatusBadRequest)
					return
				}

				var gid string
				if err := json.Unmarshal(call.Params[1], &gid); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if gid != []string{"gid-1", "gid-2"}[i] {
					http.Error(w, "unexpected gid", http.StatusBadRequest)
					return
				}
			}

			response := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      "goaria",
				"result": []interface{}{
					[]interface{}{"OK"},
					[]interface{}{"OK"},
				},
			}
			json.NewEncoder(w).Encode(response)
		case "aria2.saveSession":
			response := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      "goaria",
				"result":  "OK",
			}
			json.NewEncoder(w).Encode(response)
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	Init(port, "secret")

	if err := PauseMulti([]string{"gid-1", "gid-2"}); err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if count := countMethodCalls(topLevelMethods, "system.multicall"); count != 1 {
		t.Fatalf("Expected exactly 1 multicall request, got %d (%v)", count, topLevelMethods)
	}
	if count := countMethodCalls(topLevelMethods, "aria2.pause"); count != 0 {
		t.Fatalf("Expected no top-level aria2.pause requests, got %d (%v)", count, topLevelMethods)
	}
	if count := countMethodCalls(topLevelMethods, "aria2.saveSession"); count != 1 {
		t.Fatalf("Expected exactly 1 saveSession request, got %d (%v)", count, topLevelMethods)
	}
}

func TestUnpauseMulti(t *testing.T) {
	var topLevelMethods []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		topLevelMethods = append(topLevelMethods, req.Method)

		switch req.Method {
		case "system.multicall":
			if len(req.Params) != 1 {
				http.Error(w, "unexpected top-level params", http.StatusBadRequest)
				return
			}

			var calls []struct {
				MethodName string            `json:"methodName"`
				Params     []json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(req.Params[0], &calls); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			if len(calls) != 2 {
				http.Error(w, "unexpected call count", http.StatusBadRequest)
				return
			}

			for i, call := range calls {
				if call.MethodName != "aria2.unpause" {
					http.Error(w, "unexpected nested method", http.StatusBadRequest)
					return
				}
				if len(call.Params) != 2 {
					http.Error(w, "unexpected nested param count", http.StatusBadRequest)
					return
				}

				var token string
				if err := json.Unmarshal(call.Params[0], &token); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if token != "token:secret" {
					http.Error(w, "unexpected nested token", http.StatusBadRequest)
					return
				}

				var gid string
				if err := json.Unmarshal(call.Params[1], &gid); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if gid != []string{"gid-1", "gid-2"}[i] {
					http.Error(w, "unexpected gid", http.StatusBadRequest)
					return
				}
			}

			response := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      "goaria",
				"result": []interface{}{
					[]interface{}{"OK"},
					[]interface{}{"OK"},
				},
			}
			json.NewEncoder(w).Encode(response)
		case "aria2.saveSession":
			response := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      "goaria",
				"result":  "OK",
			}
			json.NewEncoder(w).Encode(response)
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	Init(port, "secret")

	if err := UnpauseMulti([]string{"gid-1", "gid-2"}); err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if count := countMethodCalls(topLevelMethods, "system.multicall"); count != 1 {
		t.Fatalf("Expected exactly 1 multicall request, got %d (%v)", count, topLevelMethods)
	}
	if count := countMethodCalls(topLevelMethods, "aria2.unpause"); count != 0 {
		t.Fatalf("Expected no top-level aria2.unpause requests, got %d (%v)", count, topLevelMethods)
	}
	if count := countMethodCalls(topLevelMethods, "aria2.saveSession"); count != 1 {
		t.Fatalf("Expected exactly 1 saveSession request, got %d (%v)", count, topLevelMethods)
	}
}

func TestPauseMultiEmpty(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result":  "OK",
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	Init(port, "secret")

	if err := PauseMulti(nil); err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("Expected no requests for empty input, got %d", requests)
	}
}

func TestUnpauseMultiEmpty(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result":  "OK",
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]

	Init(port, "secret")

	if err := UnpauseMulti([]string{}); err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("Expected no requests for empty input, got %d", requests)
	}
}

func TestAria2RealSmallFile(t *testing.T) {
	// This test requires a running Aria2 instance on port 6800 with secret "mysecret"
	t.Skip("Skipping integration test that requires real Aria2 instance")

	Init("6800", "secret")

	gid, err := AddUriWithOptions("https://example.com/file.zip", "D:\\testdown", 0, 0)
	if err != nil {
		t.Fatalf("AddUri error: %v", err)
	}
	t.Logf("Added GID: %s", gid)

	for {
		task, err := TellStatus(gid)
		if err != nil {
			t.Fatalf("TellStatus error: %v", err)
		}
		t.Logf("Status: %s, Completed: %s, Total: %s", task.Status, task.CompletedLength, task.TotalLength)
		if task.Status == "complete" || task.Status == "error" {
			if task.TotalLength == "0" && task.Status == "complete" {
				t.Errorf("BUG VERIFIED: Aria2 returned totalLength=0 for a completed task!")
			}
			break
		}
	}
}

package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type testRPCRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

type testMulticallCall struct {
	MethodName string `json:"methodName"`
	Params     []any  `json:"params"`
}

func countMethodCalls(methods []string, target string) int {
	count := 0
	for _, method := range methods {
		if method == target {
			count++
		}
	}
	return count
}

func decodeTestRPCRequest(r *http.Request) (testRPCRequest, error) {
	var req testRPCRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	return req, err
}

func decodeTestMulticallCalls(params []json.RawMessage) ([]testMulticallCall, error) {
	if len(params) != 1 {
		return nil, fmt.Errorf("unexpected top-level params: %d", len(params))
	}

	var calls []testMulticallCall
	if err := json.Unmarshal(params[0], &calls); err != nil {
		return nil, err
	}

	return calls, nil
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
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
	gids := []string{"gid-1", "gid-2"}
	keys := []string{"gid", "status", "totalLength", "completedLength", "downloadSpeed", "errorCode", "errorMessage", "files", "dir"}
	keysAny := stringsToAny(keys)

	tests := []struct {
		name           string
		secret         string
		expectedParams [][]any
	}{
		{
			name:   "WithSecret",
			secret: "secret",
			expectedParams: [][]any{
				{"token:secret", "gid-1", keysAny},
				{"token:secret", "gid-2", keysAny},
			},
		},
		{
			name:   "NoSecret",
			secret: "",
			expectedParams: [][]any{
				{"gid-1", keysAny},
				{"gid-2", keysAny},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []testRPCRequest

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req, err := decodeTestRPCRequest(r)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}

				requests = append(requests, req)

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

			Init(port, tt.secret)

			tasks, err := TellStatusMulti(gids)
			if err != nil {
				t.Fatalf("Expected no error, got %v", err)
			}

			if len(requests) != 1 {
				t.Fatalf("Expected 1 request, got %d", len(requests))
			}
			if requests[0].Method != "system.multicall" {
				t.Fatalf("Expected system.multicall, got %s", requests[0].Method)
			}

			calls, err := decodeTestMulticallCalls(requests[0].Params)
			if err != nil {
				t.Fatalf("Expected multicall params to decode, got %v", err)
			}
			if len(calls) != len(gids) {
				t.Fatalf("Expected %d nested calls, got %d", len(gids), len(calls))
			}

			for i, call := range calls {
				if call.MethodName != "aria2.tellStatus" {
					t.Fatalf("Expected nested method aria2.tellStatus, got %s", call.MethodName)
				}
				if !reflect.DeepEqual(call.Params, tt.expectedParams[i]) {
					t.Fatalf("Unexpected nested params for %s call %d: got %#v want %#v", tt.name, i, call.Params, tt.expectedParams[i])
				}
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
		})
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

func TestTellStatusMultiEmpty(t *testing.T) {
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

	tasks, err := TellStatusMulti(nil)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if tasks != nil {
		t.Fatalf("Expected nil tasks, got %+v", tasks)
	}
	if requests != 0 {
		t.Fatalf("Expected no requests for empty input, got %d", requests)
	}
}

func TestPauseMulti(t *testing.T) {
	gids := []string{"gid-1", "gid-2"}
	tests := []struct {
		name           string
		secret         string
		expectedParams [][]any
	}{
		{
			name:   "WithSecret",
			secret: "secret",
			expectedParams: [][]any{
				{"token:secret", "gid-1"},
				{"token:secret", "gid-2"},
			},
		},
		{
			name:   "NoSecret",
			secret: "",
			expectedParams: [][]any{
				{"gid-1"},
				{"gid-2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []testRPCRequest

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req, err := decodeTestRPCRequest(r)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}

				requests = append(requests, req)

				switch req.Method {
				case "system.multicall":
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

			Init(port, tt.secret)

			if err := PauseMulti(gids); err != nil {
				t.Fatalf("Expected no error, got %v", err)
			}

			topLevelMethods := make([]string, 0, len(requests))
			for _, req := range requests {
				topLevelMethods = append(topLevelMethods, req.Method)
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

			var multicallRequest *testRPCRequest
			for i := range requests {
				if requests[i].Method == "system.multicall" {
					multicallRequest = &requests[i]
					break
				}
			}
			if multicallRequest == nil {
				t.Fatal("Expected system.multicall request to be recorded")
			}

			calls, err := decodeTestMulticallCalls(multicallRequest.Params)
			if err != nil {
				t.Fatalf("Expected multicall params to decode, got %v", err)
			}
			if len(calls) != len(gids) {
				t.Fatalf("Expected %d nested calls, got %d", len(gids), len(calls))
			}

			for i, call := range calls {
				if call.MethodName != "aria2.pause" {
					t.Fatalf("Expected nested method aria2.pause, got %s", call.MethodName)
				}
				if !reflect.DeepEqual(call.Params, tt.expectedParams[i]) {
					t.Fatalf("Unexpected nested params for %s call %d: got %#v want %#v", tt.name, i, call.Params, tt.expectedParams[i])
				}
			}
		})
	}
}

func TestUnpauseMulti(t *testing.T) {
	gids := []string{"gid-1", "gid-2"}
	tests := []struct {
		name           string
		secret         string
		expectedParams [][]any
	}{
		{
			name:   "WithSecret",
			secret: "secret",
			expectedParams: [][]any{
				{"token:secret", "gid-1"},
				{"token:secret", "gid-2"},
			},
		},
		{
			name:   "NoSecret",
			secret: "",
			expectedParams: [][]any{
				{"gid-1"},
				{"gid-2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []testRPCRequest

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				req, err := decodeTestRPCRequest(r)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}

				requests = append(requests, req)

				switch req.Method {
				case "system.multicall":
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

			Init(port, tt.secret)

			if err := UnpauseMulti(gids); err != nil {
				t.Fatalf("Expected no error, got %v", err)
			}

			topLevelMethods := make([]string, 0, len(requests))
			for _, req := range requests {
				topLevelMethods = append(topLevelMethods, req.Method)
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

			var multicallRequest *testRPCRequest
			for i := range requests {
				if requests[i].Method == "system.multicall" {
					multicallRequest = &requests[i]
					break
				}
			}
			if multicallRequest == nil {
				t.Fatal("Expected system.multicall request to be recorded")
			}

			calls, err := decodeTestMulticallCalls(multicallRequest.Params)
			if err != nil {
				t.Fatalf("Expected multicall params to decode, got %v", err)
			}
			if len(calls) != len(gids) {
				t.Fatalf("Expected %d nested calls, got %d", len(gids), len(calls))
			}

			for i, call := range calls {
				if call.MethodName != "aria2.unpause" {
					t.Fatalf("Expected nested method aria2.unpause, got %s", call.MethodName)
				}
				if !reflect.DeepEqual(call.Params, tt.expectedParams[i]) {
					t.Fatalf("Unexpected nested params for %s call %d: got %#v want %#v", tt.name, i, call.Params, tt.expectedParams[i])
				}
			}
		})
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

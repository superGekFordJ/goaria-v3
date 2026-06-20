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

func initTestRPCServer(t *testing.T, serverURL string, secret string) {
	t.Helper()

	parts := strings.Split(serverURL, ":")
	port := parts[len(parts)-1]
	Init(port, secret)
}

func findTestRPCRequest(t *testing.T, requests []testRPCRequest, method string) testRPCRequest {
	t.Helper()

	for _, req := range requests {
		if req.Method == method {
			return req
		}
	}
	t.Fatalf("expected request for method %s in %#v", method, requests)
	return testRPCRequest{}
}

func decodeAddURIParams(t *testing.T, req testRPCRequest, hasSecret bool) ([]string, map[string]any) {
	t.Helper()

	params := req.Params
	if hasSecret {
		if len(params) != 3 {
			t.Fatalf("expected 3 params with secret, got %d", len(params))
		}
		var token string
		if err := json.Unmarshal(params[0], &token); err != nil {
			t.Fatalf("expected secret token param to decode: %v", err)
		}
		if token != "token:secret" {
			t.Fatalf("expected token:secret, got %q", token)
		}
		params = params[1:]
	} else if len(params) != 2 {
		t.Fatalf("expected 2 params without secret, got %d", len(params))
	}

	var uris []string
	if err := json.Unmarshal(params[0], &uris); err != nil {
		t.Fatalf("expected uri list to decode: %v", err)
	}
	var options map[string]any
	if err := json.Unmarshal(params[1], &options); err != nil {
		t.Fatalf("expected options to decode: %v", err)
	}

	return uris, options
}

func TestAddUriWithAria2OptionsSerializesHeadersOutAndSmartThreadOptions(t *testing.T) {
	directURL := "https://files.example.com/downloads/file.bin"
	var requests []testRPCRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeTestRPCRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests = append(requests, req)

		switch req.Method {
		case "aria2.addUri":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "gid-test"})
		case "aria2.saveSession":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "OK"})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	initTestRPCServer(t, server.URL, "secret")

	gid, err := AddUriWithAria2Options(directURL, AddURIOptions{
		Dir:          "D:/Downloads",
		Out:          "file.bin",
		Headers:      []string{"Authorization: Bearer test-token", "User-Agent: GoAria-Test"},
		Split:        8,
		MinSplitSize: 1_048_576,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if gid != "gid-test" {
		t.Fatalf("expected gid-test, got %q", gid)
	}

	addReq := findTestRPCRequest(t, requests, "aria2.addUri")
	uris, options := decodeAddURIParams(t, addReq, true)
	if !reflect.DeepEqual(uris, []string{directURL}) {
		t.Fatalf("expected uri list %#v, got %#v", []string{directURL}, uris)
	}
	expectedScalars := map[string]string{
		"dir":                       "D:/Downloads",
		"out":                       "file.bin",
		"split":                     "8",
		"max-connection-per-server": "8",
		"min-split-size":            "1048576",
	}
	for key, want := range expectedScalars {
		if got, ok := options[key].(string); !ok || got != want {
			t.Fatalf("expected option %s=%q, got %#v", key, want, options[key])
		}
	}
	if got := options["header"]; !reflect.DeepEqual(got, []any{"Authorization: Bearer test-token", "User-Agent: GoAria-Test"}) {
		t.Fatalf("expected ordered header list, got %#v", got)
	}

	methods := make([]string, 0, len(requests))
	for _, req := range requests {
		methods = append(methods, req.Method)
	}
	if count := countMethodCalls(methods, "aria2.saveSession"); count != 1 {
		t.Fatalf("expected one saveSession call, got %d (%v)", count, methods)
	}
}

func TestAddUriWithAria2OptionsSerializesGroupDirBasenameOutHeadersAndSplit(t *testing.T) {
	directURL := "https://files.example.com/downloads/file.bin"
	groupDir := `D:\Downloads\Batch 2026-05-07 15-04-05 dg-a1b2c3`
	var requests []testRPCRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeTestRPCRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests = append(requests, req)
		switch req.Method {
		case "aria2.addUri":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "gid-group"})
		case "aria2.saveSession":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "OK"})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	initTestRPCServer(t, server.URL, "")

	if _, err := AddUriWithAria2Options(directURL, AddURIOptions{
		Dir:          groupDir,
		Out:          "file.bin",
		Headers:      []string{"Authorization: Bearer test-token"},
		Split:        4,
		MinSplitSize: 1024,
	}); err != nil {
		t.Fatalf("AddUriWithAria2Options() error = %v", err)
	}

	_, options := decodeAddURIParams(t, findTestRPCRequest(t, requests, "aria2.addUri"), false)
	if options["dir"] != groupDir {
		t.Fatalf("expected group dir option %q, got %#v", groupDir, options["dir"])
	}
	if options["out"] != "file.bin" || strings.Contains(fmt.Sprint(options["out"]), "Batch") {
		t.Fatalf("expected basename-only out, got %#v", options["out"])
	}
	if got := options["header"]; !reflect.DeepEqual(got, []any{"Authorization: Bearer test-token"}) {
		t.Fatalf("expected header list preserved, got %#v", got)
	}
	if options["split"] != "4" || options["min-split-size"] != "1024" {
		t.Fatalf("expected smartthread options preserved, got %#v", options)
	}
}

func TestAddUriWithAria2OptionsRejectsUnsafeHeaders(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "unexpected"})
	}))
	defer server.Close()
	initTestRPCServer(t, server.URL, "")

	tests := []struct {
		name   string
		header string
	}{
		{name: "CRLF", header: "X-Test: value\r\nX-Evil: value"},
		{name: "NoColon", header: "X-Test value"},
		{name: "EmptyName", header: " : value"},
		{name: "EmptyAfterTrim", header: "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AddUriWithAria2Options("https://example.com/file.zip", AddURIOptions{
				Dir:     "D:/Downloads",
				Headers: []string{tt.header},
			})
			if err == nil {
				t.Fatal("expected unsafe header error, got nil")
			}
		})
	}
	if requests != 0 {
		t.Fatalf("expected no RPC requests for unsafe headers, got %d", requests)
	}
}

func TestAddUriWithAria2OptionsTreatsEmptyMessageRPCErrorAsFailure(t *testing.T) {
	var requests []testRPCRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeTestRPCRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests = append(requests, req)

		switch req.Method {
		case "aria2.addUri":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      "goaria",
				"error": map[string]any{
					"code":    1,
					"message": "",
				},
			})
		case "aria2.saveSession":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "OK"})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	initTestRPCServer(t, server.URL, "")

	if _, err := AddUriWithAria2Options("https://example.com/file.zip", AddURIOptions{Dir: "D:/Downloads"}); err == nil {
		t.Fatal("expected JSON-RPC error object to fail even with empty message")
	}
	if count := countMethodCalls(methodsFromRequests(requests), "aria2.saveSession"); count != 0 {
		t.Fatalf("expected no saveSession after addUri RPC error, got %d", count)
	}
}

func TestAddUriWithAria2OptionsHookRunsBeforeSaveSession(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeTestRPCRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		methods = append(methods, req.Method)
		switch req.Method {
		case "aria2.addUri":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "gid-hook"})
		case "aria2.saveSession":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "OK"})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	initTestRPCServer(t, server.URL, "")

	hookCalled := false
	_, err := AddUriWithAria2OptionsHook("https://example.com/file.bin", AddURIOptions{Dir: "D:/Downloads"}, func(gid string) error {
		if gid != "gid-hook" {
			t.Fatalf("hook gid = %q, want gid-hook", gid)
		}
		if countMethodCalls(methods, "aria2.saveSession") != 0 {
			t.Fatalf("hook ran after saveSession; methods=%#v", methods)
		}
		hookCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("AddUriWithAria2OptionsHook() error = %v", err)
	}
	if !hookCalled {
		t.Fatal("expected hook to be called")
	}
	if countMethodCalls(methods, "aria2.saveSession") != 1 {
		t.Fatalf("expected one saveSession after hook, methods=%#v", methods)
	}
}

func TestAddUriWithAria2OptionsHookErrorSkipsSaveSession(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeTestRPCRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		methods = append(methods, req.Method)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "gid-hook-error"})
	}))
	defer server.Close()
	initTestRPCServer(t, server.URL, "")

	_, err := AddUriWithAria2OptionsHook("https://example.com/file.bin", AddURIOptions{Dir: "D:/Downloads"}, func(gid string) error {
		return fmt.Errorf("hook failed")
	})
	if err == nil {
		t.Fatal("expected hook error")
	}
	if countMethodCalls(methods, "aria2.saveSession") != 0 {
		t.Fatalf("expected no saveSession after hook error, methods=%#v", methods)
	}
}

func TestAddUriAndAddUriWithOptionsRemainCompatible(t *testing.T) {
	directURL := "https://example.com/file.zip"
	downloadDir := "D:/Downloads"
	var requests []testRPCRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeTestRPCRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests = append(requests, req)

		switch req.Method {
		case "aria2.addUri":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": fmt.Sprintf("gid-%d", countMethodCalls(methodsFromRequests(requests), "aria2.addUri"))})
		case "aria2.saveSession":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "OK"})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	initTestRPCServer(t, server.URL, "")

	if err := AddUri(directURL, downloadDir); err != nil {
		t.Fatalf("AddUri returned error: %v", err)
	}
	gid, err := AddUriWithOptions(directURL, downloadDir, 4, 2_097_152)
	if err != nil {
		t.Fatalf("AddUriWithOptions returned error: %v", err)
	}
	if gid != "gid-2" {
		t.Fatalf("expected gid-2 from second add, got %q", gid)
	}

	var addRequests []testRPCRequest
	for _, req := range requests {
		if req.Method == "aria2.addUri" {
			addRequests = append(addRequests, req)
		}
	}
	if len(addRequests) != 2 {
		t.Fatalf("expected two addUri requests, got %d", len(addRequests))
	}

	uris, options := decodeAddURIParams(t, addRequests[0], false)
	if !reflect.DeepEqual(uris, []string{directURL}) || !reflect.DeepEqual(options, map[string]any{"dir": downloadDir}) {
		t.Fatalf("unexpected AddUri params: uris=%#v options=%#v", uris, options)
	}

	uris, options = decodeAddURIParams(t, addRequests[1], false)
	if !reflect.DeepEqual(uris, []string{directURL}) {
		t.Fatalf("unexpected AddUriWithOptions uris: %#v", uris)
	}
	expectedOptions := map[string]any{
		"dir":                       downloadDir,
		"split":                     "4",
		"max-connection-per-server": "4",
		"min-split-size":            "2097152",
	}
	if !reflect.DeepEqual(options, expectedOptions) {
		t.Fatalf("unexpected AddUriWithOptions params: got %#v want %#v", options, expectedOptions)
	}
	if count := countMethodCalls(methodsFromRequests(requests), "aria2.saveSession"); count != 2 {
		t.Fatalf("expected two saveSession calls, got %d", count)
	}
}

func methodsFromRequests(requests []testRPCRequest) []string {
	methods := make([]string, len(requests))
	for i, req := range requests {
		methods[i] = req.Method
	}
	return methods
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

	stat, err := GetGlobalStat()
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if stat.DownloadSpeed != "1024" {
		t.Errorf("Expected speed 1024, got %s", stat.DownloadSpeed)
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

func TestTellStatusMultiDoesNotRequestDownloadGroupKey(t *testing.T) {
	var requestedKeys []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeTestRPCRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method != "system.multicall" {
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		calls, err := decodeTestMulticallCalls(req.Params)
		if err != nil || len(calls) != 1 {
			http.Error(w, "bad multicall", http.StatusBadRequest)
			return
		}
		if len(calls[0].Params) < 2 {
			http.Error(w, "bad nested params", http.StatusBadRequest)
			return
		}
		requestedKeys, _ = calls[0].Params[len(calls[0].Params)-1].([]any)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result":  []any{[]any{map[string]any{"gid": "gid-1", "status": "active"}}},
		})
	}))
	defer server.Close()
	initTestRPCServer(t, server.URL, "secret")

	if _, err := TellStatusMulti([]string{"gid-1"}); err != nil {
		t.Fatalf("TellStatusMulti() error = %v", err)
	}
	for _, key := range requestedKeys {
		if key == "download_group" {
			t.Fatalf("download_group must not be requested from Aria2; keys=%#v", requestedKeys)
		}
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

func TestPauseMultiResults_SecretLayoutOrderAndSaveSession(t *testing.T) {
	assertPauseResumeMultiResultsLayoutAndSaveSession(t, "aria2.pause", PauseMultiResults)
}

func TestUnpauseMultiResults_SecretLayoutOrderAndSaveSession(t *testing.T) {
	assertPauseResumeMultiResultsLayoutAndSaveSession(t, "aria2.unpause", UnpauseMultiResults)
}

func assertPauseResumeMultiResultsLayoutAndSaveSession(t *testing.T, nestedMethod string, call func([]string) ([]MultiCallItemResult, error)) {
	t.Helper()
	gids := []string{"gid-1", "gid-2", "gid-3"}
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
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": []any{[]any{"OK"}, []any{"OK"}, []any{"OK"}}})
		case "aria2.saveSession":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "OK"})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	initTestRPCServer(t, server.URL, "secret")

	results, err := call(gids)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(results) != len(gids) {
		t.Fatalf("expected %d results, got %#v", len(gids), results)
	}
	for i, result := range results {
		if result.GID != gids[i] || !result.OK || result.Error != "" {
			t.Fatalf("unexpected result %d: %#v", i, result)
		}
	}

	methods := methodsFromRequests(requests)
	if count := countMethodCalls(methods, "system.multicall"); count != 1 {
		t.Fatalf("expected one multicall, got %d (%v)", count, methods)
	}
	if count := countMethodCalls(methods, nestedMethod); count != 0 {
		t.Fatalf("expected no top-level %s, got %d (%v)", nestedMethod, count, methods)
	}
	if count := countMethodCalls(methods, "aria2.saveSession"); count != 1 {
		t.Fatalf("expected one saveSession, got %d (%v)", count, methods)
	}

	multicall := findTestRPCRequest(t, requests, "system.multicall")
	calls, err := decodeTestMulticallCalls(multicall.Params)
	if err != nil {
		t.Fatalf("failed to decode multicall: %v", err)
	}
	if len(calls) != len(gids) {
		t.Fatalf("expected %d nested calls, got %d", len(gids), len(calls))
	}
	for i, nested := range calls {
		if nested.MethodName != nestedMethod {
			t.Fatalf("expected nested method %q, got %q", nestedMethod, nested.MethodName)
		}
		want := []any{"token:secret", gids[i]}
		if !reflect.DeepEqual(nested.Params, want) {
			t.Fatalf("unexpected nested params %d: got %#v want %#v", i, nested.Params, want)
		}
	}
}

func TestPauseMultiResults_PartialNestedErrors(t *testing.T) {
	assertPauseResumeMultiResultsPartialNestedErrors(t, "aria2.pause", PauseMultiResults)
}

func TestUnpauseMultiResults_PartialNestedErrors(t *testing.T) {
	assertPauseResumeMultiResultsPartialNestedErrors(t, "aria2.unpause", UnpauseMultiResults)
}

func assertPauseResumeMultiResultsPartialNestedErrors(t *testing.T, nestedMethod string, call func([]string) ([]MultiCallItemResult, error)) {
	t.Helper()
	gids := []string{"gid-ok", "gid-jsonrpc-fault", "gid-multicall-fault", "gid-ok-2"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeTestRPCRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "system.multicall":
			calls, err := decodeTestMulticallCalls(req.Params)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if len(calls) != len(gids) {
				t.Fatalf("expected %d calls, got %d", len(gids), len(calls))
			}
			for _, nested := range calls {
				if nested.MethodName != nestedMethod {
					t.Fatalf("expected nested method %q, got %q", nestedMethod, nested.MethodName)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      "goaria",
				"result": []any{
					[]any{"OK"},
					[]any{map[string]any{"code": 1, "message": "cannot pause gid-jsonrpc-fault"}},
					map[string]any{"faultCode": 2, "faultString": "cannot pause gid-multicall-fault"},
					[]any{"OK"},
				},
			})
		case "aria2.saveSession":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "OK"})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	initTestRPCServer(t, server.URL, "")

	results, err := call(gids)
	if err != nil {
		t.Fatalf("expected partial nested failures not to fail whole helper, got %v", err)
	}
	want := []MultiCallItemResult{
		{GID: "gid-ok", OK: true},
		{GID: "gid-jsonrpc-fault", OK: false, Error: "cannot pause gid-jsonrpc-fault"},
		{GID: "gid-multicall-fault", OK: false, Error: "cannot pause gid-multicall-fault"},
		{GID: "gid-ok-2", OK: true},
	}
	if !reflect.DeepEqual(results, want) {
		t.Fatalf("unexpected results:\n got %#v\nwant %#v", results, want)
	}
}

func TestPauseUnpauseMultiResults_EmptyInputShortCircuits(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "goaria", "result": "OK"})
	}))
	defer server.Close()
	initTestRPCServer(t, server.URL, "secret")

	pauseResults, err := PauseMultiResults(nil)
	if err != nil {
		t.Fatalf("PauseMultiResults(nil) error = %v", err)
	}
	unpauseResults, err := UnpauseMultiResults([]string{})
	if err != nil {
		t.Fatalf("UnpauseMultiResults(empty) error = %v", err)
	}
	if len(pauseResults) != 0 || len(unpauseResults) != 0 {
		t.Fatalf("expected empty result slices, got pause=%#v unpause=%#v", pauseResults, unpauseResults)
	}
	if requests != 0 {
		t.Fatalf("expected no RPC requests for empty input, got %d", requests)
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

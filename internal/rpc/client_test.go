package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

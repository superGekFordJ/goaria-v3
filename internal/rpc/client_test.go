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

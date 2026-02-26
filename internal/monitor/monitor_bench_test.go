package monitor

import (
	"encoding/json"
	"goaria-v3/internal/rpc"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockAria2Server handles RPC requests with simulated latency
func mockAria2Server(delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Simulate network latency
		time.Sleep(delay)

		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Simplified response structure
		emptyListResponse := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result":  []interface{}{},
		}

		// Handle specific methods involved in tick()
		if strings.Contains(req.Method, "tellActive") ||
			strings.Contains(req.Method, "tellWaiting") ||
			strings.Contains(req.Method, "tellStopped") {
			json.NewEncoder(w).Encode(emptyListResponse)
			return
		}

		// Default success for other calls (e.g., tellStatus if called)
		successResponse := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result":  nil,
		}
		json.NewEncoder(w).Encode(successResponse)
	}
}

func BenchmarkMonitorTick(b *testing.B) {
	// 1. Setup Mock Server
	latency := 10 * time.Millisecond
	// Use 127.0.0.1 to match rpc.Init hardcoded host
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	server := httptest.NewUnstartedServer(mockAria2Server(latency))
	server.Listener.Close()
	server.Listener = l
	server.Start()
	defer server.Close()

	// 2. Configure RPC client
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	rpc.Init(port, "secret")

	// 3. Initialize Monitor with minimal dependencies
	// We only need tracker to be non-nil for Update() call
	m := &Monitor{
		tracker: NewTaskTracker(),
		// Ensure stopped tasks are fetched to maximize workload
		shouldFetchStopped: true,
	}

	// 4. Run Benchmark
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Force fetching stopped tasks every iteration to measure full impact
		m.mu.Lock()
		m.shouldFetchStopped = true
		m.mu.Unlock()

		m.tick()
	}
}

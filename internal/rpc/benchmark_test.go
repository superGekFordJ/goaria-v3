package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func BenchmarkUnmarshalTasks(b *testing.B) {
	// 1. Construct a heavy response (with 1000 files)
	// Use generic paths to avoid platform confusion, though this is just JSON data.
	var files []string
	for i := range 1000 {
		files = append(files, fmt.Sprintf(`{"path": "downloads/file_%d.txt", "uris": [{"uri": "http://example.com/file_%d.txt", "status": "used"}]}`, i, i))
	}
	filesJson := strings.Join(files, ",")
	heavyJson := fmt.Sprintf(`{
		"result": [
			{
				"gid": "2089b05e72f5370a",
				"status": "active",
				"totalLength": "1000000000",
				"completedLength": "500000",
				"downloadSpeed": "10000",
				"errorCode": "0",
				"errorMessage": "",
				"dir": "downloads",
				"files": [%s]
			}
		]
	}`, filesJson)

	// 2. Construct a light response (no files)
	lightJson := `{
		"result": [
			{
				"gid": "2089b05e72f5370a",
				"status": "active",
				"totalLength": "1000000000",
				"completedLength": "500000",
				"downloadSpeed": "10000",
				"errorCode": "0",
				"errorMessage": "",
				"dir": "downloads"
			}
		]
	}`

	b.Run("HeavyPayload", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var result struct {
				Result []Task `json:"result"`
			}
			json.Unmarshal([]byte(heavyJson), &result)
		}
	})

	b.Run("LightPayload", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var result struct {
				Result []Task `json:"result"`
			}
			json.Unmarshal([]byte(lightJson), &result)
		}
	})
}

func BenchmarkBatchPause_Sequential(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result":  "12345",
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]
	Init(port, "secret")

	gids := make([]string, 100)
	for i := range 100 {
		gids[i] = fmt.Sprintf("gid-%d", i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, gid := range gids {
			Pause(gid)
		}
	}
}

func BenchmarkBatchResume_Sequential(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result":  "12345",
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]
	Init(port, "secret")

	gids := make([]string, 100)
	for i := range 100 {
		gids[i] = fmt.Sprintf("gid-%d", i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, gid := range gids {
			Unpause(gid)
		}
	}
}

func BenchmarkGetTaskMetadata_Sequential(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result": map[string]any{
				"gid":             "gid-x",
				"status":          "active",
				"totalLength":     "100",
				"completedLength": "10",
				"downloadSpeed":   "1",
				"errorCode":       "",
				"errorMessage":    "",
				"files": []any{map[string]any{
					"path": "D:/Downloads/a.zip",
					"uris": []any{map[string]any{"uri": "https://example.com/a.zip"}},
				}},
				"dir": "D:/Downloads",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]
	Init(port, "secret")

	gids := make([]string, 100)
	for i := range 100 {
		gids[i] = fmt.Sprintf("gid-%d", i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := make(map[string]Task)
		for _, gid := range gids {
			task, err := TellStatus(gid)
			if err == nil && task != nil {
				result[gid] = *task
			}
		}
	}
}

func BenchmarkBatchPause_Multi(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result":  []any{"12345"},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]
	Init(port, "secret")

	gids := make([]string, 100)
	for i := range 100 {
		gids[i] = fmt.Sprintf("gid-%d", i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PauseMulti(gids)
	}
}

func BenchmarkBatchResume_Multi(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result":  []any{"12345"},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]
	Init(port, "secret")

	gids := make([]string, 100)
	for i := range 100 {
		gids[i] = fmt.Sprintf("gid-%d", i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		UnpauseMulti(gids)
	}
}

func BenchmarkGetTaskMetadata_Multi(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resultArr := make([]any, 100)
		for i := range 100 {
			resultArr[i] = []any{map[string]any{
				"gid":             fmt.Sprintf("gid-%d", i),
				"status":          "active",
				"totalLength":     "100",
				"completedLength": "10",
				"downloadSpeed":   "1",
				"errorCode":       "",
				"errorMessage":    "",
				"files": []any{map[string]any{
					"path": "D:/Downloads/a.zip",
					"uris": []any{map[string]any{"uri": "https://example.com/a.zip"}},
				}},
				"dir": "D:/Downloads",
			}}
		}
		response := map[string]any{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result":  resultArr,
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	port := parts[len(parts)-1]
	Init(port, "secret")

	gids := make([]string, 100)
	for i := range 100 {
		gids[i] = fmt.Sprintf("gid-%d", i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := make(map[string]Task)
		tasks, err := TellStatusMulti(gids)
		if err == nil {
			for _, task := range tasks {
				if task != nil {
					result[task.GID] = *task
				}
			}
		}
	}
}

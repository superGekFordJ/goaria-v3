package rpc

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func BenchmarkUnmarshalTasks(b *testing.B) {
	// 1. Construct a heavy response (with 1000 files)
	// Use generic paths to avoid platform confusion, though this is just JSON data.
	var files []string
	for i := 0; i < 1000; i++ {
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

package monitor

import (
	"goaria-v3/internal/rpc"
)

// createMockTask 创建模拟任务
func createMockTask(gid, status string) rpc.Task {
	return rpc.Task{
		GID:             gid,
		Status:          status,
		TotalLength:     "100000000",
		CompletedLength: "50000000",
		DownloadSpeed:   "1000000",
		Dir:             "D:\\Downloads",
		ErrorCode:       "",
		ErrorMessage:    "",
		Files: []rpc.File{
			{
				Path: "D:\\Downloads\\file-" + gid + ".zip",
				Uris: []rpc.Uri{
					{Uri: "https://example.com/file.zip", Status: "used"},
				},
			},
		},
	}
}

// ==================== Status Desync Regression Tests ====================

// activeSetContains reports whether gid is present in GetActiveTrackedTasks.
func activeSetContains(tracker *TaskTracker, gid string) bool {
	for _, tt := range tracker.GetActiveTrackedTasks() {
		if tt.GID == gid {
			return true
		}
	}
	return false
}

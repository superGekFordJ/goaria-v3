package monitor

import (
	"strconv"
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	surgeEvents "goaria-v3/internal/surge/types"
)

// TestHandleSurgeEvent_ShortTaskSpeedstatsDrainRegression tests that:
//  1. A short task without convergence ticker cycles (PeakSpeed == 0, PeakThreadCount == 0)
//     preserves its allocated ThreadCount = 6 across progress events, even when tail-end
//     worker drain reports Connections = 2.
//  2. Both when State.HasWindow() is false (minimized to tray) and true, LiveConnections
//     is updated to 2 without corrupting ThreadCount.
//  3. On EventComplete fallback, speedstats.AddRecordV2 records the untampered allocated
//     ThreadCount = 6 instead of the drain connection count 2.
func TestHandleSurgeEvent_ShortTaskSpeedstatsDrainRegression(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	defer State.SetWindowExists(prevWindow)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(nil, surge)
	m := &Monitor{
		hub:      hub,
		pusher:   pusher,
		engine:   he,
		tracker:  tracker,
		stopChan: make(chan struct{}),
	}

	gid := "sg_short_drain_task"
	rawID := "short_drain_task"
	totalSize := int64(100 * 1024 * 1024) // 100MB (> MinFileSize 50MB)

	// 1. 模拟任务分配主力并发 ThreadCount = 6
	tracker.SetThreadInfo(gid, 6, false)
	tracker.SetScopeAndEnv(gid, "wan", 20, "shortdrain.example.com", "testenv")
	tracker.EnsureTrackedFromEvent(gid, totalSize, "https://shortdrain.example.com/data.bin", 6, "active")

	if tt := tracker.tasks[gid]; tt != nil {
		tt.FilePath = "D:\\Downloads\\data.bin"
	}
	Cache.sgActive = []rpc.Task{{
		GID:             gid,
		Status:          "active",
		TotalLength:     strconv.FormatInt(totalSize, 10),
		CompletedLength: "0",
	}}
	defer func() { Cache.sgActive = nil }()

	// 验证前置状态：短任务无 Convergence D3 记录
	if tc, _, ok := tracker.GetThreadInfo(gid); !ok || tc != 6 {
		t.Fatalf("precondition: expected ThreadCount = 6, got %d", tc)
	}
	if tt := tracker.tasks[gid]; tt.PeakSpeed != 0 || tt.PeakThreadCount != 0 {
		t.Fatalf("precondition: short task should have PeakSpeed=0 and PeakThreadCount=0, got %d, %d", tt.PeakSpeed, tt.PeakThreadCount)
	}

	// 2. 在 State.HasWindow() == false (托盘无窗) 下，触发全量并发进度
	State.SetWindowExists(false)
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:        surgeEvents.EventProgress,
		DownloadID:  rawID,
		Total:       totalSize,
		Downloaded:  30 * 1024 * 1024,
		Speed:       60 * 1024 * 1024,
		Connections: 6,
	})
	if live := tracker.GetLiveConnections(gid); live != 6 {
		t.Fatalf("expected LiveConnections = 6 under HasWindow(false), got %d", live)
	}
	if tc, _, _ := tracker.GetThreadInfo(gid); tc != 6 {
		t.Fatalf("expected ThreadCount to stay 6 under HasWindow(false), got %d", tc)
	}

	// 3. 在 State.HasWindow() == false 下，触发尾段排空 (Connections = 2)
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:        surgeEvents.EventProgress,
		DownloadID:  rawID,
		Total:       totalSize,
		Downloaded:  95 * 1024 * 1024,
		Speed:       65 * 1024 * 1024,
		Connections: 2,
	})
	if live := tracker.GetLiveConnections(gid); live != 2 {
		t.Fatalf("expected LiveConnections = 2 under HasWindow(false), got %d", live)
	}
	if tc, _, _ := tracker.GetThreadInfo(gid); tc != 6 {
		t.Fatalf("expected ThreadCount to remain 6 after drain under HasWindow(false), got %d", tc)
	}

	// 4. 在 State.HasWindow() == false 下，触发尾段全部 Worker 排空完毕 (Connections = 0)
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:        surgeEvents.EventProgress,
		DownloadID:  rawID,
		Total:       totalSize,
		Downloaded:  99 * 1024 * 1024,
		Speed:       65 * 1024 * 1024,
		Connections: 0,
	})
	if live := tracker.GetLiveConnections(gid); live != 0 {
		t.Fatalf("expected LiveConnections = 0 after complete drain under HasWindow(false), got %d", live)
	}
	if tc, _, _ := tracker.GetThreadInfo(gid); tc != 6 {
		t.Fatalf("expected ThreadCount to remain 6 after drain to 0 under HasWindow(false), got %d", tc)
	}

	// 5. 在 State.HasWindow() == true (窗口开启) 下，测试 6 -> 2 -> 0 实时推送
	State.SetWindowExists(true)
	pusher = NewPusher(hub)
	m.pusher = pusher

	// 5.1 上报 2 线程排空
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:        surgeEvents.EventProgress,
		DownloadID:  rawID,
		Total:       totalSize,
		Downloaded:  99 * 1024 * 1024,
		Speed:       65 * 1024 * 1024,
		Connections: 2,
	})
	if live := tracker.GetLiveConnections(gid); live != 2 {
		t.Fatalf("expected LiveConnections = 2 under HasWindow(true), got %d", live)
	}
	if tc, _, _ := tracker.GetThreadInfo(gid); tc != 6 {
		t.Fatalf("expected ThreadCount to remain 6 after drain under HasWindow(true), got %d", tc)
	}
	pusher.mu.Lock()
	if len(pusher.pending) == 0 {
		pusher.mu.Unlock()
		t.Fatal("expected pending delta in pusher")
	}
	delta := pusher.pending[len(pusher.pending)-1]
	pusher.mu.Unlock()

	payload, ok := delta.Payload.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string payload, got %T", delta.Payload)
	}
	if payload["threads"] != "2" {
		t.Fatalf("expected payload threads = \"2\", got %q", payload["threads"])
	}

	// 5.2 上报 0 线程排空（所有 worker 退出）
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:        surgeEvents.EventProgress,
		DownloadID:  rawID,
		Total:       totalSize,
		Downloaded:  100 * 1024 * 1024,
		Speed:       0,
		Connections: 0,
	})
	if live := tracker.GetLiveConnections(gid); live != 0 {
		t.Fatalf("expected LiveConnections = 0 under HasWindow(true), got %d", live)
	}
	if tc, _, _ := tracker.GetThreadInfo(gid); tc != 6 {
		t.Fatalf("expected ThreadCount to remain 6 after drain to 0 under HasWindow(true), got %d", tc)
	}
	pusher.mu.Lock()
	if len(pusher.pending) == 0 {
		pusher.mu.Unlock()
		t.Fatal("expected pending delta in pusher on worker drain")
	}
	delta = pusher.pending[len(pusher.pending)-1]
	pusher.mu.Unlock()

	payload, ok = delta.Payload.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string payload, got %T", delta.Payload)
	}
	if payload["threads"] != "0" {
		t.Fatalf("expected payload threads = \"0\" on worker drain, got %q", payload["threads"])
	}

	// 6. 触发 EventComplete 完成事件（携带全程平均速度 65MB/s）
	beforeCount := speedstatsRecordCount()
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: rawID,
		Total:      totalSize,
		AvgSpeed:   65 * 1024 * 1024,
	})
	afterCount := speedstatsRecordCount()
	if afterCount != beforeCount+1 {
		t.Fatalf("expected 1 new speedstats record, got %d (before=%d, after=%d)", afterCount-beforeCount, beforeCount, afterCount)
	}

	// 7. 断言 speedstats 中的 ThreadCount 为主力分配并发 6，而非收尾残余 2 或 0
	rec := findRecordByDomain("shortdrain.example.com")
	if rec == nil {
		t.Fatal("expected to find speedstats record for shortdrain.example.com")
	}
	if rec.ThreadCount != 6 {
		t.Fatalf("speedstats ThreadCount = %d, want 6 (allocated workers, not drain 0/2)", rec.ThreadCount)
	}
	if rec.PeakSpeed != 65*1024*1024 {
		t.Fatalf("speedstats PeakSpeed = %d, want %d", rec.PeakSpeed, 65*1024*1024)
	}
}

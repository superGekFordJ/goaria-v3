package events

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type TaskDelta struct {
	Type    string      `json:"type"`
	GID     string      `json:"gid"`
	Payload interface{} `json:"payload,omitempty"`
}

type TaskMove struct {
	GID  string      `json:"gid"`
	From string      `json:"from"` // "active", "waiting", "stopped"
	To   string      `json:"to"`
	Task interface{} `json:"task"` // Full task with metadata
}

type Hub struct {
	app *application.App
	mu  sync.RWMutex

	// Listeners for internal events
	deltaListeners []func(TaskDelta)
}

func NewHub(app *application.App) *Hub {
	return &Hub{
		app:            app,
		deltaListeners: make([]func(TaskDelta), 0),
	}
}

func (h *Hub) SubscribeTaskDelta(fn func(TaskDelta)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deltaListeners = append(h.deltaListeners, fn)
}

func (h *Hub) EmitTaskDelta(delta TaskDelta) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Notify internal listeners
	for _, fn := range h.deltaListeners {
		fn(delta)
	}

	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("task:delta", delta)
	}
}

func (h *Hub) EmitFullSync() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("task:fullsync")
	}
}

func (h *Hub) EmitConnectionStatus(connected bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("aria2:connection", map[string]bool{"connected": connected})
	}
}

// EmitTraySnapshot 推送托盘状态快照（用于前端同步）
func (h *Hub) EmitTraySnapshot(snapshot interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("tray:snapshot", snapshot)
	}
}

// EmitWindowCreated 通知前端窗口已创建
func (h *Hub) EmitWindowCreated() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("window:created")
	}
}

// EmitWindowFocus 手动发送窗口焦点事件
// 用于托盘恢复时触发剪贴板检测（Wails 对新创建窗口可能不发送 focus 事件）
func (h *Hub) EmitWindowFocus() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("common:WindowFocus")
	}
}

// EmitTaskComplete 推送任务完成事件
func (h *Hub) EmitTaskComplete(gid string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("task:complete", map[string]string{"gid": gid})
	}
}

// EmitTaskDeltas 批量推送任务增量
func (h *Hub) EmitTaskDeltas(deltas []TaskDelta) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil && len(deltas) > 0 {
		h.app.Event.Emit("task:deltas", deltas)
	}
}

// EmitTaskMove 推送任务列表转移事件（用于跨列表元数据保留）
func (h *Hub) EmitTaskMove(move TaskMove) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("task:move", move)
	}
}

// EmitUpdateStatus 推送更新状态变化
func (h *Hub) EmitUpdateStatus(status string, payload interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("update:status", map[string]interface{}{
			"status":  status,
			"payload": payload,
		})
	}
}

// EmitUpdateProgress 推送更新下载进度 (0-100)
func (h *Hub) EmitUpdateProgress(percent int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("update:progress", map[string]int{"percent": percent})
	}
}

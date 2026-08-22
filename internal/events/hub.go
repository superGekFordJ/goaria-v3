package events

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type TaskDelta struct {
	Type    string `json:"type"`
	GID     string `json:"gid"`
	Payload any    `json:"payload,omitempty"`
}

type TaskMove struct {
	GID  string `json:"gid"`
	From string `json:"from"` // "active", "waiting", "stopped"
	To   string `json:"to"`
	Task any    `json:"task"` // Full task with metadata
}

type Hub struct {
	app *application.App
	mu  sync.RWMutex

	// Listeners for internal events
	deltaListeners []func(TaskDelta)
	moveListeners  []func(TaskMove)
}

func NewHub(app *application.App) *Hub {
	return &Hub{
		app:            app,
		deltaListeners: make([]func(TaskDelta), 0),
		moveListeners:  make([]func(TaskMove), 0),
	}
}

func (h *Hub) SubscribeTaskDelta(fn func(TaskDelta)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deltaListeners = append(h.deltaListeners, fn)
}

func (h *Hub) SubscribeTaskMove(fn func(TaskMove)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.moveListeners = append(h.moveListeners, fn)
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

// NotifyInternal 仅通知内部监听器，不向前端发射事件
// 用于 WebSocket Sensor 模式：触发 Monitor 的 forceTickChan，但不直推前端
func (h *Hub) NotifyInternal(delta TaskDelta) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, fn := range h.deltaListeners {
		fn(delta)
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
func (h *Hub) EmitTraySnapshot(snapshot any) {
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
	for _, fn := range h.moveListeners {
		fn(move)
	}
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("task:move", move)
	}
}

// EmitUpdateStatus 推送更新状态变化
func (h *Hub) EmitUpdateStatus(status string, payload any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("update:status", map[string]any{
			"status":  status,
			"payload": payload,
		})
	}
}

// EmitUpdateProgress 推送更新下载进度（字节级精度，供前端平滑算法使用）
func (h *Hub) EmitUpdateProgress(downloaded, total, speed int64) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("update:progress", map[string]int64{
			"downloaded": downloaded,
			"total":      total,
			"speed":      speed,
		})
	}
}

// EmitExtensionStatus pushes extension connection status to the frontend.
func (h *Hub) EmitExtensionStatus(status any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("extension:status", status)
	}
}

// EmitExtensionPaired notifies the frontend that pairing completed.
func (h *Hub) EmitExtensionPaired() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("extension:paired")
	}
}

// EmitExtensionUnpaired notifies the frontend that the extension was unpaired.
func (h *Hub) EmitExtensionUnpaired() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("extension:unpaired")
	}
}

// EmitExtensionAuthFailed notifies the desktop frontend that a WS auth mismatch
// occurred. This goes ONLY to the desktop frontend — never back over the WS.
func (h *Hub) EmitExtensionAuthFailed() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.app != nil && h.app.Event != nil {
		h.app.Event.Emit("extension:auth_failed")
	}
}

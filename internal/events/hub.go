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

type Hub struct {
	app *application.App
	mu  sync.RWMutex
}

func NewHub(app *application.App) *Hub {
	return &Hub{app: app}
}

func (h *Hub) EmitTaskDelta(delta TaskDelta) {
	h.mu.RLock()
	defer h.mu.RUnlock()
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

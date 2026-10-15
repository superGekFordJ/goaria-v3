package monitor

import (
	"sync"
	"time"

	"goaria-v3/internal/events"
)

const (
	pushDebounce = 50 * time.Millisecond // 推送防抖
	maxBatchSize = 50                    // 最大批量大小
)

// DeltaType 增量类型
type DeltaType string

const (
	DeltaAdd      DeltaType = "add"
	DeltaComplete DeltaType = "complete"
	DeltaPause    DeltaType = "pause"
	DeltaResume   DeltaType = "resume"
	DeltaError    DeltaType = "error"
	DeltaRemove   DeltaType = "remove"
	DeltaProgress DeltaType = "progress"
)

// TaskDelta 任务增量
type TaskDelta struct {
	Type DeltaType `json:"type"`
	GID  string    `json:"gid"`
}

// Pusher 增量推送器
type Pusher struct {
	hub      *events.Hub
	mu       sync.Mutex
	pending  []events.TaskDelta
	timer    *time.Timer
	dedupMap map[string]int // gid -> index in pending（去重）
}

// NewPusher 创建新的推送器
func NewPusher(hub *events.Hub) *Pusher {
	return &Pusher{
		hub:      hub,
		dedupMap: make(map[string]int),
	}
}

// Queue 入队增量（带去重）
func (p *Pusher) Queue(delta events.TaskDelta) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 去重：相同 GID 的新事件覆盖旧事件（Queue 在 p.mu 下串行，到达序即提交序）
	if idx, exists := p.dedupMap[delta.GID]; exists {
		if shouldReplace(p.pending[idx].Type, delta.Type) {
			p.pending[idx] = delta
		}
	} else {
		p.dedupMap[delta.GID] = len(p.pending)
		p.pending = append(p.pending, delta)
	}

	// 防抖：延迟推送
	if p.timer == nil {
		p.timer = time.AfterFunc(pushDebounce, p.flush)
	}
}

// shouldReplace 判断新事件是否应替换 pending 中的旧事件。
// 入队串行 ⇒ 后到的事件总是更新的事实：lifecycle 事件恒置换 pending；
// progress 只折叠同类，永不覆盖 lifecycle（保留折叠语义）。
func shouldReplace(old, new string) bool {
	return new != string(DeltaProgress) || old == string(DeltaProgress)
}

// flush 批量推送
func (p *Pusher) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.pending) == 0 {
		return
	}

	// 限制批量大小
	batch := p.pending
	if len(batch) > maxBatchSize {
		batch = batch[:maxBatchSize]
		p.pending = p.pending[maxBatchSize:]
		// 重建去重 map
		p.rebuildDedupMap()
		// 继续推送剩余
		p.timer = time.AfterFunc(pushDebounce, p.flush)
	} else {
		p.pending = nil
		p.dedupMap = make(map[string]int)
		p.timer = nil
	}

	// 批量推送
	p.hub.EmitTaskDeltas(batch)
}

// rebuildDedupMap 重建去重 map
func (p *Pusher) rebuildDedupMap() {
	p.dedupMap = make(map[string]int)
	for i, d := range p.pending {
		p.dedupMap[d.GID] = i
	}
}

// FlushNow 立即推送（用于用户操作后）
func (p *Pusher) FlushNow() {
	p.mu.Lock()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.mu.Unlock()
	p.flush()
}
